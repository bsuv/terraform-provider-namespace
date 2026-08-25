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

func setupTestIDTokenEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := iam.NewIDTokenEphemeralResource()

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

func TestIDTokenEphemeralResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := iam.NewIDTokenEphemeralResource()

	var metaResp ephemeral.MetadataResponse
	r.Metadata(ctx, ephemeral.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_id_token" {
		t.Fatalf("expected type name 'namespace_id_token', got %q", metaResp.TypeName)
	}

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"audience", "id_token"}
	for _, name := range expectedAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("missing expected attribute %q", name)
		}
	}

	// Verify sensitive flag
	if !attrs["id_token"].IsSensitive() {
		t.Errorf("expected id_token to be marked Sensitive")
	}
	if !attrs["id_token"].IsComputed() {
		t.Errorf("expected id_token to be Computed")
	}
	if !attrs["audience"].IsRequired() {
		t.Errorf("expected audience to be Required")
	}
}

func TestIDTokenEphemeralResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := iam.NewIDTokenEphemeralResource()

	confR, ok := r.(ephemeral.EphemeralResourceWithConfigure)
	if !ok {
		t.Fatalf("expected EphemeralResourceWithConfigure")
	}

	var confResp ephemeral.ConfigureResponse
	confR.Configure(ctx, ephemeral.ConfigureRequest{
		ProviderData: 12345,
	}, &confResp)

	if !confResp.Diagnostics.HasError() {
		t.Fatalf("expected error on invalid configure provider data")
	}
}

func TestIDTokenEphemeralResource_Open_Success(t *testing.T) {
	srv := testutil.NewMockServer()
	expectedToken := "ey" + "JhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "test_id_token"
	srv.Token.IssueIdTokenFunc = func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
		if req.Audience != "https://vault.example.com" {
			t.Errorf("expected Audience 'https://vault.example.com', got %q", req.Audience)
		}
		return &iamv1beta.IssueIdTokenResponse{
			IdToken: expectedToken,
		}, nil
	}

	r, schemaResp := setupTestIDTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.IDTokenEphemeralModel{
		Audience: types.StringValue("https://vault.example.com"),
		IDToken:  types.StringNull(),
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

	var outputModel iam.IDTokenEphemeralModel
	diags = openResp.Result.Get(ctx, &outputModel)
	if diags.HasError() {
		t.Fatalf("failed to read result: %v", diags)
	}

	if outputModel.IDToken.ValueString() != expectedToken {
		t.Fatalf("expected id_token %q, got %q", expectedToken, outputModel.IDToken.ValueString())
	}
}

func TestIDTokenEphemeralResource_Open_DeferralUnknownInputs(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestIDTokenEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := iam.IDTokenEphemeralModel{
		Audience: types.StringUnknown(),
		IDToken:  types.StringNull(),
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
		t.Fatalf("expected OpenResponse to be deferred on unknown audience")
	}
	if openResp.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
		t.Fatalf("unexpected deferral reason: %v", openResp.Deferred.Reason)
	}
}

func TestIDTokenEphemeralResource_Open_Errors(t *testing.T) {
	tests := []struct {
		name       string
		grpcErr    error
		errSummary string
	}{
		{
			name:       "PermissionDenied",
			grpcErr:    status.Error(codes.PermissionDenied, "token lacks federation permission"),
			errSummary: "Authorization Failure",
		},
		{
			name:       "Unauthenticated",
			grpcErr:    status.Error(codes.Unauthenticated, "invalid tenant token"),
			errSummary: "Authorization Failure",
		},
		{
			name:       "InvalidArgument",
			grpcErr:    status.Error(codes.InvalidArgument, "audience cannot be empty"),
			errSummary: "gRPC Error (InvalidArgument)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewMockServer()
			srv.Token.IssueIdTokenFunc = func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
				return nil, tc.grpcErr
			}

			r, schemaResp := setupTestIDTokenEphemeralResource(t, srv)
			ctx := context.Background()

			inputModel := iam.IDTokenEphemeralModel{
				Audience: types.StringValue("https://app.example.com"),
				IDToken:  types.StringNull(),
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

func TestIDTokenEphemeralResource_Open_UnconfiguredClient(t *testing.T) {
	r := iam.NewIDTokenEphemeralResource()
	ctx := context.Background()

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	inputModel := iam.IDTokenEphemeralModel{
		Audience: types.StringValue("https://app.example.com"),
		IDToken:  types.StringNull(),
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
