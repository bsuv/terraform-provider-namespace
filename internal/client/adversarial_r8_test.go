package client_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/iam/v1beta/iamv1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/gradle/v1beta/gradlev1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/httpcache/v1beta/httpcachev1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/private/sessions/sessionsv1betagrpc"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	gradlev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/gradle/v1beta"
	httpcachev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/httpcache/v1beta"
	sessions "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/sessions"
	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Adversarial mock payloads
var (
	mockRSAPrivateKey     = testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "mock-rsa-key-body-payload")
	mockPKCS8PrivateKey   = testutil.SynthesizePEMBlock("PRIVATE KEY", "mock-pkcs8-key-body-payload")
	mockOpenSSHPrivateKey = testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "mock-openssh-key-body-payload")
	mockDSAPrivateKey     = testutil.SynthesizePEMBlock("DSA PRIVATE KEY", "mock-dsa-key-body-payload")
)

type advSessionsServer struct {
	sessionsv1betagrpc.UnimplementedUserSessionsServiceServer
}

func (s *advSessionsServer) IssueTenantTokenFromSession(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
	if req.TokenDuration != nil && req.TokenDuration.Seconds == 777 {
		// Adversarial error string containing nested secrets
		return nil, status.Errorf(codes.Unauthenticated,
			"session error: st_adversarial_session_token_12345 failed with CACHE_PASSWORD=leak_pass_777 and %s", mockDSAPrivateKey)
	}
	return &sessions.IssueTenantTokenFromSessionResponse{
		TenantToken: "tok_adversarial_tenant_token_session_exchange_555",
	}, nil
}

func setupAdversarialServer(t *testing.T) (*bufconn.Listener, func()) {
	t.Helper()
	lis := bufconn.Listen(10 * 1024 * 1024)
	grpcSrv := grpc.NewServer()

	mockSrv := testutil.NewMockServer()
	sessionsSrv := &advSessionsServer{}

	var counter int64
	var mu sync.Mutex

	mockSrv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		mu.Lock()
		counter++
		c := counter
		mu.Unlock()

		jwt := createTestJWT(map[string]interface{}{
			"tenant_id": fmt.Sprintf("tenant-adv-%d", c),
			"secret_payload": map[string]interface{}{
				"sub": fmt.Sprintf("user_adv_%d", c),
				"key": mockRSAPrivateKey,
			},
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: "nsct_" + jwt,
		}, nil
	}

	mockSrv.Token.IssueIdTokenFunc = func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
		jwt := createTestJWT(map[string]interface{}{
			"sub": "user:id_token_sub_" + req.Audience,
			"aud": req.Audience,
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		return &iamv1beta.IssueIdTokenResponse{
			IdToken: jwt,
		}, nil
	}

	mockSrv.Tenant.IssueTenantClientCertificateFunc = func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
		return &iamv1beta.IssueTenantClientCertificateResponse{
			ClientCertificatePem: testutil.SynthesizePEMBlock("CERTIFICATE", "MIIDXTCCAkWgAwIBAgIUadversarialCert1234567890=="),
			PrivateKeyPem:        mockPKCS8PrivateKey,
		}, nil
	}

	mockSrv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
		mu.Lock()
		counter++
		c := counter
		mu.Unlock()
		return &gradlev1beta.EnsureGradleCacheResponse{
			Password: fmt.Sprintf("super-secret-gradle-pass-dyn-%d-999", c),
		}, nil
	}

	mockSrv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
		mu.Lock()
		counter++
		c := counter
		mu.Unlock()
		return &httpcachev1beta.EnsureHttpCacheResponse{
			Password: fmt.Sprintf("super-secret-http-pass-dyn-%d-888", c),
		}, nil
	}

	iamv1betagrpc.RegisterTokenServiceServer(grpcSrv, mockSrv.Token)
	iamv1betagrpc.RegisterTenantServiceServer(grpcSrv, mockSrv.Tenant)
	gradlev1betagrpc.RegisterGradleCacheServiceServer(grpcSrv, mockSrv.GradleCache)
	httpcachev1betagrpc.RegisterHttpCacheServiceServer(grpcSrv, mockSrv.HttpCache)
	sessionsv1betagrpc.RegisterUserSessionsServiceServer(grpcSrv, sessionsSrv)

	go func() {
		_ = grpcSrv.Serve(lis)
	}()

	return lis, func() {
		grpcSrv.Stop()
		_ = lis.Close()
	}
}

