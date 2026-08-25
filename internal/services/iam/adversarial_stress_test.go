package iam_test

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// --- Mission 1: Preflight Collision Handling on Create ---

func TestAdversarial_TenantGroup_Create_PreflightScenarios(t *testing.T) {
	testCases := []struct {
		name                 string
		preflightError       error
		preflightResp        *iamv1beta.DescribeTenantGroupResponse
		expectEnsureCalled   bool
		expectErrorSubstring string
	}{
		{
			name: "Group exists remotely -> preflight collision error with import instructions",
			preflightResp: &iamv1beta.DescribeTenantGroupResponse{
				Name: "existing-group",
				Policies: []*iamv1beta.TenantGroupPolicy{
					{Policy: "pol1", Value: "val1"},
				},
				EnrolledTenantIds: []string{"tenant-1"},
			},
			expectEnsureCalled:   false,
			expectErrorSubstring: "terraform import namespace_tenant_group.existing-group existing-group",
		},
		{
			name:                 "Preflight returns PermissionDenied -> abort without calling Ensure",
			preflightError:       status.Error(codes.PermissionDenied, "caller does not have iam.describe permission"),
			expectEnsureCalled:   false,
			expectErrorSubstring: "caller does not have iam.describe permission",
		},
		{
			name:                 "Preflight returns Unavailable -> abort without calling Ensure",
			preflightError:       status.Error(codes.Unavailable, "iam backend unavailable"),
			expectEnsureCalled:   false,
			expectErrorSubstring: "iam backend unavailable",
		},
		{
			name:                 "Preflight returns Internal error -> abort without calling Ensure",
			preflightError:       status.Error(codes.Internal, "database connection error"),
			expectEnsureCalled:   false,
			expectErrorSubstring: "database connection error",
		},
		{
			name:                 "Preflight returns Unauthenticated -> abort without calling Ensure",
			preflightError:       status.Error(codes.Unauthenticated, "invalid token"),
			expectEnsureCalled:   false,
			expectErrorSubstring: "invalid token",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv := testutil.NewMockServer()

			var ensureCalled bool
			srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
				if tc.preflightError != nil {
					return nil, tc.preflightError
				}
				return tc.preflightResp, nil
			}

			srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
				ensureCalled = true
				return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
			}

			r, schemaResp := setupTestTenantGroupResource(t, srv)

			planModel := iam.TenantGroupResourceModel{
				ID:                types.StringUnknown(),
				Name:              types.StringValue("existing-group"),
				Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
				EnrolledTenantIDs: types.ListNull(types.StringType),
			}

			planState := tfsdk.State{Schema: schemaResp.Schema}
			diags := planState.Set(ctx, &planModel)
			if diags.HasError() {
				t.Fatalf("failed to set plan state: %v", diags)
			}

			req := resource.CreateRequest{
				Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
			}
			resp := &resource.CreateResponse{
				State: tfsdk.State{Schema: schemaResp.Schema},
			}

			r.Create(ctx, req, resp)

			if tc.expectEnsureCalled != ensureCalled {
				t.Errorf("expected EnsureTenantGroup called=%v, got=%v", tc.expectEnsureCalled, ensureCalled)
			}

			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected error diagnostics, got none")
			}

			errString := ""
			for _, errDiag := range resp.Diagnostics.Errors() {
				errString += errDiag.Summary() + " : " + errDiag.Detail() + "\n"
			}

			if !strings.Contains(errString, tc.expectErrorSubstring) {
				t.Errorf("expected error containing %q, got: %s", tc.expectErrorSubstring, errString)
			}
		})
	}
}

func TestAdversarial_TenantGroup_Create_RaceCondition_EnsureFailsAlreadyExists(t *testing.T) {
	// Scenario: Preflight DescribeTenantGroup returns NotFound, but concurrent creator creates it before EnsureTenantGroup runs.
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var describeCalls int
	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		describeCalls++
		return nil, status.Error(codes.NotFound, "group not found")
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		return nil, status.Error(codes.AlreadyExists, "concurrent conflict: group already created")
	}

	r, schemaResp := setupTestTenantGroupResource(t, srv)

	planModel := iam.TenantGroupResourceModel{
		ID:                types.StringUnknown(),
		Name:              types.StringValue("race-group"),
		Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
		EnrolledTenantIDs: types.ListNull(types.StringType),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Create(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic when EnsureTenantGroup returns AlreadyExists, got none")
	}

	if describeCalls != 1 {
		t.Errorf("expected exactly 1 preflight describe call, got %d", describeCalls)
	}
}

