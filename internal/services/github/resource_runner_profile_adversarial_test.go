package github_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// Helper to convert Go string slice to types.List
func toTFStringList(t *testing.T, items []string) types.List {
	t.Helper()
	if items == nil {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, len(items))
	for i, v := range items {
		elems[i] = types.StringValue(v)
	}
	l, diags := types.ListValue(types.StringType, elems)
	if diags.HasError() {
		t.Fatalf("failed to create TF string list: %v", diags)
	}
	return l
}

// -----------------------------------------------------------------------------
// GROUP 1: COMPLEX NESTED CONFIGURATIONS
// -----------------------------------------------------------------------------

// TestRunnerProfile_Adversarial_CustomImageSpecs tests:
// 1. Ubuntu APT packages only
// 2. Dockerfile content only
// 3. Both Ubuntu APT and Dockerfile content together
// 4. Update transitions between image spec combinations
func TestRunnerProfile_Adversarial_CustomImageSpecs(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var lastCreatedSpec *githubv1beta.RunnerProfileSpec
	var lastUpdatedSpec *githubv1beta.RunnerProfileSpec

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		lastCreatedSpec = req.GetSpec()
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_image_spec_test",
				Version:   1,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		lastUpdatedSpec = req.GetSpec()
		now := timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: req.GetProfileId(),
				Version:   req.GetUpdateVersion() + 1,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	// Case 1: Both Ubuntu APT packages AND Dockerfile content
	dockerContent := `FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends curl jq git \
    && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["/bin/bash"]`

	aptList := toTFStringList(t, []string{"build-essential", "pkg-config", "libssl-dev", "cmake"})
	uSpecObj, _ := types.ObjectValue(github.UbuntuSpecAttrTypes, map[string]attr.Value{
		"apt_package": aptList,
	})
	dSpecObj, _ := types.ObjectValue(github.DockerSpecAttrTypes, map[string]attr.Value{
		"dockerfile_content": types.StringValue(dockerContent),
	})
	customSpecObj, _ := types.ObjectValue(github.CustomRunnerImageSpecAttrTypes, map[string]attr.Value{
		"ubuntu_spec": uSpecObj,
		"docker_spec": dSpecObj,
	})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("dual-custom-spec"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringValue("Dual custom image spec runner"),
		Emoji:                 types.StringValue("📦"),
		SwapSizeMB:            types.Int64Value(4096),
		BuilderMode:           types.StringValue("USE_LOCAL_CACHE"),
		AccessLevel:           types.StringValue("ACCESS_LEVEL_PERMISSIVE"),
		ExperimentalFeatures:  toTFStringList(t, []string{"nested-virt", "fast-disk"}),
		CustomRunnerImageSpec: customSpecObj,
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := planState.Set(ctx, &planModel); diags.HasError() {
		t.Fatalf("failed to set plan state: %v", diags)
	}

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed for dual custom image spec: %v", createResp.Diagnostics)
	}

	if lastCreatedSpec == nil || lastCreatedSpec.CustomRunnerImageSpec == nil {
		t.Fatal("expected CustomRunnerImageSpec to be sent to mock server")
	}
	if lastCreatedSpec.CustomRunnerImageSpec.UbuntuSpec == nil || len(lastCreatedSpec.CustomRunnerImageSpec.UbuntuSpec.AptPackage) != 4 {
		t.Errorf("expected 4 APT packages, got %v", lastCreatedSpec.CustomRunnerImageSpec.UbuntuSpec)
	}
	if lastCreatedSpec.CustomRunnerImageSpec.DockerSpec == nil || lastCreatedSpec.CustomRunnerImageSpec.DockerSpec.DockerfileContent != dockerContent {
		t.Errorf("expected dockerfile content to match exactly, got %q", lastCreatedSpec.CustomRunnerImageSpec.DockerSpec.GetDockerfileContent())
	}

	var stateAfterCreate github.RunnerProfileResourceModel
	createResp.State.Get(ctx, &stateAfterCreate)
	if stateAfterCreate.CustomRunnerImageSpec.IsNull() {
		t.Fatal("expected stateAfterCreate to retain CustomRunnerImageSpec")
	}
	var customRes github.CustomRunnerImageSpecModel
	stateAfterCreate.CustomRunnerImageSpec.As(ctx, &customRes, basetypes.ObjectAsOptions{})
	if customRes.UbuntuSpec.IsNull() || customRes.DockerSpec.IsNull() {
		t.Fatal("expected stateAfterCreate to retain both UbuntuSpec and DockerSpec")
	}

	// Case 2: Update transition - Remove UbuntuSpec, keep only DockerSpec
	dSpecObj2, _ := types.ObjectValue(github.DockerSpecAttrTypes, map[string]attr.Value{
		"dockerfile_content": types.StringValue("FROM alpine:3.19\nRUN apk add --no-cache bash"),
	})
	customSpecObj2, _ := types.ObjectValue(github.CustomRunnerImageSpecAttrTypes, map[string]attr.Value{
		"ubuntu_spec": types.ObjectNull(github.UbuntuSpecAttrTypes),
		"docker_spec": dSpecObj2,
	})

	updatePlanModel := stateAfterCreate
	updatePlanModel.CustomRunnerImageSpec = customSpecObj2

	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := updatePlanState.Set(ctx, &updatePlanModel); diags.HasError() {
		t.Fatalf("failed to set update plan state: %v", diags)
	}

	updateReq := resource.UpdateRequest{
		State: createResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed transitioning to DockerSpec-only: %v", updateResp.Diagnostics)
	}

	if lastUpdatedSpec.CustomRunnerImageSpec.UbuntuSpec != nil {
		t.Errorf("expected UbuntuSpec to be nil after update, got %v", lastUpdatedSpec.CustomRunnerImageSpec.UbuntuSpec)
	}
	if lastUpdatedSpec.CustomRunnerImageSpec.DockerSpec == nil || lastUpdatedSpec.CustomRunnerImageSpec.DockerSpec.DockerfileContent != "FROM alpine:3.19\nRUN apk add --no-cache bash" {
		t.Errorf("expected updated DockerfileContent, got %v", lastUpdatedSpec.CustomRunnerImageSpec.DockerSpec)
	}

	// Case 3: Update transition - Remove CustomRunnerImageSpec entirely (null)
	updatePlanModel2 := updatePlanModel
	updatePlanModel2.CustomRunnerImageSpec = types.ObjectNull(github.CustomRunnerImageSpecAttrTypes)

	updatePlanState2 := tfsdk.State{Schema: schemaResp.Schema}
	if diags := updatePlanState2.Set(ctx, &updatePlanModel2); diags.HasError() {
		t.Fatalf("failed to set update plan state 2: %v", diags)
	}

	updateReq2 := resource.UpdateRequest{
		State: updateResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState2.Raw},
	}
	updateResp2 := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq2, updateResp2)
	if updateResp2.Diagnostics.HasError() {
		t.Fatalf("Update failed removing CustomRunnerImageSpec: %v", updateResp2.Diagnostics)
	}

	if lastUpdatedSpec.CustomRunnerImageSpec != nil {
		t.Errorf("expected CustomRunnerImageSpec to be nil after update, got %v", lastUpdatedSpec.CustomRunnerImageSpec)
	}
}

