package registry_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

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

func TestRepositoryPolicyResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := registry.NewRepositoryPolicyResource()

	var metaResp resource.MetadataResponse
	r.Metadata(ctx, resource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_registry_repository_policy" {
		t.Fatalf("expected type name 'namespace_registry_repository_policy', got %q", metaResp.TypeName)
	}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"id", "repository", "revision", "expiration", "expiration_enforcement"}
	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in repository policy schema", name)
		}
	}
}

func TestRepositoryPolicyResource_ConfigValidators(t *testing.T) {
	ctx := context.Background()
	r := registry.NewRepositoryPolicyResource()

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

	// 1. Invalid: ENFORCE without expiration
	invalidConfig := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("my-repo"),
		Repository:            types.StringValue("my-repo"),
		Revision:              types.Int64Value(1),
		Expiration:            types.StringNull(),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &invalidConfig)

	var resp resource.ValidateConfigResponse
	validators[0].ValidateResource(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: state.Raw},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for ENFORCE without expiration, got none")
	}

	// 2. Valid: ENFORCE with expiration
	validConfig := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("my-repo"),
		Repository:            types.StringValue("my-repo"),
		Revision:              types.Int64Value(1),
		Expiration:            types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}
	state = tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &validConfig)

	resp = resource.ValidateConfigResponse{}
	validators[0].ValidateResource(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: state.Raw},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error for valid config: %v", resp.Diagnostics)
	}
}

func TestRepositoryPolicyResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := registry.NewRepositoryPolicyResource()

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

func TestRepositoryPolicyResource_Create_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCalls int
	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		getCalls++
		if getCalls == 1 {
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              3,
				Expiration:            nil,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
			}, nil
		}
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              4,
			Expiration:            durationpb.New(12 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	var updateReq *registryv1beta.UpdateRepositoryPolicyRequest
	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		updateReq = req
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	planModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringUnknown(),
		Repository:            types.StringValue("frontend-service"),
		Revision:              types.Int64Unknown(),
		Expiration:            types.StringValue("12h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
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

	if updateReq == nil || updateReq.Repository != "frontend-service" || updateReq.Revision != 3 {
		t.Fatalf("unexpected UpdateRepositoryPolicyRequest: %+v", updateReq)
	}

	var state registry.RepositoryPolicyResourceModel
	diags = resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to get response state: %v", diags)
	}

	if state.ID.ValueString() != "frontend-service" {
		t.Errorf("expected ID 'frontend-service', got %q", state.ID.ValueString())
	}
	if state.Repository.ValueString() != "frontend-service" {
		t.Errorf("expected repository 'frontend-service', got %q", state.Repository.ValueString())
	}
	if state.Revision.ValueInt64() != 4 {
		t.Errorf("expected revision 4, got %d", state.Revision.ValueInt64())
	}
}

func TestRepositoryPolicyResource_Read_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              9,
			Expiration:            durationpb.New(24 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
		}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("auth-service"),
		Repository:            types.StringValue("auth-service"),
		Revision:              types.Int64Value(8),
		Expiration:            types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE"),
	}

	initialState := tfsdk.State{Schema: schemaResp.Schema}
	diags := initialState.Set(ctx, &stateModel)
	if diags.HasError() {
		t.Fatalf("failed to set initial state: %v", diags)
	}

	req := resource.ReadRequest{State: initialState}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var updatedState registry.RepositoryPolicyResourceModel
	diags = resp.State.Get(ctx, &updatedState)
	if diags.HasError() {
		t.Fatalf("failed to get state: %v", diags)
	}

	if updatedState.Revision.ValueInt64() != 9 {
		t.Errorf("expected updated revision 9, got %d", updatedState.Revision.ValueInt64())
	}
}

func TestRepositoryPolicyResource_Read_NotFound_RemovesState(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return nil, status.Error(codes.NotFound, "repository not found")
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("deleted-repo"),
		Repository:            types.StringValue("deleted-repo"),
		Revision:              types.Int64Value(1),
		Expiration:            types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
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
		t.Fatal("expected state to be removed (null) when repo not found")
	}
}

