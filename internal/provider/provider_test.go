package provider_test

import (
	"context"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	nsProvider "github.com/bsuv/terraform-provider-namespace/internal/provider"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

func TestProvider_Schema(t *testing.T) {
	ctx := context.Background()
	p := nsProvider.New("test-version")()

	req := provider.SchemaRequest{}
	resp := &provider.SchemaResponse{}
	p.Schema(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema diagnostics: %v", resp.Diagnostics)
	}

	attrs := resp.Schema.Attributes
	expectedAttrs := []string{"token", "token_file", "global_endpoint", "iam_endpoint", "compute_endpoint"}

	for _, name := range expectedAttrs {
		attr, exists := attrs[name]
		if !exists {
			t.Errorf("expected provider attribute %q not found in schema", name)
			continue
		}
		if name == "token" && !attr.IsSensitive() {
			t.Errorf("attribute %q should be marked sensitive", name)
		}
	}
}

func TestProvider_Configure_WithMockServer(t *testing.T) {
	grpcapi.RequireTransportSecurity = false

	srv := testutil.NewMockServer()
	srv.Start()
	t.Cleanup(func() {
		srv.Stop()
	})

	ctx := context.Background()

	// Configure provider client directly with mock server client
	cl, err := client.New(ctx, client.Config{
		TokenSource:     client.StaticTokenSource{Token: "test-token"},
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Creds:           insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(srv.Dialer()),
		},
	})
	if err != nil {
		t.Fatalf("failed to initialize test client: %v", err)
	}
	t.Cleanup(func() {
		_ = cl.Close()
	})

	if cl.Pool() == nil {
		t.Fatal("expected endpoint pool to be initialized")
	}
}

func TestProviderServer_Protocol6(t *testing.T) {
	// Verify provider server factory satisfies framework requirements
	serverFactory := nsProvider.New("1.0.0")
	if serverFactory == nil {
		t.Fatal("expected non-nil provider factory")
	}

	p := serverFactory()

	// Test Metadata
	var metaResp provider.MetadataResponse
	p.Metadata(context.Background(), provider.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace" {
		t.Errorf("expected TypeName 'namespace', got %q", metaResp.TypeName)
	}
	if metaResp.Version != "1.0.0" {
		t.Errorf("expected Version '1.0.0', got %q", metaResp.Version)
	}

	// Test Registration
	if len(p.Resources(context.Background())) != 5 {
		t.Errorf("expected 5 resources in Phase 2, got %d", len(p.Resources(context.Background())))
	}
	if len(p.DataSources(context.Background())) != 13 {
		t.Errorf("expected 13 data sources in Phase 4, got %d", len(p.DataSources(context.Background())))
	}
	if pWithEph, ok := p.(provider.ProviderWithEphemeralResources); ok {
		if len(pWithEph.EphemeralResources(context.Background())) != 6 {
			t.Errorf("expected 6 ephemeral resources in Phase 3, got %d", len(pWithEph.EphemeralResources(context.Background())))
		}
	} else {
		t.Fatal("provider should implement ProviderWithEphemeralResources")
	}

	_ = providerserver.NewProtocol6(serverFactory())
}
