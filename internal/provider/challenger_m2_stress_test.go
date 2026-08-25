package provider_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"namespacelabs.dev/integrations/nsc/grpcapi"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

// =============================================================================
// CHALLENGER 2 STRESS HARNESS 1: CONCURRENCY CONFLICT & INTERCEPTOR SAFETY
// =============================================================================

func TestChallenger2_Stress_ConcurrencyConflict_MultipleUpdates(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	tag := "stress-conf-runner"
	profileID := "prof-stress-001"
	serverVersion := int64(1)

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   serverVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if req.UpdateVersion != serverVersion {
			return nil, status.Errorf(codes.FailedPrecondition, "revision conflict: requested update_version %d does not match current server version %d", req.UpdateVersion, serverVersion)
		}
		serverVersion++
		now := timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   serverVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	r, schemaResp := setupTestRunnerProfileResource(t, srv)

	// Step 1: Initial creation
	createPlan := makeRunnerProfilePlanModel(tag, "Version 1", 512)
	createPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = createPlanState.Set(ctx, &createPlan)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics)
	}

	// Step 2: Stress test concurrent conflicting updates
	const numGoroutines = 10
	var wg sync.WaitGroup
	var conflictErrors atomic.Int32
	var successCount atomic.Int32

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(iter int) {
			defer wg.Done()

			updatePlan := makeRunnerProfilePlanModel(tag, fmt.Sprintf("Concurrent Update %d", iter), 1024)
			updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
			_ = updatePlanState.Set(ctx, &updatePlan)

			updateReq := resource.UpdateRequest{
				Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
				State: createResp.State, // All send stale version 1
			}
			updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Update(ctx, updateReq, updateResp)

			if updateResp.Diagnostics.HasError() {
				conflictErrors.Add(1)
			} else {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	// Exactly 1 update might succeed (bumping version 1 -> 2), and the remaining 9 must fail with FailedPrecondition
	if successCount.Load() > 1 {
		t.Fatalf("expected at most 1 update to succeed on version 1, got %d", successCount.Load())
	}
	if conflictErrors.Load() < int32(numGoroutines-1) {
		t.Fatalf("expected at least %d conflict errors, got %d", numGoroutines-1, conflictErrors.Load())
	}
}

// TestChallenger2_Stress_ConcurrencyConflictInterceptor_EndToEnd tests the gRPC interceptor
// mutation mechanism used in acceptance tests with live in-memory server.
func TestChallenger2_Stress_ConcurrencyConflictInterceptor_EndToEnd(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	grpcapi.RequireTransportSecurity = false
	srv.Start()
	t.Cleanup(func() { srv.Stop() })

	var mu sync.Mutex
	targetTag := "interceptor-stress-tag"
	profileID := "prof-interceptor-001"
	serverVersion := int64(1)

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		now := timestamppb.Now()
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   serverVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if req.UpdateVersion != serverVersion {
			return nil, status.Errorf(codes.FailedPrecondition, "revision conflict: requested update_version %d does not match current server version %d", req.UpdateVersion, serverVersion)
		}
		serverVersion++
		now := timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: profileID,
				Version:   serverVersion,
				CreatedAt: now,
				UpdatedAt: now,
				Spec:      req.GetSpec(),
			},
		}, nil
	}

	// Build interceptor
	var triggerOnce sync.Once
	interceptor := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if strings.HasSuffix(method, "UpdateProfile") {
			if uReq, ok := req.(*githubv1beta.UpdateProfileRequest); ok {
				if uReq.GetSpec() != nil && strings.Contains(uReq.GetSpec().GetTag(), targetTag) {
					triggerOnce.Do(func() {
						// Intervening mutation bumps serverVersion 1 -> 2
						mutatedSpec := proto.Clone(uReq.GetSpec()).(*githubv1beta.RunnerProfileSpec)
						mutatedSpec.Description = "Intervening out-of-band writer collision"
						var directResp githubv1beta.UpdateProfileResponse
						_ = invoker(ctx, method, &githubv1beta.UpdateProfileRequest{
							ProfileId:     uReq.GetProfileId(),
							UpdateVersion: uReq.GetUpdateVersion(),
							Spec:          mutatedSpec,
						}, &directResp, cc, opts...)
					})
				}
			}
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}

	r := github.NewRunnerProfileResource()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	cfg := client.Config{
		TokenSource:     client.StaticTokenSource{Token: "test-token-nsct-12345"},
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Version:         "test",
		Creds:           insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(srv.Dialer()),
			grpc.WithUnaryInterceptor(interceptor),
		},
	}

	cli, err := client.New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	var confResp resource.ConfigureResponse
	if confR, ok := r.(resource.ResourceWithConfigure); ok {
		confR.Configure(ctx, resource.ConfigureRequest{ProviderData: cli}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure error: %v", confResp.Diagnostics)
		}
	}

	// Create step
	createPlan := makeRunnerProfilePlanModel(targetTag, "Initial description", 512)
	createPlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = createPlanState.Set(ctx, &createPlan)

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: createPlanState.Raw}}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, createReq, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics)
	}

	// Update step: interceptor will mutate server version during this call before provider update executes
	updatePlan := makeRunnerProfilePlanModel(targetTag, "Second version", 1024)
	updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
	_ = updatePlanState.Set(ctx, &updatePlan)

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
		State: createResp.State,
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(ctx, updateReq, updateResp)

	// Provider must receive FailedPrecondition and output a Concurrency Conflict diagnostic
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected update to fail with Concurrency Conflict error, but it succeeded")
	}

	var hasConcurrencyDiag bool
	for _, d := range updateResp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Concurrency Conflict") || strings.Contains(d.Summary(), "Failed Precondition") ||
			strings.Contains(d.Detail(), "terraform refresh") {
			hasConcurrencyDiag = true
			break
		}
	}
	if !hasConcurrencyDiag {
		t.Fatalf("expected concurrency conflict diagnostic advising refresh, got: %v", updateResp.Diagnostics)
	}
}

