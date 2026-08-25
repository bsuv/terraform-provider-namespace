package apicontract

import "fmt"

// RPC Method identifiers matching generated protobuf service and method definitions.
const (
	// GitHub Runner Profiles & Images (githubv1beta)
	RPCProfileServiceCreateProfile = "ProfileService.CreateProfile"
	RPCProfileServiceUpdateProfile = "ProfileService.UpdateProfile"
	RPCProfileServiceListProfiles  = "ProfileService.ListProfiles"
	RPCProfileServiceDeleteProfile = "ProfileService.DeleteProfile"
	RPCImageServiceListBaseImages  = "ImageService.ListBaseImages"

	// IAM Tenant Management, Membership & Ephemeral Tokens (iamv1beta)
	RPCTenantServiceEnsureTenantGroup            = "TenantService.EnsureTenantGroup"
	RPCTenantServiceDescribeTenantGroup          = "TenantService.DescribeTenantGroup"
	RPCTenantServiceListTenantGroups             = "TenantService.ListTenantGroups"
	RPCTenantServiceDeleteTenantGroup            = "TenantService.DeleteTenantGroup"
	RPCTenantServiceIssueTenantClientCertificate = "TenantService.IssueTenantClientCertificate"
	RPCTenantMembershipServiceListTenantMembers  = "TenantMembershipService.ListTenantMembers"
	RPCTokenServiceIssueTenantToken              = "TokenService.IssueTenantToken"
	RPCTokenServiceIssueIdToken                  = "TokenService.IssueIdToken"

	// Container Registry (registryv1beta)
	RPCContainerRegistryServiceShareImage             = "ContainerRegistryService.ShareImage"
	RPCContainerRegistryServiceUnshareImage           = "ContainerRegistryService.UnshareImage"
	RPCContainerRegistryServiceListSharedImages       = "ContainerRegistryService.ListSharedImages"
	RPCContainerRegistryServiceGetDefaultPolicy       = "ContainerRegistryService.GetDefaultPolicy"
	RPCContainerRegistryServiceUpdateDefaultPolicy    = "ContainerRegistryService.UpdateDefaultPolicy"
	RPCContainerRegistryServiceGetRepositoryPolicy    = "ContainerRegistryService.GetRepositoryPolicy"
	RPCContainerRegistryServiceUpdateRepositoryPolicy = "ContainerRegistryService.UpdateRepositoryPolicy"
	RPCContainerRegistryServiceGetImage               = "ContainerRegistryService.GetImage"
	RPCContainerRegistryServiceListImages             = "ContainerRegistryService.ListImages"

	// Vault Secret Metadata (vaultv1beta)
	RPCVaultServiceListObjects = "VaultService.ListObjects"

	// Integration Build Caches (bazelv1beta, gradlev1beta, httpcachev1beta)
	RPCBazelCacheServiceEnsureBazelCache   = "BazelCacheService.EnsureBazelCache"
	RPCGradleCacheServiceEnsureGradleCache = "GradleCacheService.EnsureGradleCache"
	RPCHttpCacheServiceEnsureHttpCache     = "HttpCacheService.EnsureHttpCache"

	// Compute, Storage & Builder Infrastructure (computev1beta, builderv1beta)
	RPCStorageServiceGetVolumeTagSummary      = "StorageService.GetVolumeTagSummary"
	RPCStorageServiceDescribePersistentVolume = "StorageService.DescribePersistentVolume"
	RPCComputeServiceGetNetworkConfiguration  = "ComputeService.GetNetworkConfiguration"
	RPCBuilderServiceGetBuilderConfiguration  = "BuilderService.GetBuilderConfiguration"
	RPCBuilderServiceGetBuilderUsage          = "BuilderService.GetBuilderUsage"

	// General & Composite RPC identifiers used in test lifecycle verification
	RPCGeneralProvider                                      = "General Provider RPC"
	RPCProfileServiceFullLifecycle                          = "ProfileService.CreateProfile / UpdateProfile / DeleteProfile"
	RPCTenantServiceFullLifecycle                           = "TenantService.EnsureTenantGroup / DeleteTenantGroup"
	RPCContainerRegistryServiceShareLifecycle               = "ContainerRegistryService.ShareImage / UnshareImage"
	RPCProfileServiceDeleteProfileRead                      = "ProfileService.DeleteProfile / Read"
	RPCContainerRegistryServiceUnshareImageRead             = "ContainerRegistryService.UnshareImage / Read"
	RPCTokenServiceIssueTenantTokenProfileService           = RPCTokenServiceIssueTenantToken + " / " + RPCProfileServiceListProfiles
	RPCTokenServiceIssueTenantTokenContainerRegistryService = RPCTokenServiceIssueTenantToken + " / " + RPCContainerRegistryServiceUpdateRepositoryPolicy
)

