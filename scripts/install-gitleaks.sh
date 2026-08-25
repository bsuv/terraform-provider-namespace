#!/usr/bin/env bash

set -euo pipefail

GITLEAKS_VERSION="8.30.0"
GITLEAKS_LINUX_X64_SHA256="79a3ab579b53f71efd634f3aaf7e04a0fa0cf206b7ed434638d1547a2470a66e"

if [[ $# -gt 1 ]]; then
  echo "Usage: $0 [install-directory]" >&2
  exit 1
fi

install_dir="${1:-${RUNNER_TEMP:-/tmp}/gitleaks-bin}"
if [[ -z "${install_dir}" ]]; then
  echo "Error: install directory cannot be empty." >&2
  exit 1
fi

if [[ "$(uname -s)" != "Linux" ]] || [[ "$(uname -m)" != "x86_64" ]]; then
  echo "Error: this CI installer supports only Linux x86_64; install Gitleaks ${GITLEAKS_VERSION} directly on this platform." >&2
  exit 1
fi

for required_command in curl sha256sum tar install; do
  if ! command -v "${required_command}" >/dev/null 2>&1; then
    echo "Error: required installer command '${required_command}' was not found in PATH." >&2
    exit 1
  fi
done

archive_name="gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz"
download_url="https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/${archive_name}"
temp_dir="$(mktemp -d)"
trap 'rm -rf "${temp_dir}"' EXIT
archive_path="${temp_dir}/${archive_name}"

echo "Downloading checksum-pinned Gitleaks ${GITLEAKS_VERSION}..."
curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location --retry 3 \
  --output "${archive_path}" "${download_url}"

if ! printf '%s  %s\n' "${GITLEAKS_LINUX_X64_SHA256}" "${archive_path}" | sha256sum --check --status; then
  echo "Error: checksum verification failed for ${archive_name}." >&2
  exit 1
fi

tar -xzf "${archive_path}" -C "${temp_dir}" gitleaks
mkdir -p "${install_dir}"
install -m 0755 "${temp_dir}/gitleaks" "${install_dir}/gitleaks"

installed_version="$("${install_dir}/gitleaks" version | tr -d '[:space:]')"
if [[ "${installed_version}" != "${GITLEAKS_VERSION}" ]]; then
  echo "Error: installed Gitleaks version '${installed_version:-unknown}' does not match ${GITLEAKS_VERSION}." >&2
  exit 1
fi

echo "Installed Gitleaks ${GITLEAKS_VERSION} at ${install_dir}/gitleaks"
