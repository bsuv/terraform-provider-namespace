package github_test

import (
	"context"
	"strings"
	"testing"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
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
		CustomPermissions:     types.ListNull(github.CustomPermissionObjectType),
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
		"custom_permissions", "experimental_features", "instance_shape", "cache_volume_settings",
		"custom_runner_image_spec", "network_policy", "custom_runner_images",
	}

	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in schema", name)
		}
	}

	if schemaResp.Schema.Version != 1 {
		t.Errorf("expected schema version 1, got %d", schemaResp.Schema.Version)
	}
	if _, ok := r.(resource.ResourceWithUpgradeState); !ok {
		t.Error("expected runnerProfileResource to implement resource.ResourceWithUpgradeState")
	}

	cvAttr := attrs["cache_volume_settings"].(rschema.ListNestedAttribute)
	if _, hasVolTag := cvAttr.NestedObject.Attributes["volume_tag"]; hasVolTag {
		t.Error("cache_volume_settings schema must not contain removed attribute 'volume_tag'")
	}

	shapeAttr := attrs["instance_shape"].(rschema.SingleNestedAttribute)
	osAttr := shapeAttr.Attributes["os"].(rschema.StringAttribute)
	if !strings.Contains(strings.ToLower(osAttr.Description), "windows") {
		t.Errorf("expected instance_shape.os description to include 'windows', got %q", osAttr.Description)
	}

	alAttr := attrs["access_level"].(rschema.StringAttribute)
	if !strings.Contains(alAttr.Description, "ACCESS_LEVEL_CUSTOM") {
		t.Errorf("expected access_level description to include 'ACCESS_LEVEL_CUSTOM', got %q", alAttr.Description)
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
		CustomPermissions:     types.ListUnknown(github.CustomPermissionObjectType),
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
		CustomPermissions:     types.ListNull(github.CustomPermissionObjectType),
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
		CustomPermissions:     types.ListNull(github.CustomPermissionObjectType),
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

func TestRunnerProfileResource_UpgradeState_V0ToV1(t *testing.T) {
	ctx := context.Background()
	r := github.NewRunnerProfileResource()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	upgR, ok := r.(resource.ResourceWithUpgradeState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithUpgradeState")
	}
	upgraders := upgR.UpgradeState(ctx)
	upgrader, exists := upgraders[0]
	if !exists || upgrader.PriorSchema == nil {
		t.Fatal("expected version 0 StateUpgrader with non-nil PriorSchema")
	}

	priorType := upgrader.PriorSchema.Type().TerraformType(ctx)

	upgradeJSON := func(t *testing.T, rawJSON string) github.RunnerProfileResourceModel {
		t.Helper()
		rawVal, err := (&tfprotov6.RawState{JSON: []byte(rawJSON)}).Unmarshal(priorType)
		if err != nil {
			t.Fatalf("RawState.Unmarshal failed: %v", err)
		}
		req := resource.UpgradeStateRequest{
			State: &tfsdk.State{
				Schema: *upgrader.PriorSchema,
				Raw:    rawVal,
			},
		}
		resp := &resource.UpgradeStateResponse{
			State: tfsdk.State{Schema: schemaResp.Schema},
		}
		upgrader.StateUpgrader(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("UpgradeState returned errors: %v", resp.Diagnostics)
		}
		var out github.RunnerProfileResourceModel
		diags := resp.State.Get(ctx, &out)
		if diags.HasError() {
			t.Fatalf("failed to decode upgraded v1 state: %v", diags)
		}
		return out
	}

	t.Run("populated multi-item cache_volume_settings strips volume_tag and preserves fields", func(t *testing.T) {
		v0JSON := `{
			"id": "prof_v0_1",
			"profile_id": "prof_v0_1",
			"version": 3,
			"created_at": "2026-08-22T10:00:00Z",
			"updated_at": "2026-08-22T11:00:00Z",
			"tag": "legacy-runner",
			"os": "ubuntu-24.04",
			"description": "Legacy v0 profile",
			"emoji": "🚀",
			"swap_size_mb": 2048,
			"builder_mode": "USE_REMOTE_BUILDER",
			"access_level": "ACCESS_LEVEL_PERMISSIVE",
			"experimental_features": ["nested-virt"],
			"instance_shape": {
				"virtual_cpu": 4,
				"memory_megabytes": 8192,
				"machine_arch": "amd64",
				"os": "linux",
				"selectors": [{"name": "tier", "value": "fast"}]
			},
			"cache_volume_settings": [
				{
					"volume_tag": "vol-a",
					"volume_size_gb": 100,
					"volume_reserved_size_gb": 10,
					"bundled_container_cache": true,
					"bundled_buildkit_cache": true,
					"bundled_git_mirror": true,
					"bundled_runner_tools_cache": true,
					"bundled_runner_actions_cache": true,
					"allow_commit_from_branch": ["main", "release/*"]
				},
				{
					"volume_tag": "vol-b",
					"volume_size_gb": 50,
					"volume_reserved_size_gb": 5,
					"bundled_container_cache": false,
					"bundled_buildkit_cache": false,
					"bundled_git_mirror": false,
					"bundled_runner_tools_cache": false,
					"bundled_runner_actions_cache": false,
					"allow_commit_from_branch": []
				},
				{
					"volume_tag": "vol-c",
					"volume_size_gb": 25,
					"volume_reserved_size_gb": 0,
					"bundled_container_cache": false,
					"bundled_buildkit_cache": false,
					"bundled_git_mirror": false,
					"bundled_runner_tools_cache": false,
					"bundled_runner_actions_cache": false,
					"allow_commit_from_branch": null
				}
			],
			"custom_runner_image_spec": null,
			"network_policy": null,
			"custom_runner_images": null
		}`

		out := upgradeJSON(t, v0JSON)
		if out.ID.ValueString() != "prof_v0_1" || out.Version.ValueInt64() != 3 || out.Tag.ValueString() != "legacy-runner" {
			t.Fatalf("unexpected top-level upgraded fields: id=%q version=%d tag=%q", out.ID.ValueString(), out.Version.ValueInt64(), out.Tag.ValueString())
		}
		if !out.CustomPermissions.IsNull() {
			t.Errorf("expected custom_permissions to be null after v0->v1 upgrade, got %v", out.CustomPermissions)
		}
		if out.CacheVolumeSettings.IsNull() || out.CacheVolumeSettings.IsUnknown() {
			t.Fatal("expected populated cache_volume_settings")
		}
		var cvs []github.CacheVolumeSettingModel
		if d := out.CacheVolumeSettings.ElementsAs(ctx, &cvs, false); d.HasError() {
			t.Fatalf("failed to decode upgraded cache_volume_settings: %v", d)
		}
		if len(cvs) != 3 {
			t.Fatalf("expected 3 cache_volume_settings items, got %d", len(cvs))
		}
		if cvs[0].VolumeSizeGB.ValueInt64() != 100 || !cvs[0].BundledContainerCache.ValueBool() {
			t.Errorf("item 0 fields mismatch: %+v", cvs[0])
		}
		var b0 []string
		_ = cvs[0].AllowCommitFromBranch.ElementsAs(ctx, &b0, false)
		if len(b0) != 2 || b0[0] != "main" || b0[1] != "release/*" {
			t.Errorf("item 0 branches mismatch: %v", b0)
		}
		if cvs[1].VolumeReservedSizeGB.ValueInt64() != 5 || cvs[1].AllowCommitFromBranch.IsNull() || len(cvs[1].AllowCommitFromBranch.Elements()) != 0 {
			t.Errorf("item 1 expected reserved=5 and empty non-null branches, got %+v", cvs[1])
		}
		if cvs[2].VolumeSizeGB.ValueInt64() != 25 || !cvs[2].AllowCommitFromBranch.IsNull() {
			t.Errorf("item 2 expected size=25 and null branches, got %+v", cvs[2])
		}
	})

	t.Run("cache_volume_settings null", func(t *testing.T) {
		v0JSON := `{
			"id": "prof_v0_null",
			"profile_id": "prof_v0_null",
			"version": 1,
			"created_at": "2026-08-22T10:00:00Z",
			"updated_at": "2026-08-22T10:00:00Z",
			"tag": "null-cv",
			"os": "ubuntu-24.04",
			"description": null,
			"emoji": null,
			"swap_size_mb": null,
			"builder_mode": null,
			"access_level": null,
			"experimental_features": null,
			"instance_shape": null,
			"cache_volume_settings": null,
			"custom_runner_image_spec": null,
			"network_policy": null,
			"custom_runner_images": null
		}`
		out := upgradeJSON(t, v0JSON)
		if !out.CacheVolumeSettings.IsNull() {
			t.Errorf("expected null cache_volume_settings to remain null, got %v", out.CacheVolumeSettings)
		}
		if !out.CustomPermissions.IsNull() {
			t.Errorf("expected custom_permissions to be null, got %v", out.CustomPermissions)
		}
	})

	t.Run("cache_volume_settings explicit empty list", func(t *testing.T) {
		v0JSON := `{
			"id": "prof_v0_empty",
			"profile_id": "prof_v0_empty",
			"version": 1,
			"created_at": "2026-08-22T10:00:00Z",
			"updated_at": "2026-08-22T10:00:00Z",
			"tag": "empty-cv",
			"os": "ubuntu-24.04",
			"description": null,
			"emoji": null,
			"swap_size_mb": 0,
			"builder_mode": null,
			"access_level": null,
			"experimental_features": null,
			"instance_shape": null,
			"cache_volume_settings": [],
			"custom_runner_image_spec": null,
			"network_policy": null,
			"custom_runner_images": null
		}`
		out := upgradeJSON(t, v0JSON)
		if out.CacheVolumeSettings.IsNull() || len(out.CacheVolumeSettings.Elements()) != 0 {
			t.Errorf("expected empty non-null cache_volume_settings list, got %v", out.CacheVolumeSettings)
		}
	})
}

func TestRunnerProfileResource_PositionalCacheVolumeBranchReconciliation(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Server simulates protobuf wire behavior: omits empty AllowCommitFromBranch (nil)
	var stored *githubv1beta.RunnerProfileWithStatus
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		now := timestamppb.Now()
		var serverCVs []*githubv1beta.CacheVolumeSettings
		for _, cv := range req.GetSpec().GetCacheVolumeSettings() {
			var branches []string
			if len(cv.GetAllowCommitFromBranch()) > 0 {
				branches = append([]string(nil), cv.GetAllowCommitFromBranch()...)
			}
			serverCVs = append(serverCVs, &githubv1beta.CacheVolumeSettings{
				VolumeSizeGb:          cv.GetVolumeSizeGb(),
				VolumeReservedSizeGb:  cv.GetVolumeReservedSizeGb(),
				AllowCommitFromBranch: branches,
			})
		}
		stored = &githubv1beta.RunnerProfileWithStatus{
			ProfileId: "prof_pos_cv",
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec: &githubv1beta.RunnerProfileSpec{
				Tag:                 req.GetSpec().GetTag(),
				Os:                  req.GetSpec().GetOs(),
				CacheVolumeSettings: serverCVs,
			},
		}
		return &githubv1beta.CreateProfileResponse{Profile: stored}, nil
	}
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{Profiles: []*githubv1beta.RunnerProfileWithStatus{stored}}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	makeCV := func(size int64, branches types.List) attr.Value {
		obj, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
			"volume_size_gb":               types.Int64Value(size),
			"volume_reserved_size_gb":      types.Int64Value(0),
			"bundled_container_cache":      types.BoolValue(false),
			"bundled_buildkit_cache":       types.BoolValue(false),
			"bundled_git_mirror":           types.BoolValue(false),
			"bundled_runner_tools_cache":   types.BoolValue(false),
			"bundled_runner_actions_cache": types.BoolValue(false),
			"allow_commit_from_branch":     branches,
		})
		return obj
	}

	emptyBranches, _ := types.ListValue(types.StringType, []attr.Value{})
	mainBranches, _ := types.ListValue(types.StringType, []attr.Value{types.StringValue("main")})

	cvList, _ := types.ListValue(github.CacheVolumeSettingObjectType, []attr.Value{
		makeCV(10, emptyBranches),
		makeCV(20, types.ListNull(types.StringType)),
		makeCV(30, mainBranches),
	})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("pos-cv"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		CustomPermissions:     types.ListNull(github.CustomPermissionObjectType),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   cvList,
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics)
	}

	var got github.RunnerProfileResourceModel
	_ = createResp.State.Get(ctx, &got)
	var gotCVs []github.CacheVolumeSettingModel
	_ = got.CacheVolumeSettings.ElementsAs(ctx, &gotCVs, false)
	if len(gotCVs) != 3 {
		t.Fatalf("expected 3 cache volumes, got %d", len(gotCVs))
	}
	if gotCVs[0].AllowCommitFromBranch.IsNull() || len(gotCVs[0].AllowCommitFromBranch.Elements()) != 0 {
		t.Errorf("index 0 expected empty non-null list, got %v", gotCVs[0].AllowCommitFromBranch)
	}
	if !gotCVs[1].AllowCommitFromBranch.IsNull() {
		t.Errorf("index 1 expected null list, got %v", gotCVs[1].AllowCommitFromBranch)
	}
	if len(gotCVs[2].AllowCommitFromBranch.Elements()) != 1 {
		t.Errorf("index 2 expected 1 element, got %v", gotCVs[2].AllowCommitFromBranch)
	}

	// Simulate remote out-of-band addition of a 4th item (i >= len(plannedCVModels)) -> no panic, empty flattens to null
	stored.Spec.CacheVolumeSettings = append(stored.Spec.CacheVolumeSettings, &githubv1beta.CacheVolumeSettings{
		VolumeSizeGb:          40,
		AllowCommitFromBranch: nil,
	})
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, resource.ReadRequest{State: createResp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics)
	}
	var readGot github.RunnerProfileResourceModel
	_ = readResp.State.Get(ctx, &readGot)
	var readCVs []github.CacheVolumeSettingModel
	_ = readGot.CacheVolumeSettings.ElementsAs(ctx, &readCVs, false)
	if len(readCVs) != 4 {
		t.Fatalf("expected 4 cache volumes after remote addition, got %d", len(readCVs))
	}
	if !readCVs[3].AllowCommitFromBranch.IsNull() {
		t.Errorf("index 3 (beyond plannedCVModels) expected null branches, got %v", readCVs[3].AllowCommitFromBranch)
	}
}

