package registry_test

import (
	"context"
	"strings"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceRegistryImage(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := registry.NewRegistryImageDataSource()

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

func TestRegistryImageDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := registry.NewRegistryImageDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_registry_image" {
		t.Fatalf("expected type name 'namespace_registry_image', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{"id", "repository", "reference", "digest", "created_at", "expires_at", "size_bytes", "tags"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}
}

func TestRegistryImageDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := registry.NewRegistryImageDataSource()

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

func TestRegistryImageDataSource_Read_ByTag(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	expiresTime := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)

	srv.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		if req.GetRepository() != "my-app" || req.GetReference() != "v1.2.0" {
			return nil, status.Errorf(codes.NotFound, "image not found")
		}
		return &registryv1beta.GetImageResponse{
			Image: &registryv1beta.Image{
				Repository: "my-app",
				Digest:     "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				CreatedAt:  timestamppb.New(createdTime),
				ExpiresAt:  timestamppb.New(expiresTime),
				Sizes: &registryv1beta.Image_Sizes{
					Total: 104857600,
				},
				// Unsorted tags to verify alphabetical sorting
				Tags: []string{"v1.2.0", "latest", "prod"},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRegistryImage(t, srv)

	cfg := registry.RegistryImageDataSourceModel{
		ID:         types.StringNull(),
		Repository: types.StringValue("my-app"),
		Reference:  types.StringValue("v1.2.0"),
		Digest:     types.StringNull(),
		CreatedAt:  types.StringNull(),
		ExpiresAt:  types.StringNull(),
		SizeBytes:  types.Int64Null(),
		Tags:       nil,
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

	var state registry.RegistryImageDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	expectedID := "my-app@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if state.ID.ValueString() != expectedID {
		t.Errorf("expected ID %q, got %q", expectedID, state.ID.ValueString())
	}
	if state.Repository.ValueString() != "my-app" {
		t.Errorf("expected Repository 'my-app', got %q", state.Repository.ValueString())
	}
	if state.Reference.ValueString() != "v1.2.0" {
		t.Errorf("expected Reference 'v1.2.0', got %q", state.Reference.ValueString())
	}
	if state.Digest.ValueString() != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("expected Digest match, got %q", state.Digest.ValueString())
	}
	if state.SizeBytes.ValueInt64() != 104857600 {
		t.Errorf("expected SizeBytes 104857600, got %d", state.SizeBytes.ValueInt64())
	}
	if len(state.Tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(state.Tags))
	}
	// Verify alphabetical order: "latest", "prod", "v1.2.0"
	if state.Tags[0].ValueString() != "latest" || state.Tags[1].ValueString() != "prod" || state.Tags[2].ValueString() != "v1.2.0" {
		t.Errorf("expected sorted tags ['latest', 'prod', 'v1.2.0'], got [%s, %s, %s]", state.Tags[0].ValueString(), state.Tags[1].ValueString(), state.Tags[2].ValueString())
	}
}

func TestRegistryImageDataSource_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		return nil, status.Errorf(codes.NotFound, "image reference not found")
	}

	d, schemaResp := setupTestDataSourceRegistryImage(t, srv)

	cfg := registry.RegistryImageDataSourceModel{
		ID:         types.StringNull(),
		Repository: types.StringValue("my-app"),
		Reference:  types.StringValue("non-existent-tag"),
		Digest:     types.StringNull(),
		CreatedAt:  types.StringNull(),
		ExpiresAt:  types.StringNull(),
		SizeBytes:  types.Int64Null(),
		Tags:       nil,
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
		t.Fatal("expected error diagnostic for non-existent image")
	}

	errMsg := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(errMsg, "Registry Image Not Found") {
		t.Errorf("expected summary containing 'Registry Image Not Found', got %q", errMsg)
	}
}
