package client_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

var nextR1MockIAMPort atomic.Int32

func init() {
	nextR1MockIAMPort.Store(20000)
}

// r1MockIAMServer represents an independent mock IAM server on an in-memory bufconn listener.
type r1MockIAMServer struct {
	sessionsv1betagrpc.UnimplementedUserSessionsServiceServer

	serverID       string
	expectedToken  string
	tokenExpiryDur time.Duration
	port           int32
	listener       *bufconn.Listener
	grpcServer     *grpc.Server
	requestCount   atomic.Int64
	mu             sync.Mutex
	receivedTokens []string
}

func startR1MockIAMServer(t *testing.T, serverID, expectedToken string, tokenExpiryDur time.Duration) *r1MockIAMServer {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)

	grpcServer := grpc.NewServer()
	mock := &r1MockIAMServer{
		serverID:       serverID,
		expectedToken:  expectedToken,
		tokenExpiryDur: tokenExpiryDur,
		port:           nextR1MockIAMPort.Add(1),
		listener:       lis,
		grpcServer:     grpcServer,
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

func (m *r1MockIAMServer) Endpoint() string {
	return fmt.Sprintf("https://127.0.0.1:%d", m.port)
}

func (m *r1MockIAMServer) DialOption() grpc.DialOption {
	return grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return m.listener.Dial()
	})
}

func (m *r1MockIAMServer) IssueTenantTokenFromSession(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
	m.requestCount.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()

	md, _ := metadata.FromIncomingContext(ctx)
	var authHeader string
	if vals := md.Get("authorization"); len(vals) > 0 {
		authHeader = vals[0]
	}
	m.receivedTokens = append(m.receivedTokens, authHeader)

	expDur := m.tokenExpiryDur
	if expDur <= 0 {
		expDur = time.Hour
	}

	jwt := createTestJWT(map[string]interface{}{
		"tenant_id": fmt.Sprintf("tenant-%s", m.serverID),
		"exp":       time.Now().Add(expDur).Unix(),
	})

	return &sessions.IssueTenantTokenFromSessionResponse{
		TenantToken: "nsct_" + jwt,
	}, nil
}

func (m *r1MockIAMServer) RequestCount() int64 {
	return m.requestCount.Load()
}

func (m *r1MockIAMServer) ReceivedTokens() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]string, len(m.receivedTokens))
	copy(copied, m.receivedTokens)
	return copied
}

