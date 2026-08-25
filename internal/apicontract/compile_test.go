package apicontract_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bsuv/terraform-provider-namespace/internal/apicontract"

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
	imagesv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/images"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	stdtime "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib/time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"namespacelabs.dev/integrations/api"
	"namespacelabs.dev/integrations/auth"
	"namespacelabs.dev/integrations/nsc/apienv"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

// Compile gate asserting all retained gRPC client interfaces, methods, request/response structs,
// and enum constants compile cleanly against the pinned module versions.
func TestAPIContractCompilation(t *testing.T) {
	_ = context.Background()

	// 1. Assert gRPC Client Interfaces and FullMethodName constants
	var (
		_ = (githubv1betagrpc.ProfileServiceClient)(nil)
		_ = (githubv1betagrpc.ImageServiceClient)(nil)
		_ = (iamv1betagrpc.TenantServiceClient)(nil)
		_ = (iamv1betagrpc.TenantMembershipServiceClient)(nil)
		_ = (iamv1betagrpc.TokenServiceClient)(nil)
		_ = (registryv1betagrpc.ContainerRegistryServiceClient)(nil)
		_ = (vaultv1betagrpc.VaultServiceClient)(nil)
		_ = (bazelv1betagrpc.BazelCacheServiceClient)(nil)
		_ = (gradlev1betagrpc.GradleCacheServiceClient)(nil)
		_ = (httpcachev1betagrpc.HttpCacheServiceClient)(nil)
		_ = (computev1betagrpc.StorageServiceClient)(nil)
		_ = (computev1betagrpc.ComputeServiceClient)(nil)
		_ = (builderv1betagrpc.BuilderServiceClient)(nil)
	)

	// Verify FullMethodName string constants for all retained RPCs
	_ = githubv1betagrpc.ProfileService_CreateProfile_FullMethodName
	_ = githubv1betagrpc.ProfileService_UpdateProfile_FullMethodName
	_ = githubv1betagrpc.ProfileService_ListProfiles_FullMethodName
	_ = githubv1betagrpc.ProfileService_DeleteProfile_FullMethodName
	_ = githubv1betagrpc.ImageService_ListBaseImages_FullMethodName

	_ = iamv1betagrpc.TenantService_EnsureTenantGroup_FullMethodName
	_ = iamv1betagrpc.TenantService_DescribeTenantGroup_FullMethodName
	_ = iamv1betagrpc.TenantService_ListTenantGroups_FullMethodName
	_ = iamv1betagrpc.TenantService_DeleteTenantGroup_FullMethodName
	_ = iamv1betagrpc.TenantService_IssueTenantClientCertificate_FullMethodName
	_ = iamv1betagrpc.TenantMembershipService_ListTenantMembers_FullMethodName
	_ = iamv1betagrpc.TokenService_IssueTenantToken_FullMethodName
	_ = iamv1betagrpc.TokenService_IssueIdToken_FullMethodName

	_ = registryv1betagrpc.ContainerRegistryService_ShareImage_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_UnshareImage_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_ListSharedImages_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_GetDefaultPolicy_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_UpdateDefaultPolicy_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_GetRepositoryPolicy_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_UpdateRepositoryPolicy_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_GetImage_FullMethodName
	_ = registryv1betagrpc.ContainerRegistryService_ListImages_FullMethodName

	_ = vaultv1betagrpc.VaultService_ListObjects_FullMethodName

	_ = bazelv1betagrpc.BazelCacheService_EnsureBazelCache_FullMethodName
	_ = gradlev1betagrpc.GradleCacheService_EnsureGradleCache_FullMethodName
	_ = httpcachev1betagrpc.HttpCacheService_EnsureHttpCache_FullMethodName

	_ = computev1betagrpc.StorageService_GetVolumeTagSummary_FullMethodName
	_ = computev1betagrpc.StorageService_DescribePersistentVolume_FullMethodName
	_ = computev1betagrpc.ComputeService_GetNetworkConfiguration_FullMethodName
	_ = builderv1betagrpc.BuilderService_GetBuilderConfiguration_FullMethodName
	_ = builderv1betagrpc.BuilderService_GetBuilderUsage_FullMethodName

	// 2. Assert GitHub Runner Profile Structs & Enums
	runnerSpec := &githubv1beta.RunnerProfileSpec{
		Tag:         "ubuntu-24-04-large",
		Description: "High-memory builder profile",
		Os:          "ubuntu-24.04",
		InstanceShape: &computev1beta.InstanceShape{
			VirtualCpu:      8,
			MemoryMegabytes: 32768,
			MachineArch:     "amd64",
			Os:              "linux",
			Selectors: []*stdlib.Label{
				{Name: "purpose", Value: "build"},
			},
		},
		CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{
			{
				VolumeTag:                 "build-cache",
				VolumeSizeGb:              100,
				VolumeReservedSizeGb:      10,
				BundledContainerCache:     true,
				BundledBuildkitCache:      true,
				BundledGitMirror:          true,
				BundledRunnerToolsCache:   true,
				BundledRunnerActionsCache: true,
				AllowCommitFromBranch:     []string{"main", "release/*"},
			},
		},
		BuilderMode:          githubv1beta.BuilderMode_USE_REMOTE_BUILDER,
		ExperimentalFeatures: []string{"nested-virtualization"},
		Emoji:                "🚀",
		CustomRunnerImageSpec: &githubv1beta.CustomRunnerImageSpec{
			UbuntuSpec: &githubv1beta.UbuntuBaseImageSpec{
				AptPackage: []string{"clang", "lld"},
			},
			DockerSpec: &githubv1beta.DockerBaseImageSpec{
				DockerfileContent: "RUN echo hello",
			},
		},
		NetworkPolicy: &githubv1beta.NetworkPolicy{
			Egress:                githubv1beta.NetworkPolicy_EGRESS_POLICY_DOMAIN_ALLOW_LIST,
			EgressDomainAllowList: []string{"github.com", "*.npmjs.org"},
			Advisory:              false,
			EgressPolicyTag:       "",
		},
		AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_PERMISSIVE,
		SwapSizeMb:  2048,
	}

	profileWithStatus := &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof_123456",
		Spec:      runnerSpec,
		Version:   42,
		CreatedAt: timestamppb.Now(),
		UpdatedAt: timestamppb.Now(),
		Status: &githubv1beta.RunnerProfileStatus{
			CustomRunnerImage: []*githubv1beta.CustomRunnerImage{
				{
					ImageStatus: &githubv1beta.CustomRunnerImageStatus{
						ImageRef:       "nscr.io/workspace/custom:v1",
						Status:         githubv1beta.CustomRunnerImageStatus_READY,
						LastBuildError: "",
						StartedBuildAt: timestamppb.Now(),
						BuiltAt:        timestamppb.Now(),
						LastErrorAt:    nil,
						BuildRef:       "build_789",
					},
				},
			},
		},
	}

	_ = &githubv1beta.CreateProfileRequest{Spec: runnerSpec}
	_ = &githubv1beta.CreateProfileResponse{Profile: profileWithStatus}
	_ = &githubv1beta.UpdateProfileRequest{ProfileId: "prof_123", Spec: runnerSpec, UpdateVersion: 42}
	_ = &githubv1beta.UpdateProfileResponse{Profile: profileWithStatus}
	_ = &githubv1beta.ListProfilesResponse{Profiles: []*githubv1beta.RunnerProfileWithStatus{profileWithStatus}}
	_ = &githubv1beta.DeleteProfileRequest{ProfileId: "prof_123"}
	_ = &emptypb.Empty{}

	// Enums
	_ = githubv1beta.BuilderMode_BUILDER_MODE_UNSPECIFIED
	_ = githubv1beta.BuilderMode_USE_LOCAL_CACHE
	_ = githubv1beta.BuilderMode_NO_CACHING
	_ = githubv1beta.AccessLevel_ACCESS_LEVEL_UNSPECIFIED
	_ = githubv1beta.AccessLevel_ACCESS_LEVEL_LIMITED
	_ = githubv1beta.AccessLevel_ACCESS_LEVEL_RESTRICTED
	_ = githubv1beta.NetworkPolicy_EGRESS_POLICY_UNKNOWN
	_ = githubv1beta.NetworkPolicy_EGRESS_POLICY_NONE
	_ = githubv1beta.NetworkPolicy_EGRESS_POLICY_TAG

	// 3. Assert GitHub Base Images Structs & Enums
	_ = &githubv1beta.ListBaseImagesRequest{
		Labels: &stdlib.StringMatcher{
			Values: []string{"ubuntu-24.04"},
			Op:     stdlib.StringMatcher_IS_ANY_OF,
		},
		ReleaseStages: []imagesv1beta.ReleaseStage{
			imagesv1beta.ReleaseStage_PRODUCTION,
			imagesv1beta.ReleaseStage_CANARY,
		},
	}
	_ = &githubv1beta.ListBaseImagesResponse{
		Images: []*githubv1beta.RunnerBaseImage{
			{
				Label:    "ubuntu-24.04",
				ImageRef: "nscr.io/official/ubuntu-24.04:latest",
				Spec: &imagesv1beta.BaseImage{
					Id:          "img_ubuntu_2404",
					Os:          "linux",
					Description: "Ubuntu 24.04 Noble Numbat",
				},
				Title: "Ubuntu 24.04 Noble Numbat",
			},
		},
	}
	_ = imagesv1beta.ReleaseStage_STAGE_UNKNOWN

	// 4. Assert IAM Tenant & Membership Structs
	tenantPolicy := &iamv1beta.TenantGroupPolicy{
		Policy: "namespace.cloud.compute.v1beta.UsagePolicy",
		Value:  `{"max_concurrent_instances": 10}`,
	}
	_ = &iamv1beta.EnsureTenantGroupRequest{
		Name:     "engineering",
		Policies: []*iamv1beta.TenantGroupPolicy{tenantPolicy},
	}
	_ = &iamv1beta.TenantGroupResponse{
		Name: "engineering",
	}
	_ = &iamv1beta.TenantGroupReference{
		Name: "engineering",
	}
	_ = &iamv1beta.DescribeTenantGroupResponse{
		Name:              "engineering",
		Policies:          []*iamv1beta.TenantGroupPolicy{tenantPolicy},
		EnrolledTenantIds: []string{"tenant_abc", "tenant_xyz"},
		Versions:          []*iamv1beta.TenantGroupPolicy{tenantPolicy},
	}
	_ = &iamv1beta.ListTenantGroupsRequest{}
	_ = &iamv1beta.ListTenantGroupsResponse{
		TenantGroups: []*iamv1beta.ListTenantGroupsResponse_TenantGroup{
			{
				Name:     "engineering",
				Policies: []*iamv1beta.TenantGroupPolicy{tenantPolicy},
			},
		},
	}
	_ = &iamv1beta.ListTenantMembersRequest{}
	_ = &iamv1beta.ListTenantMembersResponse{
		Members: []*iamv1beta.TenantMember{
			{
				UserId:    "usr_123",
				FirstName: "Jane",
				LastName:  "Doe",
				Email:     "jane@example.com",
				Role:      iamv1beta.TenantMemberRole_TENANT_MEMBER_ROLE_ADMIN,
			},
		},
	}
	_ = &iamv1beta.IssueTenantTokenRequest{
		TenantId:     "tenant_abc",
		ActorId:      "actor_1",
		DurationSecs: 3600,
		Policies: []*iamv1beta.TenantTokenPolicy{
			{
				PolicyType: "namespace.cloud.iam.v1beta.AdminPolicy",
				Value:      "{}",
			},
		},
	}
	_ = &iamv1beta.IssueTenantTokenResponse{
		BearerToken: "nsct_test_token",
	}
	_ = &iamv1beta.IssueIdTokenRequest{
		Audience: "https://auth.example.com",
	}
	_ = &iamv1beta.IssueIdTokenResponse{
		IdToken: "eyJhbGciOi...",
	}
	_ = &iamv1beta.IssueTenantClientCertificateRequest{
		TenantId:     "tenant_abc",
		ActorId:      "actor_1",
		DurationSecs: 3600,
		PublicKeyPem: "PUBLIC KEY",
	}
	_ = &iamv1beta.IssueTenantClientCertificateResponse{
		ClientCertificatePem: "CERT PEM",
		PrivateKeyPem:        "KEY PEM",
	}

	// 5. Assert Container Registry Structs & Enums
	_ = &registryv1beta.ShareImageRequest{
		Repository: "my-app",
		Digest:     "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ExpiresAt:  timestamppb.New(time.Now().Add(24 * time.Hour)),
		Visibility: registryv1beta.Visibility_PUBLIC,
		Suffix:     "latest",
	}
	_ = &registryv1beta.SharedImage{
		Id:               "share_12345",
		SharedRepository: "public/my-app",
		SharedDigest:     "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ExpiresAt:        timestamppb.New(time.Now().Add(24 * time.Hour)),
		ImageRef:         "public.nscr.io/share_12345/latest",
		Visibility:       registryv1beta.Visibility_PUBLIC,
		Suffix:           "latest",
	}
	_ = &registryv1beta.UnshareImageRequest{Id: "share_12345"}
	_ = &registryv1beta.ListSharedImagesRequest{}
	_ = &registryv1beta.ListSharedImagesResponse{
		SharedImages: []*registryv1beta.SharedImage{},
	}
	_ = &registryv1beta.GetDefaultPolicyResponse{
		Revision:              10,
		DefaultExpiration:     durationpb.New(24 * time.Hour),
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
	}
	_ = &registryv1beta.UpdateDefaultPolicyRequest{
		Revision:              10,
		DefaultExpiration:     durationpb.New(48 * time.Hour),
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
	}
	_ = &registryv1beta.GetRepositoryPolicyRequest{Repository: "my-app"}
	_ = &registryv1beta.GetRepositoryPolicyResponse{
		Revision:              5,
		Expiration:            durationpb.New(12 * time.Hour),
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
	}
	_ = &registryv1beta.UpdateRepositoryPolicyRequest{
		Repository:            "my-app",
		Revision:              5,
		Expiration:            durationpb.New(24 * time.Hour),
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
	}
	_ = &registryv1beta.GetImageRequest{Repository: "my-app", Reference: "v1.0.0"}
	_ = &registryv1beta.GetImageResponse{
		Image: &registryv1beta.Image{
			Repository: "my-app",
			Digest:     "sha256:abcdef",
			CreatedAt:  timestamppb.Now(),
		},
	}
	_ = &registryv1beta.ListImagesRequest{
		PaginationCursor: []byte("cursor_xyz"),
		MaxEntries:       50,
	}
	_ = &registryv1beta.ListImagesResponse{
		Images:           []*registryv1beta.Image{},
		PaginationCursor: []byte("cursor_next"),
	}

	_ = registryv1beta.Visibility_UNKNOWN_VISIBILITY
	_ = registryv1beta.Visibility_PARTNER
	_ = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN
	_ = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE

	// 6. Assert Builder, Vault, Integrations, Storage, and Compute Structs
	_ = &builderv1beta.GetBuilderConfigurationRequest{
		Platform:            "linux/amd64",
		SkipBuilderPreSpawn: true,
	}
	_ = &builderv1beta.GetBuilderConfigurationResponse{
		BuildkitEndpoint:     "builder.namespace.so:443",
		FullBuildkitEndpoint: "tcp://builder.namespace.so:443",
		ServerCaPem:          "test-server-ca-pem",
		CacheSizeMb:          51200,
		Shape: &computev1beta.InstanceShape{
			VirtualCpu:      8,
			MemoryMegabytes: 16384,
			MachineArch:     "amd64",
			Os:              "linux",
		},
	}
	_ = &builderv1beta.GetBuilderUsageRequest{
		PeriodStart: &stdtime.Date{Year: 2026, Month: 8, Day: 1},
		PeriodEnd:   &stdtime.Date{Year: 2026, Month: 8, Day: 21},
	}
	_ = &builderv1beta.GetBuilderUsageResponse{
		Total: &builderv1beta.GetBuilderUsageResponse_Usage{
			BuildCount:       100,
			BuildWallMinutes: 500,
			BuildUnitMinutes: 2000,
		},
		PendingTotal: &builderv1beta.GetBuilderUsageResponse_Usage{
			BuildCount: 5,
		},
		PerDay: []*builderv1beta.GetBuilderUsageResponse_PerDay{
			{
				Date: &stdtime.Date{Year: 2026, Month: 8, Day: 20},
				Usage: &builderv1beta.GetBuilderUsageResponse_Usage{
					BuildCount: 20,
				},
			},
		},
	}

	_ = &vaultv1beta.ListObjectsRequest{}
	_ = &vaultv1beta.ListObjectsResponse{
		Objects: []*vaultv1beta.VaultObjectMetadata{
			{
				ObjectId:  "secret_123",
				CreatedAt: timestamppb.Now(),
			},
		},
	}

	_ = &bazelv1beta.EnsureBazelCacheRequest{Key: "ci"}
	_ = &bazelv1beta.EnsureBazelCacheResponse{
		CacheEndpoint:           "cache.namespace.so:443",
		HttpsMtlsCacheEndpoint:  "https://cache.namespace.so:443",
		HttpsCacheEndpoint:      "https://cache.namespace.so:443",
		ServerCaPem:             "CA PEM",
		ClientCertPem:           "CERT PEM",
		ClientKeyPem:            "KEY PEM",
		ExpiresAt:               timestamppb.Now(),
		CredentialHelperDomains: []string{"namespace.so"},
		BuildEventEndpoint:      "bes.namespace.so:443",
		RemoteAssetEndpoint:     "asset.namespace.so:443",
		UseWorkloadMtls:         true,
	}
	_ = &gradlev1beta.EnsureGradleCacheRequest{Name: "cache1"}
	_ = &gradlev1beta.EnsureGradleCacheResponse{
		CacheEndpointUrl: "https://gradle.namespace.so:443",
		Username:         "gradle_user",
		Password:         "gradle_pass",
		ExpiresAt:        timestamppb.Now(),
		Site:             "eu",
	}
	_ = &httpcachev1beta.EnsureHttpCacheRequest{Name: "cache1"}
	_ = &httpcachev1beta.EnsureHttpCacheResponse{
		CacheEndpointUrl: "https://http.namespace.so:443",
		Username:         "http_user",
		Password:         "http_pass",
		ExpiresAt:        timestamppb.Now(),
		Site:             "eu",
	}

	_ = &computev1beta.GetVolumeTagSummaryRequest{Tag: "my-tag"}
	_ = &computev1beta.GetVolumeTagSummaryResponse{
		Summary: &computev1beta.TagSummary{
			Tag:       "my-tag",
			SizeMb:    10240,
			CreatedAt: timestamppb.Now(),
			Generations: []*computev1beta.CacheVolume{
				{
					Id:  "gen_1",
					Tag: "my-tag",
					Metadata: &computev1beta.VolumeMetadata{
						CreatedAt:        timestamppb.Now(),
						DestroyedAt:      nil,
						SizeMb:           10240,
						State:            computev1beta.VolumeMetadata_STATE_IN_USE,
						GenerationReason: computev1beta.VolumeMetadata_RESERVATION_NOT_MET,
					},
					Attachment: &computev1beta.VolumeAttachment{
						AttachedTo:         "inst_123",
						AttachedAt:         timestamppb.Now(),
						DetachedAt:         nil,
						UsedMbWhenAttached: 500,
						UsedMbWhenDetached: 0,
					},
				},
			},
		},
	}
	_ = &computev1beta.DescribePersistentVolumeRequest{
		Id:   "vol_123",
		Tag:  "my-tag",
		Site: "eu",
	}
	_ = &computev1beta.DescribePersistentVolumeResponse{
		Volume: &computev1beta.PersistentVolume{
			Id:             "vol_123",
			Tag:            "my-tag",
			Site:           "eu",
			SizeMb:         20480,
			CreatedAt:      timestamppb.Now(),
			DestroyedAt:    nil,
			AttachedTo:     "inst_123",
			ReleasedAt:     nil,
			ReleasedReason: "",
		},
	}
	_ = &computev1beta.GetNetworkConfigurationRequest{}
	_ = &computev1beta.GetNetworkConfigurationResponse{
		Egress: &computev1beta.GetNetworkConfigurationResponse_Egress{
			EgressCidrs: []*computev1beta.GetNetworkConfigurationResponse_CIDR{
				{
					Cidr:    "198.51.100.0/24",
					Kind:    computev1beta.GetNetworkConfigurationResponse_CIDR_NAMESPACE_MANAGED,
					Segment: "default",
				},
			},
		},
	}

	// 7. Assert Integrations SDK Connection & Auth API
	_ = apienv.GlobalEndpoint()
	_ = apienv.IAMEndpoint()

	var _ api.TokenSource = nil
	_, _ = auth.LoadLocalToken("/dev/null")

	// Ensure grpcapi compile surface is accessible
	_ = grpcapi.RequireTransportSecurity
	var _ = grpc.WithUserAgent("test")

	t.Logf("Compile gate passed: all gRPC client interfaces, methods, request/response structs, and enums verified.")
}

