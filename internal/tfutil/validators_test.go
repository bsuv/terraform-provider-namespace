package tfutil

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestValidEndpoint_URLValidation(t *testing.T) {
	ctx := context.Background()
	p := path.Root("endpoint")
	v := ValidEndpoint()

	if desc := v.Description(ctx); desc == "" {
		t.Errorf("expected non-empty description")
	}
	if md := v.MarkdownDescription(ctx); md == "" {
		t.Errorf("expected non-empty markdown description")
	}

	// Null and Unknown values should pass
	var respNull validator.StringResponse
	v.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringNull()}, &respNull)
	if respNull.Diagnostics.HasError() {
		t.Errorf("null value should not error: %v", respNull.Diagnostics)
	}

	var respUnknown validator.StringResponse
	v.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringUnknown()}, &respUnknown)
	if respUnknown.Diagnostics.HasError() {
		t.Errorf("unknown value should not error: %v", respUnknown.Diagnostics)
	}

	validCases := []struct {
		name     string
		endpoint string
	}{
		{"Standard HTTPS", "https://global.namespaceapis.com"},
		{"Standard HTTPS with port", "https://global.namespaceapis.com:443"},
		{"HTTPS regional compute with port", "https://eu.compute.namespaceapis.com:8443"},
		{"HTTPS IP with port", "https://127.0.0.1:8443"},
		{"HTTPS IP with trailing slash", "https://127.0.0.1:8443/"},
		{"Bare host with port", "global.namespaceapis.com:443"},
		{"Bare host without port", "global.namespaceapis.com"},
		{"Bare IP with port", "127.0.0.1:8443"},
		{"Bare localhost with port", "localhost:9000"},
	}

	for _, tc := range validCases {
		t.Run("Valid_"+tc.name, func(t *testing.T) {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue(tc.endpoint)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("expected endpoint %q to pass validation, got errors: %v", tc.endpoint, resp.Diagnostics)
			}
		})
	}

	invalidCases := []struct {
		name        string
		endpoint    string
		errContains string
	}{
		{"Empty string", "", "must not be empty"},
		{"Whitespace only", "   \t\n ", "must not be empty"},
		{"Insecure HTTP scheme", "http://global.namespaceapis.com", "must use HTTPS"},
		{"Insecure HTTP with port", "http://localhost:8080", "must use HTTPS"},
		{"Unsupported FTP scheme", "ftp://global.namespaceapis.com", "must use HTTPS scheme"},
		{"Unsupported gRPC scheme", "grpc://global.namespaceapis.com", "must use HTTPS scheme"},
		{"HTTPS with path segment", "https://global.namespaceapis.com/api/v1", "must not contain path segments"},
		{"HTTPS with trailing subpath", "https://global.namespaceapis.com/path", "must not contain path segments"},
		{"Bare host with path segment", "global.namespaceapis.com/some/path", "must not contain path segments"},
		{"Missing host in HTTPS", "https://", "must be a valid HTTPS URL"},
		{"Port without host", "https://:443", "missing host"},
		{"Port zero", "https://global.namespaceapis.com:0", "between 1 and 65535"},
		{"Port above range", "global.namespaceapis.com:65536", "between 1 and 65535"},
		{"Empty port", "global.namespaceapis.com:", "empty port"},
		// Security & credential leakage prevention cases:
		{"HTTPS with userinfo (user:pass)", "https://user:password@global.namespaceapis.com:443", "must not contain userinfo or credentials"},
		{"HTTPS with userinfo (user only)", "https://user@global.namespaceapis.com", "must not contain userinfo or credentials"},
		{"Bare host with userinfo (user:pass)", "user:secret@global.namespaceapis.com:443", "must not contain userinfo or credentials"},
		{"Bare host with userinfo (user only)", "admin@global.namespaceapis.com", "must not contain userinfo or credentials"},
		{"HTTPS with query string", "https://global.namespaceapis.com:443?token=secret123", "must not contain query parameters"},
		{"HTTPS with query parameter", "https://global.namespaceapis.com?debug=true", "must not contain query parameters"},
		{"Bare host with query string", "global.namespaceapis.com:443?key=value", "must not contain query parameters"},
		{"HTTPS with URL fragment", "https://global.namespaceapis.com:443#secret_fragment", "must not contain URL fragments"},
		{"HTTPS with empty URL fragment", "https://global.namespaceapis.com#hash", "must not contain URL fragments"},
		{"Bare host with URL fragment", "global.namespaceapis.com:443#anchor", "must not contain URL fragments"},
		{"Combined userinfo query and fragment", "https://admin:pass@global.namespaceapis.com:443/path?a=b#c", "must not contain userinfo or credentials"},
	}

	for _, tc := range invalidCases {
		t.Run("Invalid_"+tc.name, func(t *testing.T) {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue(tc.endpoint)}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected endpoint %q to fail validation, but it passed", tc.endpoint)
			}
			if tc.errContains != "" {
				matched := false
				for _, d := range resp.Diagnostics.Errors() {
					if strings.Contains(d.Detail(), tc.errContains) || strings.Contains(d.Summary(), tc.errContains) {
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("expected error containing %q, got: %v", tc.errContains, resp.Diagnostics)
				}
			}

			// Ensure credentials and userinfo are never echoed in error summaries or details
			sensitiveTerms := []string{"password", "secret", "user:", "admin@"}
			for _, term := range sensitiveTerms {
				if strings.Contains(tc.endpoint, term) {
					for _, d := range resp.Diagnostics.Errors() {
						if strings.Contains(d.Detail(), term) || strings.Contains(d.Summary(), term) {
							t.Errorf("diagnostic leaked sensitive term %q in: %s - %s", term, d.Summary(), d.Detail())
						}
					}
				}
			}
		})
	}
}