// TestAdversarial_R1_MultiProviderConcurrentIsolation runs concurrent provider instances
// with different IAM endpoints and asserts:
// 1. Zero cross-talk between provider instances.
// 2. Correct session token exchange routing to specific mock IAM servers.
// 3. Zero mutations to os.Getenv("NSC_IAM_ENDPOINT").
// 4. Zero race conditions under go test -race.
func TestAdversarial_R1_MultiProviderConcurrentIsolation(t *testing.T) {
	ctx := context.Background()
	const (
		numServers   = 8
		numWorkers   = 32
		opsPerWorker = 15
		sentinelEnv  = "https://iam.immutable-sentinel-env.namespaceapis.com"
	)

	t.Setenv("NSC_IAM_ENDPOINT", sentinelEnv)

	servers := make([]*r1MockIAMServer, numServers)
	expectedTokens := make([]string, numServers)
	for i := 0; i < numServers; i++ {
		token := fmt.Sprintf("st_session_cluster_%d_%s", i, randomHex(8))
		expectedTokens[i] = token
		servers[i] = startR1MockIAMServer(t, fmt.Sprintf("cluster-%d", i), token, time.Hour)
	}

	tmpDir := t.TempDir()
	tokenFiles := make([]string, numServers)
	for i := 0; i < numServers; i++ {
		tf := filepath.Join(tmpDir, fmt.Sprintf("token_file_%d.json", i))
		data, _ := json.Marshal(map[string]string{
			"session_token": expectedTokens[i],
		})
		if err := os.WriteFile(tf, data, 0600); err != nil {
			t.Fatalf("failed to write token file %d: %v", i, err)
		}
		tokenFiles[i] = tf
	}

	var wg sync.WaitGroup
	errCh := make(chan error, numWorkers*opsPerWorker)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			srvIdx := workerID % numServers
			targetServer := servers[srvIdx]
			tokenFile := tokenFiles[srvIdx]
			expectedTenant := fmt.Sprintf("tenant-cluster-%d", srvIdx)
			expectedAuthHeader := "Bearer " + expectedTokens[srvIdx]

			for op := 0; op < opsPerWorker; op++ {
				// Assert environment remains unmutated at each iteration
				if curEnv := os.Getenv("NSC_IAM_ENDPOINT"); curEnv != sentinelEnv {
					errCh <- fmt.Errorf("worker %d op %d: VIOLATION: os.Getenv(\"NSC_IAM_ENDPOINT\") was mutated to %q (expected %q)",
						workerID, op, curEnv, sentinelEnv)
					return
				}

				// 1. Resolve token source
				ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
					TokenFile:   tokenFile,
					IAMEndpoint: targetServer.Endpoint(),
					Creds:       insecure.NewCredentials(),
					DialOptions: []grpc.DialOption{targetServer.DialOption()},
				})
				if err != nil {
					errCh <- fmt.Errorf("worker %d op %d: ResolveTokenSource failed: %w", workerID, op, err)
					return
				}

				// 2. Issue token
				forceRenewal := (op%7 == 0)
				issuedTok, err := ts.IssueToken(ctx, time.Minute, forceRenewal)
				if err != nil {
					errCh <- fmt.Errorf("worker %d op %d: IssueToken failed: %w", workerID, op, err)
					return
				}

				// 3. Verify tenant from issued token
				resolvedTenant, err := client.ResolveTenantFromToken(issuedTok)
				if err != nil {
					errCh <- fmt.Errorf("worker %d op %d: ResolveTenantFromToken failed: %w", workerID, op, err)
					return
				}
				if resolvedTenant != expectedTenant {
					errCh <- fmt.Errorf("worker %d op %d: CROSS-TALK DETECTED! Expected tenant %q, got %q from %s",
						workerID, op, expectedTenant, resolvedTenant, targetServer.Endpoint())
					return
				}

				// 4. Verify client creation & pool isolation
				cli, err := client.New(ctx, client.Config{
					TokenSource: ts,
					IAMEndpoint: targetServer.Endpoint(),
					Creds:       insecure.NewCredentials(),
					DialOptions: []grpc.DialOption{targetServer.DialOption()},
				})
				if err != nil {
					errCh <- fmt.Errorf("worker %d op %d: client.New failed: %w", workerID, op, err)
					return
				}
				if cli.IAMEndpoint() != targetServer.Endpoint() {
					errCh <- fmt.Errorf("worker %d op %d: cli.IAMEndpoint mismatch: expected %q, got %q",
						workerID, op, targetServer.Endpoint(), cli.IAMEndpoint())
					_ = cli.Close()
					return
				}

				_ = cli.Close()
			}

			// Check that all auth headers received by the target server match this cluster's token
			for _, authHdr := range targetServer.ReceivedTokens() {
				if authHdr != expectedAuthHeader {
					errCh <- fmt.Errorf("CROSS-TALK DETECTED on server %s: received auth header %q, expected %q",
						targetServer.serverID, authHdr, expectedAuthHeader)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		t.Fatalf("Encountered %d errors during multi-provider concurrent isolation stress test: %v", len(errs), errs[0])
	}

	// Final check on environment variable
	if finalEnv := os.Getenv("NSC_IAM_ENDPOINT"); finalEnv != sentinelEnv {
		t.Fatalf("VIOLATION: final NSC_IAM_ENDPOINT was mutated to %q (expected %q)", finalEnv, sentinelEnv)
	}
}

