package provider_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	gradlev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/gradle/v1beta"
	httpcachev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/httpcache/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	imagesv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/images"
	"github.com/hashicorp/go-plugin"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/provider"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// Secret tokens and certificates used across blackbox tests to verify non-persistence and non-leakage.
var (
	blackboxSensitiveProviderToken = "nsct_" + "live_blackbox_master_token_secret_998877"
	blackboxSensitiveIDToken       = "ey" + "JhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "ey" + "JzdWIiOiJ0ZXN0LXdvcmtsb2FkLWlkZW50aXR5IiwiaXNzIjoiaHR0cHM6Ly9hdXRoLm5hbWVzcGFjZWxhYnMuY2xvdWQifQ" + "." + "secret_id_signature_payload"
	blackboxSensitiveTenantToken   = "nsct_" + "live_tenant_token_secret_xyz123456789"
	blackboxSensitiveCertPEM       = testutil.SynthesizePEMBlock("CERTIFICATE", "test-blackbox-cert-body")
	blackboxSensitivePrivateKeyPEM = testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-blackbox-privkey-body")
	blackboxSensitiveBazelKeyPEM   = testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-blackbox-bazelkey-body")
	blackboxSensitiveGradlePass    = "nscg_" + "live_gradle_secret_password_ghijkl123456"
	blackboxSensitiveHTTPPass      = "nsch_" + "live_http_secret_password_mnopqr123456"
)

type rawTerraformState struct {
	Version          int            `json:"version"`
	TerraformVersion string         `json:"terraform_version"`
	Serial           int            `json:"serial"`
	Lineage          string         `json:"lineage"`
	Outputs          map[string]any `json:"outputs"`
	Resources        []struct {
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Instances []any  `json:"instances"`
	} `json:"resources"`
}

type blackboxHarness struct {
	workDir     string
	reattachEnv string
	mockServer  *testutil.MockServer
}

func setupBlackBoxHarness(t *testing.T) *blackboxHarness {
	t.Helper()

	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform binary not found in PATH; skipping black-box Terraform CLI tests")
	}

	client.RequireTransportSecurity = false

	// 1. Initialize MockServer with all services
	mockSrv := testutil.NewMockServer()

	// Configure mock hooks
	mockSrv.Token.IssueIdTokenFunc = func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
		return &iamv1beta.IssueIdTokenResponse{
			IdToken: blackboxSensitiveIDToken,
		}, nil
	}

	mockSrv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: blackboxSensitiveTenantToken,
		}, nil
	}

	mockSrv.Tenant.IssueTenantClientCertificateFunc = func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
		return &iamv1beta.IssueTenantClientCertificateResponse{
			ClientCertificatePem: blackboxSensitiveCertPEM,
			PrivateKeyPem:        blackboxSensitivePrivateKeyPEM,
		}, nil
	}

	mockSrv.BazelCache.EnsureBazelCacheFunc = func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
		return &bazelv1beta.EnsureBazelCacheResponse{
			CacheEndpoint:          "grpcs://bazel-cache.namespaceapis.com:443",
			HttpsCacheEndpoint:     "https://bazel-cache.namespaceapis.com:443",
			HttpsMtlsCacheEndpoint: "https://mtls.bazel-cache.namespaceapis.com:443",
			ServerCaPem:            blackboxSensitiveCertPEM,
			ClientCertPem:          blackboxSensitiveCertPEM,
			ClientKeyPem:           blackboxSensitiveBazelKeyPEM,
			BuildEventEndpoint:     "grpcs://bes.namespaceapis.com:443",
		}, nil
	}

	mockSrv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
		return &gradlev1beta.EnsureGradleCacheResponse{
			CacheEndpointUrl: "https://gradle-cache.namespaceapis.com:443",
			Username:         "gradle-user",
			Password:         blackboxSensitiveGradlePass,
		}, nil
	}

	mockSrv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
		return &httpcachev1beta.EnsureHttpCacheResponse{
			CacheEndpointUrl: "https://http-cache.namespaceapis.com:443",
			Username:         "http-user",
			Password:         blackboxSensitiveHTTPPass,
		}, nil
	}

	mockSrv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "profile-blackbox-12345",
				Spec:      req.GetSpec(),
				Version:   1,
			},
		}, nil
	}

	mockSrv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: "profile-blackbox-12345",
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag: "mixed-runner-tag",
						Os:  "ubuntu-24.04",
					},
					Version: 1,
				},
			},
		}, nil
	}

	mockSrv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: req.GetProfileId(),
				Spec:      req.GetSpec(),
				Version:   2,
			},
		}, nil
	}

	// Mock registry policy hooks
	mockSrv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	mockSrv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              1,
			Expiration:            durationpb.New(720 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	// 2. Initialize in-process test client
	cl := testutil.NewTestClient(t, mockSrv)

	// 3. Start provider server for Terraform reattach
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reattachCh := make(chan *plugin.ReattachConfig, 1)
	closeCh := make(chan struct{})

	go func() {
		_ = tf6server.Serve(
			"provider",
			providerserver.NewProtocol6(provider.NewWithClient("test-blackbox", cl)()),
			tf6server.WithDebug(ctx, reattachCh, closeCh),
		)
	}()

	var reattachConfig *plugin.ReattachConfig
	select {
	case reattachConfig = <-reattachCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for provider tf6server reattach config")
	}

	reattachMap := map[string]any{
		"registry.terraform.io/bsuv/namespace": map[string]any{
			"Protocol":        "grpc",
			"ProtocolVersion": 6,
			"Pid":             reattachConfig.Pid,
			"Test":            true,
			"Addr": map[string]string{
				"Network": reattachConfig.Addr.Network(),
				"String":  reattachConfig.Addr.String(),
			},
		},
	}
	reattachBytes, err := json.Marshal(reattachMap)
	if err != nil {
		t.Fatalf("failed to marshal reattach config: %v", err)
	}

	workDir := t.TempDir()

	return &blackboxHarness{
		workDir:     workDir,
		reattachEnv: string(reattachBytes),
		mockServer:  mockSrv,
	}
}

