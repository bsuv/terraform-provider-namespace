package github_test

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	imagesv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/images"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// ============================================================================
// 1. ADVERSARIAL TESTS: namespace_github_runner_profile
// ============================================================================

// TestRunnerProfile_Adversarial_FilterValidation tests all combinations of filter inputs:
// - neither profile_id nor tag
// - empty strings
// - both profile_id and tag
func TestRunnerProfile_Adversarial_FilterValidation(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	profile := sampleFullProfile("prof_adv_1", 1)
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{profile},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	tests := []struct {
		name        string
		profileID   types.String
		tag         types.String
		expectError bool
		errSummary  string
	}{
		{
			name:        "both null",
			profileID:   types.StringNull(),
			tag:         types.StringNull(),
			expectError: true,
			errSummary:  "Missing Filter Parameter",
		},
		{
			name:        "both empty string",
			profileID:   types.StringValue(""),
			tag:         types.StringValue(""),
			expectError: true,
			errSummary:  "Missing Filter Parameter",
		},
		{
			name:        "profile_id empty, tag null",
			profileID:   types.StringValue(""),
			tag:         types.StringNull(),
			expectError: true,
			errSummary:  "Missing Filter Parameter",
		},
		{
			name:        "tag empty, profile_id null",
			profileID:   types.StringNull(),
			tag:         types.StringValue(""),
			expectError: true,
			errSummary:  "Missing Filter Parameter",
		},
		{
			name:        "valid profile_id alone",
			profileID:   types.StringValue("prof_adv_1"),
			tag:         types.StringNull(),
			expectError: false,
		},
		{
			name:        "valid tag alone",
			profileID:   types.StringNull(),
			tag:         types.StringValue("ubuntu-24-04-large"),
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configModel := github.RunnerProfileDataSourceModel{
				ID:                    types.StringNull(),
				ProfileID:             tc.profileID,
				Tag:                   tc.tag,
				Version:               types.Int64Null(),
				CreatedAt:             types.StringNull(),
				UpdatedAt:             types.StringNull(),
				OS:                    types.StringNull(),
				Description:           types.StringNull(),
				Emoji:                 types.StringNull(),
				SwapSizeMB:            types.Int64Null(),
				BuilderMode:           types.StringNull(),
				AccessLevel:           types.StringNull(),
				ExperimentalFeatures:  types.ListNull(types.StringType),
				InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
				CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
				CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
				NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
				CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
			}

			configState := tfsdk.State{Schema: schemaResp.Schema}
			_ = configState.Set(ctx, &configModel)

			req := datasource.ReadRequest{
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
			}
			resp := &datasource.ReadResponse{
				State: tfsdk.State{Schema: schemaResp.Schema},
			}

			d.Read(ctx, req, resp)

			if tc.expectError {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected error for case %q, got none", tc.name)
				}
				if tc.errSummary != "" {
					found := false
					for _, diag := range resp.Diagnostics.Errors() {
						if strings.Contains(diag.Summary(), tc.errSummary) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected error containing %q, got %v", tc.errSummary, resp.Diagnostics)
					}
				}
			} else {
				if resp.Diagnostics.HasError() {
					t.Fatalf("unexpected error for case %q: %v", tc.name, resp.Diagnostics)
				}
			}
		})
	}
}

