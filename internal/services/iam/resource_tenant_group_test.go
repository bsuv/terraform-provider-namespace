package iam_test

import (
	"context"
	"strings"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestTenantGroupResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := iam.NewTenantGroupResource()

	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	var confResp resource.ConfigureResponse
	if confR, ok := r.(resource.ResourceWithConfigure); ok {
		confR.Configure(ctx, resource.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return r, schemaResp
}

func makePoliciesList(t *testing.T, pairs ...[2]string) types.List {
	t.Helper()
	if len(pairs) == 0 {
		return types.ListNull(iam.TenantGroupPolicyObjectType)
	}
	var elements []attr.Value
	for _, p := range pairs {
		obj, err := types.ObjectValue(iam.TenantGroupPolicyAttrTypes, map[string]attr.Value{
			"policy": types.StringValue(p[0]),
			"value":  types.StringValue(p[1]),
		})
		if err.HasError() {
			t.Fatalf("failed to create policy object: %v", err)
		}
		elements = append(elements, obj)
	}
	res, diags := types.ListValue(iam.TenantGroupPolicyObjectType, elements)
	if diags.HasError() {
		t.Fatalf("failed to create policies list: %v", diags)
	}
	return res
}

func decodePoliciesList(t *testing.T, ctx context.Context, list types.List) []iam.TenantGroupPolicyModel {
	t.Helper()
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	var res []iam.TenantGroupPolicyModel
	diags := list.ElementsAs(ctx, &res, false)
	if diags.HasError() {
		t.Fatalf("failed to decode policies list: %v", diags)
	}
	return res
}

func TestTenantGroupResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := iam.NewTenantGroupResource()

	var metaResp resource.MetadataResponse
	r.Metadata(ctx, resource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_tenant_group" {
		t.Fatalf("expected type name 'namespace_tenant_group', got %q", metaResp.TypeName)
	}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"id", "name", "policies", "enrolled_tenant_ids"}
	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in tenant group schema", name)
		}
	}
}

func TestTenantGroupResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := iam.NewTenantGroupResource()

	var confResp resource.ConfigureResponse
	if confR, ok := r.(resource.ResourceWithConfigure); ok {
		confR.Configure(ctx, resource.ConfigureRequest{
			ProviderData: "invalid-type",
		}, &confResp)
		if !confResp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for invalid configure provider data")
		}
	}
}

func TestTenantGroupResource_Create_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var describeCalls int
	var ensuredRequest *iamv1beta.EnsureTenantGroupRequest

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		describeCalls++
		if describeCalls == 1 {
			// Preflight check: not found
			return nil, status.Error(codes.NotFound, "tenant group not found")
		}
		// Post-ensure describe: return group
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "namespace.cloud.compute.v1beta.UsagePolicy", Value: `{"max_instances": 10}`},
			},
			EnrolledTenantIds: []string{"tenant_123"},
			Versions: []*iamv1beta.TenantGroupPolicy{
				{Policy: "namespace.cloud.compute.v1beta.UsagePolicy", Value: `{"max_instances": 10}`},
			},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		ensuredRequest = req
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringUnknown(),
		Name:              types.StringValue("engineering"),
		Policies:          makePoliciesList(t, [2]string{"namespace.cloud.compute.v1beta.UsagePolicy", `{"max_instances": 10}`}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	diags := planState.Set(ctx, &planModel)
	if diags.HasError() {
		t.Fatalf("failed to set plan state: %v", diags)
	}

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
	}
	resp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected create errors: %v", resp.Diagnostics)
	}

	if ensuredRequest == nil || ensuredRequest.Name != "engineering" {
		t.Fatalf("expected EnsureTenantGroup called with name 'engineering', got %+v", ensuredRequest)
	}

	var state iam.TenantGroupResourceModel
	diags = resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to get response state: %v", diags)
	}

	if state.ID.ValueString() != "engineering" {
		t.Errorf("expected state ID 'engineering', got %q", state.ID.ValueString())
	}
	if state.Name.ValueString() != "engineering" {
		t.Errorf("expected state Name 'engineering', got %q", state.Name.ValueString())
	}
	policies := decodePoliciesList(t, ctx, state.Policies)
	if len(policies) != 1 || policies[0].Policy.ValueString() != "namespace.cloud.compute.v1beta.UsagePolicy" {
		t.Errorf("unexpected policies in state: %+v", policies)
	}
}

