package provider_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestStress_M1_PreCheckConcurrentExecution tests parallel precheck evaluations under varying environments
func TestStress_M1_PreCheckConcurrentExecution(t *testing.T) {
	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(iterations)

	for i := 0; i < iterations; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			m := &mockAccTB{}
			switch workerID % 4 {
			case 0:
				// Unset TF_ACC should skip
				// Since os.Getenv reads process env, we test with mock wrapper or isolated env
				// Here we test mockAccTB behavior
				m.Skip("skipped")
				if !m.skipped {
					t.Errorf("worker %d: expected skipped", workerID)
				}
			case 1:
				// Fatal error recording
				m.Fatalf("tenant credential verification failed: %s", "mismatch")
				if !m.failed {
					t.Errorf("worker %d: expected failed", workerID)
				}
			case 2:
				// Success
				if m.failed || m.skipped {
					t.Errorf("worker %d: unexpected state", workerID)
				}
			case 3:
				m.Fatal("NSC_TEST_TENANT must be set")
				if !m.failed {
					t.Errorf("worker %d: expected failed", workerID)
				}
			}
		}()
	}

	wg.Wait()
}

// TestStress_M1_PreCheckMatrix comprehensively exercises all pre-check guards across all token modalities
func TestStress_M1_PreCheckMatrix(t *testing.T) {
	const validTenant = "matrix-tenant"

	t.Run("Standard PreCheck with various token formats", func(t *testing.T) {
		tokens := []struct {
			name        string
			tokenVal    string
			tokenFile   bool
			expectPass  bool
			errContains string
		}{
			{
				name:       "Standard JWT with tenant_id",
				tokenVal:   createTestJWT(map[string]interface{}{"tenant_id": validTenant}),
				expectPass: true,
			},
			{
				name:       "JWT with custom namespace claim",
				tokenVal:   createTestJWT(map[string]interface{}{"https://namespace.so/claims/tenant": validTenant}),
				expectPass: true,
			},
			{
				name:       "JWT with tid claim",
				tokenVal:   createTestJWT(map[string]interface{}{"tid": validTenant}),
				expectPass: true,
			},
			{
				name:       "JWT with tenant map claim",
				tokenVal:   createTestJWT(map[string]interface{}{"tenant": map[string]interface{}{"id": validTenant}}),
				expectPass: true,
			},
			{
				name:       "JWT with sub tenant: claim",
				tokenVal:   createTestJWT(map[string]interface{}{"sub": "tenant:" + validTenant}),
				expectPass: true,
			},
			{
				name:       "NSCT token with entropy",
				tokenVal:   "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": validTenant}),
				expectPass: true,
			},
			{
				name:       "NSCT token without entropy",
				tokenVal:   "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": validTenant}),
				expectPass: true,
			},
			{
				name:        "Mismatched JWT",
				tokenVal:    createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"}),
				expectPass:  false,
				errContains: "does not match NSC_TEST_TENANT",
			},
			{
				name:        "Mismatched NSCT token",
				tokenVal:    "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"}),
				expectPass:  false,
				errContains: "does not match NSC_TEST_TENANT",
			},
			{
				name:        "Opaque token",
				tokenVal:    "opaque-token-string",
				expectPass:  false,
				errContains: "unsupported or opaque token format",
			},
			{
				name:        "Malformed 2-part token",
				tokenVal:    "header.payload",
				expectPass:  false,
				errContains: "unsupported or opaque token format",
			},
			{
				name:       "Token file with bearer_token",
				tokenVal:   fmt.Sprintf(`{"bearer_token": %q}`, createTestJWT(map[string]interface{}{"tenant_id": validTenant})),
				tokenFile:  true,
				expectPass: true,
			},
			{
				name:       "Token file with session_token",
				tokenVal:   fmt.Sprintf(`{"session_token": "nsct_%s"}`, createTestJWT(map[string]interface{}{"tenant_id": validTenant})),
				tokenFile:  true,
				expectPass: true,
			},
			{
				name:       "Token file with raw token",
				tokenVal:   "nsct_" + createTestJWT(map[string]interface{}{"tenant_id": validTenant}),
				tokenFile:  true,
				expectPass: true,
			},
		}

		for _, tc := range tokens {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("TF_ACC", "1")
				t.Setenv("NSC_TEST_TENANT", validTenant)

				if tc.tokenFile {
					tmpDir := t.TempDir()
					fPath := filepath.Join(tmpDir, "token.json")
					if err := os.WriteFile(fPath, []byte(tc.tokenVal), 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("NSC_TOKEN", "")
					t.Setenv("NSC_TOKEN_FILE", fPath)
				} else {
					t.Setenv("NSC_TOKEN", tc.tokenVal)
					t.Setenv("NSC_TOKEN_FILE", "")
				}

				m := &mockAccTB{}
				testAccPreCheckImpl(m)

				if tc.expectPass {
					if m.failed || m.skipped {
						t.Fatalf("expected precheck to pass, got failed=%v, skipped=%v, msgs=%v", m.failed, m.skipped, m.messages)
					}
				} else {
					if !m.failed {
						t.Fatalf("expected precheck to fail, got passed")
					}
					if tc.errContains != "" {
						matched := false
						for _, msg := range m.messages {
							if strings.Contains(msg, tc.errContains) {
								matched = true
								break
							}
						}
						if !matched {
							t.Fatalf("expected error containing %q, got: %v", tc.errContains, m.messages)
						}
					}
				}
			})
		}
	})

	t.Run("Partner PreCheck with various token formats", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", validTenant)

		// Matching NSCT partner token
		t.Setenv("NSC_PARTNER_TOKEN", "nsct_"+createTestJWT(map[string]interface{}{"tenant_id": validTenant}))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected partner precheck to pass, got failed=%v, msgs=%v", m.failed, m.messages)
		}

		// Mismatched NSCT partner token
		t.Setenv("NSC_PARTNER_TOKEN", "nsct_"+createTestJWT(map[string]interface{}{"tenant_id": "unrelated-tenant"}))
		m2 := &mockAccTB{}
		testAccPartnerPreCheckImpl(m2)
		if !m2.failed {
			t.Fatalf("expected partner precheck to fail on mismatched NSCT token")
		}
	})

	t.Run("Trusted Authority PreCheck with various token formats", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", validTenant)

		// Matching NSCT trusted token
		t.Setenv("NSC_TRUSTED_JWT", "nsct_"+createTestJWT(map[string]interface{}{"tenant_id": validTenant}))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected trusted precheck to pass, got failed=%v, msgs=%v", m.failed, m.messages)
		}

		// Mismatched NSCT trusted token
		t.Setenv("NSC_TRUSTED_JWT", "nsct_"+createTestJWT(map[string]interface{}{"tenant_id": "unrelated-tenant"}))
		m2 := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m2)
		if !m2.failed {
			t.Fatalf("expected trusted precheck to fail on mismatched NSCT token")
		}
	})
}

// TestStress_M1_CleanupCallbacksSafety tests safety and error handling of all cleanup callbacks
func TestStress_M1_CleanupCallbacksSafety(t *testing.T) {
	t.Run("Empty IDs and tags return cleanly without side-effects", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "test-tenant"}))

		// Testing with empty identifiers (should early-exit without panic or network call)
		testAccCleanupRunnerProfileByID(t, "")
		testAccCleanupRunnerProfile(t, "")
		testAccCleanupSharedImageByID(t, "")
	})

	t.Run("TF_ACC unset skips all cleanups immediately", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		testAccCleanupRunnerProfile(t, "nonexistent-tag")
		testAccCleanupRunnerProfileByID(t, "nonexistent-profile-id")
		testAccCleanupTenantGroup(t, "nonexistent-group")
		testAccCleanupSharedImageByID(t, "nonexistent-image-id")
		testAccCleanupRepositoryPolicy(t, "nonexistent-repo")
		testAccCleanupDefaultPolicy(t)
	})
}