// =============================================================================
// CHALLENGER 2 STRESS HARNESS 2: PAGINATION TRAVERSAL BOUNDARIES
// =============================================================================

func TestChallenger2_Stress_PaginationBoundaries(t *testing.T) {
	ctx := context.Background()

	t.Run("ZeroRecords", func(t *testing.T) {
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			return nil, "", nil
		}
		items, err := tfutil.PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("expected nil error for empty pagination, got: %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("expected 0 items, got %d", len(items))
		}
	})

	t.Run("SingleRecordTerminal", func(t *testing.T) {
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			return []string{"item-0"}, "", nil
		}
		items, err := tfutil.PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if len(items) != 1 || items[0] != "item-0" {
			t.Fatalf("expected ['item-0'], got %v", items)
		}
	})

	t.Run("ExactPageBoundary_NextTokenEmpty", func(t *testing.T) {
		var page []string
		for i := 0; i < 50; i++ {
			page = append(page, fmt.Sprintf("img-%d", i))
		}
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			return page, "", nil
		}
		items, err := tfutil.PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if len(items) != 50 {
			t.Fatalf("expected 50 items, got %d", len(items))
		}
	})

	t.Run("ExactPageBoundary_NextTokenThenEmpty", func(t *testing.T) {
		var page1 []string
		for i := 0; i < 50; i++ {
			page1 = append(page1, fmt.Sprintf("img-%d", i))
		}
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			if pageToken == "" {
				return page1, "page_2", nil
			}
			if pageToken == "page_2" {
				return nil, "", nil
			}
			return nil, "", fmt.Errorf("unexpected token %q", pageToken)
		}
		items, err := tfutil.PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if len(items) != 50 {
			t.Fatalf("expected 50 items, got %d", len(items))
		}
	})

	t.Run("LargeDataset_250Records_5Pages", func(t *testing.T) {
		const total = 250
		const pageSize = 50
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			var offset int
			if pageToken != "" {
				_, _ = fmt.Sscanf(pageToken, "offset_%d", &offset)
			}
			end := offset + pageSize
			if end > total {
				end = total
			}
			var page []string
			for i := offset; i < end; i++ {
				page = append(page, fmt.Sprintf("item-%04d", i))
			}
			var nextTok string
			if end < total {
				nextTok = fmt.Sprintf("offset_%d", end)
			}
			return page, nextTok, nil
		}
		items, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithMaxPages(10))
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if len(items) != total {
			t.Fatalf("expected %d items, got %d", total, len(items))
		}
	})

	t.Run("CycleDetectionInfiniteLoop", func(t *testing.T) {
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			if pageToken == "" {
				return []string{"item-1"}, "tok_B", nil
			}
			if pageToken == "tok_B" {
				return []string{"item-2"}, "tok_A", nil
			}
			if pageToken == "tok_A" {
				return []string{"item-3"}, "tok_B", nil // Cycle
			}
			return nil, "", nil
		}
		_, err := tfutil.PaginateAll(ctx, fetcher)
		if err == nil {
			t.Fatal("expected cycle detection error, got nil")
		}
		if !errors.Is(err, tfutil.ErrPaginationCycleDetected) {
			t.Fatalf("expected ErrPaginationCycleDetected, got: %v", err)
		}
	})

	t.Run("MaxPagesExceeded", func(t *testing.T) {
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			var pageNum int
			if pageToken != "" {
				_, _ = fmt.Sscanf(pageToken, "page_%d", &pageNum)
			}
			return []string{fmt.Sprintf("item-page-%d", pageNum)}, fmt.Sprintf("page_%d", pageNum+1), nil
		}
		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithMaxPages(5))
		if err == nil {
			t.Fatal("expected max pages exceeded error, got nil")
		}
		if !errors.Is(err, tfutil.ErrMaxPagesExceeded) {
			t.Fatalf("expected ErrMaxPagesExceeded, got: %v", err)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		fetcher := func(fCtx context.Context, pageToken string) ([]string, string, error) {
			cancel() // Cancel on first page
			return []string{"item-1"}, "next_tok", nil
		}
		_, err := tfutil.PaginateAll(cancelCtx, fetcher)
		if err == nil {
			t.Fatal("expected context cancelled error, got nil")
		}
	})
}