// TestRunnerProfile_Adversarial_CacheVolumeSettings tests:
// 1. Multiple attached cache volumes with diverse configurations
// 2. Multiple branch patterns with wildcards and exact matches
// 3. Flags all enabled, all disabled, and mixed
// 4. Zero vs positive reserved sizes
func TestRunnerProfile_Adversarial_CacheVolumeSettings(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var receivedSpec *githubv1beta.RunnerProfileSpec

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		receivedSpec = req.GetSpec()
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_cache_multi",
				Version:   1,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	cv1, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
		"volume_tag":                   types.StringValue("primary-build-cache"),
		"volume_size_gb":               types.Int64Value(200),
		"volume_reserved_size_gb":      types.Int64Value(20),
		"bundled_container_cache":      types.BoolValue(true),
		"bundled_buildkit_cache":       types.BoolValue(true),
		"bundled_git_mirror":           types.BoolValue(true),
		"bundled_runner_tools_cache":   types.BoolValue(true),
		"bundled_runner_actions_cache": types.BoolValue(true),
		"allow_commit_from_branch":     toTFStringList(t, []string{"main", "master", "release/*", "refs/heads/prod-*"}),
	})
	cv2, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
		"volume_tag":                   types.StringValue("secondary-maven-cache"),
		"volume_size_gb":               types.Int64Value(50),
		"volume_reserved_size_gb":      types.Int64Value(0),
		"bundled_container_cache":      types.BoolValue(false),
		"bundled_buildkit_cache":       types.BoolValue(false),
		"bundled_git_mirror":           types.BoolValue(false),
		"bundled_runner_tools_cache":   types.BoolValue(true),
		"bundled_runner_actions_cache": types.BoolValue(false),
		"allow_commit_from_branch":     toTFStringList(t, []string{"*"}),
	})
	cv3, _ := types.ObjectValue(github.CacheVolumeSettingAttrTypes, map[string]attr.Value{
		"volume_tag":                   types.StringValue("scratch-cache"),
		"volume_size_gb":               types.Int64Value(10),
		"volume_reserved_size_gb":      types.Int64Value(1),
		"bundled_container_cache":      types.BoolValue(false),
		"bundled_buildkit_cache":       types.BoolValue(false),
		"bundled_git_mirror":           types.BoolValue(false),
		"bundled_runner_tools_cache":   types.BoolValue(false),
		"bundled_runner_actions_cache": types.BoolValue(false),
		"allow_commit_from_branch":     toTFStringList(t, []string{}),
	})

	cacheVolumesList, _ := types.ListValue(github.CacheVolumeSettingObjectType, []attr.Value{cv1, cv2, cv3})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("multi-cache-runner"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringValue("Multi cache volume test runner"),
		Emoji:                 types.StringValue("💾"),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		CacheVolumeSettings:   cacheVolumesList,
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := planState.Set(ctx, &planModel); diags.HasError() {
		t.Fatalf("failed to set plan state: %v", diags)
	}

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed for multi-cache volumes: %v", createResp.Diagnostics)
	}

	if receivedSpec == nil {
		t.Fatal("expected CreateProfile to be called")
	}
	if len(receivedSpec.CacheVolumeSettings) != 3 {
		t.Fatalf("expected 3 cache volume settings in proto spec, got %d", len(receivedSpec.CacheVolumeSettings))
	}

	// Verify Volume 1
	v1 := receivedSpec.CacheVolumeSettings[0]
	if v1.VolumeTag != "primary-build-cache" || v1.VolumeSizeGb != 200 || v1.VolumeReservedSizeGb != 20 {
		t.Errorf("v1 mismatch: %+v", v1)
	}
	if !v1.BundledContainerCache || !v1.BundledBuildkitCache || !v1.BundledGitMirror || !v1.BundledRunnerToolsCache || !v1.BundledRunnerActionsCache {
		t.Errorf("v1 all flags should be true: %+v", v1)
	}
	if len(v1.AllowCommitFromBranch) != 4 || v1.AllowCommitFromBranch[2] != "release/*" {
		t.Errorf("v1 branches mismatch: %v", v1.AllowCommitFromBranch)
	}

	// Verify Volume 2
	v2 := receivedSpec.CacheVolumeSettings[1]
	if v2.VolumeTag != "secondary-maven-cache" || v2.VolumeSizeGb != 50 || v2.VolumeReservedSizeGb != 0 {
		t.Errorf("v2 mismatch: %+v", v2)
	}
	if v2.BundledContainerCache || v2.BundledBuildkitCache || v2.BundledGitMirror || !v2.BundledRunnerToolsCache || v2.BundledRunnerActionsCache {
		t.Errorf("v2 flags mismatch: %+v", v2)
	}
	if len(v2.AllowCommitFromBranch) != 1 || v2.AllowCommitFromBranch[0] != "*" {
		t.Errorf("v2 branches mismatch: %v", v2.AllowCommitFromBranch)
	}

	// Verify Volume 3
	v3 := receivedSpec.CacheVolumeSettings[2]
	if v3.VolumeTag != "scratch-cache" || v3.VolumeSizeGb != 10 || v3.VolumeReservedSizeGb != 1 {
		t.Errorf("v3 mismatch: %+v", v3)
	}
	if v3.BundledContainerCache || v3.BundledBuildkitCache || v3.BundledGitMirror || v3.BundledRunnerToolsCache || v3.BundledRunnerActionsCache {
		t.Errorf("v3 flags should all be false: %+v", v3)
	}

	// Verify state flattening matches
	var stateResult github.RunnerProfileResourceModel
	createResp.State.Get(ctx, &stateResult)
	var stateCVs []github.CacheVolumeSettingModel
	stateResult.CacheVolumeSettings.ElementsAs(ctx, &stateCVs, false)
	if len(stateCVs) != 3 {
		t.Fatalf("expected 3 cache volume settings in state, got %d", len(stateCVs))
	}
	if stateCVs[0].VolumeTag.ValueString() != "primary-build-cache" {
		t.Errorf("state volume 0 tag mismatch: %s", stateCVs[0].VolumeTag.ValueString())
	}
}