func TestCompileCheckedConstants(t *testing.T) {
	// 1. Assert all single RPC constants match generated protobuf FullMethodName suffixes
	methodMappings := map[string]string{
		apicontract.RPCProfileServiceCreateProfile: githubv1betagrpc.ProfileService_CreateProfile_FullMethodName,
		apicontract.RPCProfileServiceUpdateProfile: githubv1betagrpc.ProfileService_UpdateProfile_FullMethodName,
		apicontract.RPCProfileServiceListProfiles:  githubv1betagrpc.ProfileService_ListProfiles_FullMethodName,
		apicontract.RPCProfileServiceDeleteProfile: githubv1betagrpc.ProfileService_DeleteProfile_FullMethodName,
		apicontract.RPCImageServiceListBaseImages:  githubv1betagrpc.ImageService_ListBaseImages_FullMethodName,

		apicontract.RPCTenantServiceEnsureTenantGroup:            iamv1betagrpc.TenantService_EnsureTenantGroup_FullMethodName,
		apicontract.RPCTenantServiceDescribeTenantGroup:          iamv1betagrpc.TenantService_DescribeTenantGroup_FullMethodName,
		apicontract.RPCTenantServiceListTenantGroups:             iamv1betagrpc.TenantService_ListTenantGroups_FullMethodName,
		apicontract.RPCTenantServiceDeleteTenantGroup:            iamv1betagrpc.TenantService_DeleteTenantGroup_FullMethodName,
		apicontract.RPCTenantServiceIssueTenantClientCertificate: iamv1betagrpc.TenantService_IssueTenantClientCertificate_FullMethodName,
		apicontract.RPCTenantMembershipServiceListTenantMembers:  iamv1betagrpc.TenantMembershipService_ListTenantMembers_FullMethodName,
		apicontract.RPCTokenServiceIssueTenantToken:              iamv1betagrpc.TokenService_IssueTenantToken_FullMethodName,
		apicontract.RPCTokenServiceIssueIdToken:                  iamv1betagrpc.TokenService_IssueIdToken_FullMethodName,

		apicontract.RPCContainerRegistryServiceShareImage:             registryv1betagrpc.ContainerRegistryService_ShareImage_FullMethodName,
		apicontract.RPCContainerRegistryServiceUnshareImage:           registryv1betagrpc.ContainerRegistryService_UnshareImage_FullMethodName,
		apicontract.RPCContainerRegistryServiceListSharedImages:       registryv1betagrpc.ContainerRegistryService_ListSharedImages_FullMethodName,
		apicontract.RPCContainerRegistryServiceGetDefaultPolicy:       registryv1betagrpc.ContainerRegistryService_GetDefaultPolicy_FullMethodName,
		apicontract.RPCContainerRegistryServiceUpdateDefaultPolicy:    registryv1betagrpc.ContainerRegistryService_UpdateDefaultPolicy_FullMethodName,
		apicontract.RPCContainerRegistryServiceGetRepositoryPolicy:    registryv1betagrpc.ContainerRegistryService_GetRepositoryPolicy_FullMethodName,
		apicontract.RPCContainerRegistryServiceUpdateRepositoryPolicy: registryv1betagrpc.ContainerRegistryService_UpdateRepositoryPolicy_FullMethodName,
		apicontract.RPCContainerRegistryServiceGetImage:               registryv1betagrpc.ContainerRegistryService_GetImage_FullMethodName,
		apicontract.RPCContainerRegistryServiceListImages:             registryv1betagrpc.ContainerRegistryService_ListImages_FullMethodName,

		apicontract.RPCVaultServiceListObjects: vaultv1betagrpc.VaultService_ListObjects_FullMethodName,

		apicontract.RPCBazelCacheServiceEnsureBazelCache:   bazelv1betagrpc.BazelCacheService_EnsureBazelCache_FullMethodName,
		apicontract.RPCGradleCacheServiceEnsureGradleCache: gradlev1betagrpc.GradleCacheService_EnsureGradleCache_FullMethodName,
		apicontract.RPCHttpCacheServiceEnsureHttpCache:     httpcachev1betagrpc.HttpCacheService_EnsureHttpCache_FullMethodName,

		apicontract.RPCStorageServiceGetVolumeTagSummary:      computev1betagrpc.StorageService_GetVolumeTagSummary_FullMethodName,
		apicontract.RPCStorageServiceDescribePersistentVolume: computev1betagrpc.StorageService_DescribePersistentVolume_FullMethodName,
		apicontract.RPCComputeServiceGetNetworkConfiguration:  computev1betagrpc.ComputeService_GetNetworkConfiguration_FullMethodName,
		apicontract.RPCBuilderServiceGetBuilderConfiguration:  builderv1betagrpc.BuilderService_GetBuilderConfiguration_FullMethodName,
		apicontract.RPCBuilderServiceGetBuilderUsage:          builderv1betagrpc.BuilderService_GetBuilderUsage_FullMethodName,
	}

	for rpcName, fullMethod := range methodMappings {
		parts := strings.Split(rpcName, ".")
		if len(parts) != 2 {
			t.Fatalf("RPC constant %q is not in Service.Method format", rpcName)
		}
		expectedSuffix := "." + parts[0] + "/" + parts[1]
		if !strings.HasSuffix(fullMethod, expectedSuffix) {
			t.Errorf("RPC constant %q does not match protobuf FullMethodName suffix %q (got %q)", rpcName, expectedSuffix, fullMethod)
		}
	}

	// 2. Assert Endpoint constants
	endpoints := []string{
		apicontract.EndpointGlobal,
		apicontract.EndpointIAM,
		apicontract.EndpointRegionalCompute,
		apicontract.EndpointCompute,
		apicontract.EndpointIAMGlobal,
	}
	for _, ep := range endpoints {
		if ep == "" {
			t.Errorf("Endpoint constant is empty")
		}
	}

	// 3. Assert Status constants
	statuses := []string{
		apicontract.StatusOk,
		apicontract.StatusOkReconciled,
		apicontract.StatusOkAlreadyExistsImport,
		apicontract.StatusOkCursorTraversal,
		apicontract.StatusFailedPrecondition,
		apicontract.StatusNotFoundRecreated,
		apicontract.StatusUnauthenticatedRejected,
		apicontract.StatusUnauthenticated,
		apicontract.StatusAlreadyExists,
		apicontract.StatusNotFound,
		apicontract.StatusPermissionDenied,
		apicontract.StatusInvalidArgument,
		apicontract.StatusUnavailable,
	}
	for _, st := range statuses {
		if !strings.HasPrefix(st, "codes.") {
			t.Errorf("Status constant %q does not start with 'codes.'", st)
		}
	}

	// 4. Assert composite and general constants are non-empty
	composites := []string{
		apicontract.RPCGeneralProvider,
		apicontract.RPCProfileServiceFullLifecycle,
		apicontract.RPCTenantServiceFullLifecycle,
		apicontract.RPCContainerRegistryServiceShareLifecycle,
		apicontract.RPCProfileServiceDeleteProfileRead,
		apicontract.RPCContainerRegistryServiceUnshareImageRead,
		apicontract.RPCTokenServiceIssueTenantTokenProfileService,
		apicontract.RPCTokenServiceIssueTenantTokenContainerRegistryService,
	}
	for _, c := range composites {
		if c == "" {
			t.Errorf("Composite RPC constant is empty")
		}
	}

	if got, want := apicontract.RPCTokenServiceIssueTenantTokenProfileService,
		apicontract.RPCTokenServiceIssueTenantToken+" / "+apicontract.RPCProfileServiceListProfiles; got != want {
		t.Errorf("profile downstream composite RPC mismatch: got %q, want %q", got, want)
	}
	if got, want := apicontract.RPCTokenServiceIssueTenantTokenContainerRegistryService,
		apicontract.RPCTokenServiceIssueTenantToken+" / "+apicontract.RPCContainerRegistryServiceUpdateRepositoryPolicy; got != want {
		t.Errorf("registry downstream composite RPC mismatch: got %q, want %q", got, want)
	}
}

