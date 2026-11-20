package provider_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBlackBox_RegistryImagesAndImage_FiltersDeletedAtLabels(t *testing.T) {
	h := setupBlackBoxHarness(t)

	seed := []*registryv1beta.Image{
		{
			Repository: "app/service",
			Digest:     "sha256:live1",
			CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
			ExpiresAt:  timestamppb.New(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)),
			Labels: []*stdlib.Label{
				{Name: "env", Value: "prod"},
			},
			Sizes: &registryv1beta.Image_Sizes{Total: 1024},
			Tags:  []string{"v1"},
		},
		{
			Repository: "app/service",
			Digest:     "sha256:deleted2",
			CreatedAt:  timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
			ExpiresAt:  timestamppb.New(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)),
			DeletedAt:  timestamppb.New(time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC)),
			Sizes:      &registryv1beta.Image_Sizes{Total: 2048},
			Tags:       []string{"v2-del"},
		},
		{
			Repository: "app/service",
			Digest:     "sha256:neverexp",
			CreatedAt:  timestamppb.New(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
			Sizes:      &registryv1beta.Image_Sizes{Total: 512},
			Tags:       []string{"v3"},
		},
	}
	h.mockServer.Registry.EnableStatefulImages(seed, 1)

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

data "namespace_registry_images" "active_window" {
  repository      = "app/service"
  deletion_filter = "EXCLUDE_DELETED"
  expires_at = {
    after  = "2026-11-10T00:00:00Z"
    before = "2026-11-25T00:00:00Z"
  }
}

data "namespace_registry_images" "with_deleted" {
  repository      = "app/service"
  deletion_filter = "INCLUDE_DELETED"
}

data "namespace_registry_image" "deleted_one" {
  repository = "app/service"
  reference  = "v2-del"
}
`
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(mainTF), 0600); err != nil {
		t.Fatalf("failed to write main.tf: %v", err)
	}

	tfEnv := []string{"TF_VAR_nsc_token=" + blackboxSensitiveProviderToken}
	if out, serr, err := h.runTerraform(t, nil, "init", "-backend=false"); err != nil {
		t.Fatalf("init failed: %v\n%s\n%s", err, out, serr)
	}
	if out, serr, err := h.runTerraform(t, tfEnv, "apply", "-auto-approve"); err != nil {
		t.Fatalf("apply failed: %v\n%s\n%s", err, out, serr)
	}

	st, _ := h.readState(t)
	var seenActive, seenAll, seenSingle bool
	for _, r := range st.Resources {
		inst := r.Instances[0].(map[string]any)
		attrs := inst["attributes"].(map[string]any)
		switch r.Name {
		case "active_window":
			seenActive = true
			imgs, _ := attrs["images"].([]any)
			if len(imgs) != 1 {
				t.Fatalf("expected 1 image in active_window, got %d", len(imgs))
			}
			img0 := imgs[0].(map[string]any)
			if img0["digest"] != "sha256:live1" {
				t.Errorf("expected sha256:live1, got %v", img0["digest"])
			}
			if img0["deleted_at"] != nil {
				t.Errorf("expected null deleted_at, got %v", img0["deleted_at"])
			}
			lbls := img0["labels"].(map[string]any)
			if lbls["env"] != "prod" {
				t.Errorf("expected labels.env=prod, got %#v", lbls)
			}
		case "with_deleted":
			seenAll = true
			imgs, _ := attrs["images"].([]any)
			if len(imgs) != 3 {
				t.Fatalf("expected 3 images in with_deleted, got %d", len(imgs))
			}
		case "deleted_one":
			seenSingle = true
			if attrs["deleted_at"] != "2026-11-05T01:02:03Z" {
				t.Errorf("expected deleted_at 2026-11-05T01:02:03Z, got %v", attrs["deleted_at"])
			}
			lbls, ok := attrs["labels"].(map[string]any)
			if !ok || len(lbls) != 0 {
				t.Errorf("expected empty map {} for deleted_one labels, got %#v", attrs["labels"])
			}
		}
	}
	if !seenActive || !seenAll || !seenSingle {
		t.Fatalf("missing state resources: active=%v all=%v single=%v", seenActive, seenAll, seenSingle)
	}

	// Verify inverted expires_at is rejected cleanly at plan/validate time
	badTF := strings.ReplaceAll(mainTF, `after  = "2026-11-10T00:00:00Z"`, `after  = "2026-11-26T00:00:00Z"`)
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(badTF), 0600); err != nil {
		t.Fatalf("failed to write badTF: %v", err)
	}
	out, serr, err := h.runTerraform(t, tfEnv, "plan")
	if err == nil {
		t.Fatalf("expected error for inverted expires_at range, got success:\n%s\n%s", out, serr)
	}
	combined := out + "\n" + serr
	if !strings.Contains(combined, "expires_at") {
		t.Errorf("expected expires_at error diagnostic, got:\n%s", combined)
	}
}
