package provider_test

import (
	"context"
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
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

// =============================================================================
// CHALLENGER STRESS TEST 1: CONCURRENCY CONFLICTS & DIAGNOSTIC ACCURACY
// =============================================================================

func TestEmpirical_ConcurrencyConflict_Variations(t *testing.T) {
	ctx := context.Background()

	t.Run("FailedPrecondition_RevisionMismatch", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var currentVer int64 = 1
		profileID := "prof-conf-var-1"
		tag := "conf-var-1"

		srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
			return &githubv1beta.CreateProfileResponse{
				Profile: &githubv1beta.RunnerProfileWithStatus{
					ProfileId: profileID,
					Version:   currentVer,
					CreatedAt: timestamppb.Now(),
					UpdatedAt: timestamppb.Now(),
					Spec:      req.GetSpec(),
				},
			}, nil
		}

		srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
			if req.UpdateVersion != currentVer {
				return nil, status.Errorf(codes.FailedPrecondition, "revision mismatch: requested version %d, server version is %d", req.UpdateVersion, currentVer)
			}
			currentVer++
			return &githubv1beta.UpdateProfileResponse{
				Profile: &githubv1beta.RunnerProfileWithStatus{
					ProfileId: profileID,
					Version:   currentVer,
					Spec:      req.GetSpec(),
				},
			}, nil
		}

		r, schemaResp := setupTestRunnerProfileResource(t, srv)

		// Create
		plan := makeRunnerProfilePlanModel(tag, "Initial", 512)
		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &plan)
		createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
		createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Create(ctx, createReq, createResp)
		if createResp.Diagnostics.HasError() {
			t.Fatalf("create failed: %v", createResp.Diagnostics)
		}

		// Out of band bump
		currentVer = 5

		// Update with stale version (1)
		updatePlan := makeRunnerProfilePlanModel(tag, "Updated", 1024)
		updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
		_ = updatePlanState.Set(ctx, &updatePlan)
		updateReq := resource.UpdateRequest{
			Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
			State: createResp.State,
		}
		updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Update(ctx, updateReq, updateResp)

		if !updateResp.Diagnostics.HasError() {
			t.Fatal("expected update error on FailedPrecondition, got none")
		}

		var summary, detail string
		for _, d := range updateResp.Diagnostics.Errors() {
			summary += d.Summary() + " "
			detail += d.Detail() + " "
		}

		if !strings.Contains(summary, "Concurrency Conflict") {
			t.Errorf("expected summary to contain 'Concurrency Conflict', got %q", summary)
		}
		if !strings.Contains(detail, "terraform refresh") {
			t.Errorf("expected detail to contain 'terraform refresh', got %q", detail)
		}
	})

	t.Run("HandleGRPCError_CodesAborted_And_FailedPrecondition", func(t *testing.T) {
		var diags diag.Diagnostics
		handled := client.HandleGRPCError(ctx, status.Error(codes.Aborted, "concurrent revision collision"), &diags, "update", "namespace_github_runner_profile")
		if handled {
			t.Error("expected HandleGRPCError to return false for error diags")
		}
		if !diags.HasError() {
			t.Fatal("expected diags to have error")
		}
		if !strings.Contains(diags.Errors()[0].Summary(), "Concurrency Conflict") {
			t.Errorf("expected Concurrency Conflict summary on Aborted, got: %s", diags.Errors()[0].Summary())
		}
	})
}

// =============================================================================
// CHALLENGER STRESS TEST 2: PAGINATION TRAVERSAL ACROSS 250 RECORDS & CYCLES
// =============================================================================

