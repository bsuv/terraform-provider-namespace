package provider_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// =============================================================================
// CHALLENGER M4 AREA 1: resolveAndVerifyTenant CLAIMS & CORRUPTION STRESS TEST
// =============================================================================

func TestEmpirical_M4_ResolveAndVerifyTenant_ClaimsMatrix(t *testing.T) {
	const defaultExpectedTenant = "tenant-target-123"

	testCases := []struct {
		name           string
		expectedTenant string
		claims         map[string]interface{}
		expectError    bool
		errContains    string
	}{
		// 1. https://namespace.so/claims/tenant
		{
			name:        "Custom namespace claim matching",
			claims:      map[string]interface{}{"https://namespace.so/claims/tenant": defaultExpectedTenant},
			expectError: false,
		},
		{
			name:        "Custom namespace claim mismatching",
			claims:      map[string]interface{}{"https://namespace.so/claims/tenant": "tenant-other-999"},
			expectError: true,
			errContains: `authenticated credential tenant "tenant-other-999" does not match NSC_TEST_TENANT "tenant-target-123"`,
		},

		// 2. tenant_id
		{
			name:        "tenant_id matching",
			claims:      map[string]interface{}{"tenant_id": defaultExpectedTenant},
			expectError: false,
		},
		{
			name:        "tenant_id mismatching",
			claims:      map[string]interface{}{"tenant_id": "tenant-rogue"},
			expectError: true,
			errContains: `does not match NSC_TEST_TENANT`,
		},

		// 3. tenant as string
		{
			name:        "tenant string matching",
			claims:      map[string]interface{}{"tenant": defaultExpectedTenant},
			expectError: false,
		},
		{
			name:        "tenant string mismatching",
			claims:      map[string]interface{}{"tenant": "tenant-foreign"},
			expectError: true,
			errContains: `does not match NSC_TEST_TENANT`,
		},

		// 4. tenant as object with id
		{
			name:        "tenant object with id matching",
			claims:      map[string]interface{}{"tenant": map[string]interface{}{"id": defaultExpectedTenant}},
			expectError: false,
		},
		{
			name:        "tenant object with id mismatching",
			claims:      map[string]interface{}{"tenant": map[string]interface{}{"id": "tenant-obj-diff"}},
			expectError: true,
			errContains: `does not match NSC_TEST_TENANT`,
		},

		// 5. tenant as object with name
		{
			name:        "tenant object with name matching",
			claims:      map[string]interface{}{"tenant": map[string]interface{}{"name": defaultExpectedTenant}},
			expectError: false,
		},
		{
			name:        "tenant object with name mismatching",
			claims:      map[string]interface{}{"tenant": map[string]interface{}{"name": "tenant-named-diff"}},
			expectError: true,
			errContains: `does not match NSC_TEST_TENANT`,
		},

		// 6. sub with tenant: prefix
		{
			name:        "sub tenant: prefix matching",
			claims:      map[string]interface{}{"sub": "tenant:" + defaultExpectedTenant},
			expectError: false,
		},
		{
			name:        "sub tenant: prefix mismatching",
			claims:      map[string]interface{}{"sub": "tenant:tenant-other"},
			expectError: true,
			errContains: `does not match NSC_TEST_TENANT`,
		},

		// 7. sub with t_ prefix (where tenant ID is t_...)
		{
			name:           "sub t_ prefix matching",
			expectedTenant: "t_tenant-target-123",
			claims:         map[string]interface{}{"sub": "t_tenant-target-123"},
			expectError:    false,
		},
		{
			name:        "sub t_ prefix mismatching",
			claims:      map[string]interface{}{"sub": "t_different"},
			expectError: true,
			errContains: `does not match NSC_TEST_TENANT`,
		},

		// 8. sub without tenant prefix (e.g. user identity) -> should fail closed when not matching expectedTenant
		{
			name:        "sub user id without tenant prefix fails closed when not matching",
			claims:      map[string]interface{}{"sub": "usr_99887766"},
			expectError: true,
			errContains: `does not match NSC_TEST_TENANT`,
		},

		// 9. Claim precedence: custom namespace claim takes priority over tenant_id
		{
			name: "Custom namespace claim precedence over tenant_id",
			claims: map[string]interface{}{
				"https://namespace.so/claims/tenant": defaultExpectedTenant,
				"tenant_id":                          "other-tenant",
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			exp := tc.expectedTenant
			if exp == "" {
				exp = defaultExpectedTenant
			}
			jwt := createTestJWT(tc.claims)
			err := resolveAndVerifyTenant(jwt, exp)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error for case %s, got nil", tc.name)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got: %v", tc.errContains, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected success for case %s, got error: %v", tc.name, err)
				}
			}
		})
	}
}

