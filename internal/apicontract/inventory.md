# Namespace Cloud gRPC API Surface Catalog & Terraform Object Inventory

- **Specification Revision:** 2026-08-21
- **Protobuf Package Base:** `buf.build/gen/go/namespace/cloud/protocolbuffers/go@v1.36.12-20260820164744-a1973bcf4d87.1`
- **gRPC Stubs Package Base:** `buf.build/gen/go/namespace/cloud/grpc/go@v1.6.2-20260820164744-a1973bcf4d87.1`
- **Integrations SDK:** `namespacelabs.dev/integrations@v0.0.10`
- **Terraform Plugin Framework:** `v1.19.0`
- **Go Baseline:** `1.26.7`

---

## 1. Managed Resources

### 1.1 `resource "namespace_github_runner_profile"`
- **Milestone:** Phase 1
- **gRPC Service:** `namespace.cloud.github.v1beta.ProfileService` (`githubv1betagrpc.ProfileServiceClient`)
- **RPC Methods:**
  - Create: `CreateProfile(CreateProfileRequest) returns (CreateProfileResponse)`
  - Read: `ListProfiles(Empty) returns (ListProfilesResponse)` (client-side matching by stable `profile_id`)
  - Update: `UpdateProfile(UpdateProfileRequest) returns (UpdateProfileResponse)`
  - Delete: `DeleteProfile(DeleteProfileRequest) returns (Empty)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`nsct_...`)
- **Minimum Permissions:** `{"github/profile", "manage"}`
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | `UseStateForUnknown()`, maps server `profile_id` |
  | `profile_id` | String | Computed | `UseStateForUnknown()`, server stable ID |
  | `version` | Int64 | Computed | `UseStateForUnknown()`, tracks `update_version` |
  | `created_at` | String | Computed | `UseStateForUnknown()`, RFC3339 timestamp |
  | `updated_at` | String | Computed | `UseStateForUnknown()`, RFC3339 timestamp |
  | `tag` | String | Required | Non-empty string; unique within workspace |
  | `os` | String | Required | Base image label (e.g. `ubuntu-24.04`, `macos-15`) |
  | `description` | String | Optional, Computed | UTF-8 description string |
  | `emoji` | String | Optional, Computed | Display icon string |
  | `swap_size_mb` | Int64 | Optional, Computed | `AtLeast(0)`, `Between(0, math.MaxInt32)` |
  | `builder_mode` | String | Optional, Computed | `OneOf("BUILDER_MODE_UNSPECIFIED", "USE_REMOTE_BUILDER", "USE_LOCAL_CACHE", "NO_CACHING")` |
  | `access_level` | String | Optional, Computed | `OneOf("ACCESS_LEVEL_UNSPECIFIED", "ACCESS_LEVEL_PERMISSIVE", "ACCESS_LEVEL_LIMITED", "ACCESS_LEVEL_RESTRICTED")` |
  | `experimental_features` | List(String) | Optional, Computed | Preserved in configuration order |
  | `instance_shape` | Single Nested | Optional, Computed | Compute sizing block |
  | `instance_shape.virtual_cpu` | Int64 | Optional, Computed | `Between(1, math.MaxInt32)` |
  | `instance_shape.memory_megabytes` | Int64 | Optional, Computed | `Between(1, math.MaxInt32)` |
  | `instance_shape.machine_arch` | String | Optional, Computed | `OneOf("amd64", "arm64", "")` |
  | `instance_shape.os` | String | Optional, Computed | `OneOf("linux", "macos", "")` |
  | `instance_shape.selectors` | List Nested | Optional, Computed | Labels preserved in configuration order |
  | `cache_volume_settings` | List Nested | Optional, Computed | Cache volume attachments preserved in configuration order |
  | `cache_volume_settings.*.volume_tag` | String | Required | Unique non-empty tag |
  | `cache_volume_settings.*.volume_size_gb` | Int64 | Required | `Between(1, math.MaxInt32)` |
  | `cache_volume_settings.*.volume_reserved_size_gb` | Int64 | Optional, Computed | `Between(0, math.MaxInt32)`, must be `< volume_size_gb` |
  | `cache_volume_settings.*.bundled_container_cache` | Bool | Optional, Computed | Boolean flag |
  | `cache_volume_settings.*.bundled_buildkit_cache` | Bool | Optional, Computed | Boolean flag |
  | `cache_volume_settings.*.bundled_git_mirror` | Bool | Optional, Computed | Boolean flag |
  | `cache_volume_settings.*.bundled_runner_tools_cache` | Bool | Optional, Computed | Boolean flag |
  | `cache_volume_settings.*.bundled_runner_actions_cache` | Bool | Optional, Computed | Boolean flag |
  | `cache_volume_settings.*.allow_commit_from_branch` | List(String) | Optional, Computed | Preserved in configuration order |
  | `custom_runner_image_spec` | Single Nested | Optional | Customization container spec; at least one of ubuntu/docker spec required if block present |
  | `custom_runner_image_spec.ubuntu_spec.apt_package` | List(String) | Required | List of apt packages |
  | `custom_runner_image_spec.docker_spec.dockerfile_content` | String | Required | Inline Dockerfile |
  | `network_policy` | Single Nested | Optional, Computed | Egress rules |
  | `network_policy.egress` | String | Required | `OneOf("EGRESS_POLICY_UNKNOWN", "EGRESS_POLICY_NONE", "EGRESS_POLICY_DOMAIN_ALLOW_LIST", "EGRESS_POLICY_TAG")` |
  | `network_policy.egress_domain_allow_list` | List(String) | Optional, Computed | Allowed domains; only valid with `EGRESS_POLICY_DOMAIN_ALLOW_LIST` |
  | `network_policy.advisory` | Bool | Optional, Computed | Advisory flag; only valid with `EGRESS_POLICY_DOMAIN_ALLOW_LIST` |
  | `network_policy.egress_policy_tag` | String | Optional, Computed | Policy tag; only valid with `EGRESS_POLICY_TAG` |
  | `custom_runner_images` | List Nested | Computed | Flattened status list sorted by `image_ref` |
- **Stable Identity:** `profile_id`.
- **Custom-image product contract:** Omitting `custom_runner_image_spec` models neither customization; a present block requires at least one child and permits Ubuntu-only, Docker-only, or both because the pinned generated fields are independent. `TestAccRunnerProfile_CustomImageCombinations` exercises all four cases against the live service, and the release report fails closed if that protected test is absent, skipped, or rejected.
- **Import Support:** Supported by `profile_id` or unique single-match `tag` (state is rewritten to stable `profile_id`).
- **Replacement Boundaries:** Changing `profile_id` forces replace; spec edits update in-place.
- **Pagination & Ordering:** Unpaginated `ListProfiles`; deterministically sorted.
- **NotFound Semantics:** Read NotFound -> remove from state; Delete NotFound -> success.
- **Concurrency Behavior:** `UpdateProfile` sends `update_version`. On `FailedPrecondition`, returns diagnostic advising `terraform refresh`.
- **Deadline / Retry Class:** Read: 30s deadline, 15s per attempt, at most 4 attempts. Mutating: 60s, 1 attempt (fail-fast).

---

### 1.2 `resource "namespace_tenant_group"`
- **Milestone:** Phase 2
- **gRPC Service:** `namespace.cloud.iam.v1beta.TenantService` (`iamv1betagrpc.TenantServiceClient`)
- **RPC Methods:**
  - Create: `DescribeTenantGroup` (preflight collision check) then `EnsureTenantGroup(EnsureTenantGroupRequest) returns (TenantGroupResponse)`
  - Read: `DescribeTenantGroup(TenantGroupReference) returns (DescribeTenantGroupResponse)`
  - Update: `EnsureTenantGroup(EnsureTenantGroupRequest) returns (TenantGroupResponse)`
  - Delete: `DeleteTenantGroup(TenantGroupReference) returns (Empty)`
- **Endpoint Class:** IAM (`https://iam.namespaceapis.com:443`)
- **Credential Class:** Partner Token
- **Minimum Permissions:** `{"iam/tenant-group", "manage"}`
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Matches `name` |
  | `name` | String | Required | `RequiresReplace()`, unique partner-scoped group identifier |
  | `policies` | List Nested | Optional | Key-value policy configuration; preserved in configuration/server order |
  | `policies.*.policy` | String | Required | Policy identifier string |
  | `policies.*.value` | String | Required | JSON/protobuf serialized policy value |
  | `enrolled_tenant_ids` | List(String) | Computed | Sorted deterministically |
