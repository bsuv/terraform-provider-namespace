package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/private/sessions/sessionsv1betagrpc"
	sessions "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/sessions"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TokenSource issues bearer tokens for authenticating Namespace Cloud gRPC requests.
type TokenSource interface {
	IssueToken(ctx context.Context, minDuration time.Duration, force bool) (string, error)
}

// ErrNotLoggedIn is returned when a token lacks a supported prefix or cannot be parsed as a JWT.
var ErrNotLoggedIn = errors.New("not logged in")

// NumericDate represents a JSON numeric date value (RFC 7519 Section 2: Unix epoch seconds).
type NumericDate struct {
	time.Time
}

// UnmarshalJSON decodes an integer or floating-point JSON Unix timestamp into NumericDate.
func (d *NumericDate) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if string(trimmed) == "null" {
		return nil
	}
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return fmt.Errorf("invalid numeric date: %s", string(b))
	}
	var num json.Number
	if err := json.Unmarshal(trimmed, &num); err != nil {
		return err
	}
	if sec, err := num.Int64(); err == nil {
		*d = NumericDate{Time: time.Unix(sec, 0).UTC()}
		return nil
	}
	f, err := num.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f >= 9223372036854775808.0 || f < -9223372036854775808.0 {
		return fmt.Errorf("invalid numeric date: %s", string(b))
	}
	sec, frac := math.Modf(f)
	if sec >= 9223372036854775808.0 || sec < -9223372036854775808.0 {
		return fmt.Errorf("invalid numeric date: %s", string(b))
	}
	*d = NumericDate{Time: time.Unix(int64(sec), int64(frac*1e9)).UTC()}
	return nil
}

// TokenClaims holds the unverified claims extracted from a Namespace prefixed JWT token.
type TokenClaims struct {
	ExpiresAt      *NumericDate `json:"exp,omitempty"`
	TenantID       string       `json:"tenant_id"`
	ActorID        string       `json:"actor_id"`
	InstanceID     string       `json:"instance_id"`
	OwnerID        string       `json:"owner_id"`
	PrimaryRegion  string       `json:"primary_region"`
	WorkloadRegion string       `json:"workload_region"`
}

// ExtractClaims decodes unverified JWT claims from st_, nsct_, or nscw_ prefixed tokens.
func ExtractClaims(token string) (*TokenClaims, error) {
	switch {
	case strings.HasPrefix(token, "st_"):
		return parseClaims(strings.TrimPrefix(token, "st_"))
	case strings.HasPrefix(token, "nsct_"):
		return parseClaims(strings.TrimPrefix(token, "nsct_"))
	case strings.HasPrefix(token, "nscw_"):
		return parseClaims(strings.TrimPrefix(token, "nscw_"))
	default:
		return nil, ErrNotLoggedIn
	}
}

func parseClaims(raw string) (*TokenClaims, error) {
	for _, r := range raw {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f {
			return nil, ErrNotLoggedIn
		}
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, ErrNotLoggedIn
	}
	if !isBase64URLSegment(parts[2]) {
		return nil, ErrNotLoggedIn
	}
	headerBytes, err := decodeBase64URL(parts[0])
	if err != nil {
		return nil, ErrNotLoggedIn
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil || header == nil {
		return nil, ErrNotLoggedIn
	}
	claimBytes, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, ErrNotLoggedIn
	}
	trimmedClaims := bytes.TrimSpace(claimBytes)
	if len(trimmedClaims) == 0 || trimmedClaims[0] != '{' {
		return nil, ErrNotLoggedIn
	}
	var claims TokenClaims
	if err := json.Unmarshal(trimmedClaims, &claims); err != nil {
		return nil, ErrNotLoggedIn
	}
	return &claims, nil
}

// AuthConfig defines the input parameters for credential resolution.
type AuthConfig struct {
	Token       string
	TokenFile   string
	IAMEndpoint string
	Creds       credentials.TransportCredentials
	DialOptions []grpc.DialOption
}

// StaticTokenSource implements TokenSource for static string tokens.
type StaticTokenSource struct {
	Token string
}

func (s StaticTokenSource) IssueToken(ctx context.Context, minDuration time.Duration, force bool) (string, error) {
	tok := strings.TrimSpace(s.Token)
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}

