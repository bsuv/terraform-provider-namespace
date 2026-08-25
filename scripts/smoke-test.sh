#!/usr/bin/env bash
# ==============================================================================
# Namespace Terraform Provider — Clean Install Smoke Test
# Validates provider binary build / distribution archive unpack, checksums,
# manifest integrity, filesystem mirror installation, and schema validation
# against Terraform CLI and example configurations.
# ==============================================================================

set -euo pipefail

PROVIDER_NAME="namespace"
PROVIDER_NAMESPACE="bsuv"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

DIST_DIR="${DIST_DIR:-}"
ALLOW_UNSIGNED_SNAPSHOT=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dist-dir)
      if [[ $# -lt 2 ]]; then
        echo "Error: --dist-dir requires an argument" >&2
        exit 1
      fi
      DIST_DIR="$2"
      shift 2
      ;;
    --dist-dir=*)
      DIST_DIR="${1#*=}"
      shift 1
      ;;
    --allow-unsigned-snapshot)
      ALLOW_UNSIGNED_SNAPSHOT=true
      shift 1
      ;;
    -h|--help)
      echo "Usage: $0 [--dist-dir <path>] [--allow-unsigned-snapshot]"
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

if [[ "${ALLOW_UNSIGNED_SNAPSHOT}" == true ]] && [[ -z "${DIST_DIR}" ]]; then
  echo "Error: --allow-unsigned-snapshot requires --dist-dir." >&2
  exit 1
fi

# 1. Detect OS and ARCH
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  i386|i686) ARCH="386" ;;
  armv7l|armv6l) ARCH="arm" ;;
  *)
    echo "Unsupported architecture: ${ARCH}" >&2
    exit 1
    ;;
esac
PLATFORM="${OS}_${ARCH}"
echo "==> Target platform: ${PLATFORM}"

# 2. Prepare temporary directory
TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TEMP_DIR}"' EXIT

MIRROR_DIR="${TEMP_DIR}/mirror"

