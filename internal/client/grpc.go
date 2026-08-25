package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"namespacelabs.dev/integrations/api"
	"namespacelabs.dev/integrations/nsc"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

var (
	// DebugWriterMutex protects DebugWriter, DebugShowRequests, and DebugShowResponses.
	DebugWriterMutex sync.RWMutex
	// DebugWriter overrides the output destination for debug logging (useful in tests).
	DebugWriter io.Writer
	// DebugShowRequests forces request payload logging when debug is active.
	DebugShowRequests bool
	// DebugShowResponses forces response payload logging when debug is active.
	DebugShowResponses bool

	// RequireTransportSecurity controls whether transport security is required for per-RPC credentials.
	RequireTransportSecurity = true

	defaultStderrWriter = NewSynchronizedRedactingWriter(nil)
)

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
					EmitLog(writer, "[DEBUG] Response Payload: %s\n", string(b))
				}
			}
		} else {
			EmitLog(writer, "[DEBUG] request failed: %v\n", err)
		}

		return err
	}
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
	token api.TokenSource,
	creds credentials.TransportCredentials,
	opts ...grpc.DialOption,
) (*grpc.ClientConn, error) {
	parsed, err := ParseGRPCEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	if creds == nil {
		creds = credentials.NewTLS(&tls.Config{})
	}

	ourOpts := []grpc.DialOption{
		grpc.WithUserAgent(fmt.Sprintf("nsc-go/%s", nsc.Version)),
		grpc.WithTransportCredentials(creds),
	}

	if token != nil {
		ourOpts = append(ourOpts, grpc.WithPerRPCCredentials(tokenCredWrapper{token: token}))
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
	return grpc.NewClient(parsed, append(ourOpts, opts...)...)
}

// ParseGRPCEndpoint parses and validates an endpoint target for gRPC dialing.
func ParseGRPCEndpoint(endpoint string) (string, error) {
	value := strings.TrimSpace(endpoint)
	if err := ValidateEndpoint(value); err != nil {
		return "", err
	}

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
	token api.TokenSource
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

func (tokenCredWrapper) RequireTransportSecurity() bool {
	return RequireTransportSecurity && grpcapi.RequireTransportSecurity
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
