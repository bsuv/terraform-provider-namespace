package iam_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// TestChallenger_TenantGroup_InterveningWriters_MultipleMutations verifies that when multiple
// intervening out-of-band writers modify a tenant group between plan and apply, Terraform's
// subsequent Update unconditionally enforces the planned state (last-writer-wins).
func TestChallenger_TenantGroup_InterveningWriters_MultipleMutations(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	var ensureCalls []*iamv1beta.EnsureTenantGroupRequest
	currentPolicies := []*iamv1beta.TenantGroupPolicy{
		{Policy: "baseline.policy", Value: `{"tier":"standard"}`},
	}

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          currentPolicies,
			EnrolledTenantIds: []string{},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		ensureCalls = append(ensureCalls, req)
		currentPolicies = req.Policies
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// Step 1: Initial state recorded in Terraform
	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("multi-writer-group"),
		Name:              types.StringValue("multi-writer-group"),
		Policies:          makePoliciesList(t, [2]string{"baseline.policy", `{"tier":"standard"}`}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	// Step 2: Terraform plans to update to planned.policy
	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("multi-writer-group"),
		Name:              types.StringValue("multi-writer-group"),
		Policies:          makePoliciesList(t, [2]string{"planned.policy", `{"tier":"enterprise"}`}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	// Step 3: Out-of-band writer 1 mutates server state
	mu.Lock()
	currentPolicies = []*iamv1beta.TenantGroupPolicy{
		{Policy: "oob1.policy", Value: `{"writer":"writer-1"}`},
	}
	mu.Unlock()

	// Step 4: Out-of-band writer 2 overwrites server state with different policies
	mu.Lock()
	currentPolicies = []*iamv1beta.TenantGroupPolicy{
		{Policy: "oob2.policy", Value: `{"writer":"writer-2"}`},
		{Policy: "oob3.policy", Value: `{"writer":"writer-3"}`},
	}
	mu.Unlock()

	// Step 5: Terraform Update executes
	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
		State: state,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("unexpected update errors: %v", updateResp.Diagnostics)
	}

	// Verify EnsureTenantGroup was called with planned.policy, overwriting all intervening mutations
	mu.Lock()
	defer mu.Unlock()
	if len(ensureCalls) != 1 {
		t.Fatalf("expected exactly 1 EnsureTenantGroup call, got %d", len(ensureCalls))
	}
	if len(ensureCalls[0].Policies) != 1 || ensureCalls[0].Policies[0].Policy != "planned.policy" {
		t.Fatalf("expected planned.policy to overwrite intervening writes, got: %+v", ensureCalls[0].Policies)
	}

	var finalState iam.TenantGroupResourceModel
	_ = updateResp.State.Get(ctx, &finalState)
	pols := decodePoliciesList(t, ctx, finalState.Policies)
	if len(pols) != 1 || pols[0].Policy.ValueString() != "planned.policy" {
		t.Fatalf("expected state to reflect planned.policy, got: %+v", pols)
	}
}

// TestChallenger_TenantGroup_InterveningDeletion_RecreationOnUpdate tests that if an out-of-band
// actor deletes the tenant group on the server between Plan and Update, EnsureTenantGroup recreates
// it and Update succeeds.
func TestChallenger_TenantGroup_InterveningDeletion_RecreationOnUpdate(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	var currentPolicies []*iamv1beta.TenantGroupPolicy
	groupExists := true

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if !groupExists {
			return nil, status.Error(codes.NotFound, "group deleted out-of-band")
		}
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          currentPolicies,
			EnrolledTenantIds: []string{},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		groupExists = true
		currentPolicies = req.Policies
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// Step 1: Initial state in Terraform
	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("del-recreate-group"),
		Name:              types.StringValue("del-recreate-group"),
		Policies:          makePoliciesList(t, [2]string{"p1", "v1"}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	// Step 2: Out-of-band deletion
	mu.Lock()
	groupExists = false
	currentPolicies = nil
	mu.Unlock()

	// Step 3: Terraform Update runs
	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("del-recreate-group"),
		Name:              types.StringValue("del-recreate-group"),
		Policies:          makePoliciesList(t, [2]string{"p2", "v2"}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
		State: state,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("unexpected update error when group was recreated after intervening deletion: %v", updateResp.Diagnostics)
	}

	var finalState iam.TenantGroupResourceModel
	_ = updateResp.State.Get(ctx, &finalState)
	if finalState.Name.ValueString() != "del-recreate-group" {
		t.Fatalf("expected state name 'del-recreate-group', got %q", finalState.Name.ValueString())
	}
	pols := decodePoliciesList(t, ctx, finalState.Policies)
	if len(pols) != 1 || pols[0].Policy.ValueString() != "p2" {
		t.Fatalf("expected policy 'p2' in recreated group state, got: %+v", pols)
	}
}

// TestChallenger_TenantGroup_InterveningEnrolledTenants tests that if out-of-band actors enroll
// tenants into the group, Terraform Update preserves the server-reported enrolled tenant IDs
// deterministically sorted without model corruption.
func TestChallenger_TenantGroup_InterveningEnrolledTenants(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "pol-1", Value: "val-1"},
			},
			EnrolledTenantIds: []string{"tenant-gamma", "tenant-alpha", "tenant-beta"},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue("enrolled-tg"),
		Name:              types.StringValue("enrolled-tg"),
		Policies:          makePoliciesList(t, [2]string{"pol-1", "val-1"}),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	stateModel := planModel
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
		State: state,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("unexpected update error: %v", updateResp.Diagnostics)
	}

	var resultState iam.TenantGroupResourceModel
	_ = updateResp.State.Get(ctx, &resultState)
	if resultState.EnrolledTenantIDs.IsNull() {
		t.Fatal("expected non-null enrolled_tenant_ids in state")
	}

	var enrolled []string
	_ = resultState.EnrolledTenantIDs.ElementsAs(ctx, &enrolled, false)
	if len(enrolled) != 3 {
		t.Fatalf("expected 3 enrolled tenants, got %d", len(enrolled))
	}
	// Verify deterministic sorting: alpha, beta, gamma
	if enrolled[0] != "tenant-alpha" || enrolled[1] != "tenant-beta" || enrolled[2] != "tenant-gamma" {
		t.Fatalf("expected sorted enrolled tenants [tenant-alpha, tenant-beta, tenant-gamma], got: %v", enrolled)
	}
}