// TestRunnerProfile_Adversarial_ZeroMatches tests 0 matches against various backend responses
func TestRunnerProfile_Adversarial_ZeroMatches(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// 5 profiles exist in backend, but none match our search query
	var existingProfiles []*githubv1beta.RunnerProfileWithStatus
	for i := 1; i <= 5; i++ {
		p := sampleFullProfile(fmt.Sprintf("prof_existing_%d", i), 1)
		p.Spec.Tag = fmt.Sprintf("tag-existing-%d", i)
		existingProfiles = append(existingProfiles, p)
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: existingProfiles,
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	testCases := []struct {
		name      string
		profileID string
		tag       string
	}{
		{name: "nonexistent profile_id", profileID: "prof_nonexistent_xyz_999"},
		{name: "nonexistent tag", tag: "tag-nonexistent-xyz"},
		{name: "invalid id format", profileID: "invalid/id!@#$%^&*()"},
		{name: "case sensitive mismatch", tag: "TAG-EXISTING-1"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configModel := github.RunnerProfileDataSourceModel{
				ID:                    types.StringNull(),
				ProfileID:             types.StringValue(tc.profileID),
				Tag:                   types.StringValue(tc.tag),
				Version:               types.Int64Null(),
				CreatedAt:             types.StringNull(),
				UpdatedAt:             types.StringNull(),
				OS:                    types.StringNull(),
				Description:           types.StringNull(),
				Emoji:                 types.StringNull(),
				SwapSizeMB:            types.Int64Null(),
				BuilderMode:           types.StringNull(),
				AccessLevel:           types.StringNull(),
				ExperimentalFeatures:  types.ListNull(types.StringType),
				InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
				CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
				CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
				NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
				CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
			}
			if tc.profileID == "" {
				configModel.ProfileID = types.StringNull()
			}
			if tc.tag == "" {
				configModel.Tag = types.StringNull()
			}

			configState := tfsdk.State{Schema: schemaResp.Schema}
			_ = configState.Set(ctx, &configModel)

			req := datasource.ReadRequest{
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
			}
			resp := &datasource.ReadResponse{
				State: tfsdk.State{Schema: schemaResp.Schema},
			}

			d.Read(ctx, req, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected error for zero matches in %q, got none", tc.name)
			}

			hasNotFoundSummary := false
			for _, diag := range resp.Diagnostics.Errors() {
				if strings.Contains(diag.Summary(), "Runner Profile Not Found") {
					hasNotFoundSummary = true
					break
				}
			}
			if !hasNotFoundSummary {
				t.Errorf("expected 'Runner Profile Not Found' summary, got %v", resp.Diagnostics)
			}
		})
	}
}

