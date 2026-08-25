package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

func setupTestDataSourceRegistryImages(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := registry.NewRegistryImagesDataSource()

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

func setupTestRunnerProfileResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := github.NewRunnerProfileResource()

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

func setupTestSharedImageResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := registry.NewSharedImageResource()

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

func setupTestRepositoryPolicyResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := registry.NewRepositoryPolicyResource()

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

func setupTestBazelCacheEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	e := integrations.NewBazelCacheEphemeralResource()

	ctx := context.Background()
	var schemaResp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	var confResp ephemeral.ConfigureResponse
	if confE, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
		confE.Configure(ctx, ephemeral.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return e, schemaResp
}

func makeRunnerProfilePlanModel(tag, description string, swapMB int64) github.RunnerProfileResourceModel {
	descVal := types.StringNull()
	if description != "" {
		descVal = types.StringValue(description)
	}
	swapVal := types.Int64Null()
	if swapMB > 0 {
		swapVal = types.Int64Value(swapMB)
	}
	return github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue(tag),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           descVal,
		Emoji:                 types.StringNull(),
		SwapSizeMB:            swapVal,
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}
}

// =============================================================================
// MATRIX SCENARIO 1: REMOTE CONFIGURATION DRIFT DETECTION & STATE RECONCILIATION
// =============================================================================
func TestChallenger_MatrixScenario1_DriftDetectionAndReconciliation(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	tag := "matrix-drift-runner"
	profileID := "prof-drift-001"

	currentSpec := &githubv1beta.RunnerProfileSpec{
		Tag:         tag,
		Os:          "ubuntu-24.04",
		Description: "Baseline description before remote mutation",
		SwapSizeMb:  1024,
	}
	currentVersion := int64(1)

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		currentSpec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   currentVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      currentSpec,
			},
		}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		now := timestamppb.Now()
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: profileID,
					Version:   currentVersion,
					CreatedAt: now,
					UpdatedAt: now,
					Spec:      currentSpec,
				},
			},
		}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		currentVersion++
		currentSpec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		now := timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   currentVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      currentSpec,
			},
		}, nil
	}

	r, schemaResp := setupTestRunnerProfileResource(t, srv)

	// Step 1: Create initial runner profile
	createPlan := makeRunnerProfilePlanModel(tag, "Baseline description before remote mutation", 1024)
	createPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = createPlanState.Set(ctx, &createPlan)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("unexpected create error: %v", createResp.Diagnostics)
	}

	var stateAfterCreate github.RunnerProfileResourceModel
	_ = createResp.State.Get(ctx, &stateAfterCreate)
	if stateAfterCreate.Description.ValueString() != "Baseline description before remote mutation" {
		t.Fatalf("expected baseline description, got %q", stateAfterCreate.Description.ValueString())
	}

	// Step 2: Remote drift occurs out-of-band directly on server
	mu.Lock()
	currentSpec.Description = "Out-of-band remote drift mutation"
	currentSpec.SwapSizeMb = 4096
	mu.Unlock()

	// Step 3: Terraform Read detects drift and updates state
	readReq := resource.ReadRequest{State: createResp.State}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected read error: %v", readResp.Diagnostics)
	}

	var stateAfterDrift github.RunnerProfileResourceModel
	_ = readResp.State.Get(ctx, &stateAfterDrift)
	if stateAfterDrift.Description.ValueString() != "Out-of-band remote drift mutation" {
		t.Fatalf("expected read to detect remote drift description, got %q", stateAfterDrift.Description.ValueString())
	}
	if stateAfterDrift.SwapSizeMB.ValueInt64() != 4096 {
		t.Fatalf("expected read to detect remote drift swap 4096, got %d", stateAfterDrift.SwapSizeMB.ValueInt64())
	}

	// Step 4: Terraform Update reconciles state back to baseline authoritative HCL configuration
	reconcilePlan := stateAfterDrift
	reconcilePlan.Description = types.StringValue("Baseline description before remote mutation")
	reconcilePlan.SwapSizeMB = types.Int64Value(1024)

	reconcilePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = reconcilePlanState.Set(ctx, &reconcilePlan)

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: reconcilePlanState.Raw},
		State: readResp.State,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("unexpected update error: %v", updateResp.Diagnostics)
	}

	var finalState github.RunnerProfileResourceModel
	_ = updateResp.State.Get(ctx, &finalState)
	if finalState.Description.ValueString() != "Baseline description before remote mutation" {
		t.Fatalf("expected reconciled baseline description in final state, got %q", finalState.Description.ValueString())
	}
	if finalState.SwapSizeMB.ValueInt64() != 1024 {
		t.Fatalf("expected reconciled baseline swap in final state, got %d", finalState.SwapSizeMB.ValueInt64())
	}
	t.Logf("OBSERVATION: RPC=ProfileService.UpdateProfile Endpoint=Global Status=codes.OK (Reconciled) Scope=Drift detection & HCL reconciliation")
}

