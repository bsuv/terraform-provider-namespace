package integrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	gradlev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/gradle/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestGradleCacheEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := integrations.NewGradleCacheEphemeralResource()

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

func TestGradleCacheEphemeralResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := integrations.NewGradleCacheEphemeralResource()

	var metaResp ephemeral.MetadataResponse
	r.Metadata(ctx, ephemeral.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_gradle_cache" {
		t.Fatalf("expected type name 'namespace_gradle_cache', got %q", metaResp.TypeName)
	}

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	// Verify mutating ensure side-effect warning in description
	if !strings.Contains(schemaResp.Schema.Description, "Warning: Opening this ephemeral resource invokes an Ensure RPC") {
		t.Errorf("expected Schema.Description to contain mutating ensure side-effect warning")
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{"name", "site", "cache_endpoint_url", "username", "password", "expires_at"}
	for _, name := range expectedAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("missing expected attribute %q", name)
		}
	}

	// Verify sensitive flags
	if !attrs["username"].IsSensitive() {
		t.Errorf("expected username to be marked Sensitive")
	}
	if !attrs["password"].IsSensitive() {
		t.Errorf("expected password to be marked Sensitive")
	}
	if !attrs["name"].IsRequired() {
		t.Errorf("expected name to be Required")
	}
}

func TestGradleCacheEphemeralResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := integrations.NewGradleCacheEphemeralResource()

	confR, ok := r.(ephemeral.EphemeralResourceWithConfigure)
	if !ok {
		t.Fatalf("expected EphemeralResourceWithConfigure")
	}

	var confResp ephemeral.ConfigureResponse
	confR.Configure(ctx, ephemeral.ConfigureRequest{
		ProviderData: 42,
	}, &confResp)

	if !confResp.Diagnostics.HasError() {
		t.Fatalf("expected error on invalid configure provider data")
	}
}

func TestGradleCacheEphemeralResource_Open_Success(t *testing.T) {
	expiryTime := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	srv := testutil.NewMockServer()
	srv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
		if req.Name != "ci-gradle-cache" {
			t.Errorf("expected Name 'ci-gradle-cache', got %q", req.Name)
		}
		if req.Site != "us" {
			t.Errorf("expected Site 'us', got %q", req.Site)
		}
		return &gradlev1beta.EnsureGradleCacheResponse{
			CacheEndpointUrl: "https://gradle.cache.namespaceapis.com/cache/ci-gradle-cache",
			Username:         "user_12345",
			Password:         "secret_pass_67890",
			ExpiresAt:        timestamppb.New(expiryTime),
			Site:             "us",
		}, nil
	}

	r, schemaResp := setupTestGradleCacheEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := integrations.GradleCacheEphemeralModel{
		Name:             types.StringValue("ci-gradle-cache"),
		Site:             types.StringValue("us"),
		CacheEndpointURL: types.StringNull(),
		Username:         types.StringNull(),
		Password:         types.StringNull(),
		ExpiresAt:        types.StringNull(),
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

	var outputModel integrations.GradleCacheEphemeralModel
	diags = openResp.Result.Get(ctx, &outputModel)
	if diags.HasError() {
		t.Fatalf("failed to read result: %v", diags)
	}

	if outputModel.CacheEndpointURL.ValueString() != "https://gradle.cache.namespaceapis.com/cache/ci-gradle-cache" {
		t.Errorf("unexpected CacheEndpointURL: %q", outputModel.CacheEndpointURL.ValueString())
	}
	if outputModel.Username.ValueString() != "user_12345" {
		t.Errorf("unexpected Username: %q", outputModel.Username.ValueString())
	}
	if outputModel.Password.ValueString() != "secret_pass_67890" {
		t.Errorf("unexpected Password: %q", outputModel.Password.ValueString())
	}
	if outputModel.ExpiresAt.ValueString() != expiryTime.Format(time.RFC3339Nano) {
		t.Errorf("unexpected ExpiresAt: %q", outputModel.ExpiresAt.ValueString())
	}
	if outputModel.Site.ValueString() != "us" {
		t.Errorf("unexpected Site: %q", outputModel.Site.ValueString())
	}
}

