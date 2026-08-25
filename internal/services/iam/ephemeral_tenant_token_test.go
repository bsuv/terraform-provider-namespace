package iam_test

import (
	"context"
	"strings"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestTenantTokenEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := iam.NewTenantTokenEphemeralResource()

	ctx := context.Background()
	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	var confResp ephemeral.ConfigureResponse
	if confR, ok := r.(ephemeral.EphemeralResourceWithConfigure); ok {
		confR.Configure(ctx, ephemeral.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return r, schemaResp
}

func TestTenantTokenEphemeralResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := iam.NewTenantTokenEphemeralResource()

	var metaResp ephemeral.MetadataResponse
	r.Metadata(ctx, ephemeral.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_tenant_token" {
		t.Fatalf("expected type name 'namespace_tenant_token', got %q", metaResp.TypeName)
	}

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"tenant_id", "actor_id", "duration_secs", "policies", "access", "bearer_token"}
	for _, name := range expectedAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("missing expected attribute %q", name)
		}
	}

	// Verify sensitive flag
	if !attrs["bearer_token"].IsSensitive() {
		t.Errorf("expected bearer_token to be marked Sensitive")
	}
	if !attrs["bearer_token"].IsComputed() {
		t.Errorf("expected bearer_token to be Computed")
	}
	if !attrs["tenant_id"].IsRequired() {
		t.Errorf("expected tenant_id to be Required")
	}
	if !attrs["actor_id"].IsRequired() {
		t.Errorf("expected actor_id to be Required")
	}
	if !attrs["duration_secs"].IsOptional() {
		t.Errorf("expected duration_secs to be Optional")
	}
}

func TestTenantTokenEphemeralResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := iam.NewTenantTokenEphemeralResource()

	confR, ok := r.(ephemeral.EphemeralResourceWithConfigure)
	if !ok {
		t.Fatalf("expected EphemeralResourceWithConfigure")
	}

	var confResp ephemeral.ConfigureResponse
	confR.Configure(ctx, ephemeral.ConfigureRequest{
		ProviderData: "invalid-client-type",
	}, &confResp)

	if !confResp.Diagnostics.HasError() {
		t.Fatalf("expected error on invalid configure provider data")
	}
}

func TestTenantTokenEphemeralResource_Open_Success(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		if req.TenantId != "tenant_123" {
			t.Errorf("expected TenantId 'tenant_123', got %q", req.TenantId)
		}
		if req.ActorId != "actor_456" {
			t.Errorf("expected ActorId 'actor_456', got %q", req.ActorId)
		}
		if req.DurationSecs != 1800 {
			t.Errorf("expected DurationSecs 1800, got %d", req.DurationSecs)
		}
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: "nsct_" + "generated_secret_token_abc",
		}, nil
	}

	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(1800),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags := stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected Open error: %v", openResp.Diagnostics)
	}
	if openResp.Deferred != nil {
		t.Fatalf("expected result not to be deferred")
	}

	var outputModel iam.TenantTokenEphemeralModel
	diags = openResp.Result.Get(ctx, &outputModel)
	if diags.HasError() {
		t.Fatalf("failed to read result: %v", diags)
	}

	if outputModel.BearerToken.ValueString() != "nsct_"+"generated_secret_token_abc" {
		t.Fatalf("expected bearer token 'nsct_generated_secret_token_abc', got %q", outputModel.BearerToken.ValueString())
	}
}

