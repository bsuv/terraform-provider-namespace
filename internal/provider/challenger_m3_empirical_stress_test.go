package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	gradlev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/gradle/v1beta"
	httpcachev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/httpcache/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
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

func setupTestGradleCacheEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	e := integrations.NewGradleCacheEphemeralResource()

	ctx := context.Background()
	var schemaResp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	var confResp ephemeral.ConfigureResponse
	if confE, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
		confE.Configure(ctx, ephemeral.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return e, schemaResp
}

func setupTestHTTPCacheEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	e := integrations.NewHTTPCacheEphemeralResource()

	ctx := context.Background()
	var schemaResp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	var confResp ephemeral.ConfigureResponse
	if confE, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
		confE.Configure(ctx, ephemeral.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return e, schemaResp
}

func setupTestTenantTokenEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	e := iam.NewTenantTokenEphemeralResource()

	ctx := context.Background()
	var schemaResp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	var confResp ephemeral.ConfigureResponse
	if confE, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
		confE.Configure(ctx, ephemeral.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return e, schemaResp
}

func setupTestIDTokenEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	e := iam.NewIDTokenEphemeralResource()

	ctx := context.Background()
	var schemaResp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	var confResp ephemeral.ConfigureResponse
	if confE, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
		confE.Configure(ctx, ephemeral.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return e, schemaResp
}

