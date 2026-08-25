package github_test

import (
	"context"
	"strings"
	"testing"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
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

func sampleFullProfile(profileID string, version int64) *githubv1beta.RunnerProfileWithStatus {
	now := timestamppb.Now()
	return &githubv1beta.RunnerProfileWithStatus{
		ProfileId: profileID,
		Version:   version,
		CreatedAt: now,
		UpdatedAt: now,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag:         "ubuntu-24-04-large",
			Description: "High performance builder",
			Os:          "ubuntu-24.04",
			Emoji:       "🚀",
			SwapSizeMb:  2048,
			BuilderMode: githubv1beta.BuilderMode_USE_REMOTE_BUILDER,
			AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_PERMISSIVE,
			ExperimentalFeatures: []string{
				"nested-virtualization",
			},
			InstanceShape: &computev1beta.InstanceShape{
				VirtualCpu:      8,
				MemoryMegabytes: 32768,
				MachineArch:     "amd64",
				Os:              "linux",
				Selectors: []*stdlib.Label{
					{Name: "purpose", Value: "build"},
				},
			},
			CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{
				{
					VolumeTag:                 "build-cache",
					VolumeSizeGb:              100,
					VolumeReservedSizeGb:      10,
					BundledContainerCache:     true,
					BundledBuildkitCache:      true,
					BundledGitMirror:          true,
					BundledRunnerToolsCache:   true,
					BundledRunnerActionsCache: true,
					AllowCommitFromBranch:     []string{"main", "release/*"},
				},
			},
			CustomRunnerImageSpec: &githubv1beta.CustomRunnerImageSpec{
				UbuntuSpec: &githubv1beta.UbuntuBaseImageSpec{
					AptPackage: []string{"clang", "lld"},
				},
				DockerSpec: &githubv1beta.DockerBaseImageSpec{
					DockerfileContent: "RUN echo custom",
				},
			},
			NetworkPolicy: &githubv1beta.NetworkPolicy{
				Egress:                githubv1beta.NetworkPolicy_EGRESS_POLICY_DOMAIN_ALLOW_LIST,
				EgressDomainAllowList: []string{"github.com", "*.npmjs.org"},
				Advisory:              true,
				EgressPolicyTag:       "",
			},
		},
		Status: &githubv1beta.RunnerProfileStatus{
			CustomRunnerImage: []*githubv1beta.CustomRunnerImage{
				{
					ImageStatus: &githubv1beta.CustomRunnerImageStatus{
						ImageRef:       "ncr.io/workspace/custom:v1",
						Status:         githubv1beta.CustomRunnerImageStatus_READY,
						StartedBuildAt: now,
						BuiltAt:        now,
						BuildRef:       "bld_123",
					},
				},
			},
		},
	}
}