func (h *blackboxHarness) runTerraform(t *testing.T, extraEnv []string, args ...string) (string, string, error) {
	t.Helper()

	cmd := exec.Command("terraform", args...)
	cmd.Dir = h.workDir

	baseEnv := os.Environ()
	customEnv := append(baseEnv,
		"TF_REATTACH_PROVIDERS="+h.reattachEnv,
		"TF_IN_AUTOMATION=1",
	)
	customEnv = append(customEnv, extraEnv...)
	cmd.Env = customEnv

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

func (h *blackboxHarness) readState(t *testing.T) (*rawTerraformState, []byte) {
	t.Helper()
	statePath := filepath.Join(h.workDir, "terraform.tfstate")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read terraform.tfstate file at %s: %v", statePath, err)
	}

	var state rawTerraformState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("failed to parse terraform.tfstate JSON: %v", err)
	}
	return &state, data
}

func (h *blackboxHarness) showPlanJSON(t *testing.T, planPath string, extraEnv []string) *tfjson.Plan {
	t.Helper()
	stdout, stderr, err := h.runTerraform(t, extraEnv, "show", "-json", planPath)
	if err != nil {
		t.Fatalf("terraform show -json failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	var plan tfjson.Plan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("failed to parse terraform show -json output: %v\nRaw output: %s", err, stdout)
	}
	return &plan
}

// allBlackboxSecrets returns all known sensitive token and certificate strings.
func allBlackboxSecrets() []string {
	return []string{
		blackboxSensitiveProviderToken,
		blackboxSensitiveIDToken,
		blackboxSensitiveTenantToken,
		blackboxSensitiveCertPEM,
		blackboxSensitivePrivateKeyPEM,
		blackboxSensitiveBazelKeyPEM,
		blackboxSensitiveGradlePass,
		blackboxSensitiveHTTPPass,
	}
}

func assertNoSecretsInRawBytes(t *testing.T, data []byte, secrets []string, contextName string) {
	t.Helper()
	str := string(data)
	for _, secret := range secrets {
		if strings.Contains(str, secret) {
			t.Errorf("CRITICAL SECURITY VIOLATION: Secret %q found in %s raw contents!", secret, contextName)
		}
	}
}

func assertNoSecretsInCLIOutput(t *testing.T, output string, secrets []string, commandName string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Errorf("CRITICAL SECURITY VIOLATION: Secret %q was leaked in plain text in %s CLI output!\nOutput snippet: %s", secret, commandName, output)
		}
	}
}