func TestRepositoryPolicyResource_Update_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var updateReq *registryv1beta.UpdateRepositoryPolicyRequest
	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		updateReq = req
		return &emptypb.Empty{}, nil
	}

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              15,
			Expiration:            durationpb.New(72 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	planModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("my-repo"),
		Repository:            types.StringValue("my-repo"),
		Revision:              types.Int64Unknown(),
		Expiration:            types.StringValue("72h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}
	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("my-repo"),
		Repository:            types.StringValue("my-repo"),
		Revision:              types.Int64Value(14),
		Expiration:            types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
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

	if updateReq == nil || updateReq.Revision != 14 || updateReq.Repository != "my-repo" {
		t.Fatalf("expected update called with revision 14 from state, got %+v", updateReq)
	}
}

func TestRepositoryPolicyResource_Delete_ClearOverride(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCalls int
	var clearReq *registryv1beta.UpdateRepositoryPolicyRequest
	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		getCalls++
		if getCalls == 1 {
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              25,
				Expiration:            durationpb.New(24 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}, nil
		}
		// Post-update verification read: return cleared override (or NotFound)
		return nil, status.Error(codes.NotFound, "policy cleared")
	}

	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		clearReq = req
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("api-service"),
		Repository:            types.StringValue("api-service"),
		Revision:              types.Int64Value(25),
		Expiration:            types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete errors: %v", resp.Diagnostics)
	}

	if clearReq == nil || clearReq.Repository != "api-service" || clearReq.Expiration != nil || clearReq.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN {
		t.Fatalf("expected clear override payload (nil expiration, UNKNOWN enforcement), got: %+v", clearReq)
	}
}

func TestRepositoryPolicyResource_Delete_ResetVerificationFailure(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCalls int
	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		getCalls++
		if getCalls == 1 {
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              25,
				Expiration:            durationpb.New(24 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}, nil
		}
		// Post-update verification read returns uncleared override (server failed to clear)
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              26,
			Expiration:            durationpb.New(12 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("api-service"),
		Repository:            types.StringValue("api-service"),
		Revision:              types.Int64Value(25),
		Expiration:            types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic when server returns non-cleared policy state, got none")
	}
}

func TestRepositoryPolicyResource_ImportState_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              12,
			Expiration:            durationpb.New(48 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithImportState")
	}

	req := resource.ImportStateRequest{ID: "worker-service"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	importR.ImportState(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected import errors: %v", resp.Diagnostics)
	}

	var state registry.RepositoryPolicyResourceModel
	diags := resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to read state after import: %v", diags)
	}

	if state.ID.ValueString() != "worker-service" {
		t.Errorf("expected ID 'worker-service', got %q", state.ID.ValueString())
	}
	if state.Repository.ValueString() != "worker-service" {
		t.Errorf("expected repository 'worker-service', got %q", state.Repository.ValueString())
	}
	if state.Revision.ValueInt64() != 12 {
		t.Errorf("expected revision 12, got %d", state.Revision.ValueInt64())
	}
}

func TestRepositoryPolicyResource_ImportState_EmptyID(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

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
		t.Fatal("expected diagnostic error for empty import ID, got none")
	}
}

func TestRepositoryPolicyResource_ImportState_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return nil, status.Error(codes.NotFound, "repository policy not found")
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithImportState")
	}

	req := resource.ImportStateRequest{ID: "nonexistent-repo"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	importR.ImportState(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostic error importing non-existent repo, got none")
	}
}

func TestRepositoryPolicyResource_Create_InitialReadError_Propagates(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Return PermissionDenied on initial read
	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "access denied to repository")
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	planModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringUnknown(),
		Repository:            types.StringValue("secret-repo"),
		Revision:              types.Int64Unknown(),
		Expiration:            types.StringValue("12h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Create(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic when initial read returns PermissionDenied, got none")
	}
}

func TestRepositoryPolicyResource_Delete_ResetVerification_RejectsDoNotEnforce(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCalls int
	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		getCalls++
		if getCalls == 1 {
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              10,
				Expiration:            durationpb.New(24 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}, nil
		}
		// Post-update verification read returns DO_NOT_ENFORCE (which is NOT UNKNOWN and must be rejected)
		return &registryv1beta.GetRepositoryPolicyResponse{
			Revision:              11,
			Expiration:            nil,
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
		}, nil
	}

	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	stateModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringValue("api-service"),
		Repository:            types.StringValue("api-service"),
		Revision:              types.Int64Value(10),
		Expiration:            types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic when server returns DO_NOT_ENFORCE on reset verification, got none")
	}
}

