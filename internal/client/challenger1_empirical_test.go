package client_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	nsProvider "github.com/bsuv/terraform-provider-namespace/internal/provider"
	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

// =============================================================================
// AREA 1: REMOTE VS LOCAL DEADLINEEXCEEDED RETRY ENGINE STRESS TEST
// =============================================================================

func TestEmpirical_Area1_RetryEngine_RemoteVsLocalDeadlineExceeded(t *testing.T) {
	ctx := context.Background()

	t.Run("Remote gRPC DeadlineExceeded terminates in exactly 1 attempt", func(t *testing.T) {
		var attempts int32
		var sleepCalls int32

		policy := client.DefaultReadPolicy()
		policy.Sleep = func(ctx context.Context, d time.Duration) error {
			atomic.AddInt32(&sleepCalls, 1)
			return nil
		}

		remoteErr := status.Error(codes.DeadlineExceeded, "remote server downstream timeout")
		_, err := client.ExecuteWithRetry(ctx, policy, func(callCtx context.Context) (string, error) {
			atomic.AddInt32(&attempts, 1)
			return "", remoteErr
		})

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt for remote DeadlineExceeded, got %d", attempts)
		}
		if sleepCalls != 0 {
			t.Fatalf("expected 0 sleep calls for remote DeadlineExceeded, got %d", sleepCalls)
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.DeadlineExceeded {
			t.Fatalf("expected DeadlineExceeded status error, got %v", err)
		}
	})

	t.Run("Local attempt context timeout retries with exponential backoff when budget permits", func(t *testing.T) {
		var attempts int32
		var recordedDelays []time.Duration

		mockSleep := func(ctx context.Context, d time.Duration) error {
			recordedDelays = append(recordedDelays, d)
			return nil
		}

		policy := client.RetryPolicy{
			MaxAttempts:      4,
			InitialInterval:  100 * time.Millisecond,
			MaxInterval:      1 * time.Second,
			AttemptTimeout:   10 * time.Millisecond,
			OperationTimeout: 5 * time.Second,
			IsIdempotent:     true,
			Sleep:            mockSleep,
		}

		res, err := client.ExecuteWithRetry(ctx, policy, func(attemptCtx context.Context) (string, error) {
			att := atomic.AddInt32(&attempts, 1)
			if att < 4 {
				// Simulate slow call exceeding per-attempt timeout
				<-attemptCtx.Done()
				return "", attemptCtx.Err()
			}
			return "recovered_on_attempt_4", nil
		})

		if err != nil {
			t.Fatalf("expected successful recovery on attempt 4, got err: %v", err)
		}
		if res != "recovered_on_attempt_4" {
			t.Fatalf("unexpected result: %q", res)
		}
		if attempts != 4 {
			t.Fatalf("expected exactly 4 attempts, got %d", attempts)
		}
		if len(recordedDelays) != 3 {
			t.Fatalf("expected 3 sleep delays for 4 attempts, got %d", len(recordedDelays))
		}
		// Verify full jitter bounds for backoff
		for i, d := range recordedDelays {
			if d < 0 {
				t.Errorf("delay %d cannot be negative: %v", i, d)
			}
		}
	})

	t.Run("Overall operation timeout stops local retry loop immediately", func(t *testing.T) {
		var attempts int32

		policy := client.RetryPolicy{
			MaxAttempts:      10,
			AttemptTimeout:   50 * time.Millisecond,
			OperationTimeout: 80 * time.Millisecond, // Budget expires after ~1-2 attempts
			IsIdempotent:     true,
			Sleep: func(ctx context.Context, d time.Duration) error {
				time.Sleep(40 * time.Millisecond)
				return ctx.Err()
			},
		}

		_, err := client.ExecuteWithRetry(ctx, policy, func(attemptCtx context.Context) (string, error) {
			atomic.AddInt32(&attempts, 1)
			<-attemptCtx.Done()
			return "", attemptCtx.Err()
		})

		if err == nil {
			t.Fatal("expected error when operation budget is exhausted, got nil")
		}
		if attempts >= 10 {
			t.Fatalf("expected retry loop to abort before 10 attempts due to operation timeout, got %d", attempts)
		}
	})

	t.Run("Mutating policy with MaxAttempts=1 never retries on local or remote failure", func(t *testing.T) {
		var attempts int32
		policy := client.DefaultMutatingPolicy()
		policy.Sleep = func(ctx context.Context, d time.Duration) error {
			t.Fatal("sleep must not be called for mutating policy")
			return nil
		}

		_, err := client.ExecuteWithRetry(ctx, policy, func(attemptCtx context.Context) (string, error) {
			atomic.AddInt32(&attempts, 1)
			return "", status.Error(codes.Unavailable, "temporary outage")
		})

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt for mutating policy, got %d", attempts)
		}
	})
}