func TestTenantGroupResource_Create_OmittedUnknownPolicies_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var describeCalls int
	var ensuredRequest *iamv1beta.EnsureTenantGroupRequest

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		describeCalls++
		if describeCalls == 1 {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          nil,
			EnrolledTenantIds: []string{},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		ensuredRequest = req
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// In real Terraform plan when `policies` is omitted from HCL, it is Unknown in Plan
	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringUnknown(),
		Name:              types.StringValue("ops-group"),
		Policies:          types.ListUnknown(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListUnknown(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	diags := planState.Set(ctx, &planModel)
	if diags.HasError() {
		t.Fatalf("failed to set plan state: %v", diags)
	}

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
	}
	resp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error when creating tenant group with unknown policies (Value Conversion Error regression): %v", resp.Diagnostics)
	}

	if ensuredRequest == nil || ensuredRequest.Name != "ops-group" {
		t.Fatalf("expected EnsureTenantGroup with name 'ops-group', got %+v", ensuredRequest)
	}
	if len(ensuredRequest.Policies) != 0 {
		t.Errorf("expected 0 policies sent in Ensure request, got %d", len(ensuredRequest.Policies))
	}

	var state iam.TenantGroupResourceModel
	diags = resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to read state: %v", diags)
	}
	if state.Name.ValueString() != "ops-group" {
		t.Errorf("expected state name 'ops-group', got %q", state.Name.ValueString())
	}
}

func TestTenantGroupResource_Create_Collision_AlreadyExists(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Preflight check finds an existing group
	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: "engineering",
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "some.policy", Value: "{}"},
			},
		}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringUnknown(),
		Name:              types.StringValue("engineering"),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	diags := planState.Set(ctx, &planModel)
	if diags.HasError() {
		t.Fatalf("failed to set plan state: %v", diags)
	}

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
	}
	resp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected collision error diagnostic on create, got none")
	}

	foundImportHint := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Already Exists") && strings.Contains(d.Detail(), "terraform import") {
			foundImportHint = true
			break
		}
	}
	if !foundImportHint {
		t.Errorf("expected AlreadyExists diagnostic with terraform import hint, got: %v", resp.Diagnostics)
	}
}

func TestTenantGroupResource_Read_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "p1", Value: "v1"},
			},
			EnrolledTenantIds: []string{"t1", "t2"},
		}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("engineering"),
		Name:              types.StringValue("engineering"),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	initialState := tfsdk.State{Schema: schemaResp.Schema}
	diags := initialState.Set(ctx, &stateModel)
	if diags.HasError() {
		t.Fatalf("failed to set initial state: %v", diags)
	}

	req := resource.ReadRequest{
		State: initialState,
	}
	resp := &resource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var updatedState iam.TenantGroupResourceModel
	diags = resp.State.Get(ctx, &updatedState)
	if diags.HasError() {
		t.Fatalf("failed to get state: %v", diags)
	}

	policies := decodePoliciesList(t, ctx, updatedState.Policies)
	if len(policies) != 1 || policies[0].Policy.ValueString() != "p1" {
		t.Errorf("unexpected policies: %+v", policies)
	}
}

func TestTenantGroupResource_Read_NotFound_RemovesState(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return nil, status.Error(codes.NotFound, "group does not exist")
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("engineering"),
		Name:              types.StringValue("engineering"),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	initialState := tfsdk.State{Schema: schemaResp.Schema}
	_ = initialState.Set(ctx, &stateModel)

	req := resource.ReadRequest{State: initialState}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Fatal("expected state to be removed (null) after NotFound on read")
	}
}