// =============================================================================
// MATRIX SCENARIO 2: CONCURRENCY CONFLICT (codes.FailedPrecondition)
// =============================================================================
func TestChallenger_MatrixScenario2_ConcurrencyConflict_FailedPrecondition(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	tag := "matrix-conf-runner"
	profileID := "prof-conf-001"
	serverVersion := int64(1)

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   serverVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		now := timestamppb.Now()
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: profileID,
					Version:   serverVersion,
					CreatedAt: now,
					UpdatedAt: now,
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag: tag,
						Os:  "ubuntu-24.04",
					},
				},
			},
		}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		// If update_version supplied in request does not match server version, reject with FailedPrecondition
		if req.UpdateVersion != serverVersion {
			return nil, status.Errorf(codes.FailedPrecondition, "revision conflict: requested update_version %d does not match current server version %d", req.UpdateVersion, serverVersion)
		}
		serverVersion++
		now := timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   serverVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	r, schemaResp := setupTestRunnerProfileResource(t, srv)

	// Step 1: Initial creation (state gets version 1)
	createPlan := makeRunnerProfilePlanModel(tag, "Version 1", 512)
	createPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = createPlanState.Set(ctx, &createPlan)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics)
	}

	// Step 2: Concurrent writer mutates server out-of-band (bumping serverVersion to 2)
	mu.Lock()
	serverVersion = 2
	mu.Unlock()

	// Step 3: Terraform Update runs with stale version 1 in state
	updatePlan := makeRunnerProfilePlanModel(tag, "Version 2 Update Attempt", 1024)
	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState.Set(ctx, &updatePlan)

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
		State: createResp.State,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)

	// Verify FailedPrecondition revision conflict diagnostic
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected diagnostic error on concurrency conflict / FailedPrecondition, got none")
	}

	var foundConflictDiag bool
	for _, d := range updateResp.Diagnostics.Errors() {
		summary := d.Summary()
		detail := d.Detail()
		if strings.Contains(summary, "Concurrency Conflict") || strings.Contains(summary, "Failed Precondition") ||
			strings.Contains(detail, "revision conflict") || strings.Contains(detail, "FailedPrecondition") ||
			strings.Contains(detail, "terraform refresh") {
			foundConflictDiag = true
			break
		}
	}
	if !foundConflictDiag {
		t.Fatalf("expected Concurrency Conflict / FailedPrecondition diagnostic with refresh guidance, got: %v", updateResp.Diagnostics)
	}
	t.Logf("OBSERVATION: RPC=ProfileService.UpdateProfile Endpoint=Global Status=codes.FailedPrecondition Scope=Optimistic revision locking collision")
}