// SessionTokenSource implements TokenSource for session token exchange against a specific IAM endpoint.
// It maintains provider-local isolation without reading or mutating global process environment variables.
type SessionTokenSource struct {
	mu             sync.Mutex
	sessionToken   string
	iamEndpoint    string
	creds          credentials.TransportCredentials
	dialOpts       []grpc.DialOption
	cachedToken    string
	cachedExpiry   time.Time
	sessionsClient sessionsv1betagrpc.UserSessionsServiceClient
	conn           *grpc.ClientConn
}

// NewSessionTokenSource creates a new SessionTokenSource targeting the given IAM endpoint.
func NewSessionTokenSource(sessionToken, iamEndpoint string, creds credentials.TransportCredentials, dialOpts ...grpc.DialOption) *SessionTokenSource {
	ep := strings.Trim(iamEndpoint, " ")
	if strings.TrimSpace(iamEndpoint) == "" {
		if envIAM := os.Getenv("NSC_IAM_ENDPOINT"); strings.TrimSpace(envIAM) != "" {
			ep = strings.Trim(envIAM, " ")
		} else {
			ep = DefaultIAMEndpoint
		}
	}
	if creds == nil {
		creds = credentials.NewTLS(newDefaultTLSConfig())
	}
	var optsCopy []grpc.DialOption
	if len(dialOpts) > 0 {
		optsCopy = make([]grpc.DialOption, len(dialOpts))
		copy(optsCopy, dialOpts)
	}
	return &SessionTokenSource{
		sessionToken: strings.TrimSpace(sessionToken),
		iamEndpoint:  ep,
		creds:        creds,
		dialOpts:     optsCopy,
	}
}

// IAMEndpoint returns the IAM endpoint configured for this session token source.
func (s *SessionTokenSource) IAMEndpoint() string {
	return s.iamEndpoint
}

// IssueToken exchanges the session token for a tenant token via gRPC UserSessionsService at the configured IAM endpoint.
func (s *SessionTokenSource) IssueToken(ctx context.Context, minDuration time.Duration, force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(s.sessionToken) == "" {
		return "", errors.New("empty session token")
	}

	if !force && s.cachedToken != "" && !s.cachedExpiry.IsZero() && time.Now().Add(minDuration).Before(s.cachedExpiry) {
		return s.cachedToken, nil
	}

	if s.sessionsClient == nil {
		conn, err := NewConnectionWithEndpointWithTransportCredentials(
			ctx, s.iamEndpoint, StaticTokenSource{Token: s.sessionToken}, s.creds, s.dialOpts...,
		)
		if err != nil {
			return "", fmt.Errorf("failed to connect to IAM endpoint %s: %w", s.iamEndpoint, err)
		}
		s.conn = conn
		s.sessionsClient = sessionsv1betagrpc.NewUserSessionsServiceClient(conn)
	}

	dur := 2 * minDuration
	if dur > time.Hour {
		dur = time.Hour
	}
	if dur < time.Minute {
		dur = time.Hour
	}

	res, err := ExecuteTokenIssuance(ctx, func(callCtx context.Context) (*sessions.IssueTenantTokenFromSessionResponse, error) {
		return s.sessionsClient.IssueTenantTokenFromSession(callCtx, &sessions.IssueTenantTokenFromSessionRequest{
			TokenDuration: durationpb.New(dur),
		})
	})
	if err != nil {
		return "", fmt.Errorf("failed to issue tenant token from session at %s: %w", s.iamEndpoint, err)
	}
	tenantToken := strings.TrimSpace(res.GetTenantToken())
	if tenantToken == "" {
		return "", fmt.Errorf("IAM endpoint %s returned an empty tenant token", s.iamEndpoint)
	}

	s.cachedToken = tenantToken
	if claims, err := ExtractClaims(tenantToken); err == nil && claims != nil && claims.ExpiresAt != nil {
		s.cachedExpiry = claims.ExpiresAt.Time
	} else {
		s.cachedExpiry = time.Now().Add(dur)
	}

	return s.cachedToken, nil
}

// Close closes the underlying gRPC connection if open.
func (s *SessionTokenSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		s.sessionsClient = nil
		return err
	}
	return nil
}

// LocalTokenSource loads token files from disk, re-reading on IssueToken, and dispatches
// bearer tokens directly or exchanges session tokens through a provider-local SessionTokenSource.
type LocalTokenSource struct {
	mu          sync.Mutex
	filePath    string
	iamEndpoint string
	creds       credentials.TransportCredentials
	dialOpts    []grpc.DialOption

	lastSessionToken string
	sessionSource    *SessionTokenSource
}

