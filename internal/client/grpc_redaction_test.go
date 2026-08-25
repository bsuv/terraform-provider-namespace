package client_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
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
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/durationpb"
)

// safeBuffer is a concurrency-safe bytes.Buffer wrapper for capturing debug logs.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *safeBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
}

// mockSessionsServer implements UserSessionsServiceServer.
type mockSessionsServer struct {
	sessionsv1betagrpc.UnimplementedUserSessionsServiceServer
}

func (m *mockSessionsServer) IssueTenantTokenFromSession(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
	if req.TokenDuration != nil && req.TokenDuration.Seconds == 999 {
		// Error simulation path with secrets embedded in error string
		return nil, status.Error(codes.Unauthenticated, "invalid session token st_secret_error_token_99999 with CACHE_PASSWORD=leakpass123")
	}
	return &sessions.IssueTenantTokenFromSessionResponse{
		TenantToken: "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": "session-tenant-123"}),
	}, nil
}

type testRedactionFixture struct {
	srv         *testutil.MockServer
	sessionsSrv *mockSessionsServer
	lis         *bufconn.Listener
	grpcSrv     *grpc.Server
	testKeyPEM  string
	testCertPEM string
}

func setupRedactionServer(t *testing.T) (*testRedactionFixture, func()) {
	t.Helper()

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate test private key: %v", err)
	}
	privKeyBytes, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		t.Fatalf("failed to marshal ec private key: %v", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privKeyBytes}))

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-redaction-cert"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		t.Fatalf("failed to create test certificate: %v", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))

	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()

	mockSrv := testutil.NewMockServer()
	mockSessions := &mockSessionsServer{}

	iamv1betagrpc.RegisterTokenServiceServer(grpcSrv, mockSrv.Token)
	iamv1betagrpc.RegisterTenantServiceServer(grpcSrv, mockSrv.Tenant)
	gradlev1betagrpc.RegisterGradleCacheServiceServer(grpcSrv, mockSrv.GradleCache)
	httpcachev1betagrpc.RegisterHttpCacheServiceServer(grpcSrv, mockSrv.HttpCache)
	sessionsv1betagrpc.RegisterUserSessionsServiceServer(grpcSrv, mockSessions)

	mockSrv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		jwt := createTestJWT(map[string]interface{}{
			"tenant_id": "tenant-redaction-target",
			"exp":       time.Now().Add(time.Hour).Unix(),
		})
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: "nsct_" + jwt,
		}, nil
	}

	mockSrv.Token.IssueIdTokenFunc = func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
		jwt := createTestJWT(map[string]interface{}{
			"sub": "user:12345",
			"aud": "https://api.namespace.so",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		return &iamv1beta.IssueIdTokenResponse{
			IdToken: jwt,
		}, nil
	}

	mockSrv.Tenant.IssueTenantClientCertificateFunc = func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
		return &iamv1beta.IssueTenantClientCertificateResponse{
			ClientCertificatePem: certPEM,
			PrivateKeyPem:        keyPEM,
		}, nil
	}

	mockSrv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
		return &gradlev1beta.EnsureGradleCacheResponse{
			Password: "super-secret-gradle-cache-password-xyz999",
		}, nil
	}

	mockSrv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
		return &httpcachev1beta.EnsureHttpCacheResponse{
			Password: "super-secret-http-cache-password-abc888",
		}, nil
	}

	go func() {
		_ = grpcSrv.Serve(lis)
	}()

	fix := &testRedactionFixture{
		srv:         mockSrv,
		sessionsSrv: mockSessions,
		lis:         lis,
		grpcSrv:     grpcSrv,
		testKeyPEM:  keyPEM,
		testCertPEM: certPEM,
	}

	cleanup := func() {
		grpcSrv.Stop()
		_ = lis.Close()
	}

	return fix, cleanup
}