func setupTestClientCertificateEphemeralResource(t *testing.T, srv *testutil.MockServer) (ephemeral.EphemeralResource, ephemeral.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	e := iam.NewClientCertificateEphemeralResource()

	ctx := context.Background()
	var schemaResp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	var confResp ephemeral.ConfigureResponse
	if confE, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
		confE.Configure(ctx, ephemeral.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return e, schemaResp
}

func makeBazelCacheEphemeralModel(key types.String) integrations.BazelCacheEphemeralModel {
	return integrations.BazelCacheEphemeralModel{
		Key:                     key,
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
}

func makeGradleCacheEphemeralModel(name types.String) integrations.GradleCacheEphemeralModel {
	return integrations.GradleCacheEphemeralModel{
		Name:             name,
		Site:             types.StringNull(),
		CacheEndpointURL: types.StringNull(),
		Username:         types.StringNull(),
		Password:         types.StringNull(),
		ExpiresAt:        types.StringNull(),
	}
}

func makeHTTPCacheEphemeralModel(name types.String) integrations.HTTPCacheEphemeralModel {
	return integrations.HTTPCacheEphemeralModel{
		Name:             name,
		Site:             types.StringNull(),
		CacheEndpointURL: types.StringNull(),
		Username:         types.StringNull(),
		Password:         types.StringNull(),
		ExpiresAt:        types.StringNull(),
	}
}

func makeTenantTokenEphemeralModel(tenantID, actorID types.String, durationSecs types.Int64) iam.TenantTokenEphemeralModel {
	return iam.TenantTokenEphemeralModel{
		TenantID:     tenantID,
		ActorID:      actorID,
		DurationSecs: durationSecs,
		Policies:     types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:       types.ObjectNull(iam.AccessPolicyAttrTypes),
		BearerToken:  types.StringNull(),
	}
}

func makeIDTokenEphemeralModel(audience types.String) iam.IDTokenEphemeralModel {
	return iam.IDTokenEphemeralModel{
		Audience: audience,
		IDToken:  types.StringNull(),
	}
}

func makeClientCertEphemeralModel(tenantID, actorID types.String, durationSecs types.Int64) iam.ClientCertificateEphemeralModel {
	return iam.ClientCertificateEphemeralModel{
		TenantID:             tenantID,
		ActorID:              actorID,
		DurationSecs:         durationSecs,
		PublicKeyPEM:         types.StringNull(),
		Policies:             types.ListNull(iam.TenantTokenPolicyObjectType),
		Access:               types.ObjectNull(iam.AccessPolicyAttrTypes),
		ClientCertificatePEM: types.StringNull(),
		PrivateKeyPEM:        types.StringNull(),
	}
}

// =============================================================================
// CHALLENGE 1: CONCURRENT EPHEMERAL OPEN OPERATIONS & RACE DETECTION
// =============================================================================

func TestChallenger_Stress_ConcurrentEphemeralOpen_RaceDetection(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var counter int64

	srv.BazelCache.EnsureBazelCacheFunc = func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
		val := atomic.AddInt64(&counter, 1)
		return &bazelv1beta.EnsureBazelCacheResponse{
			CacheEndpoint: fmt.Sprintf("grpcs://bazel-%d.namespaceapis.com:443", val),
			ExpiresAt:     timestamppb.New(time.Now().Add(1 * time.Hour)),
		}, nil
	}

	srv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
		val := atomic.AddInt64(&counter, 1)
		return &gradlev1beta.EnsureGradleCacheResponse{
			CacheEndpointUrl: fmt.Sprintf("https://gradle-%d.namespaceapis.com:443", val),
			Username:         "user",
			Password:         "pass",
			ExpiresAt:        timestamppb.New(time.Now().Add(1 * time.Hour)),
		}, nil
	}

	srv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
		val := atomic.AddInt64(&counter, 1)
		return &httpcachev1beta.EnsureHttpCacheResponse{
			CacheEndpointUrl: fmt.Sprintf("https://http-%d.namespaceapis.com:443", val),
			Username:         "user",
			Password:         "pass",
			ExpiresAt:        timestamppb.New(time.Now().Add(1 * time.Hour)),
		}, nil
	}

	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		val := atomic.AddInt64(&counter, 1)
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: fmt.Sprintf("nsct_tok_%d", val),
		}, nil
	}

	srv.Token.IssueIdTokenFunc = func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
		val := atomic.AddInt64(&counter, 1)
		return &iamv1beta.IssueIdTokenResponse{
			IdToken: fmt.Sprintf("eyJ_id_token_%d", val),
		}, nil
	}

	srv.Tenant.IssueTenantClientCertificateFunc = func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
		val := atomic.AddInt64(&counter, 1)
		return &iamv1beta.IssueTenantClientCertificateResponse{
			ClientCertificatePem: fmt.Sprintf("CERT-%d", val),
			PrivateKeyPem:        fmt.Sprintf("KEY-%d", val),
		}, nil
	}

	bazelRes, bazelSchema := setupTestBazelCacheEphemeralResource(t, srv)
	gradleRes, gradleSchema := setupTestGradleCacheEphemeralResource(t, srv)
	httpRes, httpSchema := setupTestHTTPCacheEphemeralResource(t, srv)
	tokenRes, tokenSchema := setupTestTenantTokenEphemeralResource(t, srv)
	idTokenRes, idTokenSchema := setupTestIDTokenEphemeralResource(t, srv)
	certRes, certSchema := setupTestClientCertificateEphemeralResource(t, srv)

	const goroutinesPerType = 10
	var wg sync.WaitGroup
	wg.Add(goroutinesPerType * 6)

	// 1. Bazel Cache
	for i := 0; i < goroutinesPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			model := makeBazelCacheEphemeralModel(types.StringValue("ci"))
			state := tfsdk.State{Schema: bazelSchema.Schema}
			_ = state.Set(ctx, &model)
			var resp ephemeral.OpenResponse
			resp.Result = tfsdk.EphemeralResultData{Schema: bazelSchema.Schema}
			bazelRes.Open(ctx, ephemeral.OpenRequest{
				Config: tfsdk.Config{Schema: bazelSchema.Schema, Raw: state.Raw},
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("Bazel open %d failed: %v", idx, resp.Diagnostics)
			}
		}(i)
	}

	// 2. Gradle Cache
	for i := 0; i < goroutinesPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			model := makeGradleCacheEphemeralModel(types.StringValue(fmt.Sprintf("gradle-cache-%d", idx)))
			state := tfsdk.State{Schema: gradleSchema.Schema}
			_ = state.Set(ctx, &model)
			var resp ephemeral.OpenResponse
			resp.Result = tfsdk.EphemeralResultData{Schema: gradleSchema.Schema}
			gradleRes.Open(ctx, ephemeral.OpenRequest{
				Config: tfsdk.Config{Schema: gradleSchema.Schema, Raw: state.Raw},
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("Gradle open %d failed: %v", idx, resp.Diagnostics)
			}
		}(i)
	}

	// 3. HTTP Cache
	for i := 0; i < goroutinesPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			model := makeHTTPCacheEphemeralModel(types.StringValue(fmt.Sprintf("http-cache-%d", idx)))
			state := tfsdk.State{Schema: httpSchema.Schema}
			_ = state.Set(ctx, &model)
			var resp ephemeral.OpenResponse
			resp.Result = tfsdk.EphemeralResultData{Schema: httpSchema.Schema}
			httpRes.Open(ctx, ephemeral.OpenRequest{
				Config: tfsdk.Config{Schema: httpSchema.Schema, Raw: state.Raw},
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("HTTP open %d failed: %v", idx, resp.Diagnostics)
			}
		}(i)
	}

	// 4. Tenant Token
	for i := 0; i < goroutinesPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			model := makeTenantTokenEphemeralModel(types.StringValue("test-tenant"), types.StringValue("test-actor"), types.Int64Value(300))
			state := tfsdk.State{Schema: tokenSchema.Schema}
			_ = state.Set(ctx, &model)
			var resp ephemeral.OpenResponse
			resp.Result = tfsdk.EphemeralResultData{Schema: tokenSchema.Schema}
			tokenRes.Open(ctx, ephemeral.OpenRequest{
				Config: tfsdk.Config{Schema: tokenSchema.Schema, Raw: state.Raw},
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("Tenant Token open %d failed: %v", idx, resp.Diagnostics)
			}
		}(i)
	}

	// 5. ID Token
	for i := 0; i < goroutinesPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			model := makeIDTokenEphemeralModel(types.StringValue("vault.ns.ci"))
			state := tfsdk.State{Schema: idTokenSchema.Schema}
			_ = state.Set(ctx, &model)
			var resp ephemeral.OpenResponse
			resp.Result = tfsdk.EphemeralResultData{Schema: idTokenSchema.Schema}
			idTokenRes.Open(ctx, ephemeral.OpenRequest{
				Config: tfsdk.Config{Schema: idTokenSchema.Schema, Raw: state.Raw},
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("ID Token open %d failed: %v", idx, resp.Diagnostics)
			}
		}(i)
	}

	// 6. Client Certificate
	for i := 0; i < goroutinesPerType; i++ {
		go func(idx int) {
			defer wg.Done()
			model := makeClientCertEphemeralModel(types.StringValue("test-tenant"), types.StringValue("test-actor"), types.Int64Value(600))
			state := tfsdk.State{Schema: certSchema.Schema}
			_ = state.Set(ctx, &model)
			var resp ephemeral.OpenResponse
			resp.Result = tfsdk.EphemeralResultData{Schema: certSchema.Schema}
			certRes.Open(ctx, ephemeral.OpenRequest{
				Config: tfsdk.Config{Schema: certSchema.Schema, Raw: state.Raw},
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("Client Certificate open %d failed: %v", idx, resp.Diagnostics)
			}
		}(i)
	}

	wg.Wait()
}

