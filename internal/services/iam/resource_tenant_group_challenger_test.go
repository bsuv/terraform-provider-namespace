package iam_test

import (
	"context"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// TestChallenger_TenantGroup_EmptyVsNullPoliciesTransition tests transitioning
// a tenant group from multiple policies to explicit empty list `[]` and from empty list
// to omitted (null), confirming accurate EnsureTenantGroup requests and state updates.
func TestChallenger_TenantGroup_EmptyVsNullPoliciesTransition(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var describeCount int
	var currentPolicies []*iamv1beta.TenantGroupPolicy
	var lastEnsuredPolicies []*iamv1beta.TenantGroupPolicy

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		describeCount++
		if describeCount == 1 {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          currentPolicies,
			EnrolledTenantIds: []string{},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		lastEnsuredPolicies = req.Policies
		currentPolicies = req.Policies
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// 1. Create with 2 policies
	planModel := iam.TenantGroupResourceModel{
		ID:   types.StringUnknown(),
		Name: types.StringValue("transition-group"),
		Policies: makePoliciesList(t,
			[2]string{"policy-1", "val-1"},
			[2]string{"policy-2", "val-2"},
		),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics)
	}

	if len(lastEnsuredPolicies) != 2 {
		t.Fatalf("expected 2 policies on create, got %d", len(lastEnsuredPolicies))
	}

	var stateAfterCreate iam.TenantGroupResourceModel
	createResp.State.Get(ctx, &stateAfterCreate)
	createPolicies := decodePoliciesList(t, ctx, stateAfterCreate.Policies)
	if len(createPolicies) != 2 {
		t.Fatalf("expected 2 policies in state, got %d", len(createPolicies))
	}

	// 2. Update to empty list `[]` of policies
	emptyList, _ := types.ListValue(iam.TenantGroupPolicyObjectType, []attr.Value{})
	updatePlan := stateAfterCreate
	updatePlan.Policies = emptyList

	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState.Set(ctx, &updatePlan)

	updateReq := resource.UpdateRequest{
		State: createResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update to empty list failed: %v", updateResp.Diagnostics)
	}

	if len(lastEnsuredPolicies) != 0 {
		t.Fatalf("expected 0 policies after update to empty list, got %d", len(lastEnsuredPolicies))
	}

	var stateAfterEmpty iam.TenantGroupResourceModel
	updateResp.State.Get(ctx, &stateAfterEmpty)
	emptyPolicies := decodePoliciesList(t, ctx, stateAfterEmpty.Policies)
	if len(emptyPolicies) != 0 {
		t.Fatalf("expected 0 policies in state after empty list update, got %d", len(emptyPolicies))
	}

	// 3. Update to null policies (omitted)
	updatePlan2 := stateAfterEmpty
	updatePlan2.Policies = types.ListNull(iam.TenantGroupPolicyObjectType)

	updatePlanState2 := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState2.Set(ctx, &updatePlan2)

	updateReq2 := resource.UpdateRequest{
		State: updateResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState2.Raw},
	}
	updateResp2 := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq2, updateResp2)
	if updateResp2.Diagnostics.HasError() {
		t.Fatalf("Update to null policies failed: %v", updateResp2.Diagnostics)
	}

	var stateAfterNull iam.TenantGroupResourceModel
	updateResp2.State.Get(ctx, &stateAfterNull)
	if !stateAfterNull.Policies.IsNull() && len(stateAfterNull.Policies.Elements()) != 0 {
		t.Fatalf("expected null/empty policies in state, got %d", len(stateAfterNull.Policies.Elements()))
	}
}

// TestChallenger_TenantGroup_PartialUnknownPoliciesInPlan tests when individual policy objects
// in the policies list have unknown attributes during Plan.
func TestChallenger_TenantGroup_PartialUnknownPoliciesInPlan(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var describeCount int
	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		describeCount++
		if describeCount == 1 {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "computed-pol", Value: "computed-val"},
			},
			EnrolledTenantIds: []string{},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// Object with known policy type and unknown value
	pObj, _ := types.ObjectValue(iam.TenantGroupPolicyAttrTypes, map[string]attr.Value{
		"policy": types.StringValue("computed-pol"),
		"value":  types.StringUnknown(),
	})
	polList, _ := types.ListValue(iam.TenantGroupPolicyObjectType, []attr.Value{pObj})

	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringUnknown(),
		Name:              types.StringValue("partial-pol-group"),
		Policies:          polList,
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed with partial unknown policy value: %v", createResp.Diagnostics)
	}

	var state iam.TenantGroupResourceModel
	createResp.State.Get(ctx, &state)
	if state.Name.ValueString() != "partial-pol-group" {
		t.Errorf("state name mismatch: %s", state.Name.ValueString())
	}
}
