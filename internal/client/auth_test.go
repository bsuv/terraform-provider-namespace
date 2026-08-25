package client_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

func createTestJWT(claims map[string]interface{}) string {
	header := `{"alg":"none","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))
	claimsBytes, _ := json.Marshal(claims)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsBytes)
	sigB64 := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return fmt.Sprintf("%s.%s.%s", headerB64, claimsB64, sigB64)
}

func TestResolveTokenSource(t *testing.T) {
	ctx := context.Background()

	t.Run("Provider Config Token takes precedence", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "env-token")
		t.Setenv("NSC_TOKEN_FILE", "/nonexistent")
		cfg := client.AuthConfig{Token: "explicit-token"}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil {
			t.Fatalf("unexpected IssueToken error: %v", err)
		}
		if tok != "explicit-token" {
			t.Fatalf("expected explicit-token, got %q", tok)
		}
	})

	t.Run("Provider Config TokenFile takes precedence over env vars", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token.json")
		_ = os.WriteFile(tokenFile, []byte(`{"bearer_token": "file-token-123"}`), 0600)

		t.Setenv("NSC_TOKEN", "env-token")
		cfg := client.AuthConfig{TokenFile: tokenFile}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil {
			t.Fatalf("unexpected IssueToken error: %v", err)
		}
		if tok != "file-token-123" {
			t.Fatalf("expected file-token-123, got %q", tok)
		}
	})

	t.Run("NSC_TOKEN env var takes precedence over NSC_TOKEN_FILE", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "env-token-456")
		t.Setenv("NSC_TOKEN_FILE", "/nonexistent")
		cfg := client.AuthConfig{}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil {
			t.Fatalf("unexpected IssueToken error: %v", err)
		}
		if tok != "env-token-456" {
			t.Fatalf("expected env-token-456, got %q", tok)
		}
	})

	t.Run("NSC_TOKEN_FILE env var is loaded", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "env_token.json")
		_ = os.WriteFile(tokenFile, []byte(`{"bearer_token": "bearer-token-789"}`), 0600)

		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", tokenFile)
		cfg := client.AuthConfig{}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil {
			t.Fatalf("unexpected IssueToken error: %v", err)
		}
		if tok != "bearer-token-789" {
			t.Fatalf("expected bearer-token-789, got %q", tok)
		}
	})

	t.Run("Empty or malformed token file errors cleanly", func(t *testing.T) {
		tmpDir := t.TempDir()
		emptyFile := filepath.Join(tmpDir, "empty.json")
		_ = os.WriteFile(emptyFile, []byte(""), 0600)

		cfg := client.AuthConfig{TokenFile: emptyFile}
		_, err := client.ResolveTokenSource(ctx, cfg)
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("expected empty file error, got: %v", err)
		}

		malformedFile := filepath.Join(tmpDir, "malformed.json")
		_ = os.WriteFile(malformedFile, []byte("not-json"), 0600)

		cfg2 := client.AuthConfig{TokenFile: malformedFile}
		_, err = client.ResolveTokenSource(ctx, cfg2)
		if err == nil || !strings.Contains(err.Error(), "contains malformed JSON") {
			t.Fatalf("expected malformed JSON error, got: %v", err)
		}
	})

	t.Run("Malformed default credential fails eagerly without falling through", func(t *testing.T) {
		if _, err := os.Stat("/var/run/nsc/token.json"); err == nil {
			t.Skip("cannot isolate the provider-local user credential while a workload credential is mounted")
		}

		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")
		configRoot := t.TempDir()
		t.Setenv("HOME", configRoot)
		t.Setenv("XDG_CONFIG_HOME", configRoot)

		userConfigDir, err := os.UserConfigDir()
		if err != nil {
			t.Fatalf("failed to resolve isolated user config directory: %v", err)
		}
		credentialDir := filepath.Join(userConfigDir, "ns")
		if err := os.MkdirAll(credentialDir, 0700); err != nil {
			t.Fatalf("failed to create isolated credential directory: %v", err)
		}
		credentialPath := filepath.Join(credentialDir, "token.json")
		if err := os.WriteFile(credentialPath, []byte("not-json"), 0600); err != nil {
			t.Fatalf("failed to write malformed default credential: %v", err)
		}

		_, err = client.ResolveTokenSource(ctx, client.AuthConfig{})
		if err == nil {
			t.Fatal("expected malformed default credential to fail during token-source resolution")
		}
		if !strings.Contains(err.Error(), "default user credential") || !strings.Contains(err.Error(), "malformed JSON") {
			t.Fatalf("expected an actionable malformed default credential error, got: %v", err)
		}
	})
}

func TestResolveTenantFromToken(t *testing.T) {
	t.Run("Valid JWT claims decoding", func(t *testing.T) {
		testCases := []struct {
			name           string
			claims         map[string]interface{}
			expectedTenant string
		}{
			{
				name:           "https://namespace.so/claims/tenant custom claim",
				claims:         map[string]interface{}{"https://namespace.so/claims/tenant": "test-tenant-1"},
				expectedTenant: "test-tenant-1",
			},
			{
				name:           "tenant_id standard claim",
				claims:         map[string]interface{}{"tenant_id": "test-tenant-2"},
				expectedTenant: "test-tenant-2",
			},
			{
				name:           "tid claim",
				claims:         map[string]interface{}{"tid": "test-tenant-3"},
				expectedTenant: "test-tenant-3",
			},
			{
				name:           "tenant string claim",
				claims:         map[string]interface{}{"tenant": "test-tenant-4"},
				expectedTenant: "test-tenant-4",
			},
			{
				name:           "tenant object with id",
				claims:         map[string]interface{}{"tenant": map[string]interface{}{"id": "test-tenant-5"}},
				expectedTenant: "test-tenant-5",
			},
			{
				name:           "tenant object with name",
				claims:         map[string]interface{}{"tenant": map[string]interface{}{"name": "test-tenant-6"}},
				expectedTenant: "test-tenant-6",
			},
			{
				name:           "sub claim with tenant: prefix",
				claims:         map[string]interface{}{"sub": "tenant:test-tenant-7"},
				expectedTenant: "test-tenant-7",
			},
			{
				name:           "sub claim with t_ prefix",
				claims:         map[string]interface{}{"sub": "t_test-tenant-8"},
				expectedTenant: "t_test-tenant-8",
			},
			{
				name:           "sub claim without prefix",
				claims:         map[string]interface{}{"sub": "test-tenant-9"},
				expectedTenant: "test-tenant-9",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				jwt := createTestJWT(tc.claims)
				resolved, err := client.ResolveTenantFromToken(jwt)
				if err != nil {
					t.Fatalf("unexpected error resolving tenant: %v", err)
				}
				if resolved != tc.expectedTenant {
					t.Fatalf("expected tenant %q, got %q", tc.expectedTenant, resolved)
				}
			})
		}
	})

	t.Run("Valid Namespace tenant tokens (nsct_...)", func(t *testing.T) {
		testCases := []struct {
			name           string
			token          string
			expectedTenant string
		}{
			{
				name:           "nsct_ with valid JWT",
				token:          "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": "my-org"}),
				expectedTenant: "my-org",
			},
			{
				name:           "st_ with valid JWT",
				token:          "st_" + createTestJWT(map[string]interface{}{"tenant_id": "isolated-tenant"}),
				expectedTenant: "isolated-tenant",
			},
			{
				name:           "nscw_ with valid JWT",
				token:          "nscw_" + createTestJWT(map[string]interface{}{"tenant_id": "tenant1"}),
				expectedTenant: "tenant1",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				resolved, err := client.ResolveTenantFromToken(tc.token)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resolved != tc.expectedTenant {
					t.Fatalf("expected %q, got %q", tc.expectedTenant, resolved)
				}
			})
		}
	})

	t.Run("Token files resolution", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Bearer token file
		bearerFile := filepath.Join(tmpDir, "bearer.json")
		jwt := createTestJWT(map[string]interface{}{"tenant_id": "file-tenant"})
		_ = os.WriteFile(bearerFile, []byte(fmt.Sprintf(`{"bearer_token": %q}`, jwt)), 0600)

		resolved, err := client.ResolveTenantFromToken(bearerFile)
		if err != nil || resolved != "file-tenant" {
			t.Fatalf("expected file-tenant from bearer file, got %q, err: %v", resolved, err)
		}

		// Session token file with nsct_ token
		sessionFile := filepath.Join(tmpDir, "session.json")
		_ = os.WriteFile(sessionFile, []byte(fmt.Sprintf(`{"session_token": "nsct_%s"}`, createTestJWT(map[string]interface{}{"tenant_id": "session-tenant"}))), 0600)

		resolved, err = client.ResolveTenantFromToken(sessionFile)
		if err != nil || resolved != "session-tenant" {
			t.Fatalf("expected session-tenant from session file, got %q, err: %v", resolved, err)
		}

		// Raw token file
		rawFile := filepath.Join(tmpDir, "raw.txt")
		_ = os.WriteFile(rawFile, []byte("nsct_"+createTestJWT(map[string]interface{}{"tenant_id": "raw-tenant"})), 0600)

		resolved, err = client.ResolveTenantFromToken(rawFile)
		if err != nil || resolved != "raw-tenant" {
			t.Fatalf("expected raw-tenant from raw file, got %q, err: %v", resolved, err)
		}
	})

	t.Run("Invalid, empty, and opaque tokens fail closed", func(t *testing.T) {
		testCases := []struct {
			name        string
			token       string
			errContains string
		}{
			{
				name:        "Empty string",
				token:       "",
				errContains: "token cannot be empty",
			},
			{
				name:        "Whitespace string",
				token:       "   \t\n  ",
				errContains: "token cannot be empty",
			},
			{
				name:        "Opaque mock token",
				token:       "mock-token",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "Opaque nsct_ token",
				token:       "nsct_my-org_1234567890abcdef",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "Opaque st_ token",
				token:       "st_session-token-opaque",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "Opaque nscw_ token",
				token:       "nscw_workspace-token-opaque",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "2-part token",
				token:       "part1.part2",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "Corrupted base64 payload",
				token:       "header.!!!not-base64!!!.sig",
				errContains: "malformed JWT payload",
			},
			{
				name:        "Corrupted JSON payload",
				token:       fmt.Sprintf("header.%s.sig", base64.RawURLEncoding.EncodeToString([]byte("not json"))),
				errContains: "malformed JWT claims JSON",
			},
			{
				name:        "JWT missing tenant claim",
				token:       createTestJWT(map[string]interface{}{"aud": "api", "exp": 123456}),
				errContains: "does not contain a supported tenant identity claim",
			},
			{
				name:        "nsct_ with empty remainder",
				token:       "nsct_",
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "nsct_ with empty identifier before underscore",
				token:       "nsct__suffix",
				errContains: "unsupported or opaque token format",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := client.ResolveTenantFromToken(tc.token)
				if err == nil {
					t.Fatalf("expected error for case %s, got nil", tc.name)
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got: %v", tc.errContains, err)
				}
			})
		}
	})
}

func TestVerifyTenantTokenMatch(t *testing.T) {
	const expected = "test-tenant"

	t.Run("Matching JWT passes", func(t *testing.T) {
		jwt := createTestJWT(map[string]interface{}{"tenant_id": expected})
		if err := client.VerifyTenantTokenMatch(jwt, expected); err != nil {
			t.Fatalf("expected matching JWT to pass, got: %v", err)
		}
	})

	t.Run("Matching nsct_ token passes", func(t *testing.T) {
		tok := "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": expected})
		if err := client.VerifyTenantTokenMatch(tok, expected); err != nil {
			t.Fatalf("expected matching nsct_ token to pass, got: %v", err)
		}
	})

	t.Run("Mismatched JWT fails with descriptive error", func(t *testing.T) {
		jwt := createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"})
		err := client.VerifyTenantTokenMatch(jwt, expected)
		if err == nil || !strings.Contains(err.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected mismatch error, got: %v", err)
		}
	})

	t.Run("Mismatched nsct_ token fails", func(t *testing.T) {
		tok := "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"})
		err := client.VerifyTenantTokenMatch(tok, expected)
		if err == nil || !strings.Contains(err.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected mismatch error for nsct_, got: %v", err)
		}
	})

	t.Run("Empty expectedTenant returns error", func(t *testing.T) {
		jwt := createTestJWT(map[string]interface{}{"tenant_id": expected})
		err := client.VerifyTenantTokenMatch(jwt, "")
		if err == nil || !strings.Contains(err.Error(), "expected tenant (NSC_TEST_TENANT) cannot be empty") {
			t.Fatalf("expected empty expectedTenant error, got: %v", err)
		}
	})

	t.Run("Empty token returns error", func(t *testing.T) {
		err := client.VerifyTenantTokenMatch("", expected)
		if err == nil || !strings.Contains(err.Error(), "token cannot be empty") {
			t.Fatalf("expected empty token error, got: %v", err)
		}
	})

	t.Run("Opaque token fails closed with unsupported format error", func(t *testing.T) {
		err := client.VerifyTenantTokenMatch("opaque-mock-token", expected)
		if err == nil || !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected opaque token error, got: %v", err)
		}
	})
}