// TestRunnerProfile_Adversarial_NetworkEgressPolicies tests:
// 1. EGRESS_POLICY_NONE
// 2. EGRESS_POLICY_DOMAIN_ALLOW_LIST with advisory=true and advisory=false
// 3. EGRESS_POLICY_TAG with custom egress_policy_tag
// 4. EGRESS_POLICY_UNKNOWN fallback
func TestRunnerProfile_Adversarial_NetworkEgressPolicies(t *testing.T) {
	modes := []struct {
		name              string
		egressMode        string
		domains           []string
		advisory          bool
		policyTag         string
		expectedProtoMode githubv1beta.NetworkPolicy_EgressPolicy
	}{
		{
			name:              "EGRESS_POLICY_NONE",
			egressMode:        "EGRESS_POLICY_NONE",
			domains:           nil,
			advisory:          false,
			policyTag:         "",
			expectedProtoMode: githubv1beta.NetworkPolicy_EGRESS_POLICY_NONE,
		},
		{
			name:              "EGRESS_POLICY_DOMAIN_ALLOW_LIST_Advisory_True",
			egressMode:        "EGRESS_POLICY_DOMAIN_ALLOW_LIST",
			domains:           []string{"*.internal.net", "api.github.com", "pkg.go.dev"},
			advisory:          true,
			policyTag:         "",
			expectedProtoMode: githubv1beta.NetworkPolicy_EGRESS_POLICY_DOMAIN_ALLOW_LIST,
		},
		{
			name:              "EGRESS_POLICY_DOMAIN_ALLOW_LIST_Advisory_False",
			egressMode:        "EGRESS_POLICY_DOMAIN_ALLOW_LIST",
			domains:           []string{"registry.npmjs.org", "pypi.org"},
			advisory:          false,
			policyTag:         "",
			expectedProtoMode: githubv1beta.NetworkPolicy_EGRESS_POLICY_DOMAIN_ALLOW_LIST,
		},
		{
			name:              "EGRESS_POLICY_TAG",
			egressMode:        "EGRESS_POLICY_TAG",
			domains:           nil,
			advisory:          false,
			policyTag:         "corp-strict-egress-v2",
			expectedProtoMode: githubv1beta.NetworkPolicy_EGRESS_POLICY_TAG,
		},
		{
			name:              "EGRESS_POLICY_UNKNOWN",
			egressMode:        "EGRESS_POLICY_UNKNOWN",
			domains:           nil,
			advisory:          false,
			policyTag:         "",
			expectedProtoMode: githubv1beta.NetworkPolicy_EGRESS_POLICY_UNKNOWN,
		},
	}

	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv := testutil.NewMockServer()

			var capturedNP *githubv1beta.NetworkPolicy
			srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
				capturedNP = req.GetSpec().GetNetworkPolicy()
				now := timestamppb.Now()
				return &githubv1beta.CreateProfileResponse{
					Profile: &githubv1beta.RunnerProfileWithStatus{
						ProfileId: "prof_net_test",
						Version:   1,
						CreatedAt: now,
						UpdatedAt: now,
						Spec:      req.GetSpec(),
					},
				}, nil
			}

			r, schemaResp := setupTestResource(t, srv)

			npObj, _ := types.ObjectValue(github.NetworkPolicyAttrTypes, map[string]attr.Value{
				"egress":                   types.StringValue(tc.egressMode),
				"egress_domain_allow_list": toTFStringList(t, tc.domains),
				"advisory":                 types.BoolValue(tc.advisory),
				"egress_policy_tag":        types.StringValue(tc.policyTag),
			})

			planModel := github.RunnerProfileResourceModel{
				ID:                    types.StringUnknown(),
				ProfileID:             types.StringUnknown(),
				Version:               types.Int64Unknown(),
				CreatedAt:             types.StringUnknown(),
				UpdatedAt:             types.StringUnknown(),
				Tag:                   types.StringValue("net-policy-runner"),
				OS:                    types.StringValue("ubuntu-24.04"),
				Description:           types.StringValue("Network policy testing"),
				Emoji:                 types.StringNull(),
				SwapSizeMB:            types.Int64Value(0),
				BuilderMode:           types.StringNull(),
				AccessLevel:           types.StringNull(),
				ExperimentalFeatures:  types.ListNull(types.StringType),
				NetworkPolicy:         npObj,
				InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
				CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
				CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
				CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
			}

			planState := tfsdk.State{Schema: schemaResp.Schema}
			if diags := planState.Set(ctx, &planModel); diags.HasError() {
				t.Fatalf("failed to set plan state: %v", diags)
			}

			createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
			createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(ctx, createReq, createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create failed for %s: %v", tc.name, createResp.Diagnostics)
			}

			if capturedNP == nil {
				t.Fatal("expected NetworkPolicy in proto spec")
			}
			if capturedNP.Egress != tc.expectedProtoMode {
				t.Errorf("expected proto egress mode %v, got %v", tc.expectedProtoMode, capturedNP.Egress)
			}
			if capturedNP.Advisory != tc.advisory {
				t.Errorf("expected advisory %v, got %v", tc.advisory, capturedNP.Advisory)
			}
			if tc.policyTag != "" && capturedNP.EgressPolicyTag != tc.policyTag {
				t.Errorf("expected egress policy tag %q, got %q", tc.policyTag, capturedNP.EgressPolicyTag)
			}
			if len(tc.domains) > 0 {
				if len(capturedNP.EgressDomainAllowList) != len(tc.domains) {
					t.Errorf("expected %d domains, got %d", len(tc.domains), len(capturedNP.EgressDomainAllowList))
				}
			}

			var stateResult github.RunnerProfileResourceModel
			createResp.State.Get(ctx, &stateResult)
			if stateResult.NetworkPolicy.IsNull() {
				t.Fatal("expected NetworkPolicy in state")
			}
			var npRes github.NetworkPolicyModel
			stateResult.NetworkPolicy.As(ctx, &npRes, basetypes.ObjectAsOptions{})
			if npRes.Egress.ValueString() != tc.egressMode {
				t.Errorf("expected state egress %s, got %s", tc.egressMode, npRes.Egress.ValueString())
			}
			if npRes.Advisory.ValueBool() != tc.advisory {
				t.Errorf("expected state advisory %v, got %v", tc.advisory, npRes.Advisory.ValueBool())
			}
		})
	}
}

