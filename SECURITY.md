# Security Policy

## Reporting Security Vulnerabilities

The maintainer takes the security of this provider and its users seriously. If you discover a security vulnerability, report it privately rather than opening a public issue.

- **Preferred channel**: [GitHub private vulnerability reporting](https://github.com/bsuv/terraform-provider-namespace/security/advisories/new). Private vulnerability reporting must be enabled before this repository is made public and must remain enabled while the provider is supported.
- **Fallback**: If private vulnerability reporting is unavailable, email [bsuv@pm.me](mailto:bsuv@pm.me) with a non-sensitive request to establish a secure reporting channel. Do not include exploit details, credentials, or other sensitive material until a private channel is confirmed, and do not disclose the issue publicly.
- **Response timeline**: Reports are acknowledged and triaged as soon as practical.

Please provide sufficient detail to reproduce the issue, including:
- Provider version
- Terraform CLI version
- Minimal reproducible configuration (redacting actual tokens, keys, and tenant identifiers)
- Potential impact and attack vectors

---

## Security Model & State Handling

### Ephemeral Credentials
This provider leverages Terraform 1.11+ **Ephemeral Resources** for all short-lived credentials, including:
- IAM Tenant Tokens (`namespace_tenant_token`)
- OIDC Identity Tokens (`namespace_id_token`)
- Client TLS Certificates & Private Keys (`namespace_client_certificate`)
- Cache credentials (`namespace_bazel_cache`, `namespace_gradle_cache`, `namespace_http_cache`)

**Key Guarantee**: Ephemeral resources are valid only during the execution of a Terraform plan or apply operation. Their values are **never stored** in Terraform state files (`terraform.tfstate`) or saved plan JSON artifacts.

### State Backend Protection
While ephemeral resources prevent credentials from entering state, managed resources and data sources contain operational metadata and resource identifiers. Operators are responsible for:
1. **Remote Backend Encryption & KMS**: Always use remote state backends supporting customer-managed encryption at rest (e.g., AWS S3 with KMS CMKs, Google Cloud Storage with Cloud KMS CMEK, Azure Blob Storage with customer-managed keys, Terraform Cloud).
2. **In-Transit Encryption**: Ensure all communication with state backends uses TLS 1.3 or TLS 1.2.
3. **Access Control & Least Privilege**: Apply strict least-privilege IAM role-based access policies and conditional access (e.g., MFA, IP filtering) restricting read/write access to state buckets.
4. **Access Audit Logging**: Enable comprehensive access audit logging (e.g., AWS CloudTrail data events, S3 server access logs, GCP Storage audit logs) on state storage buckets to track all state read, write, and list operations.
5. **Backup, Versioning & Immutability**: Enable object versioning and automated backup policies on state storage buckets to defend against accidental deletion, state corruption, and ransomware. Apply object lock / WORM policies where compliance mandates exist.
6. **Retention & Disposal Policies**: Establish strict data retention rules and automated lifecycle policies for state snapshots and historical versions to balance audit needs with data minimization principles.
7. **State Locking**: Enable distributed state locking (e.g., DynamoDB for S3, native locking in GCS / Terraform Cloud) to prevent concurrent mutating modifications and race conditions.
8. **No Local State on Shared Hosts**: Avoid using local state files on shared workstations or CI runners where unredacted variables or state snapshots could be exposed.

### Credential Handling & Redaction Boundaries
- Never commit literal tokens into Terraform HCL files (`.tf`).
- Use environment variables (`NSC_TOKEN`, `NSC_TOKEN_SPEC`), token files (`NSC_TOKEN_FILE`, `NSC_TOKEN_SPEC_FILE`), or Terraform 1.11 ephemeral sensitive input variables (`ephemeral = true`, `sensitive = true`).
- **Automated Provider Redaction**: The provider automatically redacts authorization headers, bearer tokens (`nsct_...`, `nscw_...`, `nscu_...`, `nsrt_...`, `tok_...`, `st_...`), JWTs, private keys (RSA, EC, OpenSSH, PKCS#8), client certificates, passwords/passphrases, and opaque values stored under credential-like structured fields. Redaction covers ordinary and JSON-escaped provider errors, shared `internal/tfutil` diagnostics, custom `NotFound` data-source handlers, gRPC interceptor logs, and generated acceptance-test reports.
- **Operator Redaction Boundaries**: Operators remain responsible for protecting data outside the provider runtime boundary, including CI/CD runner terminal output buffers, unredacted Terraform CLI output blocks (`terraform output`), local `.tfvars` files, OS process environment dumps, and external CLI execution wrappers.

---

## Transport, Endpoint & Token File Hardening

### Endpoint & Proxy Trust Boundaries
- Custom endpoint overrides (`global_endpoint`, `iam_endpoint`, `compute_endpoint`, and environment variables `NSC_GLOBAL_ENDPOINT`, `NSC_IAM_ENDPOINT`, `NSC_ENDPOINT`) are **security-sensitive trust boundaries**.
- The provider transmits `Authorization: Bearer <token>` credentials and session-exchange requests over TLS to the resolved endpoint host. Never point provider endpoints at untrusted third-party servers or untrusted environment overrides.
- Trusted Namespace domains are `namespaceapis.com` (`*.namespaceapis.com`), `namespaceapi.com` (`*.namespaceapi.com`), `namespace.so` (`*.namespace.so`), and `namespacelabs.dev` (`*.namespacelabs.dev`). Loopback addresses (`localhost`, `127.0.0.1`, `::1`) are permitted for local test harnesses.
- When any explicitly configured or environment-resolved endpoint resolves outside trusted Namespace domains and loopback addresses, the provider emits a **non-blocking configuration warning** (`Warning`) alerting the operator that credentials will be sent to a custom host.

### Metadata SSRF & Malformed Endpoint Protections
- Endpoint validation enforces `https://` (or bare `host[:port]` dialed over TLS) and rejects `http://` or non-HTTPS schemes, userinfo/credentials (`user:pass@`), URL paths, query strings, and fragments.
- Raw endpoint strings containing ASCII control characters (`< 0x20`, `0x7f`) or embedded whitespace are rejected.
- To mitigate Server-Side Request Forgery (SSRF) against cloud instance metadata services, resolver scheme confusion, and ambiguous bind targets, endpoint validation and dial-time IP/proxy controls reject:
  - Reserved gRPC resolver scheme names (`unix`, `unix-abstract`, `passthrough`, `dns`, `xds`, `google-c2p`) and enforce explicit `dns:///` target URIs for direct gRPC connections
  - Cloud metadata hostnames (`metadata`, `metadata.google.internal`, `metadata.goog`, `instance-data`, `instance-data.ec2.internal`, `metadata.azure.internal`, `metadata.nic.internal`) and non-canonical numeric IPv4 literals
  - Unspecified IP addresses (`0.0.0.0`, `::`, and IPv4-mapped/compatible/NAT64 forms)
  - IPv4 link-local and cloud metadata addresses (`169.254.0.0/16`, including `169.254.169.254`, `100.100.100.200`, and IPv4-mapped/compatible/NAT64 forms)
  - IPv6 link-local addresses (`fe80::/10`, including zoned addresses)
  - AWS IPv6 EC2 instance metadata (`fd00:ec2::254`)
  - Resolved metadata/link-local/unspecified IPs at TCP dial time (`net.Dialer.Control`) for both direct connections and `HTTPS_PROXY`/`HTTP_PROXY` tunnels
- Loopback hosts (`localhost`, `127.0.0.1`, `::1`), private test/enterprise proxy hosts, and valid custom domain hostnames remain allowed.

### TLS 1.2+ Floor & Per-Connection Transport Security
- All default gRPC connections (Global, IAM, Compute, and session-token exchange clients) explicitly require `tls.VersionTLS12` (`MinVersion: tls.VersionTLS12`) via a shared TLS configuration helper rather than relying on mutable runtime defaults.
- Per-RPC Bearer credential enforcement (`RequireTransportSecurity()`) is scoped per connection rather than via a process-wide exported toggle: TLS connections always require transport security, and insecure transport is permitted only when an in-memory test harness explicitly injects `insecure.NewCredentials()` alongside `client.RequireTransportSecurity = false`.

### Token File Permissions & Size Caps
- Token files (`token_file`, `NSC_TOKEN_FILE`, default credential discovery paths, and acceptance-test tenant checks) are opened with `os.Open`, inspected via `Stat()`, and read through `io.LimitReader` with a strict **1 MiB (`1 << 20` bytes)** cap. Files exceeding 1 MiB fail closed with an explicit size-limit error.
- Inline token strings (`nsct_`, `st_`, `nscw_`, `nscu_`, `nsrt_`, `tok_`, and 3-part `eyJ...` JWTs) are matched before filesystem inspection so raw tokens never probe `os.Stat`.
- Token files on Unix systems should be restricted to owner-only permissions (`0600` or `0400`). If an active file-backed token source has group or world permission bits set (`mode & 0o077 != 0`), the provider emits a **non-blocking configuration warning** recommending `chmod 600`.
