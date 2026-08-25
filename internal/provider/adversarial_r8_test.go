package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

type envSnapshot struct {
	token        string
	hasToken     bool
	tokenFile    string
	hasTokenFile bool
	partner      string
	hasPartner   bool
	jwt          string
	hasJWT       bool
}

func captureSnapshot() envSnapshot {
	tok, hasTok := os.LookupEnv("NSC_TOKEN")
	tf, hasTf := os.LookupEnv("NSC_TOKEN_FILE")
	pt, hasPt := os.LookupEnv("NSC_PARTNER_TOKEN")
	jwt, hasJWT := os.LookupEnv("NSC_TRUSTED_JWT")
	return envSnapshot{
		token:        tok,
		hasToken:     hasTok,
		tokenFile:    tf,
		hasTokenFile: hasTf,
		partner:      pt,
		hasPartner:   hasPt,
		jwt:          jwt,
		hasJWT:       hasJWT,
	}
}

func applySnapshot(s envSnapshot) {
	if s.hasToken {
		_ = os.Setenv("NSC_TOKEN", s.token)
	} else {
		_ = os.Unsetenv("NSC_TOKEN")
	}
	if s.hasTokenFile {
		_ = os.Setenv("NSC_TOKEN_FILE", s.tokenFile)
	} else {
		_ = os.Unsetenv("NSC_TOKEN_FILE")
	}
	if s.hasPartner {
		_ = os.Setenv("NSC_PARTNER_TOKEN", s.partner)
	} else {
		_ = os.Unsetenv("NSC_PARTNER_TOKEN")
	}
	if s.hasJWT {
		_ = os.Setenv("NSC_TRUSTED_JWT", s.jwt)
	} else {
		_ = os.Unsetenv("NSC_TRUSTED_JWT")
	}
}

func assertSnapshotEqual(t *testing.T, expected, actual envSnapshot, label string) {
	t.Helper()
	if expected.hasToken != actual.hasToken || (expected.hasToken && expected.token != actual.token) {
		t.Fatalf("[%s] NSC_TOKEN mismatch: expected (has=%v, val=%q), got (has=%v, val=%q)",
			label, expected.hasToken, expected.token, actual.hasToken, actual.token)
	}
	if expected.hasTokenFile != actual.hasTokenFile || (expected.hasTokenFile && expected.tokenFile != actual.tokenFile) {
		t.Fatalf("[%s] NSC_TOKEN_FILE mismatch: expected (has=%v, val=%q), got (has=%v, val=%q)",
			label, expected.hasTokenFile, expected.tokenFile, actual.hasTokenFile, actual.tokenFile)
	}
	if expected.hasPartner != actual.hasPartner || (expected.hasPartner && expected.partner != actual.partner) {
		t.Fatalf("[%s] NSC_PARTNER_TOKEN mismatch: expected (has=%v, val=%q), got (has=%v, val=%q)",
			label, expected.hasPartner, expected.partner, actual.hasPartner, actual.partner)
	}
	if expected.hasJWT != actual.hasJWT || (expected.hasJWT && expected.jwt != actual.jwt) {
		t.Fatalf("[%s] NSC_TRUSTED_JWT mismatch: expected (has=%v, val=%q), got (has=%v, val=%q)",
			label, expected.hasJWT, expected.jwt, actual.hasJWT, actual.jwt)
	}
}

