package provider_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBlackBox_PlanInspection_DeepJSON_VariableSensitivity validates that:
// 1. All sensitive Terraform variables and provider credentials are typed and flagged as sensitive in the plan JSON.
// 2. Ephemeral resources are never emitted into PlannedValues, ResourceChanges, or PriorState in the plan JSON.
// 3. Ephemeral resource outputs and values are strictly excluded from the persisted plan artifact.
func TestBlackBox_PlanInspection_DeepJSON_VariableSensitivity(t *testing.T) {
	h := setupBlackBoxHarness(t)

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "master_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "workload_audience" {
  type    = string
  default = "https://iam.amazonaws.com/oidc"
}

provider "namespace" {
  token = var.master_token
}

ephemeral "namespace_id_token" "id_tok" {
  audience = var.workload_audience
}

ephemeral "namespace_tenant_token" "tenant_tok" {
  tenant_id     = "tenant-plan-deep-001"
  actor_id      = "actor-plan-deep-001"
  duration_secs = 600
}

ephemeral "namespace_client_certificate" "cert" {
  tenant_id     = "tenant-plan-deep-001"
  actor_id      = "actor-plan-deep-001"
  duration_secs = 1200
}

ephemeral "namespace_bazel_cache" "b_cache" {
  key = "ci"
}

ephemeral "namespace_gradle_cache" "g_cache" {
  name = "deep-gradle-cache"
}