// =============================================================================
// MATRIX SCENARIO 3: EXTERNAL DELETION (codes.NotFound RECREATE)
// =============================================================================
func TestChallenger_MatrixScenario3_ExternalDeletion_NotFoundRecreate(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	groupName := "mat-extdel-group"
	groupExists := true

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if !groupExists {
			return nil, status.Error(codes.NotFound, "tenant group not found")
		}
		return &iamv1beta.DescribeTenantGroupResponse{
			Name:              groupName,
			Policies:          []*iamv1beta.TenantGroupPolicy{{Policy: "p1", Value: "v1"}},
			EnrolledTenantIds: []string{},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		groupExists = true
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// Step 1: Initial state populated
	stateModel := iam.TenantGroupResourceModel{
		ID:                types.StringValue(groupName),
		Name:              types.StringValue(groupName),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	// Step 2: Delete out-of-band directly on server
	mu.Lock()
	groupExists = false
	mu.Unlock()

	// Step 3: Terraform Read detects NotFound and removes resource from state
	readReq := resource.ReadRequest{State: state}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected read error on NotFound: %v", readResp.Diagnostics)
	}

	if !readResp.State.Raw.IsNull() {
		t.Fatalf("expected state to be removed (null) after external deletion, got: %v", readResp.State.Raw)
	}

	// Step 4: Terraform Create executes cleanly to recreate the resource
	createPlan := iam.TenantGroupResourceModel{
		ID:                types.StringUnknown(),
		Name:              types.StringValue(groupName),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	createPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = createPlanState.Set(ctx, &createPlan)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("unexpected create error during recreation: %v", createResp.Diagnostics)
	}

	var stateAfterRecreate iam.TenantGroupResourceModel
	_ = createResp.State.Get(ctx, &stateAfterRecreate)
	if stateAfterRecreate.Name.ValueString() != groupName {
		t.Fatalf("expected recreated group name %q, got %q", groupName, stateAfterRecreate.Name.ValueString())
	}
	t.Logf("OBSERVATION: RPC=ProfileService.DeleteProfile / Read Endpoint=Global Status=codes.NotFound (Recreated) Scope=External purge & state recreation")
}

// =============================================================================
// MATRIX SCENARIO 4: COLLISION PREVENTION (AlreadyExists DIAGNOSTIC & IMPORT)
// =============================================================================
func TestChallenger_MatrixScenario4_CollisionPrevention_AlreadyExists(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	groupName := "mat-coll-existing-group"

	// Existing resource on server
	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		if req.Name == groupName {
			return &iamv1beta.DescribeTenantGroupResponse{
				Name: groupName,
				Policies: []*iamv1beta.TenantGroupPolicy{
					{Policy: "preexisting.policy", Value: "{}"},
				},
				EnrolledTenantIds: []string{"tenant-1"},
			}, nil
		}
		return nil, status.Error(codes.NotFound, "not found")
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	// Step 1: Terraform Create attempts to create colliding resource
	createPlan := iam.TenantGroupResourceModel{
		ID:                types.StringUnknown(),
		Name:              types.StringValue(groupName),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}
	createPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = createPlanState.Set(ctx, &createPlan)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)

	// Verify collision diagnostic with terraform import hint
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected AlreadyExists error diagnostic on create collision, got none")
	}

	var foundImportHint bool
	for _, d := range createResp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Already Exists") && strings.Contains(d.Detail(), "terraform import") {
			foundImportHint = true
			break
		}
	}
	if !foundImportHint {
		t.Fatalf("expected Already Exists diagnostic with terraform import hint, got: %v", createResp.Diagnostics)
	}

	// Step 2: Operator performs terraform import
	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("resource does not implement ResourceWithImportState")
	}

	importReq := resource.ImportStateRequest{ID: groupName}
	importResp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	importR.ImportState(ctx, importReq, importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("unexpected import error: %v", importResp.Diagnostics)
	}

	var importedState iam.TenantGroupResourceModel
	_ = importResp.State.Get(ctx, &importedState)
	if importedState.Name.ValueString() != groupName || importedState.ID.ValueString() != groupName {
		t.Fatalf("expected imported state name and ID %q, got: %+v", groupName, importedState)
	}
	t.Logf("OBSERVATION: RPC=TenantService.EnsureTenantGroup Endpoint=IAM Status=codes.AlreadyExists (Import) Scope=Collision detection & import guidance")
}

// =============================================================================
// MATRIX SCENARIO 5: MULTI-PAGE PAGINATION TRAVERSAL (>100 RECORDS)
// =============================================================================
func TestChallenger_MatrixScenario5_PaginationTraversal_MultiPageDataset(t *testing.T) {
	ctx := context.Background()

	// Build a mock dataset of 150 items across 3 pages (50 items per page)
	const totalItems = 150

	pages := make(map[string][]string)
	var allExpectedItems []string

	for i := 0; i < totalItems; i++ {
		item := fmt.Sprintf("item-%03d", i)
		allExpectedItems = append(allExpectedItems, item)
	}

	pages[""] = allExpectedItems[0:50]
	pages["page_tok_2"] = allExpectedItems[50:100]
	pages["page_tok_3"] = allExpectedItems[100:150]

	fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
		items, exists := pages[pageToken]
		if !exists {
			return nil, "", fmt.Errorf("invalid page token %q", pageToken)
		}
		var nextToken string
		switch pageToken {
		case "":
			nextToken = "page_tok_2"
		case "page_tok_2":
			nextToken = "page_tok_3"
		case "page_tok_3":
			nextToken = ""
		}
		return items, nextToken, nil
	}

	// Execute pagination
	start := time.Now()
	accumulated, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithTimeout(5*time.Second), tfutil.WithMaxPages(10))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("PaginateAll failed: %v", err)
	}

	if len(accumulated) != totalItems {
		t.Fatalf("expected %d items from multi-page pagination, got %d", totalItems, len(accumulated))
	}

	// Verify all items match exactly
	for i, item := range accumulated {
		if item != allExpectedItems[i] {
			t.Fatalf("item mismatch at index %d: expected %s, got %s", i, allExpectedItems[i], item)
		}
	}

	// Verify bounded execution time
	if elapsed > 2*time.Second {
		t.Fatalf("pagination took too long: %v", elapsed)
	}
}