func TestValidEndpoint_CredentialRedaction_Adversarial(t *testing.T) {
	ctx := context.Background()
	p := path.Root("endpoint")
	v := ValidEndpoint()

	adversarialEndpoints := []string{
		"https://username:superSecretPassword123@global.namespaceapis.com:443",
		"https://admin:my-token-secret-xyz@eu.compute.namespaceapis.com",
		"username:superSecretPassword123@global.namespaceapis.com:443",
		"admin:my-token-secret-xyz@eu.compute.namespaceapis.com:8443",
		"https://service_account_token:nsct_1234567890abcdef@global.namespaceapis.com",
	}

	for _, ep := range adversarialEndpoints {
		var resp validator.StringResponse
		v.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue(ep)}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatalf("expected endpoint %q to fail validation", ep)
		}
		for _, d := range resp.Diagnostics.Errors() {
			if strings.Contains(d.Detail(), "superSecretPassword123") ||
				strings.Contains(d.Detail(), "my-token-secret-xyz") ||
				strings.Contains(d.Detail(), "nsct_1234567890abcdef") ||
				strings.Contains(d.Summary(), "superSecretPassword123") ||
				strings.Contains(d.Summary(), "my-token-secret-xyz") {
				t.Fatalf("diagnostic leaked credentials for %q: summary=%q detail=%q", ep, d.Summary(), d.Detail())
			}
		}
	}
}

func TestOtherValidators_Smoke(t *testing.T) {
	ctx := context.Background()
	p := path.Root("smoke")

	t.Run("DurationAtLeast", func(t *testing.T) {
		val := DurationAtLeast(2 * time.Hour)
		var resp validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("3h")}, &resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("expected 3h to pass >= 2h: %v", resp.Diagnostics)
		}
	})

	t.Run("NonEmptyString", func(t *testing.T) {
		val := NonEmptyString()
		var resp validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("hello")}, &resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("expected 'hello' to pass: %v", resp.Diagnostics)
		}
	})
}