// TestChallenger_TenantGroup_ConcurrentGoroutines_RaceStress tests 30 concurrent goroutines
// issuing Create, Update, Read, Delete, and Ensure calls against MockServer to stress race detection.
func TestChallenger_TenantGroup_ConcurrentGoroutines_RaceStress(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.RWMutex
	store := make(map[string]*iamv1beta.DescribeTenantGroupResponse)

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.RLock()
		defer mu.RUnlock()
		item, exists := store[req.Name]
		if !exists {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:              item.Name,
			Policies:          item.Policies,
			EnrolledTenantIds: item.EnrolledTenantIds,
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		store[req.Name] = &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.Name,
			Policies:          req.Policies,
			EnrolledTenantIds: []string{"tenant-1", "tenant-2"},
		}
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(store, req.Name)
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	var wg sync.WaitGroup
	numWorkers := 30

	for i := 0; i < numWorkers; i++ {
		workerID := i
		groupName := fmt.Sprintf("race-group-%d", workerID%5)
		wg.Add(1)
		go func() {
			defer wg.Done()

			switch workerID % 4 {
			case 0:
				// Worker performs Create
				planModel := iam.TenantGroupResourceModel{
					ID:                types.StringUnknown(),
					Name:              types.StringValue(groupName),
					Policies:          makePoliciesList(t, [2]string{fmt.Sprintf("pol-%d", workerID), "val"}),
					EnrolledTenantIDs: types.ListNull(types.StringType),
				}
				planState := tfsdk.State{Schema: schemaResp.Schema}
				_ = planState.Set(ctx, &planModel)
				createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
				createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
				r.Create(ctx, createReq, createResp)

			case 1:
				// Worker performs Update
				planModel := iam.TenantGroupResourceModel{
					ID:                types.StringValue(groupName),
					Name:              types.StringValue(groupName),
					Policies:          makePoliciesList(t, [2]string{fmt.Sprintf("pol-updated-%d", workerID), "val"}),
					EnrolledTenantIDs: types.ListNull(types.StringType),
				}
				planState := tfsdk.State{Schema: schemaResp.Schema}
				_ = planState.Set(ctx, &planModel)
				updateReq := resource.UpdateRequest{
					Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
					State: planState,
				}
				updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
				r.Update(ctx, updateReq, updateResp)

			case 2:
				// Worker performs Read
				stateModel := iam.TenantGroupResourceModel{
					ID:                types.StringValue(groupName),
					Name:              types.StringValue(groupName),
					Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
					EnrolledTenantIDs: types.ListNull(types.StringType),
				}
				state := tfsdk.State{Schema: schemaResp.Schema}
				_ = state.Set(ctx, &stateModel)
				readReq := resource.ReadRequest{State: state}
				readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
				r.Read(ctx, readReq, readResp)

			case 3:
				// Worker performs Delete
				stateModel := iam.TenantGroupResourceModel{
					ID:                types.StringValue(groupName),
					Name:              types.StringValue(groupName),
					Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
					EnrolledTenantIDs: types.ListNull(types.StringType),
				}
				state := tfsdk.State{Schema: schemaResp.Schema}
				_ = state.Set(ctx, &stateModel)
				delReq := resource.DeleteRequest{State: state}
				delResp := &resource.DeleteResponse{State: state}
				r.Delete(ctx, delReq, delResp)
			}
		}()
	}

	wg.Wait()
}

// TestChallenger_TenantGroup_SchemaAndDocumentationContract verifies that the resource schema description
// and documentation explicitly warn about non-atomic Describe+Ensure lifecycle, exclusive ownership,
// and last-writer-wins behavior.
func TestChallenger_TenantGroup_SchemaAndDocumentationContract(t *testing.T) {
	ctx := context.Background()
	r := iam.NewTenantGroupResource()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema error: %v", schemaResp.Diagnostics)
	}

	schemaDesc := schemaResp.Schema.Description
	requiredKeywords := []string{
		"Exclusive ownership",
		"non-atomic Describe + Ensure",
		"last-writer-wins",
		"out-of-band writers",
	}

	for _, kw := range requiredKeywords {
		if !strings.Contains(schemaDesc, kw) {
			t.Errorf("tenant group schema description missing required concurrency keyword %q:\n%s", kw, schemaDesc)
		}
	}

	// Verify documentation file
	docPath := filepath.Join("..", "..", "..", "docs", "resources", "tenant_group.md")
	docBytes, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("failed to read docs/resources/tenant_group.md: %v", err)
	}
	docContent := string(docBytes)

	for _, kw := range requiredKeywords {
		if !strings.Contains(docContent, kw) {
			t.Errorf("docs/resources/tenant_group.md missing required concurrency keyword %q", kw)
		}
	}
}