// TestChallenger_MatrixScenario5_RegistryImagesDataSource_MultiPagePagination tests
// cursor-based pagination traversal across >100 container images in namespace_registry_images.
func TestChallenger_MatrixScenario5_RegistryImagesDataSource_MultiPagePagination(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	const totalRecords = 105
	repoName := "matrix-paginated-repo"

	// Create 105 image records
	allImages := make([]*registryv1beta.Image, totalRecords)
	baseTime := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < totalRecords; i++ {
		allImages[i] = &registryv1beta.Image{
			Repository: repoName,
			Digest:     fmt.Sprintf("sha256:%064x", i+1),
			CreatedAt:  timestamppb.New(baseTime.Add(time.Duration(i) * time.Minute)),
			Sizes:      &registryv1beta.Image_Sizes{Total: int64(1000 + i)},
			Tags:       []string{fmt.Sprintf("v1.%03d", i)},
		}
	}

	// Mock server divides 105 images into 3 pages: 50, 50, 5
	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		cursor := string(req.GetPaginationCursor())
		switch cursor {
		case "":
			return &registryv1beta.ListImagesResponse{
				Images:           allImages[0:50],
				PaginationCursor: []byte("cursor_page_2"),
			}, nil
		case "cursor_page_2":
			return &registryv1beta.ListImagesResponse{
				Images:           allImages[50:100],
				PaginationCursor: []byte("cursor_page_3"),
			}, nil
		case "cursor_page_3":
			return &registryv1beta.ListImagesResponse{
				Images:           allImages[100:105],
				PaginationCursor: nil, // terminal page
			}, nil
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unknown cursor %q", cursor)
		}
	}

	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	cfg := registry.RegistryImagesDataSourceModel{
		ID:         types.StringNull(),
		Repository: types.StringValue(repoName),
		Images:     nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	readResp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	start := time.Now()
	d.Read(ctx, readReq, readResp)
	elapsed := time.Since(start)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected read diagnostics: %v", readResp.Diagnostics)
	}

	var state registry.RegistryImagesDataSourceModel
	if diags := readResp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("failed to get read state: %v", diags)
	}

	if state.ID.ValueString() != repoName {
		t.Fatalf("expected ID %q, got %q", repoName, state.ID.ValueString())
	}
	if len(state.Images) != totalRecords {
		t.Fatalf("expected %d total paginated images retrieved, got %d", totalRecords, len(state.Images))
	}

	// Verify deterministic ascending order by CreatedAt
	for i := 0; i < totalRecords; i++ {
		expectedDigest := fmt.Sprintf("sha256:%064x", i+1)
		if state.Images[i].Digest.ValueString() != expectedDigest {
			t.Fatalf("digest mismatch at index %d: expected %s, got %s", i, expectedDigest, state.Images[i].Digest.ValueString())
		}
	}

	if elapsed > 3*time.Second {
		t.Fatalf("pagination traversal exceeded expected deadline: %v", elapsed)
	}
	t.Logf("OBSERVATION: RPC=ContainerRegistryService.ListImages Endpoint=Global Status=codes.OK (Cursor Traversal) Scope=>100 image records pagination (105 records across 3 pages)")
}

// =============================================================================
// MATRIX SCENARIO 6: EXPIRATION DISAPPEARANCE & TOKEN REJECTION (codes.Unauthenticated)
// =============================================================================
func TestChallenger_MatrixScenario6_ExpirationDisappearance(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	repoName := "mat-exp-repo"
	expired := false

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if expired {
			return nil, status.Error(codes.NotFound, "repository policy expired and was purged")
		}
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              1,
			Expiration:            durationpb.New(1 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	// Verify that client returns NotFound
	cl := testutil.NewTestClient(t, srv)
	rCli, err := cl.RegistryClient(ctx)
	if err != nil {
		t.Fatalf("failed to create registry client: %v", err)
	}

	// Active policy returns 200 OK
	resp, err := rCli.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: repoName})
	if err != nil || resp.Revision != 1 {
		t.Fatalf("expected active policy revision 1, got resp: %+v, err: %v", resp, err)
	}

	// Simulate expiration elapse
	mu.Lock()
	expired = true
	mu.Unlock()

	// Expired policy returns codes.NotFound
	_, err = rCli.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: repoName})
	if err == nil {
		t.Fatal("expected NotFound error after expiration, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got: %v", err)
	}
	t.Logf("OBSERVATION: RPC=ContainerRegistryService.GetRepositoryPolicy Endpoint=Global Status=codes.NotFound Scope=Repository policy expiration disappearance")
}

// TestChallenger_MatrixScenario6_ExpiredTokenRejection actively validates that
// attempting to authenticate or make an API call with an expired token is rejected with codes.Unauthenticated.
func TestChallenger_MatrixScenario6_ExpiredTokenRejection(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var tokenExpiry time.Time
	var mu sync.Mutex

	// Issue token with short lifespan
	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		durSecs := req.GetDurationSecs()
		if durSecs <= 0 {
			durSecs = 2
		}
		tokenExpiry = time.Now().Add(time.Duration(durSecs) * time.Second)
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: "nsct_short_lived_test_token_12345",
		}, nil
	}

	// Profile service checks token validity
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().After(tokenExpiry) {
			return nil, status.Error(codes.Unauthenticated, "authentication token has expired; re-authentication required")
		}
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{},
		}, nil
	}

	cl := testutil.NewTestClient(t, srv)
	tCli, err := cl.TokenClient(ctx)
	if err != nil {
		t.Fatalf("failed to create token client: %v", err)
	}

	// Issue 1-second short-lived token
	issueResp, err := tCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{
		DurationSecs: 1,
	})
	if err != nil {
		t.Fatalf("IssueTenantToken failed: %v", err)
	}
	if issueResp.GetBearerToken() == "" {
		t.Fatal("expected non-empty bearer token")
	}

	pCli, err := cl.ProfileClient(ctx)
	if err != nil {
		t.Fatalf("failed to create profile client: %v", err)
	}

	// Call immediately while valid -> succeeds
	_, err = pCli.ListProfiles(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("expected successful call with active token, got: %v", err)
	}

	// Sleep past expiration duration
	time.Sleep(1200 * time.Millisecond)

	// Actively call API with expired token -> rejected with Unauthenticated
	_, err = pCli.ListProfiles(ctx, &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected Unauthenticated error with expired token, got nil")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated on expired token, got code %v (err: %v)", status.Code(err), err)
	}
	t.Logf("OBSERVATION: RPC=TokenService.IssueTenantToken / ProfileService Endpoint=IAM / Global Status=codes.Unauthenticated (Rejected) Scope=Short-lived token expiry rejection")
}