// TestRunnerProfile_Adversarial_TagCollisionsStress tests multiple tag collisions across large scale
func TestRunnerProfile_Adversarial_TagCollisionsStress(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// 20 profiles have tag "colliding-tag", 30 profiles have unique tags
	var profiles []*githubv1beta.RunnerProfileWithStatus
	for i := 1; i <= 20; i++ {
		p := sampleFullProfile(fmt.Sprintf("prof_collide_%03d", i), 1)
		p.Spec.Tag = "colliding-tag"
		profiles = append(profiles, p)
	}
	for i := 1; i <= 30; i++ {
		p := sampleFullProfile(fmt.Sprintf("prof_unique_%03d", i), 1)
		p.Spec.Tag = fmt.Sprintf("unique-tag-%03d", i)
		profiles = append(profiles, p)
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: profiles,
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	// 1. Query colliding tag -> MUST error with "Multiple Runner Profiles Found"
	t.Run("query colliding tag", func(t *testing.T) {
		configModel := github.RunnerProfileDataSourceModel{
			ID:                    types.StringNull(),
			ProfileID:             types.StringNull(),
			Tag:                   types.StringValue("colliding-tag"),
			Version:               types.Int64Null(),
			CreatedAt:             types.StringNull(),
			UpdatedAt:             types.StringNull(),
			OS:                    types.StringNull(),
			Description:           types.StringNull(),
			Emoji:                 types.StringNull(),
			SwapSizeMB:            types.Int64Null(),
			BuilderMode:           types.StringNull(),
			AccessLevel:           types.StringNull(),
			ExperimentalFeatures:  types.ListNull(types.StringType),
			InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
			CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
			CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
			NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
			CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
		}

		configState := tfsdk.State{Schema: schemaResp.Schema}
		_ = configState.Set(ctx, &configModel)

		req := datasource.ReadRequest{
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
		}
		resp := &datasource.ReadResponse{
			State: tfsdk.State{Schema: schemaResp.Schema},
		}

		d.Read(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error on multiple tag collision, got none")
		}

		hasMultipleMatches := false
		for _, diag := range resp.Diagnostics.Errors() {
			if strings.Contains(diag.Summary(), "Multiple Runner Profiles Found") {
				hasMultipleMatches = true
				break
			}
		}
		if !hasMultipleMatches {
			t.Errorf("expected 'Multiple Runner Profiles Found' summary, got %v", resp.Diagnostics)
		}
	})

	// 2. Query individual profile_id of a colliding profile -> MUST SUCCEED (unambiguous by ID)
	t.Run("query by profile_id for colliding tag member", func(t *testing.T) {
		configModel := github.RunnerProfileDataSourceModel{
			ID:                    types.StringNull(),
			ProfileID:             types.StringValue("prof_collide_007"),
			Tag:                   types.StringNull(),
			Version:               types.Int64Null(),
			CreatedAt:             types.StringNull(),
			UpdatedAt:             types.StringNull(),
			OS:                    types.StringNull(),
			Description:           types.StringNull(),
			Emoji:                 types.StringNull(),
			SwapSizeMB:            types.Int64Null(),
			BuilderMode:           types.StringNull(),
			AccessLevel:           types.StringNull(),
			ExperimentalFeatures:  types.ListNull(types.StringType),
			InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
			CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
			CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
			NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
			CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
		}

		configState := tfsdk.State{Schema: schemaResp.Schema}
		_ = configState.Set(ctx, &configModel)

		req := datasource.ReadRequest{
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
		}
		resp := &datasource.ReadResponse{
			State: tfsdk.State{Schema: schemaResp.Schema},
		}

		d.Read(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error reading by profile_id: %v", resp.Diagnostics)
		}

		var result github.RunnerProfileDataSourceModel
		resp.State.Get(ctx, &result)
		if result.ProfileID.ValueString() != "prof_collide_007" {
			t.Errorf("expected profile_id 'prof_collide_007', got %q", result.ProfileID.ValueString())
		}
		if result.Tag.ValueString() != "colliding-tag" {
			t.Errorf("expected tag 'colliding-tag', got %q", result.Tag.ValueString())
		}
	})
}

// TestRunnerProfile_Adversarial_NilSpecAndFields verifies no nil-pointer panics when backend returns sparse objects
func TestRunnerProfile_Adversarial_NilSpecAndFields(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Return sparse profiles (some with nil spec, some with empty spec, nil nested fields)
	p1 := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_nil_spec",
		Version:   1,
		Spec:      nil,
	}
	p2 := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_empty_spec",
		Version:   2,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag: "sparse-tag",
		},
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{p1, p2},
		}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

	t.Run("read sparse profile with empty spec", func(t *testing.T) {
		configModel := github.RunnerProfileDataSourceModel{
			ID:                    types.StringNull(),
			ProfileID:             types.StringValue("prof_empty_spec"),
			Tag:                   types.StringNull(),
			Version:               types.Int64Null(),
			CreatedAt:             types.StringNull(),
			UpdatedAt:             types.StringNull(),
			OS:                    types.StringNull(),
			Description:           types.StringNull(),
			Emoji:                 types.StringNull(),
			SwapSizeMB:            types.Int64Null(),
			BuilderMode:           types.StringNull(),
			AccessLevel:           types.StringNull(),
			ExperimentalFeatures:  types.ListNull(types.StringType),
			InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
			CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
			CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
			NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
			CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
		}

		configState := tfsdk.State{Schema: schemaResp.Schema}
		_ = configState.Set(ctx, &configModel)

		req := datasource.ReadRequest{
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
		}
		resp := &datasource.ReadResponse{
			State: tfsdk.State{Schema: schemaResp.Schema},
		}

		d.Read(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error reading sparse profile: %v", resp.Diagnostics)
		}

		var res github.RunnerProfileDataSourceModel
		resp.State.Get(ctx, &res)
		if res.Tag.ValueString() != "sparse-tag" {
			t.Errorf("expected tag 'sparse-tag', got %q", res.Tag.ValueString())
		}
	})
}