ephemeral "namespace_http_cache" "h_cache" {
  name = "deep-http-cache"
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_master_token=" + blackboxSensitiveProviderToken,
	}

	planFile := "deep_plan.tfplan"
	stdout, stderr, err := h.runTerraform(t, tfEnv, "plan", "-out="+planFile)
	if err != nil {
		t.Fatalf("terraform plan failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Assert CLI outputs contain zero leaked secrets
	assertNoSecretsInCLIOutput(t, stdout+"\n"+stderr, allBlackboxSecrets(), "terraform plan (deep JSON)")

	// Inspect Plan JSON
	planJSON := h.showPlanJSON(t, planFile, tfEnv)

	// 1. Verify variable sensitivity metadata
	if planJSON.Config == nil || planJSON.Config.RootModule == nil {
		t.Fatalf("expected plan JSON to contain config root_module")
	}

	masterVar, ok := planJSON.Config.RootModule.Variables["master_token"]
	if !ok {
		t.Fatalf("master_token variable not found in plan JSON configuration")
	}
	if !masterVar.Sensitive {
		t.Errorf("expected master_token variable to have Sensitive=true in plan JSON")
	}

	// 2. Verify planned values have NO ephemeral resources
	if planJSON.PlannedValues != nil && planJSON.PlannedValues.RootModule != nil {
		if len(planJSON.PlannedValues.RootModule.Resources) != 0 {
			t.Errorf("expected 0 planned values resources, got %d", len(planJSON.PlannedValues.RootModule.Resources))
		}
	}

	// 3. Verify resource changes have NO ephemeral resources
	for _, change := range planJSON.ResourceChanges {
		for _, ephType := range []string{
			"namespace_id_token",
			"namespace_tenant_token",
			"namespace_client_certificate",
			"namespace_bazel_cache",
			"namespace_gradle_cache",
			"namespace_http_cache",
		} {
			if change.Type == ephType {
				t.Errorf("ephemeral resource %q of type %q unexpectedly found in plan ResourceChanges", change.Name, change.Type)
			}
		}
	}

	// 4. Verify prior state is empty
	if planJSON.PriorState != nil && planJSON.PriorState.Values != nil && planJSON.PriorState.Values.RootModule != nil {
		if len(planJSON.PriorState.Values.RootModule.Resources) != 0 {
			t.Errorf("expected 0 prior state resources, got %d", len(planJSON.PriorState.Values.RootModule.Resources))
		}
	}
}

// TestBlackBox_PlanInspection_BinaryPlanSafety scans the compiled binary `.tfplan` file directly
// byte-by-byte to guarantee that no ephemeral secrets, passwords, or certificate keys were serialized
// into the plan file on disk.
func TestBlackBox_PlanInspection_BinaryPlanSafety(t *testing.T) {
	h := setupBlackBoxHarness(t)

	mainTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "provider_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

provider "namespace" {
  token = var.provider_token
}

ephemeral "namespace_id_token" "id_tok" {
  audience = "https://vault.example.com"
}

ephemeral "namespace_tenant_token" "tenant_tok" {
  tenant_id     = "tenant-binary-scan-001"
  actor_id      = "actor-binary-scan-001"
  duration_secs = 300
}

ephemeral "namespace_client_certificate" "cert" {
  tenant_id     = "tenant-binary-scan-001"
  actor_id      = "actor-binary-scan-001"
  duration_secs = 300
}

ephemeral "namespace_bazel_cache" "b_cache" {
  key = "ci"
}

ephemeral "namespace_gradle_cache" "g_cache" {
  name = "binary-gradle-cache"
}

ephemeral "namespace_http_cache" "h_cache" {
  name = "binary-http-cache"
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_provider_token=" + blackboxSensitiveProviderToken,
	}

	planFile := "binary_safety.tfplan"
	stdout, stderr, err := h.runTerraform(t, tfEnv, "plan", "-out="+planFile)
	if err != nil {
		t.Fatalf("terraform plan failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Verify CLI output
	assertNoSecretsInCLIOutput(t, stdout+"\n"+stderr, allBlackboxSecrets(), "terraform plan (binary safety)")

	// Read binary plan file
	planBytes, err := os.ReadFile(filepath.Join(h.workDir, planFile))
	if err != nil {
		t.Fatalf("failed to read binary plan file: %v", err)
	}

	// Strict scan for all 8 sensitive credentials in binary plan file
	assertNoSecretsInRawBytes(t, planBytes, allBlackboxSecrets(), "binary plan file (binary_safety.tfplan)")
}

// TestBlackBox_PlanInspection_EphemeralOutputConsumption_DownstreamModule tests that a root module
// can open ephemeral resources, pass the resulting secrets to child modules with ephemeral inputs,
// and execute both plan and apply cleanly while leaving ZERO traces of secrets in state or plan files.
func TestBlackBox_PlanInspection_EphemeralOutputConsumption_DownstreamModule(t *testing.T) {
	h := setupBlackBoxHarness(t)

	// Child module setup
	childDir := filepath.Join(h.workDir, "modules", "consumer_layer")
	if err := os.MkdirAll(childDir, 0700); err != nil {
		t.Fatalf("failed to create child module dir: %v", err)
	}

	childTF := `
variable "downstream_id_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "downstream_tenant_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "downstream_cert_pem" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "downstream_private_key" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "downstream_gradle_pass" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "downstream_http_pass" {
  type      = string
  sensitive = true
  ephemeral = true
}

output "echo_token" {
  value     = var.downstream_id_token
  sensitive = true
  ephemeral = true
}

output "echo_cert" {
  value     = var.downstream_cert_pem
  sensitive = true
  ephemeral = true
}
`
	if err := os.WriteFile(filepath.Join(childDir, "main.tf"), []byte(childTF), 0600); err != nil {
		t.Fatalf("failed to write child main.tf: %v", err)
	}

	rootTF := `
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "root_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

provider "namespace" {
  token = var.root_token
}

ephemeral "namespace_id_token" "id_tok" {
  audience = "https://downstream.example.com"
}

ephemeral "namespace_tenant_token" "tenant_tok" {
  tenant_id     = "tenant-downstream-001"
  actor_id      = "actor-downstream-001"
  duration_secs = 600
}

ephemeral "namespace_client_certificate" "cert" {
  tenant_id     = "tenant-downstream-001"
  actor_id      = "actor-downstream-001"
  duration_secs = 1200
}

ephemeral "namespace_gradle_cache" "g_cache" {
  name = "downstream-gradle-cache"
}

ephemeral "namespace_http_cache" "h_cache" {
  name = "downstream-http-cache"
}

module "consumer_app" {
  source                  = "./modules/consumer_layer"
  downstream_id_token     = ephemeral.namespace_id_token.id_tok.id_token
  downstream_tenant_token = ephemeral.namespace_tenant_token.tenant_tok.bearer_token
  downstream_cert_pem     = ephemeral.namespace_client_certificate.cert.client_certificate_pem
  downstream_private_key  = ephemeral.namespace_client_certificate.cert.private_key_pem
  downstream_gradle_pass  = ephemeral.namespace_gradle_cache.g_cache.password
  downstream_http_pass    = ephemeral.namespace_http_cache.h_cache.password
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(rootTF), 0600); err != nil {
		t.Fatalf("failed to write root main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_root_token=" + blackboxSensitiveProviderToken,
	}

	// 1. Initialize modules
	if initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	// 2. Plan to binary plan
	planFile := "downstream.tfplan"
	stdout, stderr, err := h.runTerraform(t, tfEnv, "plan", "-out="+planFile)
	if err != nil {
		t.Fatalf("terraform plan failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// 3. Apply binary plan
	applyStdout, applyStderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve", planFile)
	if err != nil {
		t.Fatalf("terraform apply failed: %v\nStderr: %s\nStdout: %s", err, applyStderr, applyStdout)
	}

	// 4. Verify outputs and state
	assertNoSecretsInCLIOutput(t, stdout+"\n"+stderr, allBlackboxSecrets(), "downstream consumption plan")
	assertNoSecretsInCLIOutput(t, applyStdout+"\n"+applyStderr, allBlackboxSecrets(), "downstream consumption apply")

	state, stateBytes := h.readState(t)
	if len(state.Resources) != 0 {
		t.Errorf("expected 0 resources in state for downstream ephemeral consumption, got %d", len(state.Resources))
	}
	assertNoSecretsInRawBytes(t, stateBytes, allBlackboxSecrets(), "terraform.tfstate (downstream consumption)")
}