// TestAdversarial_R8_GRPCDebug_100Goroutines_ExhaustiveSecretRedaction
// Launches 100 concurrent workers issuing thousands of requests emitting:
// - Nested malicious JSON
// - Raw PEM keys (RSA, PKCS#8, OpenSSH, DSA, EC)
// - Certificates
// - Session tokens (st_..., tok_..., nsct_..., nscw_..., nscu_...)
// - Plaintext passwords (quoted and unquoted, CACHE_PASSWORD env format)
// - JWTs and Bearer tokens
// Asserts that 0 unredacted secrets leak into log output and all redactions are applied safely under -race.
func TestAdversarial_R8_GRPCDebug_100Goroutines_ExhaustiveSecretRedaction(t *testing.T) {
	lis, cleanup := setupAdversarialServer(t)
	defer cleanup()

	logBuf := &safeBuffer{}
	client.DebugWriterMutex.Lock()
	client.DebugWriter = logBuf
	client.DebugShowResponses = true
	client.DebugShowRequests = true
	client.DebugWriterMutex.Unlock()

	t.Cleanup(func() {
		client.DebugWriterMutex.Lock()
		client.DebugWriter = nil
		client.DebugShowResponses = false
		client.DebugShowRequests = false
		client.DebugWriterMutex.Unlock()
	})

	t.Setenv("NS_GRPC_DEBUG", "1")
	t.Setenv("NSC_GRPC_DEBUG_RESPONSES", "1")
	t.Setenv("NSC_GRPC_DEBUG_REQUESTS", "1")

	conn := dialTestConn(t, lis)
	ctx := context.Background()

	const (
		numWorkers = 100
		iterations = 35
	)

	var secretsMu sync.Mutex
	generatedSecrets := make([]string, 0, numWorkers*iterations*3)

	var wg sync.WaitGroup
	errCh := make(chan error, numWorkers*iterations)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			tokenCli := iamv1betagrpc.NewTokenServiceClient(conn)
			tenantCli := iamv1betagrpc.NewTenantServiceClient(conn)
			gradleCli := gradlev1betagrpc.NewGradleCacheServiceClient(conn)
			httpCli := httpcachev1betagrpc.NewHttpCacheServiceClient(conn)
			sessionCli := sessionsv1betagrpc.NewUserSessionsServiceClient(conn)

			for i := 0; i < iterations; i++ {
				tag := fmt.Sprintf("w%d_i%d", workerID, i)
				switch i % 7 {
				case 0:
					// TokenService.IssueTenantToken (nsct_ + JWT)
					reqToken := fmt.Sprintf("req_actor_token_nsct_%s", tag)
					secretsMu.Lock()
					generatedSecrets = append(generatedSecrets, reqToken)
					secretsMu.Unlock()

					res, err := tokenCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{
						TenantId: fmt.Sprintf("tenant_%s", tag),
					})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: IssueTenantToken failed: %w", workerID, i, err)
						return
					}
					secretsMu.Lock()
					generatedSecrets = append(generatedSecrets, res.BearerToken)
					secretsMu.Unlock()

				case 1:
					// TokenService.IssueIdToken (raw JWT)
					res, err := tokenCli.IssueIdToken(ctx, &iamv1beta.IssueIdTokenRequest{
						Audience: fmt.Sprintf("https://api.namespace.so/aud_%s", tag),
					})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: IssueIdToken failed: %w", workerID, i, err)
						return
					}
					secretsMu.Lock()
					generatedSecrets = append(generatedSecrets, res.IdToken)
					secretsMu.Unlock()

				case 2:
					// TenantService.IssueTenantClientCertificate (PKCS#8 Private Key & Cert)
					res, err := tenantCli.IssueTenantClientCertificate(ctx, &iamv1beta.IssueTenantClientCertificateRequest{
						TenantId: fmt.Sprintf("tenant_cert_%s", tag),
					})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: IssueTenantClientCertificate failed: %w", workerID, i, err)
						return
					}
					secretsMu.Lock()
					generatedSecrets = append(generatedSecrets, res.PrivateKeyPem, res.ClientCertificatePem)
					secretsMu.Unlock()

				case 3:
					// GradleCacheService.EnsureGradleCache (Password)
					res, err := gradleCli.EnsureGradleCache(ctx, &gradlev1beta.EnsureGradleCacheRequest{})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: EnsureGradleCache failed: %w", workerID, i, err)
						return
					}
					secretsMu.Lock()
					generatedSecrets = append(generatedSecrets, res.Password)
					secretsMu.Unlock()

				case 4:
					// HttpCacheService.EnsureHttpCache (Password)
					res, err := httpCli.EnsureHttpCache(ctx, &httpcachev1beta.EnsureHttpCacheRequest{})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: EnsureHttpCache failed: %w", workerID, i, err)
						return
					}
					secretsMu.Lock()
					generatedSecrets = append(generatedSecrets, res.Password)
					secretsMu.Unlock()

				case 5:
					// SessionToken error path with sensitive tokens and DSA key
					_, err := sessionCli.IssueTenantTokenFromSession(ctx, &sessions.IssueTenantTokenFromSessionRequest{
						TokenDuration: durationpb.New(777 * time.Second),
					})
					if err == nil {
						errCh <- fmt.Errorf("worker %d iter %d: expected error on duration 777", workerID, i)
						return
					}

				case 6:
					// UserSessionsService.IssueTenantTokenFromSession success path (tok_ token)
					res, err := sessionCli.IssueTenantTokenFromSession(ctx, &sessions.IssueTenantTokenFromSessionRequest{})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: IssueTenantTokenFromSession failed: %w", workerID, i, err)
						return
					}
					secretsMu.Lock()
					generatedSecrets = append(generatedSecrets, res.TenantToken)
					secretsMu.Unlock()
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Adversarial concurrent stress error: %v", err)
	}

	capturedLogs := logBuf.String()

	// High-risk raw static strings that must NEVER leak
	staticForbidden := []string{
		mockRSAPrivateKey,
		mockPKCS8PrivateKey,
		mockOpenSSHPrivateKey,
		mockDSAPrivateKey,
		testutil.PEMBeginHeader("RSA PRIVATE KEY"),
		testutil.PEMBeginHeader("PRIVATE KEY"),
		testutil.PEMBeginHeader("OPENSSH PRIVATE KEY"),
		testutil.PEMBeginHeader("DSA PRIVATE KEY"),
		testutil.PEMBeginHeader("CERTIFICATE"),
		"st_" + "adversarial_session_token_12345",
		"leak_pass_777",
		"tok_" + "adversarial_tenant_token_session_exchange_555",
	}

	for _, forbidden := range staticForbidden {
		if strings.Contains(capturedLogs, forbidden) {
			t.Fatalf("ADVERSARIAL SECURITY BREACH: Debug log contains raw secret %q", forbidden)
		}
	}

	// Verify all dynamically generated passwords and tokens are redacted
	secretsMu.Lock()
	allSecrets := make([]string, len(generatedSecrets))
	copy(allSecrets, generatedSecrets)
	secretsMu.Unlock()

	for _, secret := range allSecrets {
		if secret == "" {
			continue
		}
		if strings.Contains(capturedLogs, secret) {
			t.Fatalf("ADVERSARIAL SECURITY BREACH: Dynamic secret leaked in log output: %q", secret)
		}
	}

	// Verify mandatory redaction markers are present
	requiredMarkers := []string{
		"[REDACTED TOKEN]",
		"[REDACTED JWT]",
		"[REDACTED PRIVATE KEY]",
		"[REDACTED CERTIFICATE]",
		"[REDACTED PASSWORD]",
		"CACHE_PASSWORD=[REDACTED]",
	}

	for _, m := range requiredMarkers {
		if !strings.Contains(capturedLogs, m) {
			t.Errorf("Missing expected redaction marker %q in logs", m)
		}
	}
}

