package github_test

import (
	"context"
	"testing"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
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