- **Stable Identity:** `name` (Partner-scoped).
- **Import Support:** `terraform import namespace_tenant_group.name <group-name>`.
- **Preflight Collision Check:** Create calls `DescribeTenantGroup` first. If group exists, fails with diagnostic instructing `terraform import` (no silent adoption).
- **Active Enrolled Guard:** Deleting group with enrolled tenants returns `FailedPrecondition`; diagnostic instructs user to unenrol tenants.
- **Deadline / Retry Class:** Read: 30s deadline, at most 4 attempts. Mutating: 60s, 1 attempt (fail-fast).

---

### 1.3 `resource "namespace_registry_shared_image"`
- **Milestone:** Phase 2
- **gRPC Service:** `namespace.cloud.registry.v1beta.ContainerRegistryService` (`registryv1betagrpc.ContainerRegistryServiceClient`)
- **RPC Methods:**
  - Create: `ShareImage(ShareImageRequest) returns (SharedImage)`
  - Read: `ListSharedImages(ListSharedImagesRequest) returns (ListSharedImagesResponse)` (client-side matching by stable `id`)
  - Delete: `UnshareImage(UnshareImageRequest) returns (Empty)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | `UseStateForUnknown()`, server share ID |
  | `repository` | String | Required | `RequiresReplace()`, source image repository |
  | `digest` | String | Required | `RequiresReplace()`, sha256 image digest |
  | `visibility` | String | Required | `RequiresReplace()`, `OneOf("PUBLIC", "PARTNER")` |
  | `expires_at` | String | Optional | `RequiresReplace()`, `ValidRFC3339()` |
  | `suffix` | String | Optional | `RequiresReplace()`, access ref suffix |
  | `image_ref` | String | Computed | Public access URL (e.g. `public.nscr.io/{id}/{suffix}`) |
  | `shared_repository` | String | Computed | Returned shared repository |
  | `shared_digest` | String | Computed | Returned shared digest |
- **Stable Identity:** Server-assigned `id`.
- **Import Support:** Deliberately unsupported (API does not return source repository; import cannot round-trip).
- **Replacement Boundaries:** All inputs are `RequiresReplace()`.
- **NotFound Semantics:** Read NotFound (expired or deleted) -> remove from state. Delete NotFound -> success.
- **Deadline / Retry Class:** Read: 30s, at most 4 attempts. Mutating: 60s, 1 attempt (fail-fast).

---

### 1.4 `resource "namespace_registry_default_policy"`
- **Milestone:** Phase 2
- **gRPC Service:** `namespace.cloud.registry.v1beta.ContainerRegistryService`
- **RPC Methods:**
  - Read: `GetDefaultPolicy(Empty) returns (GetDefaultPolicyResponse)`
  - Create/Update: `UpdateDefaultPolicy(UpdateDefaultPolicyRequest) returns (Empty)`
- **Endpoint Class:** Global
- **Credential Class:** Tenant Bearer Token
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Fixed singleton ID `"default"` |
  | `revision` | Int64 | Computed | `UseStateForUnknown()`, optimistic concurrency revision |
  | `default_expiration` | String | Optional, Computed | Duration string, `DurationAtLeast(4h)` |
  | `expiration_enforcement` | String | Required | `OneOf("EXPIRATION_ENFORCEMENT_ENFORCE", "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE")` |
- **Cross-Field Validation:** When `expiration_enforcement == "EXPIRATION_ENFORCEMENT_ENFORCE"`, `default_expiration` is required and must be >= 4h.
- **Destroy Semantics:** Resets policy to neutral configuration (`default_expiration = nil`, `expiration_enforcement = DO_NOT_ENFORCE`) and reads back to verify neutral state before removing state.
- **Import Support:** `terraform import namespace_registry_default_policy.name default`.
- **Deadline / Retry Class:** Read: 30s, at most 4 attempts. Mutating: 60s, 1 attempt (fail-fast).

---

### 1.5 `resource "namespace_registry_repository_policy"`
- **Milestone:** Phase 2
- **gRPC Service:** `namespace.cloud.registry.v1beta.ContainerRegistryService`
- **RPC Methods:**
  - Read: `GetRepositoryPolicy(GetRepositoryPolicyRequest) returns (GetRepositoryPolicyResponse)`
  - Create/Update: `UpdateRepositoryPolicy(UpdateRepositoryPolicyRequest) returns (Empty)`
- **Endpoint Class:** Global
- **Credential Class:** Tenant Bearer Token
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Matches `repository` |
  | `repository` | String | Required | `RequiresReplace()`, repository name |
  | `revision` | Int64 | Computed | `UseStateForUnknown()`, optimistic concurrency revision |
  | `expiration` | String | Optional, Computed | Duration string, `DurationAtLeast(4h)` |
  | `expiration_enforcement` | String | Required | `OneOf("EXPIRATION_ENFORCEMENT_ENFORCE", "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE")` |
- **Cross-Field Validation:** When `expiration_enforcement == "EXPIRATION_ENFORCEMENT_ENFORCE"`, `expiration` is required and must be >= 4h.
- **Destroy Semantics:** Resets repository override (`expiration = nil`, `expiration_enforcement = UNKNOWN`), reverting repository to default policy, and reads back to verify reset before removing state.
- **Import Support:** `terraform import namespace_registry_repository_policy.name <repository>`.
- **Deadline / Retry Class:** Read: 30s, at most 4 attempts. Mutating: 60s, 1 attempt (fail-fast).

---

## 2. Ephemeral Resources

### 2.1 `ephemeral "namespace_tenant_token"`
- **Milestone:** Phase 3
- **gRPC Service:** `namespace.cloud.iam.v1beta.TokenService` (`iamv1betagrpc.TokenServiceClient`)
- **RPC Method:** `IssueTenantToken(IssueTenantTokenRequest) returns (IssueTenantTokenResponse)`
- **Endpoint Class:** IAM (`https://iam.namespaceapis.com:443`)
- **Credential Class:** Partner Token (`{"iam/tenant-token", "issue"}`)
- **Lifecycle / Contract:** `Open` only. Ephemeral credentials exist only in-memory during Terraform runs and are not stored in state. `Renew`, `Close`, state persistence, and `import` are deliberately unsupported.
- **Deadline / Retry Class:** Mutating: 30s deadline, 1 attempt (fail-fast).
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `tenant_id` | String | Required | Non-empty string, target tenant identifier |
  | `actor_id` | String | Required | Non-empty string, requesting actor identity |
  | `duration_secs` | Int64 | Optional | `AtLeast(1)`, token validity duration in seconds |
  | `policies` | List Nested | Optional | Scoped token policies |
  | `policies.*.policy_type` | String | Required | Non-empty string policy type |
  | `policies.*.value` | String | Required | Serialized policy value string |
  | `access` | Single Nested | Optional | Granular access policy configuration |
  | `access.grants` | List Nested | Required | List of permission grants |
  | `access.grants.*.resource_type` | String | Required | Non-empty resource type identifier |
  | `access.grants.*.resource_id` | String | Required | Target resource identifier |
  | `access.grants.*.actions` | List(String) | Required | List of allowed action strings |
  | `bearer_token` | String | Computed, Sensitive | Issued short-lived bearer token |

