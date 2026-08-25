package iam_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// =============================================================================
// CHALLENGER R6 AREA 3: R4 IAM EPHEMERAL UNKNOWN CONTAINER HANDLING
// =============================================================================

func TestEmpirical_R6_R4_TenantToken_EphemeralUnknownContainerMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("Wholly unknown policies list", func(t *testing.T) {
		var apiCalls int32
		srv := testutil.NewMockServer()
		srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
			atomic.AddInt32(&apiCalls, 1)
			return &iamv1beta.IssueTenantTokenResponse{BearerToken: "should-not-reach-server"}, nil
		}

		r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)

		inputModel := iam.TenantTokenEphemeralModel{
			TenantID:     types.StringValue("tenant_123"),
			ActorID:      types.StringValue("actor_456"),
			DurationSecs: types.Int64Value(3600),
			Policies:     types.ListUnknown(iam.TenantTokenPolicyObjectType),
			Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
			BearerToken:  types.StringNull(),
		}

		stateVal := tfsdk.State{Schema: schemaResp.Schema}
		_ = stateVal.Set(ctx, &inputModel)

		// DeferralAllowed = true
		openReq := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)
		if openResp.Diagnostics.HasError() {
			t.Fatalf("unexpected error when DeferralAllowed=true: %v", openResp.Diagnostics)
		}
		if openResp.Deferred == nil || openResp.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
			t.Fatalf("expected deferred response on unknown policies list")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, got %d", apiCalls)
		}

		// DeferralAllowed = false
		openReqFalse := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}
		var openRespFalse ephemeral.OpenResponse
		openRespFalse.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReqFalse, &openRespFalse)
		if !openRespFalse.Diagnostics.HasError() {
			t.Fatal("expected error diagnostics when unknown policies and DeferralAllowed=false")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls on error path, got %d", apiCalls)
		}
	})

	t.Run("Wholly unknown access object", func(t *testing.T) {
		var apiCalls int32
		srv := testutil.NewMockServer()
		srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
			atomic.AddInt32(&apiCalls, 1)
			return &iamv1beta.IssueTenantTokenResponse{BearerToken: "should-not-reach-server"}, nil
		}

		r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)

		inputModel := iam.TenantTokenEphemeralModel{
			TenantID:     types.StringValue("tenant_123"),
			ActorID:      types.StringValue("actor_456"),
			DurationSecs: types.Int64Value(3600),
			Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
			Access:       types.ObjectUnknown(iam.AccessPolicyAttrTypes),
			BearerToken:  types.StringNull(),
		}

		stateVal := tfsdk.State{Schema: schemaResp.Schema}
		_ = stateVal.Set(ctx, &inputModel)

		// DeferralAllowed = true
		openReq := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)
		if openResp.Diagnostics.HasError() {
			t.Fatalf("unexpected error: %v", openResp.Diagnostics)
		}
		if openResp.Deferred == nil {
			t.Fatal("expected deferred on unknown access object")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, got %d", apiCalls)
		}

		// DeferralAllowed = false
		openReqFalse := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}
		var openRespFalse ephemeral.OpenResponse
		openRespFalse.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReqFalse, &openRespFalse)
		if !openRespFalse.Diagnostics.HasError() {
			t.Fatal("expected error diagnostics when unknown access and DeferralAllowed=false")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls on error path, got %d", apiCalls)
		}
	})

	t.Run("Known access object with unknown grants list", func(t *testing.T) {
		var apiCalls int32
		srv := testutil.NewMockServer()
		srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
			atomic.AddInt32(&apiCalls, 1)
			return &iamv1beta.IssueTenantTokenResponse{BearerToken: "should-not-reach-server"}, nil
		}

		r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)

		accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
			Grants: types.ListUnknown(iam.AccessGrantPermissionObjectType),
		})
		if diags.HasError() {
			t.Fatalf("failed to build access object: %v", diags)
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
		_ = stateVal.Set(ctx, &inputModel)

		// DeferralAllowed = true
		openReq := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)
		if openResp.Diagnostics.HasError() {
			t.Fatalf("unexpected error: %v", openResp.Diagnostics)
		}
		if openResp.Deferred == nil {
			t.Fatal("expected deferred on unknown grants list")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, got %d", apiCalls)
		}

		// DeferralAllowed = false
		openReqFalse := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}
		var openRespFalse ephemeral.OpenResponse
		openRespFalse.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReqFalse, &openRespFalse)
		if !openRespFalse.Diagnostics.HasError() {
			t.Fatal("expected error diagnostics when unknown grants and DeferralAllowed=false")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, got %d", apiCalls)
		}
	})

	t.Run("Known grants with unknown actions and elements", func(t *testing.T) {
		var apiCalls int32
		srv := testutil.NewMockServer()
		srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
			atomic.AddInt32(&apiCalls, 1)
			return nil, nil
		}

		r, schemaResp := setupTestTenantTokenEphemeralResource(t, srv)

		// Unknown actions list
		grantsList, diags := types.ListValueFrom(ctx, iam.AccessGrantPermissionObjectType, []iam.PermissionModel{
			{
				ResourceType: types.StringValue("compute"),
				ResourceID:   types.StringValue("vm-1"),
				Actions:      types.ListUnknown(types.StringType),
			},
		})
		if diags.HasError() {
			t.Fatalf("failed to build grants list: %v", diags)
		}

		accessObj, diags := types.ObjectValueFrom(ctx, iam.AccessPolicyAttrTypes, iam.AccessPolicyModel{
			Grants: grantsList,
		})
		if diags.HasError() {
			t.Fatalf("failed to build access object: %v", diags)
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
		_ = stateVal.Set(ctx, &inputModel)

		// DeferralAllowed = true
		openReq := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)
		if openResp.Diagnostics.HasError() || openResp.Deferred == nil {
			t.Fatalf("expected deferred on unknown actions list, diags=%v, deferred=%v", openResp.Diagnostics, openResp.Deferred)
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, got %d", apiCalls)
		}

		// DeferralAllowed = false
		openReqFalse := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}
		var openRespFalse ephemeral.OpenResponse
		openRespFalse.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReqFalse, &openRespFalse)
		if !openRespFalse.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic on unknown actions list with DeferralAllowed=false")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls on error path, got %d", apiCalls)
		}
	})
}

