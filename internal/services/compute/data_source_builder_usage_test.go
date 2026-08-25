package compute_test

import (
	"context"
	"strings"
	"testing"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	stdtime "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib/time"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/services/compute"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceBuilderUsage(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := compute.NewBuilderUsageDataSource()

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

func TestBuilderUsageDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := compute.NewBuilderUsageDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_builder_usage" {
		t.Fatalf("expected type name 'namespace_builder_usage', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{"id", "period_start", "period_end", "total", "pending_total", "per_day"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}
	for _, attrName := range []string{"period_start", "period_end"} {
		attr, ok := attrs[attrName].(datasourceschema.StringAttribute)
		if !ok {
			t.Fatalf("expected %q to be a string attribute, got %T", attrName, attrs[attrName])
		}
		if len(attr.Validators) == 0 {
			t.Fatalf("expected %q to validate YYYY-MM-DD input", attrName)
		}
	}
}

func TestBuilderUsageDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := compute.NewBuilderUsageDataSource()

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

func TestBuilderUsageDataSource_Read_SuccessAndChronologicalSort(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedReq *builderv1beta.GetBuilderUsageRequest
	srv.Builder.GetBuilderUsageFunc = func(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error) {
		capturedReq = req
		return &builderv1beta.GetBuilderUsageResponse{
			Total: &builderv1beta.GetBuilderUsageResponse_Usage{
				BuildCount:       150,
				BuildWallMinutes: 300,
				BuildUnitMinutes: 1200,
			},
			PendingTotal: &builderv1beta.GetBuilderUsageResponse_Usage{
				BuildCount:       5,
				BuildWallMinutes: 10,
				BuildUnitMinutes: 40,
			},
			// Return out-of-order days to verify deterministic chronological sorting
			PerDay: []*builderv1beta.GetBuilderUsageResponse_PerDay{
				{
					Date: &stdtime.Date{Year: 2026, Month: 8, Day: 20},
					Usage: &builderv1beta.GetBuilderUsageResponse_Usage{
						BuildCount:       80,
						BuildWallMinutes: 160,
						BuildUnitMinutes: 640,
					},
				},
				{
					Date: &stdtime.Date{Year: 2026, Month: 8, Day: 10},
					Usage: &builderv1beta.GetBuilderUsageResponse_Usage{
						BuildCount:       70,
						BuildWallMinutes: 140,
						BuildUnitMinutes: 560,
					},
					PendingUsage: &builderv1beta.GetBuilderUsageResponse_Usage{
						BuildCount:       5,
						BuildWallMinutes: 10,
						BuildUnitMinutes: 40,
					},
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceBuilderUsage(t, srv)

	cfg := compute.BuilderUsageDataSourceModel{
		ID:           types.StringNull(),
		PeriodStart:  types.StringValue("2026-08-01"),
		PeriodEnd:    types.StringValue("2026-08-21"),
		Total:        nil,
		PendingTotal: nil,
		PerDay:       nil,
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

	if capturedReq == nil {
		t.Fatal("expected GetBuilderUsageRequest to be captured")
	}
	if capturedReq.GetPeriodStart().GetYear() != 2026 || capturedReq.GetPeriodStart().GetMonth() != 8 || capturedReq.GetPeriodStart().GetDay() != 1 {
		t.Errorf("unexpected period_start proto: %v", capturedReq.GetPeriodStart())
	}
	if capturedReq.GetPeriodEnd().GetYear() != 2026 || capturedReq.GetPeriodEnd().GetMonth() != 8 || capturedReq.GetPeriodEnd().GetDay() != 21 {
		t.Errorf("unexpected period_end proto: %v", capturedReq.GetPeriodEnd())
	}

	var state compute.BuilderUsageDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "usage:2026-08-01:2026-08-21" {
		t.Errorf("expected ID 'usage:2026-08-01:2026-08-21', got %q", state.ID.ValueString())
	}
	if state.Total == nil || state.Total.BuildCount.ValueInt64() != 150 {
		t.Errorf("unexpected Total: %+v", state.Total)
	}
	if state.PendingTotal == nil || state.PendingTotal.BuildCount.ValueInt64() != 5 {
		t.Errorf("unexpected PendingTotal: %+v", state.PendingTotal)
	}
	if len(state.PerDay) != 2 {
		t.Fatalf("expected 2 per_day entries, got %d", len(state.PerDay))
	}

	// Verify chronological order: 2026-08-10 before 2026-08-20
	if state.PerDay[0].Date.ValueString() != "2026-08-10" {
		t.Errorf("expected first per_day entry date '2026-08-10', got %q", state.PerDay[0].Date.ValueString())
	}
	if state.PerDay[1].Date.ValueString() != "2026-08-20" {
		t.Errorf("expected second per_day entry date '2026-08-20', got %q", state.PerDay[1].Date.ValueString())
	}
}

func TestBuilderUsageDataSource_Read_OmittedPeriodEnd(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedReq *builderv1beta.GetBuilderUsageRequest
	srv.Builder.GetBuilderUsageFunc = func(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error) {
		capturedReq = req
		return &builderv1beta.GetBuilderUsageResponse{}, nil
	}

	d, schemaResp := setupTestDataSourceBuilderUsage(t, srv)

	cfg := compute.BuilderUsageDataSourceModel{
		ID:           types.StringNull(),
		PeriodStart:  types.StringValue("2026-08-01"),
		PeriodEnd:    types.StringNull(),
		Total:        nil,
		PendingTotal: nil,
		PerDay:       nil,
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

	if capturedReq.GetPeriodEnd() != nil {
		t.Errorf("expected PeriodEnd to be nil in request when omitted, got %v", capturedReq.GetPeriodEnd())
	}
}

func TestBuilderUsageDataSource_Read_DateValidations(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	d, schemaResp := setupTestDataSourceBuilderUsage(t, srv)

	// 1. Invalid start date format
	{
		cfg := compute.BuilderUsageDataSourceModel{
			ID:           types.StringNull(),
			PeriodStart:  types.StringValue("invalid-date"),
			PeriodEnd:    types.StringNull(),
			Total:        nil,
			PendingTotal: nil,
			PerDay:       nil,
		}
		configState := tfsdk.State{Schema: schemaResp.Schema}
		if diags := configState.Set(ctx, &cfg); diags.HasError() {
			t.Fatalf("failed to set config state: %v", diags)
		}
		req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
		resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		d.Read(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for invalid start date format")
		}
	}

	// 2. Invalid end date format
	{
		cfg := compute.BuilderUsageDataSourceModel{
			ID:           types.StringNull(),
			PeriodStart:  types.StringValue("2026-08-01"),
			PeriodEnd:    types.StringValue("2026/08/21"),
			Total:        nil,
			PendingTotal: nil,
			PerDay:       nil,
		}
		configState := tfsdk.State{Schema: schemaResp.Schema}
		if diags := configState.Set(ctx, &cfg); diags.HasError() {
			t.Fatalf("failed to set config state: %v", diags)
		}
		req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
		resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		d.Read(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for invalid end date format")
		}
	}

	// 3. period_end < period_start
	{
		cfg := compute.BuilderUsageDataSourceModel{
			ID:           types.StringNull(),
			PeriodStart:  types.StringValue("2026-08-20"),
			PeriodEnd:    types.StringValue("2026-08-10"),
			Total:        nil,
			PendingTotal: nil,
			PerDay:       nil,
		}
		configState := tfsdk.State{Schema: schemaResp.Schema}
		if diags := configState.Set(ctx, &cfg); diags.HasError() {
			t.Fatalf("failed to set config state: %v", diags)
		}
		req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
		resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		d.Read(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic when period_end < period_start")
		}
		summary := resp.Diagnostics.Errors()[0].Summary()
		if !strings.Contains(summary, "Invalid Period Range") {
			t.Errorf("expected summary containing 'Invalid Period Range', got %q", summary)
		}
	}
}