func sampleFullModel() github.RunnerProfileResourceModel {
	aptList, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("clang"),
		types.StringValue("lld"),
	})
	expList, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("nested-virtualization"),
	})
	branchList, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("main"),
		types.StringValue("release/*"),
	})
	domainList, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("github.com"),
		types.StringValue("*.npmjs.org"),
	})

	selObj, _ := types.ObjectValue(github.LabelAttrTypes, map[string]attr.Value{
		"name":  types.StringValue("purpose"),
		"value": types.StringValue("build"),
	})
	selectorsList, _ := types.ListValue(github.LabelObjectType, []attr.Value{selObj})

	shapeObj, _ := types.ObjectValue(github.InstanceShapeAttrTypes, map[string]attr.Value{
		"virtual_cpu":      types.Int64Value(8),
		"memory_megabytes": types.Int64Value(32768),
		"machine_arch":     types.StringValue("amd64"),
		"os":               types.StringValue("linux"),
		"selectors":        selectorsList,
	})

	cvObj, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
		"volume_tag":                   types.StringValue("build-cache"),
		"volume_size_gb":               types.Int64Value(100),
		"volume_reserved_size_gb":      types.Int64Value(10),
		"bundled_container_cache":      types.BoolValue(true),
		"bundled_buildkit_cache":       types.BoolValue(true),
		"bundled_git_mirror":           types.BoolValue(true),
		"bundled_runner_tools_cache":   types.BoolValue(true),
		"bundled_runner_actions_cache": types.BoolValue(true),
		"allow_commit_from_branch":     branchList,
	})
	cvList, _ := types.ListValue(github.CacheVolumeSettingObjectType, []attr.Value{cvObj})

	uSpecObj, _ := types.ObjectValue(github.UbuntuSpecAttrTypes, map[string]attr.Value{
		"apt_package": aptList,
	})
	dSpecObj, _ := types.ObjectValue(github.DockerSpecAttrTypes, map[string]attr.Value{
		"dockerfile_content": types.StringValue("RUN echo custom"),
	})
	customSpecObj, _ := types.ObjectValue(github.CustomRunnerImageSpecAttrTypes, map[string]attr.Value{
		"ubuntu_spec": uSpecObj,
		"docker_spec": dSpecObj,
	})

	npObj, _ := types.ObjectValue(github.NetworkPolicyAttrTypes, map[string]attr.Value{
		"egress":                   types.StringValue("EGRESS_POLICY_DOMAIN_ALLOW_LIST"),
		"egress_domain_allow_list": domainList,
		"advisory":                 types.BoolValue(true),
		"egress_policy_tag":        types.StringValue(""),
	})

	return github.RunnerProfileResourceModel{
		ID:                    types.StringValue("prof_123"),
		ProfileID:             types.StringValue("prof_123"),
		Version:               types.Int64Value(1),
		CreatedAt:             types.StringValue("2026-08-22T10:00:00Z"),
		UpdatedAt:             types.StringValue("2026-08-22T10:00:00Z"),
		Tag:                   types.StringValue("ubuntu-24-04-large"),
		Description:           types.StringValue("High performance builder"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Emoji:                 types.StringValue("🚀"),
		SwapSizeMB:            types.Int64Value(2048),
		BuilderMode:           types.StringValue("USE_REMOTE_BUILDER"),
		AccessLevel:           types.StringValue("ACCESS_LEVEL_PERMISSIVE"),
		ExperimentalFeatures:  expList,
		InstanceShape:         shapeObj,
		CacheVolumeSettings:   cvList,
		CustomRunnerImageSpec: customSpecObj,
		NetworkPolicy:         npObj,
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}
}

func TestRunnerProfileResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := github.NewRunnerProfileResource()

	var metaResp resource.MetadataResponse
	r.Metadata(ctx, resource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_github_runner_profile" {
		t.Fatalf("expected type name 'namespace_github_runner_profile', got %q", metaResp.TypeName)
	}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{
		"id", "profile_id", "version", "created_at", "updated_at", "tag", "os",
		"description", "emoji", "swap_size_mb", "builder_mode", "access_level",
		"experimental_features", "instance_shape", "cache_volume_settings",
		"custom_runner_image_spec", "network_policy", "custom_runner_images",
	}

	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in schema", name)
		}
	}
}

