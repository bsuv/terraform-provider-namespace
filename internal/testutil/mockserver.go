package testutil

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	builderv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/builder/v1beta/builderv1betagrpc"
	computev1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/compute/v1beta/computev1betagrpc"
	githubv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/github/v1beta/githubv1betagrpc"
	iamv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/iam/v1beta/iamv1betagrpc"
	bazelv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/bazel/v1beta/bazelv1betagrpc"
	gradlev1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/gradle/v1beta/gradlev1betagrpc"
	httpcachev1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/httpcache/v1beta/httpcachev1betagrpc"
	registryv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/registry/v1beta/registryv1betagrpc"
	vaultv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/vault/v1beta/vaultv1betagrpc"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	gradlev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/gradle/v1beta"
	httpcachev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/httpcache/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	vaultv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/vault/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const bufSize = 1024 * 1024

// MockServer provides an in-memory gRPC test server using bufconn.
type MockServer struct {
	listener *bufconn.Listener
	server   *grpc.Server

	Profile     *MockProfileServiceServer
	Image       *MockImageServiceServer
	Tenant      *MockTenantServiceServer
	Membership  *MockTenantMembershipServiceServer
	Registry    *MockContainerRegistryServiceServer
	Token       *MockTokenServiceServer
	BazelCache  *MockBazelCacheServiceServer
	GradleCache *MockGradleCacheServiceServer
	HttpCache   *MockHttpCacheServiceServer
	Storage     *MockStorageServiceServer
	Builder     *MockBuilderServiceServer
	Compute     *MockComputeServiceServer
	Vault       *MockVaultServiceServer
}

// NewMockServer creates and initializes an in-memory gRPC MockServer with all services.
func NewMockServer() *MockServer {
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()

	mockProfile := &MockProfileServiceServer{}
	mockImage := &MockImageServiceServer{}
	mockTenant := &MockTenantServiceServer{}
	mockMembership := &MockTenantMembershipServiceServer{}
	mockRegistry := &MockContainerRegistryServiceServer{}
	mockToken := &MockTokenServiceServer{}
	mockBazel := &MockBazelCacheServiceServer{}
	mockGradle := &MockGradleCacheServiceServer{}
	mockHttp := &MockHttpCacheServiceServer{}
	mockStorage := &MockStorageServiceServer{}
	mockBuilder := &MockBuilderServiceServer{}
	mockCompute := &MockComputeServiceServer{}
	mockVault := &MockVaultServiceServer{}

	githubv1betagrpc.RegisterProfileServiceServer(srv, mockProfile)
	githubv1betagrpc.RegisterImageServiceServer(srv, mockImage)
	iamv1betagrpc.RegisterTenantServiceServer(srv, mockTenant)
	iamv1betagrpc.RegisterTenantMembershipServiceServer(srv, mockMembership)
	registryv1betagrpc.RegisterContainerRegistryServiceServer(srv, mockRegistry)
	iamv1betagrpc.RegisterTokenServiceServer(srv, mockToken)
	bazelv1betagrpc.RegisterBazelCacheServiceServer(srv, mockBazel)
	gradlev1betagrpc.RegisterGradleCacheServiceServer(srv, mockGradle)
	httpcachev1betagrpc.RegisterHttpCacheServiceServer(srv, mockHttp)
	computev1betagrpc.RegisterStorageServiceServer(srv, mockStorage)
	builderv1betagrpc.RegisterBuilderServiceServer(srv, mockBuilder)
	computev1betagrpc.RegisterComputeServiceServer(srv, mockCompute)
	vaultv1betagrpc.RegisterVaultServiceServer(srv, mockVault)

	return &MockServer{
		listener:    lis,
		server:      srv,
		Profile:     mockProfile,
		Image:       mockImage,
		Tenant:      mockTenant,
		Membership:  mockMembership,
		Registry:    mockRegistry,
		Token:       mockToken,
		BazelCache:  mockBazel,
		GradleCache: mockGradle,
		HttpCache:   mockHttp,
		Storage:     mockStorage,
		Builder:     mockBuilder,
		Compute:     mockCompute,
		Vault:       mockVault,
	}
}

// Start launches the in-memory server in a background goroutine.
func (s *MockServer) Start() {
	go func() {
		_ = s.server.Serve(s.listener)
	}()
}

