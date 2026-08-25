#!/usr/bin/env bash

set -euo pipefail

EXPECTED_GITLEAKS_VERSION="8.30.0"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GITLEAKS_CONFIG="${REPO_ROOT}/.gitleaks.toml"
GITLEAKS_IGNORE="${REPO_ROOT}/.gitleaksignore"
HISTORY_LOG_OPTS="HEAD"
HISTORY_LABEL="HEAD history"
failed=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all-refs)
      HISTORY_LOG_OPTS="--all"
      HISTORY_LABEL="all local refs"
      shift
      ;;
    -h|--help)
      echo "Usage: $0 [--all-refs]"
      exit 0
      ;;
    *)
      echo "Error: unknown argument '$1'." >&2
      exit 1
      ;;
  esac
done

require_command() {
  local command_name="$1"

  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Error: required secret-scanning command '${command_name}' was not found in PATH." >&2
    exit 1
  fi
}

require_command gitleaks
require_command grep

installed_gitleaks_version="$(gitleaks version | tr -d '[:space:]')"
if [[ "${installed_gitleaks_version}" != "${EXPECTED_GITLEAKS_VERSION}" ]]; then
  echo "Error: Gitleaks ${EXPECTED_GITLEAKS_VERSION} is required; found '${installed_gitleaks_version:-unknown}'." >&2
  echo "Install the pinned binary with scripts/install-gitleaks.sh before running this gate." >&2
  exit 1
fi

# Prove that the configured default rules are active before trusting a clean
# result. Keep the probe label and value on separate source lines so this file
# is not itself a finding. Gitleaks returns 1 when the probe is detected.
probe_label='api_key'
probe_value='v7pQ9rS2tU4wX6yZ8aB1cD3eF5gH7jK9m'
set +e
printf '%s = "%s"\n' "${probe_label}" "${probe_value}" |
  gitleaks stdin --config "${GITLEAKS_CONFIG}" --gitleaks-ignore-path "${GITLEAKS_IGNORE}" --no-banner --redact=100 >/dev/null 2>&1
canary_status=$?
set -e
if [[ "${canary_status}" -ne 1 ]]; then
  echo "Error: Gitleaks canary was not detected (exit ${canary_status}); refusing a potentially empty or broken scan." >&2
  exit 1
fi

# Scan the complete ancestry of the checked-out revision by default. Release
# preflight uses --all-refs after fetching every GitHub-visible ref.
echo "Running Gitleaks against ${HISTORY_LABEL}..."
if ! gitleaks git --config "${GITLEAKS_CONFIG}" --gitleaks-ignore-path "${GITLEAKS_IGNORE}" --log-opts="${HISTORY_LOG_OPTS}" --no-banner --redact=100 --verbose "${REPO_ROOT}"; then
  echo "Gitleaks failed or detected a secret in ${HISTORY_LABEL}." >&2
  failed=1
fi

# History scans do not cover uncommitted and untracked files, so scan the
# release tree independently as a fail-closed working-directory gate.
echo "Running Gitleaks against the working directory..."
if ! gitleaks dir --config "${GITLEAKS_CONFIG}" --gitleaks-ignore-path "${GITLEAKS_IGNORE}" --no-banner --redact=100 --verbose "${REPO_ROOT}"; then
  echo "Gitleaks failed or detected a secret in the working directory." >&2
  failed=1
fi

scan_working_tree() {
  local scan_name="$1"
  local finding_message="$2"
  local scan_status=0
  shift 2

  # System grep is present on the supported GitHub runner and developer
  # platforms. Treat grep errors separately from a clean no-match result so a
  # broken defense-in-depth scan cannot silently pass.
  LC_ALL=C grep -rInHE \
    --exclude-dir=.git \
    --exclude-dir=.agents \
    "$@" \
    "${REPO_ROOT}" || scan_status=$?

  case "${scan_status}" in
    0)
      echo "${finding_message}" >&2
      failed=1
      ;;
    1)
      ;;
    *)
      echo "Error: ${scan_name} failed with exit ${scan_status}; refusing a potentially incomplete scan." >&2
      exit 1
      ;;
  esac
}

# Defense-in-depth token and JWT patterns across the entire working tree.
scan_working_tree \
  'token and JWT scan' \
  'Potential live token or JWT credential found.' \
  -e 'ghp_[A-Za-z0-9_]{36}' \
  -e 'gho_[A-Za-z0-9_]{36}' \
  -e 'xoxb-[0-9]{11,12}-[0-9]{11,12}-[A-Za-z0-9]{24}' \
  -e '(nsct|nscw|nscu|tok|st)_eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}' \
  -e '(^|[^A-Za-z0-9_-])eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}([^A-Za-z0-9_-]|$)'

# Static private-key fixtures are forbidden even in tests. Tests synthesize PEM
# framing at runtime so complete key blocks never enter source or Git history.
scan_working_tree \
  'private-key scan' \
  'Potential static private key block found in codebase.' \
  -e '-----BEGIN ([A-Za-z0-9_-]+ )*PRIVATE KEY-----'

if [[ "${failed}" -ne 0 ]]; then
  exit 1
fi

echo 'Secret scan passed.'