func TestEmpirical_R6_R4_ClientCertificate_EphemeralUnknownContainerMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("Wholly unknown policies list on client certificate", func(t *testing.T) {
		var apiCalls int32
		srv := testutil.NewMockServer()
		srv.Tenant.IssueTenantClientCertificateFunc = func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
			atomic.AddInt32(&apiCalls, 1)
			return nil, nil
		}

		r, schemaResp := setupTestClientCertificateEphemeralResource(t, srv)

		inputModel := iam.ClientCertificateEphemeralModel{
			TenantID:             types.StringValue("tenant_123"),
			ActorID:              types.StringValue("actor_456"),
			DurationSecs:         types.Int64Value(3600),
			PublicKeyPEM:         types.StringNull(),
			Policies:             types.ListUnknown(iam.TenantTokenPolicyObjectType),
			Access:               types.ObjectNull(iam.AccessPolicyAttrTypes),
			ClientCertificatePEM: types.StringNull(),
			PrivateKeyPEM:        types.StringNull(),
		}

		stateVal := tfsdk.State{Schema: schemaResp.Schema}
		_ = stateVal.Set(ctx, &inputModel)

		// DeferralAllowed = true
		openReq := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)
		if openResp.Diagnostics.HasError() || openResp.Deferred == nil {
			t.Fatalf("expected deferral on unknown policies, diags=%v", openResp.Diagnostics)
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, got %d", apiCalls)
		}

		// DeferralAllowed = false
		openReqFalse := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}
		var openRespFalse ephemeral.OpenResponse
		openRespFalse.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReqFalse, &openRespFalse)
		if !openRespFalse.Diagnostics.HasError() {
			t.Fatal("expected error diagnostics on unknown policies with DeferralAllowed=false")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls on error path, got %d", apiCalls)
		}
	})

	t.Run("Wholly unknown access object on client certificate", func(t *testing.T) {
		var apiCalls int32
		srv := testutil.NewMockServer()
		srv.Tenant.IssueTenantClientCertificateFunc = func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
			atomic.AddInt32(&apiCalls, 1)
			return nil, nil
		}

		r, schemaResp := setupTestClientCertificateEphemeralResource(t, srv)

		inputModel := iam.ClientCertificateEphemeralModel{
			TenantID:             types.StringValue("tenant_123"),
			ActorID:              types.StringValue("actor_456"),
			DurationSecs:         types.Int64Value(3600),
			PublicKeyPEM:         types.StringNull(),
			Policies:             types.ListNull(iam.TenantTokenPolicyObjectType),
			Access:               types.ObjectUnknown(iam.AccessPolicyAttrTypes),
			ClientCertificatePEM: types.StringNull(),
			PrivateKeyPEM:        types.StringNull(),
		}

		stateVal := tfsdk.State{Schema: schemaResp.Schema}
		_ = stateVal.Set(ctx, &inputModel)

		// DeferralAllowed = true
		openReq := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)
		if openResp.Diagnostics.HasError() || openResp.Deferred == nil {
			t.Fatalf("expected deferral on unknown access, diags=%v", openResp.Diagnostics)
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, got %d", apiCalls)
		}

		// DeferralAllowed = false
		openReqFalse := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}
		var openRespFalse ephemeral.OpenResponse
		openRespFalse.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReqFalse, &openRespFalse)
		if !openRespFalse.Diagnostics.HasError() {
			t.Fatal("expected error diagnostics on unknown access with DeferralAllowed=false")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls on error path, got %d", apiCalls)
		}
	})

	t.Run("Client-provided public key returns Null private_key_pem", func(t *testing.T) {
		srv := testutil.NewMockServer()
		_, pubPEM := testutil.GenerateTestECDSAPrivateKey(t)
		certPEM, _ := testutil.GenerateTestCertificate(t, "tenant_123")
		srv.Tenant.IssueTenantClientCertificateFunc = func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
			return &iamv1beta.IssueTenantClientCertificateResponse{
				ClientCertificatePem: certPEM,
				PrivateKeyPem:        "",
			}, nil
		}

		r, schemaResp := setupTestClientCertificateEphemeralResource(t, srv)

		inputModel := iam.ClientCertificateEphemeralModel{
			TenantID:             types.StringValue("tenant_123"),
			ActorID:              types.StringValue("actor_456"),
			DurationSecs:         types.Int64Null(),
			PublicKeyPEM:         types.StringValue(pubPEM),
			Policies:             types.ListNull(iam.TenantTokenPolicyObjectType),
			Access:               types.ObjectNull(iam.AccessPolicyAttrTypes),
			ClientCertificatePEM: types.StringNull(),
			PrivateKeyPEM:        types.StringNull(),
		}

		stateVal := tfsdk.State{Schema: schemaResp.Schema}
		_ = stateVal.Set(ctx, &inputModel)

		openReq := ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: stateVal.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)
		if openResp.Diagnostics.HasError() {
			t.Fatalf("unexpected error: %v", openResp.Diagnostics)
		}

		var outputModel iam.ClientCertificateEphemeralModel
		_ = openResp.Result.Get(ctx, &outputModel)

		if !strings.Contains(outputModel.ClientCertificatePEM.ValueString(), "CERTIFICATE") {
			t.Fatalf("expected client_certificate_pem to be populated")
		}
		if !outputModel.PrivateKeyPEM.IsNull() {
			t.Fatalf("expected private_key_pem to be strictly Null, got %q", outputModel.PrivateKeyPEM.ValueString())
		}
	})
}
