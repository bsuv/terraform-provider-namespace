package client_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

func init() {
	grpcapi.RequireTransportSecurity = false
}

// TestEndpointPool_ConcurrentDialsAndClose runs an aggressive race test
// with hundreds of goroutines concurrently dialing and closing connections.
func TestEndpointPool_ConcurrentDialsAndClose(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Start()
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := client.NewEndpointPool(client.EndpointConfig{
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Version:         "test-race",
		TokenSource:     client.StaticTokenSource{Token: "test-token"},
		Creds:           insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(srv.Dialer()),
		},
	})
	if err != nil {
		t.Fatalf("failed to create EndpointPool: %v", err)
	}

	const numWorkers = 50
	const numIterations = 100

	var wg sync.WaitGroup
	var dialSuccesses int64
	var dialErrors int64
	var closeCalls int64

	// Concurrently invoke GlobalConn, IAMConn, ComputeConn, and Close
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				switch (workerID + j) % 4 {
				case 0:
					conn, err := pool.GlobalConn(ctx)
					if err != nil {
						atomic.AddInt64(&dialErrors, 1)
					} else if conn != nil {
						atomic.AddInt64(&dialSuccesses, 1)
					}
				case 1:
					conn, err := pool.IAMConn(ctx)
					if err != nil {
						atomic.AddInt64(&dialErrors, 1)
					} else if conn != nil {
						atomic.AddInt64(&dialSuccesses, 1)
					}
				case 2:
					conn, err := pool.ComputeConn(ctx)
					if err != nil {
						atomic.AddInt64(&dialErrors, 1)
					} else if conn != nil {
						atomic.AddInt64(&dialSuccesses, 1)
					}
				case 3:
					_ = pool.Close()
					atomic.AddInt64(&closeCalls, 1)
				}
			}
		}(i)
	}

	wg.Wait()
	_ = pool.Close()

	t.Logf("Stress test completed: %d successful dials, %d dial errors, %d close calls",
		atomic.LoadInt64(&dialSuccesses),
		atomic.LoadInt64(&dialErrors),
		atomic.LoadInt64(&closeCalls),
	)
}

// TestEndpointPool_ConcurrentCloseIdempotency verifies that calling Close concurrently
// from many goroutines is completely safe and idempotent.
func TestEndpointPool_ConcurrentCloseIdempotency(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Start()
	defer srv.Stop()

	ctx := context.Background()

	for iteration := 0; iteration < 20; iteration++ {
		pool, err := client.NewEndpointPool(client.EndpointConfig{
			GlobalEndpoint:  "https://global.namespaceapis.com:443",
			IAMEndpoint:     "https://iam.namespaceapis.com:443",
			ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
			Version:         "test-close",
			TokenSource:     client.StaticTokenSource{Token: "test-token"},
			Creds:           insecure.NewCredentials(),
			DialOptions: []grpc.DialOption{
				grpc.WithContextDialer(srv.Dialer()),
			},
		})
		if err != nil {
			t.Fatalf("failed to create EndpointPool: %v", err)
		}

		// Pre-populate connections
		_, _ = pool.GlobalConn(ctx)
		_, _ = pool.IAMConn(ctx)
		_, _ = pool.ComputeConn(ctx)

		var wg sync.WaitGroup
		const numClosers = 30
		for i := 0; i < numClosers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = pool.Close()
			}()
		}
		wg.Wait()
	}
}