// TestRunnerProfile_Adversarial_GRPCErrors tests handling of various gRPC status codes
func TestRunnerProfile_Adversarial_GRPCErrors(t *testing.T) {
	ctx := context.Background()

	errorCodes := []codes.Code{
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.DeadlineExceeded,
		codes.ResourceExhausted,
		codes.Internal,
		codes.Unavailable,
	}

	for _, c := range errorCodes {
		t.Run(fmt.Sprintf("grpc code %s", c), func(t *testing.T) {
			srv := testutil.NewMockServer()
			srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
				return nil, status.Errorf(c, "simulated gRPC error for %s", c)
			}

			d, schemaResp := setupTestDataSourceRunnerProfile(t, srv)

			configModel := github.RunnerProfileDataSourceModel{
				ID:                    types.StringNull(),
				ProfileID:             types.StringValue("prof_test"),
				Tag:                   types.StringNull(),
				Version:               types.Int64Null(),
				CreatedAt:             types.StringNull(),
				UpdatedAt:             types.StringNull(),
				OS:                    types.StringNull(),
				Description:           types.StringNull(),
				Emoji:                 types.StringNull(),
				SwapSizeMB:            types.Int64Null(),
				BuilderMode:           types.StringNull(),
				AccessLevel:           types.StringNull(),
				ExperimentalFeatures:  types.ListNull(types.StringType),
				InstanceShape:         types.ObjectNull(github.InstanceShapeAttrTypes),
				CacheVolumeSettings:   types.ListNull(github.CacheVolumeSettingObjectType),
				CustomRunnerImageSpec: types.ObjectNull(github.CustomRunnerImageSpecAttrTypes),
				NetworkPolicy:         types.ObjectNull(github.NetworkPolicyAttrTypes),
				CustomRunnerImages:    types.ListNull(github.CustomRunnerImageStatusObjectType),
			}

			configState := tfsdk.State{Schema: schemaResp.Schema}
			_ = configState.Set(ctx, &configModel)

			req := datasource.ReadRequest{
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
			}
			resp := &datasource.ReadResponse{
				State: tfsdk.State{Schema: schemaResp.Schema},
			}

			d.Read(ctx, req, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected error for gRPC code %s, got none", c)
			}
		})
	}
}

// ============================================================================
// 2. ADVERSARIAL TESTS: namespace_github_runner_profiles (Sorting & Stability)
// ============================================================================

// TestRunnerProfiles_Adversarial_FuzzedOrderSort verifies deterministic ascending sort order under arbitrary permutations
func TestRunnerProfiles_Adversarial_FuzzedOrderSort(t *testing.T) {
	ctx := context.Background()

	// Generate 100 profiles with deterministic expected sorted list
	profileCount := 100
	canonicalIDs := make([]string, profileCount)
	for i := 0; i < profileCount; i++ {
		// Use varied ID patterns: prof_001, prof_099, prof_uuid_..., prof_z, etc.
		canonicalIDs[i] = fmt.Sprintf("prof_perm_%04d", i)
	}

	// Make sure canonicalIDs is sorted
	sort.Strings(canonicalIDs)

	// Run 25 iterations with different random seeds and shuffle orders
	for iter := 0; iter < 25; iter++ {
		rng := rand.New(rand.NewSource(int64(iter*1000 + 42)))

		// Create shuffled profiles
		shuffledIDs := make([]string, len(canonicalIDs))
		copy(shuffledIDs, canonicalIDs)
		rng.Shuffle(len(shuffledIDs), func(i, j int) {
			shuffledIDs[i], shuffledIDs[j] = shuffledIDs[j], shuffledIDs[i]
		})

		var backendProfiles []*githubv1beta.RunnerProfileWithStatus
		for _, id := range shuffledIDs {
			backendProfiles = append(backendProfiles, sampleFullProfile(id, 1))
		}

		srv := testutil.NewMockServer()
		srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
			return &githubv1beta.ListProfilesResponse{
				Profiles: backendProfiles,
			}, nil
		}

		d, schemaResp := setupTestDataSourceRunnerProfiles(t, srv)

		req := datasource.ReadRequest{
			Config: tfsdk.Config{Schema: schemaResp.Schema},
		}
		resp := &datasource.ReadResponse{
			State: tfsdk.State{Schema: schemaResp.Schema},
		}

		d.Read(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("iteration %d: unexpected read errors: %v", iter, resp.Diagnostics)
		}

		var stateResult github.RunnerProfilesDataSourceModel
		resp.State.Get(ctx, &stateResult)

		if len(stateResult.Profiles) != profileCount {
			t.Fatalf("iteration %d: expected %d profiles, got %d", iter, profileCount, len(stateResult.Profiles))
		}

		// Verify strict ascending sort matches canonicalIDs
		for idx, expectedID := range canonicalIDs {
			actualID := stateResult.Profiles[idx].ProfileID.ValueString()
			if actualID != expectedID {
				t.Fatalf("iteration %d at index %d: expected profile_id %q, got %q", iter, idx, expectedID, actualID)
			}
		}
	}
}

