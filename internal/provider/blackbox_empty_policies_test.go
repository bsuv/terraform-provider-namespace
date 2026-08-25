package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestBlackBox_Challenger_TenantGroup_ExplicitEmptyPoliciesList tests applying a tenant group
// with explicit empty policies list `policies = []` in HCL.
func TestBlackBox_Challenger_TenantGroup_ExplicitEmptyPoliciesList(t *testing.T) {
	h := setupBlackBoxHarness(t)

	var mu sync.Mutex
	groups := make(map[string]*iamv1beta.DescribeTenantGroupResponse)

	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		g, ok := groups[req.Name]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}

	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var p []*iamv1beta.TenantGroupPolicy
		if len(req.Policies) > 0 {
			p = req.Policies
		}
		groups[req.Name] = &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          p,
			EnrolledTenantIds: []string{},
		}
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
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

resource "namespace_tenant_group" "empty_pol" {
  name     = "bb-empty-policy-group"
  policies = []
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

	// Step 1: Initial apply with empty policies list
	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on empty policies list: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after apply on empty policies should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 2: Transition from empty policies to populated policies
	populatedTF := `
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

resource "namespace_tenant_group" "empty_pol" {
  name     = "bb-empty-policy-group"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value  = "{\"max_instances\": 10}"
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(populatedTF), 0600); err != nil {
		t.Fatalf("failed to write populated main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on transition to populated policies: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after transition to populated policies should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 3: Transition back from populated policies to empty list
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write empty main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on transition back to empty policies: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after transition back to empty policies should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}
}