func TestRunnerProfileResource_ConfigValidators(t *testing.T) {
	ctx := context.Background()
	r := github.NewRunnerProfileResource()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	valR, ok := r.(resource.ResourceWithConfigValidators)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithConfigValidators")
	}
	validators := valR.ConfigValidators(ctx)
	if len(validators) == 0 {
		t.Fatal("expected config validators")
	}

	// 1. Invalid: Empty custom_runner_image_spec (neither ubuntu nor docker)
	model := sampleFullModel()
	emptyCustomSpec, _ := types.ObjectValue(github.CustomRunnerImageSpecAttrTypes, map[string]attr.Value{
		"ubuntu_spec": types.ObjectNull(github.UbuntuSpecAttrTypes),
		"docker_spec": types.ObjectNull(github.DockerSpecAttrTypes),
	})
	model.CustomRunnerImageSpec = emptyCustomSpec
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &model)

	var resp resource.ValidateConfigResponse
	validators[0].ValidateResource(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: state.Raw},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for empty custom_runner_image_spec, got none")
	}

	// 2. Invalid: volume_reserved_size_gb >= volume_size_gb
	model = sampleFullModel()
	badCVObj, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
		"volume_tag":                   types.StringValue("build-cache"),
		"volume_size_gb":               types.Int64Value(50),
		"volume_reserved_size_gb":      types.Int64Value(50),
		"bundled_container_cache":      types.BoolValue(true),
		"bundled_buildkit_cache":       types.BoolValue(true),
		"bundled_git_mirror":           types.BoolValue(true),
		"bundled_runner_tools_cache":   types.BoolValue(true),
		"bundled_runner_actions_cache": types.BoolValue(true),
		"allow_commit_from_branch":     types.ListNull(types.StringType),
	})
	badCVList, _ := types.ListValue(github.CacheVolumeSettingObjectType, []attr.Value{badCVObj})
	model.CacheVolumeSettings = badCVList
	state = tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &model)

	resp = resource.ValidateConfigResponse{}
	validators[0].ValidateResource(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: state.Raw},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for volume_reserved_size_gb >= volume_size_gb, got none")
	}

	// 3. Invalid: EGRESS_POLICY_DOMAIN_ALLOW_LIST with empty allowlist
	model = sampleFullModel()
	emptyList, _ := types.ListValue(types.StringType, []attr.Value{})
	badNPObj, _ := types.ObjectValue(github.NetworkPolicyAttrTypes, map[string]attr.Value{
		"egress":                   types.StringValue("EGRESS_POLICY_DOMAIN_ALLOW_LIST"),
		"egress_domain_allow_list": emptyList,
		"advisory":                 types.BoolValue(true),
		"egress_policy_tag":        types.StringValue(""),
	})
	model.NetworkPolicy = badNPObj
	state = tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &model)

	resp = resource.ValidateConfigResponse{}
	validators[0].ValidateResource(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: state.Raw},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for EGRESS_POLICY_DOMAIN_ALLOW_LIST with empty domains, got none")
	}

	// 4. Invalid: advisory = true with EGRESS_POLICY_NONE
	model = sampleFullModel()
	badNPObj2, _ := types.ObjectValue(github.NetworkPolicyAttrTypes, map[string]attr.Value{
		"egress":                   types.StringValue("EGRESS_POLICY_NONE"),
		"egress_domain_allow_list": emptyList,
		"advisory":                 types.BoolValue(true),
		"egress_policy_tag":        types.StringValue(""),
	})
	model.NetworkPolicy = badNPObj2
	state = tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &model)

	resp = resource.ValidateConfigResponse{}
	validators[0].ValidateResource(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: state.Raw},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for advisory = true with EGRESS_POLICY_NONE, got none")
	}
}

func TestRunnerProfileResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := github.NewRunnerProfileResource()

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

func TestRunnerProfileResource_Create_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedReq *githubv1beta.CreateProfileRequest
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		capturedReq = req
		return &githubv1beta.CreateProfileResponse{
			Profile: sampleFullProfile("prof_new_123", 1),
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	planModel := sampleFullModel()
	planModel.ID = types.StringUnknown()
	planModel.ProfileID = types.StringUnknown()
	planModel.Version = types.Int64Unknown()
	planModel.CreatedAt = types.StringUnknown()
	planModel.UpdatedAt = types.StringUnknown()

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

	if capturedReq == nil || capturedReq.Spec == nil {
		t.Fatal("expected CreateProfile gRPC request to be called with non-nil spec")
	}

	if capturedReq.Spec.Tag != "ubuntu-24-04-large" {
		t.Errorf("expected tag 'ubuntu-24-04-large', got %q", capturedReq.Spec.Tag)
	}
	if capturedReq.Spec.Os != "ubuntu-24.04" {
		t.Errorf("expected os 'ubuntu-24.04', got %q", capturedReq.Spec.Os)
	}
	if capturedReq.Spec.InstanceShape.VirtualCpu != 8 {
		t.Errorf("expected vcpu 8, got %d", capturedReq.Spec.InstanceShape.VirtualCpu)
	}

	var stateResult github.RunnerProfileResourceModel
	diags = resp.State.Get(ctx, &stateResult)
	if diags.HasError() {
		t.Fatalf("failed to get state result: %v", diags)
	}

	if stateResult.ID.ValueString() != "prof_new_123" {
		t.Errorf("expected id 'prof_new_123', got %q", stateResult.ID.ValueString())
	}
	if stateResult.Version.ValueInt64() != 1 {
		t.Errorf("expected version 1, got %d", stateResult.Version.ValueInt64())
	}
}

