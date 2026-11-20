package github_test

import (
	"context"
	"strings"
	"testing"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceRunnerProfiles(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := github.NewRunnerProfilesDataSource()

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

func TestRunnerProfilesDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := github.NewRunnerProfilesDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_github_runner_profiles" {
		t.Fatalf("expected type name 'namespace_github_runner_profiles', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	if _, exists := attrs["id"]; !exists {
		t.Error("missing 'id' attribute in runner profiles data source schema")
	}
	if _, exists := attrs["profiles"]; !exists {
		t.Error("missing 'profiles' attribute in runner profiles data source schema")
	}

	profilesAttr := attrs["profiles"].(dschema.ListNestedAttribute)
	nested := profilesAttr.NestedObject.Attributes
	if _, exists := nested["custom_permissions"]; !exists {
		t.Error("missing 'custom_permissions' attribute in runner profiles nested schema")
	}
	cvAttr := nested["cache_volume_settings"].(dschema.ListNestedAttribute)
	if _, hasVolTag := cvAttr.NestedObject.Attributes["volume_tag"]; hasVolTag {
		t.Error("runner profiles cache_volume_settings schema must not contain removed attribute 'volume_tag'")
	}
	shapeAttr := nested["instance_shape"].(dschema.SingleNestedAttribute)
	osAttr := shapeAttr.Attributes["os"].(dschema.StringAttribute)
	if !strings.Contains(strings.ToLower(osAttr.Description), "windows") {
		t.Errorf("expected profiles.*.instance_shape.os description to include 'windows', got %q", osAttr.Description)
	}
	alAttr := nested["access_level"].(dschema.StringAttribute)
	if !strings.Contains(alAttr.Description, "ACCESS_LEVEL_CUSTOM") {
		t.Errorf("expected profiles.*.access_level description to include 'ACCESS_LEVEL_CUSTOM', got %q", alAttr.Description)
	}
}

func TestRunnerProfilesDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := github.NewRunnerProfilesDataSource()

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

func TestRunnerProfilesDataSource_Read_Success_Sorted(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Return profiles out of order: z, a, m
	pz := sampleFullProfile("prof_z", 1)
	pa := sampleFullProfile("prof_a", 1)
	pm := sampleFullProfile("prof_m", 1)

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{pz, pa, pm},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfiles(t, srv)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.RunnerProfilesDataSourceModel
	resp.State.Get(ctx, &stateResult)

	if stateResult.ID.ValueString() != "profiles" {
		t.Errorf("expected id 'profiles', got %q", stateResult.ID.ValueString())
	}
	if len(stateResult.Profiles) != 3 {
		t.Fatalf("expected 3 profiles in state, got %d", len(stateResult.Profiles))
	}

	// Verify ascending sort by profile_id
	if stateResult.Profiles[0].ProfileID.ValueString() != "prof_a" {
		t.Errorf("expected index 0 to be 'prof_a', got %q", stateResult.Profiles[0].ProfileID.ValueString())
	}
	if stateResult.Profiles[1].ProfileID.ValueString() != "prof_m" {
		t.Errorf("expected index 1 to be 'prof_m', got %q", stateResult.Profiles[1].ProfileID.ValueString())
	}
	if stateResult.Profiles[2].ProfileID.ValueString() != "prof_z" {
		t.Errorf("expected index 2 to be 'prof_z', got %q", stateResult.Profiles[2].ProfileID.ValueString())
	}
}

func TestRunnerProfilesDataSource_Read_Empty(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfiles(t, srv)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.RunnerProfilesDataSourceModel
	resp.State.Get(ctx, &stateResult)

	if stateResult.ID.ValueString() != "profiles" {
		t.Errorf("expected id 'profiles', got %q", stateResult.ID.ValueString())
	}
	if len(stateResult.Profiles) != 0 {
		t.Errorf("expected 0 profiles, got %d", len(stateResult.Profiles))
	}
}

func TestRunnerProfilesDataSource_Read_GRPCError(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return nil, status.Error(codes.Internal, "internal database error")
	}

	d, schemaResp := setupTestDataSourceRunnerProfiles(t, srv)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic on gRPC failure")
	}
}

func TestRunnerProfilesDataSource_Read_CustomPermissionsAndWindows(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	p := sampleFullProfile("prof_list_custom_win", 1)
	p.Spec.Os = "windows-2022"
	p.Spec.AccessLevel = githubv1beta.AccessLevel(4)
	p.Spec.CustomPermissions = []*iamv1beta.Permission{
		{ResourceType: "cache/bazel", ResourceId: "c1", Actions: []string{"read", "write"}},
	}
	p.Spec.InstanceShape = &computev1beta.InstanceShape{
		VirtualCpu:      4,
		MemoryMegabytes: 8192,
		MachineArch:     "amd64",
		Os:              "windows",
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{p},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfiles(t, srv)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var got github.RunnerProfilesDataSourceModel
	_ = resp.State.Get(ctx, &got)
	if len(got.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(got.Profiles))
	}
	if got.Profiles[0].AccessLevel.ValueString() != "ACCESS_LEVEL_CUSTOM" {
		t.Errorf("expected ACCESS_LEVEL_CUSTOM, got %q", got.Profiles[0].AccessLevel.ValueString())
	}
	var perms []github.CustomPermissionModel
	_ = got.Profiles[0].CustomPermissions.ElementsAs(ctx, &perms, false)
	if len(perms) != 1 || perms[0].ResourceType.ValueString() != "cache/bazel" || perms[0].ResourceID.ValueString() != "c1" {
		t.Errorf("unexpected custom_permissions: %+v", perms)
	}
	var shape github.InstanceShapeModel
	_ = got.Profiles[0].InstanceShape.As(ctx, &shape, basetypes.ObjectAsOptions{})
	if shape.OS.ValueString() != "windows" {
		t.Errorf("expected instance_shape.os 'windows', got %q", shape.OS.ValueString())
	}
}