// TestRunnerProfiles_Adversarial_ConcurrentReads ensures no race conditions during concurrent sorting and reading
func TestRunnerProfiles_Adversarial_ConcurrentReads(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var profiles []*githubv1beta.RunnerProfileWithStatus
	for i := 50; i >= 1; i-- {
		profiles = append(profiles, sampleFullProfile(fmt.Sprintf("prof_conc_%02d", i), 1))
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		// Return fresh slice copy to avoid race on slice header in mock
		cp := make([]*githubv1beta.RunnerProfileWithStatus, len(profiles))
		copy(cp, profiles)
		return &githubv1beta.ListProfilesResponse{Profiles: cp}, nil
	}

	d, schemaResp := setupTestDataSourceRunnerProfiles(t, srv)

	var wg sync.WaitGroup
	concurrency := 20
	errChan := make(chan error, concurrency)

	for c := 0; c < concurrency; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := datasource.ReadRequest{
				Config: tfsdk.Config{Schema: schemaResp.Schema},
			}
			resp := &datasource.ReadResponse{
				State: tfsdk.State{Schema: schemaResp.Schema},
			}

			d.Read(ctx, req, resp)
			if resp.Diagnostics.HasError() {
				errChan <- fmt.Errorf("concurrent read error: %v", resp.Diagnostics)
				return
			}

			var stateResult github.RunnerProfilesDataSourceModel
			resp.State.Get(ctx, &stateResult)
			if len(stateResult.Profiles) != 50 {
				errChan <- fmt.Errorf("expected 50 profiles, got %d", len(stateResult.Profiles))
				return
			}
			if stateResult.Profiles[0].ProfileID.ValueString() != "prof_conc_01" {
				errChan <- fmt.Errorf("expected first profile 'prof_conc_01', got %q", stateResult.Profiles[0].ProfileID.ValueString())
				return
			}
			if stateResult.Profiles[49].ProfileID.ValueString() != "prof_conc_50" {
				errChan <- fmt.Errorf("expected last profile 'prof_conc_50', got %q", stateResult.Profiles[49].ProfileID.ValueString())
				return
			}
		}()
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Fatal(err)
	}
}

// ============================================================================
// 3. ADVERSARIAL TESTS: namespace_github_base_images (Filters, Stages & Sorting)
// ============================================================================

