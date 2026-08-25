# Namespace Cloud Terraform Provider

[![Tests & Lint](https://github.com/bsuv/terraform-provider-namespace/actions/workflows/test.yml/badge.svg)](https://github.com/bsuv/terraform-provider-namespace/actions/workflows/test.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

The **Namespace Cloud Terraform Provider** manages Namespace Cloud runner profiles, IAM tenant groups, registry configuration, and shared images; it also exposes read-only service metadata and short-lived credentials. Cache ephemerals may ensure durable server-side cache infrastructure, but Terraform does not own or delete that infrastructure.

> [!IMPORTANT]
> This is a community-maintained provider published by `bsuv`. It is not an official Namespace Labs product and is not currently affiliated with or endorsed by Namespace Labs.

---

## Requirements

- [Terraform](https://www.terraform.io/downloads.html) `>= 1.11.0` (required for Ephemeral Resources and sensitive variable contracts)
- [Go](https://go.dev/doc/install) `>= 1.26.7` (for building the provider from source)

---

## Installation

Declare the provider in your Terraform configuration:

```hcl
terraform {
  required_version = ">= 1.11.0"
  required_providers {
    namespace = {
      source  = "bsuv/namespace"
      version = "~> 0.1"
    }
  }
}
```

---

## Authentication & Configuration

The Namespace provider resolves credentials through a deterministic precedence chain:

1. **Explicit Token**: `token` provider attribute
2. **Explicit Token File**: `token_file` provider attribute
3. **Environment Variable**: `NSC_TOKEN`
4. **Environment File**: `NSC_TOKEN_FILE`
5. **Provider-local Default Discovery**: `/var/run/nsc/token.json`, then `ns/token.json` beneath `os.UserConfigDir()`

Token files are re-read by the provider-local `LocalTokenSource`. Bearer tokens are used directly; session tokens are exchanged only against the configured `iam_endpoint`, cached in memory, and closed with the owning Namespace client.

### Recommended: Terraform 1.11 Ephemeral Sensitive Variable

To pass a token directly without recording it in Terraform plan or state JSON, declare the variable as both `sensitive` and `ephemeral`:

```hcl
variable "namespace_token" {
  type        = string
  description = "Namespace Cloud Bearer Token"
  sensitive   = true
  ephemeral   = true
}

provider "namespace" {
  token = var.namespace_token
}
```

### Multi-Region Compute Aliasing

Namespace Cloud separates global services (runner profiles, Registry, Vault metadata, and Caches), IAM services, and regional compute services. Each provider instance binds to a single regional compute endpoint. To query compute services across multiple regions, use provider aliases:

```hcl
# Default provider for global resources
provider "namespace" {
  token = var.namespace_token
}

# European compute region
provider "namespace" {
  alias            = "eu"
  token            = var.namespace_token
  compute_endpoint = "https://eu.compute.namespaceapis.com:443"
}

# US compute region
provider "namespace" {
  alias            = "us"
  token            = var.namespace_token
  compute_endpoint = "https://us.compute.namespaceapis.com:443"
}
```

---

## Supported Provider Surface

### Managed Resources
| Resource | Description |
|---|---|
| [`namespace_github_runner_profile`](docs/resources/github_runner_profile.md) | GitHub Actions self-hosted runner profile with custom vCPU/RAM shapes, OS images, and caching |
| [`namespace_tenant_group`](docs/resources/tenant_group.md) | IAM tenant group for federated access control and membership grouping |
| [`namespace_registry_shared_image`](docs/resources/registry_shared_image.md) | Container registry image sharing across tenant boundaries |
| [`namespace_registry_default_policy`](docs/resources/registry_default_policy.md) | Tenant-wide container image expiration and retention policy |
| [`namespace_registry_repository_policy`](docs/resources/registry_repository_policy.md) | Per-repository container image retention policy override |

### Ephemeral Resources (Terraform 1.11+)
| Resource | Description |
|---|---|
| [`namespace_tenant_token`](docs/ephemeral-resources/tenant_token.md) | Short-lived bearer token scoped to specific tenants and policies |
| [`namespace_id_token`](docs/ephemeral-resources/id_token.md) | OIDC ID token for cloud federation (AWS STS, GCP Workload Identity, HashiCorp Vault) |
| [`namespace_client_certificate`](docs/ephemeral-resources/client_certificate.md) | Short-lived mTLS client certificate and private key |
| [`namespace_bazel_cache`](docs/ephemeral-resources/bazel_cache.md) | Remote Bazel build cache endpoint and mTLS authentication |
| [`namespace_gradle_cache`](docs/ephemeral-resources/gradle_cache.md) | Remote Gradle build cache endpoint and basic auth credentials |
| [`namespace_http_cache`](docs/ephemeral-resources/http_cache.md) | Generic HTTP remote cache endpoint and basic auth credentials |

### Data Sources
| Data Source | Description |
|---|---|
| [`namespace_github_runner_profile`](docs/data-sources/github_runner_profile.md) | Look up a runner profile by ID or tag |
| [`namespace_github_runner_profiles`](docs/data-sources/github_runner_profiles.md) | List all configured runner profiles |
| [`namespace_github_base_images`](docs/data-sources/github_base_images.md) | Query available base OS images |
| [`namespace_tenant_group`](docs/data-sources/tenant_group.md) | Query IAM tenant group metadata and members |
| [`namespace_tenant_members`](docs/data-sources/tenant_members.md) | List members of the authenticated tenant |
| [`namespace_cache_volume_summary`](docs/data-sources/cache_volume_summary.md) | Query storage volume utilization and metrics |
| [`namespace_persistent_volume`](docs/data-sources/persistent_volume.md) | Describe persistent volumes by ID or tag |
| [`namespace_builder_configuration`](docs/data-sources/builder_configuration.md) | Query remote builder cluster capacity and endpoint |
| [`namespace_builder_usage`](docs/data-sources/builder_usage.md) | Query build compute usage statistics over time |
| [`namespace_network_configuration`](docs/data-sources/network_configuration.md) | Query regional network routing and subnet configurations |
| [`namespace_registry_image`](docs/data-sources/registry_image.md) | Get container image digest and metadata |
| [`namespace_registry_images`](docs/data-sources/registry_images.md) | List container images in a repository |
| [`namespace_vault_secret_metadata`](docs/data-sources/vault_secret_metadata.md) | Query Vault secret version and metadata (without disclosing secret values) |

---

## Security & State Responsibility

- **Ephemeral Credentials**: Short-lived tokens, keys, and cache passwords use Terraform Ephemeral Resources and are omitted from Terraform state and saved plan data. Operators must still protect HCL literals, variable files, environment variables, process memory, shell history, external consumers, and CI output.
- **State Backend Encryption**: Managed resource attributes (such as tags, shapes, and repository names) are stored in Terraform state. Always configure an encrypted remote backend (e.g. S3 + KMS, GCS + CMEK, Terraform Cloud) with strict access controls, audit logging, object versioning, and retention policies.
- **Provider Log Redaction**: Provider diagnostics and configured gRPC debug logs redact recognized authorization headers, bearer tokens, private keys, certificates, and passwords. This does not sanitize output produced outside the provider boundary.

See [SECURITY.md](SECURITY.md) for full security details and vulnerability reporting procedures.

---

## Development & Testing

### Running Unit & Contract Tests

```bash
go test -v -race -count=1 ./...
go vet ./...
```

### Running Acceptance Tests

Acceptance tests execute against live Namespace Cloud APIs. They require a dedicated test tenant and valid credentials:

```bash
export TF_ACC=1
export NSC_TOKEN="nsct_..."
go test -v -count=1 -timeout 120m ./internal/provider/... -run '^TestAcc'
```

The full suite also requires the dedicated-tenant guard, partner/trusted-authority credentials, and deterministic fixture variables documented in [TEST_READY.md](TEST_READY.md) and [docs/release_checklist.md](docs/release_checklist.md). A local run without the protected workflow's immutable provenance is not release evidence.

### Documentation Generation

Documentation is generated using `tfplugindocs`:

```bash
go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name namespace --rendered-provider-name "Namespace Cloud"
go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs validate --provider-name namespace
```

---

## License

This project is licensed under the [Apache License, Version 2.0](LICENSE).