// TestRunnerProfile_Adversarial_InstanceShapeAndSelectors tests:
// 1. Complex shape with multiple placement selectors
// 2. Arch and OS configurations
// 3. Updates to instance shape and transition to nil
func TestRunnerProfile_Adversarial_InstanceShapeAndSelectors(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var lastCreatedSpec *githubv1beta.RunnerProfileSpec
	var lastUpdatedSpec *githubv1beta.RunnerProfileSpec

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		lastCreatedSpec = req.GetSpec()
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_shape_test",
				Version:   1,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		lastUpdatedSpec = req.GetSpec()
		now := timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: req.GetProfileId(),
				Version:   req.GetUpdateVersion() + 1,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	// Create with 64 vCPU, 256GB RAM, arm64, linux, 3 selectors
	s1, _ := types.ObjectValue(github.LabelAttrTypes, map[string]attr.Value{
		"name":  types.StringValue("accelerator"),
		"value": types.StringValue("none"),
	})
	s2, _ := types.ObjectValue(github.LabelAttrTypes, map[string]attr.Value{
		"name":  types.StringValue("topology.kubernetes.io/zone"),
		"value": types.StringValue("us-central1-a"),
	})
	s3, _ := types.ObjectValue(github.LabelAttrTypes, map[string]attr.Value{
		"name":  types.StringValue("workload-class"),
		"value": types.StringValue("batch-ci"),
	})
	selectorsList, _ := types.ListValue(github.LabelObjectType, []attr.Value{s1, s2, s3})

	shapeObj, _ := types.ObjectValue(github.InstanceShapeAttrTypes, map[string]attr.Value{
		"virtual_cpu":      types.Int64Value(64),
		"memory_megabytes": types.Int64Value(262144),
		"machine_arch":     types.StringValue("arm64"),
		"os":               types.StringValue("linux"),
		"selectors":        selectorsList,
	})

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("high-cpu-arm64"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringValue("Heavy ARM64 builder"),
		Emoji:                 types.StringValue("⚡"),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         shapeObj,
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := planState.Set(ctx, &planModel); diags.HasError() {
		t.Fatalf("failed to set plan state: %v", diags)
	}

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed for instance shape: %v", createResp.Diagnostics)
	}

	if lastCreatedSpec.InstanceShape == nil {
		t.Fatal("expected InstanceShape in proto spec")
	}
	shape := lastCreatedSpec.InstanceShape
	if shape.VirtualCpu != 64 || shape.MemoryMegabytes != 262144 || shape.MachineArch != "arm64" || shape.Os != "linux" {
		t.Errorf("instance shape mismatch: %+v", shape)
	}
	if len(shape.Selectors) != 3 {
		t.Fatalf("expected 3 selectors, got %d", len(shape.Selectors))
	}
	if shape.Selectors[1].Name != "topology.kubernetes.io/zone" || shape.Selectors[1].Value != "us-central1-a" {
		t.Errorf("selector 1 mismatch: %+v", shape.Selectors[1])
	}

	// Update to remove selectors and lower CPU/memory
	var createdState github.RunnerProfileResourceModel
	createResp.State.Get(ctx, &createdState)

	updatedShapeObj, _ := types.ObjectValue(github.InstanceShapeAttrTypes, map[string]attr.Value{
		"virtual_cpu":      types.Int64Value(4),
		"memory_megabytes": types.Int64Value(16384),
		"machine_arch":     types.StringValue("amd64"),
		"os":               types.StringValue("linux"),
		"selectors":        types.ListNull(github.LabelObjectType),
	})

	updatePlan := createdState
	updatePlan.InstanceShape = updatedShapeObj

	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState.Set(ctx, &updatePlan)

	updateReq := resource.UpdateRequest{
		State: createResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics)
	}

	if len(lastUpdatedSpec.InstanceShape.Selectors) != 0 {
		t.Errorf("expected 0 selectors after update, got %d", len(lastUpdatedSpec.InstanceShape.Selectors))
	}
	if lastUpdatedSpec.InstanceShape.VirtualCpu != 4 || lastUpdatedSpec.InstanceShape.MachineArch != "amd64" {
		t.Errorf("updated shape mismatch: %+v", lastUpdatedSpec.InstanceShape)
	}
}