// --- Mission 2: Deletion Preconditions & Errors ---

func TestAdversarial_TenantGroup_Delete_VariousGRPCStatuses(t *testing.T) {
	testCases := []struct {
		name          string
		grpcErr       error
		expectSuccess bool
		expectErrSub  string
	}{
		{
			name:          "Active tenants enrolled -> FailedPrecondition",
			grpcErr:       status.Error(codes.FailedPrecondition, "cannot delete tenant group with 3 active enrolled tenants"),
			expectSuccess: false,
			expectErrSub:  "cannot delete tenant group with 3 active enrolled tenants",
		},
		{
			name:          "NotFound -> idempotent delete success (already deleted)",
			grpcErr:       status.Error(codes.NotFound, "tenant group already deleted"),
			expectSuccess: true,
		},
		{
			name:          "PermissionDenied -> error diagnostic",
			grpcErr:       status.Error(codes.PermissionDenied, "user lacks tenant group deletion privileges"),
			expectSuccess: false,
			expectErrSub:  "user lacks tenant group deletion privileges",
		},
		{
			name:          "Unavailable -> error diagnostic",
			grpcErr:       status.Error(codes.Unavailable, "cluster currently undergoing maintenance"),
			expectSuccess: false,
			expectErrSub:  "cluster currently undergoing maintenance",
		},
		{
			name:          "Internal -> error diagnostic",
			grpcErr:       status.Error(codes.Internal, "unexpected database crash"),
			expectSuccess: false,
			expectErrSub:  "unexpected database crash",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv := testutil.NewMockServer()

			srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
				return nil, tc.grpcErr
			}

			r, schemaResp := setupTestTenantGroupResource(t, srv)

			stateModel := iam.TenantGroupResourceModel{
				ID:                types.StringValue("grp-del-test"),
				Name:              types.StringValue("grp-del-test"),
				Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
				EnrolledTenantIDs: types.ListNull(types.StringType),
			}

			state := tfsdk.State{Schema: schemaResp.Schema}
			_ = state.Set(ctx, &stateModel)

			req := resource.DeleteRequest{State: state}
			resp := &resource.DeleteResponse{State: state}

			r.Delete(ctx, req, resp)

			if tc.expectSuccess {
				if resp.Diagnostics.HasError() {
					t.Fatalf("expected delete to succeed, got errors: %v", resp.Diagnostics)
				}
			} else {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected delete error, got success")
				}
				var allErrors []string
				for _, errDiag := range resp.Diagnostics.Errors() {
					allErrors = append(allErrors, errDiag.Summary()+": "+errDiag.Detail())
				}
				combined := strings.Join(allErrors, " | ")
				if !strings.Contains(combined, tc.expectErrSub) {
					t.Errorf("expected error containing %q, got: %s", tc.expectErrSub, combined)
				}
			}
		})
	}
}

// --- Mission 3: Import Behavior for Valid and Non-existent IDs ---