// =============================================================================
// CHALLENGE 2: CACHE DEFERRAL SEMANTICS ACROSS BAZEL, GRADLE, AND HTTP CACHES
// =============================================================================

func TestChallenger_Stress_CacheDeferralSemantics_AllCaches(t *testing.T) {
	ctx := context.Background()

	t.Run("BazelCache_DeferralAndErrors", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.BazelCache.EnsureBazelCacheFunc = func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
			return &bazelv1beta.EnsureBazelCacheResponse{
				CacheEndpoint: "grpcs://bazel.namespaceapis.com:443",
				ExpiresAt:     timestamppb.New(time.Now().Add(1 * time.Hour)),
			}, nil
		}
		res, schema := setupTestBazelCacheEphemeralResource(t, srv)

		// 1. Unknown Key with DeferralAllowed=true -> Deferred
		modelUnknown := makeBazelCacheEphemeralModel(types.StringUnknown())
		state := tfsdk.State{Schema: schema.Schema}
		_ = state.Set(ctx, &modelUnknown)
		var resp1 ephemeral.OpenResponse
		resp1.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}, &resp1)
		if resp1.Deferred == nil || resp1.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
			t.Fatalf("expected Deferred reason Unknown, got: %+v", resp1.Deferred)
		}

		// 2. Unknown Key with DeferralAllowed=false -> Error
		var resp2 ephemeral.OpenResponse
		resp2.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}, &resp2)
		if !resp2.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic when DeferralAllowed=false with unknown key")
		}

		// 3. Known Key with optional unknown direct mTLS with DeferralAllowed=true -> Deferred
		modelPartialUnknown := makeBazelCacheEphemeralModel(types.StringValue("ci"))
		modelPartialUnknown.ExperimentalDirectMTLS = types.BoolUnknown()
		statePartial := tfsdk.State{Schema: schema.Schema}
		_ = statePartial.Set(ctx, &modelPartialUnknown)
		var resp3 ephemeral.OpenResponse
		resp3.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: statePartial.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}, &resp3)
		if resp3.Deferred == nil {
			t.Fatal("expected Deferred response when optional attribute is unknown and DeferralAllowed=true")
		}

		// 4. Known Key with DeferralAllowed=true -> Success
		modelKnown := makeBazelCacheEphemeralModel(types.StringValue("ci"))
		stateKnown := tfsdk.State{Schema: schema.Schema}
		_ = stateKnown.Set(ctx, &modelKnown)
		var resp4 ephemeral.OpenResponse
		resp4.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: stateKnown.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}, &resp4)
		if resp4.Diagnostics.HasError() {
			t.Fatalf("unexpected error on valid Bazel open: %v", resp4.Diagnostics)
		}
		var out integrations.BazelCacheEphemeralModel
		_ = resp4.Result.Get(ctx, &out)
		if out.CacheEndpoint.ValueString() != "grpcs://bazel.namespaceapis.com:443" {
			t.Fatalf("unexpected endpoint: %s", out.CacheEndpoint.ValueString())
		}
	})

	t.Run("GradleCache_DeferralAndErrors", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.GradleCache.EnsureGradleCacheFunc = func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
			return &gradlev1beta.EnsureGradleCacheResponse{
				CacheEndpointUrl: "https://gradle.namespaceapis.com:443",
				Username:         "gradle-user",
				Password:         "gradle-pass",
				ExpiresAt:        timestamppb.New(time.Now().Add(1 * time.Hour)),
			}, nil
		}
		res, schema := setupTestGradleCacheEphemeralResource(t, srv)

		// 1. Unknown Name with DeferralAllowed=true -> Deferred
		modelUnknown := makeGradleCacheEphemeralModel(types.StringUnknown())
		state := tfsdk.State{Schema: schema.Schema}
		_ = state.Set(ctx, &modelUnknown)
		var resp1 ephemeral.OpenResponse
		resp1.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}, &resp1)
		if resp1.Deferred == nil || resp1.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
			t.Fatalf("expected Deferred reason Unknown for Gradle, got: %+v", resp1.Deferred)
		}

		// 2. Unknown Name with DeferralAllowed=false -> Error
		var resp2 ephemeral.OpenResponse
		resp2.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}, &resp2)
		if !resp2.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for Gradle when DeferralAllowed=false with unknown name")
		}

		// 3. Known Name -> Open succeeds
		modelKnown := makeGradleCacheEphemeralModel(types.StringValue("ci-gradle"))
		stateKnown := tfsdk.State{Schema: schema.Schema}
		_ = stateKnown.Set(ctx, &modelKnown)
		var resp3 ephemeral.OpenResponse
		resp3.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config: tfsdk.Config{Schema: schema.Schema, Raw: stateKnown.Raw},
		}, &resp3)
		if resp3.Diagnostics.HasError() {
			t.Fatalf("unexpected error on Gradle open: %v", resp3.Diagnostics)
		}
		var out integrations.GradleCacheEphemeralModel
		_ = resp3.Result.Get(ctx, &out)
		if out.CacheEndpointURL.ValueString() != "https://gradle.namespaceapis.com:443" {
			t.Fatalf("unexpected endpoint: %s", out.CacheEndpointURL.ValueString())
		}
	})

	t.Run("HTTPCache_DeferralAndErrors", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.HttpCache.EnsureHttpCacheFunc = func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
			return &httpcachev1beta.EnsureHttpCacheResponse{
				CacheEndpointUrl: "https://http.namespaceapis.com:443",
				Username:         "http-user",
				Password:         "http-pass",
				ExpiresAt:        timestamppb.New(time.Now().Add(1 * time.Hour)),
			}, nil
		}
		res, schema := setupTestHTTPCacheEphemeralResource(t, srv)

		// 1. Unknown Name with DeferralAllowed=true -> Deferred
		modelUnknown := makeHTTPCacheEphemeralModel(types.StringUnknown())
		state := tfsdk.State{Schema: schema.Schema}
		_ = state.Set(ctx, &modelUnknown)
		var resp1 ephemeral.OpenResponse
		resp1.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: true},
		}, &resp1)
		if resp1.Deferred == nil || resp1.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
			t.Fatalf("expected Deferred reason Unknown for HTTP cache, got: %+v", resp1.Deferred)
		}

		// 2. Unknown Name with DeferralAllowed=false -> Error
		var resp2 ephemeral.OpenResponse
		resp2.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config:             tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
			ClientCapabilities: ephemeral.OpenClientCapabilities{DeferralAllowed: false},
		}, &resp2)
		if !resp2.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for HTTP cache when DeferralAllowed=false with unknown name")
		}

		// 3. Known Name -> Open succeeds
		modelKnown := makeHTTPCacheEphemeralModel(types.StringValue("ci-http"))
		stateKnown := tfsdk.State{Schema: schema.Schema}
		_ = stateKnown.Set(ctx, &modelKnown)
		var resp3 ephemeral.OpenResponse
		resp3.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config: tfsdk.Config{Schema: schema.Schema, Raw: stateKnown.Raw},
		}, &resp3)
		if resp3.Diagnostics.HasError() {
			t.Fatalf("unexpected error on HTTP cache open: %v", resp3.Diagnostics)
		}
		var out integrations.HTTPCacheEphemeralModel
		_ = resp3.Result.Get(ctx, &out)
		if out.CacheEndpointURL.ValueString() != "https://http.namespaceapis.com:443" {
			t.Fatalf("unexpected endpoint: %s", out.CacheEndpointURL.ValueString())
		}
	})
}