func TestEmpirical_PaginationTraversal_250Records_And_CycleProtection(t *testing.T) {
	ctx := context.Background()

	t.Run("250Records_5Pages_DeterministicSort", func(t *testing.T) {
		srv := testutil.NewMockServer()
		const totalRecords = 250
		const pageSize = 50
		repoName := "stress-paginated-repo"

		baseTime := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
		allImages := make([]*registryv1beta.Image, totalRecords)
		for i := 0; i < totalRecords; i++ {
			allImages[i] = &registryv1beta.Image{
				Repository: repoName,
				Digest:     fmt.Sprintf("sha256:%064x", i+1),
				CreatedAt:  timestamppb.New(baseTime.Add(time.Duration(totalRecords-i) * time.Minute)), // reverse creation order
				Sizes:      &registryv1beta.Image_Sizes{Total: int64(2048 + i)},
				Tags:       []string{fmt.Sprintf("tag-z-%03d", i), fmt.Sprintf("tag-a-%03d", i)},
			}
		}

		srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
			cursor := string(req.GetPaginationCursor())
			var startIdx int
			switch cursor {
			case "":
				startIdx = 0
			case "page_1":
				startIdx = 50
			case "page_2":
				startIdx = 100
			case "page_3":
				startIdx = 150
			case "page_4":
				startIdx = 200
			default:
				return nil, status.Errorf(codes.InvalidArgument, "unknown cursor %q", cursor)
			}

			endIdx := startIdx + pageSize
			if endIdx > totalRecords {
				endIdx = totalRecords
			}

			var nextCursor []byte
			if endIdx < totalRecords {
				nextCursor = []byte(fmt.Sprintf("page_%d", (endIdx / pageSize)))
			}

			return &registryv1beta.ListImagesResponse{
				Images:           allImages[startIdx:endIdx],
				PaginationCursor: nextCursor,
			}, nil
		}

		d, schemaResp := setupTestDataSourceRegistryImages(t, srv)

		cfg := registry.RegistryImagesDataSourceModel{
			Repository: types.StringValue(repoName),
		}
		configState := tfsdk.State{Schema: schemaResp.Schema}
		_ = configState.Set(ctx, &cfg)

		readReq := datasource.ReadRequest{
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
		}
		readResp := &datasource.ReadResponse{
			State: tfsdk.State{Schema: schemaResp.Schema},
		}

		start := time.Now()
		d.Read(ctx, readReq, readResp)
		elapsed := time.Since(start)

		if readResp.Diagnostics.HasError() {
			t.Fatalf("unexpected read error: %v", readResp.Diagnostics)
		}

		var state registry.RegistryImagesDataSourceModel
		_ = readResp.State.Get(ctx, &state)

		if len(state.Images) != totalRecords {
			t.Fatalf("expected %d total images, got %d", totalRecords, len(state.Images))
		}

		// Verify deterministic ascending sort by CreatedAt
		for i := 0; i < totalRecords-1; i++ {
			t1, err1 := time.Parse(time.RFC3339, state.Images[i].CreatedAt.ValueString())
			t2, err2 := time.Parse(time.RFC3339, state.Images[i+1].CreatedAt.ValueString())
			if err1 != nil || err2 != nil {
				t.Fatalf("failed parsing timestamps: %v, %v", err1, err2)
			}
			if t1.After(t2) {
				t.Fatalf("images not sorted in ascending order: index %d (%v) after index %d (%v)", i, t1, i+1, t2)
			}
		}

		// Verify tags are sorted alphabetically inside each entry
		for i, img := range state.Images {
			if len(img.Tags) == 2 {
				if img.Tags[0].ValueString() > img.Tags[1].ValueString() {
					t.Fatalf("tags not sorted alphabetically in image %d: %v", i, img.Tags)
				}
			}
		}

		if elapsed > 3*time.Second {
			t.Fatalf("pagination took too long: %v", elapsed)
		}
	})

	t.Run("CycleDetection_TerminatesWithError", func(t *testing.T) {
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			if pageToken == "" {
				return []string{"a"}, "tok_repeat", nil
			}
			if pageToken == "tok_repeat" {
				return []string{"b"}, "tok_repeat", nil // returns same token!
			}
			return nil, "", nil
		}

		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithTimeout(2*time.Second))
		if err == nil {
			t.Fatal("expected error on pagination cycle, got nil")
		}
		if !strings.Contains(err.Error(), "pagination cycle detected") {
			t.Fatalf("expected 'pagination cycle detected' error, got: %v", err)
		}
	})

	t.Run("MaxPagesExceeded_TerminatesWithError", func(t *testing.T) {
		var pageCounter int
		fetcher := func(ctx context.Context, pageToken string) ([]string, string, error) {
			pageCounter++
			return []string{"item"}, fmt.Sprintf("page_%d", pageCounter), nil
		}

		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithMaxPages(5), tfutil.WithTimeout(2*time.Second))
		if err == nil {
			t.Fatal("expected error on max pages exceeded, got nil")
		}
		if !strings.Contains(err.Error(), "maximum pagination pages exceeded") {
			t.Fatalf("expected 'maximum pagination pages exceeded' error, got: %v", err)
		}
	})
}

// =============================================================================
// CHALLENGER STRESS TEST 3: TOKEN EXPIRATION REJECTION & DIAGNOSTICS
// =============================================================================