func TestObservationFormatting(t *testing.T) {
	formatted := apicontract.FormatObservation(
		apicontract.RPCProfileServiceListProfiles,
		apicontract.EndpointGlobal,
		apicontract.StatusOk,
		"Runner profiles collection data source query",
	)
	expected := "OBSERVATION: RPC=ProfileService.ListProfiles Endpoint=Global Status=codes.OK Scope=Runner profiles collection data source query"
	if formatted != expected {
		t.Fatalf("FormatObservation mismatch:\ngot:  %q\nwant: %q", formatted, expected)
	}

	// Test with Builder Service and Regional Compute
	builderFormatted := apicontract.FormatObservation(
		apicontract.RPCBuilderServiceGetBuilderConfiguration,
		apicontract.EndpointRegionalCompute,
		apicontract.StatusOk,
		"Builder configuration data source query",
	)
	builderExpected := "OBSERVATION: RPC=BuilderService.GetBuilderConfiguration Endpoint=Regional Compute Status=codes.OK Scope=Builder configuration data source query"
	if builderFormatted != builderExpected {
		t.Fatalf("FormatObservation mismatch for builder:\ngot:  %q\nwant: %q", builderFormatted, builderExpected)
	}
}

func TestAssertionFormatting(t *testing.T) {
	formatted := apicontract.FormatAssertion(apicontract.AssertionSecretScrubbing)
	expected := "ASSERTION: SecretScrubbingVerified"
	if formatted != expected {
		t.Fatalf("FormatAssertion mismatch:\ngot:  %q\nwant: %q", formatted, expected)
	}
}
