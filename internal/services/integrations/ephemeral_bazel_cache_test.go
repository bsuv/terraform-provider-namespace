package integrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestBazelCacheEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := integrations.NewBazelCacheEphemeralResource()

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

func TestBazelCacheEphemeralResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := integrations.NewBazelCacheEphemeralResource()

	var metaResp ephemeral.MetadataResponse
	r.Metadata(ctx, ephemeral.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_bazel_cache" {
		t.Fatalf("expected type name 'namespace_bazel_cache', got %q", metaResp.TypeName)
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
	expectedAttrs := []string{
		"key", "version", "site", "enable_remote_asset_api", "experimental_direct_mtls", "experimental_cache_name",
		"cache_endpoint", "https_mtls_cache_endpoint", "https_cache_endpoint", "server_ca_pem",
		"client_cert_pem", "client_key_pem", "expires_at", "credential_helper_domains",
		"build_event_endpoint", "remote_asset_endpoint", "use_workload_mtls",
	}
	for _, name := range expectedAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("missing expected attribute %q", name)
		}
	}

	// Verify sensitive flags
	if !attrs["client_cert_pem"].IsSensitive() {
		t.Errorf("expected client_cert_pem to be marked Sensitive")
	}
	if !attrs["client_key_pem"].IsSensitive() {
		t.Errorf("expected client_key_pem to be marked Sensitive")
	}
	if !attrs["key"].IsRequired() {
		t.Errorf("expected key to be Required")
	}
}

func TestBazelCacheEphemeralResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := integrations.NewBazelCacheEphemeralResource()

	confR, ok := r.(ephemeral.EphemeralResourceWithConfigure)
	if !ok {
		t.Fatalf("expected EphemeralResourceWithConfigure")
	}

	var confResp ephemeral.ConfigureResponse
	confR.Configure(ctx, ephemeral.ConfigureRequest{
		ProviderData: "invalid-type",
	}, &confResp)

	if !confResp.Diagnostics.HasError() {
		t.Fatalf("expected error on invalid configure provider data")
	}
}

func TestBazelCacheEphemeralResource_Open_Success(t *testing.T) {
	expiryTime := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	serverCaPem, _ := testutil.GenerateTestCertificate(t, "bazel.cache.namespaceapis.com")
	clientCertPem, clientKeyPem := testutil.GenerateTestCertificate(t, "bazel-client")

	srv := testutil.NewMockServer()
	srv.BazelCache.EnsureBazelCacheFunc = func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
		if req.Key != "ci" {
			t.Errorf("expected Key 'ci', got %q", req.Key)
		}
		if req.Site != "eu" {
			t.Errorf("expected Site 'eu', got %q", req.Site)
		}
		if !req.EnableRemoteAssetApi {
			t.Errorf("expected EnableRemoteAssetApi true")
		}
		return &bazelv1beta.EnsureBazelCacheResponse{
			CacheEndpoint:           "grpcs://bazel.cache.namespaceapis.com:443",
			HttpsMtlsCacheEndpoint:  "https://mtls.bazel.cache.namespaceapis.com",
			HttpsCacheEndpoint:      "https://bazel.cache.namespaceapis.com",
			ServerCaPem:             serverCaPem,
			ClientCertPem:           clientCertPem,
			ClientKeyPem:            clientKeyPem,
			ExpiresAt:               timestamppb.New(expiryTime),
			CredentialHelperDomains: []string{"bazel.cache.namespaceapis.com", "mtls.bazel.cache.namespaceapis.com"},
			BuildEventEndpoint:      "grpcs://bes.namespaceapis.com:443",
			RemoteAssetEndpoint:     "grpcs://assets.namespaceapis.com:443",
			UseWorkloadMtls:         true,
		}, nil
	}

	r, schemaResp := setupTestBazelCacheEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := integrations.BazelCacheEphemeralModel{
		Key:                     types.StringValue("ci"),
		Version:                 types.Int64Value(2),
		Site:                    types.StringValue("eu"),
		EnableRemoteAssetAPI:    types.BoolValue(true),
		ExperimentalDirectMTLS:  types.BoolValue(false),
		ExperimentalCacheName:   types.StringNull(),
		CacheEndpoint:           types.StringNull(),
		HttpsMtlsCacheEndpoint:  types.StringNull(),
		HttpsCacheEndpoint:      types.StringNull(),
		ServerCAPEM:             types.StringNull(),
		ClientCertPEM:           types.StringNull(),
		ClientKeyPEM:            types.StringNull(),
		ExpiresAt:               types.StringNull(),
		CredentialHelperDomains: types.ListNull(types.StringType),
		BuildEventEndpoint:      types.StringNull(),
		RemoteAssetEndpoint:     types.StringNull(),
		UseWorkloadMTLS:         types.BoolNull(),
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

	var outputModel integrations.BazelCacheEphemeralModel
	diags = openResp.Result.Get(ctx, &outputModel)
	if diags.HasError() {
		t.Fatalf("failed to read result: %v", diags)
	}

	if outputModel.CacheEndpoint.ValueString() != "grpcs://bazel.cache.namespaceapis.com:443" {
		t.Errorf("unexpected CacheEndpoint: %q", outputModel.CacheEndpoint.ValueString())
	}
	if outputModel.ClientCertPEM.ValueString() != clientCertPem {
		t.Errorf("unexpected ClientCertPEM: %q", outputModel.ClientCertPEM.ValueString())
	}
	if outputModel.ClientKeyPEM.ValueString() != clientKeyPem {
		t.Errorf("unexpected ClientKeyPEM: %q", outputModel.ClientKeyPEM.ValueString())
	}
	if outputModel.ExpiresAt.ValueString() != expiryTime.Format(time.RFC3339Nano) {
		t.Errorf("unexpected ExpiresAt: %q", outputModel.ExpiresAt.ValueString())
	}
	if !outputModel.UseWorkloadMTLS.ValueBool() {
		t.Errorf("expected UseWorkloadMTLS true")
	}
}

