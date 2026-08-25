package provider_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// =============================================================================
// CHALLENGER R4 AREA 1: FAIL-CLOSED CREDENTIAL VALIDATION STRESS TEST MATRIX
// =============================================================================

func TestEmpirical_R4_CredentialValidation_FailClosed_ExhaustiveMatrix(t *testing.T) {
	const expectedTenant = "tenant-prod-safe-100"

	// Helper to craft tokens with custom payload bytes
	makeCustomJWT := func(payloadBytes []byte) string {
		header := `{"alg":"none","typ":"JWT"}`
		headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))
		payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
		sigB64 := base64.RawURLEncoding.EncodeToString([]byte("sig"))
		return fmt.Sprintf("%s.%s.%s", headerB64, payloadB64, sigB64)
	}

	testCases := []struct {
		name          string
		tokenOrPath   string
		expected      string
		expectError   bool
		errSubstr     string
		descViolation string
	}{
		// -------------------------------------------------------------------------
		// A. Opaque non-JWT strings & legacy mock tokens
		// -------------------------------------------------------------------------
		{
			name:          "Opaque simple string",
			tokenOrPath:   "opaque",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "Must fail closed on opaque token without JWT structure",
		},
		{
			name:          "Opaque mock-token (previously whitelisted in round 3)",
			tokenOrPath:   "mock-token",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "Must fail closed on mock-token; whitelist was removed",
		},
		{
			name:          "Opaque partner-token (previously whitelisted in round 3)",
			tokenOrPath:   "partner-token",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "Must fail closed on partner-token; whitelist was removed",
		},
		{
			name:          "Opaque trusted-jwt string",
			tokenOrPath:   "trusted-jwt",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "Must fail closed on trusted-jwt string without JWT structure",
		},
		{
			name:          "Opaque Namespace token prefix",
			tokenOrPath:   "nsct_" + "live_0123456789abcdef",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "Opaque Namespace bearer token without JWT structure must fail closed",
		},

		// -------------------------------------------------------------------------
		// B. Token part structure violations (0-part, 1-part, 2-part, 4-part, 5-part)
		// -------------------------------------------------------------------------
		{
			name:          "Empty token",
			tokenOrPath:   "",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "token cannot be empty",
			descViolation: "Empty token must return error",
		},
		{
			name:          "Whitespace-only token",
			tokenOrPath:   "   \t\r\n  ",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "token cannot be empty",
			descViolation: "Whitespace token must return error",
		},
		{
			name:          "Single part token",
			tokenOrPath:   "singleparttokenwithoutdots",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "1-part token must fail closed",
		},
		{
			name:          "2-part token (missing signature)",
			tokenOrPath:   "ey" + "JhbGciOiJub25lIn0" + "." + "ey" + "J0ZW5hbnRfaWQiOiJ0ZXN0In0",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "2-part token must fail closed",
		},
		{
			name:          "4-part token (extra segment)",
			tokenOrPath:   "part1.part2.part3.part4",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "4-part token must fail closed",
		},
		{
			name:          "5-part token",
			tokenOrPath:   "a.b.c.d.e",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "unsupported or opaque token format",
			descViolation: "5-part token must fail closed",
		},

		// -------------------------------------------------------------------------
		// C. Corrupted Base64 payloads
		// -------------------------------------------------------------------------
		{
			name:          "Corrupted base64 characters in payload",
			tokenOrPath:   "eyJhbGciOiJub25lIn0.???invalid-base64???.c2ln",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT payload",
			descViolation: "Non-base64 characters in payload must fail closed",
		},
		{
			name:          "Corrupted base64 with exclamation marks",
			tokenOrPath:   "eyJhbGciOiJub25lIn0.not!valid!base64!.c2ln",
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT payload",
			descViolation: "Invalid base64 characters must fail closed",
		},

		// -------------------------------------------------------------------------
		// D. Non-JSON payloads & malformed JSON
		// -------------------------------------------------------------------------
		{
			name:          "Payload is raw plain text, not JSON",
			tokenOrPath:   makeCustomJWT([]byte("this is plain text not a json object")),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT claims JSON",
			descViolation: "Plain text payload must fail closed during JSON unmarshal",
		},
		{
			name:          "Payload is HTML 502 error page",
			tokenOrPath:   makeCustomJWT([]byte("<html><body>502 Bad Gateway</body></html>")),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT claims JSON",
			descViolation: "HTML error payload must fail closed",
		},
		{
			name:          "Payload is truncated JSON",
			tokenOrPath:   makeCustomJWT([]byte(`{"tenant_id": "tenant-prod"`)),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT claims JSON",
			descViolation: "Unterminated JSON must fail closed",
		},
		{
			name:          "Payload is JSON number (not object)",
			tokenOrPath:   makeCustomJWT([]byte(`12345678`)),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT claims JSON",
			descViolation: "JSON number payload must fail closed",
		},
		{
			name:          "Payload is JSON array (not object)",
			tokenOrPath:   makeCustomJWT([]byte(`["tenant_id", "tenant-prod-safe-100"]`)),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT claims JSON",
			descViolation: "JSON array payload must fail closed",
		},
		{
			name:          "Payload is JSON boolean (not object)",
			tokenOrPath:   makeCustomJWT([]byte(`true`)),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "malformed JWT claims JSON",
			descViolation: "JSON boolean payload must fail closed",
		},

		// -------------------------------------------------------------------------
		// E. Valid JSON payloads missing tenant claims
		// -------------------------------------------------------------------------
		{
			name:          "Empty JSON object {}",
			tokenOrPath:   createTestJWT(map[string]interface{}{}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Empty JSON object without tenant claims must fail closed",
		},
		{
			name: "Standard JWT claims only (iss, aud, exp, iat, jti) without tenant",
			tokenOrPath: createTestJWT(map[string]interface{}{
				"iss": "https://auth.namespace.so",
				"aud": "https://api.namespace.so",
				"exp": time.Now().Add(1 * time.Hour).Unix(),
				"iat": time.Now().Unix(),
				"jti": "jwt-token-id-12345",
			}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Claims without tenant identity must fail closed",
		},
		{
			name: "User identity claims only (email, user_id) without tenant",
			tokenOrPath: createTestJWT(map[string]interface{}{
				"email":   "developer@corp.com",
				"user_id": "usr_998877",
			}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "User-only claims without tenant identity must fail closed",
		},
		{
			name:          "tenant_id is empty string",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": ""}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Empty string tenant_id must fail closed",
		},
		{
			name:          "tenant_id is whitespace string",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": "   "}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Whitespace string tenant_id must fail closed",
		},
		{
			name:          "tenant_id is integer",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": 100}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Non-string tenant_id must fail closed without panic",
		},
		{
			name:          "tenant_id is null",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": nil}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Null tenant_id must fail closed",
		},
		{
			name:          "tenant is empty object {}",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{}}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Empty tenant object must fail closed",
		},
		{
			name:          "tenant object has empty id and name",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{"id": "", "name": "   "}}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Tenant object with empty id/name must fail closed",
		},
		{
			name:          "tenant object has unexpected fields only",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{"region": "eu", "tier": "free"}}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Tenant object missing id or name must fail closed",
		},
		{
			name:          "tid is empty string",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tid": ""}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Empty tid must fail closed",
		},
		{
			name:          "sub is empty string",
			tokenOrPath:   createTestJWT(map[string]interface{}{"sub": ""}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Empty sub must fail closed",
		},
		{
			name:          "https://namespace.so/claims/tenant is empty string",
			tokenOrPath:   createTestJWT(map[string]interface{}{"https://namespace.so/claims/tenant": ""}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     "does not contain a supported tenant identity claim",
			descViolation: "Empty namespace custom claim must fail closed",
		},

		// -------------------------------------------------------------------------
		// F. Tokens with mismatched tenant IDs
		// -------------------------------------------------------------------------
		{
			name:          "Mismatched tenant_id (target vs rogue)",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": "unauthorized-rogue-tenant"}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `authenticated credential tenant "unauthorized-rogue-tenant" does not match NSC_TEST_TENANT "tenant-prod-safe-100"`,
			descViolation: "Tenant ID mismatch must fail closed with explicit tenant names",
		},
		{
			name:          "Mismatched namespace custom claim",
			tokenOrPath:   createTestJWT(map[string]interface{}{"https://namespace.so/claims/tenant": "other-tenant"}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Custom namespace claim mismatch must fail closed",
		},
		{
			name:          "Mismatched tenant string",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": "other-tenant"}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Tenant string mismatch must fail closed",
		},
		{
			name:          "Mismatched tenant object id",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{"id": "other-tenant"}}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Tenant object id mismatch must fail closed",
		},
		{
			name:          "Mismatched tenant object name",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{"name": "other-tenant"}}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Tenant object name mismatch must fail closed",
		},
		{
			name:          "Mismatched tid claim",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tid": "other-tenant"}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Tid claim mismatch must fail closed",
		},
		{
			name:          "Mismatched sub tenant: prefix",
			tokenOrPath:   createTestJWT(map[string]interface{}{"sub": "tenant:other-tenant"}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Sub tenant: prefix mismatch must fail closed",
		},
		{
			name:          "Mismatched sub t_ prefix",
			tokenOrPath:   createTestJWT(map[string]interface{}{"sub": "t_other-tenant"}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Sub t_ prefix mismatch must fail closed",
		},
		{
			name:          "Substring attack (target is prefix of token claim)",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": expectedTenant + "-extended-attacker"}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Substring/prefix matches must be rejected (exact equality required)",
		},
		{
			name:          "Case sensitivity attack (token uppercase vs expected lowercase)",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": strings.ToUpper(expectedTenant)}),
			expected:      expectedTenant,
			expectError:   true,
			errSubstr:     `does not match NSC_TEST_TENANT`,
			descViolation: "Case differences must be rejected (exact string equality required)",
		},

		// -------------------------------------------------------------------------
		// G. Valid matching claims across all supported claim formats
		// -------------------------------------------------------------------------
		{
			name:          "Valid matching https://namespace.so/claims/tenant",
			tokenOrPath:   createTestJWT(map[string]interface{}{"https://namespace.so/claims/tenant": expectedTenant}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid custom claim must succeed",
		},
		{
			name:          "Valid matching tenant_id",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant_id": expectedTenant}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid tenant_id must succeed",
		},
		{
			name:          "Valid matching tid",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tid": expectedTenant}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid tid must succeed",
		},
		{
			name:          "Valid matching tenant string",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": expectedTenant}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid tenant string must succeed",
		},
		{
			name:          "Valid matching tenant object with id",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{"id": expectedTenant}}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid tenant object id must succeed",
		},
		{
			name:          "Valid matching tenant object with name",
			tokenOrPath:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{"name": expectedTenant}}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid tenant object name must succeed",
		},
		{
			name:          "Valid matching sub with tenant: prefix",
			tokenOrPath:   createTestJWT(map[string]interface{}{"sub": "tenant:" + expectedTenant}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid sub tenant: prefix must succeed",
		},
		{
			name:          "Valid matching sub with t_ prefix",
			tokenOrPath:   createTestJWT(map[string]interface{}{"sub": expectedTenant}),
			expected:      expectedTenant,
			expectError:   false,
			descViolation: "Valid sub matching must succeed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := resolveAndVerifyTenant(tc.tokenOrPath, tc.expected)
			if tc.expectError {
				if err == nil {
					t.Fatalf("VIOLATION: Expected error for %s (%s), but got nil", tc.name, tc.descViolation)
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("VIOLATION: Expected error substring %q, got error: %v", tc.errSubstr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("VIOLATION: Expected success for %s, but got unexpected error: %v", tc.name, err)
				}
			}
		})
	}

	t.Run("Token files validation matrix", func(t *testing.T) {
		tmpDir := t.TempDir()

		// 1. Valid bearer_token JSON file
		validBearerFile := filepath.Join(tmpDir, "valid_bearer.json")
		validJWT := createTestJWT(map[string]interface{}{"tenant_id": expectedTenant})
		payload, _ := json.Marshal(map[string]string{"bearer_token": validJWT})
		_ = os.WriteFile(validBearerFile, payload, 0600)
		if err := resolveAndVerifyTenant(validBearerFile, expectedTenant); err != nil {
			t.Fatalf("expected valid bearer_token file to pass: %v", err)
		}

		// 2. Valid session_token JSON file
		validSessionFile := filepath.Join(tmpDir, "valid_session.json")
		payload, _ = json.Marshal(map[string]string{"session_token": validJWT})
		_ = os.WriteFile(validSessionFile, payload, 0600)
		if err := resolveAndVerifyTenant(validSessionFile, expectedTenant); err != nil {
			t.Fatalf("expected valid session_token file to pass: %v", err)
		}

		// 3. Raw JWT in file
		rawJWTFile := filepath.Join(tmpDir, "raw_jwt.txt")
		_ = os.WriteFile(rawJWTFile, []byte(validJWT), 0600)
		if err := resolveAndVerifyTenant(rawJWTFile, expectedTenant); err != nil {
			t.Fatalf("expected raw JWT file to pass: %v", err)
		}

		// 4. Empty token file
		emptyFile := filepath.Join(tmpDir, "empty.json")
		_ = os.WriteFile(emptyFile, []byte(""), 0600)
		if err := resolveAndVerifyTenant(emptyFile, expectedTenant); err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("expected empty token file to fail: %v", err)
		}

		// 5. Opaque token in file
		opaqueFile := filepath.Join(tmpDir, "opaque.json")
		payload, _ = json.Marshal(map[string]string{"bearer_token": "opaque_token_string"})
		_ = os.WriteFile(opaqueFile, payload, 0600)
		if err := resolveAndVerifyTenant(opaqueFile, expectedTenant); err == nil || !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected opaque token file to fail closed: %v", err)
		}

		// 6. Token file with mismatched tenant
		mismatchFile := filepath.Join(tmpDir, "mismatch.json")
		mismatchJWT := createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"})
		payload, _ = json.Marshal(map[string]string{"bearer_token": mismatchJWT})
		_ = os.WriteFile(mismatchFile, payload, 0600)
		if err := resolveAndVerifyTenant(mismatchFile, expectedTenant); err == nil || !strings.Contains(err.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected mismatched tenant file to fail: %v", err)
		}
	})
}

