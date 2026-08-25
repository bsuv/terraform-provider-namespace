# RPC Retry, Timeout, and Idempotency Policy

## 1. Operation Deadline & Retry Matrix

| Operation Class | Operations / Methods | Operation Deadline | Per-Attempt Timeout | Max Attempts | Retryable Error Codes | Backoff Strategy |
|---|---|---:|---:|---:|---|---|
| **Unpaginated Idempotent Read** | `ProfileService.ListProfiles`, `ImageService.ListBaseImages`, `TenantService.DescribeTenantGroup`, `TenantMembershipService.ListTenantMembers`, `ContainerRegistryService.GetDefaultPolicy`, `GetRepositoryPolicy`, `GetImage`, `ListSharedImages`, `StorageService.GetVolumeTagSummary`, `DescribePersistentVolume`, `BuilderService.GetBuilderConfiguration`, `GetBuilderUsage`, `ComputeService.GetNetworkConfiguration`, `VaultService.ListObjects` | 30s | 15s | 4 | `codes.Unavailable`, `codes.ResourceExhausted` (with `RetryInfo`), locally timed-out `codes.DeadlineExceeded` | Exponential with full jitter (initial ceiling 250ms, doubling to 5s max) |
| **Paginated Read** | `ContainerRegistryService.ListImages` | 2m (overall) | 15s (per page) | 4 (per page) | Same as above | Same as above; repeating cursor fails immediately |
| **Mutating CRUD** | `CreateProfile`, `UpdateProfile`, `DeleteProfile`, `EnsureTenantGroup`, `DeleteTenantGroup`, `ShareImage`, `UnshareImage`, `Update*Policy` | 60s | 60s | 1 | None (Fail-fast, zero automatic retry) | N/A |
| **IAM Ephemeral Credential Issuance** | `IssueTenantToken`, `IssueIdToken`, `IssueTenantClientCertificate` | 30s | 30s | 1 | None (Fail-fast, zero automatic retry) | N/A |
| **Cache Integration Provisioning** | `EnsureBazelCache`, `EnsureGradleCache`, `EnsureHttpCache` | 60s | 60s | 1 | None (Fail-fast, zero automatic retry) | N/A |

---

## 2. Retry Decision Engine

1. **Idempotency Classification**:
   - Only read-only operations whose RPC semantics are demonstrably idempotent are eligible for retries.
   - Mutating calls (`Create`, `Update`, `Delete`, `Ensure`, `Share`, `Unshare`) must **NEVER** be automatically retried by the provider client layer to avoid duplicate creations, conflicting version increments, or race conditions.
2. **Retryable Status Codes**:
   - `codes.Unavailable`: Network blip or backend restart -> retryable with jitter.
   - `codes.ResourceExhausted`: Retryable **only** when `google.rpc.RetryInfo` is attached to status details; honors server-requested `retry_delay`.
   - `codes.DeadlineExceeded`: Retryable only if attempt timed out while the overall operation deadline still has remaining budget.
3. **Non-Retryable Status Codes**:
   - `codes.InvalidArgument` (invalid payload format/range) -> Abort immediately.
   - `codes.NotFound` (target object missing) -> Abort immediately (handled by caller).
   - `codes.AlreadyExists` (unique constraint conflict) -> Abort immediately.
   - `codes.PermissionDenied` / `codes.Unauthenticated` -> Abort immediately.
   - `codes.FailedPrecondition` (revision / optimistic locking mismatch) -> Abort immediately.
4. **Backoff Calculation**:
   $$\text{Delay} = \text{random}(0, \min(250\text{ms} \times 2^{\text{attempt}-1}, 5\text{s}))$$
5. **Cancellation & Context**:
   - Client context cancellation (e.g. `SIGINT` / `Ctrl+C` from Terraform CLI) immediately terminates retry loops without sleeping.

---

## 3. Terraform timeout decision

The first release intentionally exposes no user-configurable resource `timeouts` blocks. Every retained operation is a bounded unary RPC governed by the deadlines above, and runner-image rebuild—the known long-running operation—is outside the provider CRUD surface. If protected live evidence shows that a retained operation cannot reliably complete within these limits, its contract and implementation must be updated with an operation-specific Terraform timeout before release; the provider must not silently widen these deadlines.
