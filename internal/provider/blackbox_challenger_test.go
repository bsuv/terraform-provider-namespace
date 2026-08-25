package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestBlackBox_Challenger_RunnerProfile_EmptyListsAndTransitions tests applying a runner profile
// with explicit empty lists in HCL (`selectors = []`, `allow_commit_from_branch = []`), verifying
// that terraform apply succeeds and subsequent plans report zero diff.
func TestBlackBox_Challenger_RunnerProfile_EmptyListsAndTransitions(t *testing.T) {
	h := setupBlackBoxHarness(t)

	var mu sync.Mutex
	profiles := make(map[string]*githubv1beta.RunnerProfileWithStatus)

	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		profID := "profile-bb-empty-lists"
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
		p := profiles[req.GetProfileId()]
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

resource "namespace_github_runner_profile" "empty_lists" {
  tag                   = "bb-empty-lists-runner"
  os                    = "ubuntu-24.04"
  experimental_features = []

  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = "linux"
    selectors        = []
  }

  cache_volume_settings = [
    {
      volume_tag               = "empty-branch-cache"
      volume_size_gb           = 25
      volume_reserved_size_gb  = 0
      allow_commit_from_branch = []
    }
  ]
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
		t.Fatalf("terraform apply failed on empty lists config: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	state, _ := h.readState(t)
	if len(state.Resources) != 1 {
		t.Fatalf("expected 1 resource in state, got %d", len(state.Resources))
	}

	// Verify plan reports zero changes
	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after apply should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 2: Transition from empty lists to populated lists
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

resource "namespace_github_runner_profile" "empty_lists" {
  tag                   = "bb-empty-lists-runner"
  os                    = "ubuntu-24.04"
  experimental_features = ["fast-disk", "nested-virt"]

  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = "linux"
    selectors = [
      {
        name  = "tier"
        value = "high"
      }
    ]
  }

  cache_volume_settings = [
    {
      volume_tag               = "empty-branch-cache"
      volume_size_gb           = 25
      volume_reserved_size_gb  = 0
      allow_commit_from_branch = ["main", "release"]
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(populatedTF), 0600); err != nil {
		t.Fatalf("failed to write populated main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on transition to populated lists: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after transition to populated lists should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 3: Transition back from populated lists to explicit empty lists
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write empty main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed on transition back to empty lists: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after transition back to empty lists should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}
}

// TestBlackBox_Challenger_TenantGroup_MultiPolicyLifecycle tests applying a tenant group
// with multiple policies, updating policies in place, and removing all policies.
func TestBlackBox_Challenger_TenantGroup_MultiPolicyLifecycle(t *testing.T) {
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
		groups[req.Name] = &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          req.Policies,
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

resource "namespace_tenant_group" "multi_pol" {
  name = "bb-multi-policy-group"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value  = "{\"max_instances\": 20}"
    },
    {
      policy = "namespace.cloud.iam.v1beta.SecurityPolicy"
      value  = "{\"require_mfa\": true}"
    }
  ]
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

	// 1. Initial Apply
	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Plan should have 0 diff
	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after apply should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// 2. Update to omit policies
	omittedPoliciesTF := `
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

resource "namespace_tenant_group" "multi_pol" {
  name = "bb-multi-policy-group"
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(omittedPoliciesTF), 0600); err != nil {
		t.Fatalf("failed to write updated main.tf: %v", err)
	}

	upStdout, upStderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply update to omit policies failed: %v\nStderr: %s\nStdout: %s", err, upStderr, upStdout)
	}

	// Plan should have 0 diff
	planStdout2, planStderr2, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("terraform plan after update to omit policies should indicate 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr2, planStdout2)
	}
}