func TestEmpirical_TokenExpirationRejection_MultiService(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var tokenExpired atomic.Bool

	srv.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		return &iamv1beta.IssueTenantTokenResponse{
			BearerToken: "nsct_ephemeral_token_xyz987",
		}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		if tokenExpired.Load() {
			return nil, status.Error(codes.Unauthenticated, "credential token expired; session invalid")
		}
		return &githubv1beta.ListProfilesResponse{}, nil
	}

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		if tokenExpired.Load() {
			return nil, status.Error(codes.Unauthenticated, "expired partner token")
		}
		return &iamv1beta.DescribeTenantGroupResponse{Name: req.Name}, nil
	}

	cl := testutil.NewTestClient(t, srv)
	tCli, err := cl.TokenClient(ctx)
	if err != nil {
		t.Fatalf("failed creating token client: %v", err)
	}

	// Issue token
	resp, err := tCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{DurationSecs: 1})
	if err != nil || resp.GetBearerToken() == "" {
		t.Fatalf("IssueTenantToken failed: %v", err)
	}

	pCli, err := cl.ProfileClient(ctx)
	if err != nil {
		t.Fatalf("failed creating profile client: %v", err)
	}
	tenantCli, err := cl.TenantClient(ctx)
	if err != nil {
		t.Fatalf("failed creating tenant client: %v", err)
	}

	// Step 1: Pre-expiry calls succeed
	if _, err := pCli.ListProfiles(ctx, &emptypb.Empty{}); err != nil {
		t.Fatalf("pre-expiry profile call failed: %v", err)
	}
	if _, err := tenantCli.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: "tg-1"}); err != nil {
		t.Fatalf("pre-expiry tenant call failed: %v", err)
	}

	// Step 2: Expire token
	tokenExpired.Store(true)

	// Step 3: Post-expiry calls actively fail with Unauthenticated
	_, err = pCli.ListProfiles(ctx, &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected post-expiry profile call to fail with Unauthenticated, got nil")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v", status.Code(err))
	}

	_, err = tenantCli.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: "tg-1"})
	if err == nil {
		t.Fatal("expected post-expiry tenant call to fail with Unauthenticated, got nil")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v", status.Code(err))
	}

	// Step 4: Verify HandleGRPCError maps Unauthenticated to Authorization Failure
	var diags diag.Diagnostics
	client.HandleGRPCError(ctx, err, &diags, "read", "namespace_tenant_group")
	if !diags.HasError() {
		t.Fatal("expected diagnostic error for Unauthenticated")
	}
	if !strings.Contains(diags.Errors()[0].Summary(), "Authorization Failure") {
		t.Errorf("expected 'Authorization Failure' in diagnostic summary, got: %s", diags.Errors()[0].Summary())
	}
}

// =============================================================================
// CHALLENGER STRESS TEST 4: CONCURRENT OPERATIONS THREAD SAFETY IN MOCK
// =============================================================================

func TestEmpirical_ConcurrentOperations_ThreadSafety(t *testing.T) {
	srv := testutil.NewMockServer()
	var idCounter int64
	var mu sync.Mutex
	profilesMap := make(map[string]*githubv1beta.RunnerProfileWithStatus)

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		atomic.AddInt64(&idCounter, 1)
		profID := fmt.Sprintf("prof_concurrent_%d", atomic.LoadInt64(&idCounter))
		now := timestamppb.Now()
		p := &githubv1beta.RunnerProfileWithStatus{
			ProfileId: profID,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec:      req.GetSpec(),
		}
		profilesMap[profID] = p
		return &githubv1beta.CreateProfileResponse{Profile: p}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var list []*githubv1beta.RunnerProfileWithStatus
		for _, p := range profilesMap {
			list = append(list, p)
		}
		return &githubv1beta.ListProfilesResponse{Profiles: list}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		p, exists := profilesMap[req.GetProfileId()]
		if !exists {
			return nil, status.Error(codes.NotFound, "profile not found")
		}
		if p.Version != req.GetUpdateVersion() {
			return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("version mismatch: expected %d, got %d", p.Version, req.GetUpdateVersion()))
		}
		p.Version++
		p.Spec = req.GetSpec()
		p.UpdatedAt = timestamppb.Now()
		return &githubv1beta.UpdateProfileResponse{Profile: p}, nil
	}

	srv.Profile.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(profilesMap, req.GetProfileId())
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestRunnerProfileResource(t, srv)
	ctx := context.Background()

	const workerCount = 30
	var wg sync.WaitGroup
	wg.Add(workerCount)

	for i := 0; i < workerCount; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			planModel := makeRunnerProfilePlanModel(fmt.Sprintf("tag-concur-%d", workerID), "Desc", 1024)
			planState := tfsdk.State{Schema: schemaResp.Schema}
			_ = planState.Set(ctx, &planModel)

			createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
			createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(ctx, createReq, createResp)
			if createResp.Diagnostics.HasError() {
				t.Errorf("worker %d create failed: %v", workerID, createResp.Diagnostics)
				return
			}

			var createdState github.RunnerProfileResourceModel
			_ = createResp.State.Get(ctx, &createdState)

			// Update
			updatePlan := createdState
			updatePlan.Description = types.StringValue(fmt.Sprintf("Worker %d updated", workerID))
			updatePlanState := tfsdk.State{Schema: schemaResp.Schema}
			_ = updatePlanState.Set(ctx, &updatePlan)

			updateReq := resource.UpdateRequest{
				State: createResp.State,
				Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: updatePlanState.Raw},
			}
			updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Update(ctx, updateReq, updateResp)
			if updateResp.Diagnostics.HasError() {
				t.Errorf("worker %d update failed: %v", workerID, updateResp.Diagnostics)
				return
			}

			// Delete
			delReq := resource.DeleteRequest{State: updateResp.State}
			delResp := &resource.DeleteResponse{}
			r.Delete(ctx, delReq, delResp)
			if delResp.Diagnostics.HasError() {
				t.Errorf("worker %d delete failed: %v", workerID, delResp.Diagnostics)
			}
		}()
	}

	wg.Wait()
}