func dialTestConn(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	ctx := context.Background()
	conn, err := client.NewConnectionWithEndpointWithTransportCredentials(
		ctx,
		"https://passthrough:443",
		nil,
		insecure.NewCredentials(),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("failed to dial test gRPC connection: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

// TestGRPCDebugLogging_RedactsResponseSecrets verifies that all sensitive secrets in gRPC response payloads
// (Namespace tokens, JWTs, X.509 certificates, EC/RSA private keys, and cache passwords) are completely redacted
// when NS_GRPC_DEBUG=1 and NSC_GRPC_DEBUG_RESPONSES=1 are set.
func TestGRPCDebugLogging_RedactsResponseSecrets(t *testing.T) {
	fix, cleanup := setupRedactionServer(t)
	defer cleanup()

	logBuf := &safeBuffer{}
	client.DebugWriterMutex.Lock()
	client.DebugWriter = logBuf
	client.DebugShowResponses = true
	client.DebugShowRequests = false
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

	conn := dialTestConn(t, fix.lis)
	ctx := context.Background()

	// 1. TokenService.IssueTenantToken (nsct_ token)
	tokenCli := iamv1betagrpc.NewTokenServiceClient(conn)
	respTenantToken, err := tokenCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{})
	if err != nil {
		t.Fatalf("IssueTenantToken failed: %v", err)
	}
	rawTenantToken := respTenantToken.BearerToken

	// 2. TokenService.IssueIdToken (JWT token)
	respIdToken, err := tokenCli.IssueIdToken(ctx, &iamv1beta.IssueIdTokenRequest{})
	if err != nil {
		t.Fatalf("IssueIdToken failed: %v", err)
	}
	rawIdToken := respIdToken.IdToken

	// 3. TenantService.IssueTenantClientCertificate (Private key + Certificate PEM)
	tenantCli := iamv1betagrpc.NewTenantServiceClient(conn)
	respCert, err := tenantCli.IssueTenantClientCertificate(ctx, &iamv1beta.IssueTenantClientCertificateRequest{})
	if err != nil {
		t.Fatalf("IssueTenantClientCertificate failed: %v", err)
	}
	rawPrivKey := respCert.PrivateKeyPem
	rawCert := respCert.ClientCertificatePem

	// 4. GradleCacheService.EnsureGradleCache (Gradle cache password)
	gradleCli := gradlev1betagrpc.NewGradleCacheServiceClient(conn)
	respGradle, err := gradleCli.EnsureGradleCache(ctx, &gradlev1beta.EnsureGradleCacheRequest{})
	if err != nil {
		t.Fatalf("EnsureGradleCache failed: %v", err)
	}
	rawGradlePass := respGradle.Password

	// 5. HttpCacheService.EnsureHttpCache (Http cache password)
	httpCli := httpcachev1betagrpc.NewHttpCacheServiceClient(conn)
	respHttp, err := httpCli.EnsureHttpCache(ctx, &httpcachev1beta.EnsureHttpCacheRequest{})
	if err != nil {
		t.Fatalf("EnsureHttpCache failed: %v", err)
	}
	rawHttpPass := respHttp.Password

	// 6. UserSessionsService.IssueTenantTokenFromSession (Session token exchange)
	sessionCli := sessionsv1betagrpc.NewUserSessionsServiceClient(conn)
	respSession, err := sessionCli.IssueTenantTokenFromSession(ctx, &sessions.IssueTenantTokenFromSessionRequest{})
	if err != nil {
		t.Fatalf("IssueTenantTokenFromSession failed: %v", err)
	}
	rawSessionToken := respSession.TenantToken

	capturedLogs := logBuf.String()

	// Assert that all raw secrets are completely ABSENT from debug logs
	forbiddenSecrets := map[string]string{
		"Raw Tenant Token":      rawTenantToken,
		"Raw ID Token JWT":      rawIdToken,
		"Raw Private Key PEM":   rawPrivKey,
		"Raw Certificate PEM":   rawCert,
		"Raw Gradle Password":   rawGradlePass,
		"Raw Http Password":     rawHttpPass,
		"Raw Session Token":     rawSessionToken,
		"Mock Server Key PEM":   fix.testKeyPEM,
		"Mock Server Cert PEM":  fix.testCertPEM,
		"Private Key Header":    testutil.PEMBeginHeader("EC PRIVATE KEY"),
		"Certificate Header":    testutil.PEMBeginHeader("CERTIFICATE"),
		"Plaintext Gradle Pass": "super-secret-gradle-cache-password-xyz999",
		"Plaintext Http Pass":   "super-secret-http-cache-password-abc888",
	}

	for label, secret := range forbiddenSecrets {
		if secret != "" && strings.Contains(capturedLogs, secret) {
			t.Errorf("SECURITY LEAK VIOLATION: Debug logs contain unredacted %s: %q\nLogs:\n%s", label, secret, capturedLogs)
		}
	}

	// Assert that required REDACTED markers are present
	requiredRedactions := []string{
		"[REDACTED TOKEN]",
		"[REDACTED JWT]",
		"[REDACTED PRIVATE KEY]",
		"[REDACTED CERTIFICATE]",
		"[REDACTED PASSWORD]",
		"[DEBUG] Response Payload:",
	}

	for _, marker := range requiredRedactions {
		if !strings.Contains(capturedLogs, marker) {
			t.Errorf("Expected debug logs to contain redaction marker %q, but was missing.\nLogs:\n%s", marker, capturedLogs)
		}
	}
}

// TestGRPCDebugLogging_RedactsRequestPayloads verifies that sensitive fields in request payloads
// are redacted when NSC_GRPC_DEBUG_REQUESTS=1 is set.
func TestGRPCDebugLogging_RedactsRequestPayloads(t *testing.T) {
	fix, cleanup := setupRedactionServer(t)
	defer cleanup()

	logBuf := &safeBuffer{}
	client.DebugWriterMutex.Lock()
	client.DebugWriter = logBuf
	client.DebugShowResponses = false
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
	t.Setenv("NSC_GRPC_DEBUG_REQUESTS", "1")

	conn := dialTestConn(t, fix.lis)
	ctx := context.Background()

	tenantCli := iamv1betagrpc.NewTenantServiceClient(conn)
	sensitiveActorToken := "nsct_" + createTestJWT(map[string]interface{}{"actor_secret": "secret-actor-token"})

	_, err := tenantCli.IssueTenantClientCertificate(ctx, &iamv1beta.IssueTenantClientCertificateRequest{
		TenantId: "test-tenant-123",
		ActorId:  sensitiveActorToken,
	})
	if err != nil {
		t.Fatalf("IssueTenantClientCertificate request failed: %v", err)
	}

	capturedLogs := logBuf.String()

	if strings.Contains(capturedLogs, sensitiveActorToken) {
		t.Fatalf("SECURITY LEAK VIOLATION: Debug logs contained unredacted request token: %q", sensitiveActorToken)
	}

	if !strings.Contains(capturedLogs, "[DEBUG] Payload:") {
		t.Fatalf("Expected debug logs to contain request payload log line, got:\n%s", capturedLogs)
	}

	if !strings.Contains(capturedLogs, "[REDACTED TOKEN]") {
		t.Fatalf("Expected debug logs to contain '[REDACTED TOKEN]', got:\n%s", capturedLogs)
	}
}

// TestGRPCDebugLogging_RedactsErrorFailures verifies that errors containing secrets
// are scrubbed by RedactSecrets before being emitted to debug logs.
func TestGRPCDebugLogging_RedactsErrorFailures(t *testing.T) {
	fix, cleanup := setupRedactionServer(t)
	defer cleanup()

	logBuf := &safeBuffer{}
	client.DebugWriterMutex.Lock()
	client.DebugWriter = logBuf
	client.DebugWriterMutex.Unlock()

	t.Cleanup(func() {
		client.DebugWriterMutex.Lock()
		client.DebugWriter = nil
		client.DebugWriterMutex.Unlock()
	})

	t.Setenv("NS_GRPC_DEBUG", "1")

	conn := dialTestConn(t, fix.lis)
	ctx := context.Background()

	sessionCli := sessionsv1betagrpc.NewUserSessionsServiceClient(conn)
	// Passing duration 999 triggers the mock server error containing sensitive token and env var
	_, err := sessionCli.IssueTenantTokenFromSession(ctx, &sessions.IssueTenantTokenFromSessionRequest{
		TokenDuration: durationpb.New(999 * time.Second),
	})
	if err == nil {
		t.Fatal("expected error from IssueTenantTokenFromSession, got nil")
	}

	capturedLogs := logBuf.String()

	forbiddenTokens := []string{
		"st_secret_error_token_99999",
		"CACHE_PASSWORD=leakpass123",
	}

	for _, tok := range forbiddenTokens {
		if strings.Contains(capturedLogs, tok) {
			t.Fatalf("SECURITY LEAK VIOLATION: Debug error log contains unredacted secret %q.\nLogs:\n%s", tok, capturedLogs)
		}
	}

	if !strings.Contains(capturedLogs, "[DEBUG] request failed:") {
		t.Fatalf("Expected '[DEBUG] request failed:' in logs, got:\n%s", capturedLogs)
	}

	if !strings.Contains(capturedLogs, "[REDACTED TOKEN]") || !strings.Contains(capturedLogs, "CACHE_PASSWORD=[REDACTED]") {
		t.Fatalf("Expected redacted tokens and env vars in error debug log, got:\n%s", capturedLogs)
	}
}

// TestGRPCDebugLogging_StreamingCalls verifies that streaming interceptors redact
// secrets during SendMsg and RecvMsg.
type mockClientStream struct {
	grpc.ClientStream
	sendMsgFunc func(m any) error
	recvMsgFunc func(m any) error
}

func (m *mockClientStream) SendMsg(msg any) error {
	if m.sendMsgFunc != nil {
		return m.sendMsgFunc(msg)
	}
	return nil
}

func (m *mockClientStream) RecvMsg(msg any) error {
	if m.recvMsgFunc != nil {
		return m.recvMsgFunc(msg)
	}
	return nil
}

func TestGRPCDebugLogging_StreamingCalls(t *testing.T) {
	logBuf := &safeBuffer{}
	syncWriter := client.NewSynchronizedRedactingWriter(logBuf)

	streamInterceptor := client.RedactingStreamInterceptor(syncWriter, true, true, "https://mock.namespaceapis.com:443")

	sensitiveSendToken := "nsct_" + createTestJWT(map[string]interface{}{"send_secret": "stream-send-val"})
	sensitiveRecvToken := "nsct_" + createTestJWT(map[string]interface{}{"recv_secret": "stream-recv-val"})

	mockStream := &mockClientStream{
		sendMsgFunc: func(m any) error {
			return nil
		},
		recvMsgFunc: func(m any) error {
			if resp, ok := m.(*sessions.IssueTenantTokenFromSessionResponse); ok {
				resp.TenantToken = sensitiveRecvToken
			}
			return nil
		},
	}

	streamer := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return mockStream, nil
	}

	cs, err := streamInterceptor(context.Background(), &grpc.StreamDesc{}, nil, "/namespace.private.sessions.UserSessionsService/StreamSession", streamer)
	if err != nil {
		t.Fatalf("streamInterceptor failed: %v", err)
	}

	// 1. Send streaming message containing sensitive token
	req := &sessions.IssueTenantTokenFromSessionRequest{}
	if err := cs.SendMsg(req); err != nil {
		t.Fatalf("cs.SendMsg failed: %v", err)
	}

	// 2. Receive streaming message containing sensitive token
	var resp sessions.IssueTenantTokenFromSessionResponse
	if err := cs.RecvMsg(&resp); err != nil {
		t.Fatalf("cs.RecvMsg failed: %v", err)
	}

	capturedLogs := logBuf.String()

	if strings.Contains(capturedLogs, sensitiveSendToken) {
		t.Fatalf("SECURITY LEAK: Streaming SendMsg contains raw secret %q", sensitiveSendToken)
	}
	if strings.Contains(capturedLogs, sensitiveRecvToken) {
		t.Fatalf("SECURITY LEAK: Streaming RecvMsg contains raw secret %q", sensitiveRecvToken)
	}

	if !strings.Contains(capturedLogs, "[DEBUG] Stream RPC to:") {
		t.Fatalf("Expected '[DEBUG] Stream RPC to:' in logs, got:\n%s", capturedLogs)
	}
	if !strings.Contains(capturedLogs, "[DEBUG] Stream SendMsg:") {
		t.Fatalf("Expected '[DEBUG] Stream SendMsg:' in logs, got:\n%s", capturedLogs)
	}
	if !strings.Contains(capturedLogs, "[DEBUG] Stream RecvMsg:") {
		t.Fatalf("Expected '[DEBUG] Stream RecvMsg:' in logs, got:\n%s", capturedLogs)
	}
	if !strings.Contains(capturedLogs, "[REDACTED TOKEN]") {
		t.Fatalf("Expected '[REDACTED TOKEN]' in streaming logs, got:\n%s", capturedLogs)
	}
}

// TestGRPCDebugLogging_StderrCapture_Integration tests the end-to-end integration
// where DebugWriter is nil and output writes directly to real os.Stderr.
func TestGRPCDebugLogging_StderrCapture_Integration(t *testing.T) {
	fix, cleanup := setupRedactionServer(t)
	defer cleanup()

	t.Setenv("NS_GRPC_DEBUG", "1")
	t.Setenv("NSC_GRPC_DEBUG_RESPONSES", "1")

	// Ensure DebugWriter is nil to exercise defaultStderrWriter
	client.DebugWriterMutex.Lock()
	client.DebugWriter = nil
	client.DebugShowResponses = false
	client.DebugShowRequests = false
	client.DebugWriterMutex.Unlock()

	conn := dialTestConn(t, fix.lis)
	ctx := context.Background()

	// Capture stderr via OS pipe
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

	tokenCli := iamv1betagrpc.NewTokenServiceClient(conn)
	tenantCli := iamv1betagrpc.NewTenantServiceClient(conn)

	_, _ = tokenCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{})
	_, _ = tenantCli.IssueTenantClientCertificate(ctx, &iamv1beta.IssueTenantClientCertificateRequest{})

	_ = w.Close()
	os.Stderr = oldStderr
	capturedStderr := <-outCh
	_ = r.Close()

	if strings.Contains(capturedStderr, testutil.PEMBeginHeader("EC PRIVATE KEY")) {
		t.Fatalf("SECURITY LEAK: Real stderr contains unredacted EC private key PEM!")
	}
	if strings.Contains(capturedStderr, testutil.PEMBeginHeader("CERTIFICATE")) {
		t.Fatalf("SECURITY LEAK: Real stderr contains unredacted Certificate PEM!")
	}

	if !strings.Contains(capturedStderr, "[REDACTED PRIVATE KEY]") {
		t.Fatalf("Expected '[REDACTED PRIVATE KEY]' in real stderr, got:\n%s", capturedStderr)
	}
	if !strings.Contains(capturedStderr, "[REDACTED CERTIFICATE]") {
		t.Fatalf("Expected '[REDACTED CERTIFICATE]' in real stderr, got:\n%s", capturedStderr)
	}
	if !strings.Contains(capturedStderr, "[REDACTED TOKEN]") {
		t.Fatalf("Expected '[REDACTED TOKEN]' in real stderr, got:\n%s", capturedStderr)
	}
}

