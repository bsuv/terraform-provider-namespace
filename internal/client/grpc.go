package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	endpointutil "github.com/bsuv/terraform-provider-namespace/internal/endpoint"
	"github.com/bsuv/terraform-provider-namespace/internal/redact"
	"golang.org/x/net/http/httpproxy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const nscVersion = "1"

// RequireTransportSecurity enforces transport-level security for per-RPC Bearer
// credentials. It defaults to true in production and should only be set to false
// by in-memory bufconn test harnesses using insecure.NewCredentials().
var RequireTransportSecurity = true

var (
	// DebugWriterMutex protects DebugWriter, DebugShowRequests, and DebugShowResponses.
	DebugWriterMutex sync.RWMutex
	// DebugWriter overrides the output destination for debug logging (useful in tests).
	DebugWriter io.Writer
	// DebugShowRequests forces request payload logging when debug is active.
	DebugShowRequests bool
	// DebugShowResponses forces response payload logging when debug is active.
	DebugShowResponses bool

	defaultStderrWriter = NewSynchronizedRedactingWriter(nil)
)

// newDefaultTLSConfig returns the provider's baseline TLS configuration enforcing TLS 1.2+.
func newDefaultTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
}

// SynchronizedRedactingWriter wraps an io.Writer and ensures that all writes are passed
// through RedactSecrets and synchronized under a mutex for complete concurrency safety.
type SynchronizedRedactingWriter struct {
	mu  sync.Mutex
	out io.Writer
}

// NewSynchronizedRedactingWriter creates a new thread-safe redacting writer.
// If out is nil, it dynamically delegates to os.Stderr at write time.
func NewSynchronizedRedactingWriter(out io.Writer) *SynchronizedRedactingWriter {
	if sw, ok := out.(*SynchronizedRedactingWriter); ok {
		return sw
	}
	return &SynchronizedRedactingWriter{out: out}
}

func (w *SynchronizedRedactingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	redacted := RedactSecrets(string(p))
	target := w.out
	if target == nil {
		target = os.Stderr
	}
	_, err = target.Write([]byte(redacted))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// EmitLog formats a log line, applies RedactSecrets, and safely writes to the destination.
func EmitLog(writer io.Writer, format string, args ...any) {
	if writer == nil {
		return
	}
	raw := fmt.Sprintf(format, args...)
	if _, ok := writer.(*SynchronizedRedactingWriter); !ok {
		raw = RedactSecrets(raw)
	}
	_, _ = io.WriteString(writer, raw)
}

// RedactingUnaryInterceptor constructs a unary client interceptor that redacts all debug output.
func RedactingUnaryInterceptor(writer io.Writer, showRequests, showResponses bool, endpoint string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		EmitLog(writer, "[DEBUG] RPC request to: %s [%s]\n", method, endpoint)

		if showRequests {
			if b, err := json.Marshal(req); err == nil {
				EmitLog(writer, "[DEBUG] Payload: %s\n", string(b))
			}
		}

		err := invoker(ctx, method, req, reply, cc, opts...)
		if err == nil {
			if showResponses {
				if b, err := json.Marshal(reply); err == nil {
					payload := redactSensitiveRPCResponsePayload(method, string(b))
					EmitLog(writer, "[DEBUG] Response Payload: %s\n", payload)
				}
			}
		} else {
			EmitLog(writer, "[DEBUG] request failed: %v\n", err)
		}

		return err
	}
}

func redactSensitiveRPCResponsePayload(method, payload string) string {
	if strings.HasSuffix(method, "/EnsureGradleCache") || strings.HasSuffix(method, "/EnsureHttpCache") {
		return redact.CredentialUsernames(payload)
	}
	return payload
}

// RedactingStreamInterceptor constructs a stream client interceptor that redacts streaming messages.
func RedactingStreamInterceptor(writer io.Writer, showRequests, showResponses bool, endpoint string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		EmitLog(writer, "[DEBUG] Stream RPC to: %s [%s]\n", method, endpoint)
		clientStream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			EmitLog(writer, "[DEBUG] Stream creation failed: %v\n", err)
			return nil, err
		}
		return &redactingClientStream{
			ClientStream:  clientStream,
			writer:        writer,
			showRequests:  showRequests,
			showResponses: showResponses,
		}, nil
	}
}