// -----------------------------------------------------------------------------
// GROUP 2: CONCURRENCY RACE CONDITIONS & OUT-OF-BAND MUTATIONS
// -----------------------------------------------------------------------------

// TestRunnerProfile_Adversarial_OutOfBandTagAndVersionMutationOnRead tests:
// When server profile tag or version is changed out-of-band, Terraform Read
// successfully refreshes state to reflect the latest remote server state.
func TestRunnerProfile_Adversarial_OutOfBandTagAndVersionMutationOnRead(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	now := timestamppb.Now()
	remoteProfile := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_oob_mutated",
		Version:   42, // Mutated from version 1 to 42 out-of-band
		CreatedAt: now,
		UpdatedAt: now,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag:         "ubuntu-24-04-renamed-out-of-band", // Mutated tag
			Description: "Mutated description",
			Os:          "ubuntu-24.04",
			SwapSizeMb:  8192,
		},
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{remoteProfile},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	// State had old tag and old version
	oldStateModel := github.RunnerProfileResourceModel{
		ID:                    types.StringValue("prof_oob_mutated"),
		ProfileID:             types.StringValue("prof_oob_mutated"),
		Version:               types.Int64Value(1),
		CreatedAt:             types.StringValue("2026-08-21T00:00:00Z"),
		UpdatedAt:             types.StringValue("2026-08-21T00:00:00Z"),
		Tag:                   types.StringValue("ubuntu-24-04-old-tag"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringValue("Old description"),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(2048),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	oldStateRaw := tfsdk.State{Schema: schemaResp.Schema}
	if diags := oldStateRaw.Set(ctx, &oldStateModel); diags.HasError() {
		t.Fatalf("failed to set old state: %v", diags)
	}

	readReq := resource.ReadRequest{State: oldStateRaw}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed during out-of-band mutation test: %v", readResp.Diagnostics)
	}

	var stateResult github.RunnerProfileResourceModel
	readResp.State.Get(ctx, &stateResult)
	if stateResult.Version.ValueInt64() != 42 {
		t.Errorf("expected version refreshed to 42, got %d", stateResult.Version.ValueInt64())
	}
	if stateResult.Tag.ValueString() != "ubuntu-24-04-renamed-out-of-band" {
		t.Errorf("expected tag refreshed to 'ubuntu-24-04-renamed-out-of-band', got %q", stateResult.Tag.ValueString())
	}
	if stateResult.SwapSizeMB.ValueInt64() != 8192 {
		t.Errorf("expected swap_size_mb refreshed to 8192, got %d", stateResult.SwapSizeMB.ValueInt64())
	}
}