---

### 2.2 `ephemeral "namespace_id_token"`
- **Milestone:** Phase 3
- **gRPC Service:** `namespace.cloud.iam.v1beta.TokenService` (`iamv1betagrpc.TokenServiceClient`)
- **RPC Method:** `IssueIdToken(IssueIdTokenRequest) returns (IssueIdTokenResponse)`
- **Endpoint Class:** IAM (`https://iam.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`{"federation", "issue_federation"}`)
- **Lifecycle / Contract:** `Open` only. Ephemeral credentials exist only in-memory during Terraform runs and are not stored in state. `Renew`, `Close`, state persistence, and `import` are deliberately unsupported.
- **Deadline / Retry Class:** Mutating: 30s deadline, 1 attempt (fail-fast).
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `audience` | String | Required | Non-empty recipient audience URI / identifier |
  | `id_token` | String | Computed, Sensitive | Issued short-lived signed OIDC ID token |

---

### 2.3 `ephemeral "namespace_client_certificate"`
- **Milestone:** Phase 3
- **gRPC Service:** `namespace.cloud.iam.v1beta.TenantService` (`iamv1betagrpc.TenantServiceClient`)
- **RPC Method:** `IssueTenantClientCertificate(IssueTenantClientCertificateRequest) returns (IssueTenantClientCertificateResponse)`
- **Endpoint Class:** IAM (`https://iam.namespaceapis.com:443`)
- **Credential Class:** Trusted Authority Signed JWT (`{"iam/client-certificate", "issue"}`)
- **Lifecycle / Contract:** `Open` only. If `public_key_pem` is provided by caller, server signs it and `private_key_pem` remains null. If omitted, server generates a key pair and returns both certificate and private key.
- **Deadline / Retry Class:** Mutating: 30s deadline, 1 attempt (fail-fast).
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `tenant_id` | String | Required | Non-empty string, target tenant identifier |
  | `actor_id` | String | Required | Non-empty string, requesting actor identity |
  | `duration_secs` | Int64 | Optional | `AtLeast(1)`, certificate validity duration in seconds |
  | `public_key_pem` | String | Optional | PEM-encoded public key |
  | `policies` | List Nested | Optional | Scoped token policies |
  | `policies.*.policy_type` | String | Required | Non-empty string policy type |
  | `policies.*.value` | String | Required | Serialized policy value string |
  | `access` | Single Nested | Optional | Granular access policy configuration |
  | `access.grants` | List Nested | Required | List of permission grants |
  | `access.grants.*.resource_type` | String | Required | Non-empty resource type identifier |
  | `access.grants.*.resource_id` | String | Required | Target resource identifier |
  | `access.grants.*.actions` | List(String) | Required | List of allowed action strings |
  | `client_certificate_pem` | String | Computed, Sensitive | Issued PEM-encoded client certificate |
  | `private_key_pem` | String | Computed, Sensitive | Issued PEM-encoded private key (null if `public_key_pem` provided) |

---

### 2.4 `ephemeral "namespace_bazel_cache"`
- **Milestone:** Phase 3
- **gRPC Service:** `namespace.cloud.integrations.bazel.v1beta.BazelCacheService` (`bazelv1betagrpc.BazelCacheServiceClient`)
- **RPC Method:** `EnsureBazelCache(EnsureBazelCacheRequest) returns (EnsureBazelCacheResponse)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`{"integrations/bazel", "ensure"}`)
- **Lifecycle / Contract:** `Open` only. Note: Opening invokes an `EnsureBazelCache` RPC that may create or reuse durable server-side cache infrastructure. Terraform does not manage cache persistence or deletion.
- **Deadline / Retry Class:** Mutating: 60s deadline, 1 attempt (fail-fast).
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `key` | String | Required | `OneOf("ci", "workstation")` |
  | `version` | Int64 | Optional | Protocol version |
  | `site` | String | Optional | Site/region placement hint |
  | `enable_remote_asset_api` | Bool | Optional | Remote asset API support flag |
  | `experimental_direct_mtls` | Bool | Optional | Direct mTLS connection mode flag |
  | `experimental_cache_name` | String | Optional | Explicit cache namespace identifier |
  | `cache_endpoint` | String | Computed | Primary Bazel remote cache endpoint (gRPC/mTLS) |
  | `https_mtls_cache_endpoint` | String | Computed | HTTPS mTLS cache endpoint |
  | `https_cache_endpoint` | String | Computed | HTTPS cache endpoint |
  | `server_ca_pem` | String | Computed | PEM-encoded server CA certificate |
  | `client_cert_pem` | String | Computed, Sensitive | PEM-encoded client certificate |
  | `client_key_pem` | String | Computed, Sensitive | PEM-encoded private key |
  | `expires_at` | String | Computed | RFC3339 timestamp when mTLS credentials expire |
  | `credential_helper_domains` | List(String) | Computed | Domains supported by credential helper |
  | `build_event_endpoint` | String | Computed | Build Event Protocol (BES) streaming endpoint |
  | `remote_asset_endpoint` | String | Computed | Remote Asset API endpoint |
  | `use_workload_mtls` | Bool | Computed | Workload identity mTLS flag |

---

### 2.5 `ephemeral "namespace_gradle_cache"`
- **Milestone:** Phase 3
- **gRPC Service:** `namespace.cloud.integrations.gradle.v1beta.GradleCacheService` (`gradlev1betagrpc.GradleCacheServiceClient`)
- **RPC Method:** `EnsureGradleCache(EnsureGradleCacheRequest) returns (EnsureGradleCacheResponse)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`{"integrations/gradle", "ensure"}`)
- **Lifecycle / Contract:** `Open` only. Note: Opening invokes an `EnsureGradleCache` RPC that may create or reuse durable server-side cache infrastructure.
- **Deadline / Retry Class:** Mutating: 60s deadline, 1 attempt (fail-fast).
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `name` | String | Required | Non-empty cache name identifier |
  | `site` | String | Optional, Computed | Site/region placement hint |
  | `cache_endpoint_url` | String | Computed | HTTP(S) build cache URL |
  | `username` | String | Computed, Sensitive | HTTP Basic Auth username |
  | `password` | String | Computed, Sensitive | HTTP Basic Auth password |
  | `expires_at` | String | Computed | RFC3339 timestamp when credentials expire |