// Stop gracefully stops the server and closes the buffer listener.
func (s *MockServer) Stop() {
	s.server.GracefulStop()
	_ = s.listener.Close()
}

// Close immediately halts the server and listener.
func (s *MockServer) Close() {
	s.server.Stop()
	_ = s.listener.Close()
}

// Dialer returns a net dialer function connected to the bufconn listener.
func (s *MockServer) Dialer() func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return s.listener.DialContext(ctx)
	}
}

// --- Mock Service Implementations ---

// MockProfileServiceServer implements ProfileServiceServer with hook functions.
type MockProfileServiceServer struct {
	githubv1betagrpc.UnimplementedProfileServiceServer
	mu sync.Mutex

	CreateProfileFunc       func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error)
	UpdateProfileFunc       func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error)
	ListProfilesFunc        func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error)
	DeleteProfileFunc       func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error)
	RebuildProfileImageFunc func(ctx context.Context, req *githubv1beta.RebuildProfileImageRequest) (*githubv1beta.RebuildProfileImageResponse, error)
}

func (m *MockProfileServiceServer) CreateProfile(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateProfileFunc != nil {
		return m.CreateProfileFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method CreateProfile not implemented")
}

func (m *MockProfileServiceServer) UpdateProfile(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateProfileFunc != nil {
		return m.UpdateProfileFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method UpdateProfile not implemented")
}

func (m *MockProfileServiceServer) ListProfiles(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListProfilesFunc != nil {
		return m.ListProfilesFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ListProfiles not implemented")
}

func (m *MockProfileServiceServer) DeleteProfile(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteProfileFunc != nil {
		return m.DeleteProfileFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method DeleteProfile not implemented")
}

func (m *MockProfileServiceServer) RebuildProfileImage(ctx context.Context, req *githubv1beta.RebuildProfileImageRequest) (*githubv1beta.RebuildProfileImageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.RebuildProfileImageFunc != nil {
		return m.RebuildProfileImageFunc(ctx, req)
	}
	return &githubv1beta.RebuildProfileImageResponse{}, nil
}

// EnableStatefulProfiles configures in-memory stateful CRUD handlers on MockProfileServiceServer
// using deep-copied protobuf messages and validating RunnerProfileSpec fields including
// AccessLevel and CustomPermissions. Nil-hook defaults remain unchanged unless this is called.
func (m *MockProfileServiceServer) EnableStatefulProfiles() {
	m.mu.Lock()
	defer m.mu.Unlock()

	profiles := make(map[string]*githubv1beta.RunnerProfileWithStatus)
	var seq int64

	m.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		if err := validateRunnerProfileSpec(req.GetSpec()); err != nil {
			return nil, err
		}
		seq++
		id := fmt.Sprintf("prof_%d", seq)
		now := timestamppb.Now()
		stored := &githubv1beta.RunnerProfileWithStatus{
			ProfileId: id,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec:      proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec),
		}
		profiles[id] = stored
		return &githubv1beta.CreateProfileResponse{
			Profile: proto.Clone(stored).(*githubv1beta.RunnerProfileWithStatus),
		}, nil
	}

	m.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		existing, ok := profiles[req.GetProfileId()]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "runner profile %q not found", req.GetProfileId())
		}
		if req.GetUpdateVersion() != existing.GetVersion() {
			return nil, status.Errorf(codes.FailedPrecondition, "version mismatch: got %d, want %d", req.GetUpdateVersion(), existing.GetVersion())
		}
		if err := validateRunnerProfileSpec(req.GetSpec()); err != nil {
			return nil, err
		}
		updated := &githubv1beta.RunnerProfileWithStatus{
			ProfileId: existing.GetProfileId(),
			Version:   existing.GetVersion() + 1,
			CreatedAt: proto.Clone(existing.GetCreatedAt()).(*timestamppb.Timestamp),
			UpdatedAt: timestamppb.Now(),
			Spec:      proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec),
			Status:    proto.Clone(existing.GetStatus()).(*githubv1beta.RunnerProfileStatus),
		}
		profiles[req.GetProfileId()] = updated
		return &githubv1beta.UpdateProfileResponse{
			Profile: proto.Clone(updated).(*githubv1beta.RunnerProfileWithStatus),
		}, nil
	}

	m.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		ids := make([]string, 0, len(profiles))
		for id := range profiles {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := make([]*githubv1beta.RunnerProfileWithStatus, 0, len(ids))
		for _, id := range ids {
			out = append(out, proto.Clone(profiles[id]).(*githubv1beta.RunnerProfileWithStatus))
		}
		return &githubv1beta.ListProfilesResponse{Profiles: out}, nil
	}

	m.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
		if _, ok := profiles[req.GetProfileId()]; !ok {
			return nil, status.Errorf(codes.NotFound, "runner profile %q not found", req.GetProfileId())
		}
		delete(profiles, req.GetProfileId())
		return &emptypb.Empty{}, nil
	}
}

