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

func setupTestDataSourceCacheVolumeSummary(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := compute.NewCacheVolumeSummaryDataSource()

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

func TestCacheVolumeSummaryDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := compute.NewCacheVolumeSummaryDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_cache_volume_summary" {
		t.Fatalf("expected type name 'namespace_cache_volume_summary', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{"id", "tag", "size_mb", "created_at", "generations"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}
}

func TestCacheVolumeSummaryDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := compute.NewCacheVolumeSummaryDataSource()

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

func TestCacheVolumeSummaryDataSource_Read_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	attachedTime := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)
	detachedTime := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	srv.Storage.GetVolumeTagSummaryFunc = func(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
		if req.GetTag() != "build-cache" {
			return nil, status.Errorf(codes.NotFound, "tag %q not found", req.GetTag())
		}
		return &computev1beta.GetVolumeTagSummaryResponse{
			Summary: &computev1beta.TagSummary{
				Tag:       "build-cache",
				SizeMb:    10240,
				CreatedAt: timestamppb.New(createdTime),
				Generations: []*computev1beta.CacheVolume{
					{
						Id:  "gen-1",
						Tag: "build-cache",
						Metadata: &computev1beta.VolumeMetadata{
							CreatedAt:        timestamppb.New(createdTime),
							SizeMb:           10240,
							State:            computev1beta.VolumeMetadata_STATE_IN_USE,
							GenerationReason: computev1beta.VolumeMetadata_RESERVATION_NOT_MET,
						},
						Attachment: &computev1beta.VolumeAttachment{
							AttachedTo:         "inst-123",
							AttachedAt:         timestamppb.New(attachedTime),
							DetachedAt:         timestamppb.New(detachedTime),
							UsedMbWhenAttached: 2048,
							UsedMbWhenDetached: 4096,
						},
					},
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceCacheVolumeSummary(t, srv)

	cfg := compute.CacheVolumeSummaryDataSourceModel{
		ID:          types.StringNull(),
		Tag:         types.StringValue("build-cache"),
		SizeMb:      types.Int64Null(),
		CreatedAt:   types.StringNull(),
		Generations: nil,
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

	var state compute.CacheVolumeSummaryDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "build-cache" {
		t.Errorf("expected ID 'build-cache', got %q", state.ID.ValueString())
	}
	if state.Tag.ValueString() != "build-cache" {
		t.Errorf("expected Tag 'build-cache', got %q", state.Tag.ValueString())
	}
	if state.SizeMb.ValueInt64() != 10240 {
		t.Errorf("expected SizeMb 10240, got %d", state.SizeMb.ValueInt64())
	}
	if len(state.Generations) != 1 {
		t.Fatalf("expected 1 generation, got %d", len(state.Generations))
	}
	gen := state.Generations[0]
	if gen.ID.ValueString() != "gen-1" {
		t.Errorf("expected gen ID 'gen-1', got %q", gen.ID.ValueString())
	}
	if gen.AttachedTo.ValueString() != "inst-123" {
		t.Errorf("expected AttachedTo 'inst-123', got %q", gen.AttachedTo.ValueString())
	}
	if gen.UsedMbWhenAttached.ValueInt64() != 2048 {
		t.Errorf("expected UsedMbWhenAttached 2048, got %d", gen.UsedMbWhenAttached.ValueInt64())
	}
	if gen.UsedMbWhenDetached.ValueInt64() != 4096 {
		t.Errorf("expected UsedMbWhenDetached 4096, got %d", gen.UsedMbWhenDetached.ValueInt64())
	}
	if gen.State.ValueString() != "STATE_IN_USE" {
		t.Errorf("expected State 'STATE_IN_USE', got %q", gen.State.ValueString())
	}
}

func TestCacheVolumeSummaryDataSource_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Storage.GetVolumeTagSummaryFunc = func(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
		return nil, status.Errorf(codes.NotFound, "tag not found")
	}

	d, schemaResp := setupTestDataSourceCacheVolumeSummary(t, srv)

	cfg := compute.CacheVolumeSummaryDataSourceModel{
		ID:          types.StringNull(),
		Tag:         types.StringValue("non-existent"),
		SizeMb:      types.Int64Null(),
		CreatedAt:   types.StringNull(),
		Generations: nil,
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
		t.Fatal("expected error diagnostic for non-existent tag")
	}

	errMsg := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(errMsg, "Cache Volume Tag Summary Not Found") {
		t.Errorf("expected summary containing 'Cache Volume Tag Summary Not Found', got %q", errMsg)
	}
}

func TestCacheVolumeSummaryDataSource_Read_NilSummary(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Storage.GetVolumeTagSummaryFunc = func(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
		return &computev1beta.GetVolumeTagSummaryResponse{Summary: nil}, nil
	}

	d, schemaResp := setupTestDataSourceCacheVolumeSummary(t, srv)

	cfg := compute.CacheVolumeSummaryDataSourceModel{
		ID:          types.StringNull(),
		Tag:         types.StringValue("empty-tag"),
		SizeMb:      types.Int64Null(),
		CreatedAt:   types.StringNull(),
		Generations: nil,
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
		t.Fatal("expected error diagnostic for nil summary response")
	}
	errMsg := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(errMsg, "Cache Volume Tag Summary Not Found") {
		t.Errorf("expected summary containing 'Cache Volume Tag Summary Not Found', got %q", errMsg)
	}
}

func TestCacheVolumeSummaryDataSource_Read_GenerationsDeterministicSort(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	t1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

	srv.Storage.GetVolumeTagSummaryFunc = func(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
		return &computev1beta.GetVolumeTagSummaryResponse{
			Summary: &computev1beta.TagSummary{
				Tag:    "sort-test",
				SizeMb: 100,
				Generations: []*computev1beta.CacheVolume{
					{
						Id:       "gen-c",
						Tag:      "sort-test",
						Metadata: &computev1beta.VolumeMetadata{CreatedAt: timestamppb.New(t2)},
					},
					{
						Id:       "gen-b",
						Tag:      "sort-test",
						Metadata: &computev1beta.VolumeMetadata{CreatedAt: timestamppb.New(t1)},
					},
					{
						Id:       "gen-a",
						Tag:      "sort-test",
						Metadata: &computev1beta.VolumeMetadata{CreatedAt: timestamppb.New(t1)},
					},
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceCacheVolumeSummary(t, srv)

	cfg := compute.CacheVolumeSummaryDataSourceModel{
		ID:          types.StringNull(),
		Tag:         types.StringValue("sort-test"),
		SizeMb:      types.Int64Null(),
		CreatedAt:   types.StringNull(),
		Generations: nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &cfg)

	req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read diagnostics: %v", resp.Diagnostics)
	}

	var state compute.CacheVolumeSummaryDataSourceModel
	_ = resp.State.Get(ctx, &state)

	if len(state.Generations) != 3 {
		t.Fatalf("expected 3 generations, got %d", len(state.Generations))
	}

	// Expected order:
	// 0: gen-a (t1)
	// 1: gen-b (t1)
	// 2: gen-c (t2)
	if state.Generations[0].ID.ValueString() != "gen-a" {
		t.Errorf("expected index 0 to be gen-a, got %q", state.Generations[0].ID.ValueString())
	}
	if state.Generations[1].ID.ValueString() != "gen-b" {
		t.Errorf("expected index 1 to be gen-b, got %q", state.Generations[1].ID.ValueString())
	}
	if state.Generations[2].ID.ValueString() != "gen-c" {
		t.Errorf("expected index 2 to be gen-c, got %q", state.Generations[2].ID.ValueString())
	}
}