// =============================================================================
// CHALLENGE 3: EPHEMERAL LIFETIME BOUNDS & TIME PARSING
// =============================================================================

func TestChallenger_Stress_EphemeralLifetimeBounds_And_Validation(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		if req.DurationSecs < 0 {
			return nil, status.Errorf(codes.InvalidArgument, "duration_secs cannot be negative: %d", req.DurationSecs)
		}
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: fmt.Sprintf("nsct_dur_%d", req.DurationSecs),
		}, nil
	}

	res, schema := setupTestTenantTokenEphemeralResource(t, srv)

	t.Run("NegativeDurationSecs_RejectedByServer", func(t *testing.T) {
		model := makeTenantTokenEphemeralModel(types.StringValue("test-tenant"), types.StringValue("test-actor"), types.Int64Value(-50))
		state := tfsdk.State{Schema: schema.Schema}
		_ = state.Set(ctx, &model)

		var resp ephemeral.OpenResponse
		resp.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
		res.Open(ctx, ephemeral.OpenRequest{
			Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
		}, &resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for negative duration_secs, got none")
		}
	})

	t.Run("ValidDurations_Succeeds", func(t *testing.T) {
		durations := []int64{1, 60, 3600, 86400}
		for _, d := range durations {
			model := makeTenantTokenEphemeralModel(types.StringValue("test-tenant"), types.StringValue("test-actor"), types.Int64Value(d))
			state := tfsdk.State{Schema: schema.Schema}
			_ = state.Set(ctx, &model)

			var resp ephemeral.OpenResponse
			resp.Result = tfsdk.EphemeralResultData{Schema: schema.Schema}
			res.Open(ctx, ephemeral.OpenRequest{
				Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw},
			}, &resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error for duration %d: %v", d, resp.Diagnostics)
			}
			var out iam.TenantTokenEphemeralModel
			_ = resp.Result.Get(ctx, &out)
			expectedTok := fmt.Sprintf("nsct_dur_%d", d)
			if out.BearerToken.ValueString() != expectedTok {
				t.Fatalf("expected bearer token %q, got %q", expectedTok, out.BearerToken.ValueString())
			}
		}
	})
}