type redactingClientStream struct {
	grpc.ClientStream
	writer        io.Writer
	showRequests  bool
	showResponses bool
}

func (s *redactingClientStream) SendMsg(m any) error {
	if s.showRequests {
		if b, err := json.Marshal(m); err == nil {
			EmitLog(s.writer, "[DEBUG] Stream SendMsg: %s\n", string(b))
		}
	}
	err := s.ClientStream.SendMsg(m)
	if err != nil {
		EmitLog(s.writer, "[DEBUG] Stream SendMsg failed: %v\n", err)
	}
	return err
}

func (s *redactingClientStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err == nil {
		if s.showResponses {
			if b, err := json.Marshal(m); err == nil {
				EmitLog(s.writer, "[DEBUG] Stream RecvMsg: %s\n", string(b))
			}
		}
	} else if err != io.EOF {
		EmitLog(s.writer, "[DEBUG] Stream RecvMsg failed: %v\n", err)
	}
	return err
}

// NewConnectionWithEndpointWithTransportCredentials initializes a gRPC client connection with
// user-agent injection, transport credentials, per-RPC token authentication, and synchronized
// secret-redacting interceptors when debug logging is enabled.
func NewConnectionWithEndpointWithTransportCredentials(
	ctx context.Context,
	endpoint string,
	token TokenSource,
	creds credentials.TransportCredentials,
	opts ...grpc.DialOption,
) (*grpc.ClientConn, error) {
	parsed, err := ParseGRPCEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	if creds == nil {
		creds = credentials.NewTLS(newDefaultTLSConfig())
	}

	allowInsecure := shouldAllowInsecurePerRPCCreds(creds)
	userAgent := fmt.Sprintf("nsc-go/%s", nscVersion)

	proxyURL, err := resolveValidatedProxyURL(parsed)
	if err != nil {
		return nil, err
	}

	target := (&url.URL{Scheme: "dns", Path: "/" + parsed}).String()
	dialerOpt := grpc.WithContextDialer(newDirectSSRFSafeDialer())
	ourOpts := []grpc.DialOption{
		grpc.WithUserAgent(userAgent),
		grpc.WithTransportCredentials(creds),
	}
	if proxyURL != nil {
		// Use passthrough with explicit target authority so the validated HTTP/HTTPS
		// proxy resolves the target (preserving air-gapped corporate proxy support)
		// while our dialer validates the proxy's resolved IP before connect(2).
		target = (&url.URL{Scheme: "passthrough", Path: "/" + parsed}).String()
		ourOpts = append(ourOpts,
			grpc.WithNoProxy(),
			grpc.WithAuthority(parsed),
			grpc.WithContextDialer(newProxySSRFSafeDialer(proxyURL, parsed, userAgent)),
		)
	} else {
		ourOpts = append(ourOpts, dialerOpt)
	}

	if token != nil {
		ourOpts = append(ourOpts, grpc.WithPerRPCCredentials(tokenCredWrapper{
			token:         token,
			allowInsecure: allowInsecure,
		}))
	}

	DebugWriterMutex.RLock()
	customWriter := DebugWriter
	showReq := DebugShowRequests
	showResp := DebugShowResponses
	DebugWriterMutex.RUnlock()

	debugActive := customWriter != nil || parseEnvBool("NS_GRPC_DEBUG")
	if debugActive {
		var syncWriter io.Writer
		if customWriter != nil {
			syncWriter = NewSynchronizedRedactingWriter(customWriter)
		} else {
			syncWriter = defaultStderrWriter
		}

		debugRequests := showReq || parseEnvBool("NSC_GRPC_DEBUG_REQUESTS")
		debugResponses := showResp || parseEnvBool("NSC_GRPC_DEBUG_RESPONSES")

		ourOpts = append(ourOpts,
			grpc.WithUnaryInterceptor(RedactingUnaryInterceptor(syncWriter, debugRequests, debugResponses, endpoint)),
			grpc.WithStreamInterceptor(RedactingStreamInterceptor(syncWriter, debugRequests, debugResponses, endpoint)),
		)
	}

	_ = ctx
	return grpc.NewClient(target, append(ourOpts, opts...)...)
}

