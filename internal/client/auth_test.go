package client_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

func TestExtractClaims(t *testing.T) {
	t.Run("Valid st_, nsct_, nscw_ JWTs with integer and fractional exp", func(t *testing.T) {
		expSec := int64(1893456000)
		for _, prefix := range []string{"st_", "nsct_", "nscw_"} {
			jwt := createTestJWT(map[string]interface{}{
				"exp":             expSec,
				"tenant_id":       "tenant-123",
				"actor_id":        "actor-456",
				"instance_id":     "inst-789",
				"owner_id":        "owner-abc",
				"primary_region":  "eu",
				"workload_region": "us",
			})
			claims, err := client.ExtractClaims(prefix + jwt)
			if err != nil {
				t.Fatalf("ExtractClaims(%s...) failed: %v", prefix, err)
			}
			if claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != expSec {
				t.Fatalf("expected ExpiresAt unix=%d, got %v", expSec, claims.ExpiresAt)
			}
			if claims.TenantID != "tenant-123" || claims.ActorID != "actor-456" || claims.InstanceID != "inst-789" ||
				claims.OwnerID != "owner-abc" || claims.PrimaryRegion != "eu" || claims.WorkloadRegion != "us" {
				t.Fatalf("unexpected claims parsed: %+v", claims)
			}
		}

		// Fractional exp numeric date
		fracJWT := createTestJWT(map[string]interface{}{
			"exp":       1893456000.5,
			"tenant_id": "frac-tenant",
		})
		claims, err := client.ExtractClaims("nsct_" + fracJWT)
		if err != nil {
			t.Fatalf("ExtractClaims fractional exp failed: %v", err)
		}
		if claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != 1893456000 || claims.ExpiresAt.Nanosecond() == 0 {
			t.Fatalf("expected fractional ExpiresAt, got %v", claims.ExpiresAt)
		}
	})

	t.Run("Missing exp and null exp yield nil ExpiresAt", func(t *testing.T) {
		noExp := "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": "t1"})
		claims, err := client.ExtractClaims(noExp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.ExpiresAt != nil {
			t.Fatalf("expected nil ExpiresAt when missing, got %v", claims.ExpiresAt)
		}

		nullExp := "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": "t1", "exp": nil})
		claims, err = client.ExtractClaims(nullExp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.ExpiresAt != nil {
			t.Fatalf("expected nil ExpiresAt when null, got %v", claims.ExpiresAt)
		}
	})

	t.Run("Unsupported prefixes and empty string return ErrNotLoggedIn", func(t *testing.T) {
		bareJWT := createTestJWT(map[string]interface{}{"tenant_id": "t1"})
		unsupported := []string{
			"",
			bareJWT,
			"nsrt_" + bareJWT,
			"nscu_" + bareJWT,
			"tok_" + bareJWT,
		}
		for _, tok := range unsupported {
			_, err := client.ExtractClaims(tok)
			if !errors.Is(err, client.ErrNotLoggedIn) {
				t.Fatalf("expected ErrNotLoggedIn for %q, got %v", tok, err)
			}
		}
	})

	t.Run("Malformed tokens return ErrNotLoggedIn", func(t *testing.T) {
		validHdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		validPay := base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"t"}`))
		badHeader := "!!!" + "." + validPay + ".sig"
		nonJSONHeader := base64.RawURLEncoding.EncodeToString([]byte("not-json")) + "." + validPay + ".sig"
		nullHeader := base64.RawURLEncoding.EncodeToString([]byte("null")) + "." + validPay + ".sig"
		badPayload := validHdr + ".!!!.sig"
		nonJSONPayload := validHdr + "." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig"
		nullPayload := validHdr + "." + base64.RawURLEncoding.EncodeToString([]byte("null")) + ".sig"
		arrayPayload := validHdr + "." + base64.RawURLEncoding.EncodeToString([]byte("[]")) + ".sig"
		emptySig := validHdr + "." + validPay + "."
		internalSpace := " " + validHdr + " . " + validPay + " . sig"
		nonNumericExp := createTestJWT(map[string]interface{}{"exp": "not-a-number", "tenant_id": "t"})
		quotedNumericExp := createTestJWT(map[string]interface{}{"exp": "1893456000", "tenant_id": "t"})
		overflowFloatExp := validHdr + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1e20,"tenant_id":"t"}`)) + ".sig"
		underflowFloatExp := validHdr + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":-1e20,"tenant_id":"t"}`)) + ".sig"
		overflowIntExp := validHdr + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":9223372036854775808,"tenant_id":"t"}`)) + ".sig"

		malformed := []string{
			"st_",
			"st_not-a-jwt",
			"nsct_a.b",
			"nsct_" + badHeader,
			"nsct_" + nonJSONHeader,
			"nsct_" + nullHeader,
			"nsct_" + badPayload,
			"nsct_" + nonJSONPayload,
			"nsct_" + nullPayload,
			"nsct_" + arrayPayload,
			"nsct_" + emptySig,
			"nsct_" + internalSpace,
			"nsct_" + nonNumericExp,
			"nsct_" + quotedNumericExp,
			"nsct_" + overflowFloatExp,
			"nsct_" + underflowFloatExp,
			"nsct_" + overflowIntExp,
		}
		for _, tok := range malformed {
			_, err := client.ExtractClaims(tok)
			if !errors.Is(err, client.ErrNotLoggedIn) {
				t.Fatalf("expected ErrNotLoggedIn for malformed %q, got %v", tok, err)
			}
		}
	})

	t.Run("Int64 boundary exp values succeed cleanly", func(t *testing.T) {
		for _, expVal := range []int64{math.MaxInt64, math.MinInt64} {
			tok := "nsct_" + createTestJWT(map[string]interface{}{"exp": expVal, "tenant_id": "t"})
			claims, err := client.ExtractClaims(tok)
			if err != nil {
				t.Fatalf("ExtractClaims(exp=%d) failed: %v", expVal, err)
			}
			if claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != expVal {
				t.Fatalf("expected ExpiresAt=%d, got %v", expVal, claims.ExpiresAt)
			}
		}
	})

	t.Run("Control characters, non-ASCII spaces, and invalid signature segments return ErrNotLoggedIn", func(t *testing.T) {
		hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		pay := base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"t"}`))
		badTokens := []string{
			"nsct_" + hdr + "\v." + pay + ".sig",
			"nsct_" + hdr + "." + pay + ".sig\v",
			"nsct_" + hdr + "\f." + pay + ".sig",
			"nsct_" + hdr + "\u00a0." + pay + ".sig",
			"nsct_" + hdr + "\x00." + pay + ".sig",
			"nsct_" + hdr + "\x7f." + pay + ".sig",
			"nsct_" + hdr + "." + pay + ".bad sig!",
			"nsct_" + hdr + "." + pay + ".bad/sig+",
			"nsct_" + hdr + "." + pay + ".==",
		}
		for _, bad := range badTokens {
			if _, err := client.ExtractClaims(bad); !errors.Is(err, client.ErrNotLoggedIn) {
				t.Fatalf("expected ErrNotLoggedIn for %q, got %v", bad, err)
			}
		}

		// Padded base64url segments remain accepted when valid
		paddedHdr := base64.URLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		paddedPay := base64.URLEncoding.EncodeToString([]byte(`{"tenant_id":"padded-t"}`))
		paddedSig := base64.URLEncoding.EncodeToString([]byte("sig!"))
		claims, err := client.ExtractClaims("nsct_" + paddedHdr + "." + paddedPay + "." + paddedSig)
		if err != nil || claims.TenantID != "padded-t" {
			t.Fatalf("expected padded valid JWT to succeed, got claims=%+v err=%v", claims, err)
		}
	})
}

func TestResolveTokenSource_SessionTokenExchange(t *testing.T) {
	origRTS := client.RequireTransportSecurity
	client.RequireTransportSecurity = false
	t.Cleanup(func() {
		client.RequireTransportSecurity = origRTS
	})

	ctx := context.Background()

	t.Run("Raw st_ token file is validated and exchanged via UserSessionsService", func(t *testing.T) {
		mockIAM := startMockIAMServer(t, "raw-st-file")
		tmpDir := t.TempDir()
		rawSTFile := filepath.Join(tmpDir, "session.tok")
		sessionTok := "st_" + "raw_file_session_token_123456"
		if err := os.WriteFile(rawSTFile, []byte(sessionTok+"\n"), 0600); err != nil {
			t.Fatalf("failed to write raw st_ file: %v", err)
		}

		ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			TokenFile:   rawSTFile,
			IAMEndpoint: mockIAM.Endpoint(),
			Creds:       insecure.NewCredentials(),
			DialOptions: []grpc.DialOption{mockIAM.DialOption()},
		})
		if err != nil {
			t.Fatalf("expected raw st_ token file to pass validation, got: %v", err)
		}
		if closer, ok := ts.(interface{ Close() error }); ok {
			t.Cleanup(func() { _ = closer.Close() })
		}

		tok, err := ts.IssueToken(ctx, time.Minute, false)
		if err != nil {
			t.Fatalf("expected raw st_ IssueToken exchange to succeed, got: %v", err)
		}
		if !strings.HasPrefix(tok, "nsct_") {
			t.Fatalf("expected exchanged nsct_ tenant token, got %q", tok)
		}
		if mockIAM.requestCt.Load() != 1 {
			t.Fatalf("expected 1 IAM exchange request, got %d", mockIAM.requestCt.Load())
		}
	})

	t.Run("Invalid st_ literal and malformed JSON still fail validation", func(t *testing.T) {
		tmpDir := t.TempDir()
		bareST := filepath.Join(tmpDir, "bare_st.tok")
		_ = os.WriteFile(bareST, []byte("st_"), 0600)
		if _, err := client.ResolveTokenSource(ctx, client.AuthConfig{TokenFile: bareST}); err == nil || !strings.Contains(err.Error(), "contains malformed JSON") {
			t.Fatalf("expected bare st_ file to fail with malformed JSON error, got: %v", err)
		}

		spacedST := filepath.Join(tmpDir, "spaced_st.tok")
		_ = os.WriteFile(spacedST, []byte("st_foo bar"), 0600)
		if _, err := client.ResolveTokenSource(ctx, client.AuthConfig{TokenFile: spacedST}); err == nil || !strings.Contains(err.Error(), "contains malformed JSON") {
			t.Fatalf("expected spaced st_ file to fail with malformed JSON error, got: %v", err)
		}
	})

	t.Run("Inline st_ via cfg.Token wraps in SessionTokenSource and exchanges", func(t *testing.T) {
		mockIAM := startMockIAMServer(t, "inline-cfg-st")
		sessionTok := "st_" + "inline_cfg_session_token_123456"

		ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			Token:       sessionTok,
			IAMEndpoint: mockIAM.Endpoint(),
			Creds:       insecure.NewCredentials(),
			DialOptions: []grpc.DialOption{mockIAM.DialOption()},
		})
		if err != nil {
			t.Fatalf("unexpected ResolveTokenSource error: %v", err)
		}
		sts, ok := ts.(*client.SessionTokenSource)
		if !ok {
			t.Fatalf("expected *client.SessionTokenSource for inline st_ cfg.Token, got %T", ts)
		}
		t.Cleanup(func() { _ = sts.Close() })

		tok, err := ts.IssueToken(ctx, time.Minute, false)
		if err != nil {
			t.Fatalf("expected inline st_ exchange to succeed, got: %v", err)
		}
		if !strings.HasPrefix(tok, "nsct_") || mockIAM.requestCt.Load() != 1 {
			t.Fatalf("expected exchanged nsct_ token with 1 IAM call, got tok=%q calls=%d", tok, mockIAM.requestCt.Load())
		}
	})

	t.Run("Inline st_ via NSC_TOKEN wraps in SessionTokenSource and exchanges", func(t *testing.T) {
		mockIAM := startMockIAMServer(t, "inline-env-st")
		sessionTok := "st_" + "inline_env_session_token_123456"
		t.Setenv("NSC_TOKEN", sessionTok)

		ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			IAMEndpoint: mockIAM.Endpoint(),
			Creds:       insecure.NewCredentials(),
			DialOptions: []grpc.DialOption{mockIAM.DialOption()},
		})
		if err != nil {
			t.Fatalf("unexpected ResolveTokenSource error: %v", err)
		}
		sts, ok := ts.(*client.SessionTokenSource)
		if !ok {
			t.Fatalf("expected *client.SessionTokenSource for inline st_ NSC_TOKEN, got %T", ts)
		}
		t.Cleanup(func() { _ = sts.Close() })

		tok, err := ts.IssueToken(ctx, time.Minute, false)
		if err != nil {
			t.Fatalf("expected env inline st_ exchange to succeed, got: %v", err)
		}
		if !strings.HasPrefix(tok, "nsct_") || mockIAM.requestCt.Load() != 1 {
			t.Fatalf("expected exchanged nsct_ token with 1 IAM call, got tok=%q calls=%d", tok, mockIAM.requestCt.Load())
		}
	})

	t.Run("Inline nsct_ and nsrt_ remain StaticTokenSource", func(t *testing.T) {
		for _, tok := range []string{"nsct_" + "static_bearer_123456", "nsrt_" + "static_revokable_123456"} {
			ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{Token: tok})
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tok, err)
			}
			if _, ok := ts.(client.StaticTokenSource); !ok {
				t.Fatalf("expected StaticTokenSource for %q, got %T", tok, ts)
			}
		}
	})
}