// =============================================================================
// AREA 2: REGISTRY PAGINATION 2-MINUTE DEADLINE STRESS TEST
// =============================================================================

func TestEmpirical_Area2_Pagination_2MinuteDeadline(t *testing.T) {
	ctx := context.Background()

	t.Run("DefaultPaginationTimeout is strictly 2 minutes", func(t *testing.T) {
		if tfutil.DefaultPaginationTimeout != 2*time.Minute {
			t.Fatalf("expected DefaultPaginationTimeout to be 2m, got %v", tfutil.DefaultPaginationTimeout)
		}
		if client.DefaultPaginatedReadPolicy().OperationTimeout != 2*time.Minute {
			t.Fatalf("expected DefaultPaginatedReadPolicy OperationTimeout to be 2m, got %v", client.DefaultPaginatedReadPolicy().OperationTimeout)
		}
	})

	t.Run("Multi-page requests with many pages do not exceed configured timeout across all pages", func(t *testing.T) {
		const simulatedTimeout = 100 * time.Millisecond
		var pagesServed int32

		// Each page takes 25ms; after ~4 pages, timeout expires
		fetcher := func(fetchCtx context.Context, pageToken string) ([]string, string, error) {
			select {
			case <-fetchCtx.Done():
				return nil, "", fetchCtx.Err()
			case <-time.After(25 * time.Millisecond):
			}
			pg := atomic.AddInt32(&pagesServed, 1)
			return []string{fmt.Sprintf("item_%d", pg)}, fmt.Sprintf("cursor_%d", pg), nil
		}

		start := time.Now()
		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithTimeout(simulatedTimeout))
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", err)
		}
		if elapsed > simulatedTimeout+150*time.Millisecond {
			t.Fatalf("pagination exceeded deadline window: elapsed %v > timeout %v", elapsed, simulatedTimeout)
		}
		if pagesServed > 6 {
			t.Fatalf("expected at most 4-5 pages served before deadline, got %d", pagesServed)
		}
	})

	t.Run("Registry images data source drains multi-page images bounded by pagination timeout", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var pagesFetched int32

		srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
			pg := atomic.AddInt32(&pagesFetched, 1)
			var nextCursor []byte
			if pg < 5 {
				nextCursor = []byte(fmt.Sprintf("cursor_%d", pg))
			}
			return &registryv1beta.ListImagesResponse{
				PaginationCursor: nextCursor,
				Images: []*registryv1beta.Image{
					{
						Repository: "test-repo",
						Digest:     fmt.Sprintf("sha256:img_%d", pg),
					},
				},
			}, nil
		}

		cl := testutil.NewTestClient(t, srv)
		domCli := registry.NewDomainClient(cl)

		var allImages []*registryv1beta.Image
		fetcher := func(fetchCtx context.Context, pageToken string) ([]*registryv1beta.Image, string, error) {
			rpcReq := &registryv1beta.ListImagesRequest{}
			if pageToken != "" {
				rpcReq.PaginationCursor = []byte(pageToken)
			}
			rpcResp, err := domCli.ListImages(fetchCtx, rpcReq)
			if err != nil {
				return nil, "", err
			}
			var nextCursor string
			if len(rpcResp.GetPaginationCursor()) > 0 {
				nextCursor = string(rpcResp.GetPaginationCursor())
			}
			return rpcResp.GetImages(), nextCursor, nil
		}

		items, err := tfutil.PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("unexpected pagination error: %v", err)
		}
		allImages = append(allImages, items...)

		if len(allImages) != 5 {
			t.Fatalf("expected 5 images across 5 pages, got %d", len(allImages))
		}
		if pagesFetched != 5 {
			t.Fatalf("expected 5 pages fetched, got %d", pagesFetched)
		}
	})
}