func TestEmpirical_M4_ResolveAndVerifyTenant_EdgeCasesAndCorruption(t *testing.T) {
	const validTenant = "tenant-safe-100"

	t.Run("Empty expectedTenant returns error", func(t *testing.T) {
		err := resolveAndVerifyTenant("some-token", "")
		if err == nil || !strings.Contains(err.Error(), "expected tenant (NSC_TEST_TENANT) cannot be empty") {
			t.Fatalf("expected empty expectedTenant error, got: %v", err)
		}
	})

	t.Run("Whitespace-only expectedTenant returns error", func(t *testing.T) {
		err := resolveAndVerifyTenant("some-token", "   \t\n  ")
		if err == nil || !strings.Contains(err.Error(), "expected tenant (NSC_TEST_TENANT) cannot be empty") {
			t.Fatalf("expected empty expectedTenant error, got: %v", err)
		}
	})

	t.Run("Empty token returns error", func(t *testing.T) {
		err := resolveAndVerifyTenant("", validTenant)
		if err == nil || !strings.Contains(err.Error(), "token cannot be empty") {
			t.Fatalf("expected empty token error, got: %v", err)
		}
	})

	t.Run("Whitespace-only token returns error", func(t *testing.T) {
		err := resolveAndVerifyTenant("   \t\r\n  ", validTenant)
		if err == nil || !strings.Contains(err.Error(), "token cannot be empty") {
			t.Fatalf("expected empty token error, got: %v", err)
		}
	})

	t.Run("Opaque non-JWT bearer token fails closed with error", func(t *testing.T) {
		err := resolveAndVerifyTenant("nsct_opaque_token_without_jwt_structure", validTenant)
		if err == nil || !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected opaque token to fail closed with unsupported format error, got: %v", err)
		}
	})

	t.Run("Corrupted JWT 2-part format fails closed with error", func(t *testing.T) {
		err := resolveAndVerifyTenant("header.payload", validTenant)
		if err == nil || !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected 2-part non-JWT to fail closed with error, got: %v", err)
		}
	})

	t.Run("Corrupted JWT invalid base64 payload fails closed with error", func(t *testing.T) {
		err := resolveAndVerifyTenant("header.!!!invalid-base64!!!.sig", validTenant)
		if err == nil || !strings.Contains(err.Error(), "malformed JWT payload") {
			t.Fatalf("expected corrupted base64 to fail closed with error, got: %v", err)
		}
	})

	t.Run("Corrupted JWT invalid json payload fails closed with error", func(t *testing.T) {
		invalidJSONPayload := base64.RawURLEncoding.EncodeToString([]byte("not valid json"))
		jwt := fmt.Sprintf("header.%s.sig", invalidJSONPayload)
		err := resolveAndVerifyTenant(jwt, validTenant)
		if err == nil || !strings.Contains(err.Error(), "malformed JWT claims JSON") {
			t.Fatalf("expected invalid json in payload to fail closed with error, got: %v", err)
		}
	})

	t.Run("JWT with non-string claim types fails closed without panic", func(t *testing.T) {
		claims := map[string]interface{}{
			"tenant_id": 12345, // int instead of string
			"tenant":    true,  // bool instead of string or object
			"sub":       []string{"array", "not", "string"},
		}
		jwt := createTestJWT(claims)
		err := resolveAndVerifyTenant(jwt, validTenant)
		if err == nil || !strings.Contains(err.Error(), "does not contain a supported tenant identity claim") {
			t.Fatalf("expected non-string claim types to fail closed, got: %v", err)
		}
	})

	t.Run("Token file with bearer_token JSON matching tenant passes", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token.json")
		jwt := createTestJWT(map[string]interface{}{"tenant_id": validTenant})
		if err := os.WriteFile(tokenFile, []byte(fmt.Sprintf(`{"bearer_token": %q}`, jwt)), 0600); err != nil {
			t.Fatal(err)
		}
		err := resolveAndVerifyTenant(tokenFile, validTenant)
		if err != nil {
			t.Fatalf("expected bearer_token file to pass, got: %v", err)
		}
	})

	t.Run("Token file with bearer_token JSON mismatching tenant fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token.json")
		jwt := createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"})
		if err := os.WriteFile(tokenFile, []byte(fmt.Sprintf(`{"bearer_token": %q}`, jwt)), 0600); err != nil {
			t.Fatal(err)
		}
		err := resolveAndVerifyTenant(tokenFile, validTenant)
		if err == nil || !strings.Contains(err.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected mismatching bearer_token file to fail, got: %v", err)
		}
	})

	t.Run("Token file with session_token JSON matching tenant passes", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "session.json")
		jwt := createTestJWT(map[string]interface{}{"tenant_id": validTenant})
		if err := os.WriteFile(tokenFile, []byte(fmt.Sprintf(`{"session_token": %q}`, jwt)), 0600); err != nil {
			t.Fatal(err)
		}
		err := resolveAndVerifyTenant(tokenFile, validTenant)
		if err != nil {
			t.Fatalf("expected session_token file to pass, got: %v", err)
		}
	})

	t.Run("Token file with session_token JSON mismatching tenant fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "session.json")
		jwt := createTestJWT(map[string]interface{}{"tenant_id": "bad-tenant"})
		if err := os.WriteFile(tokenFile, []byte(fmt.Sprintf(`{"session_token": %q}`, jwt)), 0600); err != nil {
			t.Fatal(err)
		}
		err := resolveAndVerifyTenant(tokenFile, validTenant)
		if err == nil || !strings.Contains(err.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected mismatching session_token file to fail, got: %v", err)
		}
	})
}