// NewLocalTokenSource creates a new LocalTokenSource for a file path, isolating IAM exchanges to iamEndpoint.
func NewLocalTokenSource(filePath, iamEndpoint string, creds credentials.TransportCredentials, dialOpts ...grpc.DialOption) *LocalTokenSource {
	ep := strings.Trim(iamEndpoint, " ")
	if strings.TrimSpace(iamEndpoint) == "" {
		if envIAM := os.Getenv("NSC_IAM_ENDPOINT"); strings.TrimSpace(envIAM) != "" {
			ep = strings.Trim(envIAM, " ")
		} else {
			ep = DefaultIAMEndpoint
		}
	}
	if creds == nil {
		creds = credentials.NewTLS(newDefaultTLSConfig())
	}
	var optsCopy []grpc.DialOption
	if len(dialOpts) > 0 {
		optsCopy = make([]grpc.DialOption, len(dialOpts))
		copy(optsCopy, dialOpts)
	}
	return &LocalTokenSource{
		filePath:    strings.TrimSpace(filePath),
		iamEndpoint: ep,
		creds:       creds,
		dialOpts:    optsCopy,
	}
}

// IAMEndpoint returns the IAM endpoint configured for this local token source.
func (l *LocalTokenSource) IAMEndpoint() string {
	return l.iamEndpoint
}

// FilePath returns the file path monitored by this token source.
func (l *LocalTokenSource) FilePath() string {
	return l.filePath
}

// IssueToken reads the token file and returns the bearer token or exchanges the session token.
func (l *LocalTokenSource) IssueToken(ctx context.Context, minDuration time.Duration, force bool) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, _, err := readBoundedTokenFile(l.filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read token file %q: %w", l.filePath, err)
	}

	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return "", fmt.Errorf("token file %q is empty", l.filePath)
	}

	var payload tokenFilePayload
	if err := json.Unmarshal(data, &payload); err == nil {
		if sTok := strings.TrimSpace(payload.SessionToken); sTok != "" {
			if sTok != l.lastSessionToken || l.sessionSource == nil {
				if l.sessionSource != nil {
					_ = l.sessionSource.Close()
				}
				l.lastSessionToken = sTok
				l.sessionSource = NewSessionTokenSource(sTok, l.iamEndpoint, l.creds, l.dialOpts...)
			}
			return l.sessionSource.IssueToken(ctx, minDuration, force)
		}
		if bTok := strings.TrimSpace(payload.BearerToken); bTok != "" {
			return bTok, nil
		}
		return "", fmt.Errorf("token file %q is missing bearer_token or session_token", l.filePath)
	}

	// Non-JSON raw file content
	if strings.HasPrefix(trimmed, "st_") {
		if trimmed != l.lastSessionToken || l.sessionSource == nil {
			if l.sessionSource != nil {
				_ = l.sessionSource.Close()
			}
			l.lastSessionToken = trimmed
			l.sessionSource = NewSessionTokenSource(trimmed, l.iamEndpoint, l.creds, l.dialOpts...)
		}
		return l.sessionSource.IssueToken(ctx, minDuration, force)
	}

	return trimmed, nil
}

// Close closes any active session connections.
func (l *LocalTokenSource) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.sessionSource != nil {
		err := l.sessionSource.Close()
		l.sessionSource = nil
		return err
	}
	return nil
}

const maxTokenFileBytes = 1 << 20

type tokenFilePayload struct {
	BearerToken  string `json:"bearer_token"`
	SessionToken string `json:"session_token"`
}

func readBoundedTokenFile(filePath string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if fi.IsDir() {
		return nil, fi, fmt.Errorf("path %q is a directory", filePath)
	}
	if fi.Size() > maxTokenFileBytes {
		return nil, fi, fmt.Errorf("token file %q exceeds maximum allowed size of %d bytes", filePath, maxTokenFileBytes)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return nil, fi, err
	}
	if len(data) > maxTokenFileBytes {
		return nil, fi, fmt.Errorf("token file %q exceeds maximum allowed size of %d bytes", filePath, maxTokenFileBytes)
	}
	return data, fi, nil
}