func TestTenantTokenEphemeralResource_Open_WithPoliciesAndAccess(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		if len(req.Policies) != 1 || req.Policies[0].PolicyType != "custom.policy" {
			t.Errorf("unexpected policies: %+v", req.Policies)
		}
		if req.Access == nil || len(req.Access.Grants) != 1 {
			t.Fatalf("unexpected access grants: %+v", req.Access)
		}
		grant := req.Access.Grants[0]
		if grant.ResourceType != "compute" || grant.ResourceId != "inst_1" || len(grant.Actions) != 2 {
			t.Errorf("unexpected grant fields: %+v", grant)
		}
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: "nsct_" + "policy_scoped_token",
		}, nil
	}

	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	actionsList, diags := types.ListValueFrom(ctx, types.StringType, []string{"read", "write"})
	if diags.HasError() {
		t.Fatalf("failed to create actions list: %v", diags)
	}

	grantsList, diags := types.ListValueFrom(ctx, iam.AccessGrantPermissionObjectType, []iam.PermissionModel{
		{
			ResourceType: types.StringValue("compute"),
			ResourceID:   types.StringValue("inst_1"),
			Actions:      actionsList,
		},
	})
	if diags.HasError() {
		t.Fatalf("failed to create grants list: %v", diags)
	}

	accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
		Grants: grantsList,
	})
	if diags.HasError() {
		t.Fatalf("failed to create access object: %v", diags)
	}

	policiesList, diags := types.ListValueFrom(ctx, iam.TenantTokenPolicyObjectType, []iam.TenantTokenPolicyModel{
		{
			PolicyType: types.StringValue("custom.policy"),
			Value:      types.StringValue(`{"allow": true}`),
		},
	})
	if diags.HasError() {
		t.Fatalf("failed to create policies list: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Null(),
		Policies:     policiesList,
		Access:       accessObj,
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected Open error: %v", openResp.Diagnostics)
	}

	var outputModel iam.TenantTokenEphemeralModel
	diags = openResp.Result.Get(ctx, &outputModel)
	if diags.HasError() {
		t.Fatalf("failed to read result: %v", diags)
	}

	if outputModel.BearerToken.ValueString() != "nsct_"+"policy_scoped_token" {
		t.Fatalf("expected bearer token 'nsct_policy_scoped_token', got %q", outputModel.BearerToken.ValueString())
	}
}

func TestTenantTokenEphemeralResource_Open_DeferralUnknownInputs(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringUnknown(),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Null(),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags := stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil {
		t.Fatalf("expected OpenResponse to be deferred on unknown input")
	}
	if openResp.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
		t.Fatalf("unexpected deferral reason: %v", openResp.Deferred.Reason)
	}
}

func TestTenantTokenEphemeralResource_Open_Errors(t *testing.T) {
	tests := []struct {
		name       string
		grpcErr    error
		errSummary string
	}{
		{
			name:       "PermissionDenied",
			grpcErr:    status.Error(codes.PermissionDenied, "insufficient partner permissions"),
			errSummary: "Authorization Failure",
		},
		{
			name:       "Unauthenticated",
			grpcErr:    status.Error(codes.Unauthenticated, "invalid token"),
			errSummary: "Authorization Failure",
		},
		{
			name:       "InvalidArgument",
			grpcErr:    status.Error(codes.InvalidArgument, "duration_secs cannot be negative"),
			errSummary: "gRPC Error (InvalidArgument)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewMockServer()
			srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
				return nil, tc.grpcErr
			}

			r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
			ctx := context.Background()

			inputModel := iam.TenantTokenEphemeralModel{
				TenantID:     types.StringValue("tenant_123"),
				ActorID:      types.StringValue("actor_456"),
				DurationSecs: types.Int64Value(60),
				Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
				Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
				BearerToken:  types.StringNull(),
			}

			stateVal := tfsdk.State{Schema: schemaResp.Schema}
			diags := stateVal.Set(ctx, &inputModel)
			if diags.HasError() {
				t.Fatalf("failed to prepare config: %v", diags)
			}

			openReq := ephemeral.OpenRequest{
				Config: tfsdk.Config{
					Schema: schemaResp.Schema,
					Raw:    stateVal.Raw,
				},
				ClientCapabilities: ephemeral.OpenClientCapabilities{
					DeferralAllowed: false,
				},
			}
			var openResp ephemeral.OpenResponse
			openResp.Result = tfsdk.EphemeralResultData{
				Schema: schemaResp.Schema,
			}

			r.Open(ctx, openReq, &openResp)
			if !openResp.Diagnostics.HasError() {
				t.Fatalf("expected error diagnostics for %s", tc.name)
			}
			foundSummary := false
			for _, diag := range openResp.Diagnostics.Errors() {
				if strings.Contains(diag.Summary(), tc.errSummary) {
					foundSummary = true
					break
				}
			}
			if !foundSummary {
				t.Fatalf("expected summary to contain %q, got: %v", tc.errSummary, openResp.Diagnostics.Errors())
			}
		})
	}
}

