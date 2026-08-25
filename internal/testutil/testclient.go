package testutil

import (
	"context"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

// NewTestClient creates an initialized *client.Client configured to connect to
// the in-process MockServer over bufconn with insecure transport credentials.
func NewTestClient(t *testing.T, srv *MockServer) *client.Client {
	t.Helper()

	// Disable mandatory transport security enforcement for bufconn in-memory tests
	grpcapi.RequireTransportSecurity = false

	srv.Start()
	t.Cleanup(func() {
		srv.Stop()
	})

	ctx := context.Background()

	cfg := client.Config{
		TokenSource:     client.StaticTokenSource{Token: "test-token-nsct-12345"},
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Version:         "test",
		Creds:           insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(srv.Dialer()),
		},
	}

	cl, err := client.New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}

	t.Cleanup(func() {
		_ = cl.Close()
	})

	return cl
}