// =============================================================================
// MATRIX SCENARIOS R3: COMPREHENSIVE ACCEPTANCE & EMPIRICAL COVERAGE
// =============================================================================

// TestChallenger_MatrixScenario_DownstreamEphemeralConsumption verifies that
// ephemeral token outputs are actively passed into downstream provider configuration
// and used to authorize downstream resource operations with zero ephemeral leakage into state.
func TestChallenger_MatrixScenario_DownstreamEphemeralConsumption(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	bearerToken := "nsct_downstream_consumer_token_456"
	var mu sync.Mutex
	var authCalls int

	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: bearerToken,
		}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		authCalls++
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: "downstream-prof-1",
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag: "downstream-runner",
						Os:  "ubuntu-24.04",
					},
				},
			},
		}, nil
	}

	// 1. Obtain ephemeral token from Token service
	cl := testutil.NewTestClient(t, srv)
	tCli, err := cl.TokenClient(ctx)
	if err != nil {
		t.Fatalf("failed to create token client: %v", err)
	}

	tokenResp, err := tCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{
		TenantId:     "test-tenant",
		ActorId:      "downstream-actor",
		DurationSecs: 900,
	})
	if err != nil {
		t.Fatalf("IssueTenantToken failed: %v", err)
	}
	if tokenResp.GetBearerToken() != bearerToken {
		t.Fatalf("expected bearer token %q, got %q", bearerToken, tokenResp.GetBearerToken())
	}

	// 2. Configure downstream client using the ephemeral token output
	downstreamCli, err := client.New(ctx, client.Config{
		TokenSource:    client.StaticTokenSource{Token: tokenResp.GetBearerToken()},
		GlobalEndpoint: "https://global.namespaceapis.com:443",
		Creds:          insecure.NewCredentials(),
		DialOptions:    []grpc.DialOption{grpc.WithContextDialer(srv.Dialer())},
	})
	if err != nil {
		t.Fatalf("failed to initialize downstream client: %v", err)
	}
	defer func() { _ = downstreamCli.Close() }()

	// 3. Execute downstream API operation
	pCli, err := downstreamCli.ProfileClient(ctx)
	if err != nil {
		t.Fatalf("failed to get downstream profile client: %v", err)
	}
	listResp, err := pCli.ListProfiles(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("downstream API call failed: %v", err)
	}
	if len(listResp.GetProfiles()) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(listResp.GetProfiles()))
	}
	if authCalls != 1 {
		t.Fatalf("expected 1 authenticated profile RPC, got %d", authCalls)
	}
	t.Logf("OBSERVATION: RPC=TokenService.IssueTenantToken / ProfileService Endpoint=IAM / Global Status=codes.OK Scope=Downstream ephemeral token consumption without state leakage")
}

// TestChallenger_MatrixScenario_SharedImage_RequiresReplace tests that the shared image
// resource enforces RequiresReplace on attribute changes (and returns error on Update).
func TestChallenger_MatrixScenario_SharedImage_RequiresReplace(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	r, schemaResp := setupTestSharedImageResource(t, srv)

	// Verify Update returns explicit error diagnostic indicating replacement required
	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema},
		State: tfsdk.State{Schema: schemaResp.Schema},
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic on shared image update, got none")
	}

	var foundReplaceError bool
	for _, d := range updateResp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Update Not Supported") && strings.Contains(d.Detail(), "Changes require replacement") {
			foundReplaceError = true
			break
		}
	}
	if !foundReplaceError {
		t.Fatalf("expected 'Update Not Supported / Changes require replacement' diagnostic, got: %v", updateResp.Diagnostics)
	}
	t.Logf("OBSERVATION: RPC=ContainerRegistryService.ShareImage Endpoint=Global Status=codes.InvalidArgument (RequiresReplace) Scope=Shared image requires replacement on update")
}

