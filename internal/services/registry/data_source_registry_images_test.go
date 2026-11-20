package registry_test

import (
	"context"
	"strings"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceRegistryImages(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := registry.NewRegistryImagesDataSource()

	ctx := context.Background()
	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)

	var confResp datasource.ConfigureResponse
	if confD, ok := d.(datasource.DataSourceWithConfigure); ok {
		confD.Configure(ctx, datasource.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return d, schemaResp
}

func TestRegistryImagesDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := registry.NewRegistryImagesDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_registry_images" {
		t.Fatalf("expected type name 'namespace_registry_images', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{"id", "repository", "deletion_filter", "expires_at", "images"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}
	if expAttr, ok := attrs["expires_at"].(schema.SingleNestedAttribute); !ok {
		t.Errorf("expected expires_at to be SingleNestedAttribute, got %T", attrs["expires_at"])
	} else {
		for _, sub := range []string{"after", "before"} {
			if _, ok := expAttr.Attributes[sub]; !ok {
				t.Errorf("missing expires_at.%s attribute", sub)
			}
		}
	}
	if imgsAttr, ok := attrs["images"].(schema.ListNestedAttribute); !ok {
		t.Errorf("expected images to be ListNestedAttribute, got %T", attrs["images"])
	} else {
		for _, sub := range []string{"repository", "digest", "created_at", "expires_at", "deleted_at", "size_bytes", "labels", "tags"} {
			if _, ok := imgsAttr.NestedObject.Attributes[sub]; !ok {
				t.Errorf("missing images.%s attribute", sub)
			}
		}
	}
}

func TestRegistryImagesDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := registry.NewRegistryImagesDataSource()

	var confResp datasource.ConfigureResponse
	if confD, ok := d.(datasource.DataSourceWithConfigure); ok {
		confD.Configure(ctx, datasource.ConfigureRequest{
			ProviderData: "invalid-type",
		}, &confResp)
		if !confResp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for invalid configure provider data")
		}
	}
}

func TestRegistryImagesDataSource_Read_MultiPageAndDeterministicSorting(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	timeOlder := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	timeNewer := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		if req.GetMatchRepository() == nil {
			t.Error("expected MatchRepository to be set")
		} else {
			if req.GetMatchRepository().GetOp() != stdlib.StringMatcher_IS_ANY_OF {
				t.Errorf("expected MatchRepository Op to be IS_ANY_OF, got %v", req.GetMatchRepository().GetOp())
			}
			if len(req.GetMatchRepository().GetValues()) != 1 || req.GetMatchRepository().GetValues()[0] != "app-repo" {
				t.Errorf("expected MatchRepository Values to be ['app-repo'], got %v", req.GetMatchRepository().GetValues())
			}
		}
		cursor := string(req.GetPaginationCursor())
		switch cursor {
		case "":
			// Page 1: return newer image first to verify sort puts older image first
			return &registryv1beta.ListImagesResponse{
				Images: []*registryv1beta.Image{
					{
						Repository: "app-repo",
						Digest:     "sha256:bbbbbb",
						CreatedAt:  timestamppb.New(timeNewer),
						Sizes:      &registryv1beta.Image_Sizes{Total: 500},
						Tags:       []string{"v2.0.0"},
					},
				},
				PaginationCursor: []byte("page-2-cursor"),
			}, nil
		case "page-2-cursor":
			// Page 2: return older image
			return &registryv1beta.ListImagesResponse{
				Images: []*registryv1beta.Image{
					{
						Repository: "app-repo",
						Digest:     "sha256:aaaaaa",
						CreatedAt:  timestamppb.New(timeOlder),
						Sizes:      &registryv1beta.Image_Sizes{Total: 400},
						Tags:       []string{"v1.0.0"},
					},
				},
				PaginationCursor: nil, // terminal page
			}, nil
		default:
			return nil, status.Errorf(codes.InvalidArgument, "invalid cursor %q", cursor)
		}
	}

	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	cfg := registry.RegistryImagesDataSourceModel{
		ID:             types.StringNull(),
		Repository:     types.StringValue("app-repo"),
		DeletionFilter: types.StringNull(),
		ExpiresAt:      types.ObjectNull(registry.ExpiresAtFilterAttrTypes),
		Images:         nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read diagnostics: %v", resp.Diagnostics)
	}

	var state registry.RegistryImagesDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "app-repo" {
		t.Errorf("expected ID 'app-repo', got %q", state.ID.ValueString())
	}
	if len(state.Images) != 2 {
		t.Fatalf("expected 2 aggregated images from 2 pages, got %d", len(state.Images))
	}

	// Verify deterministic sorting: older image (sha256:aaaaaa) should be first
	if state.Images[0].Digest.ValueString() != "sha256:aaaaaa" {
		t.Errorf("expected first image digest 'sha256:aaaaaa', got %q", state.Images[0].Digest.ValueString())
	}
	if state.Images[1].Digest.ValueString() != "sha256:bbbbbb" {
		t.Errorf("expected second image digest 'sha256:bbbbbb', got %q", state.Images[1].Digest.ValueString())
	}
}