// =============================================================================
// AREA 3: EAGER TOKEN VALIDATION STRESS TEST
// =============================================================================

func TestEmpirical_Area3_EagerTokenValidation(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFiles := []struct {
		name        string
		content     string
		errContains string
	}{
		{
			name:        "Empty file (0 bytes)",
			content:     "",
			errContains: "empty",
		},
		{
			name:        "Whitespace only (spaces, tabs, newlines)",
			content:     "   \t\r\n \n\t  ",
			errContains: "empty",
		},
		{
			name:        "Corrupt JSON (truncated syntax)",
			content:     `{"bearer_token": "nsct_123`,
			errContains: "malformed JSON",
		},
		{
			name:        "Corrupt JSON (plain text)",
			content:     `plain text not json`,
			errContains: "malformed JSON",
		},
		{
			name:        "Corrupt JSON (array instead of object)",
			content:     `["token1", "token2"]`,
			errContains: "malformed JSON",
		},
		{
			name:        "Missing bearer_token key",
			content:     `{"access_token": "nsct_123", "token_type": "Bearer"}`,
			errContains: "bearer_token",
		},
		{
			name:        "Empty bearer_token value",
			content:     `{"bearer_token": ""}`,
			errContains: "bearer_token",
		},
		{
			name:        "Whitespace-only bearer_token value",
			content:     `{"bearer_token": "   \t\n "}`,
			errContains: "bearer_token",
		},
	}

	for _, tc := range testFiles {
		t.Run("ResolveTokenSource_"+tc.name, func(t *testing.T) {
			filePath := filepath.Join(tmpDir, strings.ReplaceAll(tc.name, " ", "_")+".json")
			if err := os.WriteFile(filePath, []byte(tc.content), 0600); err != nil {
				t.Fatalf("failed to create test file: %v", err)
			}

			// Test via AuthConfig.TokenFile
			cfg := client.AuthConfig{
				TokenFile: filePath,
			}
			_, err := client.ResolveTokenSource(ctx, cfg)
			if err == nil {
				t.Fatalf("expected eager validation error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got: %v", tc.errContains, err)
			}

			// Test via NSC_TOKEN_FILE environment variable
			t.Setenv("NSC_TOKEN", "")
			t.Setenv("NSC_TOKEN_FILE", filePath)
			_, errEnv := client.ResolveTokenSource(ctx, client.AuthConfig{})
			if errEnv == nil {
				t.Fatalf("expected eager validation error via NSC_TOKEN_FILE for %s, got nil", tc.name)
			}
			if !strings.Contains(errEnv.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got: %v", tc.errContains, errEnv)
			}
		})

		t.Run("ProviderConfigure_"+tc.name, func(t *testing.T) {
			t.Setenv("NSC_TOKEN", "")
			t.Setenv("NSC_TOKEN_FILE", "")

			filePath := filepath.Join(tmpDir, "p_"+strings.ReplaceAll(tc.name, " ", "_")+".json")
			if err := os.WriteFile(filePath, []byte(tc.content), 0600); err != nil {
				t.Fatalf("failed to create test file: %v", err)
			}

			p := nsProvider.New("dev")()
			req := provider.ConfigureRequest{
				Config: makeEmpiricalProviderConfig(ctx, p, map[string]interface{}{
					"token_file": filePath,
				}),
			}
			resp := &provider.ConfigureResponse{}
			p.Configure(ctx, req, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected Configure to fail eagerly for %s, got no errors", tc.name)
			}
			diagDetail := resp.Diagnostics[0].Detail()
			if !strings.Contains(diagDetail, tc.errContains) {
				t.Fatalf("expected diagnostic detail containing %q, got: %s", tc.errContains, diagDetail)
			}
		})
	}
}