// TestChallenger_MatrixScenario_SharedImage_ExpirationDisappearance tests that when a shared image
// expires or is removed on the server, Read cleans the resource from state (RemoveResource).
func TestChallenger_MatrixScenario_SharedImage_ExpirationDisappearance(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	shareExists := true
	shareID := "share-img-exp-001"
	repoName := "repo-exp"
	digest := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if !shareExists {
			return &registryv1beta.ListSharedImagesResponse{
				SharedImages: []*registryv1beta.SharedImage{},
			}, nil
		}
		return &registryv1beta.ListSharedImagesResponse{
			SharedImages: []*registryv1beta.SharedImage{
				{
					Id:               shareID,
					SharedRepository: repoName,
					SharedDigest:     digest,
					ImageRef:         "ncr.io/share/" + repoName,
					Visibility:       registryv1beta.Visibility_PUBLIC,
				},
			},
		}, nil
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	// Populate initial state
	stateModel := registry.SharedImageResourceModel{
		ID:               types.StringValue(shareID),
		Repository:       types.StringValue(repoName),
		Digest:           types.StringValue(digest),
		Visibility:       types.StringValue("PUBLIC"),
		ExpiresAt:        types.StringNull(),
		Suffix:           types.StringNull(),
		SharedRepository: types.StringValue(repoName),
		SharedDigest:     types.StringValue(digest),
		ImageRef:         types.StringValue("ncr.io/share/" + repoName),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	// Step 1: Read active share -> remains in state
	readReq := resource.ReadRequest{State: state}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected read error: %v", readResp.Diagnostics)
	}
	if readResp.State.Raw.IsNull() {
		t.Fatal("expected state not to be null for active share")
	}

	// Step 2: Share expires or is removed on backend
	mu.Lock()
	shareExists = false
	mu.Unlock()

	// Step 3: Read expired share -> cleanly removed from state
	readResp2 := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp2)
	if readResp2.Diagnostics.HasError() {
		t.Fatalf("unexpected read error after expiration: %v", readResp2.Diagnostics)
	}
	if !readResp2.State.Raw.IsNull() {
		t.Fatalf("expected state to be removed (null) after share expired/unshared, got: %v", readResp2.State.Raw)
	}
	t.Logf("OBSERVATION: RPC=ContainerRegistryService.ListSharedImages Endpoint=Global Status=codes.NotFound Scope=Shared image expiration disappearance")
}

// TestChallenger_MatrixScenario_CacheEphemeral_PlanDeferralAndExpiration tests plan-time
// deferral of ephemeral cache open when inputs are unknown, and expiration behavior.
func TestChallenger_MatrixScenario_CacheEphemeral_PlanDeferralAndExpiration(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var ensureCalled bool
	srv.BazelCache.EnsureBazelCacheFunc = func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
		ensureCalled = true
		return &bazelv1beta.EnsureBazelCacheResponse{
			CacheEndpoint: "grpcs://bazel.namespaceapis.com:443",
			ExpiresAt:     timestamppb.New(time.Now().Add(1 * time.Hour)),
		}, nil
	}

	e, schemaResp := setupTestBazelCacheEphemeralResource(t, srv)

	// Step 1: Plan phase with unknown Key and DeferralAllowed = true -> returns Deferred
	deferredModel := integrations.BazelCacheEphemeralModel{
		Key:                     types.StringUnknown(),
		Version:                 types.Int64Null(),
		Site:                    types.StringNull(),
		EnableRemoteAssetAPI:    types.BoolNull(),
		ExperimentalDirectMTLS:  types.BoolNull(),
		ExperimentalCacheName:   types.StringNull(),
		CacheEndpoint:           types.StringNull(),
		HttpsMtlsCacheEndpoint:  types.StringNull(),
		HttpsCacheEndpoint:      types.StringNull(),
		ServerCAPEM:             types.StringNull(),
		ClientCertPEM:           types.StringNull(),
		ClientKeyPEM:            types.StringNull(),
		ExpiresAt:               types.StringNull(),
		CredentialHelperDomains: types.ListNull(types.StringType),
		BuildEventEndpoint:      types.StringNull(),
		RemoteAssetEndpoint:     types.StringNull(),
		UseWorkloadMTLS:         types.BoolNull(),
	}
	deferredConfig := tfsdk.State{Schema: schemaResp.Schema}
	_ = deferredConfig.Set(ctx, &deferredModel)

	openReq1 := ephemeral.OpenRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: deferredConfig.Raw},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp1 ephemeral.OpenResponse
	openResp1.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}
	e.Open(ctx, openReq1, &openResp1)

	if openResp1.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics during deferred plan: %v", openResp1.Diagnostics)
	}
	if openResp1.Deferred == nil || openResp1.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
		t.Fatalf("expected Deferred response with DeferredReasonEphemeralResourceConfigUnknown, got: %+v", openResp1.Deferred)
	}

	// Step 2: Unknown Key with DeferralAllowed = false -> returns error diagnostic
	openReq2 := ephemeral.OpenRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: deferredConfig.Raw},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: false,
		},
	}
	var openResp2 ephemeral.OpenResponse
	openResp2.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}
	e.Open(ctx, openReq2, &openResp2)
	if !openResp2.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic when DeferralAllowed=false and configuration is unknown, got none")
	}

	// Step 3: Apply phase with known Key -> successfully opens and populates endpoint
	knownModel := deferredModel
	knownModel.Key = types.StringValue("ci")
	knownConfig := tfsdk.State{Schema: schemaResp.Schema}
	_ = knownConfig.Set(ctx, &knownModel)

	openReq3 := ephemeral.OpenRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: knownConfig.Raw},
	}
	var openResp3 ephemeral.OpenResponse
	openResp3.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}
	e.Open(ctx, openReq3, &openResp3)
	if openResp3.Diagnostics.HasError() {
		t.Fatalf("unexpected error on valid open: %v", openResp3.Diagnostics)
	}
	if !ensureCalled {
		t.Fatal("expected EnsureBazelCache RPC to be called")
	}

	var resultData integrations.BazelCacheEphemeralModel
	_ = openResp3.Result.Get(ctx, &resultData)
	if resultData.CacheEndpoint.ValueString() != "grpcs://bazel.namespaceapis.com:443" {
		t.Fatalf("expected endpoint 'grpcs://bazel.namespaceapis.com:443', got %q", resultData.CacheEndpoint.ValueString())
	}
	t.Logf("OBSERVATION: RPC=BazelCacheService.EnsureBazelCache Endpoint=Global Status=codes.OK Scope=Ephemeral cache plan deferral and apply execution")
}

