package compute_test

import (
	"context"
	"testing"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/services/compute"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceNetworkConfiguration(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := compute.NewNetworkConfigurationDataSource()

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

func TestNetworkConfigurationDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := compute.NewNetworkConfigurationDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_network_configuration" {
		t.Fatalf("expected type name 'namespace_network_configuration', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{"id", "egress_cidrs"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}
}

func TestNetworkConfigurationDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := compute.NewNetworkConfigurationDataSource()

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

func TestNetworkConfigurationDataSource_Read_SuccessAndDeterministicSorting(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Compute.GetNetworkConfigurationFunc = func(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error) {
		return &computev1beta.GetNetworkConfigurationResponse{
			Egress: &computev1beta.GetNetworkConfigurationResponse_Egress{
				// Unsorted order to verify deterministic sorting
				EgressCidrs: []*computev1beta.GetNetworkConfigurationResponse_CIDR{
					{
						Cidr:    "198.51.100.0/24",
						Kind:    computev1beta.GetNetworkConfigurationResponse_CIDR_DEDICATED,
						Segment: "seg-b",
					},
					{
						Cidr:    "192.0.2.0/24",
						Kind:    computev1beta.GetNetworkConfigurationResponse_CIDR_NAMESPACE_MANAGED,
						Segment: "",
					},
					{
						Cidr:    "198.51.100.0/24",
						Kind:    computev1beta.GetNetworkConfigurationResponse_CIDR_DEDICATED,
						Segment: "seg-a",
					},
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceNetworkConfiguration(t, srv)

	cfg := compute.NetworkConfigurationDataSourceModel{
		ID:          types.StringNull(),
		EgressCidrs: nil,
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

	var state compute.NetworkConfigurationDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "network" {
		t.Errorf("expected ID 'network', got %q", state.ID.ValueString())
	}
	if len(state.EgressCidrs) != 3 {
		t.Fatalf("expected 3 egress CIDRs, got %d", len(state.EgressCidrs))
	}

	// Expected sorted order:
	// 1: 192.0.2.0/24 (segment: null)
	// 2: 198.51.100.0/24 (segment: seg-a)
	// 3: 198.51.100.0/24 (segment: seg-b)
	if state.EgressCidrs[0].Cidr.ValueString() != "192.0.2.0/24" {
		t.Errorf("expected first CIDR '192.0.2.0/24', got %q", state.EgressCidrs[0].Cidr.ValueString())
	}
	if state.EgressCidrs[1].Cidr.ValueString() != "198.51.100.0/24" || state.EgressCidrs[1].Segment.ValueString() != "seg-a" {
		t.Errorf("expected second CIDR '198.51.100.0/24' with seg-a, got %q (seg %q)", state.EgressCidrs[1].Cidr.ValueString(), state.EgressCidrs[1].Segment.ValueString())
	}
	if state.EgressCidrs[2].Cidr.ValueString() != "198.51.100.0/24" || state.EgressCidrs[2].Segment.ValueString() != "seg-b" {
		t.Errorf("expected third CIDR '198.51.100.0/24' with seg-b, got %q (seg %q)", state.EgressCidrs[2].Cidr.ValueString(), state.EgressCidrs[2].Segment.ValueString())
	}
}

func TestNetworkConfigurationDataSource_Read_Empty(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Compute.GetNetworkConfigurationFunc = func(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error) {
		return &computev1beta.GetNetworkConfigurationResponse{Egress: nil}, nil
	}

	d, schemaResp := setupTestDataSourceNetworkConfiguration(t, srv)

	cfg := compute.NetworkConfigurationDataSourceModel{
		ID:          types.StringNull(),
		EgressCidrs: nil,
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

	var state compute.NetworkConfigurationDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.EgressCidrs == nil || len(state.EgressCidrs) != 0 {
		t.Errorf("expected empty non-nil slice for EgressCidrs, got %v", state.EgressCidrs)
	}
}

func TestNetworkConfigurationDataSource_Read_Error(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Compute.GetNetworkConfigurationFunc = func(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error) {
		return nil, status.Errorf(codes.Internal, "network service error")
	}

	d, schemaResp := setupTestDataSourceNetworkConfiguration(t, srv)

	cfg := compute.NetworkConfigurationDataSourceModel{
		ID:          types.StringNull(),
		EgressCidrs: nil,
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
		t.Fatal("expected error diagnostic for internal error")
	}
}