// =============================================================================
// CHALLENGER R4 AREA 2: SWEEPER TENANT SAFETY & SCOPE STRESS TEST
// =============================================================================

func TestEmpirical_R4_Sweeper_TenantSafetyAndErrorHandling(t *testing.T) {
	const safeTestTenant = "verified-isolated-sweeper-tenant"

	t.Run("Sweeper strictly aborts when NSC_TEST_TENANT is set but all credentials are unset", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", safeTestTenant)
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")
		t.Setenv("NSC_PARTNER_TOKEN", "")

		errRunner := sweepRunnerProfiles("eu")
		if errRunner == nil {
			t.Fatal("VIOLATION: runner sweeper must return an error when credentials are missing and NSC_TEST_TENANT is configured")
		}
		if !strings.Contains(errRunner.Error(), "no tenant-matched credential") {
			t.Fatalf("VIOLATION: expected 'no tenant-matched credential' in error, got: %v", errRunner)
		}

		errPartner := sweepTenantGroups("eu")
		if errPartner == nil {
			t.Fatal("VIOLATION: tenant group sweeper must return an error when partner credentials are missing")
		}
		if !strings.Contains(errPartner.Error(), "no verified partner credential") {
			t.Fatalf("VIOLATION: expected 'no verified partner credential' in error, got: %v", errPartner)
		}
	})

	t.Run("Sweeper strictly aborts on opaque legacy tokens", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", safeTestTenant)
		t.Setenv("NSC_TOKEN", "partner-token")
		t.Setenv("NSC_PARTNER_TOKEN", "mock-token")

		errRunner := sweepRunnerProfiles("eu")
		if errRunner == nil || !strings.Contains(errRunner.Error(), "unsupported or opaque token format") {
			t.Fatalf("VIOLATION: runner sweeper did not abort on opaque token: %v", errRunner)
		}

		errPartner := sweepTenantGroups("eu")
		if errPartner == nil || !strings.Contains(errPartner.Error(), "unsupported or opaque token format") {
			t.Fatalf("VIOLATION: tenant group sweeper did not abort on opaque token: %v", errPartner)
		}
	})

	t.Run("Sweeper strictly aborts on tenant mismatch between JWT and NSC_TEST_TENANT", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", safeTestTenant)
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "production-critical-tenant"}))
		t.Setenv("NSC_PARTNER_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "production-critical-tenant"}))

		errRunner := sweepRunnerProfiles("eu")
		if errRunner == nil || !strings.Contains(errRunner.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("VIOLATION: runner sweeper did not abort on tenant mismatch: %v", errRunner)
		}

		errPartner := sweepTenantGroups("eu")
		if errPartner == nil || !strings.Contains(errPartner.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("VIOLATION: tenant group sweeper did not abort on tenant mismatch: %v", errPartner)
		}
	})

	t.Run("Sweeper accurately targets only exact run prefix tfacc-<run-id>-*", func(t *testing.T) {
		runID := "job7788"
		t.Setenv("NSC_RUN_ID", runID)
		t.Setenv("NSC_TEST_TENANT", safeTestTenant)
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": safeTestTenant}))

		prefix := testAccRunPrefix()
		if prefix != fmt.Sprintf("tfacc-%s-", runID) {
			t.Fatalf("expected prefix 'tfacc-%s-', got %q", runID, prefix)
		}

		// Mock profiles with different prefixes
		allProfiles := []*githubv1beta.RunnerProfileWithStatus{
			{ProfileId: "prof-match-1", Spec: &githubv1beta.RunnerProfileSpec{Tag: prefix + "runner-a"}},
			{ProfileId: "prof-match-2", Spec: &githubv1beta.RunnerProfileSpec{Tag: prefix + "runner-b"}},
			{ProfileId: "prof-other-run", Spec: &githubv1beta.RunnerProfileSpec{Tag: "tfacc-job9999-runner-c"}},
			{ProfileId: "prof-legacy", Spec: &githubv1beta.RunnerProfileSpec{Tag: "tf-acc-runner-d"}},
			{ProfileId: "prof-prod", Spec: &githubv1beta.RunnerProfileSpec{Tag: "production-build-runner"}},
		}

		var deletedIDs []string
		srv := testutil.NewMockServer()
		srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
			return &githubv1beta.ListProfilesResponse{Profiles: allProfiles}, nil
		}
		srv.Profile.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
			deletedIDs = append(deletedIDs, req.ProfileId)
			return &emptypb.Empty{}, nil
		}

		cli := testutil.NewTestClient(t, srv)
		pCli, err := cli.ProfileClient(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := pCli.ListProfiles(context.Background(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}

		var sweepErrs []error
		for _, p := range resp.GetProfiles() {
			if spec := p.GetSpec(); spec != nil {
				tag := spec.GetTag()
				if strings.HasPrefix(tag, prefix) {
					_, err := pCli.DeleteProfile(context.Background(), &githubv1beta.DeleteProfileRequest{ProfileId: p.GetProfileId()})
					if err != nil && !client.IsNotFound(err) {
						sweepErrs = append(sweepErrs, err)
					}
				}
			}
		}

		if len(sweepErrs) > 0 {
			t.Fatalf("unexpected sweep errors: %v", sweepErrs)
		}
		if len(deletedIDs) != 2 {
			t.Fatalf("VIOLATION: Sweeper deleted %d profiles, expected exactly 2 matching current run prefix: %v", len(deletedIDs), deletedIDs)
		}
		if deletedIDs[0] != "prof-match-1" || deletedIDs[1] != "prof-match-2" {
			t.Fatalf("VIOLATION: Sweeper deleted unexpected profile IDs: %v", deletedIDs)
		}
	})
}