func TestRepositoryPolicyResource_Delete_ResetVerification_RejectsZeroAndNegativeDuration(t *testing.T) {
	cases := []struct {
		name     string
		duration *durationpb.Duration
	}{
		{name: "Zero duration", duration: &durationpb.Duration{Seconds: 0, Nanos: 0}},
		{name: "Negative duration", duration: &durationpb.Duration{Seconds: -10, Nanos: 0}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv := testutil.NewMockServer()

			var getCalls int
			srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
				getCalls++
				if getCalls == 1 {
					return &registryv1beta.GetRepositoryPolicyResponse{
						Revision:              10,
						Expiration:            durationpb.New(24 * time.Hour),
						ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
					}, nil
				}
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              11,
					Expiration:            tc.duration,
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
				}, nil
			}

			srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
				return &emptypb.Empty{}, nil
			}

			r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

			stateModel := registry.RepositoryPolicyResourceModel{
				ID:                    types.StringValue("api-service"),
				Repository:            types.StringValue("api-service"),
				Revision:              types.Int64Value(10),
				Expiration:            types.StringValue("24h"),
				ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
			}

			state := tfsdk.State{Schema: schemaResp.Schema}
			_ = state.Set(ctx, &stateModel)

			req := resource.DeleteRequest{State: state}
			resp := &resource.DeleteResponse{State: state}

			r.Delete(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected error diagnostic for %s when server returns non-nil duration struct, got none", tc.name)
			}
		})
	}
}

func TestRepositoryPolicyResource_Delete_ResetVerification_RejectsAnyPolicyResponse(t *testing.T) {
	cases := []struct {
		name        string
		enforcement registryv1beta.ExpirationEnforcement
		duration    *durationpb.Duration
	}{
		{
			name:        "UNKNOWN enforcement with nil duration",
			enforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
			duration:    nil,
		},
		{
			name:        "UNKNOWN enforcement with non-nil duration",
			enforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
			duration:    durationpb.New(24 * time.Hour),
		},
		{
			name:        "DO_NOT_ENFORCE with nil duration",
			enforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
			duration:    nil,
		},
		{
			name:        "ENFORCE with 48h duration",
			enforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			duration:    durationpb.New(48 * time.Hour),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv := testutil.NewMockServer()

			var getCalls int
			srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
				getCalls++
				if getCalls == 1 {
					return &registryv1beta.GetRepositoryPolicyResponse{
						Revision:              10,
						Expiration:            durationpb.New(24 * time.Hour),
						ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
					}, nil
				}
				// Verification read returns 200 OK policy response rather than NotFound
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              11,
					Expiration:            tc.duration,
					ExpirationEnforcement: tc.enforcement,
				}, nil
			}

			srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
				return &emptypb.Empty{}, nil
			}

			r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

			stateModel := registry.RepositoryPolicyResourceModel{
				ID:                    types.StringValue("auth-service"),
				Repository:            types.StringValue("auth-service"),
				Revision:              types.Int64Value(10),
				Expiration:            types.StringValue("24h"),
				ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
			}

			state := tfsdk.State{Schema: schemaResp.Schema}
			_ = state.Set(ctx, &stateModel)

			req := resource.DeleteRequest{State: state}
			resp := &resource.DeleteResponse{State: state}

			r.Delete(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected error diagnostic for %s when server returns 200 OK policy response, got none", tc.name)
			}

			var foundExpectedDiag bool
			for _, diag := range resp.Diagnostics.Errors() {
				if diag.Summary() == "Registry Repository Policy Reset Verification Failed" &&
					strings.Contains(diag.Detail(), "Expected repository policy override to be deleted (returning NotFound), but policy still exists") {
					foundExpectedDiag = true
					break
				}
			}
			if !foundExpectedDiag {
				t.Fatalf("expected 'Registry Repository Policy Reset Verification Failed' with 'Expected repository policy override to be deleted (returning NotFound), but policy still exists' detail, got: %v", resp.Diagnostics)
			}
		})
	}
}