// =============================================================================
// AREA 4: EPHEMERAL UNKNOWN INPUTS STRESS TEST
// =============================================================================

func TestEmpirical_Area4_EphemeralUnknownInputs(t *testing.T) {
	ctx := context.Background()

	t.Run("namespace_id_token rejects unknown audience when DeferralAllowed == false without API calls", func(t *testing.T) {
		var apiCalls int32
		srv := testutil.NewMockServer()
		srv.Token.IssueIdTokenFunc = func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
			atomic.AddInt32(&apiCalls, 1)
			return &iamv1beta.IssueIdTokenResponse{IdToken: "should-not-be-called"}, nil
		}

		cl := testutil.NewTestClient(t, srv)
		r := iam.NewIDTokenEphemeralResource()

		var schemaResp ephemeral.SchemaResponse
		r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

		if confR, ok := r.(ephemeral.EphemeralResourceWithConfigure); ok {
			var confResp ephemeral.ConfigureResponse
			confR.Configure(ctx, ephemeral.ConfigureRequest{ProviderData: cl}, &confResp)
			if confResp.Diagnostics.HasError() {
				t.Fatalf("unexpected configure error: %v", confResp.Diagnostics)
			}
		}

		inputModel := iam.IDTokenEphemeralModel{
			Audience: types.StringUnknown(),
			IDToken:  types.StringNull(),
		}
		stateVal := tfsdk.State{Schema: schemaResp.Schema}
		_ = stateVal.Set(ctx, &inputModel)

		openReq := ephemeral.OpenRequest{
			Config: tfsdk.Config{
				Schema: schemaResp.Schema,
				Raw:    stateVal.Raw,
			},
			ClientCapabilities: ephemeral.OpenClientCapabilities{
				DeferralAllowed: false, // Deferral disabled!
			},
		}
		var openResp ephemeral.OpenResponse
		openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

		r.Open(ctx, openReq, &openResp)

		if !openResp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostics when Audience is unknown and DeferralAllowed == false")
		}
		if openResp.Deferred != nil {
			t.Fatal("expected Deferred to be nil when DeferralAllowed == false")
		}
		if atomic.LoadInt32(&apiCalls) != 0 {
			t.Fatalf("expected 0 API calls, but IssueIdToken was called %d times", apiCalls)
		}
		if !strings.Contains(openResp.Diagnostics[0].Summary(), "Unknown Ephemeral Resource Configuration") {
			t.Errorf("expected 'Unknown Ephemeral Resource Configuration' summary, got: %v", openResp.Diagnostics[0].Summary())
		}
	})

	t.Run("namespace_bazel_cache rejects unknown inputs when DeferralAllowed == false without API calls", func(t *testing.T) {
		unknownFields := []struct {
			name  string
			model integrations.BazelCacheEphemeralModel
		}{
			{
				name: "Unknown Key",
				model: integrations.BazelCacheEphemeralModel{
					Key: types.StringUnknown(),
				},
			},
			{
				name: "Unknown Version",
				model: integrations.BazelCacheEphemeralModel{
					Key:     types.StringValue("ci"),
					Version: types.Int64Unknown(),
				},
			},
			{
				name: "Unknown Site",
				model: integrations.BazelCacheEphemeralModel{
					Key:  types.StringValue("ci"),
					Site: types.StringUnknown(),
				},
			},
			{
				name: "Unknown EnableRemoteAssetAPI",
				model: integrations.BazelCacheEphemeralModel{
					Key:                  types.StringValue("ci"),
					EnableRemoteAssetAPI: types.BoolUnknown(),
				},
			},
			{
				name: "Unknown ExperimentalDirectMTLS",
				model: integrations.BazelCacheEphemeralModel{
					Key:                    types.StringValue("ci"),
					ExperimentalDirectMTLS: types.BoolUnknown(),
				},
			},
			{
				name: "Unknown ExperimentalCacheName",
				model: integrations.BazelCacheEphemeralModel{
					Key:                   types.StringValue("ci"),
					ExperimentalCacheName: types.StringUnknown(),
				},
			},
		}

		for _, tc := range unknownFields {
			t.Run(tc.name, func(t *testing.T) {
				var apiCalls int32
				srv := testutil.NewMockServer()
				srv.BazelCache.EnsureBazelCacheFunc = func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
					atomic.AddInt32(&apiCalls, 1)
					return nil, nil
				}

				cl := testutil.NewTestClient(t, srv)
				r := integrations.NewBazelCacheEphemeralResource()

				var schemaResp ephemeral.SchemaResponse
				r.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

				if confR, ok := r.(ephemeral.EphemeralResourceWithConfigure); ok {
					var confResp ephemeral.ConfigureResponse
					confR.Configure(ctx, ephemeral.ConfigureRequest{ProviderData: cl}, &confResp)
					if confResp.Diagnostics.HasError() {
						t.Fatalf("unexpected configure error: %v", confResp.Diagnostics)
					}
				}

				stateVal := tfsdk.State{Schema: schemaResp.Schema}
				_ = stateVal.Set(ctx, &tc.model)

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
				openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}

				r.Open(ctx, openReq, &openResp)

				if !openResp.Diagnostics.HasError() {
					t.Fatalf("expected error diagnostics for %s when DeferralAllowed == false", tc.name)
				}
				if openResp.Deferred != nil {
					t.Fatalf("expected Deferred to be nil for %s when DeferralAllowed == false", tc.name)
				}
				if atomic.LoadInt32(&apiCalls) != 0 {
					t.Fatalf("expected 0 API calls for %s, but EnsureBazelCache was called %d times", tc.name, apiCalls)
				}
			})
		}
	})
}