func TestRunnerProfileResource_AccessLevelCustom_And_CustomPermissions(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	srv.Profile.EnableStatefulProfiles()

	var capturedCreateReq *githubv1beta.CreateProfileRequest
	origCreate := srv.Profile.CreateProfileFunc
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		capturedCreateReq = req
		return origCreate(ctx, req)
	}

	r, schemaResp := setupTestResource(t, srv)

	actionsRead, _ := types.ListValue(types.StringType, []attr.Value{types.StringValue("read"), types.StringValue("list")})
	actionsEmpty, _ := types.ListValue(types.StringType, []attr.Value{})
	cp1, _ := types.ObjectValue(github.CustomPermissionAttrTypes, map[string]attr.Value{
		"resource_type": types.StringValue("vault/object"),
		"resource_id":   types.StringValue("secret-1"),
		"actions":       actionsRead,
	})
	cp2, _ := types.ObjectValue(github.CustomPermissionAttrTypes, map[string]attr.Value{
		"resource_type": types.StringValue("cache/bazel"),
		"resource_id":   types.StringValue(""),
		"actions":       actionsEmpty,
	})
	cpList, _ := types.ListValue(github.CustomPermissionObjectType, []attr.Value{cp1, cp2})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("custom-access"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringValue("ACCESS_LEVEL_CUSTOM"),
		CustomPermissions:     cpList,
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics)
	}

	if capturedCreateReq.GetSpec().GetAccessLevel() != githubv1beta.AccessLevel_ACCESS_LEVEL_CUSTOM {
		t.Fatalf("expected ACCESS_LEVEL_CUSTOM, got %v", capturedCreateReq.GetSpec().GetAccessLevel())
	}
	if len(capturedCreateReq.GetSpec().GetCustomPermissions()) != 2 {
		t.Fatalf("expected 2 custom permissions, got %d", len(capturedCreateReq.GetSpec().GetCustomPermissions()))
	}
	if capturedCreateReq.GetSpec().GetCustomPermissions()[1].GetResourceId() != "" {
		t.Errorf("expected empty resource_id for second permission, got %q", capturedCreateReq.GetSpec().GetCustomPermissions()[1].GetResourceId())
	}

	// Verify state preserves actions = [] on item 2
	var stateAfterCreate github.RunnerProfileResourceModel
	_ = createResp.State.Get(ctx, &stateAfterCreate)
	var stateCPs []github.CustomPermissionModel
	_ = stateAfterCreate.CustomPermissions.ElementsAs(ctx, &stateCPs, false)
	if len(stateCPs) != 2 {
		t.Fatalf("expected 2 custom permissions in state, got %d", len(stateCPs))
	}
	if stateCPs[1].Actions.IsNull() || len(stateCPs[1].Actions.Elements()) != 0 {
		t.Errorf("expected non-null empty actions list on item 2, got %v", stateCPs[1].Actions)
	}

	// Update to ACCESS_LEVEL_RESTRICTED with null custom_permissions
	updateModel := stateAfterCreate
	updateModel.AccessLevel = types.StringValue("ACCESS_LEVEL_RESTRICTED")
	updateModel.CustomPermissions = types.ListNull(github.CustomPermissionObjectType)
	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState.Set(ctx, &updateModel)

	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, resource.UpdateRequest{
		State: createResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
	}, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics)
	}
	var stateAfterUpdate github.RunnerProfileResourceModel
	_ = updateResp.State.Get(ctx, &stateAfterUpdate)
	if stateAfterUpdate.AccessLevel.ValueString() != "ACCESS_LEVEL_RESTRICTED" || !stateAfterUpdate.CustomPermissions.IsNull() {
		t.Errorf("expected ACCESS_LEVEL_RESTRICTED and null custom_permissions, got %q / %v", stateAfterUpdate.AccessLevel.ValueString(), stateAfterUpdate.CustomPermissions)
	}

	// Explicit empty custom_permissions = [] preserved vs null
	emptyCPList, _ := types.ListValue(github.CustomPermissionObjectType, []attr.Value{})
	emptyPlan := stateAfterUpdate
	emptyPlan.AccessLevel = types.StringValue("ACCESS_LEVEL_CUSTOM")
	emptyPlan.CustomPermissions = emptyCPList
	emptyPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = emptyPlanState.Set(ctx, &emptyPlan)
	emptyResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, resource.UpdateRequest{
		State: updateResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: emptyPlanState.Raw},
	}, emptyResp)
	if emptyResp.Diagnostics.HasError() {
		t.Fatalf("Update with empty custom_permissions=[] failed: %v", emptyResp.Diagnostics)
	}
	var stateAfterEmpty github.RunnerProfileResourceModel
	_ = emptyResp.State.Get(ctx, &stateAfterEmpty)
	if stateAfterEmpty.CustomPermissions.IsNull() || len(stateAfterEmpty.CustomPermissions.Elements()) != 0 {
		t.Errorf("expected explicit empty custom_permissions=[], got %v", stateAfterEmpty.CustomPermissions)
	}

	// Schema validator rejects empty resource_type
	cpAttr := schemaResp.Schema.Attributes["custom_permissions"].(rschema.ListNestedAttribute)
	rtAttr := cpAttr.NestedObject.Attributes["resource_type"].(rschema.StringAttribute)
	vResp := validator.StringResponse{}
	for _, v := range rtAttr.Validators {
		v.ValidateString(ctx, validator.StringRequest{
			Path:        path.Root("custom_permissions").AtListIndex(0).AtName("resource_type"),
			ConfigValue: types.StringValue(""),
		}, &vResp)
	}
	if !vResp.Diagnostics.HasError() {
		t.Error("expected validator error for empty custom_permissions.resource_type")
	}
}

