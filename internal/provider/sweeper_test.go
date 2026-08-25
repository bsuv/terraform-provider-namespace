package provider_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

func TestMain(m *testing.M) {
	resource.TestMain(m)
}

var (
	sweepRunnerProfiles = func(region string) (resultErr error) {
		testTenant := strings.TrimSpace(os.Getenv("NSC_TEST_TENANT"))
		if testTenant == "" {
			// Guard: Only execute sweeper when dedicated test tenant is explicitly configured
			return nil
		}

		tok := strings.TrimSpace(os.Getenv("NSC_TOKEN"))
		tokFile := strings.TrimSpace(os.Getenv("NSC_TOKEN_FILE"))
		if tok == "" && tokFile == "" {
			return fmt.Errorf("runner profile sweeper aborted: no tenant-matched credential (NSC_TOKEN / NSC_TOKEN_FILE) matching NSC_TEST_TENANT %q", testTenant)
		}
		activeTok := tok
		if activeTok == "" {
			activeTok = tokFile
		}
		if err := resolveAndVerifyTenant(activeTok, testTenant); err != nil {
			return fmt.Errorf("sweeper aborted: authenticated credential does not match NSC_TEST_TENANT %q: %w", testTenant, err)
		}

		ctx := context.Background()
		var clientCfg client.Config
		if tok != "" {
			clientCfg.Token = tok
		} else {
			clientCfg.TokenFile = tokFile
		}
		cli, err := client.New(ctx, clientCfg)
		if err != nil {
			return fmt.Errorf("failed to create client for sweeper: %w", err)
		}
		defer func() {
			resultErr = errors.Join(resultErr, cli.Close())
		}()
		pCli, err := cli.ProfileClient(ctx)
		if err != nil {
			return err
		}
		resp, err := pCli.ListProfiles(ctx, &emptypb.Empty{})
		if err != nil {
			return fmt.Errorf("failed to list profiles during sweep: %w", err)
		}
		prefix := testAccRunPrefix()
		var sweepErrs []error
		for _, p := range resp.GetProfiles() {
			if spec := p.GetSpec(); spec != nil {
				tag := spec.GetTag()
				if strings.HasPrefix(tag, prefix) {
					_, err := pCli.DeleteProfile(ctx, &githubv1beta.DeleteProfileRequest{ProfileId: p.GetProfileId()})
					if err != nil && !client.IsNotFound(err) {
						sweepErrs = append(sweepErrs, fmt.Errorf("failed to sweep runner profile %s (%s): %w", p.GetProfileId(), tag, err))
					}
				}
			}
		}
		if len(sweepErrs) > 0 {
			return errors.Join(sweepErrs...)
		}
		return nil
	}

	sweepTenantGroups = func(region string) (resultErr error) {
		testTenant := strings.TrimSpace(os.Getenv("NSC_TEST_TENANT"))
		if testTenant == "" {
			return nil
		}
		partnerTok := strings.TrimSpace(os.Getenv("NSC_PARTNER_TOKEN"))
		if partnerTok == "" {
			return fmt.Errorf("tenant group sweeper aborted: no verified partner credential (NSC_PARTNER_TOKEN) matching NSC_TEST_TENANT %q", testTenant)
		}
		if err := resolveAndVerifyTenant(partnerTok, testTenant); err != nil {
			return fmt.Errorf("tenant group sweeper aborted: authenticated partner credential does not match NSC_TEST_TENANT %q: %w", testTenant, err)
		}

		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{Token: partnerTok})
		if err != nil {
			return fmt.Errorf("failed to create client for tenant group sweeper: %w", err)
		}
		defer func() {
			resultErr = errors.Join(resultErr, cli.Close())
		}()
		tCli, err := cli.TenantClient(ctx)
		if err != nil {
			return err
		}
		resp, err := tCli.ListTenantGroups(ctx, &iamv1beta.ListTenantGroupsRequest{})
		if err != nil {
			return fmt.Errorf("failed to list tenant groups during sweep: %w", err)
		}
		prefix := testAccRunPrefix()
		var sweepErrs []error
		for _, tg := range resp.GetTenantGroups() {
			name := tg.GetName()
			if strings.HasPrefix(name, prefix) {
				_, err := tCli.DeleteTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: name})
				if err != nil && !client.IsNotFound(err) {
					sweepErrs = append(sweepErrs, fmt.Errorf("failed to sweep tenant group %s: %w", name, err))
				}
			}
		}
		if len(sweepErrs) > 0 {
			return errors.Join(sweepErrs...)
		}
		return nil
	}
)

func init() {
	resource.AddTestSweepers("namespace_github_runner_profile", &resource.Sweeper{
		Name: "namespace_github_runner_profile",
		F:    sweepRunnerProfiles,
	})

	resource.AddTestSweepers("namespace_tenant_group", &resource.Sweeper{
		Name: "namespace_tenant_group",
		F:    sweepTenantGroups,
	})
}

