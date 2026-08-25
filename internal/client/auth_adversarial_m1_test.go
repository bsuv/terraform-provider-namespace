package client_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

func TestAdversarial_ResolveTenantFromToken_BoundaryCases(t *testing.T) {
	testCases := []struct {
		name          string
		input         string
		expectErr     bool
		errSubstr     string
		expectedValue string
	}{
		// Empty and whitespace
		{
			name:      "Empty string",
			input:     "",
			expectErr: true,
			errSubstr: "token cannot be empty",
		},
		{
			name:      "Whitespace string",
			input:     "   \t\r\n   ",
			expectErr: true,
			errSubstr: "token cannot be empty",
		},
		// nsct_ prefix edge cases
		{
			name:      "nsct_ only",
			input:     "nsct_",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "nsct_ with spaces only",
			input:     "nsct_   ",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "nsct_ with tabs and newlines only",
			input:     "nsct_\t\n\r",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "nsct__ (double underscore)",
			input:     "nsct__entropy",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "nsct_ with leading whitespace before underscore",
			input:     "nsct_   _entropy",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "nsct_ with legacy opaque string fails closed",
			input:     "nsct_my-corp_abc123xyz",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "nsct_ without entropy suffix fails closed when not a JWT",
			input:     "nsct_my-corp",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:          "nsct_ with valid JWT",
			input:         "nsct_" + fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"my-corp"}`))),
			expectErr:     false,
			expectedValue: "my-corp",
		},
		{
			name:          "st_ with valid JWT",
			input:         "st_" + fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"team-alpha.sub-tenant"}`))),
			expectErr:     false,
			expectedValue: "team-alpha.sub-tenant",
		},
		{
			name:          "nscw_ with valid JWT",
			input:         "nscw_" + fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"tenant1"}`))),
			expectErr:     false,
			expectedValue: "tenant1",
		},
		{
			name:          "nsct_ with padded whitespace around token",
			input:         "   nsct_" + fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"padded-tenant"}`))) + "   \n",
			expectErr:     false,
			expectedValue: "padded-tenant",
		},
		// JWT boundary and malformed cases
		{
			name:      "1-part opaque string",
			input:     "notajwt",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "2-part string (missing signature)",
			input:     "eyJhbGciOiJub25lIn0.eyJ0ZW5hbnRfaWQiOiJ0ZXN0In0",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "4-part string",
			input:     "a.b.c.d",
			expectErr: true,
			errSubstr: "unsupported or opaque token format",
		},
		{
			name:      "3-part JWT with corrupted base64 payload",
			input:     "eyJhbGciOiJub25lIn0.!!!invalid-base64!!!.sig",
			expectErr: true,
			errSubstr: "malformed JWT payload",
		},
		{
			name:      "3-part JWT with truncated JSON payload",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id": "test"`))),
			expectErr: true,
			errSubstr: "malformed JWT claims JSON",
		},
		{
			name:      "3-part JWT with invalid non-JSON payload",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte("hello world"))),
			expectErr: true,
			errSubstr: "malformed JWT claims JSON",
		},
		{
			name:      "3-part JWT with empty JSON object",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte("{}"))),
			expectErr: true,
			errSubstr: "does not contain a supported tenant identity claim",
		},
		{
			name:      "3-part JWT with unrelated claims only (no tenant, no sub)",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"auth0","aud":"api","exp":12345678}`))),
			expectErr: true,
			errSubstr: "does not contain a supported tenant identity claim",
		},
		{
			name:      "3-part JWT with empty tenant string",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"   "}`))),
			expectErr: true,
			errSubstr: "does not contain a supported tenant identity claim",
		},
		{
			name:      "3-part JWT with empty tenant object id and name",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant":{"id":"","name":""}}`))),
			expectErr: true,
			errSubstr: "does not contain a supported tenant identity claim",
		},
		{
			name:      "3-part JWT with non-string tenant_id (e.g. integer)",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":12345}`))),
			expectErr: true,
			errSubstr: "does not contain a supported tenant identity claim",
		},
		{
			name:      "3-part JWT with boolean tenant claim",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant":true}`))),
			expectErr: true,
			errSubstr: "does not contain a supported tenant identity claim",
		},
		{
			name:      "3-part JWT with null tenant claim",
			input:     fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant":null}`))),
			expectErr: true,
			errSubstr: "does not contain a supported tenant identity claim",
		},
		{
			name:          "3-part JWT with tenant object ID",
			input:         fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant":{"id":"obj-tenant-id"}}`))),
			expectErr:     false,
			expectedValue: "obj-tenant-id",
		},
		{
			name:          "3-part JWT with tenant object name fallback",
			input:         fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant":{"name":"obj-tenant-name"}}`))),
			expectErr:     false,
			expectedValue: "obj-tenant-name",
		},
		{
			name:          "3-part JWT with sub claim tenant: prefix",
			input:         fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"tenant:sub-tenant"}`))),
			expectErr:     false,
			expectedValue: "sub-tenant",
		},
		{
			name:          "3-part JWT with sub claim t_ prefix",
			input:         fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"t_custom-tenant"}`))),
			expectErr:     false,
			expectedValue: "t_custom-tenant",
		},
		{
			name:          "3-part JWT custom claim precedence over tenant_id",
			input:         fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"https://namespace.so/claims/tenant":"high-pri","tenant_id":"low-pri"}`))),
			expectErr:     false,
			expectedValue: "high-pri",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := client.ResolveTenantFromToken(tc.input)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (result: %q)", tc.errSubstr, res)
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("expected error containing %q, got: %v", tc.errSubstr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if res != tc.expectedValue {
					t.Fatalf("expected resolved tenant %q, got %q", tc.expectedValue, res)
				}
			}
		})
	}
}

func TestAdversarial_VerifyTenantTokenMatch_BoundaryCases(t *testing.T) {
	testCases := []struct {
		name           string
		token          string
		expectedTenant string
		expectErr      bool
		errSubstr      string
	}{
		{
			name:           "Empty expected tenant fails",
			token:          "nsct_valid_123",
			expectedTenant: "",
			expectErr:      true,
			errSubstr:      "expected tenant (NSC_TEST_TENANT) cannot be empty",
		},
		{
			name:           "Whitespace expected tenant fails",
			token:          "nsct_valid_123",
			expectedTenant: "   \t\n  ",
			expectErr:      true,
			errSubstr:      "expected tenant (NSC_TEST_TENANT) cannot be empty",
		},
		{
			name:           "Empty token fails",
			token:          "",
			expectedTenant: "my-tenant",
			expectErr:      true,
			errSubstr:      "token cannot be empty",
		},
		{
			name:           "Opaque token fails with actionable format message",
			token:          "opaque-secret-token",
			expectedTenant: "my-tenant",
			expectErr:      true,
			errSubstr:      "unsupported or opaque token format: acceptance testing requires a decodable JWT tenant claim matching",
		},
		{
			name:           "Matching nsct_ token succeeds",
			token:          "nsct_" + fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"my-tenant"}`))),
			expectedTenant: "my-tenant",
			expectErr:      false,
		},
		{
			name:           "Mismatched nsct_ token fails with descriptive message",
			token:          "nsct_" + fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"other-tenant"}`))),
			expectedTenant: "my-tenant",
			expectErr:      true,
			errSubstr:      "authenticated credential tenant \"other-tenant\" does not match NSC_TEST_TENANT \"my-tenant\"",
		},
		{
			name:           "Matching JWT succeeds",
			token:          fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"my-tenant"}`))),
			expectedTenant: "my-tenant",
			expectErr:      false,
		},
		{
			name:           "Mismatched JWT fails with descriptive message",
			token:          fmt.Sprintf("eyJhbGciOiJub25lIn0.%s.sig", base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"prod-corp"}`))),
			expectedTenant: "staging-corp",
			expectErr:      true,
			errSubstr:      "authenticated credential tenant \"prod-corp\" does not match NSC_TEST_TENANT \"staging-corp\"",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := client.VerifyTenantTokenMatch(tc.token, tc.expectedTenant)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSubstr)
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("expected error containing %q, got: %v", tc.errSubstr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestAdversarial_TokenFiles_EdgeCases(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("Non-existent token file returns error", func(t *testing.T) {
		// Non-existent path is treated as opaque string and fails closed
		_, err := client.ResolveTenantFromToken(filepath.Join(tmpDir, "nonexistent.json"))
		if err == nil || !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected opaque token format error for non-existent file, got: %v", err)
		}
	})

	t.Run("Empty token file returns error", func(t *testing.T) {
		emptyFile := filepath.Join(tmpDir, "empty.txt")
		_ = os.WriteFile(emptyFile, []byte("   \n\t  "), 0600)
		_, err := client.ResolveTenantFromToken(emptyFile)
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("expected empty file error, got: %v", err)
		}
	})

	t.Run("Directory path instead of file is treated as opaque string and fails closed", func(t *testing.T) {
		_, err := client.ResolveTenantFromToken(tmpDir)
		if err == nil || !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected opaque format error for directory path, got: %v", err)
		}
	})

	t.Run("Token file with empty bearer and session tokens fails closed", func(t *testing.T) {
		jsonFile := filepath.Join(tmpDir, "empty_tokens.json")
		_ = os.WriteFile(jsonFile, []byte(`{"bearer_token": "   ", "session_token": ""}`), 0600)
		_, err := client.ResolveTenantFromToken(jsonFile)
		if err == nil || !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected opaque token error when json has empty tokens, got: %v", err)
		}
	})
}