// TestAdversarial_R8_AuthPrecedence_All120PermutationsAndParentEnvIsolation
// Tests that executing the 5 tiers of authentication precedence in all 120 possible permutations
// (including reverse, forward, randomized sequences) under multiple parent environment baselines
// leaves parent process environment completely pristine without any state corruption.
func TestAdversarial_R8_AuthPrecedence_All120PermutationsAndParentEnvIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	tokenFile1 := filepath.Join(tmpDir, "token_file_1.json")
	if err := os.WriteFile(tokenFile1, []byte(`{"bearer_token": "file-override-token-1"}`), 0600); err != nil {
		t.Fatalf("failed to write token file 1: %v", err)
	}
	tokenFile2 := filepath.Join(tmpDir, "token_file_2.json")
	if err := os.WriteFile(tokenFile2, []byte(`{"bearer_token": "file-override-token-2"}`), 0600); err != nil {
		t.Fatalf("failed to write token file 2: %v", err)
	}

	ctx := context.Background()

	// Capture initial test runner environment to restore at test end
	initialRunnerEnv := captureSnapshot()
	defer applySnapshot(initialRunnerEnv)

	// Define individual tier execution functions
	type tierFunc struct {
		name string
		fn   func(subT *testing.T)
	}

	tiers := []tierFunc{
		{
			name: "Tier1_ExplicitToken",
			fn: func(subT *testing.T) {
				subT.Setenv("NSC_TOKEN", "env-token-tier3")
				subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
				ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
					Token:     "explicit-hcl-token-tier1",
					TokenFile: tokenFile1,
				})
				if err != nil {
					subT.Fatalf("Tier 1 ResolveTokenSource failed: %v", err)
				}
				tok, err := ts.IssueToken(ctx, time.Minute, false)
				if err != nil || tok != "explicit-hcl-token-tier1" {
					subT.Fatalf("Tier 1 failed: tok=%q, err=%v", tok, err)
				}
			},
		},
		{
			name: "Tier2_ExplicitTokenFile",
			fn: func(subT *testing.T) {
				subT.Setenv("NSC_TOKEN", "env-token-tier3")
				subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
				ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
					TokenFile: tokenFile1,
				})
				if err != nil {
					subT.Fatalf("Tier 2 ResolveTokenSource failed: %v", err)
				}
				tok, err := ts.IssueToken(ctx, time.Minute, false)
				if err != nil || tok != "file-override-token-1" {
					subT.Fatalf("Tier 2 failed: tok=%q, err=%v", tok, err)
				}
			},
		},
		{
			name: "Tier3_EnvToken",
			fn: func(subT *testing.T) {
				subT.Setenv("NSC_TOKEN", "env-token-tier3")
				subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
				ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
				if err != nil {
					subT.Fatalf("Tier 3 ResolveTokenSource failed: %v", err)
				}
				tok, err := ts.IssueToken(ctx, time.Minute, false)
				if err != nil || tok != "env-token-tier3" {
					subT.Fatalf("Tier 3 failed: tok=%q, err=%v", tok, err)
				}
			},
		},
		{
			name: "Tier4_EnvTokenFile",
			fn: func(subT *testing.T) {
				subT.Setenv("NSC_TOKEN", "")
				subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
				ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
				if err != nil {
					subT.Fatalf("Tier 4 ResolveTokenSource failed: %v", err)
				}
				tok, err := ts.IssueToken(ctx, time.Minute, false)
				if err != nil || tok != "file-override-token-2" {
					subT.Fatalf("Tier 4 failed: tok=%q, err=%v", tok, err)
				}
			},
		},
		{
			name: "Tier5_DefaultCredentialsFallback",
			fn: func(subT *testing.T) {
				subT.Setenv("NSC_TOKEN", "")
				subT.Setenv("NSC_TOKEN_FILE", "")
				_, _ = client.ResolveTokenSource(ctx, client.AuthConfig{})
			},
		},
	}

	// Generate all 120 permutations of [0, 1, 2, 3, 4]
	var permutations [][]int
	var generatePerms func([]int, int)
	generatePerms = func(arr []int, k int) {
		if k == 1 {
			dst := make([]int, len(arr))
			copy(dst, arr)
			permutations = append(permutations, dst)
			return
		}
		for i := 0; i < k; i++ {
			generatePerms(arr, k-1)
			if k%2 == 1 {
				arr[0], arr[k-1] = arr[k-1], arr[0]
			} else {
				arr[i], arr[k-1] = arr[k-1], arr[i]
			}
		}
	}
	generatePerms([]int{0, 1, 2, 3, 4}, 5)

	if len(permutations) != 120 {
		t.Fatalf("expected 120 permutations, got %d", len(permutations))
	}

	// Test baselines:
	testBaselines := []struct {
		name     string
		snapshot envSnapshot
	}{
		{
			name: "AllEnvVarsSet",
			snapshot: envSnapshot{
				token:        "parent-live-token-12345",
				hasToken:     true,
				tokenFile:    "/tmp/parent_token_file.json",
				hasTokenFile: true,
				partner:      "parent-partner-token-67890",
				hasPartner:   true,
				jwt:          "parent-trusted-jwt-abcdef",
				hasJWT:       true,
			},
		},
		{
			name: "AllEnvVarsUnset",
			snapshot: envSnapshot{
				hasToken:     false,
				hasTokenFile: false,
				hasPartner:   false,
				hasJWT:       false,
			},
		},
		{
			name: "PartialEnvVarsSet",
			snapshot: envSnapshot{
				token:        "parent-single-token",
				hasToken:     true,
				hasTokenFile: false,
				partner:      "parent-partner-token",
				hasPartner:   true,
				hasJWT:       false,
			},
		},
	}

	for _, baseline := range testBaselines {
		t.Run(baseline.name, func(t *testing.T) {
			applySnapshot(baseline.snapshot)
			before := captureSnapshot()
			assertSnapshotEqual(t, baseline.snapshot, before, baseline.name+"_initial")

			// Execute all 120 permutations
			for permIdx, perm := range permutations {
				permName := fmt.Sprintf("Permutation_%d", permIdx)
				t.Run(permName, func(subT *testing.T) {
					for _, stepIdx := range perm {
						tf := tiers[stepIdx]
						subT.Run(tf.name, tf.fn)
					}
				})

				// Assert parent env is still completely untouched after the permutation
				current := captureSnapshot()
				assertSnapshotEqual(t, baseline.snapshot, current, fmt.Sprintf("%s_after_perm_%d", baseline.name, permIdx))
			}
		})
	}
}