func resolveValidatedProxyURL(targetHostPort string) (*url.URL, error) {
	proxyFunc := httpproxy.FromEnvironment().ProxyFunc()
	proxyURL, err := proxyFunc(&url.URL{Scheme: "https", Host: targetHostPort})
	if err != nil {
		return nil, fmt.Errorf("invalid proxy configuration: %w", err)
	}
	if proxyURL == nil {
		return nil, nil
	}

	scheme := strings.ToLower(proxyURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("unsupported proxy scheme %q; only http and https proxies are supported", proxyURL.Scheme)
	}
	if err := endpointutil.ValidateHost(proxyURL.Hostname()); err != nil {
		return nil, fmt.Errorf("proxy host rejected: %w", err)
	}
	return proxyURL, nil
}

func ssrfDialControl(_ string, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("dial target %q is not a valid IP:port", address)
	}
	return endpointutil.ValidateIP(ap.Addr())
}

func newSSRFSafeNetDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   ssrfDialControl,
	}
}

func newDirectSSRFSafeDialer() func(context.Context, string) (net.Conn, error) {
	dialer := newSSRFSafeNetDialer()
	return func(ctx context.Context, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", addr)
	}
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}

func newProxySSRFSafeDialer(proxyURL *url.URL, targetHostPort, userAgent string) func(context.Context, string) (net.Conn, error) {
	dialer := newSSRFSafeNetDialer()
	proxyHost := proxyURL.Hostname()
	proxyPort := proxyURL.Port()
	if proxyPort == "" {
		if strings.EqualFold(proxyURL.Scheme, "https") {
			proxyPort = "443"
		} else {
			proxyPort = "80"
		}
	}
	proxyAddr := net.JoinHostPort(proxyHost, proxyPort)

	return func(ctx context.Context, _ string) (net.Conn, error) {
		if err := endpointutil.ValidateHost(proxyHost); err != nil {
			return nil, fmt.Errorf("proxy host rejected: %w", err)
		}

		conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}

		if strings.EqualFold(proxyURL.Scheme, "https") {
			tlsCfg := newDefaultTLSConfig()
			tlsCfg.ServerName = proxyHost
			tlsConn := tls.Client(conn, tlsCfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("proxy TLS handshake failed: %w", err)
			}
			conn = tlsConn
		}

		tunneled, err := performHTTPConnectHandshake(ctx, conn, targetHostPort, userAgent, proxyURL.User)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tunneled, nil
	}
}

func performHTTPConnectHandshake(ctx context.Context, conn net.Conn, targetHostPort, userAgent string, proxyUser *url.Userinfo) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer func() {
		stopCancel()
		_ = conn.SetDeadline(time.Time{})
	}()

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: targetHostPort},
		Host:   targetHostPort,
		Header: make(http.Header),
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if proxyUser != nil {
		u := proxyUser.Username()
		p, _ := proxyUser.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u+":"+p)))
	}

	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("failed to write HTTP CONNECT request: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP CONNECT response: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy HTTP CONNECT rejected with status %s", resp.Status)
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// ParseGRPCEndpoint parses and validates an endpoint target for gRPC dialing.
func ParseGRPCEndpoint(endpoint string) (string, error) {
	if err := ValidateEndpoint(endpoint); err != nil {
		return "", err
	}
	value := strings.TrimSpace(endpoint)

	parseValue := value
	if !strings.HasPrefix(strings.ToLower(value), "https://") {
		parseValue = "https://" + value
	}
	u, err := url.Parse(parseValue)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}

	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "443"), nil
	}
	return u.Host, nil
}

type tokenCredWrapper struct {
	token         TokenSource
	allowInsecure bool
}

func shouldAllowInsecurePerRPCCreds(creds credentials.TransportCredentials) bool {
	return creds != nil && creds.Info().SecurityProtocol == "insecure" && !RequireTransportSecurity
}

func (t tokenCredWrapper) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	token, err := t.token.IssueToken(ctx, 5*time.Minute, false)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Authorization": "Bearer " + token,
	}, nil
}

func (t tokenCredWrapper) RequireTransportSecurity() bool {
	return !t.allowInsecure
}

func parseEnvBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	if v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") || strings.EqualFold(v, "on") {
		return true
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}