// TestBlackBox_PlanAndApply_EphemeralSecrets_NonPersistence executes a true black-box Terraform CLI
// cycle against the Namespace provider with ephemeral resources, verifying:
// 1. `terraform plan -out=tfplan` creates a binary plan without persisting ephemeral secrets.
// 2. `terraform show -json tfplan` validates sensitivity flags and confirms absence of ephemeral state.
// 3. `terraform apply` executes cleanly, and `terraform.tfstate` contains ZERO ephemeral resources.
// 4. Raw state file bytes and CLI human stdout/stderr strictly contain zero leaked secrets.
func TestBlackBox_PlanAndApply_EphemeralSecrets_NonPersistence(t *testing.T) {
	h := setupBlackBoxHarness(t)

	// Set up child module that consumes ephemeral outputs
	moduleDir := filepath.Join(h.workDir, "modules", "consumer")
	if err := os.MkdirAll(moduleDir, 0700); err != nil {
		t.Fatalf("failed to create module dir: %v", err)
	}

	moduleTF := `
variable "id_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "tenant_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "cert_pem" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "private_key" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "gradle_pass" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "http_pass" {
  type      = string
  sensitive = true
  ephemeral = true
}

output "consumed_id_token" {
  value     = var.id_token
  sensitive = true
  ephemeral = true
}

output "consumed_cert_pem" {
  value     = var.cert_pem
  sensitive = true
  ephemeral = true
}
`
	if err := os.WriteFile(filepath.Join(moduleDir, "main.tf"), []byte(moduleTF), 0600); err != nil {
		t.Fatalf("failed to write module main.tf: %v", err)
	}

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

provider "namespace" {
  token = var.nsc_token
}

ephemeral "namespace_id_token" "test" {
  audience = "https://iam.amazonaws.com/oidc"
}

ephemeral "namespace_tenant_token" "test" {
  tenant_id     = "tenant-blackbox-001"
  actor_id      = "actor-blackbox-001"
  duration_secs = 900
}

ephemeral "namespace_client_certificate" "test" {
  tenant_id     = "tenant-blackbox-001"
  actor_id      = "actor-blackbox-001"
  duration_secs = 1800
}

ephemeral "namespace_bazel_cache" "test" {
  key = "ci"
}

ephemeral "namespace_gradle_cache" "test" {
  name = "blackbox-gradle-cache"
}

ephemeral "namespace_http_cache" "test" {
  name = "blackbox-http-cache"
}

module "consumer" {
  source       = "./modules/consumer"
  id_token     = ephemeral.namespace_id_token.test.id_token
  tenant_token = ephemeral.namespace_tenant_token.test.bearer_token
  cert_pem     = ephemeral.namespace_client_certificate.test.client_certificate_pem
  private_key  = ephemeral.namespace_client_certificate.test.private_key_pem
  gradle_pass  = ephemeral.namespace_gradle_cache.test.password
  http_pass    = ephemeral.namespace_http_cache.test.password
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken,
	}

	// 0. Initialize modules via terraform init
	initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false")
	if err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	// 1. Run `terraform plan -out=tfplan`
	planFile := "tfplan"
	stdout, stderr, err := h.runTerraform(t, tfEnv, "plan", "-out="+planFile)
	if err != nil {
		t.Fatalf("terraform plan failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Verify plan stdout/stderr does not leak secrets
	combinedPlanOutput := stdout + "\n" + stderr
	assertNoSecretsInCLIOutput(t, combinedPlanOutput, allBlackboxSecrets(), "terraform plan")

	// 2. Inspect plan JSON via `terraform show -json tfplan`
	planJSON := h.showPlanJSON(t, planFile, tfEnv)

	// Check configuration variable sensitivity
	if planJSON.Config != nil && planJSON.Config.RootModule != nil {
		if varConfig, ok := planJSON.Config.RootModule.Variables["nsc_token"]; ok {
			if !varConfig.Sensitive {
				t.Errorf("expected variable nsc_token to be marked sensitive in plan JSON")
			}
		} else {
			t.Errorf("expected variable nsc_token in plan JSON configuration")
		}
	}

	// Check planned root module resources: ephemeral resources must NOT be listed as planned managed resources
	if planJSON.PlannedValues != nil && planJSON.PlannedValues.RootModule != nil {
		for _, res := range planJSON.PlannedValues.RootModule.Resources {
			for _, ephType := range []string{
				"namespace_id_token",
				"namespace_tenant_token",
				"namespace_client_certificate",
				"namespace_bazel_cache",
				"namespace_gradle_cache",
				"namespace_http_cache",
			} {
				if res.Type == ephType {
					t.Errorf("ephemeral resource type %q unexpectedly found in planned_values resources", ephType)
				}
			}
		}
	}

	// Read binary plan file and confirm raw secrets are not stored in plan
	planBytes, err := os.ReadFile(filepath.Join(h.workDir, planFile))
	if err != nil {
		t.Fatalf("failed to read binary plan file: %v", err)
	}
	assertNoSecretsInRawBytes(t, planBytes, []string{
		blackboxSensitiveProviderToken,
		blackboxSensitiveIDToken,
		blackboxSensitiveTenantToken,
		blackboxSensitiveCertPEM,
		blackboxSensitivePrivateKeyPEM,
		blackboxSensitiveBazelKeyPEM,
		blackboxSensitiveGradlePass,
		blackboxSensitiveHTTPPass,
	}, "binary tfplan")

	// 3. Run `terraform apply` using the plan
	applyStdout, applyStderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve", planFile)
	if err != nil {
		t.Fatalf("terraform apply failed: %v\nStderr: %s\nStdout: %s", err, applyStderr, applyStdout)
	}

	// Verify apply stdout/stderr does not leak secrets
	combinedApplyOutput := applyStdout + "\n" + applyStderr
	assertNoSecretsInCLIOutput(t, combinedApplyOutput, allBlackboxSecrets(), "terraform apply")

	// 4. Read and deeply inspect `terraform.tfstate`
	state, stateBytes := h.readState(t)

	// State must contain 0 ephemeral resources
	if len(state.Resources) != 0 {
		t.Errorf("expected 0 resources in state for ephemeral-only config, got %d", len(state.Resources))
	}

	// Raw state bytes must not contain any sensitive tokens, certificates, or private keys
	assertNoSecretsInRawBytes(t, stateBytes, allBlackboxSecrets(), "terraform.tfstate")
}

// TestBlackBox_Apply_MixedManagedAndEphemeral verifies that when managed resources and ephemeral
// resources coexist in the same configuration, managed resources are correctly stored in state
// while ephemeral resources and their secrets remain completely absent from state.
func TestBlackBox_Apply_MixedManagedAndEphemeral(t *testing.T) {
	h := setupBlackBoxHarness(t)

	// Downstream consumer module
	moduleDir := filepath.Join(h.workDir, "modules", "auth_consumer")
	if err := os.MkdirAll(moduleDir, 0700); err != nil {
		t.Fatalf("failed to create module dir: %v", err)
	}

	moduleTF := `
variable "auth_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

output "forwarded_token" {
  value     = var.auth_token
  sensitive = true
  ephemeral = true
}
`
	if err := os.WriteFile(filepath.Join(moduleDir, "main.tf"), []byte(moduleTF), 0600); err != nil {
		t.Fatalf("failed to write module main.tf: %v", err)
	}

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
}

provider "namespace" {
  token = var.nsc_token
}

ephemeral "namespace_id_token" "auth" {
  audience = "https://iam.amazonaws.com/oidc"
}

ephemeral "namespace_bazel_cache" "cache" {
  key = "ci"
}

resource "namespace_registry_repository_policy" "managed_policy" {
  repository             = "test-blackbox-repo"
  expiration             = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}

module "auth_sub" {
  source     = "./modules/auth_consumer"
  auth_token = ephemeral.namespace_id_token.auth.id_token
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken,
	}

	// Initialize local modules
	if initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Assert CLI outputs contain no leaked secrets
	assertNoSecretsInCLIOutput(t, stdout+"\n"+stderr, allBlackboxSecrets(), "terraform apply (mixed)")

	// Read state
	state, stateBytes := h.readState(t)

	// Managed resource MUST be present in state
	if len(state.Resources) == 0 {
		t.Fatalf("expected managed resource namespace_registry_repository_policy to be present in state")
	}

	var foundManaged bool
	for _, res := range state.Resources {
		if res.Type == "namespace_registry_repository_policy" {
			foundManaged = true
		}
		if res.Type == "namespace_id_token" || res.Type == "namespace_bazel_cache" {
			t.Errorf("CRITICAL: Ephemeral resource %q of type %q leaked into persistent state!", res.Name, res.Type)
		}
	}
	if !foundManaged {
		t.Errorf("managed resource namespace_registry_repository_policy not found in state resources")
	}

	// Assert raw state bytes contain NO ephemeral secrets or provider auth token
	assertNoSecretsInRawBytes(t, stateBytes, []string{
		blackboxSensitiveProviderToken,
		blackboxSensitiveIDToken,
		blackboxSensitiveTenantToken,
		blackboxSensitiveCertPEM,
		blackboxSensitivePrivateKeyPEM,
		blackboxSensitiveBazelKeyPEM,
	}, "terraform.tfstate (mixed config)")
}

// TestBlackBox_CLI_OutputRedaction_NoLeakageInStdoutStderr verifies that Terraform CLI outputs
// and diagnostic messages display `(sensitive value)` and never leak raw secrets when sensitive
// variables or ephemeral outputs are evaluated.
func TestBlackBox_CLI_OutputRedaction_NoLeakageInStdoutStderr(t *testing.T) {
	h := setupBlackBoxHarness(t)

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "secret_api_token" {
  type      = string
  sensitive = true
}

provider "namespace" {
  token = var.secret_api_token
}

ephemeral "namespace_id_token" "test" {
  audience = "https://iam.amazonaws.com/oidc"
}

resource "namespace_github_runner_profile" "runner" {
  tag         = "redaction-test-runner"
  os          = "ubuntu-24.04"
  description = "Runner for CLI redaction testing"
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_secret_api_token=" + blackboxSensitiveProviderToken,
	}

	stdout, stderr, err := h.runTerraform(t, tfEnv, "plan")
	if err != nil {
		t.Fatalf("terraform plan failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	combined := stdout + "\n" + stderr

	// 1. Must never leak raw master provider secret or ephemeral ID token
	assertNoSecretsInCLIOutput(t, combined, []string{
		blackboxSensitiveProviderToken,
		blackboxSensitiveIDToken,
	}, "terraform plan (redaction check)")

	// 2. Must display planned resource creation
	if !strings.Contains(combined, "namespace_github_runner_profile.runner") {
		t.Errorf("expected plan output to show namespace_github_runner_profile.runner creation")
	}
}

// TestBlackBox_Apply_RunnerProfile_MinimalSparseLifecycle tests executing terraform apply
// with minimal sparse configuration (omitted optional blocks) and verifies that plan unknowns
// are handled without Value Conversion Error.
func TestBlackBox_Apply_RunnerProfile_MinimalSparseLifecycle(t *testing.T) {
	h := setupBlackBoxHarness(t)

	var mu sync.Mutex
	profiles := make(map[string]*githubv1beta.RunnerProfileWithStatus)

	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		profID := "profile-bb-sparse-1"
		now := timestamppb.Now()
		p := &githubv1beta.RunnerProfileWithStatus{
			ProfileId: profID,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec:      proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec),
		}
		profiles[profID] = p
		return &githubv1beta.CreateProfileResponse{Profile: p}, nil
	}

	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var list []*githubv1beta.RunnerProfileWithStatus
		for _, p := range profiles {
			list = append(list, p)
		}
		return &githubv1beta.ListProfilesResponse{Profiles: list}, nil
	}

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
}