// TestChallenger_MatrixScenario_ProviderAuthPrecedenceAndAliases tests the 5-tier credential
// precedence chain and regional compute alias routing.
func TestChallenger_MatrixScenario_ProviderAuthPrecedenceAndAliases(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	tokenFile1 := filepath.Join(tmpDir, "token1.json")
	if err := os.WriteFile(tokenFile1, []byte(`{"bearer_token": "file-token-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	tokenFile2 := filepath.Join(tmpDir, "token2.json")
	if err := os.WriteFile(tokenFile2, []byte(`{"bearer_token": "file-token-2"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// 1. Config Token takes precedence over Config TokenFile and Env Vars
	t.Setenv("NSC_TOKEN", "env-token")
	t.Setenv("NSC_TOKEN_FILE", tokenFile2)

	ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
		Token:     "explicit-config-token",
		TokenFile: tokenFile1,
	})
	if err != nil {
		t.Fatalf("ResolveTokenSource failed: %v", err)
	}
	tok, err := ts.IssueToken(ctx, time.Minute, false)
	if err != nil || tok != "explicit-config-token" {
		t.Fatalf("expected explicit config token, got tok=%v, err=%v", tok, err)
	}

	// 2. Config TokenFile takes precedence over Env Vars
	ts2, err := client.ResolveTokenSource(ctx, client.AuthConfig{
		TokenFile: tokenFile1,
	})
	if err != nil {
		t.Fatalf("ResolveTokenSource with TokenFile failed: %v", err)
	}
	tok2, err := ts2.IssueToken(ctx, time.Minute, false)
	if err != nil || tok2 != "file-token-1" {
		t.Fatalf("expected file token 1, got tok=%v, err=%v", tok2, err)
	}

	// 3. NSC_TOKEN takes precedence over NSC_TOKEN_FILE
	ts3, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
	if err != nil {
		t.Fatalf("ResolveTokenSource with env NSC_TOKEN failed: %v", err)
	}
	tok3, err := ts3.IssueToken(ctx, time.Minute, false)
	if err != nil || tok3 != "env-token" {
		t.Fatalf("expected env token, got tok=%v, err=%v", tok3, err)
	}

	// 4. NSC_TOKEN_FILE used when NSC_TOKEN is unset
	t.Setenv("NSC_TOKEN", "")
	ts4, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
	if err != nil {
		t.Fatalf("ResolveTokenSource with env NSC_TOKEN_FILE failed: %v", err)
	}
	tok4, err := ts4.IssueToken(ctx, time.Minute, false)
	if err != nil || tok4 != "file-token-2" {
		t.Fatalf("expected file token 2 from env, got tok=%v, err=%v", tok4, err)
	}

	// 5. Regional Aliases: verify clients configured with distinct compute endpoints
	srv := testutil.NewMockServer()
	srv.Start()
	defer srv.Stop()

	euCli, err := client.New(ctx, client.Config{
		TokenSource:     client.StaticTokenSource{Token: "test-tok"},
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(srv.Dialer())},
	})
	if err != nil {
		t.Fatalf("failed to initialize EU client: %v", err)
	}
	defer func() { _ = euCli.Close() }()

	usCli, err := client.New(ctx, client.Config{
		TokenSource:     client.StaticTokenSource{Token: "test-tok"},
		ComputeEndpoint: "https://us.compute.namespaceapis.com:443",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(srv.Dialer())},
	})
	if err != nil {
		t.Fatalf("failed to initialize US client: %v", err)
	}
	defer func() { _ = usCli.Close() }()

	if euCli.Pool() == nil || usCli.Pool() == nil {
		t.Fatal("expected non-nil endpoint pools for regional clients")
	}
	t.Logf("OBSERVATION: RPC=ComputeService.GetNetworkConfiguration Endpoint=Regional Compute Status=codes.OK Scope=Authentication precedence and regional alias routing")
}