func TestRunnerProfileResource_AccessLevelNumeric4AndUnknown(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedSpec *githubv1beta.RunnerProfileSpec
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		capturedSpec = req.GetSpec()
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_al_4",
				Version:   1,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	baseModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("al-4"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringValue("4"),
		CustomPermissions:     types.ListNull(github.CustomPermissionObjectType),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	// "4" maps to ACCESS_LEVEL_CUSTOM, never ACCESS_LEVEL_UNSPECIFIED
	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &baseModel)
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create with '4' failed: %v", createResp.Diagnostics)
	}
	if capturedSpec.GetAccessLevel() != githubv1beta.AccessLevel_ACCESS_LEVEL_CUSTOM {
		t.Fatalf("expected AccessLevel_ACCESS_LEVEL_CUSTOM for '4', got %v", capturedSpec.GetAccessLevel())
	}
	var stateOut github.RunnerProfileResourceModel
	_ = createResp.State.Get(ctx, &stateOut)
	if stateOut.AccessLevel.ValueString() != "ACCESS_LEVEL_CUSTOM" {
		t.Errorf("expected state AccessLevel 'ACCESS_LEVEL_CUSTOM', got %q", stateOut.AccessLevel.ValueString())
	}

	// Unrecognized value returns attribute error instead of downgrading to ACCESS_LEVEL_UNSPECIFIED
	badModel := baseModel
	badModel.AccessLevel = types.StringValue("ACCESS_LEVEL_BOGUS")
	badPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = badPlanState.Set(ctx, &badModel)
	badResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: badPlanState.Raw}}, badResp)
	if !badResp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for unrecognized access_level value")
	}

	// Unknown server enum 99 is preserved as "99", NOT mapped to ACCESS_LEVEL_CUSTOM or UNSPECIFIED
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: "prof_unknown_enum",
					Version:   1,
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag:         "unknown-enum",
						Os:          "ubuntu-24.04",
						AccessLevel: githubv1beta.AccessLevel(99),
					},
				},
			},
		}, nil
	}
	impResp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: "prof_unknown_enum"}, impResp)
	if impResp.Diagnostics.HasError() {
		t.Fatalf("ImportState failed: %v", impResp.Diagnostics)
	}
	var impModel github.RunnerProfileResourceModel
	_ = impResp.State.Get(ctx, &impModel)
	if impModel.AccessLevel.ValueString() != "99" {
		t.Errorf("expected unknown enum 99 to flatten as '99', got %q", impModel.AccessLevel.ValueString())
	}
}