---

### 2.6 `ephemeral "namespace_http_cache"`
- **Milestone:** Phase 3
- **gRPC Service:** `namespace.cloud.integrations.httpcache.v1beta.HttpCacheService` (`httpcachev1betagrpc.HttpCacheServiceClient`)
- **RPC Method:** `EnsureHttpCache(EnsureHttpCacheRequest) returns (EnsureHttpCacheResponse)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`{"integrations/httpcache", "ensure"}`)
- **Lifecycle / Contract:** `Open` only. Note: Opening invokes an `EnsureHttpCache` RPC that may create or reuse durable server-side cache infrastructure.
- **Deadline / Retry Class:** Mutating: 60s deadline, 1 attempt (fail-fast).
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `name` | String | Required | Non-empty cache name identifier |
  | `site` | String | Optional, Computed | Site/region placement hint |
  | `cache_endpoint_url` | String | Computed | HTTP(S) cache URL |
  | `username` | String | Computed, Sensitive | HTTP Basic Auth username |
  | `password` | String | Computed, Sensitive | HTTP Basic Auth password |
  | `expires_at` | String | Computed | RFC3339 timestamp when credentials expire |

---

## 3. Data Sources

### 3.1 `data "namespace_github_runner_profile"`
- **Milestone:** Phase 1
- **gRPC Service:** `namespace.cloud.github.v1beta.ProfileService` (`githubv1betagrpc.ProfileServiceClient`)
- **RPC Method:** `ListProfiles(Empty) returns (ListProfilesResponse)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`{"github/profile", "read"}`)
- **Lookup Semantics:** Exactly one of `profile_id` or `tag` must be specified. Returns error diagnostic if zero or multiple matches found.
- **Deadline / Retry Class:** Read: 30s deadline, at most 4 attempts with exponential backoff.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Stable identifier matching `profile_id` |
  | `profile_id` | String | Optional, Computed | `ExactlyOneOf("profile_id", "tag")` |
  | `tag` | String | Optional, Computed | `ExactlyOneOf("profile_id", "tag")` |
  | `version` | Int64 | Computed | Optimistic concurrency revision number |
  | `created_at` | String | Computed | RFC3339Nano creation timestamp |
  | `updated_at` | String | Computed | RFC3339Nano update timestamp |
  | `os` | String | Computed | Operating system label |
  | `description` | String | Computed | Profile description |
  | `emoji` | String | Computed | Profile emoji |
  | `swap_size_mb` | Int64 | Computed | Swap size in megabytes |
  | `builder_mode` | String | Computed | Builder caching mode |
  | `access_level` | String | Computed | Workload access level |
  | `experimental_features` | List(String) | Computed | Experimental feature flags |
  | `instance_shape` | Single Nested | Computed | Instance compute sizing |
  | `instance_shape.virtual_cpu` | Int64 | Computed | Virtual CPU count |
  | `instance_shape.memory_megabytes` | Int64 | Computed | Memory in megabytes |
  | `instance_shape.machine_arch` | String | Computed | Machine architecture (`amd64`, `arm64`) |
  | `instance_shape.os` | String | Computed | OS family (`linux`, `macos`) |
  | `instance_shape.selectors` | List Nested | Computed | Node selectors |
  | `cache_volume_settings` | List Nested | Computed | Attached cache volume configurations |
  | `custom_runner_image_spec` | Single Nested | Computed | Custom runner container image spec |
  | `network_policy` | Single Nested | Computed | Egress network policy configuration |
  | `custom_runner_images` | List Nested | Computed | Flattened built custom images |