func validateRunnerProfileSpec(spec *githubv1beta.RunnerProfileSpec) error {
	if spec == nil || strings.TrimSpace(spec.GetTag()) == "" {
		return status.Error(codes.InvalidArgument, "runner profile spec tag must be non-empty")
	}
	if _, ok := githubv1beta.AccessLevel_name[int32(spec.GetAccessLevel())]; !ok {
		return status.Errorf(codes.InvalidArgument, "unsupported access_level enum %d", spec.GetAccessLevel())
	}
	for i, perm := range spec.GetCustomPermissions() {
		if perm == nil || strings.TrimSpace(perm.GetResourceType()) == "" {
			return status.Errorf(codes.InvalidArgument, "custom_permissions[%d].resource_type must be non-empty", i)
		}
	}
	return nil
}

// MockImageServiceServer implements ImageServiceServer with hook functions.
type MockImageServiceServer struct {
	githubv1betagrpc.UnimplementedImageServiceServer
	mu sync.Mutex

	ListBaseImagesFunc func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error)
}

func (m *MockImageServiceServer) ListBaseImages(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListBaseImagesFunc != nil {
		return m.ListBaseImagesFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ListBaseImages not implemented")
}

// MockTenantServiceServer implements TenantServiceServer with hook functions.
type MockTenantServiceServer struct {
	iamv1betagrpc.UnimplementedTenantServiceServer
	mu sync.Mutex

	EnsureTenantGroupFunc            func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error)
	DescribeTenantGroupFunc          func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error)
	ListTenantGroupsFunc             func(ctx context.Context, req *iamv1beta.ListTenantGroupsRequest) (*iamv1beta.ListTenantGroupsResponse, error)
	DeleteTenantGroupFunc            func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error)
	IssueTenantClientCertificateFunc func(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error)
}

func (m *MockTenantServiceServer) EnsureTenantGroup(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.EnsureTenantGroupFunc != nil {
		return m.EnsureTenantGroupFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method EnsureTenantGroup not implemented")
}

func (m *MockTenantServiceServer) DescribeTenantGroup(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DescribeTenantGroupFunc != nil {
		return m.DescribeTenantGroupFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method DescribeTenantGroup not implemented")
}

func (m *MockTenantServiceServer) ListTenantGroups(ctx context.Context, req *iamv1beta.ListTenantGroupsRequest) (*iamv1beta.ListTenantGroupsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListTenantGroupsFunc != nil {
		return m.ListTenantGroupsFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ListTenantGroups not implemented")
}

func (m *MockTenantServiceServer) DeleteTenantGroup(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteTenantGroupFunc != nil {
		return m.DeleteTenantGroupFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method DeleteTenantGroup not implemented")
}

func (m *MockTenantServiceServer) IssueTenantClientCertificate(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.IssueTenantClientCertificateFunc != nil {
		return m.IssueTenantClientCertificateFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method IssueTenantClientCertificate not implemented")
}

// MockTenantMembershipServiceServer implements TenantMembershipServiceServer with hook functions.
type MockTenantMembershipServiceServer struct {
	iamv1betagrpc.UnimplementedTenantMembershipServiceServer
	mu sync.Mutex

	ListTenantMembersFunc func(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error)
}

func (m *MockTenantMembershipServiceServer) ListTenantMembers(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListTenantMembersFunc != nil {
		return m.ListTenantMembersFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ListTenantMembers not implemented")
}

// MockContainerRegistryServiceServer implements ContainerRegistryServiceServer with hook functions.
type MockContainerRegistryServiceServer struct {
	registryv1betagrpc.UnimplementedContainerRegistryServiceServer
	mu sync.Mutex

	ShareImageFunc             func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error)
	UnshareImageFunc           func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error)
	ListSharedImagesFunc       func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error)
	GetDefaultPolicyFunc       func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error)
	UpdateDefaultPolicyFunc    func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error)
	GetRepositoryPolicyFunc    func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error)
	UpdateRepositoryPolicyFunc func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error)
	GetImageFunc               func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error)
	ListImagesFunc             func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error)
}

