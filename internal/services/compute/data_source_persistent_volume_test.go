package compute_test

import (
	"context"
	"strings"
	"testing"
	"time"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/compute"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourcePersistentVolume(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := compute.NewPersistentVolumeDataSource()

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

func TestPersistentVolumeDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := compute.NewPersistentVolumeDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_persistent_volume" {
		t.Fatalf("expected type name 'namespace_persistent_volume', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{"id", "tag", "site", "size_mb", "created_at", "destroyed_at", "attached_to", "released_at", "released_reason"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}
}

func TestPersistentVolumeDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := compute.NewPersistentVolumeDataSource()

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

func TestPersistentVolumeDataSource_Read_ByID(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	releasedTime := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	srv.Storage.DescribePersistentVolumeFunc = func(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error) {
		if req.GetId() != "vol-123" {
			return nil, status.Errorf(codes.NotFound, "volume id %q not found", req.GetId())
		}
		return &computev1beta.DescribePersistentVolumeResponse{
			Volume: &computev1beta.PersistentVolume{
				Id:             "vol-123",
				Tag:            "my-persisted-volume",
				Site:           "eu",
				SizeMb:         20480,
				CreatedAt:      timestamppb.New(createdTime),
				AttachedTo:     "inst-456",
				ReleasedAt:     timestamppb.New(releasedTime),
				ReleasedReason: "instance stopped",
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourcePersistentVolume(t, srv)

	cfg := compute.PersistentVolumeDataSourceModel{
		ID:             types.StringValue("vol-123"),
		Tag:            types.StringNull(),
		Site:           types.StringNull(),
		SizeMb:         types.Int64Null(),
		CreatedAt:      types.StringNull(),
		DestroyedAt:    types.StringNull(),
		AttachedTo:     types.StringNull(),
		ReleasedAt:     types.StringNull(),
		ReleasedReason: types.StringNull(),
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

	var state compute.PersistentVolumeDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "vol-123" {
		t.Errorf("expected ID 'vol-123', got %q", state.ID.ValueString())
	}
	if state.Tag.ValueString() != "my-persisted-volume" {
		t.Errorf("expected Tag 'my-persisted-volume', got %q", state.Tag.ValueString())
	}
	if state.Site.ValueString() != "eu" {
		t.Errorf("expected Site 'eu', got %q", state.Site.ValueString())
	}
	if state.SizeMb.ValueInt64() != 20480 {
		t.Errorf("expected SizeMb 20480, got %d", state.SizeMb.ValueInt64())
	}
	if state.AttachedTo.ValueString() != "inst-456" {
		t.Errorf("expected AttachedTo 'inst-456', got %q", state.AttachedTo.ValueString())
	}
	if state.ReleasedReason.ValueString() != "instance stopped" {
		t.Errorf("expected ReleasedReason 'instance stopped', got %q", state.ReleasedReason.ValueString())
	}
}

func TestPersistentVolumeDataSource_Read_ByTagAndSite(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	srv.Storage.DescribePersistentVolumeFunc = func(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error) {
		if req.GetTag() != "db-volume" || req.GetSite() != "ord" {
			return nil, status.Errorf(codes.NotFound, "volume tag %q with site %q not found", req.GetTag(), req.GetSite())
		}
		return &computev1beta.DescribePersistentVolumeResponse{
			Volume: &computev1beta.PersistentVolume{
				Id:        "vol-999",
				Tag:       "db-volume",
				Site:      "ord",
				SizeMb:    40960,
				CreatedAt: timestamppb.New(createdTime),
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourcePersistentVolume(t, srv)

	cfg := compute.PersistentVolumeDataSourceModel{
		ID:             types.StringNull(),
		Tag:            types.StringValue("db-volume"),
		Site:           types.StringValue("ord"),
		SizeMb:         types.Int64Null(),
		CreatedAt:      types.StringNull(),
		DestroyedAt:    types.StringNull(),
		AttachedTo:     types.StringNull(),
		ReleasedAt:     types.StringNull(),
		ReleasedReason: types.StringNull(),
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

	var state compute.PersistentVolumeDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "vol-999" {
		t.Errorf("expected ID 'vol-999', got %q", state.ID.ValueString())
	}
	if state.Tag.ValueString() != "db-volume" {
		t.Errorf("expected Tag 'db-volume', got %q", state.Tag.ValueString())
	}
	if state.Site.ValueString() != "ord" {
		t.Errorf("expected Site 'ord', got %q", state.Site.ValueString())
	}
}

func TestPersistentVolumeDataSource_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Storage.DescribePersistentVolumeFunc = func(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error) {
		return nil, status.Errorf(codes.NotFound, "volume not found")
	}

	d, schemaResp := setupTestDataSourcePersistentVolume(t, srv)

	cfg := compute.PersistentVolumeDataSourceModel{
		ID:             types.StringValue("non-existent-vol"),
		Tag:            types.StringNull(),
		Site:           types.StringNull(),
		SizeMb:         types.Int64Null(),
		CreatedAt:      types.StringNull(),
		DestroyedAt:    types.StringNull(),
		AttachedTo:     types.StringNull(),
		ReleasedAt:     types.StringNull(),
		ReleasedReason: types.StringNull(),
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
		t.Fatal("expected error diagnostic for non-existent volume")
	}
	errMsg := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(errMsg, "Persistent Volume Not Found") {
		t.Errorf("expected summary containing 'Persistent Volume Not Found', got %q", errMsg)
	}
}

func TestPersistentVolumeDataSource_Read_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	d, schemaResp := setupTestDataSourcePersistentVolume(t, srv)

	// Neither ID nor Tag
	{
		cfg := compute.PersistentVolumeDataSourceModel{
			ID:             types.StringNull(),
			Tag:            types.StringNull(),
			Site:           types.StringNull(),
			SizeMb:         types.Int64Null(),
			CreatedAt:      types.StringNull(),
			DestroyedAt:    types.StringNull(),
			AttachedTo:     types.StringNull(),
			ReleasedAt:     types.StringNull(),
			ReleasedReason: types.StringNull(),
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
			t.Fatal("expected error diagnostic when neither ID nor Tag is set")
		}
	}

	// Both ID and Tag
	{
		cfg := compute.PersistentVolumeDataSourceModel{
			ID:             types.StringValue("vol-1"),
			Tag:            types.StringValue("tag-1"),
			Site:           types.StringNull(),
			SizeMb:         types.Int64Null(),
			CreatedAt:      types.StringNull(),
			DestroyedAt:    types.StringNull(),
			AttachedTo:     types.StringNull(),
			ReleasedAt:     types.StringNull(),
			ReleasedReason: types.StringNull(),
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
			t.Fatal("expected error diagnostic when both ID and Tag are set")
		}
	}
}