func TestRunnerProfileResource_Create_WithUnknownNestedBlocks(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedReq *githubv1beta.CreateProfileRequest
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		capturedReq = req
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_sparse_1",
				Version:   1,
				CreatedAt: timestamppb.Now(),
				UpdatedAt: timestamppb.Now(),
				Spec: &githubv1beta.RunnerProfileSpec{
					Tag: "sparse-tag",
					Os:  "ubuntu-24.04",
				},
			},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	// Simulate plan where all optional/computed blocks are unknown (as produced by Terraform plan)
	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("sparse-tag"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringUnknown(),
		Emoji:                 types.StringUnknown(),
		SwapSizeMB:            types.Int64Unknown(),
		BuilderMode:           types.StringUnknown(),
		AccessLevel:           types.StringUnknown(),
		ExperimentalFeatures:  types.ListUnknown(types.StringType),
		InstanceShape:         types.ObjectUnknown(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListUnknown(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectUnknown(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectUnknown(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListUnknown(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	diags := planState.Set(ctx, &planModel)
	if diags.HasError() {
		t.Fatalf("failed to set plan state with unknown blocks: %v", diags)
	}

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
	}
	resp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create failed with unknown nested blocks (Value Conversion Error regression): %v", resp.Diagnostics)
	}

	if capturedReq == nil || capturedReq.Spec == nil {
		t.Fatal("expected CreateProfile to be invoked")
	}
	if capturedReq.Spec.Tag != "sparse-tag" || capturedReq.Spec.Os != "ubuntu-24.04" {
		t.Errorf("unexpected spec in request: %+v", capturedReq.Spec)
	}

	var resultState github.RunnerProfileResourceModel
	diags = resp.State.Get(ctx, &resultState)
	if diags.HasError() {
		t.Fatalf("failed to read state after create: %v", diags)
	}
	if resultState.ID.ValueString() != "prof_sparse_1" {
		t.Errorf("expected ID 'prof_sparse_1', got %q", resultState.ID.ValueString())
	}
}

func TestRunnerProfileResource_Read_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	profile := sampleFullProfile("prof_read_1", 2)
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{profile},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	stateModel := sampleFullModel()
	stateModel.ID = types.StringValue("prof_read_1")
	stateModel.ProfileID = types.StringValue("prof_read_1")
	stateModel.Version = types.Int64Value(1)

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.ReadRequest{State: state}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.RunnerProfileResourceModel
	diags := resp.State.Get(ctx, &stateResult)
	if diags.HasError() {
		t.Fatalf("failed to read state: %v", diags)
	}

	if stateResult.Version.ValueInt64() != 2 {
		t.Errorf("expected refreshed version 2, got %d", stateResult.Version.ValueInt64())
	}
}

func TestRunnerProfileResource_Read_NotFound_RemovesFromState(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	stateModel := sampleFullModel()
	stateModel.ID = types.StringValue("prof_deleted")
	stateModel.ProfileID = types.StringValue("prof_deleted")

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.ReadRequest{State: state}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Fatal("expected state to be removed (null) after profile is not found on server")
	}
}

func TestRunnerProfileResource_Update_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedReq *githubv1beta.UpdateProfileRequest
	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		capturedReq = req
		return &githubv1beta.UpdateProfileResponse{
			Profile: sampleFullProfile("prof_update_1", 3),
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	planModel := sampleFullModel()
	planModel.ID = types.StringValue("prof_update_1")
	planModel.ProfileID = types.StringValue("prof_update_1")
	planModel.Version = types.Int64Unknown()
	planModel.Description = types.StringValue("Updated description")

	stateModel := sampleFullModel()
	stateModel.ID = types.StringValue("prof_update_1")
	stateModel.ProfileID = types.StringValue("prof_update_1")
	stateModel.Version = types.Int64Value(2)

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

	if capturedReq == nil {
		t.Fatal("expected UpdateProfile gRPC call, got none")
	}

	if capturedReq.ProfileId != "prof_update_1" {
		t.Errorf("expected ProfileId 'prof_update_1', got %q", capturedReq.ProfileId)
	}
	if capturedReq.UpdateVersion != 2 {
		t.Errorf("expected UpdateVersion 2, got %d", capturedReq.UpdateVersion)
	}

	var stateResult github.RunnerProfileResourceModel
	_ = resp.State.Get(ctx, &stateResult)
	if stateResult.Version.ValueInt64() != 3 {
		t.Errorf("expected updated version 3, got %d", stateResult.Version.ValueInt64())
	}
}

func TestRunnerProfileResource_Update_ConcurrencyConflict(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, "version mismatch: remote profile has been updated")
	}

	r, schemaResp := setupTestResource(t, srv)

	planModel := sampleFullModel()
	planModel.ID = types.StringValue("prof_conflict_1")
	planModel.ProfileID = types.StringValue("prof_conflict_1")

	stateModel := sampleFullModel()
	stateModel.ID = types.StringValue("prof_conflict_1")
	stateModel.ProfileID = types.StringValue("prof_conflict_1")
	stateModel.Version = types.Int64Value(2)

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
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error on concurrency conflict, got none")
	}

	foundConflictMsg := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Concurrency Conflict") || strings.Contains(d.Detail(), "re-plan") {
			foundConflictMsg = true
			break
		}
	}
	if !foundConflictMsg {
		t.Errorf("expected concurrency conflict diagnostic message, got: %v", resp.Diagnostics)
	}
}