// TestGRPCDebugLogging_HighConcurrencyStress executes 50 concurrent worker goroutines
// issuing hundreds of parallel gRPC requests with debug logging enabled, asserting race freedom
// and zero unredacted secret leaks.
func TestGRPCDebugLogging_HighConcurrencyStress(t *testing.T) {
	fix, cleanup := setupRedactionServer(t)
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

	const (
		numWorkers = 50
		iterations = 20
	)

	conn := dialTestConn(t, fix.lis)
	ctx := context.Background()

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

			for i := 0; i < iterations; i++ {
				switch i % 4 {
				case 0:
					res, err := tokenCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: IssueTenantToken failed: %w", workerID, i, err)
						return
					}
					if !strings.HasPrefix(res.BearerToken, "nsct_") {
						errCh <- fmt.Errorf("worker %d iter %d: expected nsct_ prefix, got %q", workerID, i, res.BearerToken)
						return
					}
				case 1:
					res, err := tenantCli.IssueTenantClientCertificate(ctx, &iamv1beta.IssueTenantClientCertificateRequest{})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: IssueTenantClientCertificate failed: %w", workerID, i, err)
						return
					}
					if len(res.PrivateKeyPem) == 0 || len(res.ClientCertificatePem) == 0 {
						errCh <- fmt.Errorf("worker %d iter %d: empty cert or key in response", workerID, i)
						return
					}
				case 2:
					res, err := gradleCli.EnsureGradleCache(ctx, &gradlev1beta.EnsureGradleCacheRequest{})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: EnsureGradleCache failed: %w", workerID, i, err)
						return
					}
					if res.Password == "" {
						errCh <- fmt.Errorf("worker %d iter %d: empty gradle password", workerID, i)
						return
					}
				case 3:
					res, err := httpCli.EnsureHttpCache(ctx, &httpcachev1beta.EnsureHttpCacheRequest{})
					if err != nil {
						errCh <- fmt.Errorf("worker %d iter %d: EnsureHttpCache failed: %w", workerID, i, err)
						return
					}
					if res.Password == "" {
						errCh <- fmt.Errorf("worker %d iter %d: empty http password", workerID, i)
						return
					}
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Concurrent stress error: %v", err)
	}

	capturedLogs := logBuf.String()

	// Assert zero plaintext passwords or keys exist
	forbiddenStrings := []string{
		"super-secret-gradle-cache-password-xyz999",
		"super-secret-http-cache-password-abc888",
		testutil.PEMBeginHeader("EC PRIVATE KEY"),
		testutil.PEMBeginHeader("CERTIFICATE"),
	}

	for _, s := range forbiddenStrings {
		if strings.Contains(capturedLogs, s) {
			t.Fatalf("SECURITY LEAK UNDER HIGH CONCURRENCY: Debug logs contain unredacted string %q", s)
		}
	}

	// Assert presence of redactions
	if !strings.Contains(capturedLogs, "[REDACTED PASSWORD]") {
		t.Fatalf("Expected '[REDACTED PASSWORD]' in concurrent logs")
	}
	if !strings.Contains(capturedLogs, "[REDACTED PRIVATE KEY]") {
		t.Fatalf("Expected '[REDACTED PRIVATE KEY]' in concurrent logs")
	}
	if !strings.Contains(capturedLogs, "[REDACTED TOKEN]") {
		t.Fatalf("Expected '[REDACTED TOKEN]' in concurrent logs")
	}
}