---

### 3.2 `data "namespace_github_runner_profiles"`
- **Milestone:** Phase 1
- **gRPC Service:** `namespace.cloud.github.v1beta.ProfileService`
- **RPC Method:** `ListProfiles(Empty) returns (ListProfilesResponse)`
- **Endpoint Class:** Global
- **Credential Class:** Tenant Bearer Token
- **Ordering:** Deterministically sorted by `profile_id`.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Fixed constant identifier |
  | `profiles` | List Nested | Computed | List of runner profiles in workspace sorted by `profile_id` |

---

### 3.3 `data "namespace_github_base_images"`
- **Milestone:** Phase 1
- **gRPC Service:** `namespace.cloud.github.v1beta.ImageService` (`githubv1betagrpc.ImageServiceClient`)
- **RPC Method:** `ListBaseImages(ListBaseImagesRequest) returns (ListBaseImagesResponse)`
- **Endpoint Class:** Global
- **Credential Class:** Tenant Bearer Token
- **API Mapping:** `labels` filter maps to `stdlib.StringMatcher{Values: labels, Op: stdlib.StringMatcher_IS_ANY_OF}`. `release_stages` maps to protobuf enum `ReleaseStage`.
- **Ordering:** Deterministically sorted by `label`.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Data source identifier |
  | `labels` | List(String) | Optional | Filter by runner labels (maps to `IS_ANY_OF`) |
  | `release_stages` | List(String) | Optional | Filter by release stages (`STAGE_UNKNOWN`, `PRODUCTION`, `CANARY`) |
  | `images` | List Nested | Computed | Matching base images sorted by `label` |
  | `images.*.label` | String | Computed | Runs-on label (e.g. `ubuntu-24.04`) |
  | `images.*.image_ref` | String | Computed | Canonical container image reference |
  | `images.*.title` | String | Computed | Human-readable title |
  | `images.*.spec` | Single Nested | Computed | Base image detailed specification |
  | `images.*.spec.id` | String | Computed | Base image unique ID |
  | `images.*.spec.os` | String | Computed | OS family (`linux`, `macos`) |
  | `images.*.spec.os_name` | String | Computed | Human-friendly OS name |
  | `images.*.spec.arch` | String | Computed | Architecture (`amd64`, `arm64`) |
  | `images.*.spec.packages` | List Nested | Computed | Pre-installed package metadata sorted by `name`, `version`, `build`, `type` |
  | `images.*.spec.is_default` | Bool | Computed | Default image flag |

