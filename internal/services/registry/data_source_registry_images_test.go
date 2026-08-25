package registry_test

import (
	"context"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	for _, attrName := range []string{"id", "repository", "images"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
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
		ID:         types.StringNull(),
		Repository: types.StringValue("app-repo"),
		Images:     nil,
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
		ID:         types.StringNull(),
		Repository: types.StringValue("loop-repo"),
		Images:     nil,
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
		ID:         types.StringNull(),
		Repository: types.StringValue("empty-repo"),
		Images:     nil,
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