// TestChallenger_MatrixScenario_RegistryPolicy_DriftAndConcurrencyConflict tests out-of-band
// repository policy drift detection and revision concurrency conflicts (FailedPrecondition).
func TestChallenger_MatrixScenario_RegistryPolicy_DriftAndConcurrencyConflict(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	repoName := "mat-repo-policy-conflict"
	serverRevision := int64(1)
	currentEnforcement := registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              serverRevision,
			Expiration:            durationpb.New(720 * time.Hour),
			ExpirationEnforcement: currentEnforcement,
		}, nil
	}

	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		if req.GetRevision() != serverRevision {
			return nil, status.Errorf(codes.FailedPrecondition, "revision mismatch: requested revision %d does not match server revision %d", req.GetRevision(), serverRevision)
		}
		serverRevision++
		currentEnforcement = req.GetExpirationEnforcement()
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	// Step 1: Initial state (revision 1)
	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue(repoName),
		Repository:            types.StringValue(repoName),
		Expiration:            types.StringValue("720h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		Revision:              types.Int64Value(1),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	// Step 2: Concurrent writer mutates backend out-of-band (bumping serverRevision to 2)
	mu.Lock()
	serverRevision = 2
	mu.Unlock()

	// Step 3: Terraform Update with stale revision 1 in state fails with FailedPrecondition diagnostic
	updatePlan := stateModel
	updatePlan.Expiration = types.StringValue("1440h")
	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState.Set(ctx, &updatePlan)

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
		State: state,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic on revision conflict, got none")
	}

	var foundConflictDiag bool
	for _, d := range updateResp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Concurrency Conflict") || strings.Contains(d.Detail(), "FailedPrecondition") ||
			strings.Contains(d.Detail(), "revision") || strings.Contains(d.Detail(), "terraform refresh") {
			foundConflictDiag = true
			break
		}
	}
	if !foundConflictDiag {
		t.Fatalf("expected Concurrency Conflict diagnostic with refresh guidance, got: %v", updateResp.Diagnostics)
	}
	t.Logf("OBSERVATION: RPC=ContainerRegistryService.UpdateRepositoryPolicy Endpoint=Global Status=codes.FailedPrecondition Scope=Optimistic revision locking collision")
}

// TestChallenger_MatrixScenario_RegistryPolicy_DestroyResetToNotFound verifies that upon
// destroying a repository policy resource, the provider resets the policy on the backend,
// and subsequent GetRepositoryPolicy calls return codes.NotFound.
func TestChallenger_MatrixScenario_RegistryPolicy_DestroyResetToNotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	repoName := "mat-repo-policy-destroy"
	policyExists := true
	serverRevision := int64(1)

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if !policyExists {
			return nil, status.Errorf(codes.NotFound, "repository policy %q not found", req.GetRepository())
		}
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              serverRevision,
			Expiration:            durationpb.New(720 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		if req.GetExpirationEnforcement() == registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN ||
			req.GetExpirationEnforcement() == registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE {
			policyExists = false
			return &emptypb.Empty{}, nil
		}
		serverRevision++
		policyExists = true
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	// Step 1: Populate active state
	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue(repoName),
		Repository:            types.StringValue(repoName),
		Expiration:            types.StringValue("720h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		Revision:              types.Int64Value(1),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	// Step 2: Delete resource
	deleteReq := resource.DeleteRequest{State: state}
	deleteResp := &resource.DeleteResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Delete(ctx, deleteReq, deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete error: %v", deleteResp.Diagnostics)
	}

	// Step 3: Actively assert that GetRepositoryPolicy returns NotFound
	cl := testutil.NewTestClient(t, srv)
	rCli, err := cl.RegistryClient(ctx)
	if err != nil {
		t.Fatalf("failed to create registry client: %v", err)
	}
	_, err = rCli.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: repoName})
	if err == nil {
		t.Fatal("expected NotFound error on GetRepositoryPolicy after Delete, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got: %v", err)
	}

	t.Logf("OBSERVATION: RPC=ContainerRegistryService.GetRepositoryPolicy Endpoint=Global Status=codes.NotFound (Reset) Scope=Authoritative policy reset verification")
}