func TestBazelCacheEphemeralResource_Open_DeferralUnknownInputs(t *testing.T) {
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestBazelCacheEphemeralResource(t, srv)
	ctx := context.Background()

	inputModel := integrations.BazelCacheEphemeralModel{
		Key:                     types.StringUnknown(),
		Version:                 types.Int64Null(),
		Site:                    types.StringNull(),
		EnableRemoteAssetAPI:    types.BoolNull(),
		ExperimentalDirectMTLS:  types.BoolNull(),
		ExperimentalCacheName:   types.StringNull(),
		CacheEndpoint:           types.StringNull(),
		HttpsMtlsCacheEndpoint:  types.StringNull(),
		HttpsCacheEndpoint:      types.StringNull(),
		ServerCAPEM:             types.StringNull(),
		ClientCertPEM:           types.StringNull(),
		ClientKeyPEM:            types.StringNull(),
		ExpiresAt:               types.StringNull(),
		CredentialHelperDomains: types.ListNull(types.StringType),
		BuildEventEndpoint:      types.StringNull(),
		RemoteAssetEndpoint:     types.StringNull(),
		UseWorkloadMTLS:         types.BoolNull(),
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
		t.Fatalf("expected OpenResponse to be deferred on unknown key")
	}
	if openResp.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
		t.Fatalf("unexpected deferral reason: %v", openResp.Deferred.Reason)
	}
}

func TestBazelCacheEphemeralResource_Open_Errors(t *testing.T) {
	tests := []struct {
		name       string
		grpcErr    error
		errSummary string
	}{
		{
			name:       "Unavailable",
			grpcErr:    status.Error(codes.Unavailable, "cache cluster temporarily unreachable"),
			errSummary: "gRPC Error (Unavailable)",
		},
		{
			name:       "InvalidArgument",
			grpcErr:    status.Error(codes.InvalidArgument, "invalid cache key scope"),
			errSummary: "gRPC Error (InvalidArgument)",
		},
		{
			name:       "Unauthenticated",
			grpcErr:    status.Error(codes.Unauthenticated, "expired token"),
			errSummary: "Authorization Failure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewMockServer()
			srv.BazelCache.EnsureBazelCacheFunc = func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
				return nil, tc.grpcErr
			}

			r, schemaResp := setupTestBazelCacheEphemeralResource(t, srv)
			ctx := context.Background()

			inputModel := integrations.BazelCacheEphemeralModel{
				Key:                     types.StringValue("ci"),
				Version:                 types.Int64Null(),
				Site:                    types.StringNull(),
				EnableRemoteAssetAPI:    types.BoolNull(),
				ExperimentalDirectMTLS:  types.BoolNull(),
				ExperimentalCacheName:   types.StringNull(),
				CacheEndpoint:           types.StringNull(),
				HttpsMtlsCacheEndpoint:  types.StringNull(),
				HttpsCacheEndpoint:      types.StringNull(),
				ServerCAPEM:             types.StringNull(),
				ClientCertPEM:           types.StringNull(),
				ClientKeyPEM:            types.StringNull(),
				ExpiresAt:               types.StringNull(),
				CredentialHelperDomains: types.ListNull(types.StringType),
				BuildEventEndpoint:      types.StringNull(),
				RemoteAssetEndpoint:     types.StringNull(),
				UseWorkloadMTLS:         types.BoolNull(),
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

func TestBazelCacheEphemeralResource_Open_UnconfiguredClient(t *testing.T) {
	r := integrations.NewBazelCacheEphemeralResource()
	ctx := context.Background()

	var schemaResp ephemeral.SchemaResponse
	r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	inputModel := integrations.BazelCacheEphemeralModel{
		Key:                     types.StringValue("ci"),
		Version:                 types.Int64Null(),
		Site:                    types.StringNull(),
		EnableRemoteAssetAPI:    types.BoolNull(),
		ExperimentalDirectMTLS:  types.BoolNull(),
		ExperimentalCacheName:   types.StringNull(),
		CacheEndpoint:           types.StringNull(),
		HttpsMtlsCacheEndpoint:  types.StringNull(),
		HttpsCacheEndpoint:      types.StringNull(),
		ServerCAPEM:             types.StringNull(),
		ClientCertPEM:           types.StringNull(),
		ClientKeyPEM:            types.StringNull(),
		ExpiresAt:               types.StringNull(),
		CredentialHelperDomains: types.ListNull(types.StringType),
		BuildEventEndpoint:      types.StringNull(),
		RemoteAssetEndpoint:     types.StringNull(),
		UseWorkloadMTLS:         types.BoolNull(),
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