func TestAdversarial_TenantGroup_ImportState_EdgeCases(t *testing.T) {
	testCases := []struct {
		name          string
		importID      string
		mockResp      *iamv1beta.DescribeTenantGroupResponse
		mockErr       error
		expectError   bool
		expectSummary string
	}{
		{
			name:     "Valid ID with populated policies and tenants",
			importID: "production-group",
			mockResp: &iamv1beta.DescribeTenantGroupResponse{
				Name: "production-group",
				Policies: []*iamv1beta.TenantGroupPolicy{
					{Policy: "pol.a", Value: "val.a"},
					{Policy: "pol.b", Value: "val.b"},
				},
				EnrolledTenantIds: []string{"t1", "t2", "t3"},
			},
			expectError: false,
		},
		{
			name:     "Valid ID with nil slices from proto response",
			importID: "empty-fields-group",
			mockResp: &iamv1beta.DescribeTenantGroupResponse{
				Name:              "empty-fields-group",
				Policies:          nil,
				EnrolledTenantIds: nil,
			},
			expectError: false,
		},
		{
			name:          "Non-existent ID (NotFound)",
			importID:      "does-not-exist",
			mockErr:       status.Error(codes.NotFound, "group does not exist"),
			expectError:   true,
			expectSummary: "Resource Not Found",
		},
		{
			name:          "Empty string ID",
			importID:      "",
			expectError:   true,
			expectSummary: "Invalid Import ID",
		},
		{
			name:          "PermissionDenied during import",
			importID:      "restricted-group",
			mockErr:       status.Error(codes.PermissionDenied, "access denied"),
			expectError:   true,
			expectSummary: "Authorization Failure",
		},
		{
			name:          "Unavailable during import",
			importID:      "any-group",
			mockErr:       status.Error(codes.Unavailable, "service unavailable"),
			expectError:   true,
			expectSummary: "gRPC Error (Unavailable)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv := testutil.NewMockServer()

			srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
				if req.Name != tc.importID {
					t.Errorf("unexpected describe request ID: %s, expected: %s", req.Name, tc.importID)
				}
				if tc.mockErr != nil {
					return nil, tc.mockErr
				}
				return tc.mockResp, nil
			}

			r, schemaResp := setupTestTenantGroupResource(t, srv)
			importR, ok := r.(resource.ResourceWithImportState)
			if !ok {
				t.Fatal("expected resource to implement ResourceWithImportState")
			}

			req := resource.ImportStateRequest{ID: tc.importID}
			resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

			importR.ImportState(ctx, req, resp)

			if tc.expectError {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected error during import of %q, got none", tc.importID)
				}
				found := false
				for _, d := range resp.Diagnostics.Errors() {
					if strings.Contains(d.Summary(), tc.expectSummary) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected diagnostic summary containing %q, got: %v", tc.expectSummary, resp.Diagnostics)
				}
			} else {
				if resp.Diagnostics.HasError() {
					t.Fatalf("unexpected import error for %q: %v", tc.importID, resp.Diagnostics)
				}
				var state iam.TenantGroupResourceModel
				diags := resp.State.Get(ctx, &state)
				if diags.HasError() {
					t.Fatalf("failed to decode state: %v", diags)
				}
				if state.ID.ValueString() != tc.importID || state.Name.ValueString() != tc.importID {
					t.Errorf("expected state ID and Name to be %q, got id=%q name=%q",
						tc.importID, state.ID.ValueString(), state.Name.ValueString())
				}
				if tc.mockResp.Policies != nil {
					if len(state.Policies.Elements()) != len(tc.mockResp.Policies) {
						t.Errorf("expected %d policies, got %d", len(tc.mockResp.Policies), len(state.Policies.Elements()))
					}
				} else {
					if !state.Policies.IsNull() && len(state.Policies.Elements()) != 0 {
						t.Errorf("expected empty/null policies slice for nil proto policies, got %d", len(state.Policies.Elements()))
					}
				}
			}
		})
	}
}

// --- Mission 4: Deterministic Sorting under Randomized & Adversarial Inputs ---