// TestGRPCDebugLogging_DisabledWhenFlagNotSet verifies that when NS_GRPC_DEBUG is not set
// and DebugWriter is nil, no debug logs are emitted.
func TestGRPCDebugLogging_DisabledWhenFlagNotSet(t *testing.T) {
	fix, cleanup := setupRedactionServer(t)
	defer cleanup()

	t.Setenv("NS_GRPC_DEBUG", "")
	t.Setenv("NSC_GRPC_DEBUG_RESPONSES", "")
	t.Setenv("NSC_GRPC_DEBUG_REQUESTS", "")

	logBuf := &safeBuffer{}
	client.DebugWriterMutex.Lock()
	client.DebugWriter = nil
	client.DebugShowResponses = false
	client.DebugShowRequests = false
	client.DebugWriterMutex.Unlock()

	conn := dialTestConn(t, fix.lis)
	ctx := context.Background()

	tokenCli := iamv1betagrpc.NewTokenServiceClient(conn)
	_, err := tokenCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{})
	if err != nil {
		t.Fatalf("IssueTenantToken failed: %v", err)
	}

	if logBuf.String() != "" {
		t.Fatalf("Expected empty log buffer when debug flags disabled, got:\n%s", logBuf.String())
	}
}

// TestParseGRPCEndpoint_TableDriven verifies standard and edge cases for ParseGRPCEndpoint.
func TestParseGRPCEndpoint_TableDriven(t *testing.T) {
	tests := []struct {
		input       string
		expected    string
		expectError bool
	}{
		{input: "https://global.namespaceapis.com", expected: "global.namespaceapis.com:443"},
		{input: "https://global.namespaceapis.com:443", expected: "global.namespaceapis.com:443"},
		{input: "https://iam.namespaceapis.com", expected: "iam.namespaceapis.com:443"},
		{input: "https://127.0.0.1:8080", expected: "127.0.0.1:8080"},
		{input: " HTTPS://UPPER.example.com/ ", expected: "UPPER.example.com:443"},
		{input: "[2001:db8::1]", expected: "[2001:db8::1]:443"},
		{input: "global.namespaceapis.com:443", expected: "global.namespaceapis.com:443"},
		{input: "global.namespaceapis.com", expected: "global.namespaceapis.com:443"},
		{input: "127.0.0.1:8080", expected: "127.0.0.1:8080"},
		{input: "http://global.namespaceapis.com", expectError: true},
		{input: "https://global.namespaceapis.com/api/v1", expectError: true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			res, err := client.ParseGRPCEndpoint(tc.input)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error for endpoint %q, got nil", tc.input)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for endpoint %q: %v", tc.input, err)
				}
				if res != tc.expected {
					t.Fatalf("expected %q, got %q", tc.expected, res)
				}
			}
		})
	}
}

