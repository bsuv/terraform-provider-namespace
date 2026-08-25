package testutil_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	githubv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/github/v1beta/githubv1betagrpc"
	iamv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/iam/v1beta/iamv1betagrpc"
	registryv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/registry/v1beta/registryv1betagrpc"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"

	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

func init() {
	grpcapi.RequireTransportSecurity = false
}

// TestMockServer_AllFiveServicesConcurrent calls all methods across all 5 mock services
// simultaneously from 100 goroutines to test concurrency and race safety.
func TestMockServer_AllFiveServicesConcurrent(t *testing.T) {
	srv := testutil.NewMockServer()

	// 1. Setup Profile Service Hooks
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_123",
				Spec:      req.Spec,
			},
		}, nil
	}
	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		return &githubv1beta.UpdateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: req.ProfileId,
				Spec:      req.Spec,
				Version:   req.UpdateVersion,
			},
		}, nil
	}
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{ProfileId: "prof_123"},
			},
		}, nil
	}
	srv.Profile.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	// 2. Setup Image Service Hooks
	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{Label: "ubuntu-24.04", ImageRef: "nscr.io/official/ubuntu:latest"},
			},
		}, nil
	}

	// 3. Setup Tenant Service Hooks
	srv.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		return &iamv1beta.TenantGroupResponse{Name: req.Name}, nil
	}
	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{Name: req.Name}, nil
	}
	srv.Tenant.ListTenantGroupsFunc = func(ctx context.Context, req *iamv1beta.ListTenantGroupsRequest) (*iamv1beta.ListTenantGroupsResponse, error) {
		return &iamv1beta.ListTenantGroupsResponse{
			TenantGroups: []*iamv1beta.ListTenantGroupsResponse_TenantGroup{
				{Name: "engineering"},
			},
		}, nil
	}
	srv.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	// 4. Setup Tenant Membership Service Hooks
	srv.Membership.ListTenantMembersFunc = func(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
		return &iamv1beta.ListTenantMembersResponse{
			Members: []*iamv1beta.TenantMember{
				{UserId: "usr_123", Email: "dev@example.com"},
			},
		}, nil
	}

	// 5. Setup Container Registry Service Hooks
	srv.Registry.ShareImageFunc = func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
		return &registryv1beta.SharedImage{Id: "share_123", SharedRepository: req.Repository}, nil
	}
	srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
		return &registryv1beta.ListSharedImagesResponse{SharedImages: []*registryv1beta.SharedImage{{Id: "share_123"}}}, nil
	}
	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		return &registryv1beta.GetDefaultPolicyResponse{Revision: 1, DefaultExpiration: durationpb.New(24 * time.Hour)}, nil
	}
	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return &registryv1beta.GetRepositoryPolicyResponse{Revision: 1, Expiration: durationpb.New(24 * time.Hour)}, nil
	}
	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	srv.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		return &registryv1beta.GetImageResponse{Image: &registryv1beta.Image{Repository: req.Repository}}, nil
	}
	srv.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{Images: []*registryv1beta.Image{{Repository: "my-app"}}}, nil
	}

	srv.Start()
	defer srv.Stop()

	// Dial gRPC in-memory bufconn connection
	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(srv.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufnet: %v", err)
	}
	defer func() { _ = conn.Close() }()

	profClient := githubv1betagrpc.NewProfileServiceClient(conn)
	imageClient := githubv1betagrpc.NewImageServiceClient(conn)
	tenantClient := iamv1betagrpc.NewTenantServiceClient(conn)
	membershipClient := iamv1betagrpc.NewTenantMembershipServiceClient(conn)
	registryClient := registryv1betagrpc.NewContainerRegistryServiceClient(conn)

	const numWorkers = 50
	const iterations = 20

	var wg sync.WaitGroup
	var callSuccesses int64

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				callType := (workerID + j) % 19
				switch callType {
				// Profile Service Calls
				case 0:
					resp, err := profClient.CreateProfile(ctx, &githubv1beta.CreateProfileRequest{
						Spec: &githubv1beta.RunnerProfileSpec{Tag: "runner-test"},
					})
					if err != nil || resp.Profile.ProfileId != "prof_123" {
						t.Errorf("CreateProfile failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 1:
					resp, err := profClient.UpdateProfile(ctx, &githubv1beta.UpdateProfileRequest{
						ProfileId:     "prof_123",
						Spec:          &githubv1beta.RunnerProfileSpec{Tag: "runner-updated"},
						UpdateVersion: 2,
					})
					if err != nil || resp.Profile.Version != 2 {
						t.Errorf("UpdateProfile failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 2:
					resp, err := profClient.ListProfiles(ctx, &emptypb.Empty{})
					if err != nil || len(resp.Profiles) != 1 {
						t.Errorf("ListProfiles failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 3:
					_, err := profClient.DeleteProfile(ctx, &githubv1beta.DeleteProfileRequest{ProfileId: "prof_123"})
					if err != nil {
						t.Errorf("DeleteProfile failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}

				// Image Service Calls
				case 4:
					resp, err := imageClient.ListBaseImages(ctx, &githubv1beta.ListBaseImagesRequest{})
					if err != nil || len(resp.Images) != 1 {
						t.Errorf("ListBaseImages failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}

				// Tenant Service Calls
				case 5:
					resp, err := tenantClient.EnsureTenantGroup(ctx, &iamv1beta.EnsureTenantGroupRequest{Name: "engineering"})
					if err != nil || resp.Name != "engineering" {
						t.Errorf("EnsureTenantGroup failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 6:
					resp, err := tenantClient.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: "engineering"})
					if err != nil || resp.Name != "engineering" {
						t.Errorf("DescribeTenantGroup failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 7:
					resp, err := tenantClient.ListTenantGroups(ctx, &iamv1beta.ListTenantGroupsRequest{})
					if err != nil || len(resp.TenantGroups) != 1 {
						t.Errorf("ListTenantGroups failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 8:
					_, err := tenantClient.DeleteTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: "engineering"})
					if err != nil {
						t.Errorf("DeleteTenantGroup failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}

				// Tenant Membership Service Calls
				case 9:
					resp, err := membershipClient.ListTenantMembers(ctx, &iamv1beta.ListTenantMembersRequest{})
					if err != nil || len(resp.Members) != 1 {
						t.Errorf("ListTenantMembers failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}

				// Container Registry Service Calls
				case 10:
					resp, err := registryClient.ShareImage(ctx, &registryv1beta.ShareImageRequest{Repository: "my-app"})
					if err != nil || resp.Id != "share_123" {
						t.Errorf("ShareImage failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 11:
					_, err := registryClient.UnshareImage(ctx, &registryv1beta.UnshareImageRequest{Id: "share_123"})
					if err != nil {
						t.Errorf("UnshareImage failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 12:
					resp, err := registryClient.ListSharedImages(ctx, &registryv1beta.ListSharedImagesRequest{})
					if err != nil || len(resp.SharedImages) != 1 {
						t.Errorf("ListSharedImages failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 13:
					resp, err := registryClient.GetDefaultPolicy(ctx, &emptypb.Empty{})
					if err != nil || resp.Revision != 1 {
						t.Errorf("GetDefaultPolicy failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 14:
					_, err := registryClient.UpdateDefaultPolicy(ctx, &registryv1beta.UpdateDefaultPolicyRequest{})
					if err != nil {
						t.Errorf("UpdateDefaultPolicy failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 15:
					resp, err := registryClient.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: "my-app"})
					if err != nil || resp.Revision != 1 {
						t.Errorf("GetRepositoryPolicy failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 16:
					_, err := registryClient.UpdateRepositoryPolicy(ctx, &registryv1beta.UpdateRepositoryPolicyRequest{Repository: "my-app"})
					if err != nil {
						t.Errorf("UpdateRepositoryPolicy failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 17:
					resp, err := registryClient.GetImage(ctx, &registryv1beta.GetImageRequest{Repository: "my-app"})
					if err != nil || resp.Image.Repository != "my-app" {
						t.Errorf("GetImage failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				case 18:
					resp, err := registryClient.ListImages(ctx, &registryv1beta.ListImagesRequest{})
					if err != nil || len(resp.Images) != 1 {
						t.Errorf("ListImages failed: %v", err)
					} else {
						atomic.AddInt64(&callSuccesses, 1)
					}
				}
			}
		}(i)
	}

	wg.Wait()
	t.Logf("MockServer 5-service concurrency stress passed: %d successful RPCs", atomic.LoadInt64(&callSuccesses))
}

// TestMockServer_UnimplementedBehavior verifies that unconfigured hooks return codes.Unimplemented
func TestMockServer_UnimplementedBehavior(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Start()
	defer srv.Stop()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(srv.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	profClient := githubv1betagrpc.NewProfileServiceClient(conn)
	imageClient := githubv1betagrpc.NewImageServiceClient(conn)
	tenantClient := iamv1betagrpc.NewTenantServiceClient(conn)
	membershipClient := iamv1betagrpc.NewTenantMembershipServiceClient(conn)
	registryClient := registryv1betagrpc.NewContainerRegistryServiceClient(conn)

	// All calls should return codes.Unimplemented
	_, err = profClient.CreateProfile(ctx, &githubv1beta.CreateProfileRequest{})
	assertUnimplemented(t, "Profile.CreateProfile", err)

	_, err = imageClient.ListBaseImages(ctx, &githubv1beta.ListBaseImagesRequest{})
	assertUnimplemented(t, "Image.ListBaseImages", err)

	_, err = tenantClient.EnsureTenantGroup(ctx, &iamv1beta.EnsureTenantGroupRequest{})
	assertUnimplemented(t, "Tenant.EnsureTenantGroup", err)

	_, err = membershipClient.ListTenantMembers(ctx, &iamv1beta.ListTenantMembersRequest{})
	assertUnimplemented(t, "Membership.ListTenantMembers", err)

	_, err = registryClient.ShareImage(ctx, &registryv1beta.ShareImageRequest{})
	assertUnimplemented(t, "Registry.ShareImage", err)
}

// TestMockServer_RebuildProfileImage_EmpiricalCheck tests whether RebuildProfileImageFunc
// is properly invoked when configured on MockProfileServiceServer.
func TestMockServer_RebuildProfileImage_EmpiricalCheck(t *testing.T) {
	srv := testutil.NewMockServer()
	var hookCalled bool
	srv.Profile.RebuildProfileImageFunc = func(ctx context.Context, req *githubv1beta.RebuildProfileImageRequest) (*githubv1beta.RebuildProfileImageResponse, error) {
		hookCalled = true
		return &githubv1beta.RebuildProfileImageResponse{
			Status: &githubv1beta.RunnerProfileStatus{},
		}, nil
	}

	srv.Start()
	defer srv.Stop()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(srv.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	profClient := githubv1betagrpc.NewProfileServiceClient(conn)
	resp, err := profClient.RebuildProfileImage(ctx, &githubv1beta.RebuildProfileImageRequest{
		ProfileId: "prof_rebuild_123",
	})

	if !hookCalled {
		t.Errorf("EMPIRICAL BUG CONFIRMED: MockProfileServiceServer.RebuildProfileImageFunc was configured but NOT invoked! (err=%v, resp=%v)", err, resp)
	}
}

// TestMockServer_RebuildProfileImage_DefaultBehavior verifies that when RebuildProfileImageFunc is nil,
// it returns an empty RebuildProfileImageResponse without error.
func TestMockServer_RebuildProfileImage_DefaultBehavior(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Profile.RebuildProfileImageFunc = nil // explicit nil
	srv.Start()
	defer srv.Stop()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(srv.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	profClient := githubv1betagrpc.NewProfileServiceClient(conn)
	resp, err := profClient.RebuildProfileImage(ctx, &githubv1beta.RebuildProfileImageRequest{
		ProfileId: "prof_default_test",
	})
	if err != nil {
		t.Fatalf("expected nil error for default RebuildProfileImage, got: %v", err)
	}
	if resp == nil {
		t.Fatalf("expected non-nil response for default RebuildProfileImage, got nil")
	}
}

// TestMockServer_RebuildProfileImage_CustomHooksAndErrors tests various gRPC status errors
// and custom response payloads returned by RebuildProfileImageFunc.
func TestMockServer_RebuildProfileImage_CustomHooksAndErrors(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Start()
	defer srv.Stop()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(srv.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	profClient := githubv1betagrpc.NewProfileServiceClient(conn)

	// Sub-test 1: NotFound error
	srv.Profile.RebuildProfileImageFunc = func(ctx context.Context, req *githubv1beta.RebuildProfileImageRequest) (*githubv1beta.RebuildProfileImageResponse, error) {
		if req.ProfileId != "prof_not_found" {
			t.Errorf("unexpected profile ID: %s", req.ProfileId)
		}
		return nil, status.Error(codes.NotFound, "profile not found")
	}
	_, err = profClient.RebuildProfileImage(ctx, &githubv1beta.RebuildProfileImageRequest{ProfileId: "prof_not_found"})
	if st, ok := status.FromError(err); !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound code, got: %v", err)
	}

	// Sub-test 2: InvalidArgument error
	srv.Profile.RebuildProfileImageFunc = func(ctx context.Context, req *githubv1beta.RebuildProfileImageRequest) (*githubv1beta.RebuildProfileImageResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid profile id")
	}
	_, err = profClient.RebuildProfileImage(ctx, &githubv1beta.RebuildProfileImageRequest{ProfileId: ""})
	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument code, got: %v", err)
	}

	// Sub-test 3: Populated status response
	srv.Profile.RebuildProfileImageFunc = func(ctx context.Context, req *githubv1beta.RebuildProfileImageRequest) (*githubv1beta.RebuildProfileImageResponse, error) {
		return &githubv1beta.RebuildProfileImageResponse{
			Status: &githubv1beta.RunnerProfileStatus{
				CustomRunnerImage: []*githubv1beta.CustomRunnerImage{
					{
						ImageStatus: &githubv1beta.CustomRunnerImageStatus{
							ImageRef: "nscr.io/org/custom-runner:latest",
							Status:   githubv1beta.CustomRunnerImageStatus_READY,
						},
					},
				},
			},
		}, nil
	}
	resp, err := profClient.RebuildProfileImage(ctx, &githubv1beta.RebuildProfileImageRequest{ProfileId: "prof_valid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetStatus().GetCustomRunnerImage()) == 0 ||
		resp.GetStatus().GetCustomRunnerImage()[0].GetImageStatus().GetImageRef() != "nscr.io/org/custom-runner:latest" {
		t.Fatalf("unexpected image ref in response: %v", resp.GetStatus())
	}
}

// TestMockServer_RebuildProfileImage_ConcurrencyAndRace tests 100 concurrent workers
// calling RebuildProfileImage while hooks dynamically process different profile IDs.
func TestMockServer_RebuildProfileImage_ConcurrencyAndRace(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Profile.RebuildProfileImageFunc = func(ctx context.Context, req *githubv1beta.RebuildProfileImageRequest) (*githubv1beta.RebuildProfileImageResponse, error) {
		if req.ProfileId == "fail" {
			return nil, status.Error(codes.Internal, "rebuild failed")
		}
		return &githubv1beta.RebuildProfileImageResponse{
			Status: &githubv1beta.RunnerProfileStatus{
				CustomRunnerImage: []*githubv1beta.CustomRunnerImage{
					{
						ImageStatus: &githubv1beta.CustomRunnerImageStatus{
							ImageRef: "image-for-" + req.ProfileId,
							Status:   githubv1beta.CustomRunnerImageStatus_READY,
						},
					},
				},
			},
		}, nil
	}

	srv.Start()
	defer srv.Stop()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(srv.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	profClient := githubv1betagrpc.NewProfileServiceClient(conn)

	const numWorkers = 50
	const numIterations = 20
	var wg sync.WaitGroup
	var successes int64
	var errors int64

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				if (workerID+j)%2 == 0 {
					resp, err := profClient.RebuildProfileImage(ctx, &githubv1beta.RebuildProfileImageRequest{
						ProfileId: "prof_worker",
					})
					if err != nil || len(resp.GetStatus().GetCustomRunnerImage()) == 0 ||
						resp.GetStatus().GetCustomRunnerImage()[0].GetImageStatus().GetImageRef() != "image-for-prof_worker" {
						t.Errorf("unexpected failure on success case: %v", err)
					} else {
						atomic.AddInt64(&successes, 1)
					}
				} else {
					_, err := profClient.RebuildProfileImage(ctx, &githubv1beta.RebuildProfileImageRequest{
						ProfileId: "fail",
					})
					if st, ok := status.FromError(err); !ok || st.Code() != codes.Internal {
						t.Errorf("unexpected error for fail case: %v", err)
					} else {
						atomic.AddInt64(&errors, 1)
					}
				}
			}
		}(i)
	}

	wg.Wait()
	t.Logf("RebuildProfileImage concurrency test passed: %d successes, %d expected errors",
		atomic.LoadInt64(&successes), atomic.LoadInt64(&errors))
}

func assertUnimplemented(t *testing.T, methodName string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: expected Unimplemented error, got nil", methodName)
		return
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unimplemented {
		t.Errorf("%s: expected code Unimplemented, got %v", methodName, err)
	}
}