if [ -n "${DIST_DIR}" ]; then
  if [ ! -d "${DIST_DIR}" ]; then
    echo "Error: Distribution directory '${DIST_DIR}' does not exist" >&2
    exit 1
  fi
  DIST_DIR="$(cd "${DIST_DIR}" && pwd)"
  echo "==> Running Clean Install Smoke Test in Distribution Mode using ${DIST_DIR}..."

  # a. Discover version and find *SHA256SUMS
  SHA_FILE=""
  SHA_FILE_COUNT=0
  for candidate in "${DIST_DIR}"/*SHA256SUMS; do
    [ -f "${candidate}" ] || continue
    SHA_FILE="${candidate}"
    SHA_FILE_COUNT=$((SHA_FILE_COUNT + 1))
  done
  if [ "${SHA_FILE_COUNT}" -ne 1 ]; then
    echo "Error: Expected exactly one *SHA256SUMS file in ${DIST_DIR}, found ${SHA_FILE_COUNT}" >&2
    exit 1
  fi
  SHA_BASENAME="$(basename "${SHA_FILE}")"
  VERSION="${SHA_BASENAME#terraform-provider-"${PROVIDER_NAME}"_}"
  VERSION="${VERSION%_SHA256SUMS}"
  echo "    Discovered provider version: ${VERSION}"
  echo "    Checksums file: ${SHA_BASENAME}"

  MANIFEST_FILE="${DIST_DIR}/terraform-provider-${PROVIDER_NAME}_${VERSION}_manifest.json"
  if [[ "${ALLOW_UNSIGNED_SNAPSHOT}" == true ]] && [[ "${VERSION}" != *SNAPSHOT* ]]; then
    echo "Error: --allow-unsigned-snapshot is valid only for a GoReleaser SNAPSHOT version." >&2
    exit 1
  fi

  # b. Authenticate release checksums before trusting them. Unsigned input is
  # accepted only for an explicitly marked GoReleaser snapshot in CI.
  SIG_FILE="${SHA_FILE}.sig"
  if [ -f "${SIG_FILE}" ]; then
    if ! command -v gpg >/dev/null 2>&1; then
      echo "Error: 'gpg' command is required for distribution signature verification but not found in PATH" >&2
      exit 1
    fi

    echo "==> Verifying mandatory GPG signature for ${SHA_BASENAME}..."
    if ! gpg --batch --verify "${SIG_FILE}" "${SHA_FILE}"; then
      echo "Error: GPG signature verification failed for ${SIG_FILE} (signature invalid or public key missing)" >&2
      exit 1
    fi
    echo "    GPG signature verified successfully."
  elif [[ "${ALLOW_UNSIGNED_SNAPSHOT}" == true ]]; then
    echo "==> Warning: accepting unsigned GoReleaser snapshot for CI-only packaging validation." >&2
  else
    echo "Error: Required GPG signature file '${SHA_BASENAME}.sig' not found in ${DIST_DIR}" >&2
    exit 1
  fi

  # c. Verify authenticated checksums (or internally generated snapshot checksums).
  echo "==> Verifying SHA256 checksums..."
  (
    cd "${DIST_DIR}"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c "${SHA_BASENAME}"
    elif command -v shasum >/dev/null 2>&1; then
      shasum -a 256 -c "${SHA_BASENAME}"
    else
      echo "Error: Neither sha256sum nor shasum is available" >&2
      exit 1
    fi
  )

  # d. Verify manifest file presence and JSON validity
  echo "==> Verifying manifest file..."
  if [ ! -f "${MANIFEST_FILE}" ]; then
    echo "Error: Manifest file ${MANIFEST_FILE} not found in distribution directory" >&2
    exit 1
  fi
  if command -v jq >/dev/null 2>&1; then
    jq -e '.version == 1 and (.metadata.protocol_versions | index("6.0") != null)' "${MANIFEST_FILE}" >/dev/null || {
      echo "Error: Manifest ${MANIFEST_FILE} is invalid or missing protocol 6.0" >&2
      exit 1
    }
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json, sys; d=json.load(open(sys.argv[1])); sys.exit(0 if d.get("version")==1 and "6.0" in d.get("metadata",{}).get("protocol_versions",[]) else 1)' "${MANIFEST_FILE}" || {
      echo "Error: Manifest ${MANIFEST_FILE} is invalid or missing protocol 6.0" >&2
      exit 1
    }
  else
    if ! grep -q '"version"[[:space:]]*:[[:space:]]*1' "${MANIFEST_FILE}" || ! grep -q '"6.0"' "${MANIFEST_FILE}"; then
      echo "Error: Manifest ${MANIFEST_FILE} is invalid or missing protocol 6.0" >&2
      exit 1
    fi
  fi
  echo "    Manifest JSON and Protocol 6 metadata verified."

  # e. Unpack host platform zip archive
  ZIP_NAME="terraform-provider-${PROVIDER_NAME}_${VERSION}_${PLATFORM}.zip"
  ZIP_PATH="${DIST_DIR}/${ZIP_NAME}"
  if [ ! -f "${ZIP_PATH}" ]; then
    echo "Error: Release archive '${ZIP_NAME}' not found in ${DIST_DIR}" >&2
    exit 1
  fi
  echo "==> Unpacking release archive: ${ZIP_NAME}..."
  UNPACK_DIR="${TEMP_DIR}/unpacked"
  mkdir -p "${UNPACK_DIR}"
  unzip -q "${ZIP_PATH}" -d "${UNPACK_DIR}"

  # f. Validate binary filename, executable bit, manifest, license, and readme
  echo "==> Validating unpacked archive contents..."
  BINARY_NAME="terraform-provider-${PROVIDER_NAME}_v${VERSION}"
  if [ "${OS}" = "windows" ]; then
    BINARY_NAME="${BINARY_NAME}.exe"
  fi

  if [ ! -f "${UNPACK_DIR}/${BINARY_NAME}" ]; then
    echo "Error: Expected binary '${BINARY_NAME}' not found in archive" >&2
    exit 1
  fi

  if [ "${OS}" != "windows" ] && [ ! -x "${UNPACK_DIR}/${BINARY_NAME}" ]; then
    echo "Error: Binary '${BINARY_NAME}' is not executable" >&2
    exit 1
  fi

  if [ ! -f "${UNPACK_DIR}/terraform-registry-manifest.json" ]; then
    echo "Error: 'terraform-registry-manifest.json' missing from archive" >&2
    exit 1
  fi

  if [ ! -f "${UNPACK_DIR}/LICENSE" ]; then
    echo "Error: 'LICENSE' missing from archive" >&2
    exit 1
  fi

  if [ ! -f "${UNPACK_DIR}/README.md" ]; then
    echo "Error: 'README.md' missing from archive" >&2
    exit 1
  fi
  echo "    Unpacked archive contents verified successfully."

  # g. Install unpacked binary into local mirror directory
  echo "==> Installing unpacked binary into local filesystem mirror..."
  PLUGIN_DIR="${MIRROR_DIR}/registry.terraform.io/${PROVIDER_NAMESPACE}/${PROVIDER_NAME}/${VERSION}/${PLATFORM}"
  mkdir -p "${PLUGIN_DIR}"
  cp "${UNPACK_DIR}/${BINARY_NAME}" "${PLUGIN_DIR}/${BINARY_NAME}"
  chmod +x "${PLUGIN_DIR}/${BINARY_NAME}"

  if [ "${VERSION}" != "0.1.0" ]; then
    CANONICAL_DIR="${MIRROR_DIR}/registry.terraform.io/${PROVIDER_NAMESPACE}/${PROVIDER_NAME}/0.1.0/${PLATFORM}"
    mkdir -p "${CANONICAL_DIR}"
    CANONICAL_BIN="terraform-provider-${PROVIDER_NAME}_v0.1.0"
    if [ "${OS}" = "windows" ]; then
      CANONICAL_BIN="${CANONICAL_BIN}.exe"
    fi
    cp "${UNPACK_DIR}/${BINARY_NAME}" "${CANONICAL_DIR}/${CANONICAL_BIN}"
    chmod +x "${CANONICAL_DIR}/${CANONICAL_BIN}"
  fi

else
  VERSION="0.1.0"
  echo "==> Starting Clean Install Smoke Test in Build Mode (fallback) for v${VERSION}..."
  BINARY_NAME="terraform-provider-${PROVIDER_NAME}_v${VERSION}"
  if [ "${OS}" = "windows" ]; then
    BINARY_NAME="${BINARY_NAME}.exe"
  fi
  PLUGIN_DIR="${MIRROR_DIR}/registry.terraform.io/${PROVIDER_NAMESPACE}/${PROVIDER_NAME}/${VERSION}/${PLATFORM}"
  mkdir -p "${PLUGIN_DIR}"
  echo "==> Compiling provider binary..."
  (
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${PLUGIN_DIR}/${BINARY_NAME}" .
  )
  chmod +x "${PLUGIN_DIR}/${BINARY_NAME}"
  echo "    Built: ${PLUGIN_DIR}/${BINARY_NAME}"
fi

# 4. Generate Terraform CLI configuration (.terraformrc)
CLI_CONFIG="${TEMP_DIR}/terraformrc"
cat << EOF > "${CLI_CONFIG}"
provider_installation {
  filesystem_mirror {
    path    = "${MIRROR_DIR}"
    include = ["*/*"]
  }
  direct {
    exclude = ["*/*"]
  }
}
EOF

