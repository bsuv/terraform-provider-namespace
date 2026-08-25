package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/private/sessions/sessionsv1betagrpc"
	sessions "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/sessions"
	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

var nextMockIAMPort atomic.Int32

func init() {
	nextMockIAMPort.Store(10000)
}

// mockIAMServer is an in-process gRPC test server implementing UserSessionsService.
type mockIAMServer struct {
	sessionsv1betagrpc.UnimplementedUserSessionsServiceServer

	serverID    string
	port        int32
	listener    *bufconn.Listener
	grpcServer  *grpc.Server
	requestCt   atomic.Int64
	mu          sync.Mutex
	authHeaders []string
	requests    []*sessions.IssueTenantTokenFromSessionRequest
}

func startMockIAMServer(t *testing.T, serverID string) *mockIAMServer {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)

	grpcServer := grpc.NewServer()
	mock := &mockIAMServer{
		serverID:   serverID,
		port:       nextMockIAMPort.Add(1),
		listener:   lis,
		grpcServer: grpcServer,
	}

	sessionsv1betagrpc.RegisterUserSessionsServiceServer(grpcServer, mock)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	t.Cleanup(func() {
		grpcServer.Stop()
		_ = lis.Close()
	})

	return mock
}

func (m *mockIAMServer) Endpoint() string {
	return fmt.Sprintf("https://127.0.0.1:%d", m.port)
}

func (m *mockIAMServer) DialOption() grpc.DialOption {
	return grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return m.listener.Dial()
	})
}

func (m *mockIAMServer) IssueTenantTokenFromSession(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
	m.requestCt.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()

	md, _ := metadata.FromIncomingContext(ctx)
	var authHeader string
	if vals := md.Get("authorization"); len(vals) > 0 {
		authHeader = vals[0]
	}
	m.authHeaders = append(m.authHeaders, authHeader)
	m.requests = append(m.requests, req)

	// Issue a verifiable tenant token embedding the mock server's ID
	jwt := createTestJWT(map[string]interface{}{
		"tenant_id": fmt.Sprintf("tenant-from-%s", m.serverID),
		"exp":       time.Now().Add(time.Hour).Unix(),
	})

	return &sessions.IssueTenantTokenFromSessionResponse{
		TenantToken: "nsct_" + jwt,
	}, nil
}

func (m *mockIAMServer) RequestCount() int64 {
	return m.requestCt.Load()
}

// TestIAMIsolation_NoEnvironmentMutation verifies that client creation and token resolution
// never write to or mutate os.Getenv("NSC_IAM_ENDPOINT").
func TestIAMIsolation_NoEnvironmentMutation(t *testing.T) {
	ctx := context.Background()
	const sentinelEnv = "https://iam.initial-sentinel.namespaceapis.com"
	t.Setenv("NSC_IAM_ENDPOINT", sentinelEnv)

	serverA := startMockIAMServer(t, "server-A")
	serverB := startMockIAMServer(t, "server-B")

	// 1. Create client targeting Server A
	cliA, err := client.New(ctx, client.Config{
		Token:       "nsct_dummy_token",
		IAMEndpoint: serverA.Endpoint(),
		Creds:       insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{serverA.DialOption()},
	})
	if err != nil {
		t.Fatalf("failed to create client A: %v", err)
	}
	defer func() { _ = cliA.Close() }()

	if os.Getenv("NSC_IAM_ENDPOINT") != sentinelEnv {
		t.Fatalf("VIOLATION: NSC_IAM_ENDPOINT was mutated by client A! Expected %q, got %q", sentinelEnv, os.Getenv("NSC_IAM_ENDPOINT"))
	}

	// 2. Create client targeting Server B
	cliB, err := client.New(ctx, client.Config{
		Token:       "nsct_dummy_token",
		IAMEndpoint: serverB.Endpoint(),
		Creds:       insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{serverB.DialOption()},
	})
	if err != nil {
		t.Fatalf("failed to create client B: %v", err)
	}
	defer func() { _ = cliB.Close() }()

	if os.Getenv("NSC_IAM_ENDPOINT") != sentinelEnv {
		t.Fatalf("VIOLATION: NSC_IAM_ENDPOINT was mutated by client B! Expected %q, got %q", sentinelEnv, os.Getenv("NSC_IAM_ENDPOINT"))
	}

	// 3. Resolve token sources with custom IAM endpoints
	_, err = client.ResolveTokenSource(ctx, client.AuthConfig{
		Token:       "explicit-token",
		IAMEndpoint: serverA.Endpoint(),
		DialOptions: []grpc.DialOption{serverA.DialOption()},
	})
	if err != nil {
		t.Fatalf("failed to resolve static token source: %v", err)
	}

	if os.Getenv("NSC_IAM_ENDPOINT") != sentinelEnv {
		t.Fatalf("VIOLATION: NSC_IAM_ENDPOINT was mutated during token resolution! Expected %q, got %q", sentinelEnv, os.Getenv("NSC_IAM_ENDPOINT"))
	}
}