// TestAdversarial_R8_GRPCDebug_NestedMaliciousJSONStreamAndUnary
// Adversarially feeds deeply nested JSON payloads and weird whitespace to unary/stream interceptors.
func TestAdversarial_R8_GRPCDebug_NestedMaliciousJSONStreamAndUnary(t *testing.T) {
	logBuf := &safeBuffer{}
	syncWriter := client.NewSynchronizedRedactingWriter(logBuf)

	unaryInterceptor := client.RedactingUnaryInterceptor(syncWriter, true, true, "https://mock.namespaceapis.com:443")
	streamInterceptor := client.RedactingStreamInterceptor(syncWriter, true, true, "https://mock.namespaceapis.com:443")

	// Complex nested JSON with various secret patterns
	type nestedPayload struct {
		Auth     map[string]any    `json:"auth"`
		Env      string            `json:"env"`
		Keys     []string          `json:"keys"`
		Password string            `json:"password"`
		PassMap  map[string]string `json:"passwords"`
	}

	maliciousData := nestedPayload{
		Auth: map[string]any{
			"bearer":  "Authorization: Bearer " + "nsct_" + "super_secret_nested_token_1234567890",
			"jwt":     "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIiwidGVuYW50X2lkIjoidGVuYW50LTEyMyJ9" + "." + "40u10d0-pZ2c52n9Q_1234567890abcdef",
			"session": "st_" + "secret_session_token_deeply_nested_999",
		},
		Env: "NSC_TOKEN=" + "super_secret_env_token_value_999 CACHE_PASSWORD=secret_cache_env_password_888",
		Keys: []string{
			mockOpenSSHPrivateKey,
			mockRSAPrivateKey,
		},
		Password: "top_level_secret_password_111",
		PassMap: map[string]string{
			"password": "nested_secret_password_222",
		},
	}

	// 1. Test Unary Interceptor with malicious request and reply
	ctx := context.Background()
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		if r, ok := reply.(*nestedPayload); ok {
			*r = maliciousData
		}
		return nil
	}

	var reply nestedPayload
	err := unaryInterceptor(ctx, "/test.Service/UnaryCall", &maliciousData, &reply, nil, invoker)
	if err != nil {
		t.Fatalf("unaryInterceptor failed: %v", err)
	}

	// 2. Test Stream Interceptor
	mockStream := &mockClientStream{
		sendMsgFunc: func(m any) error { return nil },
		recvMsgFunc: func(m any) error {
			if r, ok := m.(*nestedPayload); ok {
				*r = maliciousData
			}
			return nil
		},
	}

	streamer := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return mockStream, nil
	}

	cs, err := streamInterceptor(ctx, &grpc.StreamDesc{}, nil, "/test.Service/StreamCall", streamer)
	if err != nil {
		t.Fatalf("streamInterceptor failed: %v", err)
	}

	_ = cs.SendMsg(&maliciousData)
	var streamRecv nestedPayload
	_ = cs.RecvMsg(&streamRecv)

	captured := logBuf.String()

	forbiddenStrings := []string{
		"nsct_" + "super_secret_nested_token_1234567890",
		"st_" + "secret_session_token_deeply_nested_999",
		"super_secret_env_token_value_999",
		"secret_cache_env_password_888",
		mockOpenSSHPrivateKey,
		mockRSAPrivateKey,
		"top_level_secret_password_111",
		"nested_secret_password_222",
	}

	for _, str := range forbiddenStrings {
		if strings.Contains(captured, str) {
			t.Fatalf("ADVERSARIAL SECURITY BREACH: Nested malicious JSON leaked secret %q in logs:\n%s", str, captured)
		}
	}
}