provider "namespace" {
  token = var.nsc_token
}

resource "namespace_github_runner_profile" "sparse" {
  tag = "bb-sparse-runner"
  os  = "ubuntu-24.04"
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken,
	}

	if initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on sparse runner profile (Value Conversion Error regression): %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	state, _ := h.readState(t)
	if len(state.Resources) != 1 {
		t.Fatalf("expected 1 resource in state, got %d", len(state.Resources))
	}
	res := state.Resources[0]
	if res.Type != "namespace_github_runner_profile" || res.Name != "sparse" {
		t.Errorf("expected resource namespace_github_runner_profile.sparse, got %s.%s", res.Type, res.Name)
	}

	// Verify plan reports 0 changes
	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after apply should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}
}

// TestBlackBox_Apply_RunnerProfile_ComplexNestedLifecycle tests executing terraform apply and update
// with complex nested configuration (instance_shape, cache_volume_settings, custom_runner_image_spec, network_policy).
func TestBlackBox_Apply_RunnerProfile_ComplexNestedLifecycle(t *testing.T) {
	h := setupBlackBoxHarness(t)

	var mu sync.Mutex
	profiles := make(map[string]*githubv1beta.RunnerProfileWithStatus)

	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		profID := "profile-bb-complex-1"
		now := timestamppb.Now()
		p := &githubv1beta.RunnerProfileWithStatus{
			ProfileId: profID,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec:      proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec),
		}
		profiles[profID] = p
		return &githubv1beta.CreateProfileResponse{Profile: p}, nil
	}

	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var list []*githubv1beta.RunnerProfileWithStatus
		for _, p := range profiles {
			list = append(list, p)
		}
		return &githubv1beta.ListProfilesResponse{Profiles: list}, nil
	}

	h.mockServer.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		p, ok := profiles[req.GetProfileId()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		p.Version++
		p.Spec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		p.UpdatedAt = timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{Profile: p}, nil
	}

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
}