// TestRunnerProfile_Adversarial_ConcurrentOperationsRace tests thread-safety
// under heavy concurrent operations across multiple goroutines.
func TestRunnerProfile_Adversarial_ConcurrentOperationsRace(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	profilesMap := make(map[string]*githubv1beta.RunnerProfileWithStatus)

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		profID := fmt.Sprintf("prof_race_%d", len(profilesMap)+1)
		now := timestamppb.Now()
		p := &githubv1beta.RunnerProfileWithStatus{
			ProfileId: profID,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec:      proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec),
		}
		profilesMap[profID] = p
		return &githubv1beta.CreateProfileResponse{Profile: proto.Clone(p).(*githubv1beta.RunnerProfileWithStatus)}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var list []*githubv1beta.RunnerProfileWithStatus
		for _, p := range profilesMap {
			list = append(list, proto.Clone(p).(*githubv1beta.RunnerProfileWithStatus))
		}
		return &githubv1beta.ListProfilesResponse{Profiles: list}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		p, exists := profilesMap[req.GetProfileId()]
		if !exists {
			return nil, status.Error(codes.NotFound, "profile not found")
		}
		if p.Version != req.GetUpdateVersion() {
			return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("version mismatch: expected %d, got %d", p.Version, req.GetUpdateVersion()))
		}
		p.Version++
		p.Spec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		p.UpdatedAt = timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{Profile: proto.Clone(p).(*githubv1beta.RunnerProfileWithStatus)}, nil
	}

	srv.Profile.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(profilesMap, req.GetProfileId())
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	const workerCount = 20
	var wg sync.WaitGroup
	wg.Add(workerCount)

	for i := 0; i < workerCount; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			// 1. Create a profile
			planModel := github.RunnerProfileResourceModel{
				ID:                    types.StringUnknown(),
				ProfileID:             types.StringUnknown(),
				Version:               types.Int64Unknown(),
				CreatedAt:             types.StringUnknown(),
				UpdatedAt:             types.StringUnknown(),
				Tag:                   types.StringValue(fmt.Sprintf("race-tag-%d", workerID)),
				OS:                    types.StringValue("ubuntu-24.04"),
				Description:           types.StringValue(fmt.Sprintf("Worker %d description", workerID)),
				Emoji:                 types.StringNull(),
				SwapSizeMB:            types.Int64Value(1024),
				BuilderMode:           types.StringNull(),
				AccessLevel:           types.StringNull(),
				ExperimentalFeatures:  types.ListNull(types.StringType),
				InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
				CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
				CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
				NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
				CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
			}

			planState := tfsdk.State{Schema: schemaResp.Schema}
			_ = planState.Set(ctx, &planModel)

			createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
			createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(ctx, createReq, createResp)
			if createResp.Diagnostics.HasError() {
				t.Errorf("concurrent create failed worker %d: %v", workerID, createResp.Diagnostics)
				return
			}

			var createdState github.RunnerProfileResourceModel
			createResp.State.Get(ctx, &createdState)

			// 2. Read the profile
			readReq := resource.ReadRequest{State: createResp.State}
			readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Read(ctx, readReq, readResp)
			if readResp.Diagnostics.HasError() {
				t.Errorf("concurrent read failed worker %d: %v", workerID, readResp.Diagnostics)
				return
			}

			// 3. Update the profile
			updatePlan := createdState
			updatePlan.Description = types.StringValue(fmt.Sprintf("Worker %d updated description", workerID))
			updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
			_ = updatePlanState.Set(ctx, &updatePlan)

			updateReq := resource.UpdateRequest{
				State: createResp.State,
				Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
			}
			updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Update(ctx, updateReq, updateResp)
			if updateResp.Diagnostics.HasError() {
				t.Errorf("concurrent update failed worker %d: %v", workerID, updateResp.Diagnostics)
				return
			}

			// 4. Import the profile by Tag
			if impR, ok := r.(resource.ResourceWithImportState); ok {
				impReq := resource.ImportStateRequest{ID: fmt.Sprintf("race-tag-%d", workerID)}
				impResp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
				impR.ImportState(ctx, impReq, impResp)
				if impResp.Diagnostics.HasError() {
					t.Errorf("concurrent import failed worker %d: %v", workerID, impResp.Diagnostics)
					return
				}
			}

			// 5. Delete the profile
			delReq := resource.DeleteRequest{State: updateResp.State}
			delResp := &resource.DeleteResponse{}
			r.Delete(ctx, delReq, delResp)
			if delResp.Diagnostics.HasError() {
				t.Errorf("concurrent delete failed worker %d: %v", workerID, delResp.Diagnostics)
				return
			}
		}()
	}

	wg.Wait()
}

// -----------------------------------------------------------------------------
// GROUP 3: IMPORT BEHAVIOR & EDGE CASES
// -----------------------------------------------------------------------------