// TestIAMIsolation_MultiProviderSessionTokenRouting verifies that SessionTokenSource
// routes exchanges directly to its configured IAM endpoint without cross-contamination.
func TestIAMIsolation_MultiProviderSessionTokenRouting(t *testing.T) {
	ctx := context.Background()
	t.Setenv("NSC_IAM_ENDPOINT", "https://iam.should-not-be-contacted.namespaceapis.com")

	serverAlpha := startMockIAMServer(t, "alpha")
	serverBeta := startMockIAMServer(t, "beta")
	serverGamma := startMockIAMServer(t, "gamma")

	stsAlpha := client.NewSessionTokenSource(
		"st_session_token_alpha",
		serverAlpha.Endpoint(),
		insecure.NewCredentials(),
		serverAlpha.DialOption(),
	)
	defer func() { _ = stsAlpha.Close() }()

	stsBeta := client.NewSessionTokenSource(
		"st_session_token_beta",
		serverBeta.Endpoint(),
		insecure.NewCredentials(),
		serverBeta.DialOption(),
	)
	defer func() { _ = stsBeta.Close() }()

	stsGamma := client.NewSessionTokenSource(
		"st_session_token_gamma",
		serverGamma.Endpoint(),
		insecure.NewCredentials(),
		serverGamma.DialOption(),
	)
	defer func() { _ = stsGamma.Close() }()

	// Issue token from Alpha
	tokA, err := stsAlpha.IssueToken(ctx, time.Minute, false)
	if err != nil {
		t.Fatalf("stsAlpha.IssueToken failed: %v", err)
	}
	tenantA, err := client.ResolveTenantFromToken(tokA)
	if err != nil {
		t.Fatalf("failed to resolve tenant from token A: %v", err)
	}
	if tenantA != "tenant-from-alpha" {
		t.Fatalf("expected tenant 'tenant-from-alpha', got %q", tenantA)
	}

	// Issue token from Beta
	tokB, err := stsBeta.IssueToken(ctx, time.Minute, false)
	if err != nil {
		t.Fatalf("stsBeta.IssueToken failed: %v", err)
	}
	tenantB, err := client.ResolveTenantFromToken(tokB)
	if err != nil {
		t.Fatalf("failed to resolve tenant from token B: %v", err)
	}
	if tenantB != "tenant-from-beta" {
		t.Fatalf("expected tenant 'tenant-from-beta', got %q", tenantB)
	}

	// Issue token from Gamma
	tokC, err := stsGamma.IssueToken(ctx, time.Minute, false)
	if err != nil {
		t.Fatalf("stsGamma.IssueToken failed: %v", err)
	}
	tenantC, err := client.ResolveTenantFromToken(tokC)
	if err != nil {
		t.Fatalf("failed to resolve tenant from token C: %v", err)
	}
	if tenantC != "tenant-from-gamma" {
		t.Fatalf("expected tenant 'tenant-from-gamma', got %q", tenantC)
	}

	// Assert server request counts: exactly 1 per server, 0 cross-talk
	if serverAlpha.RequestCount() != 1 {
		t.Errorf("serverAlpha expected 1 request, got %d", serverAlpha.RequestCount())
	}
	if serverBeta.RequestCount() != 1 {
		t.Errorf("serverBeta expected 1 request, got %d", serverBeta.RequestCount())
	}
	if serverGamma.RequestCount() != 1 {
		t.Errorf("serverGamma expected 1 request, got %d", serverGamma.RequestCount())
	}
}

