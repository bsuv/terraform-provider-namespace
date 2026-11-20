package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	nsProvider "github.com/bsuv/terraform-provider-namespace/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider"
)

func TestProvider_Configure_CustomEndpointTrustWarnings(t *testing.T) {
	ctx := context.Background()
	p := nsProvider.New("dev")()

	t.Run("Trusted Namespace and loopback endpoints produce zero warnings", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "")
		t.Setenv("NSC_IAM_ENDPOINT", "")
		t.Setenv("NSC_ENDPOINT", "")

		cases := []map[string]interface{}{
			{"token": "nsct_test"},
			{
				"token":            "nsct_test",
				"global_endpoint":  "https://global.namespaceapis.com:443",
				"iam_endpoint":     "https://auth.namespace.so",
				"compute_endpoint": "https://eu.namespacelabs.dev:8443",
			},
			{
				"token":            "nsct_test",
				"global_endpoint":  "https://localhost:8443",
				"iam_endpoint":     "https://127.0.0.1:8443",
				"compute_endpoint": "https://[::1]:8443",
			},
		}

		for _, cfg := range cases {
			resp := &provider.ConfigureResponse{}
			p.Configure(ctx, provider.ConfigureRequest{Config: makeProviderConfig(ctx, p, cfg)}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected errors for trusted config %v: %v", cfg, resp.Diagnostics)
			}
			if len(resp.Diagnostics.Warnings()) != 0 {
				t.Fatalf("expected 0 warnings for trusted config %v, got: %v", cfg, resp.Diagnostics.Warnings())
			}
		}
	})

	t.Run("Explicit non-Namespace non-loopback endpoint emits non-blocking warning", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "")
		t.Setenv("NSC_IAM_ENDPOINT", "")
		t.Setenv("NSC_ENDPOINT", "")

		resp := &provider.ConfigureResponse{}
		p.Configure(ctx, provider.ConfigureRequest{
			Config: makeProviderConfig(ctx, p, map[string]interface{}{
				"token":            "nsct_test",
				"compute_endpoint": "https://proxy.corp.example:8443",
			}),
		}, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("expected non-blocking warning, got errors: %v", resp.Diagnostics.Errors())
		}
		warnings := resp.Diagnostics.Warnings()
		if len(warnings) != 1 {
			t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
		}
		if !strings.Contains(warnings[0].Detail(), "proxy.corp.example") {
			t.Errorf("expected warning detail to mention custom endpoint host, got: %s", warnings[0].Detail())
		}
		if resp.ResourceData == nil {
			t.Fatal("expected provider client to still initialize when warning is non-blocking")
		}
	})

	t.Run("Environment-resolved custom endpoint emits warning unless overridden by trusted HCL config", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "https://custom-global.corp.example:443")
		t.Setenv("NSC_IAM_ENDPOINT", "")
		t.Setenv("NSC_ENDPOINT", "")

		respEnv := &provider.ConfigureResponse{}
		p.Configure(ctx, provider.ConfigureRequest{
			Config: makeProviderConfig(ctx, p, map[string]interface{}{
				"token": "nsct_test",
			}),
		}, respEnv)
		if respEnv.Diagnostics.HasError() {
			t.Fatalf("expected non-blocking warning for env endpoint, got errors: %v", respEnv.Diagnostics)
		}
		if len(respEnv.Diagnostics.Warnings()) != 1 {
			t.Fatalf("expected 1 warning for env-resolved untrusted endpoint, got %d: %v", len(respEnv.Diagnostics.Warnings()), respEnv.Diagnostics.Warnings())
		}

		// Explicit HCL override to trusted Namespace domain suppresses warning
		respOverride := &provider.ConfigureResponse{}
		p.Configure(ctx, provider.ConfigureRequest{
			Config: makeProviderConfig(ctx, p, map[string]interface{}{
				"token":           "nsct_test",
				"global_endpoint": "https://global.namespaceapis.com:443",
			}),
		}, respOverride)
		if respOverride.Diagnostics.HasError() {
			t.Fatalf("unexpected error: %v", respOverride.Diagnostics)
		}
		if len(respOverride.Diagnostics.Warnings()) != 0 {
			t.Fatalf("expected 0 warnings when HCL overrides untrusted env with trusted Namespace domain, got: %v", respOverride.Diagnostics.Warnings())
		}
	})

	t.Run("Metadata SSRF and unspecified endpoints fail Configure with error", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "")
		t.Setenv("NSC_IAM_ENDPOINT", "")
		t.Setenv("NSC_ENDPOINT", "")

		badEndpoints := []string{
			"https://169.254.169.254",
			"https://0.0.0.0:443",
			"https://[::]:443",
			"https://[fe80::1]:443",
			"https://[fd00:ec2::254]:443",
			"https://global.namespaceapis.com\n",
		}
		for _, ep := range badEndpoints {
			resp := &provider.ConfigureResponse{}
			p.Configure(ctx, provider.ConfigureRequest{
				Config: makeProviderConfig(ctx, p, map[string]interface{}{
					"token":            "nsct_test",
					"compute_endpoint": ep,
				}),
			}, resp)
			if !resp.Diagnostics.HasError() {
				t.Errorf("expected error diagnostic for blocked endpoint %q, got none", ep)
			}
		}
	})
}