---

### 3.4 `data "namespace_tenant_group"`
- **Milestone:** Phase 2
- **gRPC Service:** `namespace.cloud.iam.v1beta.TenantService` (`iamv1betagrpc.TenantServiceClient`)
- **RPC Method:** `DescribeTenantGroup(TenantGroupReference) returns (DescribeTenantGroupResponse)`
- **Endpoint Class:** IAM (`https://iam.namespaceapis.com:443`)
- **Credential Class:** Partner Token (`{"iam/tenant-group", "read"}`)
- **Ordering:** `enrolled_tenant_ids`, `policies`, and `versions` are sorted deterministically.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Matches `name` |
  | `name` | String | Required | Non-empty tenant group name |
  | `policies` | List Nested | Computed | Active policies attached to group |
  | `policies.*.policy` | String | Computed | Fully qualified policy identifier type |
  | `policies.*.value` | String | Computed | Policy configuration value |
  | `enrolled_tenant_ids` | List(String) | Computed | Sorted list of enrolled tenant IDs |
  | `versions` | List Nested | Computed | Historical policy versions |
  | `versions.*.policy` | String | Computed | Policy identifier type |
  | `versions.*.value` | String | Computed | Policy configuration value |

---

### 3.5 `data "namespace_tenant_members"`
- **Milestone:** Phase 2
- **gRPC Service:** `namespace.cloud.iam.v1beta.TenantMembershipService` (`iamv1betagrpc.TenantMembershipServiceClient`)
- **RPC Method:** `ListTenantMembers(Empty) returns (ListTenantMembersResponse)`
- **Endpoint Class:** IAM
- **Credential Class:** Tenant Bearer Token (`{"iam/member", "read"}`)
- **Ordering:** Sorted deterministically by `user_id`.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Fixed constant identifier |
  | `members` | List Nested | Computed | List of tenant members sorted by `user_id` |
  | `members.*.user_id` | String | Computed | Opaque user ID |
  | `members.*.first_name` | String | Computed | User's first name |
  | `members.*.last_name` | String | Computed | User's last name |
  | `members.*.email` | String | Computed | User's email address |
  | `members.*.role` | String | Computed | Assigned member role enum string |

---