// TestIAMIsolation_SessionTokenCachingAndForcedRenewal tests in-memory token caching and force-refresh.
func TestIAMIsolation_SessionTokenCachingAndForcedRenewal(t *testing.T) {
	ctx := context.Background()
	server := startMockIAMServer(t, "cache-test")

	sts := client.NewSessionTokenSource(
		"st_caching_session_token",
		server.Endpoint(),
		insecure.NewCredentials(),
		server.DialOption(),
	)
	defer func() { _ = sts.Close() }()

	// First call should hit the server
	tok1, err := sts.IssueToken(ctx, 5*time.Minute, false)
	if err != nil {
		t.Fatalf("first IssueToken failed: %v", err)
	}
	if server.RequestCount() != 1 {
		t.Fatalf("expected 1 request on mock server, got %d", server.RequestCount())
	}

	// Second call with force=false should use cached token without hitting server
	tok2, err := sts.IssueToken(ctx, 5*time.Minute, false)
	if err != nil {
		t.Fatalf("second IssueToken failed: %v", err)
	}
	if tok2 != tok1 {
		t.Fatalf("expected cached token %q, got %q", tok1, tok2)
	}
	if server.RequestCount() != 1 {
		t.Fatalf("expected cached call to not increase request count; got %d", server.RequestCount())
	}

	// Third call with force=true should bypass cache and contact server
	tok3, err := sts.IssueToken(ctx, 5*time.Minute, true)
	if err != nil {
		t.Fatalf("forced IssueToken failed: %v", err)
	}
	if server.RequestCount() != 2 {
		t.Fatalf("expected forced call to increment request count to 2, got %d", server.RequestCount())
	}
	if tok3 == "" {
		t.Fatal("expected non-empty token from forced refresh")
	}
}

// TestIAMIsolation_LocalTokenSourceFileReloading tests LocalTokenSource reading files,
// exchanging session tokens, reloading on change, and isolating endpoints.
func TestIAMIsolation_LocalTokenSourceFileReloading(t *testing.T) {
	ctx := context.Background()
	serverEast := startMockIAMServer(t, "east")
	serverWest := startMockIAMServer(t, "west")

	tmpDir := t.TempDir()
	tokenFileEast := filepath.Join(tmpDir, "east_token.json")
	tokenFileWest := filepath.Join(tmpDir, "west_token.json")

	// Write initial session tokens to files
	_ = os.WriteFile(tokenFileEast, []byte(`{"session_token":"st_east_initial"}`), 0600)
	_ = os.WriteFile(tokenFileWest, []byte(`{"session_token":"st_west_initial"}`), 0600)

	ltsEast := client.NewLocalTokenSource(tokenFileEast, serverEast.Endpoint(), insecure.NewCredentials(), serverEast.DialOption())
	defer func() { _ = ltsEast.Close() }()

	ltsWest := client.NewLocalTokenSource(tokenFileWest, serverWest.Endpoint(), insecure.NewCredentials(), serverWest.DialOption())
	defer func() { _ = ltsWest.Close() }()

	// 1. Initial resolution
	tokEast1, err := ltsEast.IssueToken(ctx, time.Minute, false)
	if err != nil {
		t.Fatalf("ltsEast.IssueToken failed: %v", err)
	}
	tenantEast, _ := client.ResolveTenantFromToken(tokEast1)
	if tenantEast != "tenant-from-east" {
		t.Fatalf("expected tenant-from-east, got %q", tenantEast)
	}

	tokWest1, err := ltsWest.IssueToken(ctx, time.Minute, false)
	if err != nil {
		t.Fatalf("ltsWest.IssueToken failed: %v", err)
	}
	tenantWest, _ := client.ResolveTenantFromToken(tokWest1)
	if tenantWest != "tenant-from-west" {
		t.Fatalf("expected tenant-from-west, got %q", tenantWest)
	}

	// 2. Update token file for East with a static bearer token
	validJWT := createTestJWT(map[string]interface{}{"tenant_id": "direct-east-bearer"})
	_ = os.WriteFile(tokenFileEast, []byte(fmt.Sprintf(`{"bearer_token":%q}`, "nsct_"+validJWT)), 0600)

	tokEast2, err := ltsEast.IssueToken(ctx, time.Minute, false)
	if err != nil {
		t.Fatalf("ltsEast reload failed: %v", err)
	}
	if tokEast2 != "nsct_"+validJWT {
		t.Fatalf("expected reloaded bearer token %q, got %q", "nsct_"+validJWT, tokEast2)
	}

	// 3. West remains unperturbed
	tokWest2, err := ltsWest.IssueToken(ctx, time.Minute, false)
	if err != nil {
		t.Fatalf("ltsWest check failed: %v", err)
	}
	if tokWest2 != tokWest1 {
		t.Fatalf("expected west token to remain unchanged (%q), got %q", tokWest1, tokWest2)
	}
}

