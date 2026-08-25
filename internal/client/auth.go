package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/private/sessions/sessionsv1betagrpc"
	sessions "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/sessions"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"
	"namespacelabs.dev/integrations/api"
	"namespacelabs.dev/integrations/auth"
)

// AuthConfig defines the input parameters for credential resolution.
type AuthConfig struct {
	Token       string
	TokenFile   string
	IAMEndpoint string
	Creds       credentials.TransportCredentials
	DialOptions []grpc.DialOption
}

// StaticTokenSource implements api.TokenSource for static string tokens.
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

// SessionTokenSource implements api.TokenSource for session token exchange against a specific IAM endpoint.
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
	ep := strings.TrimSpace(iamEndpoint)
	if ep == "" {
		if envIAM := strings.TrimSpace(os.Getenv("NSC_IAM_ENDPOINT")); envIAM != "" {
			ep = envIAM
		} else {
			ep = DefaultIAMEndpoint
		}
	}
	if creds == nil {
		creds = credentials.NewTLS(&tls.Config{})
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
			ctx, s.iamEndpoint, nil, s.creds, s.dialOpts...,
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

	reqCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+s.sessionToken)
	res, err := s.sessionsClient.IssueTenantTokenFromSession(reqCtx, &sessions.IssueTenantTokenFromSessionRequest{
		TokenDuration: durationpb.New(dur),
	})
	if err != nil {
		return "", fmt.Errorf("failed to issue tenant token from session at %s: %w", s.iamEndpoint, err)
	}

	s.cachedToken = res.TenantToken
	if claims, err := auth.ExtractClaims(res.TenantToken); err == nil && claims != nil && claims.ExpiresAt != nil {
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
	ep := strings.TrimSpace(iamEndpoint)
	if ep == "" {
		if envIAM := strings.TrimSpace(os.Getenv("NSC_IAM_ENDPOINT")); envIAM != "" {
			ep = envIAM
		} else {
			ep = DefaultIAMEndpoint
		}
	}
	if creds == nil {
		creds = credentials.NewTLS(&tls.Config{})
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

	data, err := os.ReadFile(l.filePath)
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

type tokenFilePayload struct {
	BearerToken  string `json:"bearer_token"`
	SessionToken string `json:"session_token"`
}

func validateTokenFile(filePath, label string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("%s %q is unreadable: %w", label, filePath, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return fmt.Errorf("%s %q is empty", label, filePath)
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

// loadDefaultTokenSource attempts to locate default Namespace credentials from /var/run/nsc/token.json
// or the user config directory ~/.config/ns/token.json.
func loadDefaultTokenSource(iamEndpoint string, creds credentials.TransportCredentials, dialOpts ...grpc.DialOption) (api.TokenSource, error) {
	// 1. Workload token
	const workloadTokenPath = "/var/run/nsc/token.json"
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

	// 2. User token
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the operating-system user configuration directory: %w", err)
	}
	userTokenPath := filepath.Join(userConfigDir, "ns", "token.json")
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

// ResolveTokenSource resolves the active api.TokenSource using the deterministic precedence chain:
// 1. Provider configuration `token`
// 2. Provider configuration `token_file`
// 3. Environment variable `NSC_TOKEN`
// 4. Environment variable `NSC_TOKEN_FILE`
// 5. Fallback default credentials (workload token `/var/run/nsc/token.json` then user token `~/.config/ns/token.json`)
func ResolveTokenSource(ctx context.Context, cfg AuthConfig) (api.TokenSource, error) {
	iamEP := strings.TrimSpace(cfg.IAMEndpoint)
	if iamEP == "" {
		if envIAM := strings.TrimSpace(os.Getenv("NSC_IAM_ENDPOINT")); envIAM != "" {
			iamEP = envIAM
		} else {
			iamEP = DefaultIAMEndpoint
		}
	}

	// 1. Explicit provider token attribute
	if trimmed := strings.TrimSpace(cfg.Token); trimmed != "" {
		return StaticTokenSource{Token: trimmed}, nil
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
		return StaticTokenSource{Token: envTok}, nil
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
	seg = strings.TrimSpace(seg)
	if l := len(seg) % 4; l > 0 {
		seg += strings.Repeat("=", 4-l)
	}
	return base64.URLEncoding.DecodeString(seg)
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

	// If tokenOrPath is a valid file path on disk, load the token from the file
	if fi, err := os.Stat(tokenStr); err == nil && !fi.IsDir() {
		data, err := os.ReadFile(tokenStr)
		if err != nil {
			return "", fmt.Errorf("failed to read token file %q: %w", tokenStr, err)
		}
		trimmed := strings.TrimSpace(string(data))
		if len(trimmed) == 0 {
			return "", fmt.Errorf("token file %q is empty", tokenStr)
		}
		var payload tokenFilePayload
		if err := json.Unmarshal(data, &payload); err == nil {
			if strings.TrimSpace(payload.BearerToken) != "" {
				tokenStr = strings.TrimSpace(payload.BearerToken)
			} else if strings.TrimSpace(payload.SessionToken) != "" {
				tokenStr = strings.TrimSpace(payload.SessionToken)
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

	// Format 1: Namespace prefixed JWT tokens (nsct_<JWT>, st_<JWT>, nscw_<JWT>)
	if strings.HasPrefix(tokenStr, "nsct_") || strings.HasPrefix(tokenStr, "st_") || strings.HasPrefix(tokenStr, "nscw_") {
		claims, err := auth.ExtractClaims(tokenStr)
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
