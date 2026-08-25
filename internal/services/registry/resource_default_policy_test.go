package registry_test

import (
	"context"
	"strings"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDefaultPolicyResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := registry.NewDefaultPolicyResource()

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

func TestDefaultPolicyResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := registry.NewDefaultPolicyResource()

	var metaResp resource.MetadataResponse
	r.Metadata(ctx, resource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_registry_default_policy" {
		t.Fatalf("expected type name 'namespace_registry_default_policy', got %q", metaResp.TypeName)
	}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"id", "revision", "default_expiration", "expiration_enforcement"}
	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in default policy schema", name)
		}
	}
}

func TestDefaultPolicyResource_ConfigValidators(t *testing.T) {
	ctx := context.Background()
	r := registry.NewDefaultPolicyResource()

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

	// 1. Invalid: ENFORCE without default_expiration
	invalidConfig := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(1),
		DefaultExpiration:     types.StringNull(),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &invalidConfig)

	var resp resource.ValidateConfigResponse
	validators[0].ValidateResource(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: state.Raw},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for ENFORCE without default_expiration, got none")
	}

	// 2. Valid: ENFORCE with default_expiration
	validConfig := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(1),
		DefaultExpiration:     types.StringValue("24h"),
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

func TestDefaultPolicyResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := registry.NewDefaultPolicyResource()

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

func TestDefaultPolicyResource_Create_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCalls int
	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		getCalls++
		if getCalls == 1 {
			return &registryv1beta.GetDefaultPolicyResponse{
				Revision:              10,
				DefaultExpiration:     nil,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
			}, nil
		}
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              11,
			DefaultExpiration:     durationpb.New(24 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	var updateReq *registryv1beta.UpdateDefaultPolicyRequest
	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		updateReq = req
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	planModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringUnknown(),
		Revision:              types.Int64Unknown(),
		DefaultExpiration:     types.StringValue("24h"),
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

	if updateReq == nil || updateReq.Revision != 10 || updateReq.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE {
		t.Fatalf("unexpected UpdateDefaultPolicyRequest: %+v", updateReq)
	}

	var state registry.DefaultPolicyResourceModel
	diags = resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to get response state: %v", diags)
	}

	if state.ID.ValueString() != "default" {
		t.Errorf("expected ID 'default', got %q", state.ID.ValueString())
	}
	if state.Revision.ValueInt64() != 11 {
		t.Errorf("expected revision 11, got %d", state.Revision.ValueInt64())
	}
	if state.ExpirationEnforcement.ValueString() != "EXPIRATION_ENFORCEMENT_ENFORCE" {
		t.Errorf("expected enforcement ENFORCE, got %q", state.ExpirationEnforcement.ValueString())
	}
}

func TestDefaultPolicyResource_Read_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              15,
			DefaultExpiration:     durationpb.New(48 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	stateModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(14),
		DefaultExpiration:     types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
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

	var updatedState registry.DefaultPolicyResourceModel
	diags = resp.State.Get(ctx, &updatedState)
	if diags.HasError() {
		t.Fatalf("failed to get state: %v", diags)
	}

	if updatedState.Revision.ValueInt64() != 15 {
		t.Errorf("expected updated revision 15, got %d", updatedState.Revision.ValueInt64())
	}
}

func TestDefaultPolicyResource_Update_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var updateReq *registryv1beta.UpdateDefaultPolicyRequest
	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		updateReq = req
		return &emptypb.Empty{}, nil
	}

	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              21,
			DefaultExpiration:     durationpb.New(72 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	planModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Unknown(),
		DefaultExpiration:     types.StringValue("72h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}
	stateModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(20),
		DefaultExpiration:     types.StringValue("24h"),
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

	if updateReq == nil || updateReq.Revision != 20 {
		t.Fatalf("expected update called with revision 20 from state, got %+v", updateReq)
	}
}

func TestDefaultPolicyResource_Update_ConcurrencyConflict(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		return nil, status.Error(codes.FailedPrecondition, "revision mismatch / concurrent modification")
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	planModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Unknown(),
		DefaultExpiration:     types.StringValue("72h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}
	stateModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(20),
		DefaultExpiration:     types.StringValue("24h"),
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
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected conflict diagnostic error on FailedPrecondition update, got none")
	}

	foundConflictMsg := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Concurrency Conflict") || strings.Contains(d.Detail(), "re-plan") {
			foundConflictMsg = true
			break
		}
	}
	if !foundConflictMsg {
		t.Errorf("expected concurrency conflict diagnostic, got: %v", resp.Diagnostics)
	}
}

func TestDefaultPolicyResource_Delete_NeutralReset(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCalls int
	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		getCalls++
		if getCalls == 1 {
			return &registryv1beta.GetDefaultPolicyResponse{
				Revision:              50,
				DefaultExpiration:     durationpb.New(24 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}, nil
		}
		// Post-update verification read: return neutral reset
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              51,
			DefaultExpiration:     nil,
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
		}, nil
	}

	var resetReq *registryv1beta.UpdateDefaultPolicyRequest
	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		resetReq = req
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	stateModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(50),
		DefaultExpiration:     types.StringValue("24h"),
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

	if resetReq == nil || resetReq.DefaultExpiration != nil || resetReq.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE {
		t.Fatalf("expected neutral reset update payload, got: %+v", resetReq)
	}
}

func TestDefaultPolicyResource_Delete_ResetVerificationFailure(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCalls int
	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		getCalls++
		if getCalls == 1 {
			return &registryv1beta.GetDefaultPolicyResponse{
				Revision:              50,
				DefaultExpiration:     durationpb.New(24 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}, nil
		}
		// Post-update verification read returns non-neutral state (server failed to reset)
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              51,
			DefaultExpiration:     durationpb.New(12 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	stateModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(50),
		DefaultExpiration:     types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic when server returns non-neutral reset state, got none")
	}
}

func TestDefaultPolicyResource_ImportState_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              33,
			DefaultExpiration:     durationpb.New(48 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithImportState")
	}

	req := resource.ImportStateRequest{ID: "default"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	importR.ImportState(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected import errors: %v", resp.Diagnostics)
	}

	var state registry.DefaultPolicyResourceModel
	diags := resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to read state after import: %v", diags)
	}

	if state.ID.ValueString() != "default" {
		t.Errorf("expected ID 'default', got %q", state.ID.ValueString())
	}
	if state.Revision.ValueInt64() != 33 {
		t.Errorf("expected revision 33, got %d", state.Revision.ValueInt64())
	}
}

func TestDefaultPolicyResource_ImportState_InvalidID(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	importR, ok := r.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("expected resource to implement ResourceWithImportState")
	}

	req := resource.ImportStateRequest{ID: "wrong-id"}
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	importR.ImportState(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostic error for import ID other than 'default', got none")
	}
}

func TestDefaultPolicyResource_Delete_ResetVerification_RejectsZeroAndNegativeDuration(t *testing.T) {
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
			srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
				getCalls++
				if getCalls == 1 {
					return &registryv1beta.GetDefaultPolicyResponse{
						Revision:              50,
						DefaultExpiration:     durationpb.New(24 * time.Hour),
						ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
					}, nil
				}
				return &registryv1beta.GetDefaultPolicyResponse{
					Revision:              51,
					DefaultExpiration:     tc.duration,
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
				}, nil
			}

			srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
				return &emptypb.Empty{}, nil
			}

			r, schemaResp := setupTestDefaultPolicyResource(t, srv)

			stateModel := registry.DefaultPolicyResourceModel{
				ID:                    types.StringValue("default"),
				Revision:              types.Int64Value(50),
				DefaultExpiration:     types.StringValue("24h"),
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