### 3.6 `data "namespace_vault_secret_metadata"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.vault.v1beta.VaultService` (`vaultv1betagrpc.VaultServiceClient`)
- **RPC Method:** `ListObjects(ListObjectsRequest) returns (ListObjectsResponse)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`{"vault", "read"}`)
- **Security & Lifecycle:** Queries Vault metadata via `ListObjects` only; never calls value-revealing `DescribeObject`. Matches `object_id` client-side, filters out soft-deleted secrets (`deleted_at != nil`), and omits `updated_at`.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Matches `object_id` |
  | `object_id` | String | Required | Unique object identifier of the Vault secret |
  | `description` | String | Computed | Human-readable description |
  | `version` | String | Computed | Opaque version identifier |
  | `created_at` | String | Computed | RFC3339 creation timestamp |
  | `deleted_at` | String | Computed | RFC3339 deletion timestamp, if soft-deleted |
  | `labels` | Map(String) | Computed | Key-value labels attached to secret |

---

### 3.7 `data "namespace_cache_volume_summary"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.compute.v1beta.StorageService` (`computev1betagrpc.StorageServiceClient`)
- **RPC Method:** `GetVolumeTagSummary(GetVolumeTagSummaryRequest) returns (GetVolumeTagSummaryResponse)`
- **Endpoint Class:** Regional Compute (`https://eu.compute.namespaceapis.com:443` or configured `compute_endpoint`)
- **Credential Class:** Tenant Bearer Token (`{"compute/storage", "read"}`)
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Matches `tag` |
  | `tag` | String | Required | Volume tag to query |
  | `size_mb` | Int64 | Computed | Requested size in megabytes |
  | `created_at` | String | Computed | RFC3339 creation timestamp |
  | `generations` | List Nested | Computed | Cache volume generations sorted by `created_at` ascending, then `id` ascending |
  | `generations.*.id` | String | Computed | Generation unique identifier |
  | `generations.*.tag` | String | Computed | Volume tag name |
  | `generations.*.created_at` | String | Computed | RFC3339 creation timestamp |
  | `generations.*.destroyed_at` | String | Computed | RFC3339 destruction timestamp |
  | `generations.*.size_mb` | Int64 | Computed | Volume size in megabytes |
  | `generations.*.state` | String | Computed | Volume lifecycle state |
  | `generations.*.generation_reason` | String | Computed | Reason for generation creation |
  | `generations.*.attached_to` | String | Computed | Compute instance ID attached to |
  | `generations.*.attached_at` | String | Computed | RFC3339 attachment timestamp |
  | `generations.*.detached_at` | String | Computed | RFC3339 detachment timestamp |
  | `generations.*.used_mb_when_attached` | Int64 | Computed | Used megabytes when attached |
  | `generations.*.used_mb_when_detached` | Int64 | Computed | Used megabytes when detached |

---

### 3.8 `data "namespace_persistent_volume"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.compute.v1beta.StorageService` (`computev1betagrpc.StorageServiceClient`)
- **RPC Method:** `DescribePersistentVolume(DescribePersistentVolumeRequest) returns (DescribePersistentVolumeResponse)`
- **Endpoint Class:** Regional Compute
- **Credential Class:** Tenant Bearer Token (`{"compute/storage", "read"}`)
- **Lookup Semantics:** Exactly one of `id` or `tag` must be specified; optional `site` filter scopes query.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Optional, Computed | `ExactlyOneOf("id", "tag")`, persistent volume ID |
  | `tag` | String | Optional, Computed | `ExactlyOneOf("id", "tag")`, persistent volume tag |
  | `site` | String | Optional, Computed | Site/region filter (e.g. `zrh`, `ord`, `iad`) |
  | `size_mb` | Int64 | Computed | Volume size in megabytes |
  | `created_at` | String | Computed | RFC3339 creation timestamp |
  | `destroyed_at` | String | Computed | RFC3339 destruction timestamp |
  | `attached_to` | String | Computed | Compute instance ID attached to |
  | `released_at` | String | Computed | RFC3339 release timestamp |
  | `released_reason` | String | Computed | Reason for volume release |

---

### 3.9 `data "namespace_registry_image"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.registry.v1beta.ContainerRegistryService` (`registryv1betagrpc.ContainerRegistryServiceClient`)
- **RPC Method:** `GetImage(GetImageRequest) returns (GetImageResponse)`
- **Endpoint Class:** Global (`https://global.namespaceapis.com:443`)
- **Credential Class:** Tenant Bearer Token (`{"registry", "read"}`)
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Unique identifier `<repository>@<digest>` |
  | `repository` | String | Required | Container registry repository name |
  | `reference` | String | Required | Tag or sha256 digest reference |
  | `digest` | String | Computed | Authoritative sha256 content digest |
  | `created_at` | String | Computed | RFC3339 creation timestamp |
  | `expires_at` | String | Computed | RFC3339 expiration timestamp |
  | `size_bytes` | Int64 | Computed | Total image size in bytes |
  | `tags` | List(String) | Computed | Sorted list of tags pointing to this digest |

---

### 3.10 `data "namespace_registry_images"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.registry.v1beta.ContainerRegistryService`
- **RPC Method:** `ListImages(ListImagesRequest) returns (ListImagesResponse)`
- **Endpoint Class:** Global
- **Credential Class:** Tenant Bearer Token
- **Pagination & Ordering:** Follows server pagination tokens; returns images deterministically sorted.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Matches `repository` |
  | `repository` | String | Required | Container registry repository name |
  | `images` | List Nested | Computed | List of images in repository |
  | `images.*.repository` | String | Computed | Repository name |
  | `images.*.digest` | String | Computed | Image sha256 digest |
  | `images.*.created_at` | String | Computed | RFC3339 creation timestamp |
  | `images.*.expires_at` | String | Computed | RFC3339 expiration timestamp |
  | `images.*.size_bytes` | Int64 | Computed | Total image size in bytes |
  | `images.*.tags` | List(String) | Computed | Sorted list of tags pointing to digest |

