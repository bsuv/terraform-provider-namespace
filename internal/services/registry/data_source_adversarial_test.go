package registry_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// TestRegistry_Adversarial_DeterministicSortingSameTimestamp verifies that when multiple images
// share the exact same timestamp, sorting falls back deterministically to digest.
func TestRegistry_Adversarial_DeterministicSortingSameTimestamp(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	sameTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images: []*registryv1beta.Image{
				{
					Repository: "sort-repo",
					Digest:     "sha256:zzzzzz",
					CreatedAt:  timestamppb.New(sameTime),
				},
				{
					Repository: "sort-repo",
					Digest:     "sha256:aaaaaa",
					CreatedAt:  timestamppb.New(sameTime),
				},
				{
					Repository: "sort-repo",
					Digest:     "sha256:mmmmmm",
					CreatedAt:  timestamppb.New(sameTime),
				},
			},
		}, nil
	}

	ds, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	cfg := registry.RegistryImagesDataSourceModel{
		ID:         types.StringNull(),
		Repository: types.StringValue("sort-repo"),
		Images:     nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	ds.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var state registry.RegistryImagesDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if len(state.Images) != 3 {
		t.Fatalf("expected 3 images, got %d", len(state.Images))
	}

	if state.Images[0].Digest.ValueString() != "sha256:aaaaaa" ||
		state.Images[1].Digest.ValueString() != "sha256:mmmmmm" ||
		state.Images[2].Digest.ValueString() != "sha256:zzzzzz" {
		t.Errorf("expected deterministic digest sort [aaaaaa, mmmmmm, zzzzzz], got [%s, %s, %s]",
			state.Images[0].Digest.ValueString(), state.Images[1].Digest.ValueString(), state.Images[2].Digest.ValueString())
	}
}

// TestRegistry_Adversarial_ConcurrentReadStress tests 50 concurrent GetImage and ListImages calls.
func TestRegistry_Adversarial_ConcurrentReadStress(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	srv.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		return &registryv1beta.GetImageResponse{
			Image: &registryv1beta.Image{
				Repository: req.GetRepository(),
				Digest:     fmt.Sprintf("sha256:%s", req.GetReference()),
				CreatedAt:  timestamppb.New(createdTime),
			},
		}, nil
	}

	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images: []*registryv1beta.Image{
				{
					Repository: "stress-repo",
					Digest:     "sha256:img1",
					CreatedAt:  timestamppb.New(createdTime),
				},
			},
		}, nil
	}

	dsSingle, schemaSingle := setupTestDataSourceRegistryImage(t, srv)
	dsList, schemaList := setupTestDataSourceRegistryImages(t, srv)

	var wg sync.WaitGroup
	var successCount atomic.Int64
	numWorkers := 50

	for i := 0; i < numWorkers; i++ {
		wg.Add(2)

		// 1. GetImage
		go func(idx int) {
			defer wg.Done()
			cfg := registry.RegistryImageDataSourceModel{
				ID:         types.StringNull(),
				Repository: types.StringValue("stress-repo"),
				Reference:  types.StringValue(fmt.Sprintf("tag-%d", idx)),
				Digest:     types.StringNull(),
				CreatedAt:  types.StringNull(),
				ExpiresAt:  types.StringNull(),
				SizeBytes:  types.Int64Null(),
				Tags:       nil,
			}
			st := tfsdk.State{Schema: schemaSingle.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaSingle.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaSingle.Schema}}
			dsSingle.Read(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				successCount.Add(1)
			}
		}(i)

		// 2. ListImages
		go func(idx int) {
			defer wg.Done()
			cfg := registry.RegistryImagesDataSourceModel{
				ID:         types.StringNull(),
				Repository: types.StringValue("stress-repo"),
				Images:     nil,
			}
			st := tfsdk.State{Schema: schemaList.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaList.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaList.Schema}}
			dsList.Read(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if successCount.Load() != int64(numWorkers*2) {
		t.Errorf("expected %d successes, got %d", numWorkers*2, successCount.Load())
	}
}
