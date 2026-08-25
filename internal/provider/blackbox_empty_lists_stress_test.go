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

// TestBlackBox_Challenger_ComprehensiveRunnerProfileTransitions tests deep transitions:
// Step 1: Create runner profile with empty lists (experimental_features=[], selectors=[], allow_commit_from_branch=[]).
// Step 2: Plan -> zero diff.
// Step 3: Update to non-empty lists.
// Step 4: Plan -> zero diff.
// Step 5: Update back to empty lists.
// Step 6: Plan -> zero diff.
// Step 7: Update to omit optional list blocks.
// Step 8: Plan -> zero diff.
func TestBlackBox_Challenger_ComprehensiveRunnerProfileTransitions(t *testing.T) {
	h := setupBlackBoxHarness(t)

	var mu sync.Mutex
	profiles := make(map[string]*githubv1beta.RunnerProfileWithStatus)

	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		profID := "profile-bb-transitions"
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

	// Step 1: Explicit empty lists
	step1HCL := `
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

resource "namespace_github_runner_profile" "test" {
  tag                   = "bb-runner-trans"
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
      volume_tag               = "vol1"
      volume_size_gb           = 25
      allow_commit_from_branch = []
    },
    {
      volume_tag               = "vol2"
      volume_size_gb           = 50
      allow_commit_from_branch = ["main"]
    }
  ]
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step1HCL), 0600); err != nil {
		t.Fatalf("failed to write step 1 main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken,
	}

	if initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	// Step 1 Apply
	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 1 Apply (empty lists) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 2 Plan (zero diff)
	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 2 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 3: Update to non-empty lists
	step3HCL := `
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

resource "namespace_github_runner_profile" "test" {
  tag                   = "bb-runner-trans"
  os                    = "ubuntu-24.04"
  experimental_features = ["nested-virt"]

  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = "linux"
    selectors = [
      {
        name  = "tier"
        value = "high-memory"
      }
    ]
  }

  cache_volume_settings = [
    {
      volume_tag               = "vol1"
      volume_size_gb           = 25
      allow_commit_from_branch = ["develop", "feature/*"]
    },
    {
      volume_tag               = "vol2"
      volume_size_gb           = 50
      allow_commit_from_branch = ["main", "release/*"]
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step3HCL), 0600); err != nil {
		t.Fatalf("failed to write step 3 main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 3 Apply (update to non-empty) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 4 Plan (zero diff)
	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 4 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 5: Update back to empty lists
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step1HCL), 0600); err != nil {
		t.Fatalf("failed to write step 5 main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 5 Apply (update back to empty) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 6 Plan (zero diff)
	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 6 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 7: Update to sparse / omitted lists
	step7HCL := `
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

resource "namespace_github_runner_profile" "test" {
  tag = "bb-runner-trans"
  os  = "ubuntu-24.04"
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step7HCL), 0600); err != nil {
		t.Fatalf("failed to write step 7 main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 7 Apply (update to omitted) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 8 Plan (zero diff)
	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 8 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}
}

// TestBlackBox_Challenger_ComprehensiveTenantGroupTransitions tests:
// Step 1: Create tenant group with policies = []
// Step 2: Plan -> zero diff
// Step 3: Update to policies = [p1, p2]
// Step 4: Plan -> zero diff
// Step 5: Update back to policies = []
// Step 6: Plan -> zero diff
// Step 7: Update to omit policies block entirely
// Step 8: Plan -> zero diff
// Step 9: Update back to policies = []
// Step 10: Plan -> zero diff
func TestBlackBox_Challenger_ComprehensiveTenantGroupTransitions(t *testing.T) {
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
			EnrolledTenantIds: []string{"tenant-1", "tenant-2"},
		}
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	// Step 1: policies = []
	step1HCL := `
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

resource "namespace_tenant_group" "grp" {
  name     = "bb-tg-transitions"
  policies = []
}
`

	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step1HCL), 0600); err != nil {
		t.Fatalf("failed to write step 1 main.tf: %v", err)
	}

	tfEnv := []string{
		"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken,
	}

	if initStdout, initStderr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("terraform init failed: %v\nStderr: %s\nStdout: %s", err, initStderr, initStdout)
	}

	// Step 1 Apply
	stdout, stderr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 1 Apply (policies=[]) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 2 Plan (zero diff)
	planStdout, planStderr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 2 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 3: Update to multiple policies
	step3HCL := `
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

resource "namespace_tenant_group" "grp" {
  name = "bb-tg-transitions"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value  = "{\"max_instances\": 10}"
    },
    {
      policy = "namespace.cloud.iam.v1beta.SecurityPolicy"
      value  = "{\"require_mfa\": true}"
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step3HCL), 0600); err != nil {
		t.Fatalf("failed to write step 3 main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 3 Apply (update to multi policies) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 4 Plan (zero diff)
	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 4 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 5: Update back to policies = []
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step1HCL), 0600); err != nil {
		t.Fatalf("failed to write step 5 main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 5 Apply (update back to policies=[]) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 6 Plan (zero diff)
	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 6 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 7: Update to omit policies
	step7HCL := `
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

resource "namespace_tenant_group" "grp" {
  name = "bb-tg-transitions"
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step7HCL), 0600); err != nil {
		t.Fatalf("failed to write step 7 main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 7 Apply (update to omitted policies) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 8 Plan (zero diff)
	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 8 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}

	// Step 9: Update back to policies = []
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(step1HCL), 0600); err != nil {
		t.Fatalf("failed to write step 9 main.tf: %v", err)
	}

	stdout, stderr, err = h.runTerraform(t, tfEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("Step 9 Apply (update back to policies=[]) failed: %v\nStderr: %s\nStdout: %s", err, stderr, stdout)
	}

	// Step 10 Plan (zero diff)
	planStdout, planStderr, err = h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("Step 10 Plan should report 0 changes: %v\nStderr: %s\nStdout: %s", err, planStderr, planStdout)
	}
}