provider "namespace" {
  token = var.nsc_token
}

resource "namespace_github_runner_profile" "complex" {
  tag                   = "bb-complex-runner"
  os                    = "ubuntu-24.04"
  description           = "Complex runner with all nested blocks"
  emoji                 = "🚀"
  swap_size_mb          = 2048
  builder_mode          = "USE_LOCAL_CACHE"
  access_level          = "ACCESS_LEVEL_PERMISSIVE"
  experimental_features = ["fast-storage", "nested-virt"]

  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 32768
    machine_arch     = "amd64"
    os               = "linux"
    selectors = [
      {
        name  = "role"
        value = "builder"
      }
    ]
  }

  cache_volume_settings = [
    {
      volume_size_gb               = 100
      volume_reserved_size_gb      = 10
      bundled_container_cache      = true
      bundled_buildkit_cache       = true
      bundled_git_mirror           = true
      bundled_runner_tools_cache   = true
      bundled_runner_actions_cache = true
      allow_commit_from_branch     = ["main", "release/*"]
    }
  ]

  custom_runner_image_spec = {
    ubuntu_spec = {
      apt_package = ["build-essential", "cmake"]
    }
    docker_spec = {
      dockerfile_content = "RUN echo ready"
    }
  }

  network_policy = {
    egress                   = "EGRESS_POLICY_DOMAIN_ALLOW_LIST"
    egress_domain_allow_list = ["github.com", "*.npmjs.org"]
    advisory                 = true
  }
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken,
	}

	if initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on complex runner profile: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	state, _ := h.readState(t)
	if len(state.Resources) != 1 {
		t.Fatalf("expected 1 resource in state, got %d", len(state.Resources))
	}

	// Update configuration: bump vcpu and memory
	updatedTF := strings.Replace(mainTF, "virtual_cpu      = 8", "virtual_cpu      = 16", 1)
	updatedTF = strings.Replace(updatedTF, "memory_megabytes = 32768", "memory_megabytes = 65536", 1)
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(updatedTF), 0600); err != nil {
		t.Fatalf("failed to write updated main.tf: %v", err)
	}

	upStdout, upStderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply update failed: %v\nStderr: %s\nStdout: %s", err, upStderr, upStdout)
	}

	// Verify plan has no diff
	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after update should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}
}