// TestRunnerProfile_Adversarial_ImportAmbiguity tests:
// 1. Multiple profiles sharing the same tag -> Ambiguous error
// 2. Profile A profile_id equals Profile B tag -> Ambiguous error
// 3. Empty ID string -> Invalid Import ID error
// 4. gRPC error during import -> Proper diagnostic error
func TestRunnerProfile_Adversarial_ImportAmbiguity(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	now := timestamppb.Now()
	p1 := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_duplicate_tag_1",
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag: "shared-tag",
			Os:  "ubuntu-24.04",
		},
	}
	p2 := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_duplicate_tag_2",
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag: "shared-tag",
			Os:  "ubuntu-24.04",
		},
	}
	p3 := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "cross-match-id",
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag: "unrelated-tag",
			Os:  "ubuntu-24.04",
		},
	}
	p4 := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_unrelated",
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag: "cross-match-id", // Matches p3's ProfileId
			Os:  "ubuntu-24.04",
		},
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{p1, p2, p3, p4},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)
	impR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("resource does not implement ResourceWithImportState")
	}

	// Case 1: Importing shared-tag matches 2 profiles (p1 and p2)
	{
		req := resource.ImportStateRequest{ID: "shared-tag"}
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		impR.ImportState(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error when importing ambiguous tag matching multiple profiles")
		}
		summary := resp.Diagnostics[0].Summary()
		if !strings.Contains(summary, "Ambiguous Import") {
			t.Errorf("expected summary to contain 'Ambiguous Import', got %q", summary)
		}
	}

	// Case 2: Importing cross-match-id matches p3 by ID and p4 by Tag
	{
		req := resource.ImportStateRequest{ID: "cross-match-id"}
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		impR.ImportState(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error when importing ID that matches both a profile_id and another's tag")
		}
		summary := resp.Diagnostics[0].Summary()
		if !strings.Contains(summary, "Ambiguous Import") {
			t.Errorf("expected summary to contain 'Ambiguous Import', got %q", summary)
		}
	}

	// Case 3: Importing empty ID
	{
		req := resource.ImportStateRequest{ID: ""}
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		impR.ImportState(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error when importing empty ID")
		}
		summary := resp.Diagnostics[0].Summary()
		if !strings.Contains(summary, "Invalid Import ID") {
			t.Errorf("expected summary to contain 'Invalid Import ID', got %q", summary)
		}
	}

	// Case 4: gRPC error during import
	{
		srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "unauthorized access to workspace profiles")
		}
		req := resource.ImportStateRequest{ID: "any-id"}
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		impR.ImportState(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error when gRPC call fails during import")
		}
		detail := resp.Diagnostics[0].Detail()
		if !strings.Contains(detail, "unauthorized access") {
			t.Errorf("expected diagnostic detail to contain gRPC error message, got %q", detail)
		}
	}
}

// -----------------------------------------------------------------------------
// GROUP 4: NIL RESPONSES, SPARSITY, STATUS VARIANTS & ENUM MATRICES
// -----------------------------------------------------------------------------

// TestRunnerProfile_Adversarial_NilServerResponses tests defensive handling when
// the backend returns nil profile objects.
func TestRunnerProfile_Adversarial_NilServerResponses(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Nil profile on Create
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		return &githubv1beta.CreateProfileResponse{Profile: nil}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("nil-test"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected error when server returns nil profile in CreateProfileResponse")
	}
	if !strings.Contains(createResp.Diagnostics[0].Summary(), "API Response Error") {
		t.Errorf("expected summary 'API Response Error', got %q", createResp.Diagnostics[0].Summary())
	}

	// Nil profile on Update
	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		return &githubv1beta.UpdateProfileResponse{Profile: nil}, nil
	}

	stateModel := planModel
	stateModel.ID = types.StringValue("prof_nil_test")
	stateModel.ProfileID = types.StringValue("prof_nil_test")
	stateModel.Version = types.Int64Value(1)
	stateModel.CreatedAt = types.StringValue("2026-08-21T00:00:00Z")
	stateModel.UpdatedAt = types.StringValue("2026-08-21T00:00:00Z")

	stateRaw := tfsdk.State{Schema: schemaResp.Schema}
	_ = stateRaw.Set(ctx, &stateModel)

	updateReq := resource.UpdateRequest{
		State: stateRaw,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected error when server returns nil profile in UpdateProfileResponse")
	}
	if !strings.Contains(updateResp.Diagnostics[0].Summary(), "API Response Error") {
		t.Errorf("expected summary 'API Response Error', got %q", updateResp.Diagnostics[0].Summary())
	}
}

