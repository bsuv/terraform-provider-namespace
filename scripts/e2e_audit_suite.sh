#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WITH_UNIT=0
VERBOSE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --with-unit)
      WITH_UNIT=1
      shift
      ;;
    -v|--verbose)
      VERBOSE=1
      shift
      ;;
    -h|--help)
      echo "Usage: $0 [--with-unit] [-v|--verbose]"
      exit 0
      ;;
    *)
      echo "Error: unknown argument '$1'." >&2
      exit 1
      ;;
  esac
done

require_command() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "Error: required command '${cmd}' not found in PATH." >&2
    exit 1
  fi
}

require_command go
require_command terraform
require_command gitleaks
require_command shellcheck
require_command python3

cd "${REPO_ROOT}"

if [[ "${WITH_UNIT}" -eq 1 ]]; then
  require_command gofmt
  unset TF_ACC

  echo "==> Checking gofmt formatting..."
  unformatted="$(gofmt -l .)"
  if [[ -n "${unformatted}" ]]; then
    echo "Error: unformatted Go files detected:" >&2
    echo "${unformatted}" >&2
    exit 1
  fi

  echo "==> Running go vet (default and e2e tags)..."
  go vet ./...
  go vet -tags=e2e ./...

  echo "==> Running unit/contract suite with -race..."
  go test -race -count=1 ./...

  echo "==> Running secret scan..."
  bash ./scripts/secret-scan.sh

  echo "==> Running acceptance report adversarial tests..."
  bash ./scripts/test_report_adversarial.sh
fi

echo "==> Running offline E2E audit suite (-tags=e2e)..."
unset TF_ACC
test_args=(-race -count=1 -tags=e2e ./internal/provider -run '^TestE2E_')
if [[ "${VERBOSE}" -eq 1 ]]; then
  test_args=(-v "${test_args[@]}")
fi
go test "${test_args[@]}"

echo "==> E2E audit suite passed."