func TestChallenger2_Stress_RegistryImagesDataSource_DeterminismAndBoundaries(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Test case 1: Empty repository (0 images)
	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images:           nil,
			PaginationCursor: nil,
		}, nil
	}

	d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

	cfgEmpty := registry.RegistryImagesDataSourceModel{
		ID:         types.StringNull(),
		Repository: types.StringValue("empty-repo"),
		Images:     nil,
	}
	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &cfgEmpty)

	readReq := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
	readResp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, readReq, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected error reading empty repo: %v", readResp.Diagnostics)
	}

	var stateEmpty registry.RegistryImagesDataSourceModel
	_ = readResp.State.Get(ctx, &stateEmpty)
	if len(stateEmpty.Images) != 0 {
		t.Fatalf("expected 0 images for empty repo, got %d", len(stateEmpty.Images))
	}

	// Test case 2: Deterministic sorting with equal timestamps and differing digests
	baseTime := timestamppb.New(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	unorderedImages := []*registryv1beta.Image{
		{Repository: "sort-repo", Digest: "sha256:cccc", CreatedAt: baseTime, Tags: []string{"z-tag", "a-tag"}},
		{Repository: "sort-repo", Digest: "sha256:aaaa", CreatedAt: baseTime, Tags: []string{"beta"}},
		{Repository: "sort-repo", Digest: "sha256:bbbb", CreatedAt: baseTime, Tags: []string{"alpha"}},
	}

	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images:           unorderedImages,
			PaginationCursor: nil,
		}, nil
	}

	cfgSort := registry.RegistryImagesDataSourceModel{
		ID:         types.StringNull(),
		Repository: types.StringValue("sort-repo"),
		Images:     nil,
	}
	configState2 := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState2.Set(ctx, &cfgSort)

	readReq2 := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState2.Raw}}
	readResp2 := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, readReq2, readResp2)

	if readResp2.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", readResp2.Diagnostics)
	}

	var stateSort registry.RegistryImagesDataSourceModel
	_ = readResp2.State.Get(ctx, &stateSort)
	if len(stateSort.Images) != 3 {
		t.Fatalf("expected 3 images, got %d", len(stateSort.Images))
	}

	// Digests must be sorted: sha256:aaaa, sha256:bbbb, sha256:cccc
	if stateSort.Images[0].Digest.ValueString() != "sha256:aaaa" ||
		stateSort.Images[1].Digest.ValueString() != "sha256:bbbb" ||
		stateSort.Images[2].Digest.ValueString() != "sha256:cccc" {
		t.Fatalf("images not sorted deterministically by digest: %v, %v, %v",
			stateSort.Images[0].Digest.ValueString(),
			stateSort.Images[1].Digest.ValueString(),
			stateSort.Images[2].Digest.ValueString())
	}

	// Tags on image 0 (cccc originally, now index 2) must be sorted: a-tag, z-tag
	if len(stateSort.Images[2].Tags) != 2 ||
		stateSort.Images[2].Tags[0].ValueString() != "a-tag" ||
		stateSort.Images[2].Tags[1].ValueString() != "z-tag" {
		t.Fatalf("tags not sorted alphabetically: %v", stateSort.Images[2].Tags)
	}
}

// =============================================================================
// CHALLENGER 2 STRESS HARNESS 3: TOKEN EXPIRATION TIMING & ACTIVE REJECTION
// =============================================================================

func TestChallenger2_Stress_TokenExpirationTiming(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	tokenExpiryMap := make(map[string]time.Time)

	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		tok := fmt.Sprintf("nsct_exp_test_%d", time.Now().UnixNano())
		dur := req.GetDurationSecs()
		if dur <= 0 {
			dur = 1
		}
		tokenExpiryMap[tok] = time.Now().Add(time.Duration(dur) * time.Second)
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: tok,
		}, nil
	}

	cl := testutil.NewTestClient(t, srv)
	tCli, err := cl.TokenClient(ctx)
	if err != nil {
		t.Fatalf("failed to create token client: %v", err)
	}

	// 1-second token
	resp, err := tCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{DurationSecs: 1})
	if err != nil {
		t.Fatalf("failed to issue token: %v", err)
	}
	tok := resp.GetBearerToken()

	mu.Lock()
	expiry := tokenExpiryMap[tok]
	mu.Unlock()

	if time.Now().After(expiry) {
		t.Fatal("token should not be expired immediately upon creation")
	}

	// Sleep past expiry
	time.Sleep(1100 * time.Millisecond)
	if !time.Now().After(expiry) {
		t.Fatal("token should be expired after sleep")
	}
}
