# Phase 0 Live Evidence Contract

This file defines how live API evidence is produced and evaluated. It deliberately does **not** contain copied console output, sample run identifiers, hard-coded fixture IDs, or an evergreen claim that a past checkout was verified.

## Authoritative evidence

The authoritative Phase 0 evidence for a release candidate is the `acceptance-report.md`, `junit.xml`, and `sanitized-acceptance.log` artifact produced by the protected acceptance job for that exact Git commit. The release workflow cannot publish until that job and the report generator both pass.

A passing report is valid only when it contains:

- the full 40- or 64-character Git object ID checked out by the workflow;
- the numeric GitHub Actions run ID;
- the exact `https://github.com/bsuv/terraform-provider-namespace/actions/runs/<run-id>` URL;
- the Terraform version used by the live job;
- all required critical RPC observations with exact RPC, endpoint, status, scope, and assertion contracts;
- an explicit `full` suite classification so a truncated release run cannot be accepted as a split matrix or singleton run;
- a passing structured live observation for every retained first-release resource, ephemeral resource, and data source, including the custom-runner-image input-combination spike;
- authorized security, tenant-isolation, and complete ephemeral non-persistence evidence;
- zero individual-test or package-level failures.

`scripts/generate-acceptance-report.sh` enforces those conditions and exits non-zero when any are absent. Offline unit, mock-server, documentation, or example-validation results are useful quality evidence, but they are not substitutes for this credentialed artifact.

## Expected routing contract

The live job verifies the following implementation contract; this table records expectations, not an assertion that a local checkout has contacted the services.

| Endpoint class | Default | Services |
|---|---|---|
| Global | `https://global.namespaceapis.com:443` | GitHub profiles/images, container registry, Vault metadata, Bazel/Gradle/HTTP cache services |
| IAM | `https://iam.namespaceapis.com:443` | Tenant, membership, tenant-token, ID-token, session-token, and client-certificate services |
| Regional compute | Explicit `compute_endpoint` or regional Namespace endpoint | Compute, storage, and builder services |

One provider configuration is bound to one regional compute endpoint. Multiple regions require provider aliases.

## Credential and fixture requirements

The protected live environment supplies only dedicated test-tenant credentials and deterministic fixtures:

- `NSC_TOKEN` or `NSC_TOKEN_FILE`, plus `NSC_TEST_TENANT`;
- `NSC_PARTNER_TOKEN` and `NSC_TRUSTED_JWT`, whose tenant claims must match `NSC_TEST_TENANT`;
- `NSC_TEST_IMAGE_REPO` and `NSC_TEST_IMAGE_DIGEST`, which are read back as an exact image identity;
- `NSC_TEST_PAGINATED_IMAGE_REPO`, preflighted to contain more than 100 images and at least two non-empty cursor transitions;
- `NSC_TEST_CACHE_VOLUME_TAG`, `NSC_TEST_PV_TAG`, and `NSC_TEST_VAULT_SECRET_ID` where their data-source suites require them.

No acceptance helper may select an arbitrary tenant image or volume. Mutable fixtures use the guarded `tfacc-<run-id>-` prefix, and cleanup must validate both that prefix and the dedicated test tenant or repository before deletion or reset. Unscoped sweeping is forbidden.

## Safe execution and retention

Raw `go test -json` output is redirected to runner-temporary storage; it is never streamed through `tee`, printed, or uploaded. Only generator-sanitized artifacts are uploaded. The report generator itself is covered by adversarial tests for secret redaction, wrong RPC/endpoint/status/scope, misplaced assertions, partial suites, package-level failure, invalid provenance, and stdin operation.

The acceptance workflow retains the sanitized artifacts for the configured retention period. Release operators must retain or link the passing release-run artifact with the release record. A local checkout without that external artifact must be described as **not independently live-verified**, never as a passing Phase 0 run.