// =============================================================================
// AREA 5: ENDPOINT URL VALIDATOR STRESS TEST
// =============================================================================

func TestEmpirical_Area5_EndpointURLValidator(t *testing.T) {
	ctx := context.Background()
	p := path.Root("endpoint")
	v := tfutil.ValidEndpoint()

	adversarialCases := []struct {
		name        string
		endpoint    string
		expectError bool
		errContains string
	}{
		// Valid cases
		{"Valid HTTPS standard", "https://global.namespaceapis.com", false, ""},
		{"Valid HTTPS standard port", "https://global.namespaceapis.com:443", false, ""},
		{"Valid HTTPS custom port", "https://eu.compute.namespaceapis.com:8443", false, ""},
		{"Valid HTTPS IP port", "https://127.0.0.1:8443", false, ""},
		{"Valid bare host port", "global.namespaceapis.com:443", false, ""},
		{"Valid bare host no port", "global.namespaceapis.com", false, ""},
		{"Valid localhost port", "localhost:9000", false, ""},

		// Userinfo cases
		{"HTTPS with user:pass", "https://user:pass@global.namespaceapis.com", true, "userinfo"},
		{"HTTPS with user only", "https://user@global.namespaceapis.com:443", true, "userinfo"},
		{"Bare host with user:pass", "user:pass@global.namespaceapis.com:443", true, "userinfo"},
		{"Bare host with user only", "admin@global.namespaceapis.com", true, "userinfo"},

		// Query string cases
		{"HTTPS with query string", "https://global.namespaceapis.com?token=abc", true, "query"},
		{"HTTPS with multiple query params", "https://global.namespaceapis.com:443?a=1&b=2", true, "query"},
		{"Bare host with query string", "global.namespaceapis.com:443?token=abc", true, "query"},

		// Fragment cases
		{"HTTPS with fragment", "https://global.namespaceapis.com#sec", true, "fragment"},
		{"HTTPS with port and fragment", "https://global.namespaceapis.com:443#sec", true, "fragment"},
		{"Bare host with fragment", "global.namespaceapis.com:443#frag", true, "fragment"},

		// Relative paths / path segments
		{"HTTPS with api path", "https://global.namespaceapis.com/api/v1", true, "path"},
		{"HTTPS with simple path", "https://global.namespaceapis.com/path", true, "path"},
		{"Bare host with path", "global.namespaceapis.com/path", true, "path"},
		{"Relative path only", "/path/only", true, "path"},

		// Non-HTTP/HTTPS schemes
		{"Plain HTTP scheme", "http://global.namespaceapis.com", true, "HTTPS"},
		{"FTP scheme", "ftp://global.namespaceapis.com", true, "HTTPS"},
		{"gRPC scheme", "grpc://global.namespaceapis.com", true, "HTTPS"},
		{"WebSocket scheme", "ws://global.namespaceapis.com", true, "HTTPS"},
		{"File scheme", "file:///etc/passwd", true, "HTTPS"},
		{"SSH scheme", "ssh://global.namespaceapis.com", true, "HTTPS"},
	}

	for _, tc := range adversarialCases {
		t.Run("tfutil_"+tc.name, func(t *testing.T) {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{
				Path:        p,
				ConfigValue: types.StringValue(tc.endpoint),
			}, &resp)

			if tc.expectError {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected error for endpoint %q, but validation passed", tc.endpoint)
				}
				if tc.errContains != "" {
					found := false
					for _, d := range resp.Diagnostics.Errors() {
						if strings.Contains(strings.ToLower(d.Detail()), strings.ToLower(tc.errContains)) ||
							strings.Contains(strings.ToLower(d.Summary()), strings.ToLower(tc.errContains)) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected error containing %q, got: %v", tc.errContains, resp.Diagnostics)
					}
				}
			} else {
				if resp.Diagnostics.HasError() {
					t.Fatalf("expected valid endpoint %q to pass, got errors: %v", tc.endpoint, resp.Diagnostics)
				}
			}
		})

		t.Run("client_"+tc.name, func(t *testing.T) {
			err := client.ValidateEndpoint(tc.endpoint)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected ValidateEndpoint error for %q, got nil", tc.endpoint)
				}
				if tc.errContains != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errContains)) {
					t.Errorf("expected error containing %q, got: %v", tc.errContains, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected valid endpoint %q, got error: %v", tc.endpoint, err)
				}
			}
		})
	}
}

func makeEmpiricalProviderConfig(ctx context.Context, p provider.Provider, values map[string]interface{}) tfsdk.Config {
	schemaResp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, schemaResp)

	attrTypes := map[string]tftypes.Type{
		"token":            tftypes.String,
		"token_file":       tftypes.String,
		"global_endpoint":  tftypes.String,
		"iam_endpoint":     tftypes.String,
		"compute_endpoint": tftypes.String,
	}

	attrVals := make(map[string]tftypes.Value)
	for k, t := range attrTypes {
		v, exists := values[k]
		if !exists || v == nil {
			attrVals[k] = tftypes.NewValue(t, nil)
		} else if s, ok := v.(string); ok {
			attrVals[k] = tftypes.NewValue(t, s)
		}
	}

	val := tftypes.NewValue(tftypes.Object{AttributeTypes: attrTypes}, attrVals)
	return tfsdk.Config{
		Schema: schemaResp.Schema,
		Raw:    val,
	}
}