func TestRegistryImagesDataSource_Read_PaginationCycleDetection(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Returns a repeating cursor causing cycle
	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images: []*registryv1beta.Image{
				{
					Repository: "loop-repo",
					Digest:     "sha256:loop1",
					CreatedAt:  timestamppb.Now(),
				},
			},
			PaginationCursor: []byte("same-cursor"),
		}, nil
	}

	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	cfg := registry.RegistryImagesDataSourceModel{
		ID:             types.StringNull(),
		Repository:     types.StringValue("loop-repo"),
		DeletionFilter: types.StringNull(),
		ExpiresAt:      types.ObjectNull(registry.ExpiresAtFilterAttrTypes),
		Images:         nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for pagination cycle detection")
	}
}

func TestRegistryImagesDataSource_Read_EmptyRepository(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images:           []*registryv1beta.Image{},
			PaginationCursor: nil,
		}, nil
	}

	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	cfg := registry.RegistryImagesDataSourceModel{
		ID:             types.StringNull(),
		Repository:     types.StringValue("empty-repo"),
		DeletionFilter: types.StringNull(),
		ExpiresAt:      types.ObjectNull(registry.ExpiresAtFilterAttrTypes),
		Images:         nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read diagnostics: %v", resp.Diagnostics)
	}

	var state registry.RegistryImagesDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.Images == nil || len(state.Images) != 0 {
		t.Errorf("expected non-nil empty slice for images, got %v", state.Images)
	}
}