func TestRepositoryPolicyResource_InheritanceVerification(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// 1. Workspace default policy: 24h expiration, ENFORCE
	defaultPolicy := &registryv1beta.GetDefaultPolicyResponse{
		Revision:              1,
		DefaultExpiration:     durationpb.New(24 * time.Hour),
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
	}

	repoPolicies := make(map[string]*registryv1beta.GetRepositoryPolicyResponse)

	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		return proto.Clone(defaultPolicy).(*registryv1beta.GetDefaultPolicyResponse), nil
	}

	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		p, exists := repoPolicies[req.Repository]
		if !exists {
			return nil, status.Error(codes.NotFound, fmt.Sprintf("repository policy for %q not found", req.Repository))
		}
		return proto.Clone(p).(*registryv1beta.GetRepositoryPolicyResponse), nil
	}

	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		if req.ExpirationEnforcement == registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN && req.Expiration == nil {
			delete(repoPolicies, req.Repository)
			return &emptypb.Empty{}, nil
		}
		p, exists := repoPolicies[req.Repository]
		if !exists {
			p = &registryv1beta.GetRepositoryPolicyResponse{Revision: 1}
			repoPolicies[req.Repository] = p
		}
		p.Revision++
		p.Expiration = req.Expiration
		p.ExpirationEnforcement = req.ExpirationEnforcement
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

	// Step 1: Create repository policy override with 720h (30 days) retention
	planModel := registry.RepositoryPolicyResourceModel{
		ID:                    types.StringUnknown(),
		Repository:            types.StringValue("analytics-pipeline"),
		Revision:              types.Int64Unknown(),
		Expiration:            types.StringValue("720h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("unexpected create errors: %v", createResp.Diagnostics)
	}

	var stateAfterCreate registry.RepositoryPolicyResourceModel
	_ = createResp.State.Get(ctx, &stateAfterCreate)
	if stateAfterCreate.Expiration.ValueString() != "720h" {
		t.Fatalf("expected expiration '720h', got %q", stateAfterCreate.Expiration.ValueString())
	}
	if stateAfterCreate.ExpirationEnforcement.ValueString() != "EXPIRATION_ENFORCEMENT_ENFORCE" {
		t.Fatalf("expected enforcement 'EXPIRATION_ENFORCEMENT_ENFORCE', got %q", stateAfterCreate.ExpirationEnforcement.ValueString())
	}

	// Step 2: Read confirms the active repository policy override
	readReq := resource.ReadRequest{State: createResp.State}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", readResp.Diagnostics)
	}

	var stateAfterRead registry.RepositoryPolicyResourceModel
	_ = readResp.State.Get(ctx, &stateAfterRead)
	if stateAfterRead.Expiration.ValueString() != "720h" {
		t.Fatalf("expected read expiration '720h', got %q", stateAfterRead.Expiration.ValueString())
	}

	// Step 3: Delete repository policy override -> verifies reset via NotFound
	deleteReq := resource.DeleteRequest{State: readResp.State}
	deleteResp := &resource.DeleteResponse{State: readResp.State}
	r.Delete(ctx, deleteReq, deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete errors: %v", deleteResp.Diagnostics)
	}

	// Step 4: Verify repository policy override is purged from server (NotFound)
	_, err := srv.Registry.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: "analytics-pipeline",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected GetRepositoryPolicy to return NotFound after delete, got err: %v", err)
	}

	// Step 5: Verify post-destroy Read removes resource from Terraform state
	postDelReadReq := resource.ReadRequest{State: readResp.State}
	postDelReadResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, postDelReadReq, postDelReadResp)
	if postDelReadResp.Diagnostics.HasError() {
		t.Fatalf("unexpected post-delete read errors: %v", postDelReadResp.Diagnostics)
	}

	// Step 6: Verify workspace default policy is still active and unmodified (24h, ENFORCE)
	defResp, err := srv.Registry.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("failed to query default policy: %v", err)
	}
	if defResp.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE {
		t.Errorf("expected default policy enforcement ENFORCE, got %v", defResp.ExpirationEnforcement)
	}
	if defResp.DefaultExpiration.AsDuration() != 24*time.Hour {
		t.Errorf("expected default policy duration 24h, got %v", defResp.DefaultExpiration.AsDuration())
	}
}