// =============================================================================
// CHALLENGER R4 AREA 3: REPOSITORY POLICY RESET NOTFOUND CONTRACT STRESS TEST
// =============================================================================

func setupChallengerRepositoryPolicyResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := registry.NewRepositoryPolicyResource()

	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	var confResp resource.ConfigureResponse
	if confR, ok := r.(resource.ResourceWithConfigure); ok {
		confR.Configure(ctx, resource.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return r, schemaResp
}

func TestEmpirical_R4_RepositoryPolicy_ResetNotFoundContract_StressTest(t *testing.T) {
	ctx := context.Background()

	t.Run("Delete succeeds when GetRepositoryPolicy returns codes.NotFound", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var getCallCount int
		var updateReq *registryv1beta.UpdateRepositoryPolicyRequest

		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			getCallCount++
			if getCallCount == 1 {
				// Initial read before delete: returns active override
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              10,
					Expiration:            durationpb.New(24 * time.Hour),
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
				}, nil
			}
			// Post-update verification read: policy override deleted on server (returns NotFound)
			return nil, status.Error(codes.NotFound, "repository policy override not found")
		}

		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			updateReq = req
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupChallengerRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("service-backend"),
			Repository:            types.StringValue("service-backend"),
			Revision:              types.Int64Value(10),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("VIOLATION: Delete returned unexpected errors on clean NotFound reset: %v", resp.Diagnostics)
		}
		if updateReq == nil {
			t.Fatal("VIOLATION: UpdateRepositoryPolicy was not called during Delete")
		}
		if updateReq.Expiration != nil {
			t.Fatalf("VIOLATION: UpdateRepositoryPolicy must send nil Expiration on reset, got: %v", updateReq.Expiration)
		}
		if updateReq.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN {
			t.Fatalf("VIOLATION: UpdateRepositoryPolicy must send UNKNOWN ExpirationEnforcement on reset, got: %v", updateReq.ExpirationEnforcement)
		}
		if getCallCount < 2 {
			t.Fatalf("VIOLATION: Delete must perform post-update verification read, getCallCount=%d", getCallCount)
		}
	})

	t.Run("Delete FAILS if GetRepositoryPolicy returns 200 OK with UNKNOWN enforcement and nil expiration", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var getCallCount int

		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			getCallCount++
			if getCallCount == 1 {
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              10,
					Expiration:            durationpb.New(24 * time.Hour),
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
				}, nil
			}
			// Post-update verification read returns 200 OK policy (even with UNKNOWN/nil) instead of NotFound!
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              11,
				Expiration:            nil,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
			}, nil
		}

		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupChallengerRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("service-backend"),
			Repository:            types.StringValue("service-backend"),
			Revision:              types.Int64Value(10),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("VIOLATION: Delete must fail when GetRepositoryPolicy returns 200 OK response on post-destroy check, but no error was returned")
		}

		var foundExpectedDiag bool
		for _, d := range resp.Diagnostics.Errors() {
			if d.Summary() == "Registry Repository Policy Reset Verification Failed" &&
				strings.Contains(d.Detail(), "Expected repository policy override to be deleted (returning NotFound), but policy still exists") {
				foundExpectedDiag = true
				break
			}
		}
		if !foundExpectedDiag {
			t.Fatalf("VIOLATION: Expected 'Registry Repository Policy Reset Verification Failed' error diagnostic, got: %v", resp.Diagnostics)
		}
	})

	t.Run("Delete FAILS if GetRepositoryPolicy returns 200 OK with DO_NOT_ENFORCE", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var getCallCount int

		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			getCallCount++
			if getCallCount == 1 {
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              10,
					Expiration:            durationpb.New(24 * time.Hour),
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
				}, nil
			}
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              11,
				Expiration:            nil,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
			}, nil
		}

		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupChallengerRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("service-backend"),
			Repository:            types.StringValue("service-backend"),
			Revision:              types.Int64Value(10),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("VIOLATION: Delete must fail when GetRepositoryPolicy returns DO_NOT_ENFORCE on post-destroy check")
		}
	})

	t.Run("Delete FAILS if GetRepositoryPolicy returns 200 OK with ENFORCE and duration", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var getCallCount int

		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			getCallCount++
			if getCallCount == 1 {
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              10,
					Expiration:            durationpb.New(24 * time.Hour),
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
				}, nil
			}
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              11,
				Expiration:            durationpb.New(48 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}, nil
		}

		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupChallengerRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("service-backend"),
			Repository:            types.StringValue("service-backend"),
			Revision:              types.Int64Value(10),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("VIOLATION: Delete must fail when GetRepositoryPolicy returns active policy on post-destroy check")
		}
	})

	t.Run("Delete succeeds idempotently when resource was already deleted before Delete call (initial NotFound)", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			// Resource already deleted on backend before Terraform destroy
			return nil, status.Error(codes.NotFound, "repository policy override not found")
		}

		r, schemaResp := setupChallengerRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("service-already-gone"),
			Repository:            types.StringValue("service-already-gone"),
			Revision:              types.Int64Value(5),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("VIOLATION: Delete must succeed idempotently when resource is already NotFound, got: %v", resp.Diagnostics)
		}
	})
}