func TestTenantGroupResource_Update_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var updatedReq *iamv1beta.EnsureTenantGroupRequest
	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		updatedReq = req
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "new.policy", Value: "new.val"},
			},
			EnrolledTenantIds: []string{"t1"},
		}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("engineering"),
		Name:              types.StringValue("engineering"),
		Policies:          makePoliciesList(t, [2]string{"new.policy", "new.val"}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
		State: planState,
	}
	resp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected update errors: %v", resp.Diagnostics)
	}

	if updatedReq == nil || len(updatedReq.Policies) != 1 {
		t.Fatalf("expected update request with 1 policy, got: %+v", updatedReq)
	}
}

func TestTenantGroupResource_Delete_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var deletedName string
	srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		deletedName = req.Name
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("engineering"),
		Name:              types.StringValue("engineering"),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete errors: %v", resp.Diagnostics)
	}

	if deletedName != "engineering" {
		t.Errorf("expected DeleteTenantGroup called with 'engineering', got %q", deletedName)
	}
}

func TestTenantGroupResource_Delete_NotFound_Succeeds(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return nil, status.Error(codes.NotFound, "already deleted")
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("engineering"),
		Name:              types.StringValue("engineering"),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete errors when not found: %v", resp.Diagnostics)
	}
}

func TestTenantGroupResource_Delete_ActiveTenants_FailedPrecondition(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return nil, status.Error(codes.FailedPrecondition, "cannot delete tenant group with enrolled tenants")
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("engineering"),
		Name:              types.StringValue("engineering"),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostic error on FailedPrecondition delete, got none")
	}
	if got := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(got, "Unenroll all tenants") {
		t.Fatalf("expected actionable unenrollment guidance, got %q", got)
	}
}

func TestTenantGroupResource_ImportState(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "p1", Value: "v1"},
			},
			EnrolledTenantIds: []string{"t1"},
		}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithImportState")
	}

	req := resource.ImportStateRequest{ID: "engineering"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	importR.ImportState(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected import errors: %v", resp.Diagnostics)
	}

	var state iam.TenantGroupResourceModel
	diags := resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to read state after import: %v", diags)
	}

	if state.Name.ValueString() != "engineering" {
		t.Errorf("expected imported name 'engineering', got %q", state.Name.ValueString())
	}
	if state.ID.ValueString() != "engineering" {
		t.Errorf("expected imported ID 'engineering', got %q", state.ID.ValueString())
	}
	policies := decodePoliciesList(t, ctx, state.Policies)
	if len(policies) != 1 || policies[0].Policy.ValueString() != "p1" {
		t.Errorf("unexpected policies after import: %+v", policies)
	}
}

func TestTenantGroupResource_ImportState_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return nil, status.Error(codes.NotFound, "group not found")
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithImportState")
	}

	req := resource.ImportStateRequest{ID: "nonexistent"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	importR.ImportState(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostic error importing non-existent group, got none")
	}
}

func TestTenantGroupResource_ImportState_EmptyID(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithImportState")
	}

	req := resource.ImportStateRequest{ID: ""}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	importR.ImportState(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostic error importing with empty ID, got none")
	}
}

