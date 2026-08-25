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
- Use environment variables (`NSC_TOKEN`), token files (`NSC_TOKEN_FILE`), or Terraform 1.11 ephemeral sensitive input variables (`ephemeral = true`, `sensitive = true`).
- **Automated Provider Redaction**: The provider automatically redacts authorization headers, bearer tokens (`nsct_...`, `nscw_...`), JWTs, private keys (RSA, EC, OpenSSH, PKCS#8), client certificates, passwords/passphrases, and opaque values stored under credential-like structured fields. Redaction covers ordinary and JSON-escaped provider errors, diagnostics, gRPC interceptor logs, and generated acceptance-test reports.
- **Operator Redaction Boundaries**: Operators remain responsible for protecting data outside the provider runtime boundary, including CI/CD runner terminal output buffers, unredacted Terraform CLI output blocks (`terraform output`), local `.tfvars` files, OS process environment dumps, and external CLI execution wrappers.
