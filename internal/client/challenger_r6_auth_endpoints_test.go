package client_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// =============================================================================
// CHALLENGER R6 AREA 1: R1 ENDPOINT RESOLUTION, PRECEDENCE & SESSION ROUTING
// =============================================================================

func TestEmpirical_R6_R1_EndpointResolutionAndPrecedence(t *testing.T) {
	ctx := context.Background()

	t.Run("Documented public default constants", func(t *testing.T) {
		if client.DefaultGlobalEndpoint != "https://global.namespaceapis.com" {
			t.Fatalf("expected DefaultGlobalEndpoint to be %q, got %q",
				"https://global.namespaceapis.com", client.DefaultGlobalEndpoint)
		}
		if client.DefaultIAMEndpoint != "https://iam.namespaceapis.com" {
			t.Fatalf("expected DefaultIAMEndpoint to be %q, got %q",
				"https://iam.namespaceapis.com", client.DefaultIAMEndpoint)
		}
	})

	t.Run("Omitted endpoints resolve strictly to documented defaults", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "")
		t.Setenv("NSC_IAM_ENDPOINT", "")
		t.Setenv("NSC_ENDPOINT", "")

		pool, err := client.NewEndpointPool(client.EndpointConfig{
			TokenSource: client.StaticTokenSource{Token: "test-token"},
		})
		if err != nil {
			t.Fatalf("unexpected pool initialization error: %v", err)
		}
		defer func() { _ = pool.Close() }()

		if pool.GlobalEndpoint() != client.DefaultGlobalEndpoint {
			t.Errorf("expected GlobalEndpoint %q, got %q", client.DefaultGlobalEndpoint, pool.GlobalEndpoint())
		}
		if pool.IAMEndpoint() != client.DefaultIAMEndpoint {
			t.Errorf("expected IAMEndpoint %q, got %q", client.DefaultIAMEndpoint, pool.IAMEndpoint())
		}
		if pool.ComputeEndpoint() != "" {
			t.Errorf("expected ComputeEndpoint to be empty when omitted, got %q", pool.ComputeEndpoint())
		}
	})

	t.Run("Whitespace-only endpoints fall back to documented defaults", func(t *testing.T) {
		t.Setenv("NSC_GLOBAL_ENDPOINT", "   \t\n ")
		t.Setenv("NSC_IAM_ENDPOINT", "  \r\n ")
		t.Setenv("NSC_ENDPOINT", "   ")

		pool, err := client.NewEndpointPool(client.EndpointConfig{
			GlobalEndpoint:  "   ",
			IAMEndpoint:     " \t\n ",
			ComputeEndpoint: "   ",
			TokenSource:     client.StaticTokenSource{Token: "test-token"},
		})
		if err != nil {
			t.Fatalf("unexpected error with whitespace endpoints: %v", err)
		}
		defer func() { _ = pool.Close() }()

		if pool.GlobalEndpoint() != client.DefaultGlobalEndpoint {
			t.Errorf("expected GlobalEndpoint %q, got %q", client.DefaultGlobalEndpoint, pool.GlobalEndpoint())
		}
		if pool.IAMEndpoint() != client.DefaultIAMEndpoint {
			t.Errorf("expected IAMEndpoint %q, got %q", client.DefaultIAMEndpoint, pool.IAMEndpoint())
		}
	})

	t.Run("Precedence: Config overrides Environment and Defaults", func(t *testing.T) {
		const (
			envGlobal = "https://env-global.namespaceapis.com"
			envIAM    = "https://env-iam.namespaceapis.com"
			cfgGlobal = "https://cfg-global.namespaceapis.com"
			cfgIAM    = "https://cfg-iam.namespaceapis.com"
		)

		t.Setenv("NSC_GLOBAL_ENDPOINT", envGlobal)
		t.Setenv("NSC_IAM_ENDPOINT", envIAM)

		pool, err := client.NewEndpointPool(client.EndpointConfig{
			GlobalEndpoint: cfgGlobal,
			IAMEndpoint:    cfgIAM,
			TokenSource:    client.StaticTokenSource{Token: "test-token"},
		})
		if err != nil {
			t.Fatalf("unexpected pool error: %v", err)
		}
		defer func() { _ = pool.Close() }()

		if pool.GlobalEndpoint() != cfgGlobal {
			t.Errorf("expected cfgGlobal %q, got %q", cfgGlobal, pool.GlobalEndpoint())
		}
		if pool.IAMEndpoint() != cfgIAM {
			t.Errorf("expected cfgIAM %q, got %q", cfgIAM, pool.IAMEndpoint())
		}
	})

	t.Run("Precedence: Environment overrides Defaults when Config omitted", func(t *testing.T) {
		const (
			envGlobal  = "https://env-global.namespaceapis.com"
			envIAM     = "https://env-iam.namespaceapis.com"
			envCompute = "https://env-compute.namespaceapis.com:8443"
		)

		t.Setenv("NSC_GLOBAL_ENDPOINT", envGlobal)
		t.Setenv("NSC_IAM_ENDPOINT", envIAM)
		t.Setenv("NSC_ENDPOINT", envCompute)

		pool, err := client.NewEndpointPool(client.EndpointConfig{
			TokenSource: client.StaticTokenSource{Token: "test-token"},
		})
		if err != nil {
			t.Fatalf("unexpected pool error: %v", err)
		}
		defer func() { _ = pool.Close() }()

		if pool.GlobalEndpoint() != envGlobal {
			t.Errorf("expected envGlobal %q, got %q", envGlobal, pool.GlobalEndpoint())
		}
		if pool.IAMEndpoint() != envIAM {
			t.Errorf("expected envIAM %q, got %q", envIAM, pool.IAMEndpoint())
		}
		if pool.ComputeEndpoint() != envCompute {
			t.Errorf("expected envCompute %q, got %q", envCompute, pool.ComputeEndpoint())
		}
	})

	t.Run("NSC_IAM_ENDPOINT is not mutated in environment by client.New", func(t *testing.T) {
		t.Setenv("NSC_IAM_ENDPOINT", "original_iam_env")

		const customIAM = "https://iam.custom-prod.namespaceapis.com"
		cli, err := client.New(ctx, client.Config{
			Token:       "nsct_dummy_token",
			IAMEndpoint: customIAM,
		})
		if err != nil {
			t.Fatalf("unexpected error creating client: %v", err)
		}
		defer func() { _ = cli.Close() }()

		if os.Getenv("NSC_IAM_ENDPOINT") != "original_iam_env" {
			t.Errorf("expected NSC_IAM_ENDPOINT to remain %q, got %q", "original_iam_env", os.Getenv("NSC_IAM_ENDPOINT"))
		}
		if cli.IAMEndpoint() != customIAM {
			t.Errorf("expected cli.IAMEndpoint() %q, got %q", customIAM, cli.IAMEndpoint())
		}
	})
}

