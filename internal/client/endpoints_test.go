package client_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

func TestValidateEndpoint_SecurityAndFormat(t *testing.T) {
	validCases := []struct {
		name     string
		endpoint string
	}{
		{"HTTPS domain", "https://global.namespaceapis.com"},
		{"HTTPS domain with port", "https://global.namespaceapis.com:443"},
		{"HTTPS regional compute with port", "https://eu.compute.namespaceapis.com:8443"},
		{"HTTPS IP with port", "https://127.0.0.1:8443"},
		{"HTTPS IP with trailing slash", "https://127.0.0.1:8443/"},
		{"Bare domain with port", "global.namespaceapis.com:443"},
		{"Bare domain without port", "global.namespaceapis.com"},
		{"Bare IP with port", "127.0.0.1:8443"},
		{"Bare localhost with port", "localhost:9000"},
	}

	for _, tc := range validCases {
		t.Run("Valid_"+tc.name, func(t *testing.T) {
			if err := client.ValidateEndpoint(tc.endpoint); err != nil {
				t.Errorf("expected valid endpoint %q, got error: %v", tc.endpoint, err)
			}
		})
	}

	invalidCases := []struct {
		name        string
		endpoint    string
		errContains string
	}{
		{"Insecure HTTP domain", "http://global.namespaceapis.com", "http scheme is not supported"},
		{"Insecure HTTP localhost", "http://localhost:8080", "http scheme is not supported"},
		{"Unsupported FTP scheme", "ftp://global.namespaceapis.com", "endpoint must use HTTPS scheme"},
		{"Unsupported gRPC scheme", "grpc://global.namespaceapis.com", "endpoint must use HTTPS scheme"},
		{"HTTPS with path segments", "https://global.namespaceapis.com/api/v1", "endpoint must not contain path segments"},
		{"HTTPS with trailing path", "https://global.namespaceapis.com/path", "endpoint must not contain path segments"},
		{"Bare domain with path segment", "global.namespaceapis.com/path", "endpoint must not contain path segments"},
		{"Bare domain with nested path", "global.namespaceapis.com/some/path", "endpoint must not contain path segments"},
		{"Missing host in HTTPS", "https://", "missing host"},
		{"Port without host", "https://:443", "missing host"},
		{"Port zero", "https://global.namespaceapis.com:0", "between 1 and 65535"},
		{"Port above range", "global.namespaceapis.com:65536", "between 1 and 65535"},
		{"Empty port", "global.namespaceapis.com:", "empty port"},
		// Security hardening cases: userinfo, query strings, fragments
		{"HTTPS with user and password", "https://user:password@global.namespaceapis.com:443", "userinfo/credentials"},
		{"HTTPS with user only", "https://admin@global.namespaceapis.com", "userinfo/credentials"},
		{"Bare host with user and password", "user:secret@global.namespaceapis.com:443", "userinfo/credentials"},
		{"Bare host with user only", "admin@global.namespaceapis.com", "userinfo/credentials"},
		{"HTTPS with query string", "https://global.namespaceapis.com:443?token=secret123", "query parameters"},
		{"HTTPS with debug query parameter", "https://global.namespaceapis.com?debug=true", "query parameters"},
		{"Bare host with query parameter", "global.namespaceapis.com:443?token=secret", "query parameters"},
		{"HTTPS with URL fragment", "https://global.namespaceapis.com:443#secret_fragment", "URL fragments"},
		{"HTTPS with hash fragment", "https://global.namespaceapis.com#anchor", "URL fragments"},
		{"Bare host with URL fragment", "global.namespaceapis.com:443#hash", "URL fragments"},
		{"Combined userinfo and query", "https://user:pass@global.namespaceapis.com?token=secret", "userinfo/credentials"},
	}

	for _, tc := range invalidCases {
		t.Run("Invalid_"+tc.name, func(t *testing.T) {
			err := client.ValidateEndpoint(tc.endpoint)
			if err == nil {
				t.Fatalf("expected error for endpoint %q (%s), got nil", tc.endpoint, tc.name)
			}
			if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

func TestEndpointPool_DefaultEndpointResolution(t *testing.T) {
	t.Run("Constants match documented public endpoints", func(t *testing.T) {
		if client.DefaultGlobalEndpoint != "https://global.namespaceapis.com" {
			t.Fatalf("expected DefaultGlobalEndpoint to be %q, got %q", "https://global.namespaceapis.com", client.DefaultGlobalEndpoint)
		}
		if client.DefaultIAMEndpoint != "https://iam.namespaceapis.com" {
			t.Fatalf("expected DefaultIAMEndpoint to be %q, got %q", "https://iam.namespaceapis.com", client.DefaultIAMEndpoint)
		}
	})

	t.Run("Default resolution when endpoints are omitted/blank", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "")
		t.Setenv("NSC_IAM_ENDPOINT", "")
		t.Setenv("NSC_ENDPOINT", "")

		pool, err := client.NewEndpointPool(client.EndpointConfig{
			TokenSource: client.StaticTokenSource{Token: "test-token"},
		})
		if err != nil {
			t.Fatalf("unexpected error creating pool: %v", err)
		}
		defer func() { _ = pool.Close() }()

		if pool.GlobalEndpoint() != client.DefaultGlobalEndpoint {
			t.Errorf("expected GlobalEndpoint %q, got %q", client.DefaultGlobalEndpoint, pool.GlobalEndpoint())
		}
		if pool.IAMEndpoint() != client.DefaultIAMEndpoint {
			t.Errorf("expected IAMEndpoint %q, got %q", client.DefaultIAMEndpoint, pool.IAMEndpoint())
		}
		if pool.ComputeEndpoint() != "" {
			t.Errorf("expected empty ComputeEndpoint, got %q", pool.ComputeEndpoint())
		}
	})

	t.Run("Environment overrides resolution", func(t *testing.T) {
		const customGlobal = "https://custom-global.namespaceapis.com"
		const customIAM = "https://custom-iam.namespaceapis.com"
		const customCompute = "https://custom-compute.namespaceapis.com:8443"

		t.Setenv("NSC_GLOBAL_ENDPOINT", customGlobal)
		t.Setenv("NSC_IAM_ENDPOINT", customIAM)
		t.Setenv("NSC_ENDPOINT", customCompute)

		pool, err := client.NewEndpointPool(client.EndpointConfig{
			TokenSource: client.StaticTokenSource{Token: "test-token"},
		})
		if err != nil {
			t.Fatalf("unexpected error creating pool: %v", err)
		}
		defer func() { _ = pool.Close() }()

		if pool.GlobalEndpoint() != customGlobal {
			t.Errorf("expected GlobalEndpoint %q, got %q", customGlobal, pool.GlobalEndpoint())
		}
		if pool.IAMEndpoint() != customIAM {
			t.Errorf("expected IAMEndpoint %q, got %q", customIAM, pool.IAMEndpoint())
		}
		if pool.ComputeEndpoint() != customCompute {
			t.Errorf("expected ComputeEndpoint %q, got %q", customCompute, pool.ComputeEndpoint())
		}
	})

	t.Run("Config overrides environment and defaults", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "https://env-global.namespaceapis.com")
		t.Setenv("NSC_IAM_ENDPOINT", "https://env-iam.namespaceapis.com")
		t.Setenv("NSC_ENDPOINT", "https://env-compute.namespaceapis.com")

		const cfgGlobal = "https://cfg-global.namespaceapis.com"
		const cfgIAM = "https://cfg-iam.namespaceapis.com"
		const cfgCompute = "https://cfg-compute.namespaceapis.com:443"

		pool, err := client.NewEndpointPool(client.EndpointConfig{
			GlobalEndpoint:  cfgGlobal,
			IAMEndpoint:     cfgIAM,
			ComputeEndpoint: cfgCompute,
			TokenSource:     client.StaticTokenSource{Token: "test-token"},
		})
		if err != nil {
			t.Fatalf("unexpected error creating pool: %v", err)
		}
		defer func() { _ = pool.Close() }()

		if pool.GlobalEndpoint() != cfgGlobal {
			t.Errorf("expected GlobalEndpoint %q, got %q", cfgGlobal, pool.GlobalEndpoint())
		}
		if pool.IAMEndpoint() != cfgIAM {
			t.Errorf("expected IAMEndpoint %q, got %q", cfgIAM, pool.IAMEndpoint())
		}
		if pool.ComputeEndpoint() != cfgCompute {
			t.Errorf("expected ComputeEndpoint %q, got %q", cfgCompute, pool.ComputeEndpoint())
		}
	})
}