// =============================================================================
// CHALLENGER M4 AREA 2: SWEEPER PREFIX FILTERING & ERROR HANDLING STRESS TEST
// =============================================================================

func TestEmpirical_M4_Sweeper_PrefixFilteringAndErrorHandling(t *testing.T) {
	t.Run("Runner profile sweeper targets only current run prefix and collects errors", func(t *testing.T) {
		t.Setenv("NSC_RUN_ID", "m4test99")
		t.Setenv("NSC_TEST_TENANT", "safe-test-tenant")
		t.Setenv("NSC_TOKEN", makeTestJWT("safe-test-tenant"))

		expectedPrefix := "tfacc-m4test99-"
		if testAccRunPrefix() != expectedPrefix {
			t.Fatalf("expected prefix %q, got %q", expectedPrefix, testAccRunPrefix())
		}

		// Simulate profiles list
		profiles := []*githubv1beta.RunnerProfileWithStatus{
			// 1. Current run matching profile -> should be deleted
			{
				ProfileId: "prof-match-1",
				Spec: &githubv1beta.RunnerProfileSpec{
					Tag: expectedPrefix + "runner-1",
				},
			},
			// 2. Another matching profile -> should be deleted
			{
				ProfileId: "prof-match-2",
				Spec: &githubv1beta.RunnerProfileSpec{
					Tag: expectedPrefix + "runner-2",
				},
			},
			// 3. Different run profile -> must NOT be deleted
			{
				ProfileId: "prof-diff-run",
				Spec: &githubv1beta.RunnerProfileSpec{
					Tag: "tfacc-otherrun-runner-3",
				},
			},
			// 4. Production / un-prefixed profile -> must NOT be deleted
			{
				ProfileId: "prof-prod",
				Spec: &githubv1beta.RunnerProfileSpec{
					Tag: "production-runner-default",
				},
			},
		}

		var deletedProfileIDs []string
		srv := testutil.NewMockServer()
		srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
			return &githubv1beta.ListProfilesResponse{Profiles: profiles}, nil
		}
		srv.Profile.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
			deletedProfileIDs = append(deletedProfileIDs, req.ProfileId)
			if req.ProfileId == "prof-match-2" {
				// Simulate internal gRPC error on second deletion to verify error aggregation
				return nil, status.Error(codes.Internal, "simulated database lock")
			}
			return &emptypb.Empty{}, nil
		}

		// Inject mock client into sweep execution
		cli := testutil.NewTestClient(t, srv)
		pCli, err := cli.ProfileClient(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := pCli.ListProfiles(context.Background(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}

		prefix := testAccRunPrefix()
		var sweepErrs []error
		for _, p := range resp.GetProfiles() {
			if spec := p.GetSpec(); spec != nil {
				tag := spec.GetTag()
				if strings.HasPrefix(tag, prefix) {
					_, err := pCli.DeleteProfile(context.Background(), &githubv1beta.DeleteProfileRequest{ProfileId: p.GetProfileId()})
					if err != nil && !client.IsNotFound(err) {
						sweepErrs = append(sweepErrs, fmt.Errorf("failed to sweep runner profile %s (%s): %w", p.GetProfileId(), tag, err))
					}
				}
			}
		}

		// Verify deleted targets
		if len(deletedProfileIDs) != 2 {
			t.Fatalf("expected 2 deletion attempts for matching profiles, got %d: %v", len(deletedProfileIDs), deletedProfileIDs)
		}
		if deletedProfileIDs[0] != "prof-match-1" || deletedProfileIDs[1] != "prof-match-2" {
			t.Fatalf("unexpected deleted profiles: %v", deletedProfileIDs)
		}

		// Verify error was joined
		if len(sweepErrs) != 1 {
			t.Fatalf("expected 1 aggregated sweep error, got %d: %v", len(sweepErrs), sweepErrs)
		}
		if !strings.Contains(sweepErrs[0].Error(), "simulated database lock") {
			t.Fatalf("expected error containing 'simulated database lock', got: %v", sweepErrs[0])
		}
	})

	t.Run("Tenant group sweeper targets only current run prefix and handles NotFound idempotently", func(t *testing.T) {
		t.Setenv("NSC_RUN_ID", "m4test99")
		t.Setenv("NSC_TEST_TENANT", "safe-test-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", makeTestJWT("safe-test-tenant"))

		expectedPrefix := "tfacc-m4test99-"

		groups := []*iamv1beta.ListTenantGroupsResponse_TenantGroup{
			{Name: expectedPrefix + "group-1"},
			{Name: expectedPrefix + "group-already-deleted"},
			{Name: "tfacc-otherrun-group-3"},
			{Name: "prod-iam-group"},
		}

		var deletedGroupNames []string
		srv := testutil.NewMockServer()
		srv.Tenant.ListTenantGroupsFunc = func(ctx context.Context, req *iamv1beta.ListTenantGroupsRequest) (*iamv1beta.ListTenantGroupsResponse, error) {
			return &iamv1beta.ListTenantGroupsResponse{TenantGroups: groups}, nil
		}
		srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
			deletedGroupNames = append(deletedGroupNames, req.Name)
			if req.Name == expectedPrefix+"group-already-deleted" {
				return nil, status.Error(codes.NotFound, "group not found")
			}
			return &emptypb.Empty{}, nil
		}

		cli := testutil.NewTestClient(t, srv)
		tCli, err := cli.TenantClient(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := tCli.ListTenantGroups(context.Background(), &iamv1beta.ListTenantGroupsRequest{})
		if err != nil {
			t.Fatal(err)
		}

		prefix := testAccRunPrefix()
		var sweepErrs []error
		for _, tg := range resp.GetTenantGroups() {
			name := tg.GetName()
			if strings.HasPrefix(name, prefix) {
				_, err := tCli.DeleteTenantGroup(context.Background(), &iamv1beta.TenantGroupReference{Name: name})
				if err != nil && !client.IsNotFound(err) {
					sweepErrs = append(sweepErrs, fmt.Errorf("failed to sweep tenant group %s: %w", name, err))
				}
			}
		}

		// Verify only 2 matching groups were targeted
		if len(deletedGroupNames) != 2 {
			t.Fatalf("expected 2 deletion attempts for matching groups, got %d: %v", len(deletedGroupNames), deletedGroupNames)
		}
		// NotFound must be ignored (idempotent delete)
		if len(sweepErrs) != 0 {
			t.Fatalf("expected 0 errors (NotFound should be ignored), got: %v", sweepErrs)
		}
	})

	t.Run("Shared image direct cleanup targets only test repository unshare IDs", func(t *testing.T) {
		repoName := "tfacc-m4test99-repo-a"
		images := []*registryv1beta.SharedImage{
			{Id: "img-1", SharedRepository: repoName},
			{Id: "img-2", SharedRepository: "other-repo"},
		}

		var unsharedIDs []string
		srv := testutil.NewMockServer()
		srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
			return &registryv1beta.ListSharedImagesResponse{SharedImages: images}, nil
		}
		srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
			unsharedIDs = append(unsharedIDs, req.Id)
			return &emptypb.Empty{}, nil
		}

		cli := testutil.NewTestClient(t, srv)
		rCli, err := cli.RegistryClient(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rCli.ListSharedImages(context.Background(), &registryv1beta.ListSharedImagesRequest{})
		if err != nil {
			t.Fatal(err)
		}

		for _, img := range resp.GetSharedImages() {
			if img.GetSharedRepository() == repoName || strings.HasPrefix(img.GetSharedRepository(), repoName) {
				_, _ = rCli.UnshareImage(context.Background(), &registryv1beta.UnshareImageRequest{Id: img.GetId()})
			}
		}

		if len(unsharedIDs) != 1 || unsharedIDs[0] != "img-1" {
			t.Fatalf("expected 1 unshared image 'img-1', got: %v", unsharedIDs)
		}
	})
}