func (m *MockContainerRegistryServiceServer) ShareImage(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ShareImageFunc != nil {
		return m.ShareImageFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ShareImage not implemented")
}

func (m *MockContainerRegistryServiceServer) UnshareImage(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UnshareImageFunc != nil {
		return m.UnshareImageFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method UnshareImage not implemented")
}

func (m *MockContainerRegistryServiceServer) ListSharedImages(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListSharedImagesFunc != nil {
		return m.ListSharedImagesFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ListSharedImages not implemented")
}

func (m *MockContainerRegistryServiceServer) GetDefaultPolicy(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetDefaultPolicyFunc != nil {
		return m.GetDefaultPolicyFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method GetDefaultPolicy not implemented")
}

func (m *MockContainerRegistryServiceServer) UpdateDefaultPolicy(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateDefaultPolicyFunc != nil {
		return m.UpdateDefaultPolicyFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method UpdateDefaultPolicy not implemented")
}

func (m *MockContainerRegistryServiceServer) GetRepositoryPolicy(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetRepositoryPolicyFunc != nil {
		return m.GetRepositoryPolicyFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method GetRepositoryPolicy not implemented")
}

func (m *MockContainerRegistryServiceServer) UpdateRepositoryPolicy(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateRepositoryPolicyFunc != nil {
		return m.UpdateRepositoryPolicyFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method UpdateRepositoryPolicy not implemented")
}

func (m *MockContainerRegistryServiceServer) GetImage(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetImageFunc != nil {
		return m.GetImageFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method GetImage not implemented")
}

func (m *MockContainerRegistryServiceServer) ListImages(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListImagesFunc != nil {
		return m.ListImagesFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ListImages not implemented")
}

// EnableStatefulImages configures GetImageFunc and ListImagesFunc on this mock server
// with an in-memory image store supporting proto-faithful DeletionFilter, ExpiresAt,
// CreatedAt, MatchRepository, continuation cursor validation, and deterministic pagination.
func (m *MockContainerRegistryServiceServer) EnableStatefulImages(seed []*registryv1beta.Image, pageSize int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if pageSize <= 0 {
		pageSize = 50
	}

	stored := make([]*registryv1beta.Image, 0, len(seed))
	for _, img := range seed {
		if img == nil {
			continue
		}
		stored = append(stored, proto.Clone(img).(*registryv1beta.Image))
	}

	type imageCursorState struct {
		filtered       []*registryv1beta.Image
		offset         int
		deletionFilter registryv1beta.ListImagesRequest_DeletionFilter
	}

	var stateMu sync.Mutex
	cursors := make(map[string]imageCursorState)
	var cursorSeq int

	matchesStringMatcher := func(matcher *stdlib.StringMatcher, value string) bool {
		if matcher == nil || len(matcher.GetValues()) == 0 {
			return true
		}
		switch matcher.GetOp() {
		case stdlib.StringMatcher_IS_ANY_OF:
			for _, candidate := range matcher.GetValues() {
				if value == candidate {
					return true
				}
			}
			return false
		case stdlib.StringMatcher_IS_NOT:
			for _, candidate := range matcher.GetValues() {
				if value == candidate {
					return false
				}
			}
			return true
		default:
			return true
		}
	}

	matchesTimestampRange := func(tr *stdlib.TimestampRange, ts *timestamppb.Timestamp) bool {
		if tr == nil || (tr.GetAfter() == nil && tr.GetBefore() == nil) {
			return true
		}
		if ts == nil || !ts.IsValid() {
			return false
		}
		t := ts.AsTime()
		if after := tr.GetAfter(); after != nil {
			if !after.IsValid() || !t.After(after.AsTime()) {
				return false
			}
		}
		if before := tr.GetBefore(); before != nil {
			if !before.IsValid() || !t.Before(before.AsTime()) {
				return false
			}
		}
		return true
	}

	m.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		stateMu.Lock()
		defer stateMu.Unlock()

		repo := req.GetRepository()
		ref := req.GetReference()
		for _, img := range stored {
			if img.GetRepository() != repo {
				continue
			}
			if img.GetDigest() == ref {
				return &registryv1beta.GetImageResponse{
					Image: proto.Clone(img).(*registryv1beta.Image),
				}, nil
			}
			for _, tag := range img.GetTags() {
				if tag == ref {
					return &registryv1beta.GetImageResponse{
						Image: proto.Clone(img).(*registryv1beta.Image),
					}, nil
				}
			}
		}
		return nil, status.Errorf(codes.NotFound, "image %s:%s not found", repo, ref)
	}

	m.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		stateMu.Lock()
		defer stateMu.Unlock()

		var filtered []*registryv1beta.Image
		var offset int
		var effectiveFilter registryv1beta.ListImagesRequest_DeletionFilter

		cursorToken := string(req.GetPaginationCursor())
		if cursorToken != "" {
			cs, ok := cursors[cursorToken]
			if !ok {
				return nil, status.Errorf(codes.InvalidArgument, "invalid pagination cursor %q", cursorToken)
			}
			if req.GetDeletionFilter() != registryv1beta.ListImagesRequest_DELETION_FILTER_UNSPECIFIED &&
				req.GetDeletionFilter() != cs.deletionFilter {
				return nil, status.Errorf(codes.InvalidArgument, "continuation deletion_filter %v does not match initial %v", req.GetDeletionFilter(), cs.deletionFilter)
			}
			filtered = cs.filtered
			offset = cs.offset
			effectiveFilter = cs.deletionFilter
		} else {
			effectiveFilter = req.GetDeletionFilter()
			for _, img := range stored {
				if !matchesStringMatcher(req.GetMatchRepository(), img.GetRepository()) {
					continue
				}
				if effectiveFilter == registryv1beta.ListImagesRequest_EXCLUDE_DELETED && img.GetDeletedAt() != nil {
					continue
				}
				if !matchesTimestampRange(req.GetExpiresAt(), img.GetExpiresAt()) {
					continue
				}
				if !matchesTimestampRange(req.GetCreatedAt(), img.GetCreatedAt()) {
					continue
				}
				filtered = append(filtered, img)
			}
		}

		limit := pageSize
		if maxEntries := int(req.GetMaxEntries()); maxEntries > 0 && maxEntries < limit {
			limit = maxEntries
		}

		if offset >= len(filtered) {
			return &registryv1beta.ListImagesResponse{}, nil
		}

		end := offset + limit
		if end > len(filtered) {
			end = len(filtered)
		}

		page := make([]*registryv1beta.Image, 0, end-offset)
		for _, img := range filtered[offset:end] {
			page = append(page, proto.Clone(img).(*registryv1beta.Image))
		}

		var nextCursor []byte
		if end < len(filtered) {
			cursorSeq++
			tok := fmt.Sprintf("img-cursor-%d", cursorSeq)
			cursors[tok] = imageCursorState{
				filtered:       filtered,
				offset:         end,
				deletionFilter: effectiveFilter,
			}
			nextCursor = []byte(tok)
		}

		return &registryv1beta.ListImagesResponse{
			Images:           page,
			PaginationCursor: nextCursor,
		}, nil
	}
}

// MockTokenServiceServer implements TokenServiceServer with hook functions.
type MockTokenServiceServer struct {
	iamv1betagrpc.UnimplementedTokenServiceServer
	mu sync.Mutex

	IssueTenantTokenFunc func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error)
	IssueIdTokenFunc     func(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error)
}

func (m *MockTokenServiceServer) IssueTenantToken(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.IssueTenantTokenFunc != nil {
		return m.IssueTenantTokenFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method IssueTenantToken not implemented")
}

func (m *MockTokenServiceServer) IssueIdToken(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.IssueIdTokenFunc != nil {
		return m.IssueIdTokenFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method IssueIdToken not implemented")
}

// MockBazelCacheServiceServer implements BazelCacheServiceServer with hook functions.
type MockBazelCacheServiceServer struct {
	bazelv1betagrpc.UnimplementedBazelCacheServiceServer
	mu sync.Mutex

	EnsureBazelCacheFunc func(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error)
}

func (m *MockBazelCacheServiceServer) EnsureBazelCache(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.EnsureBazelCacheFunc != nil {
		return m.EnsureBazelCacheFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method EnsureBazelCache not implemented")
}

// MockGradleCacheServiceServer implements GradleCacheServiceServer with hook functions.
type MockGradleCacheServiceServer struct {
	gradlev1betagrpc.UnimplementedGradleCacheServiceServer
	mu sync.Mutex

	EnsureGradleCacheFunc func(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error)
}

func (m *MockGradleCacheServiceServer) EnsureGradleCache(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.EnsureGradleCacheFunc != nil {
		return m.EnsureGradleCacheFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method EnsureGradleCache not implemented")
}

// MockHttpCacheServiceServer implements HttpCacheServiceServer with hook functions.
type MockHttpCacheServiceServer struct {
	httpcachev1betagrpc.UnimplementedHttpCacheServiceServer
	mu sync.Mutex

	EnsureHttpCacheFunc func(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error)
}

func (m *MockHttpCacheServiceServer) EnsureHttpCache(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.EnsureHttpCacheFunc != nil {
		return m.EnsureHttpCacheFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method EnsureHttpCache not implemented")
}

// MockStorageServiceServer implements computev1betagrpc.StorageServiceServer with hook functions.
type MockStorageServiceServer struct {
	computev1betagrpc.UnimplementedStorageServiceServer
	mu sync.Mutex

	GetVolumeTagSummaryFunc      func(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error)
	DescribePersistentVolumeFunc func(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error)
}

func (m *MockStorageServiceServer) GetVolumeTagSummary(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetVolumeTagSummaryFunc != nil {
		return m.GetVolumeTagSummaryFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method GetVolumeTagSummary not implemented")
}

func (m *MockStorageServiceServer) DescribePersistentVolume(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DescribePersistentVolumeFunc != nil {
		return m.DescribePersistentVolumeFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method DescribePersistentVolume not implemented")
}

// MockBuilderServiceServer implements builderv1betagrpc.BuilderServiceServer with hook functions.
type MockBuilderServiceServer struct {
	builderv1betagrpc.UnimplementedBuilderServiceServer
	mu sync.Mutex

	GetBuilderConfigurationFunc func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error)
	GetBuilderUsageFunc         func(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error)
}

func (m *MockBuilderServiceServer) GetBuilderConfiguration(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetBuilderConfigurationFunc != nil {
		return m.GetBuilderConfigurationFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method GetBuilderConfiguration not implemented")
}

func (m *MockBuilderServiceServer) GetBuilderUsage(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetBuilderUsageFunc != nil {
		return m.GetBuilderUsageFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method GetBuilderUsage not implemented")
}

// MockComputeServiceServer implements computev1betagrpc.ComputeServiceServer with hook functions.
type MockComputeServiceServer struct {
	computev1betagrpc.UnimplementedComputeServiceServer
	mu sync.Mutex

	GetNetworkConfigurationFunc func(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error)
}

func (m *MockComputeServiceServer) GetNetworkConfiguration(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetNetworkConfigurationFunc != nil {
		return m.GetNetworkConfigurationFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method GetNetworkConfiguration not implemented")
}

// MockVaultServiceServer implements vaultv1betagrpc.VaultServiceServer with hook functions.
type MockVaultServiceServer struct {
	vaultv1betagrpc.UnimplementedVaultServiceServer
	mu sync.Mutex

	ListObjectsFunc    func(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error)
	DescribeObjectFunc func(ctx context.Context, req *vaultv1beta.DescribeObjectRequest) (*vaultv1beta.DescribeObjectResponse, error)
}

func (m *MockVaultServiceServer) ListObjects(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListObjectsFunc != nil {
		return m.ListObjectsFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method ListObjects not implemented")
}

func (m *MockVaultServiceServer) DescribeObject(ctx context.Context, req *vaultv1beta.DescribeObjectRequest) (*vaultv1beta.DescribeObjectResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DescribeObjectFunc != nil {
		return m.DescribeObjectFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "method DescribeObject not implemented")
}