// =============================================================================
// CHALLENGE 4: POLICY OPTIMISTIC LOCKING UNDER HIGH CONCURRENCY
// =============================================================================

func TestChallenger_Stress_PolicyOptimisticLocking_RevisionCollisions(t *testing.T) {
	ctx := context.Background()

	t.Run("RepositoryPolicy_50ConcurrentWorkers_SingleWinner", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var mu sync.Mutex
		repoName := "stress-concurrency-repo"
		var currentRevision int64 = 1
		var currentEnforcement = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE

		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              currentRevision,
				Expiration:            durationpb.New(720 * time.Hour),
				ExpirationEnforcement: currentEnforcement,
			}, nil
		}

		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			mu.Lock()
			defer mu.Unlock()
			if req.GetRevision() != currentRevision {
				return nil, status.Errorf(codes.FailedPrecondition, "revision mismatch: requested %d, server is %d", req.GetRevision(), currentRevision)
			}
			currentRevision++
			currentEnforcement = req.GetExpirationEnforcement()
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

		// Base state captured at revision 1
		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue(repoName),
			Repository:            types.StringValue(repoName),
			Expiration:            types.StringValue("720h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
			Revision:              types.Int64Value(1),
		}
		baseState := tfsdk.State{Schema: schemaResp.Schema}
		_ = baseState.Set(ctx, &stateModel)

		const workerCount = 50
		var successCount int64
		var conflictCount int64
		var wg sync.WaitGroup
		wg.Add(workerCount)

		for i := 0; i < workerCount; i++ {
			workerID := i
			go func() {
				defer wg.Done()

				planModel := stateModel
				planModel.Expiration = types.StringValue(fmt.Sprintf("%dh", 100+workerID*10))
				planState := tfsdk.State{Schema: schemaResp.Schema}
				_ = planState.Set(ctx, &planModel)

				updateReq := resource.UpdateRequest{
					Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
					State: baseState,
				}
				updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
				r.Update(ctx, updateReq, updateResp)

				if updateResp.Diagnostics.HasError() {
					for _, d := range updateResp.Diagnostics.Errors() {
						if strings.Contains(d.Summary(), "Concurrency Conflict") ||
							strings.Contains(d.Detail(), "FailedPrecondition") ||
							strings.Contains(d.Detail(), "revision mismatch") {
							atomic.AddInt64(&conflictCount, 1)
							return
						}
					}
					t.Errorf("worker %d got unexpected error: %v", workerID, updateResp.Diagnostics)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}()
		}

		wg.Wait()

		if atomic.LoadInt64(&successCount) != 1 {
			t.Fatalf("expected exactly 1 worker to win optimistic lock, got %d", successCount)
		}
		if atomic.LoadInt64(&conflictCount) != int64(workerCount-1) {
			t.Fatalf("expected %d workers to receive Concurrency Conflict diagnostics, got %d", workerCount-1, conflictCount)
		}
		if currentRevision != 2 {
			t.Fatalf("expected server revision to be 2, got %d", currentRevision)
		}
	})

	t.Run("DefaultPolicy_50ConcurrentWorkers_SingleWinner", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var mu sync.Mutex
		var currentRevision int64 = 1
		var currentEnforcement = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE

		srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			return &registryv1beta.GetDefaultPolicyResponse{
				Revision:              currentRevision,
				DefaultExpiration:     durationpb.New(720 * time.Hour),
				ExpirationEnforcement: currentEnforcement,
			}, nil
		}

		srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
			mu.Lock()
			defer mu.Unlock()
			if req.GetRevision() != currentRevision {
				return nil, status.Errorf(codes.FailedPrecondition, "revision mismatch: requested %d, server is %d", req.GetRevision(), currentRevision)
			}
			currentRevision++
			currentEnforcement = req.GetExpirationEnforcement()
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupTestDefaultPolicyResource(t, srv)

		// Base state captured at revision 1
		stateModel := registry.DefaultPolicyResourceModel{
			ID:                    types.StringValue("default"),
			DefaultExpiration:     types.StringValue("720h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
			Revision:              types.Int64Value(1),
		}
		baseState := tfsdk.State{Schema: schemaResp.Schema}
		_ = baseState.Set(ctx, &stateModel)

		const workerCount = 50
		var successCount int64
		var conflictCount int64
		var wg sync.WaitGroup
		wg.Add(workerCount)

		for i := 0; i < workerCount; i++ {
			workerID := i
			go func() {
				defer wg.Done()

				planModel := stateModel
				planModel.DefaultExpiration = types.StringValue(fmt.Sprintf("%dh", 100+workerID*10))
				planState := tfsdk.State{Schema: schemaResp.Schema}
				_ = planState.Set(ctx, &planModel)

				updateReq := resource.UpdateRequest{
					Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
					State: baseState,
				}
				updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
				r.Update(ctx, updateReq, updateResp)

				if updateResp.Diagnostics.HasError() {
					for _, d := range updateResp.Diagnostics.Errors() {
						if strings.Contains(d.Summary(), "Concurrency Conflict") ||
							strings.Contains(d.Detail(), "FailedPrecondition") ||
							strings.Contains(d.Detail(), "revision mismatch") {
							atomic.AddInt64(&conflictCount, 1)
							return
						}
					}
					t.Errorf("default policy worker %d got unexpected error: %v", workerID, updateResp.Diagnostics)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}()
		}

		wg.Wait()

		if atomic.LoadInt64(&successCount) != 1 {
			t.Fatalf("expected exactly 1 default policy worker to win optimistic lock, got %d", successCount)
		}
		if atomic.LoadInt64(&conflictCount) != int64(workerCount-1) {
			t.Fatalf("expected %d default policy workers to receive Concurrency Conflict diagnostics, got %d", workerCount-1, conflictCount)
		}
		if currentRevision != 2 {
			t.Fatalf("expected server revision to be 2, got %d", currentRevision)
		}
	})
}

