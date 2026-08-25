package iam_test

import (
	"context"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceTenantGroup(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := iam.NewTenantGroupDataSource()

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

func TestTenantGroupDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := iam.NewTenantGroupDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_tenant_group" {
		t.Fatalf("expected type name 'namespace_tenant_group', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"id", "name", "policies", "enrolled_tenant_ids", "versions"}
	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in data source schema", name)
		}
	}
}

func TestTenantGroupDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := iam.NewTenantGroupDataSource()

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

func TestTenantGroupDataSource_Read_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: "engineering",
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "p1", Value: "v1"},
			},
			EnrolledTenantIds: []string{"tenant_a", "tenant_b"},
			Versions: []*iamv1beta.TenantGroupPolicy{
				{Policy: "p1_v1", Value: "v1_0"},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceTenantGroup(t, srv)

	configModel := iam.TenantGroupDataSourceModel{
		ID:                types.StringNull(),
		Name:              types.StringValue("engineering"),
		Policies:          nil,
		EnrolledTenantIDs: types.ListNull(types.StringType),
		Versions:          nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	diags := configState.Set(ctx, &configModel)
	if diags.HasError() {
		t.Fatalf("failed to set config: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var state iam.TenantGroupDataSourceModel
	diags = resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to get state: %v", diags)
	}

	if state.ID.ValueString() != "engineering" {
		t.Errorf("expected ID 'engineering', got %q", state.ID.ValueString())
	}
	if state.Name.ValueString() != "engineering" {
		t.Errorf("expected Name 'engineering', got %q", state.Name.ValueString())
	}
	if len(state.Policies) != 1 || state.Policies[0].Policy.ValueString() != "p1" {
		t.Errorf("unexpected policies: %+v", state.Policies)
	}
	if len(state.Versions) != 1 || state.Versions[0].Policy.ValueString() != "p1_v1" {
		t.Errorf("unexpected versions: %+v", state.Versions)
	}
}

func TestTenantGroupDataSource_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return nil, status.Error(codes.NotFound, "group not found")
	}

	d, schemaResp := setupTestDataSourceTenantGroup(t, srv)

	configModel := iam.TenantGroupDataSourceModel{
		ID:                types.StringNull(),
		Name:              types.StringValue("nonexistent"),
		Policies:          nil,
		EnrolledTenantIDs: types.ListNull(types.StringType),
		Versions:          nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error on read when group not found, got none")
	}
}
