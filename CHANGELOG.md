# Changelog

All notable changes to the Namespace Terraform Provider will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### Managed Resources
- `namespace_github_runner_profile`: Manage Namespace Cloud GitHub Actions runner profiles with custom machine shapes, OS images, container caching, and lifecycle policies.
- `namespace_tenant_group`: Manage IAM tenant groups and access policies with collision protection.
- `namespace_registry_shared_image`: Share container registry images across tenants with deterministic replace-on-change semantics.
- `namespace_registry_default_policy`: Manage default expiration and retention policies for container repositories with documented neutral reset on destroy.
- `namespace_registry_repository_policy`: Manage per-repository policy overrides with automatic inheritance fallback on destroy.

#### Ephemeral Resources (Terraform 1.11+)
- `namespace_tenant_token`: Issue short-lived bearer tokens scoped to target tenants and IAM policies without persisting secrets to state.
- `namespace_id_token`: Issue short-lived OIDC ID tokens for federated authentication across cloud providers (AWS, GCP, Vault).
- `namespace_client_certificate`: Issue short-lived mTLS client certificates and private keys.
- `namespace_bazel_cache`: Request expiring Bazel remote build cache endpoints and mTLS credentials.
- `namespace_gradle_cache`: Request expiring Gradle build cache endpoints and basic auth credentials.
- `namespace_http_cache`: Request expiring generic HTTP cache endpoints and basic auth credentials.

#### Data Sources
- `namespace_github_runner_profile`: Query a single runner profile by profile ID or tag.
- `namespace_github_runner_profiles`: List all configured runner profiles with deterministic sorting.
- `namespace_github_base_images`: Query available base OS images and compatibility metadata.
- `namespace_tenant_group`: Query IAM tenant group configuration and enrolled tenant IDs.
- `namespace_tenant_members`: List tenant members and assigned roles.
- `namespace_cache_volume_summary`: Query storage volume metrics and cache utilization.
- `namespace_persistent_volume`: Look up persistent volumes by ID or tag with regional scoping.
- `namespace_builder_configuration`: Query remote build cluster configuration and concurrency without exposing credentials.
- `namespace_builder_usage`: Query build compute utilization metrics over defined date ranges.
- `namespace_network_configuration`: Query effective regional network routing and CIDR configurations.
- `namespace_registry_image`: Look up container images by repository and tag/digest reference.
- `namespace_registry_images`: List container images in a repository with paginated traversal.
- `namespace_vault_secret_metadata`: Query Vault secret metadata without exposing secret values.

#### Provider Features
- Provider Protocol 6.0 compatibility.
- Multi-endpoint routing: independently configurable `global_endpoint`, `iam_endpoint`, and `compute_endpoint`.
- Terraform 1.11 ephemeral sensitive token variable pattern support.
- Comprehensive log redaction for authorization headers, bearer tokens, private keys, and passwords.
