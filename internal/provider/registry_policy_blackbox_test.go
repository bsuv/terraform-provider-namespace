package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestBlackBox_RegistryPolicy_InPlaceUpdatesIncrementRevision(t *testing.T) {
	t.Run("RepositoryPolicy", func(t *testing.T) {
		h := setupBlackBoxHarness(t)
		var mu sync.Mutex
		policies := map[string]*registryv1beta.GetRepositoryPolicyResponse{}
		h.mockServer.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			p, ok := policies[req.GetRepository()]
			if !ok {
				return nil, status.Errorf(codes.NotFound, "not found")
			}
			return proto.Clone(p).(*registryv1beta.GetRepositoryPolicyResponse), nil
		}
		h.mockServer.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			mu.Lock()
			defer mu.Unlock()
			var expected int64
			if cur, ok := policies[req.GetRepository()]; ok {
				expected = cur.GetRevision()
			}
			if req.GetRevision() != expected {
				return nil, status.Errorf(codes.FailedPrecondition, "revision mismatch: got %d want %d", req.GetRevision(), expected)
			}
			if req.GetExpiration() == nil && req.GetExpirationEnforcement() == registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN {
				delete(policies, req.GetRepository())
				return &emptypb.Empty{}, nil
			}
			var expClone *durationpb.Duration
			if req.GetExpiration() != nil {
				expClone = proto.Clone(req.GetExpiration()).(*durationpb.Duration)
			}
			policies[req.GetRepository()] = &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              expected + 1,
				Expiration:            expClone,
				ExpirationEnforcement: req.GetExpirationEnforcement(),
			}
			return &emptypb.Empty{}, nil
		}

		hdr := `
terraform {
  required_providers {
    namespace = { source = "bsuv/namespace" }
  }
}
variable "nsc_token" {
  type      = string
  sensitive = true
}
provider "namespace" {
  token = var.nsc_token
}
`
		tfEnv := []string{"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken}
		write := func(body string) {
			if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(hdr+body), 0600); err != nil {
				t.Fatalf("write main.tf: %v", err)
			}
		}

		write(`
resource "namespace_registry_repository_policy" "p" {
  repository             = "app/repo"
  expiration             = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`)
		if out, serr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
			t.Fatalf("init: %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
			t.Fatalf("create apply: %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
			t.Fatalf("post-create plan: %v\n%s\n%s", err, out, serr)
		}

		// In-place update 720h -> 48h (revision 1 -> 2)
		write(`
resource "namespace_registry_repository_policy" "p" {
  repository             = "app/repo"
  expiration             = "48h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`)
		if out, serr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
			t.Fatalf("update apply (720h->48h): %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
			t.Fatalf("post-update plan: %v\n%s\n%s", err, out, serr)
		}

		// In-place update ENFORCE -> DO_NOT_ENFORCE omitting expiration (revision 2 -> 3)
		write(`
resource "namespace_registry_repository_policy" "p" {
  repository             = "app/repo"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE"
}
`)
		if out, serr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
			t.Fatalf("update apply (DO_NOT_ENFORCE): %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
			t.Fatalf("post-do-not-enforce plan: %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "destroy", "-auto-approve"); err != nil {
			t.Fatalf("destroy: %v\n%s\n%s", err, out, serr)
		}
	})

	t.Run("DefaultPolicy", func(t *testing.T) {
		h := setupBlackBoxHarness(t)
		var mu sync.Mutex
		cur := &registryv1beta.GetDefaultPolicyResponse{
			Revision:              1,
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
		}
		h.mockServer.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			return proto.Clone(cur).(*registryv1beta.GetDefaultPolicyResponse), nil
		}
		h.mockServer.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
			mu.Lock()
			defer mu.Unlock()
			if req.GetRevision() != cur.GetRevision() {
				return nil, status.Errorf(codes.FailedPrecondition, "revision mismatch: got %d want %d", req.GetRevision(), cur.GetRevision())
			}
			var expClone *durationpb.Duration
			if req.GetDefaultExpiration() != nil {
				expClone = proto.Clone(req.GetDefaultExpiration()).(*durationpb.Duration)
			}
			cur = &registryv1beta.GetDefaultPolicyResponse{
				Revision:              cur.GetRevision() + 1,
				DefaultExpiration:     expClone,
				ExpirationEnforcement: req.GetExpirationEnforcement(),
			}
			return &emptypb.Empty{}, nil
		}

		hdr := `
terraform {
  required_providers {
    namespace = { source = "bsuv/namespace" }
  }
}
variable "nsc_token" {
  type      = string
  sensitive = true
}
provider "namespace" {
  token = var.nsc_token
}
`
		tfEnv := []string{"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken}
		write := func(body string) {
			if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(hdr+body), 0600); err != nil {
				t.Fatalf("write main.tf: %v", err)
			}
		}

		write(`
resource "namespace_registry_default_policy" "d" {
  default_expiration     = "24h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`)
		if out, serr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
			t.Fatalf("init: %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
			t.Fatalf("create apply: %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
			t.Fatalf("post-create plan: %v\n%s\n%s", err, out, serr)
		}

		write(`
resource "namespace_registry_default_policy" "d" {
  default_expiration     = "48h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`)
		if out, serr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
			t.Fatalf("update apply (24h->48h): %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
			t.Fatalf("post-update plan: %v\n%s\n%s", err, out, serr)
		}

		write(`
resource "namespace_registry_default_policy" "d" {
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE"
}
`)
		if out, serr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
			t.Fatalf("update apply (DO_NOT_ENFORCE): %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "plan", "-detailed-exitcode"); err != nil {
			t.Fatalf("post-do-not-enforce plan: %v\n%s\n%s", err, out, serr)
		}
		if out, serr, err := h.runTerraform(t, tfEnv, "destroy", "-auto-approve"); err != nil {
			t.Fatalf("destroy: %v\n%s\n%s", err, out, serr)
		}
	})
}