func TestAdversarial_TenantMembers_DeterministicSorting_RandomizedStress(t *testing.T) {
	// Generate a deterministic base set of 1,000 members with distinct and edge-case IDs
	const memberCount = 1000
	baseMembers := make([]*iamv1beta.TenantMember, 0, memberCount)

	// Include special Unicode, prefix collision, and boundary user IDs
	specialIDs := []string{
		"usr_0000",
		"usr_0001",
		"usr_00010",
		"usr_0001a",
		"usr_0001b",
		"usr_0002",
		"usr_001",
		"usr_01",
		"usr_1",
		"usr_10",
		"usr_100",
		"usr_1000",
		"usr_2",
		"usr_20",
		"usr_A",
		"usr_a",
		"usr_B",
		"usr_b",
		"usr_α", // Greek alpha
		"usr_β", // Greek beta
		"usr_🚀", // Emoji
		"usr_🌟",
		"usr_–dash–",
		"usr_—emdash—",
		"usr_with spaces",
		"usr_with.dots",
		"usr_with_underscores",
		"usr_zzz_end",
	}

	for _, id := range specialIDs {
		baseMembers = append(baseMembers, &iamv1beta.TenantMember{
			UserId:    id,
			FirstName: "Special",
			LastName:  id,
			Email:     id + "@example.com",
			Role:      iamv1beta.TenantMemberRole_TENANT_MEMBER_ROLE_READER,
		})
	}

	for i := len(specialIDs); i < memberCount; i++ {
		uid := fmt.Sprintf("usr_rand_%08x", i*7919)
		baseMembers = append(baseMembers, &iamv1beta.TenantMember{
			UserId:    uid,
			FirstName: fmt.Sprintf("First_%d", i),
			LastName:  fmt.Sprintf("Last_%d", i),
			Email:     fmt.Sprintf("user%d@example.com", i),
			Role:      iamv1beta.TenantMemberRole(i%6 + 1),
		})
	}

	// Compute the canonical ground truth sort
	groundTruth := make([]string, len(baseMembers))
	for i, m := range baseMembers {
		groundTruth[i] = m.UserId
	}
	sort.Strings(groundTruth)

	// Run 100 randomized shuffle iterations
	const iterations = 100
	r := rand.New(rand.NewSource(133742))

	ctx := context.Background()

	for iter := 0; iter < iterations; iter++ {
		// Create shuffled copy of baseMembers
		shuffled := make([]*iamv1beta.TenantMember, len(baseMembers))
		copy(shuffled, baseMembers)
		r.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})

		srv := testutil.NewMockServer()
		srv.Membership.ListTenantMembersFunc = func(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
			return &iamv1beta.ListTenantMembersResponse{Members: shuffled}, nil
		}

		d, schemaResp := setupTestDataSourceTenantMembers(t, srv)

		configModel := iam.TenantMembersDataSourceModel{
			ID:      types.StringNull(),
			Members: nil,
		}

		configState := tfsdk.State{Schema: schemaResp.Schema}
		_ = configState.Set(ctx, &configModel)

		req := datasource.ReadRequest{
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
		}
		resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

		d.Read(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("iteration %d: unexpected read error: %v", iter, resp.Diagnostics)
		}

		var state iam.TenantMembersDataSourceModel
		diags := resp.State.Get(ctx, &state)
		if diags.HasError() {
			t.Fatalf("iteration %d: failed to get state: %v", iter, diags)
		}

		if len(state.Members) != len(groundTruth) {
			t.Fatalf("iteration %d: expected %d members, got %d", iter, len(groundTruth), len(state.Members))
		}

		// Verify state members exactly match canonical ground truth
		for i, expectedID := range groundTruth {
			actualID := state.Members[i].UserID.ValueString()
			if actualID != expectedID {
				t.Fatalf("iteration %d mismatch at index %d: expected %q, got %q",
					iter, i, expectedID, actualID)
			}
		}
	}
	t.Logf("Successfully verified deterministic sorting across %d random permutations of %d members",
		iterations, memberCount)
}

// --- Concurrency and Race Stress Tests ---