// TestRunnerProfile_Adversarial_CustomRunnerImagesStatusVariants tests status conversion
// across all status enums, errors, timestamps, and build references.
func TestRunnerProfile_Adversarial_CustomRunnerImagesStatusVariants(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	now := timestamppb.Now()
	remoteProfile := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_status_matrix",
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag: "status-test",
			Os:  "ubuntu-24.04",
		},
		Status: &githubv1beta.RunnerProfileStatus{
			CustomRunnerImage: []*githubv1beta.CustomRunnerImage{
				{
					ImageStatus: &githubv1beta.CustomRunnerImageStatus{
						ImageRef:       "nscr.io/workspace/custom:ready",
						Status:         githubv1beta.CustomRunnerImageStatus_READY,
						StartedBuildAt: now,
						BuiltAt:        now,
						BuildRef:       "build_ready_01",
					},
				},
				{
					ImageStatus: &githubv1beta.CustomRunnerImageStatus{
						ImageRef:       "nscr.io/workspace/custom:building",
						Status:         githubv1beta.CustomRunnerImageStatus_BUILDING,
						StartedBuildAt: now,
						BuildRef:       "build_building_02",
					},
				},
				{
					ImageStatus: &githubv1beta.CustomRunnerImageStatus{
						ImageRef:       "nscr.io/workspace/custom:failed",
						Status:         githubv1beta.CustomRunnerImageStatus_FAILED,
						LastBuildError: "exit code 127: package libmissing not found",
						StartedBuildAt: now,
						LastErrorAt:    now,
						BuildRef:       "build_failed_03",
					},
				},
				{
					ImageStatus: &githubv1beta.CustomRunnerImageStatus{
						ImageRef: "nscr.io/workspace/custom:unknown",
						Status:   githubv1beta.CustomRunnerImageStatus_STATUS_UNKNOWN,
					},
				},
			},
		},
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{remoteProfile},
		}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	stateModel := github.RunnerProfileResourceModel{
		ID:                    types.StringValue("prof_status_matrix"),
		ProfileID:             types.StringValue("prof_status_matrix"),
		Version:               types.Int64Value(1),
		CreatedAt:             types.StringValue("2026-08-21T00:00:00Z"),
		UpdatedAt:             types.StringValue("2026-08-21T00:00:00Z"),
		Tag:                   types.StringValue("status-test"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	stateRaw := tfsdk.State{Schema: schemaResp.Schema}
	_ = stateRaw.Set(ctx, &stateModel)

	readReq := resource.ReadRequest{State: stateRaw}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed during status variants test: %v", readResp.Diagnostics)
	}

	var stateResult github.RunnerProfileResourceModel
	readResp.State.Get(ctx, &stateResult)
	var stateImgs []github.CustomRunnerImageStatusModel
	stateResult.CustomRunnerImages.ElementsAs(ctx, &stateImgs, false)
	if len(stateImgs) != 4 {
		t.Fatalf("expected 4 custom runner image statuses in state, got %d", len(stateImgs))
	}

	if stateImgs[0].Status.ValueString() != "BUILDING" {
		t.Errorf("img 0 status mismatch: %s", stateImgs[0].Status.ValueString())
	}
	if stateImgs[1].Status.ValueString() != "FAILED" || !strings.Contains(stateImgs[1].LastBuildError.ValueString(), "libmissing") {
		t.Errorf("img 1 error mismatch: %+v", stateImgs[1])
	}
	if stateImgs[2].Status.ValueString() != "READY" {
		t.Errorf("img 2 status mismatch: %s", stateImgs[2].Status.ValueString())
	}
	if stateImgs[3].Status.ValueString() != "STATUS_UNKNOWN" {
		t.Errorf("img 3 status mismatch: %s", stateImgs[3].Status.ValueString())
	}
}

// TestRunnerProfile_Adversarial_MinimalSparseLifecycle tests creating, reading,
// and updating a minimal profile containing only the required fields (tag, os).
func TestRunnerProfile_Adversarial_MinimalSparseLifecycle(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var currentProfile *githubv1beta.RunnerProfileWithStatus
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		now := timestamppb.Now()
		currentProfile = &githubv1beta.RunnerProfileWithStatus{
			ProfileId: "prof_sparse_1",
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec:      req.GetSpec(),
		}
		return &githubv1beta.CreateProfileResponse{Profile: currentProfile}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{currentProfile},
		}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		currentProfile.Version++
		currentProfile.Spec = req.GetSpec()
		currentProfile.UpdatedAt = timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{Profile: currentProfile}, nil
	}

	r, schemaResp := setupTestResource(t, srv)

	// 1. Create minimal sparse profile
	planModel := github.RunnerProfileResourceModel{
		ID:                    types.StringUnknown(),
		ProfileID:             types.StringUnknown(),
		Version:               types.Int64Unknown(),
		CreatedAt:             types.StringUnknown(),
		UpdatedAt:             types.StringUnknown(),
		Tag:                   types.StringValue("minimal-tag"),
		OS:                    types.StringValue("ubuntu-24.04"),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Value(0),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed for minimal profile: %v", createResp.Diagnostics)
	}

	// 2. Read minimal sparse profile
	readReq := resource.ReadRequest{State: createResp.State}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed for minimal profile: %v", readResp.Diagnostics)
	}

	// 3. Update minimal profile with description
	var createdState github.RunnerProfileResourceModel
	createResp.State.Get(ctx, &createdState)

	updatePlan := createdState
	updatePlan.Description = types.StringValue("Added a description to sparse profile")
	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState.Set(ctx, &updatePlan)

	updateReq := resource.UpdateRequest{
		State: createResp.State,
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed on sparse profile: %v", updateResp.Diagnostics)
	}

	var updatedState github.RunnerProfileResourceModel
	updateResp.State.Get(ctx, &updatedState)
	if updatedState.Version.ValueInt64() != 2 {
		t.Errorf("expected version 2 after update, got %d", updatedState.Version.ValueInt64())
	}
	if updatedState.Description.ValueString() != "Added a description to sparse profile" {
		t.Errorf("description mismatch: %s", updatedState.Description.ValueString())
	}
}