// TestAdversarial_R1_TokenExpirationCachingAndRenewal tests:
// 1. In-memory caching when token is unexpired.
// 2. Automatic refresh when token approaches/passes expiration.
// 3. Forced renewal with force=true under concurrent contention.
func TestAdversarial_R1_TokenExpirationCachingAndRenewal(t *testing.T) {
	ctx := context.Background()

	t.Run("Token expires quickly and triggers automatic re-issuance", func(t *testing.T) {
		// Server issues tokens valid for 2 seconds
		srv := startR1MockIAMServer(t, "short-lived", "st_short_lived_session", 2*time.Second)

		sts := client.NewSessionTokenSource("st_short_lived_session", srv.Endpoint(), insecure.NewCredentials(), srv.DialOption())
		defer func() { _ = sts.Close() }()

		// Call 1: Fetches token from server
		tok1, err := sts.IssueToken(ctx, 100*time.Millisecond, false)
		if err != nil {
			t.Fatalf("IssueToken 1 failed: %v", err)
		}
		if srv.RequestCount() != 1 {
			t.Fatalf("expected 1 request on server, got %d", srv.RequestCount())
		}

		// Call 2 immediately: uses cache (well within 2s window)
		tok2, err := sts.IssueToken(ctx, 100*time.Millisecond, false)
		if err != nil {
			t.Fatalf("IssueToken 2 failed: %v", err)
		}
		if tok2 != tok1 {
			t.Fatalf("expected cached token %q, got %q", tok1, tok2)
		}
		if srv.RequestCount() != 1 {
			t.Fatalf("expected server request count to remain 1, got %d", srv.RequestCount())
		}

		// Sleep past token expiration (> 2s)
		time.Sleep(2200 * time.Millisecond)

		// Call 3: Token is expired, must contact server again
		tok3, err := sts.IssueToken(ctx, 100*time.Millisecond, false)
		if err != nil {
			t.Fatalf("IssueToken 3 failed: %v", err)
		}
		if srv.RequestCount() != 2 {
			t.Fatalf("expected server request count to increment to 2 on expired token, got %d", srv.RequestCount())
		}
		if tok3 == "" {
			t.Fatal("expected non-empty renewed token")
		}
	})

	t.Run("Forced renewal under concurrent contention", func(t *testing.T) {
		srv := startR1MockIAMServer(t, "concurrent-renewal", "st_renewal_session", time.Hour)
		sts := client.NewSessionTokenSource("st_renewal_session", srv.Endpoint(), insecure.NewCredentials(), srv.DialOption())
		defer func() { _ = sts.Close() }()

		const goroutines = 50
		var wg sync.WaitGroup
		errCh := make(chan error, goroutines)

		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				// Odd workers force renewal, even workers use cache
				force := (id%2 == 1)
				tok, err := sts.IssueToken(ctx, time.Minute, force)
				if err != nil {
					errCh <- fmt.Errorf("worker %d failed: %w", id, err)
					return
				}
				if tok == "" {
					errCh <- fmt.Errorf("worker %d returned empty token", id)
					return
				}
			}(g)
		}

		wg.Wait()
		close(errCh)

		for err := range errCh {
			t.Fatal(err)
		}

		// At least 1 request made, mutex ensured no data races
		if srv.RequestCount() < 1 {
			t.Fatalf("expected at least 1 request on server, got %d", srv.RequestCount())
		}
	})
}

// TestAdversarial_R1_ConcurrentLocalTokenSourceFileRotation tests concurrent file mutations
// and reads on LocalTokenSource under race conditions.
func TestAdversarial_R1_ConcurrentLocalTokenSourceFileRotation(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "rotating_token.json")

	srvEast := startR1MockIAMServer(t, "east-rotate", "st_east", time.Hour)
	srvWest := startR1MockIAMServer(t, "west-rotate", "st_west", time.Hour)

	// Initialize file with East session token
	initData, _ := json.Marshal(map[string]string{"session_token": "st_east"})
	_ = os.WriteFile(tokenFile, initData, 0600)

	lts := client.NewLocalTokenSource(tokenFile, srvEast.Endpoint(), insecure.NewCredentials(), srvEast.DialOption())
	defer func() { _ = lts.Close() }()

	const (
		numReaders = 20
		numWriters = 4
		duration   = 1 * time.Second
	)

	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, numReaders*100)

	// Start Readers
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
					tok, err := lts.IssueToken(ctx, time.Minute, false)
					if err != nil {
						// Transient read during atomic write can happen if file write is non-atomic,
						// but our writes are atomic via write to temp + rename.
						errCh <- fmt.Errorf("reader %d failed: %w", readerID, err)
						return
					}
					if tok == "" {
						errCh <- fmt.Errorf("reader %d received empty token", readerID)
						return
					}
				}
			}
		}(r)
	}

	// Start Writers rotating between session token and bearer tokens
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			it := 0
			for {
				select {
				case <-stopCh:
					return
				default:
					it++
					var payload []byte
					if it%2 == 0 {
						payload, _ = json.Marshal(map[string]string{"session_token": "st_east"})
					} else {
						jwt := createTestJWT(map[string]interface{}{
							"tenant_id": fmt.Sprintf("direct-tenant-%d", writerID),
						})
						payload, _ = json.Marshal(map[string]string{"bearer_token": "nsct_" + jwt})
					}

					tmpWrite := fmt.Sprintf("%s.tmp.%d", tokenFile, writerID)
					_ = os.WriteFile(tmpWrite, payload, 0600)
					_ = os.Rename(tmpWrite, tokenFile)
					time.Sleep(10 * time.Millisecond)
				}
			}
		}(w)
	}

	time.Sleep(duration)
	close(stopCh)
	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		t.Fatalf("Encountered %d errors during concurrent file rotation: %v", len(errs), errs[0])
	}

	_ = srvWest // ensure referenced
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