func TestRegistryImagesDataSource_ConfigValidators_ExpiresAtRange(t *testing.T) {
	ctx := context.Background()
	d := registry.NewRegistryImagesDataSource()

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)

	valD, ok := d.(datasource.DataSourceWithConfigValidators)
	if !ok {
		t.Fatal("expected registryImagesDataSource to implement DataSourceWithConfigValidators")
	}
	validators := valD.ConfigValidators(ctx)
	if len(validators) == 0 {
		t.Fatal("expected at least one config validator")
	}

	makeConfig := func(expObj types.Object) tfsdk.Config {
		cfg := registry.RegistryImagesDataSourceModel{
			ID:             types.StringNull(),
			Repository:     types.StringValue("app/web"),
			DeletionFilter: types.StringNull(),
			ExpiresAt:      expObj,
			Images:         nil,
		}
		st := tfsdk.State{Schema: schemaResp.Schema}
		if diags := st.Set(ctx, &cfg); diags.HasError() {
			t.Fatalf("failed to set config: %v", diags)
		}
		return tfsdk.Config{Schema: schemaResp.Schema, Raw: st.Raw}
	}

	makeExpiresObj := func(after, before types.String) types.Object {
		obj, diags := types.ObjectValueFrom(ctx, registry.ExpiresAtFilterAttrTypes, registry.RegistryImagesExpiresAtModel{
			After:  after,
			Before: before,
		})
		if diags.HasError() {
			t.Fatalf("failed to build expires_at object: %v", diags)
		}
		return obj
	}

	t.Run("inverted after > before rejected", func(t *testing.T) {
		var resp datasource.ValidateConfigResponse
		validators[0].ValidateDataSource(ctx, datasource.ValidateConfigRequest{
			Config: makeConfig(makeExpiresObj(types.StringValue("2026-12-01T00:00:00Z"), types.StringValue("2026-11-01T00:00:00Z"))),
		}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error for after > before")
		}
		detail := resp.Diagnostics.Errors()[0].Detail()
		if !strings.Contains(detail, "expires_at.after") || !strings.Contains(detail, "expires_at.before") {
			t.Errorf("expected diagnostic referencing expires_at.after and expires_at.before, got: %s", detail)
		}
	})

	t.Run("equal after == before rejected", func(t *testing.T) {
		var resp datasource.ValidateConfigResponse
		validators[0].ValidateDataSource(ctx, datasource.ValidateConfigRequest{
			Config: makeConfig(makeExpiresObj(types.StringValue("2026-11-15T00:00:00Z"), types.StringValue("2026-11-15T00:00:00Z"))),
		}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error for after == before")
		}
	})

	t.Run("valid after < before accepted", func(t *testing.T) {
		var resp datasource.ValidateConfigResponse
		validators[0].ValidateDataSource(ctx, datasource.ValidateConfigRequest{
			Config: makeConfig(makeExpiresObj(types.StringValue("2026-11-01T00:00:00Z"), types.StringValue("2026-12-01T00:00:00Z"))),
		}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error for valid range: %v", resp.Diagnostics)
		}
	})

	t.Run("partial bounds and unknown values accepted", func(t *testing.T) {
		for _, obj := range []types.Object{
			makeExpiresObj(types.StringValue("2026-11-01T00:00:00Z"), types.StringNull()),
			makeExpiresObj(types.StringNull(), types.StringValue("2026-12-01T00:00:00Z")),
			makeExpiresObj(types.StringNull(), types.StringNull()),
			makeExpiresObj(types.StringUnknown(), types.StringValue("2026-12-01T00:00:00Z")),
			types.ObjectUnknown(registry.ExpiresAtFilterAttrTypes),
		} {
			var resp datasource.ValidateConfigResponse
			validators[0].ValidateDataSource(ctx, datasource.ValidateConfigRequest{
				Config: makeConfig(obj),
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error for partial/unknown expires_at: %v", resp.Diagnostics)
			}
		}
	})
}

func TestRegistryImagesDataSource_Read_UnknownDeferral(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	var called bool
	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		called = true
		return &registryv1beta.ListImagesResponse{}, nil
	}
	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	makeExpiresUnknownBound := func(after, before types.String) types.Object {
		obj, diags := types.ObjectValueFrom(ctx, registry.ExpiresAtFilterAttrTypes, registry.RegistryImagesExpiresAtModel{
			After:  after,
			Before: before,
		})
		if diags.HasError() {
			t.Fatalf("failed to build expires_at object: %v", diags)
		}
		return obj
	}

	cases := []struct {
		name string
		cfg  registry.RegistryImagesDataSourceModel
	}{
		{
			name: "unknown repository",
			cfg: registry.RegistryImagesDataSourceModel{
				ID:             types.StringNull(),
				Repository:     types.StringUnknown(),
				DeletionFilter: types.StringNull(),
				ExpiresAt:      types.ObjectNull(registry.ExpiresAtFilterAttrTypes),
			},
		},
		{
			name: "unknown deletion_filter",
			cfg: registry.RegistryImagesDataSourceModel{
				ID:             types.StringNull(),
				Repository:     types.StringValue("app/web"),
				DeletionFilter: types.StringUnknown(),
				ExpiresAt:      types.ObjectNull(registry.ExpiresAtFilterAttrTypes),
			},
		},
		{
			name: "unknown expires_at object",
			cfg: registry.RegistryImagesDataSourceModel{
				ID:             types.StringNull(),
				Repository:     types.StringValue("app/web"),
				DeletionFilter: types.StringNull(),
				ExpiresAt:      types.ObjectUnknown(registry.ExpiresAtFilterAttrTypes),
			},
		},
		{
			name: "unknown expires_at.after",
			cfg: registry.RegistryImagesDataSourceModel{
				ID:             types.StringNull(),
				Repository:     types.StringValue("app/web"),
				DeletionFilter: types.StringNull(),
				ExpiresAt:      makeExpiresUnknownBound(types.StringUnknown(), types.StringValue("2026-12-01T00:00:00Z")),
			},
		},
		{
			name: "unknown expires_at.before",
			cfg: registry.RegistryImagesDataSourceModel{
				ID:             types.StringNull(),
				Repository:     types.StringValue("app/web"),
				DeletionFilter: types.StringNull(),
				ExpiresAt:      makeExpiresUnknownBound(types.StringValue("2026-11-01T00:00:00Z"), types.StringUnknown()),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			st := tfsdk.State{Schema: schemaResp.Schema}
			if diags := st.Set(ctx, &tc.cfg); diags.HasError() {
				t.Fatalf("failed to set config: %v", diags)
			}

			// With deferral allowed
			req := datasource.ReadRequest{
				Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: st.Raw},
				ClientCapabilities: datasource.ReadClientCapabilities{DeferralAllowed: true},
			}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			d.Read(ctx, req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics with deferral allowed: %v", resp.Diagnostics)
			}
			if resp.Deferred == nil {
				t.Fatal("expected Deferred response when DeferralAllowed=true")
			}
			if called {
				t.Fatal("ListImages must not be called when config has unknown attributes")
			}
		})
	}
}