func TestRunnerProfileResource_Delete_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedReq *githubv1beta.DeleteProfileRequest
	srv.Profile.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
		capturedReq = req
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	stateModel := sampleFullModel()
	stateModel.ID = types.StringValue("prof_delete_1")
	stateModel.ProfileID = types.StringValue("prof_delete_1")

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete errors: %v", resp.Diagnostics)
	}

	if capturedReq == nil || capturedReq.ProfileId != "prof_delete_1" {
		t.Fatalf("unexpected DeleteProfile request: %+v", capturedReq)
	}
}

func TestRunnerProfileResource_ImportState_ByID(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	profile := sampleFullProfile("prof_import_1", 2)
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{profile},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	req := resource.ImportStateRequest{ID: "prof_import_1"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	if impR, ok := r.(resource.ResourceWithImportState); ok {
		impR.ImportState(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected import errors: %v", resp.Diagnostics)
		}

		var stateResult github.RunnerProfileResourceModel
		resp.State.Get(ctx, &stateResult)
		if stateResult.ID.ValueString() != "prof_import_1" {
			t.Errorf("expected imported id 'prof_import_1', got %q", stateResult.ID.ValueString())
		}
	} else {
		t.Fatal("resource does not implement ResourceWithImportState")
	}
}

func TestRunnerProfileResource_ImportState_ByTag(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	profile := sampleFullProfile("prof_import_2", 3)
	profile.Spec.Tag = "custom-tag-name"

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{profile},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	req := resource.ImportStateRequest{ID: "custom-tag-name"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	if impR, ok := r.(resource.ResourceWithImportState); ok {
		impR.ImportState(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected import errors: %v", resp.Diagnostics)
		}

		var stateResult github.RunnerProfileResourceModel
		resp.State.Get(ctx, &stateResult)
		if stateResult.ID.ValueString() != "prof_import_2" {
			t.Errorf("expected imported id 'prof_import_2', got %q", stateResult.ID.ValueString())
		}
	} else {
		t.Fatal("resource does not implement ResourceWithImportState")
	}
}

func TestRunnerProfileResource_ImportState_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	req := resource.ImportStateRequest{ID: "nonexistent"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	if impR, ok := r.(resource.ResourceWithImportState); ok {
		impR.ImportState(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error on importing nonexistent profile")
		}
	}
}

func TestRunnerProfileResource_ListOrderPreservationAndDeterminism(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedCreateReq *githubv1beta.CreateProfileRequest

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		capturedCreateReq = req
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_order_test",
				Version:   1,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.Spec,
			},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	expFeaturesList, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("feat-z"),
		types.StringValue("feat-a"),
		types.StringValue("feat-m"),
	})

	branchList, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("main"),
		types.StringValue("develop"),
	})

	sel1, _ := types.ObjectValue(github.LabelAttrTypes, map[string]attr.Value{
		"name":  types.StringValue("zone"),
		"value": types.StringValue("us-east-1"),
	})
	sel2, _ := types.ObjectValue(github.LabelAttrTypes, map[string]attr.Value{
		"name":  types.StringValue("alpha"),
		"value": types.StringValue("true"),
	})
	selectorsList, _ := types.ListValue(github.LabelObjectType, []attr.Value{sel1, sel2})

	shapeObj, _ := types.ObjectValue(github.InstanceShapeAttrTypes, map[string]attr.Value{
		"virtual_cpu":      types.Int64Value(4),
		"memory_megabytes": types.Int64Value(8192),
		"machine_arch":     types.StringValue("amd64"),
		"os":               types.StringValue("linux"),
		"selectors":        selectorsList,
	})

	cvObj, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
		"volume_tag":                   types.StringValue("cache-1"),
		"volume_size_gb":               types.Int64Value(20),
		"volume_reserved_size_gb":      types.Int64Value(0),
		"bundled_container_cache":      types.BoolValue(false),
		"bundled_buildkit_cache":       types.BoolValue(false),
		"bundled_git_mirror":           types.BoolValue(false),
		"bundled_runner_tools_cache":   types.BoolValue(false),
		"bundled_runner_actions_cache": types.BoolValue(false),
		"allow_commit_from_branch":     branchList,
	})
	cvList, _ := types.ListValue(github.CacheVolumeSettingObjectType, []attr.Value{cvObj})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringNull(),
		ProfileID:             types.StringNull(),
		Version:               types.Int64Null(),
		CreatedAt:             types.StringNull(),
		UpdatedAt:             types.StringNull(),
		Tag:                   types.StringValue("order-test"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  expFeaturesList,
		InstanceShape:         shapeObj,
		CacheVolumeSettings:   cvList,
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected create errors: %v", resp.Diagnostics)
	}

	// Verify request order preserved
	if capturedCreateReq == nil || len(capturedCreateReq.Spec.ExperimentalFeatures) != 3 {
		t.Fatalf("unexpected experimental features in create req: %v", capturedCreateReq)
	}
	if capturedCreateReq.Spec.ExperimentalFeatures[0] != "feat-z" ||
		capturedCreateReq.Spec.ExperimentalFeatures[1] != "feat-a" ||
		capturedCreateReq.Spec.ExperimentalFeatures[2] != "feat-m" {
		t.Errorf("experimental features order mutated: %v", capturedCreateReq.Spec.ExperimentalFeatures)
	}

	if len(capturedCreateReq.Spec.InstanceShape.Selectors) != 2 {
		t.Fatalf("unexpected selectors in create req: %v", capturedCreateReq.Spec.InstanceShape.Selectors)
	}
	if capturedCreateReq.Spec.InstanceShape.Selectors[0].Name != "zone" ||
		capturedCreateReq.Spec.InstanceShape.Selectors[1].Name != "alpha" {
		t.Errorf("selectors order mutated in request: %v", capturedCreateReq.Spec.InstanceShape.Selectors)
	}

	var stateResult github.RunnerProfileResourceModel
	_ = resp.State.Get(ctx, &stateResult)

	// Verify state order preserved for selectors
	var shapeRes github.InstanceShapeModel
	_ = stateResult.InstanceShape.As(ctx, &shapeRes, basetypes.ObjectAsOptions{})
	var selectorsRes []github.LabelModel
	_ = shapeRes.Selectors.ElementsAs(ctx, &selectorsRes, false)

	if len(selectorsRes) != 2 {
		t.Fatalf("unexpected selectors in state: %v", selectorsRes)
	}
	if selectorsRes[0].Name.ValueString() != "zone" ||
		selectorsRes[1].Name.ValueString() != "alpha" {
		t.Errorf("selectors order mutated in state: %v", selectorsRes)
	}

	// Verify cache volume allow_commit_from_branch preserves configuration ordering: ["main", "develop"]
	var cvSettings []github.CacheVolumeSettingModel
	_ = stateResult.CacheVolumeSettings.ElementsAs(ctx, &cvSettings, false)
	var branches []string
	_ = cvSettings[0].AllowCommitFromBranch.ElementsAs(ctx, &branches, false)
	if len(branches) != 2 || branches[0] != "main" || branches[1] != "develop" {
		t.Errorf("expected preserved branches ['main', 'develop'], got %v", branches)
	}
}

