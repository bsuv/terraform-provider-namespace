package client_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// Helper to construct arbitrary JWT payloads
func buildCustomJWT(claims map[string]interface{}) string {
	header := `{"alg":"none","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))
	claimsBytes, _ := json.Marshal(claims)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsBytes)
	sigB64 := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return fmt.Sprintf("%s.%s.%s", headerB64, claimsB64, sigB64)
}

// TestStress_M1_TokenResolutionConcurrency exercises token resolution under high concurrency (2000 goroutines)
func TestStress_M1_TokenResolutionConcurrency(t *testing.T) {
	const numGoroutines = 2000
	tmpDir := t.TempDir()

	// Pre-create token files
	bearerFile := filepath.Join(tmpDir, "bearer.json")
	_ = os.WriteFile(bearerFile, []byte(fmt.Sprintf(`{"bearer_token": %q}`, buildCustomJWT(map[string]interface{}{"tenant_id": "file-tenant-bearer"}))), 0600)

	sessionFile := filepath.Join(tmpDir, "session.json")
	_ = os.WriteFile(sessionFile, []byte(fmt.Sprintf(`{"session_token": "nsct_%s"}`, buildCustomJWT(map[string]interface{}{"tenant_id": "file-tenant-session"}))), 0600)

	rawFile := filepath.Join(tmpDir, "raw.txt")
	_ = os.WriteFile(rawFile, []byte("nsct_"+buildCustomJWT(map[string]interface{}{"tenant_id": "file-tenant-raw"})), 0600)

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			switch workerID % 8 {
			case 0:
				// Standard JWT
				tenantExpected := fmt.Sprintf("jwt-tenant-%d", workerID%50)
				jwt := buildCustomJWT(map[string]interface{}{"tenant_id": tenantExpected})
				resolved, err := client.ResolveTenantFromToken(jwt)
				if err != nil || resolved != tenantExpected {
					t.Errorf("worker %d: expected %s, got %s (err: %v)", workerID, tenantExpected, resolved, err)
				}
				if err := client.VerifyTenantTokenMatch(jwt, tenantExpected); err != nil {
					t.Errorf("worker %d: verify match failed: %v", workerID, err)
				}

			case 1:
				// Namespace NSCT token
				tenantExpected := fmt.Sprintf("nsct-tenant-%d", workerID%50)
				token := fmt.Sprintf("nsct_%s", buildCustomJWT(map[string]interface{}{"tenant_id": tenantExpected}))
				resolved, err := client.ResolveTenantFromToken(token)
				if err != nil || resolved != tenantExpected {
					t.Errorf("worker %d: expected %s, got %s (err: %v)", workerID, tenantExpected, resolved, err)
				}
				if err := client.VerifyTenantTokenMatch(token, tenantExpected); err != nil {
					t.Errorf("worker %d: verify match failed: %v", workerID, err)
				}

			case 2:
				// Bearer file
				resolved, err := client.ResolveTenantFromToken(bearerFile)
				if err != nil || resolved != "file-tenant-bearer" {
					t.Errorf("worker %d: expected file-tenant-bearer, got %s (err: %v)", workerID, resolved, err)
				}

			case 3:
				// Session file
				resolved, err := client.ResolveTenantFromToken(sessionFile)
				if err != nil || resolved != "file-tenant-session" {
					t.Errorf("worker %d: expected file-tenant-session, got %s (err: %v)", workerID, resolved, err)
				}

			case 4:
				// Raw file
				resolved, err := client.ResolveTenantFromToken(rawFile)
				if err != nil || resolved != "file-tenant-raw" {
					t.Errorf("worker %d: expected file-tenant-raw, got %s (err: %v)", workerID, resolved, err)
				}

			case 5:
				// Deep claims: tenant object with id
				tenantExpected := fmt.Sprintf("nested-tenant-%d", workerID%50)
				jwt := buildCustomJWT(map[string]interface{}{"tenant": map[string]interface{}{"id": tenantExpected}})
				resolved, err := client.ResolveTenantFromToken(jwt)
				if err != nil || resolved != tenantExpected {
					t.Errorf("worker %d: expected %s, got %s (err: %v)", workerID, tenantExpected, resolved, err)
				}

			case 6:
				// Sub claims: tenant: prefix
				tenantExpected := fmt.Sprintf("sub-tenant-%d", workerID%50)
				jwt := buildCustomJWT(map[string]interface{}{"sub": "tenant:" + tenantExpected})
				resolved, err := client.ResolveTenantFromToken(jwt)
				if err != nil || resolved != tenantExpected {
					t.Errorf("worker %d: expected %s, got %s (err: %v)", workerID, tenantExpected, resolved, err)
				}

			case 7:
				// Adversarial invalid tokens fail-closed
				badTokens := []string{
					"",
					"   ",
					"opaque-mock-token",
					"nsct_",
					"nsct__",
					"a.b",
					"a.b.c.d",
					"a.invalid-b64!@#.c",
				}
				badTok := badTokens[workerID%len(badTokens)]
				_, err := client.ResolveTenantFromToken(badTok)
				if err == nil {
					t.Errorf("worker %d: expected error for bad token %q, got nil", workerID, badTok)
				}
			}
		}()
	}

	wg.Wait()
}

// TestStress_M1_ClaimsPrecedence tests exact precedence among multiple claims in a single JWT
func TestStress_M1_ClaimsPrecedence(t *testing.T) {
	// Precedence order specified:
	// 1. https://namespace.so/claims/tenant
	// 2. tenant_id
	// 3. tid
	// 4. tenant (string / map)
	// 5. sub

	t.Run("https://namespace.so/claims/tenant overrides all other claims", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"https://namespace.so/claims/tenant": "prec-1-ns-claims",
			"tenant_id":                          "prec-2-tenant-id",
			"tid":                                "prec-3-tid",
			"tenant":                             "prec-4-tenant",
			"sub":                                "tenant:prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "prec-1-ns-claims" {
			t.Fatalf("expected prec-1-ns-claims, got %q", resolved)
		}
	})

	t.Run("tenant_id overrides tid, tenant, and sub", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"tenant_id": "prec-2-tenant-id",
			"tid":       "prec-3-tid",
			"tenant":    "prec-4-tenant",
			"sub":       "tenant:prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "prec-2-tenant-id" {
			t.Fatalf("expected prec-2-tenant-id, got %q", resolved)
		}
	})

	t.Run("tid overrides tenant and sub", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"tid":    "prec-3-tid",
			"tenant": "prec-4-tenant",
			"sub":    "tenant:prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "prec-3-tid" {
			t.Fatalf("expected prec-3-tid, got %q", resolved)
		}
	})

	t.Run("tenant string overrides sub", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"tenant": "prec-4-tenant",
			"sub":    "tenant:prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "prec-4-tenant" {
			t.Fatalf("expected prec-4-tenant, got %q", resolved)
		}
	})

	t.Run("tenant map id overrides name and sub", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"tenant": map[string]interface{}{"id": "prec-4-map-id", "name": "prec-4-map-name"},
			"sub":    "tenant:prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "prec-4-map-id" {
			t.Fatalf("expected prec-4-map-id, got %q", resolved)
		}
	})

	t.Run("tenant map name overrides sub", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"tenant": map[string]interface{}{"name": "prec-4-map-name"},
			"sub":    "tenant:prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "prec-4-map-name" {
			t.Fatalf("expected prec-4-map-name, got %q", resolved)
		}
	})

	t.Run("sub with tenant: prefix extracts tenant", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"sub": "tenant:prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "prec-5-sub" {
			t.Fatalf("expected prec-5-sub, got %q", resolved)
		}
	})

	t.Run("sub with t_ prefix retains prefix", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"sub": "t_prec-5-sub",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "t_prec-5-sub" {
			t.Fatalf("expected t_prec-5-sub, got %q", resolved)
		}
	})

	t.Run("sub plain string retains string", func(t *testing.T) {
		jwt := buildCustomJWT(map[string]interface{}{
			"sub": "plain-sub-tenant",
		})
		resolved, err := client.ResolveTenantFromToken(jwt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "plain-sub-tenant" {
			t.Fatalf("expected plain-sub-tenant, got %q", resolved)
		}
	})
}

// TestStress_M1_AdversarialJWTStructure tests weird/corrupt JWT structures
func TestStress_M1_AdversarialJWTStructure(t *testing.T) {
	testCases := []struct {
		name        string
		token       string
		expectErr   bool
		errContains string
	}{
		{
			name:        "Empty claims object",
			token:       buildCustomJWT(map[string]interface{}{}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with integer tenant_id",
			token:       buildCustomJWT(map[string]interface{}{"tenant_id": 12345}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with boolean tenant_id",
			token:       buildCustomJWT(map[string]interface{}{"tenant_id": true}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with array tenant",
			token:       buildCustomJWT(map[string]interface{}{"tenant": []interface{}{"tenant-1", "tenant-2"}}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with empty string tenant_id",
			token:       buildCustomJWT(map[string]interface{}{"tenant_id": ""}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with whitespace-only tenant_id",
			token:       buildCustomJWT(map[string]interface{}{"tenant_id": "   \t\n  "}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with empty map tenant",
			token:       buildCustomJWT(map[string]interface{}{"tenant": map[string]interface{}{}}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with whitespace-only sub",
			token:       buildCustomJWT(map[string]interface{}{"sub": "   "}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
		{
			name:        "Claims with sub: tenant: with empty remainder",
			token:       buildCustomJWT(map[string]interface{}{"sub": "tenant:"}),
			expectErr:   true,
			errContains: "does not contain a supported tenant identity claim",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.ResolveTenantFromToken(tc.token)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errContains)
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestStress_M1_FuzzedTokenInputs generates 10,000 fuzzed pseudo-random tokens to assert zero panics
func TestStress_M1_FuzzedTokenInputs(t *testing.T) {
	const iterations = 10000

	for i := 0; i < iterations; i++ {
		// Generate random bytes or strings
		randLen, _ := rand.Int(rand.Reader, big.NewInt(128))
		buf := make([]byte, int(randLen.Int64())+1)
		_, _ = rand.Read(buf)

		var tokenCandidate string
		switch i % 5 {
		case 0:
			tokenCandidate = hex.EncodeToString(buf)
		case 1:
			tokenCandidate = base64.RawURLEncoding.EncodeToString(buf)
		case 2:
			// 3-part pseudo JWT with random junk
			p1 := base64.RawURLEncoding.EncodeToString(buf[:len(buf)/3+1])
			p2 := base64.RawURLEncoding.EncodeToString(buf)
			tokenCandidate = fmt.Sprintf("%s.%s.sig", p1, p2)
		case 3:
			// nsct_ with random junk
			tokenCandidate = fmt.Sprintf("nsct_%s", string(buf))
		case 4:
			tokenCandidate = string(buf)
		}

		// Must never panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("iteration %d panicked on input %q: %v", i, tokenCandidate, r)
				}
			}()
			_, _ = client.ResolveTenantFromToken(tokenCandidate)
			_ = client.VerifyTenantTokenMatch(tokenCandidate, "expected-tenant")
		}()
	}
}

// TestStress_M1_ExactTenantMatchRejection tests subtle prefix/suffix collision scenarios
func TestStress_M1_ExactTenantMatchRejection(t *testing.T) {
	testCases := []struct {
		name           string
		token          string
		expectedTenant string
		shouldMatch    bool
	}{
		{
			name:           "Exact match JWT",
			token:          buildCustomJWT(map[string]interface{}{"tenant_id": "target-corp"}),
			expectedTenant: "target-corp",
			shouldMatch:    true,
		},
		{
			name:           "Prefix collision JWT (expected is prefix of actual)",
			token:          buildCustomJWT(map[string]interface{}{"tenant_id": "target-corp-extra"}),
			expectedTenant: "target-corp",
			shouldMatch:    false,
		},
		{
			name:           "Suffix collision JWT (actual is prefix of expected)",
			token:          buildCustomJWT(map[string]interface{}{"tenant_id": "target"}),
			expectedTenant: "target-corp",
			shouldMatch:    false,
		},
		{
			name:           "Case sensitivity mismatch JWT",
			token:          buildCustomJWT(map[string]interface{}{"tenant_id": "Target-Corp"}),
			expectedTenant: "target-corp",
			shouldMatch:    false,
		},
		{
			name:           "Exact match nsct_",
			token:          "nsct_" + buildCustomJWT(map[string]interface{}{"tenant_id": "target-corp"}),
			expectedTenant: "target-corp",
			shouldMatch:    true,
		},
		{
			name:           "Prefix collision nsct_ (expected is prefix of actual tenant name)",
			token:          "nsct_" + buildCustomJWT(map[string]interface{}{"tenant_id": "target-corp-extra"}),
			expectedTenant: "target-corp",
			shouldMatch:    false,
		},
		{
			name:           "Suffix collision nsct_",
			token:          "nsct_" + buildCustomJWT(map[string]interface{}{"tenant_id": "target"}),
			expectedTenant: "target-corp",
			shouldMatch:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := client.VerifyTenantTokenMatch(tc.token, tc.expectedTenant)
			if tc.shouldMatch && err != nil {
				t.Fatalf("expected match, got error: %v", err)
			}
			if !tc.shouldMatch && err == nil {
				t.Fatalf("expected mismatch rejection, got nil")
			}
		})
	}
}