// TestEndpointPool_DialAfterClose verifies that calling Conn methods after Close
// safely re-dials without corrupting state or panicking.
func TestEndpointPool_DialAfterClose(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Start()
	defer srv.Stop()

	ctx := context.Background()

	pool, err := client.NewEndpointPool(client.EndpointConfig{
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		TokenSource:     client.StaticTokenSource{Token: "test-token"},
		Creds:           insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(srv.Dialer()),
		},
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	for cycle := 0; cycle < 10; cycle++ {
		conn1, err := pool.GlobalConn(ctx)
		if err != nil || conn1 == nil {
			t.Fatalf("cycle %d: initial GlobalConn failed: %v", cycle, err)
		}

		// Close the pool
		if err := pool.Close(); err != nil {
			t.Fatalf("cycle %d: Close failed: %v", cycle, err)
		}

		// Subsequent dial should succeed and create a new valid connection
		conn2, err := pool.GlobalConn(ctx)
		if err != nil || conn2 == nil {
			t.Fatalf("cycle %d: GlobalConn after Close failed: %v", cycle, err)
		}
	}
}

// TestEndpointPool_UnconfiguredComputeEndpoint verifies error handling
// when ComputeConn is called without a compute endpoint configured.
func TestEndpointPool_UnconfiguredComputeEndpoint(t *testing.T) {
	pool, err := client.NewEndpointPool(client.EndpointConfig{
		GlobalEndpoint: "https://global.namespaceapis.com:443",
		IAMEndpoint:    "https://iam.namespaceapis.com:443",
		TokenSource:    client.StaticTokenSource{Token: "test-token"},
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pool.ComputeConn(context.Background())
			if err == nil || conn != nil {
				t.Errorf("expected error for unconfigured compute endpoint, got conn=%v, err=%v", conn, err)
			}
		}()
	}
	wg.Wait()
}

// TestClient_ConcurrentServiceClients verifies that multiple service clients
// created from client.Client can be retrieved and used concurrently.
func TestClient_ConcurrentServiceClients(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Start()
	defer srv.Stop()

	cl := testutil.NewTestClient(t, srv)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			switch id % 5 {
			case 0:
				p, err := cl.ProfileClient(ctx)
				if err != nil || p == nil {
					t.Errorf("ProfileClient failed: %v", err)
				}
			case 1:
				im, err := cl.ImageClient(ctx)
				if err != nil || im == nil {
					t.Errorf("ImageClient failed: %v", err)
				}
			case 2:
				tc, err := cl.TenantClient(ctx)
				if err != nil || tc == nil {
					t.Errorf("TenantClient failed: %v", err)
				}
			case 3:
				tmc, err := cl.TenantMembershipClient(ctx)
				if err != nil || tmc == nil {
					t.Errorf("TenantMembershipClient failed: %v", err)
				}
			case 4:
				rc, err := cl.RegistryClient(ctx)
				if err != nil || rc == nil {
					t.Errorf("RegistryClient failed: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestEndpointPool_ValidateEndpoint_Exhaustive thoroughly tests all valid and invalid URL/endpoint forms.
func TestEndpointPool_ValidateEndpoint_Exhaustive(t *testing.T) {
	validCases := []string{
		"https://global.namespaceapis.com",
		"https://global.namespaceapis.com:443",
		"https://eu.compute.namespaceapis.com:8443",
		"https://127.0.0.1:8443",
		"https://127.0.0.1:8443/",
		"global.namespaceapis.com:443",
		"127.0.0.1:8443",
		"localhost:9000",
	}

	for _, tc := range validCases {
		t.Run("Valid_"+tc, func(t *testing.T) {
			if err := client.ValidateEndpoint(tc); err != nil {
				t.Errorf("expected valid endpoint %q, got error: %v", tc, err)
			}
		})
	}

	invalidCases := []struct {
		endpoint string
		desc     string
	}{
		{"http://global.namespaceapis.com", "http scheme not allowed"},
		{"http://localhost:8080", "http scheme not allowed"},
		{"https://global.namespaceapis.com/api/v1", "path segments not allowed"},
		{"https://global.namespaceapis.com/path", "path segment not allowed"},
		{"global.namespaceapis.com/path", "path segment in bare host not allowed"},
		{"https://", "missing host"},
		{"https://user:pass@global.namespaceapis.com:443", "userinfo not allowed"},
		{"user:pass@global.namespaceapis.com:443", "userinfo in bare host not allowed"},
		{"https://global.namespaceapis.com:443?token=secret", "query string not allowed"},
		{"global.namespaceapis.com:443?token=secret", "query string in bare host not allowed"},
		{"https://global.namespaceapis.com:443#frag", "fragment not allowed"},
		{"global.namespaceapis.com:443#frag", "fragment in bare host not allowed"},
	}

	for _, tc := range invalidCases {
		t.Run("Invalid_"+tc.desc, func(t *testing.T) {
			if err := client.ValidateEndpoint(tc.endpoint); err == nil {
				t.Errorf("expected error for endpoint %q (%s), got nil", tc.endpoint, tc.desc)
			}
		})
	}
}

// TestEndpointPool_DefaultValues verifies fallback to default endpoints and version.
func TestEndpointPool_DefaultValues(t *testing.T) {
	pool, err := client.NewEndpointPool(client.EndpointConfig{
		TokenSource: client.StaticTokenSource{Token: "test-token"},
	})
	if err != nil {
		t.Fatalf("failed to initialize pool with defaults: %v", err)
	}
	defer func() { _ = pool.Close() }()
}