// CheckTokenFilePermissions inspects filePath permissions on Unix platforms and
// returns a warning message if group or world permission bits are set.
func CheckTokenFilePermissions(filePath string) string {
	if runtime.GOOS == "windows" || strings.TrimSpace(filePath) == "" {
		return ""
	}
	fi, err := os.Stat(filePath)
	if err != nil || fi.IsDir() {
		return ""
	}
	mode := fi.Mode().Perm()
	if mode&0o077 != 0 {
		return fmt.Sprintf("Token file %q has overly permissive mode %04o; restrict permissions to 0600 (owner read/write only) to protect credentials from other local users.", filePath, mode)
	}
	return ""
}

func validateTokenFile(filePath, label string) error {
	data, _, err := readBoundedTokenFile(filePath)
	if err != nil {
		return fmt.Errorf("%s %q is unreadable: %w", label, filePath, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return fmt.Errorf("%s %q is empty", label, filePath)
	}
	if strings.HasPrefix(trimmed, "st_") && len(trimmed) > len("st_") && !strings.ContainsAny(trimmed, " \t\r\n") {
		return nil
	}
	var payload tokenFilePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%s %q contains malformed JSON: %w", label, filePath, err)
	}
	if strings.TrimSpace(payload.BearerToken) == "" && strings.TrimSpace(payload.SessionToken) == "" {
		return fmt.Errorf("%s %q is missing or has empty bearer_token or session_token", label, filePath)
	}
	return nil
}