func TestAdversarial_IAM_ConcurrentWorkload(t *testing.T) {
	srv := testutil.NewMockServer()

	var createdCount int64
	var readCount int64
	var deleteCount int64

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
			Policies: []*iamv1beta.TenantGroupPolicy{
				{Policy: "pol1", Value: "val1"},
			},
			EnrolledTenantIds: []string{"tenant_1"},
		}, nil
	}

	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}

	srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	srv.Membership.ListTenantMembersFunc = func(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
		return &iamv1beta.ListTenantMembersResponse{
			Members: []*iamv1beta.TenantMember{
				{UserId: "usr_2", Email: "b@test.com"},
				{UserId: "usr_1", Email: "a@test.com"},
			},
		}, nil
	}

	const workers = 30
	const opsPerWorker = 30

	var wg sync.WaitGroup
	ctx := context.Background()

	cl := testutil.NewTestClient(t, srv)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			r := iam.NewTenantGroupResource()
			var schemaRespR resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schemaRespR)
			if confR, ok := r.(resource.ResourceWithConfigure); ok {
				var confResp resource.ConfigureResponse
				confR.Configure(ctx, resource.ConfigureRequest{ProviderData: cl}, &confResp)
			}

			dsGroup := iam.NewTenantGroupDataSource()
			var schemaRespDSG datasource.SchemaResponse
			dsGroup.Schema(ctx, datasource.SchemaRequest{}, &schemaRespDSG)
			if confD, ok := dsGroup.(datasource.DataSourceWithConfigure); ok {
				var confResp datasource.ConfigureResponse
				confD.Configure(ctx, datasource.ConfigureRequest{ProviderData: cl}, &confResp)
			}

			dsMembers := iam.NewTenantMembersDataSource()
			var schemaRespDSM datasource.SchemaResponse
			dsMembers.Schema(ctx, datasource.SchemaRequest{}, &schemaRespDSM)
			if confD, ok := dsMembers.(datasource.DataSourceWithConfigure); ok {
				var confResp datasource.ConfigureResponse
				confD.Configure(ctx, datasource.ConfigureRequest{ProviderData: cl}, &confResp)
			}

			for i := 0; i < opsPerWorker; i++ {
				op := (workerID*opsPerWorker + i) % 5
				switch op {
				case 0:
					// Resource Read
					stateModel := iam.TenantGroupResourceModel{
						ID:                types.StringValue(fmt.Sprintf("grp-%d", workerID)),
						Name:              types.StringValue(fmt.Sprintf("grp-%d", workerID)),
						Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
						EnrolledTenantIDs: types.ListNull(types.StringType),
					}
					st := tfsdk.State{Schema: schemaRespR.Schema}
					_ = st.Set(ctx, &stateModel)
					req := resource.ReadRequest{State: st}
					resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaRespR.Schema}}
					r.Read(ctx, req, resp)
					if resp.Diagnostics.HasError() {
						t.Errorf("concurrent resource read failed: %v", resp.Diagnostics)
					}

				case 1:
					// Resource Delete
					stateModel := iam.TenantGroupResourceModel{
						ID:                types.StringValue(fmt.Sprintf("grp-%d", workerID)),
						Name:              types.StringValue(fmt.Sprintf("grp-%d", workerID)),
						Policies:          types.ListNull(iam.TenantGroupPolicyObjectType),
						EnrolledTenantIDs: types.ListNull(types.StringType),
					}
					st := tfsdk.State{Schema: schemaRespR.Schema}
					_ = st.Set(ctx, &stateModel)
					req := resource.DeleteRequest{State: st}
					resp := &resource.DeleteResponse{State: st}
					r.Delete(ctx, req, resp)
					if resp.Diagnostics.HasError() {
						t.Errorf("concurrent resource delete failed: %v", resp.Diagnostics)
					}

				case 2:
					// Resource Import
					importR, ok := r.(resource.ResourceWithImportState)
					if ok {
						req := resource.ImportStateRequest{ID: fmt.Sprintf("grp-%d", workerID)}
						resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaRespR.Schema}}
						importR.ImportState(ctx, req, resp)
						if resp.Diagnostics.HasError() {
							t.Errorf("concurrent resource import failed: %v", resp.Diagnostics)
						}
					}

				case 3:
					// Data Source Tenant Group Read
					cfg := iam.TenantGroupDataSourceModel{
						ID:                types.StringNull(),
						Name:              types.StringValue(fmt.Sprintf("grp-%d", workerID)),
						Policies:          nil,
						EnrolledTenantIDs: types.ListNull(types.StringType),
						Versions:          nil,
					}
					st := tfsdk.State{Schema: schemaRespDSG.Schema}
					_ = st.Set(ctx, &cfg)
					req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaRespDSG.Schema, Raw: st.Raw}}
					resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaRespDSG.Schema}}
					dsGroup.Read(ctx, req, resp)
					if resp.Diagnostics.HasError() {
						t.Errorf("concurrent data source group read failed: %v", resp.Diagnostics)
					}

				case 4:
					// Data Source Tenant Members Read
					cfg := iam.TenantMembersDataSourceModel{
						ID:      types.StringNull(),
						Members: nil,
					}
					st := tfsdk.State{Schema: schemaRespDSM.Schema}
					_ = st.Set(ctx, &cfg)
					req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaRespDSM.Schema, Raw: st.Raw}}
					resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaRespDSM.Schema}}
					dsMembers.Read(ctx, req, resp)
					if resp.Diagnostics.HasError() {
						t.Errorf("concurrent data source members read failed: %v", resp.Diagnostics)
					}
				}
			}
		}(w)
	}

	wg.Wait()
	_ = createdCount
	_ = readCount
	_ = deleteCount
	t.Logf("Concurrent IAM operations stress test passed across %d goroutines", workers)
}