func TestRegistryImagesDataSource_Read_FilterForwardingAndMetadata(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	afterTS := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	beforeTS := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	deletedTS := time.Date(2026, 11, 10, 5, 6, 7, 0, time.UTC)

	var seenRequests []*registryv1beta.ListImagesRequest
	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		seenRequests = append(seenRequests, proto.Clone(req).(*registryv1beta.ListImagesRequest))
		if len(req.GetPaginationCursor()) == 0 {
			return &registryv1beta.ListImagesResponse{
				Images: []*registryv1beta.Image{
					{
						Repository: "app/web",
						Digest:     "sha256:deleted1",
						CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
						ExpiresAt:  timestamppb.New(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)),
						DeletedAt:  timestamppb.New(deletedTS),
						Labels: []*stdlib.Label{
							{Name: "env", Value: "prod"},
							nil,
							{Name: "team", Value: "core"},
						},
					},
				},
				PaginationCursor: []byte("page-2"),
			}, nil
		}
		return &registryv1beta.ListImagesResponse{
			Images: []*registryv1beta.Image{
				{
					Repository: "app/web",
					Digest:     "sha256:live2",
					CreatedAt:  timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
					ExpiresAt:  timestamppb.New(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)),
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	expObj, diags := types.ObjectValueFrom(ctx, registry.ExpiresAtFilterAttrTypes, registry.RegistryImagesExpiresAtModel{
		After:  types.StringValue("2026-11-01T00:00:00Z"),
		Before: types.StringValue("2026-12-01T00:00:00Z"),
	})
	if diags.HasError() {
		t.Fatalf("failed to construct expires_at object: %v", diags)
	}

	cfg := registry.RegistryImagesDataSourceModel{
		ID:             types.StringNull(),
		Repository:     types.StringValue("app/web"),
		DeletionFilter: types.StringValue("INCLUDE_DELETED"),
		ExpiresAt:      expObj,
	}
	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read diagnostics: %v", resp.Diagnostics)
	}

	if len(seenRequests) != 2 {
		t.Fatalf("expected 2 paginated requests, got %d", len(seenRequests))
	}
	for i, r := range seenRequests {
		if r.GetDeletionFilter() != registryv1beta.ListImagesRequest_INCLUDE_DELETED {
			t.Errorf("page %d deletion_filter = %v, want INCLUDE_DELETED", i, r.GetDeletionFilter())
		}
		if r.GetExpiresAt() == nil || !r.GetExpiresAt().GetAfter().AsTime().Equal(afterTS) || !r.GetExpiresAt().GetBefore().AsTime().Equal(beforeTS) {
			t.Errorf("page %d unexpected expires_at range: %+v", i, r.GetExpiresAt())
		}
	}

	var state registry.RegistryImagesDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("failed to read state: %v", diags)
	}
	if state.DeletionFilter.ValueString() != "INCLUDE_DELETED" {
		t.Errorf("expected preserved deletion_filter INCLUDE_DELETED, got %q", state.DeletionFilter.ValueString())
	}
	if len(state.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(state.Images))
	}

	// First image: deleted_at populated, labels populated (nil label skipped)
	if state.Images[0].DeletedAt.ValueString() != deletedTS.Format(time.RFC3339Nano) {
		t.Errorf("expected deleted_at %q, got %q", deletedTS.Format(time.RFC3339Nano), state.Images[0].DeletedAt.ValueString())
	}
	var lbl0 map[string]string
	if diags := state.Images[0].Labels.ElementsAs(ctx, &lbl0, false); diags.HasError() {
		t.Fatalf("failed to extract labels[0]: %v", diags)
	}
	if lbl0["env"] != "prod" || lbl0["team"] != "core" || len(lbl0) != 2 {
		t.Errorf("unexpected labels[0]: %#v", lbl0)
	}

	// Second image: deleted_at null, labels non-null empty map {}
	if !state.Images[1].DeletedAt.IsNull() {
		t.Errorf("expected null deleted_at for active image, got %v", state.Images[1].DeletedAt)
	}
	if state.Images[1].Labels.IsNull() || len(state.Images[1].Labels.Elements()) != 0 {
		t.Errorf("expected non-null empty map {} for empty labels, got %v", state.Images[1].Labels)
	}
}