func TestTenantTokenEphemeralResource_Open_UnconfiguredClient(t *testing.T) {
	r := iam.NewTenantTokenEphemeralResource()
	ctx := context.Background()

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(60),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	_ = stateVal.Set(ctx, &inputModel)

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if !openResp.Diagnostics.HasError() {
		t.Fatalf("expected error for unconfigured client")
	}
}

func TestTenantTokenEphemeralResource_Open_DeferralUnknownNestedPolicies(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	policiesList, diags := types.ListValueFrom(ctx, iam.TenantTokenPolicyObjectType, []iam.TenantTokenPolicyModel{
		{
			PolicyType: types.StringValue("namespace.cloud.iam.v1beta.AdminPolicy"),
			Value:      types.StringUnknown(),
		},
	})
	if diags.HasError() {
		t.Fatalf("failed to prepare policies: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     policiesList,
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil {
		t.Fatalf("expected OpenResponse to be deferred on unknown policy value")
	}
}

func TestTenantTokenEphemeralResource_Open_NoDeferralUnknownInput_ReturnsError(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringUnknown(),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags := stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: false, // Deferral not allowed
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if !openResp.Diagnostics.HasError() {
		t.Fatalf("expected diagnostic error when unknown inputs are present and deferral is false")
	}
}

func TestTenantTokenEphemeralResource_Open_WhollyUnknownPolicies_DeferralAllowed(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListUnknown(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags := stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil {
		t.Fatalf("expected OpenResponse to be deferred on wholly unknown policies list")
	}
	if openResp.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
		t.Fatalf("unexpected deferral reason: %v", openResp.Deferred.Reason)
	}
}

func TestTenantTokenEphemeralResource_Open_WhollyUnknownPolicies_DeferralFalse(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListUnknown(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags := stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: false,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if !openResp.Diagnostics.HasError() {
		t.Fatalf("expected diagnostic error when policies list is unknown and deferral is false")
	}
	found := false
	for _, diag := range openResp.Diagnostics.Errors() {
		if strings.Contains(diag.Summary(), "Unknown Ephemeral Resource Configuration") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error summary 'Unknown Ephemeral Resource Configuration', got: %v", openResp.Diagnostics.Errors())
	}
}

func TestTenantTokenEphemeralResource_Open_WhollyUnknownAccess_DeferralAllowed(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectUnknown(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags := stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil {
		t.Fatalf("expected OpenResponse to be deferred on wholly unknown access object")
	}
}

func TestTenantTokenEphemeralResource_Open_WhollyUnknownAccess_DeferralFalse(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectUnknown(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags := stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: false,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if !openResp.Diagnostics.HasError() {
		t.Fatalf("expected diagnostic error when access object is unknown and deferral is false")
	}
}

func TestTenantTokenEphemeralResource_Open_KnownAccess_UnknownGrantsList_DeferralAllowed(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
		Grants: types.ListUnknown(iam.AccessGrantPermissionObjectType),
	})
	if diags.HasError() {
		t.Fatalf("failed to create access object: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       accessObj,
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil {
		t.Fatalf("expected OpenResponse to be deferred on unknown grants list inside access")
	}
}

func TestTenantTokenEphemeralResource_Open_KnownAccess_UnknownGrantsList_DeferralFalse(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
		Grants: types.ListUnknown(iam.AccessGrantPermissionObjectType),
	})
	if diags.HasError() {
		t.Fatalf("failed to create access object: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       accessObj,
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: false,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if !openResp.Diagnostics.HasError() {
		t.Fatalf("expected diagnostic error when grants list is unknown and deferral is false")
	}
	found := false
	for _, diag := range openResp.Diagnostics.Errors() {
		if strings.Contains(diag.Summary(), "Unknown Access Grant Configuration") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error summary 'Unknown Access Grant Configuration', got: %v", openResp.Diagnostics.Errors())
	}
}

func TestTenantTokenEphemeralResource_Open_KnownGrants_UnknownActions_DeferralAllowed(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	grantsList, diags := types.ListValueFrom(ctx, iam.AccessGrantPermissionObjectType, []iam.PermissionModel{
		{
			ResourceType: types.StringValue("compute"),
			ResourceID:   types.StringValue("inst_1"),
			Actions:      types.ListUnknown(types.StringType),
		},
	})
	if diags.HasError() {
		t.Fatalf("failed to create grants list: %v", diags)
	}

	accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
		Grants: grantsList,
	})
	if diags.HasError() {
		t.Fatalf("failed to create access object: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       accessObj,
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil {
		t.Fatalf("expected OpenResponse to be deferred on unknown actions list")
	}
}

func TestTenantTokenEphemeralResource_Open_KnownGrants_UnknownActions_DeferralFalse(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	grantsList, diags := types.ListValueFrom(ctx, iam.AccessGrantPermissionObjectType, []iam.PermissionModel{
		{
			ResourceType: types.StringValue("compute"),
			ResourceID:   types.StringValue("inst_1"),
			Actions:      types.ListUnknown(types.StringType),
		},
	})
	if diags.HasError() {
		t.Fatalf("failed to create grants list: %v", diags)
	}

	accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
		Grants: grantsList,
	})
	if diags.HasError() {
		t.Fatalf("failed to create access object: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       accessObj,
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: false,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if !openResp.Diagnostics.HasError() {
		t.Fatalf("expected diagnostic error when actions list is unknown and deferral is false")
	}
	found := false
	for _, diag := range openResp.Diagnostics.Errors() {
		if strings.Contains(diag.Summary(), "Unknown Access Grant Configuration") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error summary 'Unknown Access Grant Configuration', got: %v", openResp.Diagnostics.Errors())
	}
}

func TestTenantTokenEphemeralResource_Open_KnownGrants_UnknownActionElement_DeferralAllowed(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	actionsList, diags := types.ListValueFrom(ctx, types.StringType, []types.String{types.StringValue("read"), types.StringUnknown()})
	if diags.HasError() {
		t.Fatalf("failed to create actions list: %v", diags)
	}

	grantsList, diags := types.ListValueFrom(ctx, iam.AccessGrantPermissionObjectType, []iam.PermissionModel{
		{
			ResourceType: types.StringValue("compute"),
			ResourceID:   types.StringValue("inst_1"),
			Actions:      actionsList,
		},
	})
	if diags.HasError() {
		t.Fatalf("failed to create grants list: %v", diags)
	}

	accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
		Grants: grantsList,
	})
	if diags.HasError() {
		t.Fatalf("failed to create access object: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       accessObj,
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil {
		t.Fatalf("expected OpenResponse to be deferred on unknown action element string")
	}
}

func TestTenantTokenEphemeralResource_Open_KnownGrants_UnknownActionElement_DeferralFalse(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)
	ctx := context.Background()

	actionsList, diags := types.ListValueFrom(ctx, types.StringType, []types.String{types.StringValue("read"), types.StringUnknown()})
	if diags.HasError() {
		t.Fatalf("failed to create actions list: %v", diags)
	}

	grantsList, diags := types.ListValueFrom(ctx, iam.AccessGrantPermissionObjectType, []iam.PermissionModel{
		{
			ResourceType: types.StringValue("compute"),
			ResourceID:   types.StringValue("inst_1"),
			Actions:      actionsList,
		},
	})
	if diags.HasError() {
		t.Fatalf("failed to create grants list: %v", diags)
	}

	accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
		Grants: grantsList,
	})
	if diags.HasError() {
		t.Fatalf("failed to create access object: %v", diags)
	}

	inputModel := iam.TenantTokenEphemeralModel{
		TenantID:     types.StringValue("tenant_123"),
		ActorID:      types.StringValue("actor_456"),
		DurationSecs: types.Int64Value(3600),
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       accessObj,
		BearerToken:  types.StringNull(),
	}

	stateVal := tfsdk.State{Schema: schemaResp.Schema}
	diags = stateVal.Set(ctx, &inputModel)
	if diags.HasError() {
		t.Fatalf("failed to prepare config: %v", diags)
	}

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{
			Schema: schemaResp.Schema,
			Raw:    stateVal.Raw,
		},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: false,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{
		Schema: schemaResp.Schema,
	}

	r.Open(ctx, openReq, &openResp)
	if !openResp.Diagnostics.HasError() {
		t.Fatalf("expected diagnostic error when action element string is unknown and deferral is false")
	}
	found := false
	for _, diag := range openResp.Diagnostics.Errors() {
		if strings.Contains(diag.Summary(), "Unknown Access Grant Configuration") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error summary 'Unknown Access Grant Configuration', got: %v", openResp.Diagnostics.Errors())
	}
}
