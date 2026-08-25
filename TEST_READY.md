# Release Verification Contract

This file is a runbook, not an evergreen “ready” attestation. A release candidate is ready only when the protected workflow for its exact tag and commit satisfies every gate below.

## Offline gates

- Go 1.26.7: `go test -count=1 ./...` and `go test -count=1 -race ./...`.
- Static analysis: `go vet ./...`, `golangci-lint run ./...`, `gofmt -l .`, `go mod tidy -diff` / `go mod verify`, and fail-closed secret scanning over every GitHub-visible ref.
- Terraform: `terraform fmt -check -recursive .` plus clean-install/example validation on Terraform 1.11 and the current supported latest release.
- Documentation: deterministic `tfplugindocs generate --provider-name namespace --rendered-provider-name "Namespace Cloud"`, no generated diff, and `tfplugindocs validate --provider-name namespace`.
- Reporting: `scripts/test_report_adversarial.sh` must pass all fail-closed evidence-contract cases.
- Packaging: `goreleaser check` and the build-mode clean-install smoke test.

## Protected gates

- The credentialed `TestAcc*` suite passes against the dedicated tenant with deterministic fixtures, partner credentials, and trusted-authority credentials.
- The report generator accepts all required RPC, endpoint, status, scope, assertion, package-result, and immutable-provenance contracts.
- Singleton registry reset behavior passes in the serialized acceptance stage.
- The tag resolves to the exact tested commit.
- GoReleaser 2.17.1 produces the configured archives, manifest, checksums, and a Registry-compatible RSA/DSA signature; CI smoke-tests the exact unsigned snapshot assets, and the release workflow verifies the signed staged distribution before draft promotion.

The authoritative evidence locations and retention rules are defined in `internal/apicontract/live_report.md` and `docs/release_checklist.md`. Fixed test counts are intentionally omitted because they become stale and do not prove coverage or release readiness.