export TF_CLI_CONFIG_FILE="${CLI_CONFIG}"

# 5. Create test Terraform workspace
WORK_DIR="${TEMP_DIR}/workspace"
mkdir -p "${WORK_DIR}"

cat << EOF > "${WORK_DIR}/main.tf"
terraform {
  required_version = ">= 1.11.0"
  required_providers {
    namespace = {
      source  = "bsuv/namespace"
      version = ">= 0.0.0"
    }
  }
}

variable "test_token" {
  type      = string
  default   = "test-mock-token"
  sensitive = true
  ephemeral = true
}

provider "namespace" {
  token = var.test_token
}

data "namespace_github_base_images" "linux" {
  labels = ["ubuntu-24.04"]
}

data "namespace_builder_configuration" "amd64" {
  platform = "linux/amd64"
}

output "builder_endpoint" {
  value = data.namespace_builder_configuration.amd64.buildkit_endpoint
}
EOF

# 6. Execute terraform init and terraform validate on test workspace
echo "==> Initializing Terraform from local filesystem mirror..."
(
  cd "${WORK_DIR}"
  terraform init -backend=false
)

echo "==> Running terraform validate on test workspace..."
(
  cd "${WORK_DIR}"
  terraform validate
)

# 7. Execute terraform init and terraform validate on all repository examples
echo "==> Validating all repository example configurations..."
FAILED=0
EXAMPLE_WORK_ROOT="${TEMP_DIR}/examples"
for example_dir in "${REPO_ROOT}"/examples/data-sources/* "${REPO_ROOT}"/examples/ephemeral-resources/* "${REPO_ROOT}"/examples/resources/* "${REPO_ROOT}"/examples/provider; do
  if [ -d "${example_dir}" ]; then
    rel_path="${example_dir#"${REPO_ROOT}"/}"
    staged_example="${EXAMPLE_WORK_ROOT}/${rel_path}"
    mkdir -p "${staged_example}"
    cp "${example_dir}"/*.tf "${staged_example}/"
    echo "    Validating ${rel_path}..."
    (
      cd "${staged_example}"
      terraform init -backend=false >/dev/null || { echo "Init failed for ${rel_path}" >&2; exit 1; }
      terraform validate >/dev/null || { echo "Validate failed for ${rel_path}" >&2; exit 1; }
    ) || FAILED=1
  fi
done

if [ "${FAILED}" -ne 0 ]; then
  echo "Error: One or more example configurations failed validation." >&2
  exit 1
fi

echo "==> Clean-install smoke test PASSED successfully!"