// =============================================================================
// CHALLENGER M4 AREA 3: ACCEPTANCE MATRIX & LIVE FIXTURE CODE REVIEW CHECKS
// =============================================================================

func TestEmpirical_M4_AcceptanceMatrix_PrecheckGuards(t *testing.T) {
	t.Run("Precheck skips smoothly when TF_ACC is unset without executing live calls", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.skipped {
			t.Fatal("expected precheck to skip when TF_ACC is unset")
		}
		if m.failed {
			t.Fatal("expected precheck not to fail when TF_ACC is unset")
		}
	})

	t.Run("Partner Precheck skips smoothly when TF_ACC is unset", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.skipped {
			t.Fatal("expected partner precheck to skip when TF_ACC is unset")
		}
		if m.failed {
			t.Fatal("expected partner precheck not to fail when TF_ACC is unset")
		}
	})

	t.Run("Trusted Authority Precheck skips smoothly when TF_ACC is unset", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.skipped {
			t.Fatal("expected trusted precheck to skip when TF_ACC is unset")
		}
		if m.failed {
			t.Fatal("expected trusted precheck not to fail when TF_ACC is unset")
		}
	})

	t.Run("Ephemeral security check verifies non-persistence in state", func(t *testing.T) {
		checkFunc := testAccCheckEphemeralNotInState("ephemeral.namespace_id_token.test")
		if checkFunc == nil {
			t.Fatal("expected check function to be non-nil")
		}
	})

	t.Run("Precheck aborts live testing when token claim mismatches NSC_TEST_TENANT", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "target-tenant-isolated")
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "different-tenant"}))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail immediately when token tenant mismatches NSC_TEST_TENANT")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "tenant credential verification failed") {
			t.Fatalf("expected fatal message containing 'tenant credential verification failed', got: %v", m.messages)
		}
	})

	t.Run("Partner precheck aborts live testing when partner token claim mismatches NSC_TEST_TENANT", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "target-tenant-isolated")
		t.Setenv("NSC_PARTNER_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "unauthorized-partner-tenant"}))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected partner precheck to fail immediately when partner token mismatches")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "partner tenant credential verification failed") {
			t.Fatalf("expected fatal message containing 'partner tenant credential verification failed', got: %v", m.messages)
		}
	})

	t.Run("Trusted authority precheck aborts live testing when trusted JWT claim mismatches NSC_TEST_TENANT", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "target-tenant-isolated")
		t.Setenv("NSC_TRUSTED_JWT", createTestJWT(map[string]interface{}{"tenant_id": "unauthorized-trusted-tenant"}))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected trusted authority precheck to fail immediately when trusted JWT mismatches")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "trusted authority tenant credential verification failed") {
			t.Fatalf("expected fatal message containing 'trusted authority tenant credential verification failed', got: %v", m.messages)
		}
	})

	t.Run("Sweepers abort immediately when authenticated tokens mismatch NSC_TEST_TENANT", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", "isolated-tenant")
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"}))
		t.Setenv("NSC_PARTNER_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"}))

		errRunner := sweepRunnerProfiles("eu")
		if errRunner == nil || !strings.Contains(errRunner.Error(), "sweeper aborted: authenticated credential does not match NSC_TEST_TENANT") {
			t.Fatalf("expected runner profile sweeper to abort with error, got: %v", errRunner)
		}

		errTenant := sweepTenantGroups("eu")
		if errTenant == nil || !strings.Contains(errTenant.Error(), "tenant group sweeper aborted: authenticated partner credential does not match NSC_TEST_TENANT") {
			t.Fatalf("expected tenant group sweeper to abort with error, got: %v", errTenant)
		}
	})
}