// TestIAMIsolation_ConcurrentMultiProviderStress verifies thread safety and complete isolation
// under high-concurrency race detector conditions.
func TestIAMIsolation_ConcurrentMultiProviderStress(t *testing.T) {
	ctx := context.Background()
	const (
		numServers = 4
		numThreads = 20
		iterations = 10
	)

	servers := make([]*mockIAMServer, numServers)
	for i := 0; i < numServers; i++ {
		servers[i] = startMockIAMServer(t, fmt.Sprintf("cluster-%d", i))
	}

	tmpDir := t.TempDir()
	tokenFiles := make([]string, numServers)
	for i := 0; i < numServers; i++ {
		tf := filepath.Join(tmpDir, fmt.Sprintf("token_cluster_%d.json", i))
		data, _ := json.Marshal(map[string]string{
			"session_token": fmt.Sprintf("st_session_cluster_%d", i),
		})
		_ = os.WriteFile(tf, data, 0600)
		tokenFiles[i] = tf
	}

	var wg sync.WaitGroup
	errCh := make(chan error, numThreads*iterations)

	for th := 0; th < numThreads; th++ {
		wg.Add(1)
		go func(threadID int) {
			defer wg.Done()

			targetServerIdx := threadID % numServers
			targetServer := servers[targetServerIdx]
			tokenFile := tokenFiles[targetServerIdx]
			expectedTenant := fmt.Sprintf("tenant-from-cluster-%d", targetServerIdx)

			for it := 0; it < iterations; it++ {
				// 1. Resolve Token Source via AuthConfig
				ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
					TokenFile:   tokenFile,
					IAMEndpoint: targetServer.Endpoint(),
					Creds:       insecure.NewCredentials(),
					DialOptions: []grpc.DialOption{targetServer.DialOption()},
				})
				if err != nil {
					errCh <- fmt.Errorf("thread %d iter %d: ResolveTokenSource failed: %w", threadID, it, err)
					return
				}

				// 2. Issue Token
				tok, err := ts.IssueToken(ctx, 5*time.Minute, (it%5 == 0))
				if err != nil {
					errCh <- fmt.Errorf("thread %d iter %d: IssueToken failed: %w", threadID, it, err)
					return
				}

				// 3. Verify Tenant matches the expected target server
				tenant, err := client.ResolveTenantFromToken(tok)
				if err != nil {
					errCh <- fmt.Errorf("thread %d iter %d: ResolveTenantFromToken failed: %w", threadID, it, err)
					return
				}
				if tenant != expectedTenant {
					errCh <- fmt.Errorf("thread %d iter %d: VIOLATION: expected tenant %q, got %q from endpoint %s",
						threadID, it, expectedTenant, tenant, targetServer.Endpoint())
					return
				}

				// 4. Initialize Client with target IAM endpoint
				cli, err := client.New(ctx, client.Config{
					TokenSource: ts,
					IAMEndpoint: targetServer.Endpoint(),
					Creds:       insecure.NewCredentials(),
					DialOptions: []grpc.DialOption{targetServer.DialOption()},
				})
				if err != nil {
					errCh <- fmt.Errorf("thread %d iter %d: client.New failed: %w", threadID, it, err)
					return
				}

				if cli.IAMEndpoint() != targetServer.Endpoint() {
					errCh <- fmt.Errorf("thread %d iter %d: cli.IAMEndpoint mismatch: expected %q, got %q",
						threadID, it, targetServer.Endpoint(), cli.IAMEndpoint())
					_ = cli.Close()
					return
				}

				_ = cli.Close()
			}
		}(th)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		t.Fatalf("Encountered %d errors during concurrent IAM isolation stress test: %v", len(errs), errs[0])
	}
}

// TestIAMIsolation_ErrorHandling tests graceful failure handling on unreachable endpoints or empty tokens.
func TestIAMIsolation_ErrorHandling(t *testing.T) {
	ctx := context.Background()

	t.Run("Empty session token returns error", func(t *testing.T) {
		sts := client.NewSessionTokenSource("", "https://127.0.0.1:1", insecure.NewCredentials())
		_, err := sts.IssueToken(ctx, time.Minute, false)
		if err == nil || !strings.Contains(err.Error(), "empty session token") {
			t.Fatalf("expected 'empty session token' error, got %v", err)
		}
	})

	t.Run("Unreachable IAM endpoint returns connection error without panicking", func(t *testing.T) {
		closedLis := bufconn.Listen(1024)
		_ = closedLis.Close()
		closedDialOpt := grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return closedLis.Dial()
		})
		sts := client.NewSessionTokenSource("st_valid_token", "https://iam.unreachable.local:443", insecure.NewCredentials(), closedDialOpt)
		defer func() { _ = sts.Close() }()

		_, err := sts.IssueToken(ctx, time.Minute, false)
		if err == nil {
			t.Fatal("expected error connecting to unreachable endpoint, got nil")
		}
	})
}