---

### 3.11 `data "namespace_builder_configuration"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.compute.v1beta.BuilderService` (`builderv1betagrpc.BuilderServiceClient`)
- **RPC Method:** `GetBuilderConfiguration(GetBuilderConfigurationRequest) returns (GetBuilderConfigurationResponse)`
- **Endpoint Class:** Regional Compute
- **Credential Class:** Tenant Bearer Token (`{"compute/builder", "read"}`)
- **Security Suppression:** Always sends `skip_builder_pre_spawn = true` and `return_new_credentials = false` to prevent accidental instance spawns and credential leakage.
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Identifier for configuration in state |
  | `platform` | String | Optional, Computed | Target architecture platform (`linux/amd64`, `linux/arm64`) |
  | `builder_name` | String | Optional, Computed | Specific remote builder target name |
  | `buildkit_endpoint` | String | Computed | BuildKit connection endpoint (host:port) |
  | `full_buildkit_endpoint` | String | Computed | Full BuildKit connection URI |
  | `server_ca_pem` | String | Computed | PEM-encoded server CA certificates |
  | `cache_size_mb` | Int64 | Computed | Builder cache size in megabytes |
  | `shape` | Single Nested | Computed | Builder instance compute shape |
  | `shape.virtual_cpu` | Int64 | Computed | Number of virtual CPUs |
  | `shape.memory_megabytes` | Int64 | Computed | RAM in megabytes |
  | `shape.machine_arch` | String | Computed | Architecture (`amd64`, `arm64`) |
  | `shape.os` | String | Computed | OS family (`linux`, `macos`) |

---

### 3.12 `data "namespace_builder_usage"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.compute.v1beta.BuilderService`
- **RPC Method:** `GetBuilderUsage(GetBuilderUsageRequest) returns (GetBuilderUsageResponse)`
- **Endpoint Class:** Regional Compute
- **Credential Class:** Tenant Bearer Token (`{"compute/builder", "read"}`)
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Usage query identifier in state |
  | `period_start` | String | Required | Start date in `YYYY-MM-DD` format |
  | `period_end` | String | Optional, Computed | End date in `YYYY-MM-DD` format (defaults to current date) |
  | `total` | Single Nested | Computed | Finalized total builder usage |
  | `total.build_count` | Int64 | Computed | Total builds executed |
  | `total.build_wall_minutes` | Int64 | Computed | Total elapsed wall minutes |
  | `total.build_unit_minutes` | Int64 | Computed | Total compute unit minutes |
  | `pending_total` | Single Nested | Computed | Pending builder usage |
  | `pending_total.build_count` | Int64 | Computed | Pending build count |
  | `pending_total.build_wall_minutes` | Int64 | Computed | Pending elapsed wall minutes |
  | `pending_total.build_unit_minutes` | Int64 | Computed | Pending compute unit minutes |
  | `per_day` | List Nested | Computed | Daily breakdown sorted chronologically |
  | `per_day.*.date` | String | Computed | Date string `YYYY-MM-DD` |
  | `per_day.*.build_count` | Int64 | Computed | Builds executed on date |
  | `per_day.*.build_wall_minutes` | Int64 | Computed | Wall minutes on date |
  | `per_day.*.build_unit_minutes` | Int64 | Computed | Compute unit minutes on date |
  | `per_day.*.pending_build_count` | Int64 | Computed | Pending builds on date |
  | `per_day.*.pending_build_wall_minutes` | Int64 | Computed | Pending wall minutes on date |
  | `per_day.*.pending_build_unit_minutes` | Int64 | Computed | Pending compute unit minutes on date |

---

### 3.13 `data "namespace_network_configuration"`
- **Milestone:** Phase 4
- **gRPC Service:** `namespace.cloud.compute.v1beta.ComputeService` (`computev1betagrpc.ComputeServiceClient`)
- **RPC Method:** `GetNetworkConfiguration(GetNetworkConfigurationRequest) returns (GetNetworkConfigurationResponse)`
- **Endpoint Class:** Regional Compute
- **Credential Class:** Tenant Bearer Token (`{"compute/network", "read"}`)
- **Schema Attributes:**
  | Attribute | Type | Nature | Modifiers / Validators |
  |---|---|---|---|
  | `id` | String | Computed | Fixed identifier (`network`) |
  | `egress_cidrs` | List Nested | Computed | Effective workspace source CIDR blocks sorted by `cidr` ascending, then `segment` ascending |
  | `egress_cidrs.*.cidr` | String | Computed | Egress IP CIDR block |
  | `egress_cidrs.*.kind` | String | Computed | Allocation kind (`NAMESPACE_MANAGED`, `EXTERNAL_TRANSIT_PROVIDER`, `DEDICATED`, `KIND_UNKNOWN`) |
  | `egress_cidrs.*.segment` | String | Computed | Dedicated network segment identifier |