// TestBlackBox_Apply_TenantGroup_OmittedPolicies tests executing terraform apply on a tenant group
// with omitted policies and verifies that plan unknowns are handled cleanly without error.
func TestBlackBox_Apply_TenantGroup_OmittedPolicies(t *testing.T) {
	h := setupBlackBoxHarness(t)

	var mu sync.Mutex
	groups := make(map[string]*iamv1beta.DescribeTenantGroupResponse)

	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		g, ok := groups[req.Name]
		if !ok {
			return nil, status.Error(codes.NotFound, "tenant group not found")
		}
		return g, nil
	}

	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		groups[req.Name] = &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          req.Policies,
			EnrolledTenantIds: []string{},
		}
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(groups, req.Name)
		return &emptypb.Empty{}, nil
	}

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
}

provider "namespace" {
  token = var.nsc_token
}

resource "namespace_tenant_group" "no_policies" {
  name = "blackbox-ops-team"
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken,
	}

	if initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on tenant group with omitted policies (Value Conversion Error regression): %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	state, _ := h.readState(t)
	if len(state.Resources) != 1 {
		t.Fatalf("expected 1 resource in state, got %d", len(state.Resources))
	}
	res := state.Resources[0]
	if res.Type != "namespace_tenant_group" || res.Name != "no_policies" {
		t.Errorf("expected resource namespace_tenant_group.no_policies, got %s.%s", res.Type, res.Name)
	}

	// Verify plan reports 0 changes
	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after apply should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}
}