// =============================================================================
// CHALLENGE 5: SHARED IMAGE IMMUTABILITY & EXPIRATION CLEANUP
// =============================================================================

func TestChallenger_Stress_SharedImage_Immutability_And_Disappearance(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	shares := make(map[string]*registryv1beta.SharedImage)

	srv.Registry.ShareImageFunc = func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
		mu.Lock()
		defer mu.Unlock()
		shareID := fmt.Sprintf("share_%d", len(shares)+1)
		img := &registryv1beta.SharedImage{
			Id:               shareID,
			SharedRepository: req.Repository,
			SharedDigest:     req.Digest,
			ImageRef:         fmt.Sprintf("ncr.io/share/%s", req.Repository),
			Visibility:       req.Visibility,
			ExpiresAt:        req.ExpiresAt,
			Suffix:           req.Suffix,
		}
		shares[shareID] = img
		return img, nil
	}

	srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var list []*registryv1beta.SharedImage
		for _, img := range shares {
			list = append(list, img)
		}
		return &registryv1beta.ListSharedImagesResponse{SharedImages: list}, nil
	}

	srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(shares, req.Id)
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	// Step 1: Create initial share
	createPlan := registry.SharedImageResourceModel{
		ID:               types.StringUnknown(),
		Repository:       types.StringValue("repo-1"),
		Digest:           types.StringValue("sha256:1111111111111111111111111111111111111111111111111111111111111111"),
		Visibility:       types.StringValue("PUBLIC"),
		ExpiresAt:        types.StringNull(),
		Suffix:           types.StringNull(),
		SharedRepository: types.StringNull(),
		SharedDigest:     types.StringNull(),
		ImageRef:         types.StringNull(),
	}
	createPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = createPlanState.Set(ctx, &createPlan)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics)
	}

	var stateAfterCreate registry.SharedImageResourceModel
	_ = createResp.State.Get(ctx, &stateAfterCreate)
	if stateAfterCreate.ID.ValueString() != "share_1" {
		t.Fatalf("expected ID share_1, got %q", stateAfterCreate.ID.ValueString())
	}

	// Step 2: Attempt Update -> must fail with Update Not Supported
	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw},
		State: createResp.State,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic on shared image update")
	}

	// Step 3: Out-of-band expiration / unshare -> Read removes resource cleanly
	mu.Lock()
	delete(shares, "share_1")
	mu.Unlock()

	readReq := resource.ReadRequest{State: createResp.State}
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Read(ctx, readReq, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected read error on expired share: %v", readResp.Diagnostics)
	}
	if !readResp.State.Raw.IsNull() {
		t.Fatal("expected state to be removed (null) after out-of-band unsharing")
	}
}