// TestBaseImages_Adversarial_ReleaseStageMatrix tests all combinations of release stages:
// - PRODUCTION alone
// - CANARY alone
// - STAGE_UNKNOWN alone
// - Multiple valid stages
// - Invalid / unknown stages
// - Empty / null list
func TestBaseImages_Adversarial_ReleaseStageMatrix(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		stagesInput    []string
		expectError    bool
		expectedStages []imagesv1beta.ReleaseStage
	}{
		{
			name:           "PRODUCTION only",
			stagesInput:    []string{"PRODUCTION"},
			expectError:    false,
			expectedStages: []imagesv1beta.ReleaseStage{imagesv1beta.ReleaseStage_PRODUCTION},
		},
		{
			name:           "CANARY only",
			stagesInput:    []string{"CANARY"},
			expectError:    false,
			expectedStages: []imagesv1beta.ReleaseStage{imagesv1beta.ReleaseStage_CANARY},
		},
		{
			name:           "STAGE_UNKNOWN only",
			stagesInput:    []string{"STAGE_UNKNOWN"},
			expectError:    false,
			expectedStages: []imagesv1beta.ReleaseStage{imagesv1beta.ReleaseStage_STAGE_UNKNOWN},
		},
		{
			name:           "multiple valid stages [PRODUCTION, CANARY]",
			stagesInput:    []string{"PRODUCTION", "CANARY"},
			expectError:    false,
			expectedStages: []imagesv1beta.ReleaseStage{imagesv1beta.ReleaseStage_PRODUCTION, imagesv1beta.ReleaseStage_CANARY},
		},
		{
			name:           "all 3 valid stages [STAGE_UNKNOWN, PRODUCTION, CANARY]",
			stagesInput:    []string{"STAGE_UNKNOWN", "PRODUCTION", "CANARY"},
			expectError:    false,
			expectedStages: []imagesv1beta.ReleaseStage{imagesv1beta.ReleaseStage_STAGE_UNKNOWN, imagesv1beta.ReleaseStage_PRODUCTION, imagesv1beta.ReleaseStage_CANARY},
		},
		{
			name:        "invalid stage: lowercase 'production'",
			stagesInput: []string{"production"},
			expectError: true,
		},
		{
			name:        "invalid stage: 'BETA'",
			stagesInput: []string{"BETA"},
			expectError: true,
		},
		{
			name:        "invalid stage: 'ALPHA'",
			stagesInput: []string{"ALPHA"},
			expectError: true,
		},
		{
			name:        "invalid stage: empty string",
			stagesInput: []string{""},
			expectError: true,
		},
		{
			name:        "mixed valid and invalid [PRODUCTION, INVALID]",
			stagesInput: []string{"PRODUCTION", "INVALID"},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewMockServer()
			var receivedStages []imagesv1beta.ReleaseStage
			srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
				receivedStages = req.ReleaseStages
				return &githubv1beta.ListBaseImagesResponse{
					Images: sampleBaseImages(),
				}, nil
			}

			d, schemaResp := setupTestDataSourceBaseImages(t, srv)

			var stageVals []attr.Value
			for _, s := range tc.stagesInput {
				stageVals = append(stageVals, types.StringValue(s))
			}
			stagesList, _ := types.ListValue(types.StringType, stageVals)

			configModel := github.BaseImagesDataSourceModel{
				ID:            types.StringNull(),
				Labels:        types.ListNull(types.StringType),
				ReleaseStages: stagesList,
				Images:        nil,
			}

			configState := tfsdk.State{Schema: schemaResp.Schema}
			_ = configState.Set(ctx, &configModel)

			req := datasource.ReadRequest{
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
			}
			resp := &datasource.ReadResponse{
				State: tfsdk.State{Schema: schemaResp.Schema},
			}

			d.Read(ctx, req, resp)

			if tc.expectError {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected error for stages %v, got none", tc.stagesInput)
				}
				hasStageError := false
				for _, diag := range resp.Diagnostics.Errors() {
					if strings.Contains(diag.Summary(), "Invalid Release Stage") {
						hasStageError = true
						break
					}
				}
				if !hasStageError {
					t.Errorf("expected 'Invalid Release Stage' error, got %v", resp.Diagnostics)
				}
			} else {
				if resp.Diagnostics.HasError() {
					t.Fatalf("unexpected error for valid stages %v: %v", tc.stagesInput, resp.Diagnostics)
				}
				if len(receivedStages) != len(tc.expectedStages) {
					t.Fatalf("expected %d stages sent to gRPC, got %d", len(tc.expectedStages), len(receivedStages))
				}
				for idx, exp := range tc.expectedStages {
					if receivedStages[idx] != exp {
						t.Errorf("at index %d: expected stage %v, got %v", idx, exp, receivedStages[idx])
					}
				}
			}
		})
	}
}

// TestBaseImages_Adversarial_MultiplePrefixMatchers tests filtering by multiple label matchers
func TestBaseImages_Adversarial_MultiplePrefixMatchers(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var receivedMatcher *stdlib.StringMatcher
	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		receivedMatcher = req.Labels
		return &githubv1beta.ListBaseImagesResponse{
			Images: sampleBaseImages(),
		}, nil
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	inputLabels := []string{"ubuntu-24.04", "ubuntu-22.04", "macos-15", "macos-14-arm64", "selfhosted-*"}
	var labelVals []attr.Value
	for _, l := range inputLabels {
		labelVals = append(labelVals, types.StringValue(l))
	}
	labelsList, _ := types.ListValue(types.StringType, labelVals)

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        labelsList,
		ReleaseStages: types.ListNull(types.StringType),
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	if receivedMatcher == nil {
		t.Fatal("expected non-nil Labels StringMatcher in gRPC request")
	}
	if receivedMatcher.Op != stdlib.StringMatcher_IS_ANY_OF {
		t.Errorf("expected Op IS_ANY_OF, got %v", receivedMatcher.Op)
	}
	if len(receivedMatcher.Values) != len(inputLabels) {
		t.Fatalf("expected %d matcher values, got %d", len(inputLabels), len(receivedMatcher.Values))
	}
	for i, exp := range inputLabels {
		if receivedMatcher.Values[i] != exp {
			t.Errorf("matcher value [%d]: expected %q, got %q", i, exp, receivedMatcher.Values[i])
		}
	}
}