func TestBlackBox_UnknownVars_TerraformValidatePasses(t *testing.T) {
	h := setupBlackBoxHarness(t)
	tfCfg := `
terraform {
  required_providers {
    namespace = { source = "bsuv/namespace" }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
}
variable "egress_mode" {
  type = string
}
variable "domains" {
  type = list(string)
}
variable "policy_tag" {
  type = string
}
variable "exp" {
  type = string
}
variable "flag" {
  type = bool
}

provider "namespace" {
  token = var.nsc_token
}

resource "namespace_github_runner_profile" "adv" {
  tag = "unknown-adv"
  os  = "ubuntu-24.04"
  network_policy = {
    egress   = var.egress_mode
    advisory = true
  }
}

resource "namespace_github_runner_profile" "allow" {
  tag = "unknown-allow"
  os  = "ubuntu-24.04"
  network_policy = {
    egress                   = "EGRESS_POLICY_DOMAIN_ALLOW_LIST"
    egress_domain_allow_list = var.domains
  }
}

resource "namespace_github_runner_profile" "tagged" {
  tag = "unknown-tag"
  os  = "ubuntu-24.04"
  network_policy = {
    egress            = "EGRESS_POLICY_TAG"
    egress_policy_tag = var.policy_tag
  }
}

resource "namespace_github_runner_profile" "cond_cv" {
  tag = "unknown-cond-cv"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb          = var.flag ? 20 : 40
      volume_reserved_size_gb = 10
    }
  ]
}

resource "namespace_registry_repository_policy" "repo" {
  repository             = "app/repo"
  expiration             = var.exp
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}

resource "namespace_registry_default_policy" "def" {
  default_expiration     = var.exp
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(tfCfg), 0600); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	if out, serr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("init: %v\n%s\n%s", err, out, serr)
	}
	if out, serr, err := h.runTerraform(t, nil, "validate"); err != nil {
		t.Fatalf("terraform validate with unknown variables failed: %v\n%s\n%s", err, out, serr)
	}
}

func TestBlackBox_UnknownListElement_NoValueConversionError(t *testing.T) {
	h := setupBlackBoxHarness(t)
	tfCfg := `
terraform {
  required_providers {
    namespace = { source = "bsuv/namespace" }
  }
}

variable "flag" {
  type = bool
}

provider "namespace" {}

resource "namespace_github_runner_profile" "cond_obj" {
  tag = "unknown-cond-obj"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    var.flag ? { volume_size_gb = 20 } : { volume_size_gb = 40 }
  ]
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(tfCfg), 0600); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	if out, serr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("init: %v\n%s\n%s", err, out, serr)
	}
	out, serr, _ := h.runTerraform(t, nil, "validate")
	combined := out + "\n" + serr
	if strings.Contains(combined, "Value Conversion Error") || strings.Contains(combined, "This is always an error in the provider") {
		t.Fatalf("unexpected provider Value Conversion Error on unknown list element:\n%s", combined)
	}
}