func joinPathForOS(goos string, elem ...string) string {
	if goos == "windows" && len(elem) > 0 && (elem[0] == `\` || elem[0] == `/` || strings.Contains(elem[0], `\`) || (len(elem[0]) >= 2 && elem[0][1] == ':')) {
		rootOnly := elem[0] == `\` || elem[0] == `/`
		parts := make([]string, 0, len(elem))
		for i, e := range elem {
			if i == 0 {
				if rootOnly {
					continue
				}
				e = strings.TrimRight(e, `/\`)
			} else {
				e = strings.Trim(e, `/\`)
			}
			if e != "" {
				parts = append(parts, e)
			}
		}
		if rootOnly {
			if len(parts) == 0 {
				return `\`
			}
			return `\` + strings.Join(parts, `\`)
		}
		return strings.Join(parts, `\`)
	}
	return filepath.Join(elem...)
}

func workloadTokenPaths(goos string, getenv func(string) string) []string {
	if goos == "windows" {
		var paths []string
		if dir := strings.TrimSpace(getenv("NSC_METADATA_DIR")); dir != "" {
			paths = append(paths, joinPathForOS(goos, dir, "token.json"))
		}
		if progData := strings.TrimSpace(getenv("ProgramData")); progData != "" {
			paths = append(paths, joinPathForOS(goos, progData, "Namespace", "GuestAgent", "metadata", "token.json"))
		}
		return paths
	}
	return []string{"/var/run/nsc/token.json"}
}

// loadDefaultTokenSource attempts to locate default Namespace credentials from
// /var/run/nsc/token.json (Unix), NSC_METADATA_DIR\token.json or
// %ProgramData%\Namespace\GuestAgent\metadata\token.json (Windows), or the user config directory.
func loadDefaultTokenSource(iamEndpoint string, creds credentials.TransportCredentials, dialOpts ...grpc.DialOption) (TokenSource, error) {
	return loadDefaultTokenSourceWith(runtime.GOOS, os.Getenv, os.UserConfigDir, iamEndpoint, creds, dialOpts...)
}

func loadDefaultTokenSourceWith(
	goos string,
	getenv func(string) string,
	userConfigDirFn func() (string, error),
	iamEndpoint string,
	creds credentials.TransportCredentials,
	dialOpts ...grpc.DialOption,
) (TokenSource, error) {
	// 1. Workload token
	for _, workloadTokenPath := range workloadTokenPaths(goos, getenv) {
		if fi, err := os.Stat(workloadTokenPath); err == nil {
			if fi.IsDir() {
				return nil, fmt.Errorf("default workload credential %q is a directory", workloadTokenPath)
			}
			if err := validateTokenFile(workloadTokenPath, "default workload credential"); err != nil {
				return nil, err
			}
			return NewLocalTokenSource(workloadTokenPath, iamEndpoint, creds, dialOpts...), nil
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to inspect default workload credential %q: %w", workloadTokenPath, err)
		}
	}

	// 2. User token
	userConfigDir, err := userConfigDirFn()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the operating-system user configuration directory: %w", err)
	}
	userTokenPath := joinPathForOS(goos, userConfigDir, "ns", "token.json")
	if fi, err := os.Stat(userTokenPath); err == nil {
		if fi.IsDir() {
			return nil, fmt.Errorf("default user credential %q is a directory", userTokenPath)
		}
		if err := validateTokenFile(userTokenPath, "default user credential"); err != nil {
			return nil, err
		}
		return NewLocalTokenSource(userTokenPath, iamEndpoint, creds, dialOpts...), nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to inspect default user credential %q: %w", userTokenPath, err)
	}

	return nil, fmt.Errorf("no Namespace credentials found. Provide provider token/token_file, set NSC_TOKEN/NSC_TOKEN_FILE, or log in via `nsc login`")
}

func resolveInlineTokenSource(tok, iamEP string, creds credentials.TransportCredentials, dialOpts ...grpc.DialOption) TokenSource {
	if strings.HasPrefix(tok, "st_") {
		return NewSessionTokenSource(tok, iamEP, creds, dialOpts...)
	}
	return StaticTokenSource{Token: tok}
}

// ResolveTokenSource resolves the active TokenSource using the deterministic precedence chain:
// 1. Provider configuration `token`
// 2. Provider configuration `token_file`
// 3. Environment variable `NSC_TOKEN`
// 4. Environment variable `NSC_TOKEN_FILE`
// 5. Fallback default credentials (workload token `/var/run/nsc/token.json` on Unix or `NSC_METADATA_DIR\token.json` / `%ProgramData%\Namespace\GuestAgent\metadata\token.json` on Windows, then user token)
func ResolveTokenSource(ctx context.Context, cfg AuthConfig) (TokenSource, error) {
	iamEP := strings.Trim(cfg.IAMEndpoint, " ")
	if strings.TrimSpace(cfg.IAMEndpoint) == "" {
		if envIAM := os.Getenv("NSC_IAM_ENDPOINT"); strings.TrimSpace(envIAM) != "" {
			iamEP = strings.Trim(envIAM, " ")
		} else {
			iamEP = DefaultIAMEndpoint
		}
	}

	// 1. Explicit provider token attribute
	if trimmed := strings.TrimSpace(cfg.Token); trimmed != "" {
		return resolveInlineTokenSource(trimmed, iamEP, cfg.Creds, cfg.DialOptions...), nil
	}

	// 2. Explicit provider token_file attribute
	if trimmedFile := strings.TrimSpace(cfg.TokenFile); trimmedFile != "" {
		if err := validateTokenFile(trimmedFile, "configured token_file"); err != nil {
			return nil, err
		}
		return NewLocalTokenSource(trimmedFile, iamEP, cfg.Creds, cfg.DialOptions...), nil
	}

	// 3. Environment variable NSC_TOKEN
	if envTok := strings.TrimSpace(os.Getenv("NSC_TOKEN")); envTok != "" {
		return resolveInlineTokenSource(envTok, iamEP, cfg.Creds, cfg.DialOptions...), nil
	}

	// 4. Environment variable NSC_TOKEN_FILE
	if envFile := strings.TrimSpace(os.Getenv("NSC_TOKEN_FILE")); envFile != "" {
		if err := validateTokenFile(envFile, "NSC_TOKEN_FILE"); err != nil {
			return nil, err
		}
		return NewLocalTokenSource(envFile, iamEP, cfg.Creds, cfg.DialOptions...), nil
	}

	// 5. Fallback to default credentials (workload token -> user config token)
	ts, err := loadDefaultTokenSource(iamEP, cfg.Creds, cfg.DialOptions...)
	if err != nil {
		return nil, fmt.Errorf("no Namespace credentials found. Provide provider token/token_file, set NSC_TOKEN/NSC_TOKEN_FILE, or log in via `nsc login`: %w", err)
	}

	return ts, nil
}

func decodeBase64URL(seg string) ([]byte, error) {
	if strings.Contains(seg, "=") {
		return base64.URLEncoding.DecodeString(seg)
	}
	return base64.RawURLEncoding.DecodeString(seg)
}

func isTokenPrefixChars(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isValid := (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '_' ||
			c == '-'
		if !isValid {
			return false
		}
	}
	return true
}

func isBase64URLSegment(seg string) bool {
	if seg == "" {
		return false
	}
	for i := 0; i < 2 && len(seg) > 0 && seg[len(seg)-1] == '='; i++ {
		seg = seg[:len(seg)-1]
	}
	if seg == "" {
		return false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		isValid := (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '-' ||
			c == '_'
		if !isValid {
			return false
		}
	}
	return true
}

func isInlineToken(s string) bool {
	if strings.HasPrefix(s, "nsct_") ||
		strings.HasPrefix(s, "st_") ||
		strings.HasPrefix(s, "nscw_") ||
		strings.HasPrefix(s, "nscu_") ||
		strings.HasPrefix(s, "nsrt_") ||
		strings.HasPrefix(s, "tok_") {
		return true
	}
	if strings.ContainsAny(s, "/\\ \t\r\n") {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	header := parts[0]
	if !strings.HasPrefix(header, "eyJ") {
		idx := strings.Index(header, "_eyJ")
		if idx <= 0 {
			return false
		}
		if !isTokenPrefixChars(header[:idx]) {
			return false
		}
		header = header[idx+1:]
	}
	return isBase64URLSegment(header) && isBase64URLSegment(parts[1]) && isBase64URLSegment(parts[2])
}

// ResolveTenantFromToken resolves the tenant identity from a given token string or token file path.
// It supports:
// 1. Pinned SDK claims parser for nsct_<JWT>, st_<JWT>, and nscw_<JWT> tokens.
// 2. Structured 3-part JWT tokens (decoding claims https://namespace.so/claims/tenant, tenant_id, tid, tenant string/object, and sub as a bare tenant ID, tenant:<id>, or t_<id>).
// 3. Local token files (JSON format with bearer_token or session_token, or raw token string).
// This helper decodes claims but does not verify JWT signatures. It is only an
// acceptance-test safety guard; Namespace API authentication remains authoritative.
// Opaque tokens and JWTs without a supported tenant identity claim fail closed.
func ResolveTenantFromToken(tokenOrPath string) (string, error) {
	tokenStr := strings.TrimSpace(tokenOrPath)
	if tokenStr == "" {
		return "", fmt.Errorf("token cannot be empty")
	}

	// Check inline token prefixes before probing the filesystem so raw credentials never trigger os.Stat.
	if !isInlineToken(tokenStr) {
		if fi, err := os.Stat(tokenStr); err == nil && !fi.IsDir() {
			data, _, err := readBoundedTokenFile(tokenStr)
			if err != nil {
				return "", fmt.Errorf("failed to read token file %q: %w", tokenStr, err)
			}
			trimmed := strings.TrimSpace(string(data))
			if len(trimmed) == 0 {
				return "", fmt.Errorf("token file %q is empty", tokenStr)
			}
			var payload tokenFilePayload
			if err := json.Unmarshal(data, &payload); err == nil {
				sTok := strings.TrimSpace(payload.SessionToken)
				bTok := strings.TrimSpace(payload.BearerToken)
				// Match LocalTokenSource.IssueToken precedence: session_token takes priority over bearer_token.
				// If both are present, resolve session_token first and fail closed if bearer_token conflicts.
				if sTok != "" {
					sessionTenant, err := resolveTenantFromTokenString(sTok)
					if err != nil {
						return "", err
					}
					if bTok != "" {
						bearerTenant, bErr := resolveTenantFromTokenString(bTok)
						if bErr != nil {
							return "", fmt.Errorf("token file %q contains both session_token and an unverifiable bearer_token: %w", tokenOrPath, bErr)
						}
						if bearerTenant != sessionTenant {
							return "", fmt.Errorf("token file %q contains conflicting tenants across session_token (%q) and bearer_token (%q)", tokenOrPath, sessionTenant, bearerTenant)
						}
					}
					return sessionTenant, nil
				}
				if bTok != "" {
					tokenStr = bTok
				} else {
					tokenStr = trimmed
				}
			} else {
				tokenStr = trimmed
			}
			if tokenStr == "" {
				return "", fmt.Errorf("token file %q is empty", tokenOrPath)
			}
		}
	}

	return resolveTenantFromTokenString(tokenStr)
}

func resolveTenantFromTokenString(tokenStr string) (string, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return "", fmt.Errorf("token cannot be empty")
	}

	// Format 1: Namespace prefixed JWT tokens (nsct_<JWT>, st_<JWT>, nscw_<JWT>)
	if strings.HasPrefix(tokenStr, "nsct_") || strings.HasPrefix(tokenStr, "st_") || strings.HasPrefix(tokenStr, "nscw_") {
		claims, err := ExtractClaims(tokenStr)
		if err == nil && claims != nil && strings.TrimSpace(claims.TenantID) != "" {
			return strings.TrimSpace(claims.TenantID), nil
		}
		return "", fmt.Errorf("unsupported or opaque token format: acceptance testing requires a decodable JWT tenant claim or valid Namespace tenant token")
	}

	// Format 2: Standard 3-part JWT token
	parts := strings.Split(tokenStr, ".")
	if len(parts) == 3 {
		payloadBytes, err := decodeBase64URL(parts[1])
		if err != nil {
			return "", fmt.Errorf("malformed JWT payload: %w", err)
		}

		var claims map[string]interface{}
		if err := json.Unmarshal(payloadBytes, &claims); err != nil {
			return "", fmt.Errorf("malformed JWT claims JSON: %w", err)
		}

		var resolvedTenant string
		if v, ok := claims["https://namespace.so/claims/tenant"].(string); ok && strings.TrimSpace(v) != "" {
			resolvedTenant = strings.TrimSpace(v)
		} else if v, ok := claims["tenant_id"].(string); ok && strings.TrimSpace(v) != "" {
			resolvedTenant = strings.TrimSpace(v)
		} else if v, ok := claims["tid"].(string); ok && strings.TrimSpace(v) != "" {
			resolvedTenant = strings.TrimSpace(v)
		} else if v, ok := claims["tenant"].(string); ok && strings.TrimSpace(v) != "" {
			resolvedTenant = strings.TrimSpace(v)
		} else if m, ok := claims["tenant"].(map[string]interface{}); ok {
			if id, ok := m["id"].(string); ok && strings.TrimSpace(id) != "" {
				resolvedTenant = strings.TrimSpace(id)
			} else if name, ok := m["name"].(string); ok && strings.TrimSpace(name) != "" {
				resolvedTenant = strings.TrimSpace(name)
			}
		} else if v, ok := claims["sub"].(string); ok && strings.TrimSpace(v) != "" {
			sub := strings.TrimSpace(v)
			if strings.HasPrefix(sub, "tenant:") {
				resolvedTenant = strings.TrimPrefix(sub, "tenant:")
			} else if strings.HasPrefix(sub, "t_") {
				resolvedTenant = sub
			} else {
				resolvedTenant = sub
			}
		}

		if resolvedTenant == "" {
			return "", fmt.Errorf("token payload does not contain a supported tenant identity claim")
		}

		return resolvedTenant, nil
	}

	// Format 3: Unsupported or opaque token format
	return "", fmt.Errorf("unsupported or opaque token format: acceptance testing requires a decodable JWT tenant claim or valid Namespace tenant token")
}

// VerifyTenantTokenMatch verifies that the given token or token file path contains a tenant identity
// matching expectedTenant. It compares decoded claims and does not verify JWT signatures; the
// Namespace API still authenticates the same credential before any acceptance mutation. It fails
// closed on opaque, malformed, empty, or mismatched tokens.
func VerifyTenantTokenMatch(tokenOrPath, expectedTenant string) error {
	expectedTenant = strings.TrimSpace(expectedTenant)
	if expectedTenant == "" {
		return fmt.Errorf("expected tenant (NSC_TEST_TENANT) cannot be empty")
	}
	tokenStr := strings.TrimSpace(tokenOrPath)
	if tokenStr == "" {
		return fmt.Errorf("token cannot be empty")
	}

	resolvedTenant, err := ResolveTenantFromToken(tokenStr)
	if err != nil {
		if strings.Contains(err.Error(), "unsupported or opaque token format") {
			return fmt.Errorf("unsupported or opaque token format: acceptance testing requires a decodable JWT tenant claim matching %q", expectedTenant)
		}
		return err
	}

	if resolvedTenant != expectedTenant {
		return fmt.Errorf("authenticated credential tenant %q does not match NSC_TEST_TENANT %q", resolvedTenant, expectedTenant)
	}

	return nil
}