func TestBlackBox_UpgradeState_RunnerProfile_V0ToV1(t *testing.T) {
	h := setupBlackBoxHarness(t)
	h.mockServer.Profile.EnableStatefulProfiles()

	createdResp, err := h.mockServer.Profile.CreateProfile(context.Background(), &githubv1beta.CreateProfileRequest{
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag:         "legacy-v0-runner",
			Os:          "ubuntu-24.04",
			Description: "Migrated profile",
			AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_PERMISSIVE,
			CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{
				{
					VolumeSizeGb:          50,
					VolumeReservedSizeGb:  5,
					BundledContainerCache: true,
					AllowCommitFromBranch: []string{"main"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed CreateProfile failed: %v", err)
	}
	prof := createdResp.GetProfile()

	v0State := map[string]any{
		"version":           4,
		"terraform_version": "1.9.0",
		"serial":            1,
		"lineage":           "11111111-1111-1111-1111-111111111111",
		"outputs":           map[string]any{},
		"resources": []any{
			map[string]any{
				"mode":     "managed",
				"type":     "namespace_github_runner_profile",
				"name":     "legacy",
				"provider": `provider["registry.terraform.io/bsuv/namespace"]`,
				"instances": []any{
					map[string]any{
						"schema_version": 0,
						"attributes": map[string]any{
							"id":                       prof.GetProfileId(),
							"profile_id":               prof.GetProfileId(),
							"version":                  prof.GetVersion(),
							"created_at":               prof.GetCreatedAt().AsTime().UTC().Format(time.RFC3339Nano),
							"updated_at":               prof.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339Nano),
							"tag":                      "legacy-v0-runner",
							"os":                       "ubuntu-24.04",
							"description":              "Migrated profile",
							"emoji":                    "",
							"swap_size_mb":             0,
							"builder_mode":             "BUILDER_MODE_UNSPECIFIED",
							"access_level":             "ACCESS_LEVEL_PERMISSIVE",
							"experimental_features":    nil,
							"instance_shape":           nil,
							"custom_runner_image_spec": nil,
							"network_policy":           nil,
							"custom_runner_images":     nil,
							"cache_volume_settings": []any{
								map[string]any{
									"volume_tag":                   "legacy-vol-0",
									"volume_size_gb":               50,
									"volume_reserved_size_gb":      5,
									"bundled_container_cache":      true,
									"bundled_buildkit_cache":       false,
									"bundled_git_mirror":           false,
									"bundled_runner_tools_cache":   false,
									"bundled_runner_actions_cache": false,
									"allow_commit_from_branch":     []any{"main"},
								},
							},
						},
					},
				},
			},
		},
	}
	v0Bytes, err := json.MarshalIndent(v0State, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal v0 state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.workDir, "terraform.tfstate"), v0Bytes, 0600); err != nil {
		t.Fatalf("failed to write terraform.tfstate: %v", err)
	}

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
}

provider "namespace" {
  token = var.nsc_token
}

resource "namespace_github_runner_profile" "legacy" {
  tag          = "legacy-v0-runner"
  os           = "ubuntu-24.04"
  description  = "Migrated profile"
  access_level = "ACCESS_LEVEL_PERMISSIVE"

  cache_volume_settings = [
    {
      volume_size_gb           = 50
      volume_reserved_size_gb  = 5
      bundled_container_cache  = true
      allow_commit_from_branch = ["main"]
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken}
	if initOut, initErr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initErr, initOut)
	}

	if planOut, planErr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
		t.Fatalf("expected 0 diff after v0->v1 state upgrade: %v\nStderr: %s\nStdout: %s", err, planErr, planOut)
	}

	if appOut, appErr, err := h.runTerraform(t, tfEnv, "apply", "-refresh-only", "-auto-approve"); err != nil {
		t.Fatalf("terraform apply -refresh-only failed: %v\nStderr: %s\nStdout: %s", err, appErr, appOut)
	}

	state, rawBytes := h.readState(t)
	if strings.Contains(string(rawBytes), "volume_tag") {
		t.Fatalf("upgraded terraform.tfstate still contains volume_tag:\n%s", string(rawBytes))
	}
	if len(state.Resources) != 1 || len(state.Resources[0].Instances) != 1 {
		t.Fatalf("unexpected resources in upgraded state: %+v", state.Resources)
	}
	inst, ok := state.Resources[0].Instances[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected instance type: %T", state.Resources[0].Instances[0])
	}
	if sv, _ := inst["schema_version"].(float64); sv != 1 {
		t.Errorf("expected schema_version 1 in upgraded state, got %v", inst["schema_version"])
	}
}

func TestBlackBox_RunnerProfile_PositionalCacheVolumeTransitions(t *testing.T) {
	h := setupBlackBoxHarness(t)
	h.mockServer.Profile.EnableStatefulProfiles()

	step1 := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}
variable "nsc_token" {
  type      = string
  sensitive = true
}
provider "namespace" {
  token = var.nsc_token
}
resource "namespace_github_runner_profile" "pos" {
  tag = "bb-pos-cv"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb           = 30
      allow_commit_from_branch = []
    },
    {
      volume_size_gb           = 60
      allow_commit_from_branch = ["main", "release/*"]
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step1), 0600); err != nil {
		t.Fatalf("failed to write step1 main.tf: %v", err)
	}
	tfEnv := []string{"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken}
	if initOut, initErr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initErr, initOut)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
		t.Fatalf("step1 apply failed: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
		t.Fatalf("step1 plan should have zero diff: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}

	step2 := strings.Replace(step1, "volume_size_gb           = 30\n      allow_commit_from_branch = []", "volume_size_gb           = 45\n      allow_commit_from_branch = [\"develop\"]", 1)
	step2 = strings.Replace(step2, "volume_size_gb           = 60\n      allow_commit_from_branch = [\"main\", \"release/*\"]", "volume_size_gb           = 80\n      allow_commit_from_branch = []", 1)
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step2), 0600); err != nil {
		t.Fatalf("failed to write step2 main.tf: %v", err)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
		t.Fatalf("step2 apply failed: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
		t.Fatalf("step2 plan should have zero diff: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}
}

func TestBlackBox_RunnerProfile_CustomAccessAndPermissions(t *testing.T) {
	h := setupBlackBoxHarness(t)
	h.mockServer.Profile.EnableStatefulProfiles()

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}
variable "nsc_token" {
  type      = string
  sensitive = true
}
provider "namespace" {
  token = var.nsc_token
}
resource "namespace_github_runner_profile" "custom" {
  tag          = "bb-custom-perm"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = "sec_123"
      actions       = ["read"]
    },
    {
      resource_type = "cache/bazel"
      resource_id   = ""
      actions       = []
    }
  ]
}

