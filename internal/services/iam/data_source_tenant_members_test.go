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

func setupTestDataSourceTenantMembers(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := iam.NewTenantMembersDataSource()

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

func TestTenantMembersDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := iam.NewTenantMembersDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_tenant_members" {
		t.Fatalf("expected type name 'namespace_tenant_members', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"id", "members"}
	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in tenant members data source schema", name)
		}
	}
}

func TestTenantMembersDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := iam.NewTenantMembersDataSource()

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

func TestTenantMembersDataSource_Read_Success_Sorted(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Return members out of order
	srv.Membership.ListTenantMembersFunc = func(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
		return &iamv1beta.ListTenantMembersResponse{
			Members: []*iamv1beta.TenantMember{
				{
					UserId:    "usr_zzz",
					FirstName: "Zack",
					LastName:  "Taylor",
					Email:     "zack@example.com",
					Role:      iamv1beta.TenantMemberRole_TENANT_MEMBER_ROLE_READER,
				},
				{
					UserId:    "usr_aaa",
					FirstName: "Alice",
					LastName:  "Smith",
					Email:     "alice@example.com",
					Role:      iamv1beta.TenantMemberRole_TENANT_MEMBER_ROLE_ADMIN,
				},
				{
					UserId:    "usr_mmm",
					FirstName: "Mary",
					LastName:  "Jones",
					Email:     "mary@example.com",
					Role:      iamv1beta.TenantMemberRole_TENANT_MEMBER_ROLE_OWNER,
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceTenantMembers(t, srv)

	configModel := iam.TenantMembersDataSourceModel{
		ID:      types.StringNull(),
		Members: nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	diags := configState.Set(ctx, &configModel)
	if diags.HasError() {
		t.Fatalf("failed to set config: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var state iam.TenantMembersDataSourceModel
	diags = resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to get state: %v", diags)
	}

	if state.ID.ValueString() != "tenant_members" {
		t.Errorf("expected ID 'tenant_members', got %q", state.ID.ValueString())
	}

	if len(state.Members) != 3 {
		t.Fatalf("expected 3 members, got %d", len(state.Members))
	}

	// Verify deterministic sorting by user_id ascending: usr_aaa, usr_mmm, usr_zzz
	if state.Members[0].UserID.ValueString() != "usr_aaa" || state.Members[0].FirstName.ValueString() != "Alice" {
		t.Errorf("expected member[0] to be usr_aaa (Alice), got %s", state.Members[0].UserID.ValueString())
	}
	if state.Members[1].UserID.ValueString() != "usr_mmm" || state.Members[1].FirstName.ValueString() != "Mary" {
		t.Errorf("expected member[1] to be usr_mmm (Mary), got %s", state.Members[1].UserID.ValueString())
	}
	if state.Members[2].UserID.ValueString() != "usr_zzz" || state.Members[2].FirstName.ValueString() != "Zack" {
		t.Errorf("expected member[2] to be usr_zzz (Zack), got %s", state.Members[2].UserID.ValueString())
	}
}

func TestTenantMembersDataSource_Read_Empty(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Membership.ListTenantMembersFunc = func(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
		return &iamv1beta.ListTenantMembersResponse{
			Members: []*iamv1beta.TenantMember{},
		}, nil
	}

	d, schemaResp := setupTestDataSourceTenantMembers(t, srv)

	configModel := iam.TenantMembersDataSourceModel{
		ID:      types.StringNull(),
		Members: nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var state iam.TenantMembersDataSourceModel
	diags := resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to get state: %v", diags)
	}

	if len(state.Members) != 0 {
		t.Errorf("expected empty members slice, got %d items", len(state.Members))
	}
}

func TestTenantMembersDataSource_Read_Error(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Membership.ListTenantMembersFunc = func(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "unauthorized to list members")
	}

	d, schemaResp := setupTestDataSourceTenantMembers(t, srv)

	configModel := iam.TenantMembersDataSourceModel{
		ID:      types.StringNull(),
		Members: nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostic error on PermissionDenied, got none")
	}
}