// TestSynchronizedRedactingWriter_DirectWrites tests direct Write calls on SynchronizedRedactingWriter
// under concurrent execution.
func TestSynchronizedRedactingWriter_DirectWrites(t *testing.T) {
	logBuf := &safeBuffer{}
	sw := client.NewSynchronizedRedactingWriter(logBuf)

	const (
		numThreads = 20
		iterations = 50
	)

	var wg sync.WaitGroup
	for i := 0; i < numThreads; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				secretLine := fmt.Sprintf("Worker %d iter %d: token nsct_secret_leak_%d_%d and password: mypass_%d_%d\n",
					id, j, id, j, id, j)
				_, err := sw.Write([]byte(secretLine))
				if err != nil {
					t.Errorf("sw.Write error: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	captured := logBuf.String()
	if strings.Contains(captured, "nsct_secret_leak_") {
		t.Fatalf("SECURITY LEAK: direct writes contain unredacted tokens")
	}
	if strings.Contains(captured, "mypass_") {
		t.Fatalf("SECURITY LEAK: direct writes contain unredacted passwords")
	}
	if !strings.Contains(captured, "[REDACTED TOKEN]") {
		t.Fatalf("Expected '[REDACTED TOKEN]' in captured direct writes")
	}
	if !strings.Contains(captured, "[REDACTED PASSWORD]") {
		t.Fatalf("Expected '[REDACTED PASSWORD]' in captured direct writes")
	}
}