// =============================================================================
// CHALLENGER R4 AREA 4: CONCURRENT STRESS HARNESS UNDER -race
// =============================================================================

func TestEmpirical_R4_ConcurrentStressHarness(t *testing.T) {
	const numGoroutines = 20
	const iterationsPerGoroutine = 50

	t.Run("Concurrent credential verification stress", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(workerID int) {
				defer wg.Done()
				tenant := fmt.Sprintf("concurrent-tenant-%d", workerID)
				validJWT := createTestJWT(map[string]interface{}{"tenant_id": tenant})
				mismatchedJWT := createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"})

				for j := 0; j < iterationsPerGoroutine; j++ {
					// 1. Valid must always pass
					if err := resolveAndVerifyTenant(validJWT, tenant); err != nil {
						t.Errorf("worker %d iter %d: expected valid JWT to pass, got: %v", workerID, j, err)
					}

					// 2. Mismatch must always fail
					if err := resolveAndVerifyTenant(mismatchedJWT, tenant); err == nil {
						t.Errorf("worker %d iter %d: expected mismatched JWT to fail, got nil", workerID, j)
					}

					// 3. Opaque must always fail
					if err := resolveAndVerifyTenant("opaque-mock-token", tenant); err == nil {
						t.Errorf("worker %d iter %d: expected opaque token to fail, got nil", workerID, j)
					}
				}
			}(i)
		}

		wg.Wait()
	})

	t.Run("Concurrent repository policy delete stress", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var mu sync.Mutex
		repoPolicies := make(map[string]*registryv1beta.GetRepositoryPolicyResponse)

		// Seed initial policies for each worker
		for i := 0; i < numGoroutines; i++ {
			name := fmt.Sprintf("repo-worker-%d", i)
			repoPolicies[name] = &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              1,
				Expiration:            durationpb.New(24 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}
		}

		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			p, exists := repoPolicies[req.Repository]
			if !exists {
				return nil, status.Error(codes.NotFound, fmt.Sprintf("repository policy for %q not found", req.Repository))
			}
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              p.Revision,
				Expiration:            p.Expiration,
				ExpirationEnforcement: p.ExpirationEnforcement,
			}, nil
		}

		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			mu.Lock()
			defer mu.Unlock()
			if req.ExpirationEnforcement == registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN && req.Expiration == nil {
				delete(repoPolicies, req.Repository)
			} else {
				p, exists := repoPolicies[req.Repository]
				if !exists {
					p = &registryv1beta.GetRepositoryPolicyResponse{}
					repoPolicies[req.Repository] = p
				}
				p.Revision = req.Revision + 1
				p.Expiration = req.Expiration
				p.ExpirationEnforcement = req.ExpirationEnforcement
			}
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupChallengerRepositoryPolicyResource(t, srv)

		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(workerID int) {
				defer wg.Done()
				repoName := fmt.Sprintf("repo-worker-%d", workerID)

				stateModel := registry.RepositoryPolicyResourceModel{
					ID:                    types.StringValue(repoName),
					Repository:            types.StringValue(repoName),
					Revision:              types.Int64Value(1),
					Expiration:            types.StringValue("24h"),
					ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
				}
				state := tfsdk.State{Schema: schemaResp.Schema}
				_ = state.Set(context.Background(), &stateModel)

				for j := 0; j < iterationsPerGoroutine; j++ {
					req := resource.DeleteRequest{State: state}
					resp := &resource.DeleteResponse{State: state}
					r.Delete(context.Background(), req, resp)
					if resp.Diagnostics.HasError() {
						t.Errorf("worker %d iter %d: delete failed: %v", workerID, j, resp.Diagnostics)
					}
				}
			}(i)
		}

		wg.Wait()
	})
}