data "namespace_github_runner_profile" "by_id" {
  profile_id = namespace_github_runner_profile.custom.profile_id
}

data "namespace_github_runner_profiles" "all" {
  depends_on = [namespace_github_runner_profile.custom]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}
	tfEnv := []string{"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken}
	if initOut, initErr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initErr, initOut)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
		t.Fatalf("apply failed: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
		t.Fatalf("plan should have zero diff: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}

	// Update to ACCESS_LEVEL_LIMITED without custom_permissions
	limitedTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}
variable "nsc_token" {
  type      = string
  sensitive = true
}
provider "namespace" {
  token = var.nsc_token
}
resource "namespace_github_runner_profile" "custom" {
  tag          = "bb-custom-perm"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_LIMITED"
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(limitedTF), 0600); err != nil {
		t.Fatalf("failed to write limitedTF: %v", err)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
		t.Fatalf("apply limitedTF failed: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
		t.Fatalf("plan after update to ACCESS_LEVEL_LIMITED should have zero diff: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}

	// Invalid empty resource_type fails terraform validate
	badTypeTF := strings.Replace(mainTF, `resource_type = "vault/object"`, `resource_type = ""`, 1)
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(badTypeTF), 0600); err != nil {
		t.Fatalf("failed to write badTypeTF: %v", err)
	}
	if _, _, err := h.runTerraform(t, tfEnv, "validate"); err == nil {
		t.Fatal("expected terraform validate to fail on empty resource_type")
	}

	// Missing actions fails terraform validate
	badActionsTF := strings.Replace(mainTF, `actions       = ["read"]`, "", 1)
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(badActionsTF), 0600); err != nil {
		t.Fatalf("failed to write badActionsTF: %v", err)
	}
	if _, _, err := h.runTerraform(t, tfEnv, "validate"); err == nil {
		t.Fatal("expected terraform validate to fail when actions is omitted")
	}
}

func TestBlackBox_WindowsOS_RunnerProfileAndDataSources(t *testing.T) {
	h := setupBlackBoxHarness(t)
	h.mockServer.Profile.EnableStatefulProfiles()

	h.mockServer.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{
					Label:    "windows-2022",
					ImageRef: "nscr.io/official/windows-2022:latest",
					Title:    "Windows Server 2022",
					Spec: &imagesv1beta.BaseImage{
						Id:           "img_win_2022",
						Os:           "windows",
						ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION,
					},
				},
			},
		}, nil
	}
	h.mockServer.Builder.GetBuilderConfigurationFunc = func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		return &builderv1beta.GetBuilderConfigurationResponse{
			BuildkitEndpoint:     "builder.namespace.so:443",
			FullBuildkitEndpoint: "tcp://builder.namespace.so:443",
			CacheSizeMb:          20480,
			Shape: &computev1beta.InstanceShape{
				VirtualCpu:      8,
				MemoryMegabytes: 16384,
				MachineArch:     "amd64",
				Os:              "windows",
			},
		}, nil
	}

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}
variable "nsc_token" {
  type      = string
  sensitive = true
}
provider "namespace" {
  token = var.nsc_token
}
resource "namespace_github_runner_profile" "win" {
  tag = "bb-win-runner"
  os  = "windows-2022"
  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = "windows"
  }
}
data "namespace_github_base_images" "win" {}
data "namespace_builder_configuration" "win" {
  platform = "windows/amd64"
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}
	tfEnv := []string{"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken}
	if initOut, initErr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initErr, initOut)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
		t.Fatalf("apply failed: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}
	if out, errOut, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
		t.Fatalf("plan should have zero diff: %v\nStderr: %s\nStdout: %s", err, errOut, out)
	}

	_, rawBytes := h.readState(t)
	if !strings.Contains(string(rawBytes), `"windows"`) {
		t.Fatalf("expected 'windows' in terraform.tfstate:\n%s", string(rawBytes))
	}

	badOS := strings.Replace(mainTF, `os               = "windows"`, `os               = "freebsd"`, 1)
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(badOS), 0600); err != nil {
		t.Fatalf("failed to write badOS main.tf: %v", err)
	}
	if _, _, err := h.runTerraform(t, tfEnv, "validate"); err == nil {
		t.Fatal("expected terraform validate to fail for instance_shape.os = 'freebsd'")
	}
}
