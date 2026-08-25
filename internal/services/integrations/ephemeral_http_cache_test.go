package integrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	httpcachev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/httpcache/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestHTTPCacheEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := integrations.NewHTTPCacheEphemeralResource()

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

func TestHTTPCacheEphemeralResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := integrations.NewHTTPCacheEphemeralResource()

	var metaResp ephemeral.MetadataResponse
	r.Metadata(ctx, ephemeral.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_http_cache" {
		t.Fatalf("expected type name 'namespace_http_cache', got %q", metaResp.TypeName)
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

func TestHTTPCacheEphemeralResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := integrations.NewHTTPCacheEphemeralResource()

	confR, ok := r.(ephemeral.EphemeralResourceWithConfigure)
	if !ok {
		t.Fatalf("expected EphemeralResourceWithConfigure")
	}

	var confResp ephemeral.ConfigureResponse
	confR.Configure(ctx, ephemeral.ConfigureRequest{
		ProviderData: true,
	}, &confResp)

	if !confResp.Diagnostics.HasError() {
		t.Fatalf("expected error on invalid configure provider data")
	}
}

func TestHTTPCacheEphemeralResource_Open_Success(t *testing.T) {
	expiryTime := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	srv := testutil.NewMockServer()
	srv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
		if req.Name != "ci-http-cache" {
			t.Errorf("expected Name 'ci-http-cache', got %q", req.Name)
		}
		if req.Site != "eu" {
			t.Errorf("expected Site 'eu', got %q", req.Site)
		}
		return &httpcachev1beta.EnsureHttpCacheResponse{
			CacheEndpointUrl: "https://http.cache.namespaceapis.com/cache/ci-http-cache",
			Username:         "user_http_12345",
			Password:         "secret_http_67890",
			ExpiresAt:        timestamppb.New(expiryTime),
			Site:             "eu",
		}, nil
	}

	r, schemaResp := setupTestHTTPCacheEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := integrations.HTTPCacheEphemeralModel{
		Name:             types.StringValue("ci-http-cache"),
		Site:             types.StringValue("eu"),
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

	var outputModel integrations.HTTPCacheEphemeralModel
	diags = openResp.Result.Get(ctx, &outputModel)
	if diags.HasError() {
		t.Fatalf("failed to read result: %v", diags)
	}

	if outputModel.CacheEndpointURL.ValueString() != "https://http.cache.namespaceapis.com/cache/ci-http-cache" {
		t.Errorf("unexpected CacheEndpointURL: %q", outputModel.CacheEndpointURL.ValueString())
	}
	if outputModel.Username.ValueString() != "user_http_12345" {
		t.Errorf("unexpected Username: %q", outputModel.Username.ValueString())
	}
	if outputModel.Password.ValueString() != "secret_http_67890" {
		t.Errorf("unexpected Password: %q", outputModel.Password.ValueString())
	}
	if outputModel.ExpiresAt.ValueString() != expiryTime.Format(time.RFC3339Nano) {
		t.Errorf("unexpected ExpiresAt: %q", outputModel.ExpiresAt.ValueString())
	}
	if outputModel.Site.ValueString() != "eu" {
		t.Errorf("unexpected Site: %q", outputModel.Site.ValueString())
	}
}

func TestHTTPCacheEphemeralResource_Open_DeferralUnknownInputs(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestHTTPCacheEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := integrations.HTTPCacheEphemeralModel{
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

func TestHTTPCacheEphemeralResource_Open_DeferralDisabledUnknownInputs(t *testing.T) {
	testCases := []struct {
		name  string
		model integrations.HTTPCacheEphemeralModel
	}{
		{
			name: "Unknown Name",
			model: integrations.HTTPCacheEphemeralModel{
				Name:             types.StringUnknown(),
				Site:             types.StringValue("eu"),
				CacheEndpointURL: types.StringNull(),
				Username:         types.StringNull(),
				Password:         types.StringNull(),
				ExpiresAt:        types.StringNull(),
			},
		},
		{
			name: "Unknown Site",
			model: integrations.HTTPCacheEphemeralModel{
				Name:             types.StringValue("ci-http-cache"),
				Site:             types.StringUnknown(),
				CacheEndpointURL: types.StringNull(),
				Username:         types.StringNull(),
				Password:         types.StringNull(),
				ExpiresAt:        types.StringNull(),
			},
		},
		{
			name: "Unknown Name and Site",
			model: integrations.HTTPCacheEphemeralModel{
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
			srv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
				t.Fatalf("EnsureHttpCache should NOT be called when deferral is disabled and inputs are unknown")
				return nil, nil
			}

			r, schemaResp := setupTestHTTPCacheEphemeralResource(t, srv)
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

func TestHTTPCacheEphemeralResource_Open_Errors(t *testing.T) {
	tests := []struct {
		name       string
		grpcErr    error
		errSummary string
	}{
		{
			name:       "Unavailable",
			grpcErr:    status.Error(codes.Unavailable, "http cache service unavailable"),
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
			srv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
				return nil, tc.grpcErr
			}

			r, schemaResp := setupTestHTTPCacheEphemeralResource(t, srv)
			ctx := context.Background()

			inputModel := integrations.HTTPCacheEphemeralModel{
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

func TestHTTPCacheEphemeralResource_Open_UnconfiguredClient(t *testing.T) {
	r := integrations.NewHTTPCacheEphemeralResource()
	ctx := context.Background()

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	inputModel := integrations.HTTPCacheEphemeralModel{
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