// Endpoint domain classifications.
const (
	EndpointGlobal          = "Global"
	EndpointIAM             = "IAM"
	EndpointRegionalCompute = "Regional Compute"
	EndpointCompute         = "Compute"
	EndpointIAMGlobal       = "IAM / Global"
)

// Standard gRPC status codes and test assertion outcomes.
const (
	StatusOk                      = "codes.OK"
	StatusOkReconciled            = "codes.OK (Reconciled)"
	StatusOkAlreadyExistsImport   = "codes.AlreadyExists (Import)"
	StatusOkCursorTraversal       = "codes.OK (Cursor Traversal)"
	StatusFailedPrecondition      = "codes.FailedPrecondition"
	StatusNotFoundRecreated       = "codes.NotFound (Recreated)"
	StatusUnauthenticatedRejected = "codes.Unauthenticated (Rejected)"
	StatusUnauthenticated         = "codes.Unauthenticated"
	StatusAlreadyExists           = "codes.AlreadyExists"
	StatusNotFound                = "codes.NotFound"
	StatusPermissionDenied        = "codes.PermissionDenied"
	StatusInvalidArgument         = "codes.InvalidArgument"
	StatusUnavailable             = "codes.Unavailable"
)

// Typed assertion identifiers for security, non-persistence, tenant-isolation, and matrix behavior.
const (
	AssertionSecretScrubbing           = "SecretScrubbingVerified"
	AssertionNonPersistence            = "NonPersistenceVerified"
	AssertionTenantIsolation           = "TenantIsolationVerified"
	AssertionDriftDetection            = "DriftDetectionVerified"
	AssertionConcurrencyConflict       = "ConcurrencyConflictVerified"
	AssertionExternalDeletionRecreated = "ExternalDeletionRecreatedVerified"
	AssertionCollisionPrevention       = "CollisionPreventionVerified"
	AssertionPaginationMultipleCursors = "PaginationMultipleCursorsVerified"
	AssertionExpiredTokenRejection     = "ExpiredTokenRejectionVerified"
	AssertionAuthPrecedenceHierarchy   = "AuthPrecedenceHierarchyVerified"
	AssertionCachePlanDeferral         = "CachePlanDeferralVerified"
	AssertionCustomImageCombinations   = "CustomRunnerImageCombinationsVerified"
)

// FormatObservation formats a standardized, structured observation log entry
// formatted as: OBSERVATION: RPC=<rpc> Endpoint=<endpoint> Status=<status> Scope=<scope>
func FormatObservation(rpc, endpoint, status, scope string) string {
	return fmt.Sprintf("OBSERVATION: RPC=%s Endpoint=%s Status=%s Scope=%s", rpc, endpoint, status, scope)
}

// FormatAssertion formats a standardized assertion log entry
// formatted as: ASSERTION: <assertion_id>
func FormatAssertion(assertionID string) string {
	return fmt.Sprintf("ASSERTION: %s", assertionID)
}