func TestRunnerProfileResource_EmptyListsPreservation(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var storedProfile *githubv1beta.RunnerProfileWithStatus

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		storedProfile = &githubv1beta.RunnerProfileWithStatus{
			ProfileId: "prof_empty_lists",
			Version:   1,
			CreatedAt: timestamppb.Now(),
			UpdatedAt: timestamppb.Now(),
			Spec:      req.Spec,
		}
		return &githubv1beta.CreateProfileResponse{
			Profile: storedProfile,
		}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		if storedProfile == nil {
			return &githubv1beta.ListProfilesResponse{}, nil
		}
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{storedProfile},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	emptyStrList, _ := types.ListValue(types.StringType, []attr.Value{})
	emptySelList, _ := types.ListValue(github.LabelObjectType, []attr.Value{})

	shapeObj, _ := types.ObjectValue(github.InstanceShapeAttrTypes, map[string]attr.Value{
		"virtual_cpu":      types.Int64Value(4),
		"memory_megabytes": types.Int64Value(8192),
		"machine_arch":     types.StringValue("amd64"),
		"os":               types.StringValue("linux"),
		"selectors":        emptySelList,
	})

	cvObj, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
		"volume_tag":                   types.StringValue("empty-branch-cv"),
		"volume_size_gb":               types.Int64Value(50),
		"volume_reserved_size_gb":      types.Int64Value(0),
		"bundled_container_cache":      types.BoolValue(false),
		"bundled_buildkit_cache":       types.BoolValue(false),
		"bundled_git_mirror":           types.BoolValue(false),
		"bundled_runner_tools_cache":   types.BoolValue(false),
		"bundled_runner_actions_cache": types.BoolValue(false),
		"allow_commit_from_branch":     emptyStrList,
	})
	cvList, _ := types.ListValue(github.CacheVolumeSettingObjectType, []attr.Value{cvObj})

	uSpecObj, _ := types.ObjectValue(github.UbuntuSpecAttrTypes, map[string]attr.Value{
		"apt_package": emptyStrList,
	})
	customSpecObj, _ := types.ObjectValue(github.CustomRunnerImageSpecAttrTypes, map[string]attr.Value{
		"ubuntu_spec": uSpecObj,
		"docker_spec": types.ObjectNull(github.DockerSpecAttrTypes),
	})

	npObj, _ := types.ObjectValue(github.NetworkPolicyAttrTypes, map[string]attr.Value{
		"egress":                   types.StringValue("EGRESS_POLICY_DOMAIN_ALLOW_LIST"),
		"egress_domain_allow_list": emptyStrList,
		"advisory":                 types.BoolValue(false),
		"egress_policy_tag":        types.StringValue(""),
	})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringNull(),
		ProfileID:             types.StringNull(),
		Version:               types.Int64Null(),
		CreatedAt:             types.StringNull(),
		UpdatedAt:             types.StringNull(),
		Tag:                   types.StringValue("empty-lists-tag"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  emptyStrList,
		InstanceShape:         shapeObj,
		CacheVolumeSettings:   cvList,
		CustomRunnerImageSpec: customSpecObj,
		NetworkPolicy:         npObj,
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected create errors: %v", resp.Diagnostics)
	}

	var stateResult github.RunnerProfileResourceModel
	_ = resp.State.Get(ctx, &stateResult)

	// Verify experimental_features is not null, but empty list
	if stateResult.ExperimentalFeatures.IsNull() {
		t.Errorf("expected ExperimentalFeatures to be non-null empty list")
	} else if len(stateResult.ExperimentalFeatures.Elements()) != 0 {
		t.Errorf("expected ExperimentalFeatures elements count 0, got %d", len(stateResult.ExperimentalFeatures.Elements()))
	}

	// Verify instance_shape.selectors is not null, but empty list
	var shapeRes github.InstanceShapeModel
	_ = stateResult.InstanceShape.As(ctx, &shapeRes, basetypes.ObjectAsOptions{})
	if shapeRes.Selectors.IsNull() {
		t.Errorf("expected shape selectors to be non-null empty list")
	} else if len(shapeRes.Selectors.Elements()) != 0 {
		t.Errorf("expected shape selectors count 0, got %d", len(shapeRes.Selectors.Elements()))
	}

	// Verify allow_commit_from_branch is not null, but empty list
	var cvSettingsRes []github.CacheVolumeSettingModel
	_ = stateResult.CacheVolumeSettings.ElementsAs(ctx, &cvSettingsRes, false)
	if cvSettingsRes[0].AllowCommitFromBranch.IsNull() {
		t.Errorf("expected allow_commit_from_branch to be non-null empty list")
	} else if len(cvSettingsRes[0].AllowCommitFromBranch.Elements()) != 0 {
		t.Errorf("expected allow_commit_from_branch count 0, got %d", len(cvSettingsRes[0].AllowCommitFromBranch.Elements()))
	}

	// Verify custom_runner_image_spec.ubuntu_spec.apt_package is not null, but empty list
	var customRes github.CustomRunnerImageSpecModel
	_ = stateResult.CustomRunnerImageSpec.As(ctx, &customRes, basetypes.ObjectAsOptions{})
	var uRes github.UbuntuSpecModel
	_ = customRes.UbuntuSpec.As(ctx, &uRes, basetypes.ObjectAsOptions{})
	if uRes.AptPackage.IsNull() {
		t.Errorf("expected apt_package to be non-null empty list")
	} else if len(uRes.AptPackage.Elements()) != 0 {
		t.Errorf("expected apt_package count 0, got %d", len(uRes.AptPackage.Elements()))
	}

	// Verify network_policy.egress_domain_allow_list is not null, but empty list
	var npRes github.NetworkPolicyModel
	_ = stateResult.NetworkPolicy.As(ctx, &npRes, basetypes.ObjectAsOptions{})
	if npRes.EgressDomainAllowList.IsNull() {
		t.Errorf("expected egress_domain_allow_list to be non-null empty list")
	} else if len(npRes.EgressDomainAllowList.Elements()) != 0 {
		t.Errorf("expected egress_domain_allow_list count 0, got %d", len(npRes.EgressDomainAllowList.Elements()))
	}
}