// TestBaseImages_Adversarial_FuzzedOrderSortStability tests deterministic ascending sort stability by label
func TestBaseImages_Adversarial_FuzzedOrderSortStability(t *testing.T) {
	ctx := context.Background()

	rawLabels := []string{
		"ubuntu-24.04", "ubuntu-22.04", "ubuntu-20.04",
		"macos-15", "macos-14", "macos-13",
		"windows-2022", "windows-2019",
		"archlinux-latest", "debian-12", "fedora-40",
	}

	sortedLabels := make([]string, len(rawLabels))
	copy(sortedLabels, rawLabels)
	sort.Strings(sortedLabels)

	for iter := 0; iter < 20; iter++ {
		rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(iter)))

		shuffled := make([]string, len(rawLabels))
		copy(shuffled, rawLabels)
		rng.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})

		var backendImages []*githubv1beta.RunnerBaseImage
		for _, lbl := range shuffled {
			backendImages = append(backendImages, &githubv1beta.RunnerBaseImage{
				Label:    lbl,
				ImageRef: fmt.Sprintf("nscr.io/official/%s:latest", lbl),
				Title:    fmt.Sprintf("Title for %s", lbl),
				Spec: &imagesv1beta.BaseImage{
					Id:           fmt.Sprintf("img_%s", lbl),
					Os:           "linux",
					ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION,
				},
			})
		}

		srv := testutil.NewMockServer()
		srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
			return &githubv1beta.ListBaseImagesResponse{
				Images: backendImages,
			}, nil
		}

		d, schemaResp := setupTestDataSourceBaseImages(t, srv)

		configModel := github.BaseImagesDataSourceModel{
			ID:            types.StringNull(),
			Labels:        types.ListNull(types.StringType),
			ReleaseStages: types.ListNull(types.StringType),
			Images:        nil,
		}

		configState := tfsdk.State{Schema: schemaResp.Schema}
		_ = configState.Set(ctx, &configModel)

		req := datasource.ReadRequest{
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
		}
		resp := &datasource.ReadResponse{
			State: tfsdk.State{Schema: schemaResp.Schema},
		}

		d.Read(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("iteration %d: unexpected read error: %v", iter, resp.Diagnostics)
		}

		var stateResult github.BaseImagesDataSourceModel
		resp.State.Get(ctx, &stateResult)

		if len(stateResult.Images) != len(sortedLabels) {
			t.Fatalf("iteration %d: expected %d images, got %d", iter, len(sortedLabels), len(stateResult.Images))
		}

		for idx, expLabel := range sortedLabels {
			actLabel := stateResult.Images[idx].Label.ValueString()
			if actLabel != expLabel {
				t.Fatalf("iteration %d at index %d: expected %q, got %q", iter, idx, expLabel, actLabel)
			}
		}
	}
}

// TestBaseImages_Adversarial_EmptyBackendResponse ensures proper handling of zero images returned
func TestBaseImages_Adversarial_EmptyBackendResponse(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{},
		}, nil
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        types.ListNull(types.StringType),
		ReleaseStages: types.ListNull(types.StringType),
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error on empty base images: %v", resp.Diagnostics)
	}

	var stateResult github.BaseImagesDataSourceModel
	resp.State.Get(ctx, &stateResult)

	if stateResult.ID.ValueString() != "base_images" {
		t.Errorf("expected id 'base_images', got %q", stateResult.ID.ValueString())
	}
	if len(stateResult.Images) != 0 {
		t.Errorf("expected 0 images in state, got %d", len(stateResult.Images))
	}
}