func TestTenantGroupResource_PolicyOrderPreservation(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var describeCalls int
	var capturedReq *iamv1beta.EnsureTenantGroupRequest

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		describeCalls++
		if describeCalls == 1 {
			return nil, status.Error(codes.NotFound, "not found")
		}
		// Echo policies in exact order received
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:     req.Name,
			Policies: capturedReq.Policies,
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		capturedReq = req
		return &iamv1beta.TenantGroupResponse{}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	planModel := iam.TenantGroupResourceModel{
		ID:   types.StringNull(),
		Name: types.StringValue("order-test-group"),
		Policies: makePoliciesList(t,
			[2]string{"policy-z", "val-z"},
			[2]string{"policy-a", "val-a"},
			[2]string{"policy-m", "val-m"},
		),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected create errors: %v", resp.Diagnostics)
	}

	if capturedReq == nil || len(capturedReq.Policies) != 3 {
		t.Fatalf("expected 3 policies in ensure request, got %v", capturedReq)
	}
	if capturedReq.Policies[0].Policy != "policy-z" || capturedReq.Policies[1].Policy != "policy-a" || capturedReq.Policies[2].Policy != "policy-m" {
		t.Errorf("request policy order mutated: %v, %v, %v", capturedReq.Policies[0].Policy, capturedReq.Policies[1].Policy, capturedReq.Policies[2].Policy)
	}

	var resultState iam.TenantGroupResourceModel
	_ = resp.State.Get(ctx, &resultState)
	policies := decodePoliciesList(t, ctx, resultState.Policies)
	if len(policies) != 3 {
		t.Fatalf("expected 3 policies in state, got %d", len(policies))
	}
	if policies[0].Policy.ValueString() != "policy-z" ||
		policies[1].Policy.ValueString() != "policy-a" ||
		policies[2].Policy.ValueString() != "policy-m" {
		t.Errorf("state policy order mutated: %v, %v, %v",
			policies[0].Policy.ValueString(),
			policies[1].Policy.ValueString(),
			policies[2].Policy.ValueString())
	}
}

func TestTenantGroupResource_InterveningWriter_LastWriterWins(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var ensureRequests []*iamv1beta.EnsureTenantGroupRequest
	currentPolicies := []*iamv1beta.TenantGroupPolicy{
		{Policy: "initial.policy", Value: "initial_val"},
	}

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          currentPolicies,
			EnrolledTenantIds: []string{},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		ensureRequests = append(ensureRequests, req)
		currentPolicies = req.Policies
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// Simulate out-of-band writer mutating server state before Terraform Update executes
	currentPolicies = []*iamv1beta.TenantGroupPolicy{
		{Policy: "out_of_band.policy", Value: "mutated_by_external_actor"},
	}

	// Terraform plans to apply desired configuration
	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("intervene-grp"),
		Name:              types.StringValue("intervene-grp"),
		Policies:          makePoliciesList(t, [2]string{"desired.policy", "desired_val"}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("intervene-grp"),
		Name:              types.StringValue("intervene-grp"),
		Policies:          makePoliciesList(t, [2]string{"initial.policy", "initial_val"}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)
	origState := tfsdk.State{Schema: schemaResp.Schema}
	_ = origState.Set(ctx, &stateModel)

	req := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
		State: origState,
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected update errors: %v", resp.Diagnostics)
	}

	if len(ensureRequests) != 1 {
		t.Fatalf("expected exactly 1 EnsureTenantGroup call, got %d", len(ensureRequests))
	}
	if len(ensureRequests[0].Policies) != 1 || ensureRequests[0].Policies[0].Policy != "desired.policy" {
		t.Errorf("expected planned policy to overwrite external mutation (last-writer-wins), got: %+v", ensureRequests[0].Policies)
	}

	var finalState iam.TenantGroupResourceModel
	_ = resp.State.Get(ctx, &finalState)
	policies := decodePoliciesList(t, ctx, finalState.Policies)
	if len(policies) != 1 || policies[0].Policy.ValueString() != "desired.policy" || policies[0].Value.ValueString() != "desired_val" {
		t.Errorf("expected state to reflect desired policy, got: %+v", policies)
	}

	// Verify subsequent Read returns the reconciled state
	readReq := resource.ReadRequest{State: resp.State}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", readResp.Diagnostics)
	}

	var readState iam.TenantGroupResourceModel
	_ = readResp.State.Get(ctx, &readState)
	readPolicies := decodePoliciesList(t, ctx, readState.Policies)
	if len(readPolicies) != 1 || readPolicies[0].Policy.ValueString() != "desired.policy" || readPolicies[0].Value.ValueString() != "desired_val" {
		t.Errorf("expected read state to reflect reconciled policy, got: %+v", readPolicies)
	}
}