func TestRunnerProfileResource_InstanceShapeOS_Windows(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	srv.Profile.EnableStatefulProfiles()

	r, schemaResp := setupTestResource(t, srv)

	shapeAttr := schemaResp.Schema.Attributes["instance_shape"].(rschema.SingleNestedAttribute)
	osAttr := shapeAttr.Attributes["os"].(rschema.StringAttribute)

	for _, validOS := range []string{"linux", "macos", "windows", ""} {
		vResp := validator.StringResponse{}
		for _, v := range osAttr.Validators {
			v.ValidateString(ctx, validator.StringRequest{
				Path:        path.Root("instance_shape").AtName("os"),
				ConfigValue: types.StringValue(validOS),
			}, &vResp)
		}
		if vResp.Diagnostics.HasError() {
			t.Errorf("expected OS %q to be valid, got errors: %v", validOS, vResp.Diagnostics)
		}
	}

	for _, invalidOS := range []string{"freebsd", "Windows"} {
		vResp := validator.StringResponse{}
		for _, v := range osAttr.Validators {
			v.ValidateString(ctx, validator.StringRequest{
				Path:        path.Root("instance_shape").AtName("os"),
				ConfigValue: types.StringValue(invalidOS),
			}, &vResp)
		}
		if !vResp.Diagnostics.HasError() {
			t.Errorf("expected OS %q to be rejected by validator", invalidOS)
		}
	}

	winShapeObj, _ := types.ObjectValue(github.InstanceShapeAttrTypes, map[string]attr.Value{
		"virtual_cpu":      types.Int64Value(4),
		"memory_megabytes": types.Int64Value(8192),
		"machine_arch":     types.StringValue("amd64"),
		"os":               types.StringValue("windows"),
		"selectors":        types.ListNull(github.LabelObjectType),
	})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("win-runner"),
		OS:                    types.StringValue("windows-2022"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		CustomPermissions:     types.ListNull(github.CustomPermissionObjectType),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         winShapeObj,
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create Windows runner profile failed: %v", createResp.Diagnostics)
	}

	var got github.RunnerProfileResourceModel
	_ = createResp.State.Get(ctx, &got)
	var gotShape github.InstanceShapeModel
	_ = got.InstanceShape.As(ctx, &gotShape, basetypes.ObjectAsOptions{})
	if gotShape.OS.ValueString() != "windows" {
		t.Errorf("expected instance_shape.os 'windows', got %q", gotShape.OS.ValueString())
	}
}