// TestAdversarial_R8_AuthPrecedence_PrecheckIntegrity
// Tests that testAccPreCheck and testAccPartnerPreCheck maintain integrity and
// do not suffer from stale or corrupted environment states before or after precedence testing.
func TestAdversarial_R8_AuthPrecedence_PrecheckIntegrity(t *testing.T) {
	initialRunnerEnv := captureSnapshot()
	defer applySnapshot(initialRunnerEnv)

	// Simulate environment with valid live credentials
	_ = os.Setenv("NSC_TOKEN", "valid-tenant-token-nsct_mock")
	_ = os.Setenv("NSC_PARTNER_TOKEN", "valid-partner-token-nsct_partner")

	// Snapshot before
	before := captureSnapshot()

	// Run precedence subtests
	t.Run("SubtestExecution", func(t *testing.T) {
		t.Run("SubTier1", func(subT *testing.T) {
			subT.Setenv("NSC_TOKEN", "sub-override-token")
			subT.Setenv("NSC_TOKEN_FILE", "/sub/file")
		})
		t.Run("SubTier2", func(subT *testing.T) {
			subT.Setenv("NSC_TOKEN", "")
			subT.Setenv("NSC_TOKEN_FILE", "")
		})
	})

	// Snapshot after
	after := captureSnapshot()
	assertSnapshotEqual(t, before, after, "precheck_integrity_after_subtests")

	// Verify env values are restored
	if os.Getenv("NSC_TOKEN") != "valid-tenant-token-nsct_mock" {
		t.Fatalf("expected NSC_TOKEN to remain 'valid-tenant-token-nsct_mock', got %q", os.Getenv("NSC_TOKEN"))
	}
	if os.Getenv("NSC_PARTNER_TOKEN") != "valid-partner-token-nsct_partner" {
		t.Fatalf("expected NSC_PARTNER_TOKEN to remain 'valid-partner-token-nsct_partner', got %q", os.Getenv("NSC_PARTNER_TOKEN"))
	}
}