// =============================================================================
// CHALLENGE 6: AUTHENTICATION PRECEDENCE & ERROR DIAGNOSTICS
// =============================================================================

func TestChallenger_Stress_AuthPrecedence_AdversarialScenarios(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	tokenFile := filepath.Join(tmpDir, "valid_token.json")
	if err := os.WriteFile(tokenFile, []byte(`{"bearer_token": "valid-token-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}

	malformedTokenFile := filepath.Join(tmpDir, "bad_token.json")
	if err := os.WriteFile(malformedTokenFile, []byte(`not-json`), 0600); err != nil {
		t.Fatal(err)
	}

	missingFile := filepath.Join(tmpDir, "non_existent_file.json")

	t.Run("MissingTokenFile_ReturnsError", func(t *testing.T) {
		_, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			TokenFile: missingFile,
		})
		if err == nil {
			t.Fatal("expected error for non-existent token file, got nil")
		}
	})

	t.Run("MalformedTokenFile_FailsOnIssue", func(t *testing.T) {
		ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			TokenFile: malformedTokenFile,
		})
		if err != nil {
			// Some implementations fail immediately in ResolveTokenSource, which is valid
			return
		}
		_, err = ts.IssueToken(ctx, time.Minute, false)
		if err == nil {
			t.Fatal("expected error issuing token from malformed token file, got nil")
		}
	})

	t.Run("TokenOverridesEnvVars", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "env-tok")
		t.Setenv("NSC_TOKEN_FILE", tokenFile)

		ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			Token: "winner-token",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tok, err := ts.IssueToken(ctx, time.Minute, false)
		if err != nil || tok != "winner-token" {
			t.Fatalf("expected 'winner-token', got tok=%v, err=%v", tok, err)
		}
	})
}