func TestRegistryImagesDataSource_Read_StatefulMockServer(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	seed := []*registryv1beta.Image{
		{
			Repository: "app/api",
			Digest:     "sha256:active-in-window",
			CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
			ExpiresAt:  timestamppb.New(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)),
		},
		{
			Repository: "app/api",
			Digest:     "sha256:deleted-in-window",
			CreatedAt:  timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
			ExpiresAt:  timestamppb.New(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)),
			DeletedAt:  timestamppb.New(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
		},
		{
			Repository: "app/api",
			Digest:     "sha256:never-expires",
			CreatedAt:  timestamppb.New(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
		},
		{
			Repository: "app/api",
			Digest:     "sha256:expired-not-deleted",
			CreatedAt:  timestamppb.New(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
			ExpiresAt:  timestamppb.New(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)),
		},
	}
	srv.Registry.EnableStatefulImages(seed, 1)

	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	// Query EXCLUDE_DELETED with no expires_at filter -> keeps active, never-expires, AND expired-not-deleted (3 images across 3 pages)
	cfg := registry.RegistryImagesDataSourceModel{
		ID:             types.StringNull(),
		Repository:     types.StringValue("app/api"),
		DeletionFilter: types.StringValue("EXCLUDE_DELETED"),
		ExpiresAt:      types.ObjectNull(registry.ExpiresAtFilterAttrTypes),
	}
	st := tfsdk.State{Schema: schemaResp.Schema}
	_ = st.Set(ctx, &cfg)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: st.Raw}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error with EXCLUDE_DELETED: %v", resp.Diagnostics)
	}
	var state registry.RegistryImagesDataSourceModel
	_ = resp.State.Get(ctx, &state)
	if len(state.Images) != 3 {
		t.Fatalf("EXCLUDE_DELETED should keep 3 non-deleted images (including expired), got %d", len(state.Images))
	}

	// Query INCLUDE_DELETED with expires_at window -> excludes never-expires and expired-not-deleted, includes active-in-window & deleted-in-window
	expObj, _ := types.ObjectValueFrom(ctx, registry.ExpiresAtFilterAttrTypes, registry.RegistryImagesExpiresAtModel{
		After:  types.StringValue("2026-11-05T00:00:00Z"),
		Before: types.StringValue("2026-11-20T00:00:00Z"),
	})
	cfg2 := registry.RegistryImagesDataSourceModel{
		ID:             types.StringNull(),
		Repository:     types.StringValue("app/api"),
		DeletionFilter: types.StringValue("INCLUDE_DELETED"),
		ExpiresAt:      expObj,
	}
	st2 := tfsdk.State{Schema: schemaResp.Schema}
	_ = st2.Set(ctx, &cfg2)
	resp2 := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: st2.Raw}}, resp2)
	if resp2.Diagnostics.HasError() {
		t.Fatalf("unexpected error with INCLUDE_DELETED + expires_at: %v", resp2.Diagnostics)
	}
	var state2 registry.RegistryImagesDataSourceModel
	_ = resp2.State.Get(ctx, &state2)
	if len(state2.Images) != 2 {
		t.Fatalf("expected 2 images in expires_at window, got %d", len(state2.Images))
	}
}
