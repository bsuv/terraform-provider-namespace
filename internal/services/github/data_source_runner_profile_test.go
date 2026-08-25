package github_test

import (
	"context"
	"strings"
	"testing"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceRunnerProfile(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := github.NewRunnerProfileDataSource()

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

func TestRunnerProfileDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := github.NewRunnerProfileDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_github_runner_profile" {
		t.Fatalf("expected type name 'namespace_github_runner_profile', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{
		"id", "profile_id", "tag", "version", "created_at", "updated_at",
		"os", "description", "emoji", "swap_size_mb", "builder_mode",
		"access_level", "experimental_features", "instance_shape",
		"cache_volume_settings", "custom_runner_image_spec",
		"network_policy", "custom_runner_images",
	}

	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in data source schema", name)
		}
	}
}

func TestRunnerProfileDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := github.NewRunnerProfileDataSource()

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

func TestRunnerProfileDataSource_Read_ByProfileID(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	profile := sampleFullProfile("prof_ds_1", 1)
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{profile},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	configModel := github.RunnerProfileDataSourceModel{
		ID:                    types.StringNull(),
		ProfileID:             types.StringValue("prof_ds_1"),
		Tag:                   types.StringNull(),
		Version:               types.Int64Null(),
		CreatedAt:             types.StringNull(),
		UpdatedAt:             types.StringNull(),
		OS:                    types.StringNull(),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Null(),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &configModel); diags.HasError() {
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
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.RunnerProfileDataSourceModel
	resp.State.Get(ctx, &stateResult)

	if stateResult.ID.ValueString() != "prof_ds_1" {
		t.Errorf("expected id 'prof_ds_1', got %q", stateResult.ID.ValueString())
	}
	if stateResult.Tag.ValueString() != "ubuntu-24-04-large" {
		t.Errorf("expected tag 'ubuntu-24-04-large', got %q", stateResult.Tag.ValueString())
	}
	if stateResult.OS.ValueString() != "ubuntu-24.04" {
		t.Errorf("expected OS 'ubuntu-24.04', got %q", stateResult.OS.ValueString())
	}
}

func TestRunnerProfileDataSource_Read_ByTag(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	profile := sampleFullProfile("prof_ds_tag", 2)
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{profile},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	configModel := github.RunnerProfileDataSourceModel{
		ID:                    types.StringNull(),
		ProfileID:             types.StringNull(),
		Tag:                   types.StringValue("ubuntu-24-04-large"),
		Version:               types.Int64Null(),
		CreatedAt:             types.StringNull(),
		UpdatedAt:             types.StringNull(),
		OS:                    types.StringNull(),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Null(),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &configModel); diags.HasError() {
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
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.RunnerProfileDataSourceModel
	resp.State.Get(ctx, &stateResult)

	if stateResult.ProfileID.ValueString() != "prof_ds_tag" {
		t.Errorf("expected profile_id 'prof_ds_tag', got %q", stateResult.ProfileID.ValueString())
	}
}

func TestRunnerProfileDataSource_Read_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	configModel := github.RunnerProfileDataSourceModel{
		ID:                    types.StringNull(),
		ProfileID:             types.StringValue("prof_missing"),
		Tag:                   types.StringNull(),
		Version:               types.Int64Null(),
		CreatedAt:             types.StringNull(),
		UpdatedAt:             types.StringNull(),
		OS:                    types.StringNull(),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Null(),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error on profile not found in data source")
	}

	if !strings.Contains(resp.Diagnostics[0].Summary(), "Runner Profile Not Found") {
		t.Errorf("expected summary 'Runner Profile Not Found', got %q", resp.Diagnostics[0].Summary())
	}
}

func TestRunnerProfileDataSource_Read_MultipleMatches(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	p1 := sampleFullProfile("prof_1", 1)
	p1.Spec.Tag = "duplicate-tag"
	p2 := sampleFullProfile("prof_2", 1)
	p2.Spec.Tag = "duplicate-tag"

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{p1, p2},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	configModel := github.RunnerProfileDataSourceModel{
		ID:                    types.StringNull(),
		ProfileID:             types.StringNull(),
		Tag:                   types.StringValue("duplicate-tag"),
		Version:               types.Int64Null(),
		CreatedAt:             types.StringNull(),
		UpdatedAt:             types.StringNull(),
		OS:                    types.StringNull(),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Null(),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error on multiple matching runner profiles")
	}

	if !strings.Contains(resp.Diagnostics[0].Summary(), "Multiple Runner Profiles Found") {
		t.Errorf("expected summary 'Multiple Runner Profiles Found', got %q", resp.Diagnostics[0].Summary())
	}
}

func TestRunnerProfileDataSource_Read_GRPCError(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return nil, status.Error(codes.Unavailable, "service temporarily unavailable")
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	configModel := github.RunnerProfileDataSourceModel{
		ID:                    types.StringNull(),
		ProfileID:             types.StringValue("prof_123"),
		Tag:                   types.StringNull(),
		Version:               types.Int64Null(),
		CreatedAt:             types.StringNull(),
		UpdatedAt:             types.StringNull(),
		OS:                    types.StringNull(),
		Description:           types.StringNull(),
		Emoji:                 types.StringNull(),
		SwapSizeMB:            types.Int64Null(),
		BuilderMode:           types.StringNull(),
		AccessLevel:           types.StringNull(),
		ExperimentalFeatures:  types.ListNull(types.StringType),
		InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
		CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
		CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
		NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
		CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error on gRPC failure in data source read")
	}
}