func TestClient_IAMSessionRouting(t *testing.T) {
	ctx := context.Background()

	t.Run("client.New does not mutate NSC_IAM_ENDPOINT from configured iam_endpoint", func(t *testing.T) {
		t.Setenv("NSC_IAM_ENDPOINT", "original_iam_env")

		const configuredIAM = "https://iam.custom.namespaceapis.com"
		cli, err := client.New(ctx, client.Config{
			Token:       "nsct_dummy_token",
			IAMEndpoint: configuredIAM,
		})
		if err != nil {
			t.Fatalf("unexpected error initializing client: %v", err)
		}
		defer func() { _ = cli.Close() }()

		if os.Getenv("NSC_IAM_ENDPOINT") != "original_iam_env" {
			t.Errorf("expected NSC_IAM_ENDPOINT to remain %q, got %q", "original_iam_env", os.Getenv("NSC_IAM_ENDPOINT"))
		}
		if cli.IAMEndpoint() != configuredIAM {
			t.Errorf("expected cli.IAMEndpoint() %q, got %q", configuredIAM, cli.IAMEndpoint())
		}
		if cli.GlobalEndpoint() != client.DefaultGlobalEndpoint {
			t.Errorf("expected cli.GlobalEndpoint() %q, got %q", client.DefaultGlobalEndpoint, cli.GlobalEndpoint())
		}
	})

	t.Run("client.New does not mutate NSC_IAM_ENDPOINT when iam_endpoint is omitted", func(t *testing.T) {
		t.Setenv("NSC_IAM_ENDPOINT", "")

		cli, err := client.New(ctx, client.Config{
			Token: "nsct_dummy_token",
		})
		if err != nil {
			t.Fatalf("unexpected error initializing client: %v", err)
		}
		defer func() { _ = cli.Close() }()

		if os.Getenv("NSC_IAM_ENDPOINT") != "" {
			t.Errorf("expected NSC_IAM_ENDPOINT to remain empty, got %q", os.Getenv("NSC_IAM_ENDPOINT"))
		}
		if cli.IAMEndpoint() != client.DefaultIAMEndpoint {
			t.Errorf("expected cli.IAMEndpoint() %q, got %q", client.DefaultIAMEndpoint, cli.IAMEndpoint())
		}
	})
}