func TestProvider_Configure_TokenFilePermissionsAndSizeWarnings(t *testing.T) {
	ctx := context.Background()
	p := nsProvider.New("dev")()
	tmpDir := t.TempDir()

	t.Run("Oversized token file is rejected", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		overPath := filepath.Join(tmpDir, "oversized.json")
		payload := `{"bearer_token":"nsct_tok"}` + strings.Repeat(" ", (1<<20)+1)
		if err := os.WriteFile(overPath, []byte(payload), 0600); err != nil {
			t.Fatalf("failed to write oversized file: %v", err)
		}

		resp := &provider.ConfigureResponse{}
		p.Configure(ctx, provider.ConfigureRequest{
			Config: makeProviderConfig(ctx, p, map[string]interface{}{
				"token_file": overPath,
			}),
		}, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for oversized token_file")
		}
		if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "exceeds maximum allowed size") {
			t.Errorf("expected size limit error detail, got: %s", resp.Diagnostics.Errors()[0].Detail())
		}
	})

	if runtime.GOOS == "windows" {
		return
	}

	t.Run("Permissive 0644 token file emits non-blocking warning", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")
		t.Setenv("NSC_GLOBAL_ENDPOINT", "")
		t.Setenv("NSC_IAM_ENDPOINT", "")
		t.Setenv("NSC_ENDPOINT", "")

		permPath := filepath.Join(tmpDir, "permissive.json")
		if err := os.WriteFile(permPath, []byte(`{"bearer_token":"nsct_from_file"}`), 0644); err != nil {
			t.Fatalf("failed to write token file: %v", err)
		}
		if err := os.Chmod(permPath, 0644); err != nil {
			t.Fatalf("failed to chmod 0644: %v", err)
		}

		resp := &provider.ConfigureResponse{}
		p.Configure(ctx, provider.ConfigureRequest{
			Config: makeProviderConfig(ctx, p, map[string]interface{}{
				"token_file": permPath,
			}),
		}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("expected non-blocking warning for 0644 token file, got errors: %v", resp.Diagnostics.Errors())
		}
		warnings := resp.Diagnostics.Warnings()
		if len(warnings) != 1 || !strings.Contains(warnings[0].Detail(), "0600") {
			t.Fatalf("expected 1 warning advising 0600 permissions, got: %v", warnings)
		}

		// Explicit token taking precedence over permissive NSC_TOKEN_FILE should not warn
		t.Setenv("NSC_TOKEN_FILE", permPath)
		respStatic := &provider.ConfigureResponse{}
		p.Configure(ctx, provider.ConfigureRequest{
			Config: makeProviderConfig(ctx, p, map[string]interface{}{
				"token": "nsct_explicit_override",
			}),
		}, respStatic)
		if respStatic.Diagnostics.HasError() || len(respStatic.Diagnostics.Warnings()) != 0 {
			t.Fatalf("expected no errors/warnings when explicit token overrides permissive NSC_TOKEN_FILE, got: %v", respStatic.Diagnostics)
		}
	})
}