// TestAdversarial_R8_GRPCDebug_StderrOSPipe_100Goroutines
// Exercises defaultStderrWriter redirection to live OS pipe with 100 concurrent workers.
func TestAdversarial_R8_GRPCDebug_StderrOSPipe_100Goroutines(t *testing.T) {
	lis, cleanup := setupAdversarialServer(t)
	defer cleanup()

	t.Setenv("NS_GRPC_DEBUG", "1")
	t.Setenv("NSC_GRPC_DEBUG_RESPONSES", "1")

	// Ensure DebugWriter is nil to force defaultStderrWriter -> os.Stderr path
	client.DebugWriterMutex.Lock()
	client.DebugWriter = nil
	client.DebugShowResponses = true
	client.DebugShowRequests = false
	client.DebugWriterMutex.Unlock()

	conn := dialTestConn(t, lis)
	ctx := context.Background()

	// Capture stderr via OS Pipe
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create os.Pipe: %v", err)
	}
	os.Stderr = w

	outCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		outCh <- buf.String()
	}()

	const (
		numWorkers = 100
		iterations = 10
	)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			tenantCli := iamv1betagrpc.NewTenantServiceClient(conn)
			gradleCli := gradlev1betagrpc.NewGradleCacheServiceClient(conn)

			for j := 0; j < iterations; j++ {
				_, _ = tenantCli.IssueTenantClientCertificate(ctx, &iamv1beta.IssueTenantClientCertificateRequest{})
				_, _ = gradleCli.EnsureGradleCache(ctx, &gradlev1beta.EnsureGradleCacheRequest{})
			}
		}(i)
	}

	wg.Wait()
	_ = w.Close()
	os.Stderr = oldStderr

	capturedStderr := <-outCh
	_ = r.Close()

	if strings.Contains(capturedStderr, testutil.PEMBeginHeader("PRIVATE KEY")) {
		t.Fatalf("ADVERSARIAL SECURITY BREACH: Real os.Stderr pipe contains unredacted PKCS#8 key")
	}
	if strings.Contains(capturedStderr, "super-secret-gradle-pass-") {
		t.Fatalf("ADVERSARIAL SECURITY BREACH: Real os.Stderr pipe contains unredacted gradle cache password")
	}

	if !strings.Contains(capturedStderr, "[REDACTED PRIVATE KEY]") {
		t.Fatalf("Expected '[REDACTED PRIVATE KEY]' in captured stderr")
	}
	if !strings.Contains(capturedStderr, "[REDACTED PASSWORD]") {
		t.Fatalf("Expected '[REDACTED PASSWORD]' in captured stderr")
	}
}