func TestGradleCacheEphemeralResource_Open_DeferralUnknownInputs(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestGradleCacheEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := integrations.GradleCacheEphemeralModel{
		Name:             types.StringUnknown(),
		Site:             types.StringNull(),
		CacheEndpointURL: types.StringNull(),
		Username:         types.StringNull(),
		Password:         types.StringNull(),
		ExpiresAt:        types.StringNull(),
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
		t.Fatalf("expected OpenResponse to be deferred on unknown name")
	}
	if openResp.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
		t.Fatalf("unexpected deferral reason: %v", openResp.Deferred.Reason)
	}
}

func TestGradleCacheEphemeralResource_Open_DeferralDisabledUnknownInputs(t *testing.T) {
	testCases := []struct {
		name  string
		model integrations.GradleCacheEphemeralModel
	}{
		{
			name: "Unknown Name",
			model: integrations.GradleCacheEphemeralModel{
				Name:             types.StringUnknown(),
				Site:             types.StringValue("us"),
				CacheEndpointURL: types.StringNull(),
				Username:         types.StringNull(),
				Password:         types.StringNull(),
				ExpiresAt:        types.StringNull(),
			},
		},
		{
			name: "Unknown Site",
			model: integrations.GradleCacheEphemeralModel{
				Name:             types.StringValue("ci-gradle-cache"),
				Site:             types.StringUnknown(),
				CacheEndpointURL: types.StringNull(),
				Username:         types.StringNull(),
				Password:         types.StringNull(),
				ExpiresAt:        types.StringNull(),
			},
		},
		{
			name: "Unknown Name and Site",
			model: integrations.GradleCacheEphemeralModel{
				Name:             types.StringUnknown(),
				Site:             types.StringUnknown(),
				CacheEndpointURL: types.StringNull(),
				Username:         types.StringNull(),
				Password:         types.StringNull(),
				ExpiresAt:        types.StringNull(),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewMockServer()
			srv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
				t.Fatalf("EnsureGradleCache should NOT be called when deferral is disabled and inputs are unknown")
				return nil, nil
			}

			r, schemaResp := setupTestGradleCacheEphemeralResource(t, srv)
			ctx := context.Background()

			stateVal := tfsdk.State{Schema: schemaResp.Schema}
			diags := stateVal.Set(ctx, &tc.model)
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
				t.Fatalf("expected error diagnostics when deferral is disabled and inputs are unknown")
			}

			foundExpected := false
			for _, d := range openResp.Diagnostics.Errors() {
				if d.Summary() == "Unknown Ephemeral Resource Configuration" {
					foundExpected = true
					break
				}
			}
			if !foundExpected {
				t.Fatalf("expected summary 'Unknown Ephemeral Resource Configuration', got: %v", openResp.Diagnostics.Errors())
			}
		})
	}
}

func TestGradleCacheEphemeralResource_Open_Errors(t *testing.T) {
	tests := []struct {
		name       string
		grpcErr    error
		errSummary string
	}{
		{
			name:       "Unavailable",
			grpcErr:    status.Error(codes.Unavailable, "gradle service unavailable"),
			errSummary: "gRPC Error (Unavailable)",
		},
		{
			name:       "InvalidArgument",
			grpcErr:    status.Error(codes.InvalidArgument, "name contains invalid characters"),
			errSummary: "gRPC Error (InvalidArgument)",
		},
		{
			name:       "Unauthenticated",
			grpcErr:    status.Error(codes.Unauthenticated, "invalid token"),
			errSummary: "Authorization Failure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewMockServer()
			srv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
				return nil, tc.grpcErr
			}

			r, schemaResp := setupTestGradleCacheEphemeralResource(t, srv)
			ctx := context.Background()

			inputModel := integrations.GradleCacheEphemeralModel{
				Name:             types.StringValue("ci-cache"),
				Site:             types.StringNull(),
				CacheEndpointURL: types.StringNull(),
				Username:         types.StringNull(),
				Password:         types.StringNull(),
				ExpiresAt:        types.StringNull(),
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

func TestGradleCacheEphemeralResource_Open_UnconfiguredClient(t *testing.T) {
	r := integrations.NewGradleCacheEphemeralResource()
	ctx := context.Background()

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	inputModel := integrations.GradleCacheEphemeralModel{
		Name:             types.StringValue("ci-cache"),
		Site:             types.StringNull(),
		CacheEndpointURL: types.StringNull(),
		Username:         types.StringNull(),
		Password:         types.StringNull(),
		ExpiresAt:        types.StringNull(),
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