func TestAccSweeper(t *testing.T) {
	t.Run("Sweeper prefix is scoped strictly to run-id", func(t *testing.T) {
		t.Setenv("NSC_RUN_ID", "custom123")
		prefix := testAccRunPrefix()
		expected := "tfacc-custom123-"
		if prefix != expected {
			t.Fatalf("expected run prefix %q, got %q", expected, prefix)
		}

		// Must match resources from current run
		matchingTag := prefix + "runner-xyz"
		if !strings.HasPrefix(matchingTag, prefix) {
			t.Fatalf("expected %q to match prefix %q", matchingTag, prefix)
		}

		// Must NOT match resources from other runs or broad prefixes
		otherRunTag := "tfacc-other456-runner-xyz"
		if strings.HasPrefix(otherRunTag, prefix) {
			t.Fatalf("expected other run %q NOT to match prefix %q", otherRunTag, prefix)
		}

		legacyTag := "tf-acc-runner-xyz"
		if strings.HasPrefix(legacyTag, prefix) {
			t.Fatalf("expected legacy prefix %q NOT to match prefix %q", legacyTag, prefix)
		}
	})

	t.Run("Sweeper skips gracefully when NSC_TEST_TENANT is unset", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", "")
		if err := sweepRunnerProfiles("us-west-2"); err != nil {
			t.Fatalf("expected runner profile sweeper to return nil when NSC_TEST_TENANT is unset, got: %v", err)
		}
		if err := sweepTenantGroups("us-west-2"); err != nil {
			t.Fatalf("expected tenant group sweeper to return nil when NSC_TEST_TENANT is unset, got: %v", err)
		}
	})

	t.Run("Sweeper fails when credentials are missing and NSC_TEST_TENANT is set", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", "safe-test-tenant")
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")
		t.Setenv("NSC_PARTNER_TOKEN", "")

		errRunner := sweepRunnerProfiles("us-west-2")
		if errRunner == nil || !strings.Contains(errRunner.Error(), "no tenant-matched credential") {
			t.Fatalf("expected runner sweeper to fail when credentials missing, got: %v", errRunner)
		}

		errPartner := sweepTenantGroups("us-west-2")
		if errPartner == nil || !strings.Contains(errPartner.Error(), "no verified partner credential") {
			t.Fatalf("expected tenant group sweeper to fail when partner credentials missing, got: %v", errPartner)
		}
	})

	t.Run("Sweeper fails immediately when JWT tenant mismatches NSC_TEST_TENANT", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", "safe-test-tenant")
		t.Setenv("NSC_TOKEN", makeTestJWT("unauthorized-prod-tenant"))
		t.Setenv("NSC_PARTNER_TOKEN", makeTestJWT("unauthorized-prod-tenant"))

		errRunner := sweepRunnerProfiles("us-west-2")
		if errRunner == nil || !strings.Contains(errRunner.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected runner sweeper to fail with tenant mismatch error, got: %v", errRunner)
		}

		errPartner := sweepTenantGroups("us-west-2")
		if errPartner == nil || !strings.Contains(errPartner.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected tenant group sweeper to fail with tenant mismatch error, got: %v", errPartner)
		}
	})

	t.Run("Sweeper fails immediately on opaque or invalid credentials", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", "safe-test-tenant")
		t.Setenv("NSC_TOKEN", "opaque-invalid-token")
		t.Setenv("NSC_PARTNER_TOKEN", "opaque-partner-token")

		errRunner := sweepRunnerProfiles("us-west-2")
		if errRunner == nil || !strings.Contains(errRunner.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected runner sweeper to fail on opaque token, got: %v", errRunner)
		}

		errPartner := sweepTenantGroups("us-west-2")
		if errPartner == nil || !strings.Contains(errPartner.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected tenant group sweeper to fail on opaque partner token, got: %v", errPartner)
		}
	})

	t.Run("Sweeper fails immediately when nsct_ token tenant mismatches NSC_TEST_TENANT", func(t *testing.T) {
		t.Setenv("NSC_TEST_TENANT", "safe-test-tenant")
		t.Setenv("NSC_TOKEN", "nsct_unauthorized-prod-tenant_123456")
		t.Setenv("NSC_PARTNER_TOKEN", "nsct_unauthorized-prod-tenant_123456")

		errRunner := sweepRunnerProfiles("us-west-2")
		if errRunner == nil || !strings.Contains(errRunner.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected runner sweeper to fail with tenant mismatch error for nsct_ token, got: %v", errRunner)
		}

		errPartner := sweepTenantGroups("us-west-2")
		if errPartner == nil || !strings.Contains(errPartner.Error(), "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected tenant group sweeper to fail with tenant mismatch error for nsct_ token, got: %v", errPartner)
		}
	})
}