// =============================================================================
// CHALLENGER R6 AREA 2: R6 FAIL-CLOSED AUTHENTICATION & PINNED TOKEN CLAIMS
// =============================================================================

func TestEmpirical_R6_R6_FailClosedAuthentication(t *testing.T) {
	const expectedTenant = "tenant-safe-release-2026"

	t.Run("Fabricated opaque tokens fail closed immediately", func(t *testing.T) {
		fabricatedTokens := []string{
			"nsct_bad_token",
			"nsct_fake_anything",
			"nsct_tenant_anything",
			"nsct_my-org_1234567890abcdef",
			"nsct_live_token_0123456789abcdef",
			"mock-token",
			"partner-token",
			"trusted-jwt",
			"st_session-token-opaque",
			"nscw_workspace-token-opaque",
			"nsct_",
			"nsct__token",
			"raw-opaque-string-without-dots",
		}

		for _, tok := range fabricatedTokens {
			t.Run(tok, func(t *testing.T) {
				// 1. ResolveTenantFromToken must fail closed
				tenant, err := client.ResolveTenantFromToken(tok)
				if err == nil {
					t.Fatalf("VIOLATION: ResolveTenantFromToken must fail closed on fabricated token %q, got tenant %q", tok, tenant)
				}
				if !strings.Contains(err.Error(), "unsupported or opaque token format") && !strings.Contains(err.Error(), "malformed") {
					t.Errorf("unexpected error message for %q: %v", tok, err)
				}

				// 2. VerifyTenantTokenMatch must fail closed
				err = client.VerifyTenantTokenMatch(tok, expectedTenant)
				if err == nil {
					t.Fatalf("VIOLATION: VerifyTenantTokenMatch must fail closed on fabricated token %q", tok)
				}
				if !strings.Contains(err.Error(), "unsupported or opaque token format") && !strings.Contains(err.Error(), "malformed") {
					t.Errorf("unexpected error message for %q: %v", tok, err)
				}
			})
		}
	})

	t.Run("Malformed and corrupted JWTs fail closed cleanly without panics", func(t *testing.T) {
		malformedCases := []struct {
			name        string
			token       string
			errContains string
		}{
			{
				name:        "0 parts (empty string)",
				token:       "",
				errContains: "token cannot be empty",
			},
			{
				name:        "1 part",
				token:       "headeronly",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "2 parts (missing signature)",
				token:       "part1.part2",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "4 parts",
				token:       "part1.part2.part3.part4",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "Corrupted base64URL in payload",
				token:       "header.???notbase64???.sig",
				errContains: "malformed JWT payload",
			},
			{
				name:        "Corrupted JSON in payload",
				token:       fmt.Sprintf("header.%s.sig", base64.RawURLEncoding.EncodeToString([]byte("invalid json {{"))),
				errContains: "malformed JWT claims JSON",
			},
			{
				name:        "JSON array in payload",
				token:       fmt.Sprintf("header.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`["tenant_id", "my-org"]`))),
				errContains: "malformed JWT claims JSON",
			},
			{
				name:        "Valid JSON payload but missing tenant claims",
				token:       createTestJWT(map[string]interface{}{"aud": "api", "exp": 123456789}),
				errContains: "does not contain a supported tenant identity claim",
			},
		}

		for _, tc := range malformedCases {
			t.Run(tc.name, func(t *testing.T) {
				tenant, err := client.ResolveTenantFromToken(tc.token)
				if err == nil {
					t.Fatalf("VIOLATION: expected error for %s, got tenant %q", tc.name, tenant)
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("expected error containing %q, got: %v", tc.errContains, err)
				}
			})
		}
	})

	t.Run("Valid JWTs across all supported tenant claim formats resolve accurately", func(t *testing.T) {
		rawJWTCases := []struct {
			name   string
			claims map[string]interface{}
		}{
			{"Custom claim https://namespace.so/claims/tenant", map[string]interface{}{"https://namespace.so/claims/tenant": expectedTenant}},
			{"Standard claim tenant_id", map[string]interface{}{"tenant_id": expectedTenant}},
			{"Short claim tid", map[string]interface{}{"tid": expectedTenant}},
			{"String claim tenant", map[string]interface{}{"tenant": expectedTenant}},
			{"Object claim tenant.id", map[string]interface{}{"tenant": map[string]interface{}{"id": expectedTenant}}},
			{"Object claim tenant.name", map[string]interface{}{"tenant": map[string]interface{}{"name": expectedTenant}}},
			{"Subject claim with tenant: prefix", map[string]interface{}{"sub": "tenant:" + expectedTenant}},
			{"Subject claim with t_ prefix", map[string]interface{}{"sub": "t_" + expectedTenant}},
			{"Subject claim bare", map[string]interface{}{"sub": expectedTenant}},
		}

		for _, tc := range rawJWTCases {
			t.Run("RawJWT_"+tc.name, func(t *testing.T) {
				jwt := createTestJWT(tc.claims)
				resolved, err := client.ResolveTenantFromToken(jwt)
				if err != nil {
					t.Fatalf("unexpected error resolving tenant: %v", err)
				}
				if resolved != expectedTenant && resolved != "t_"+expectedTenant {
					t.Fatalf("expected tenant %q, got %q", expectedTenant, resolved)
				}
			})
		}

		prefixedCases := []struct {
			name   string
			prefix string
		}{
			{"nsct_ prefix", "nsct_"},
			{"st_ prefix", "st_"},
			{"nscw_ prefix", "nscw_"},
		}

		for _, tc := range prefixedCases {
			t.Run("SDKPrefixed_"+tc.name, func(t *testing.T) {
				jwt := tc.prefix + createTestJWT(map[string]interface{}{"tenant_id": expectedTenant})
				resolved, err := client.ResolveTenantFromToken(jwt)
				if err != nil {
					t.Fatalf("unexpected error resolving %s token: %v", tc.name, err)
				}
				if resolved != expectedTenant {
					t.Fatalf("expected tenant %q, got %q", expectedTenant, resolved)
				}
			})
		}
	})

	t.Run("Multi-threaded concurrent token resolution stress test", func(t *testing.T) {
		const (
			goroutines   = 100
			opsPerThread = 50
		)

		validJWT := createTestJWT(map[string]interface{}{"tenant_id": expectedTenant})
		validNsct := "nsct_" + validJWT

		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "thread_token.json")
		_ = os.WriteFile(tokenFile, []byte(fmt.Sprintf(`{"bearer_token": %q}`, validJWT)), 0600)

		var wg sync.WaitGroup
		errs := make(chan error, goroutines*opsPerThread)

		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(threadID int) {
				defer wg.Done()

				for i := 0; i < opsPerThread; i++ {
					mode := (threadID*opsPerThread + i) % 5
					switch mode {
					case 0:
						// Valid JWT
						resolved, err := client.ResolveTenantFromToken(validJWT)
						if err != nil || resolved != expectedTenant {
							errs <- fmt.Errorf("thread %d op %d: valid JWT failed: res=%q err=%v", threadID, i, resolved, err)
						}
					case 1:
						// Valid nsct_ JWT
						resolved, err := client.ResolveTenantFromToken(validNsct)
						if err != nil || resolved != expectedTenant {
							errs <- fmt.Errorf("thread %d op %d: valid nsct_ failed: res=%q err=%v", threadID, i, resolved, err)
						}
					case 2:
						// File token
						resolved, err := client.ResolveTenantFromToken(tokenFile)
						if err != nil || resolved != expectedTenant {
							errs <- fmt.Errorf("thread %d op %d: file token failed: res=%q err=%v", threadID, i, resolved, err)
						}
					case 3:
						// Fabricated opaque token - MUST fail closed
						_, err := client.ResolveTenantFromToken("nsct_fabricated_bad_token")
						if err == nil {
							errs <- fmt.Errorf("thread %d op %d: opaque token succeeded unexpectedly", threadID, i)
						}
					case 4:
						// Verify match
						err := client.VerifyTenantTokenMatch(validJWT, expectedTenant)
						if err != nil {
							errs <- fmt.Errorf("thread %d op %d: verify match failed: %v", threadID, i, err)
						}
					}
				}
			}(g)
		}

		wg.Wait()
		close(errs)

		var collectedErrs []error
		for err := range errs {
			collectedErrs = append(collectedErrs, err)
		}

		if len(collectedErrs) > 0 {
			t.Fatalf("Encountered %d concurrent errors during multi-threaded resolution: %v", len(collectedErrs), collectedErrs[0])
		}
	})
}
