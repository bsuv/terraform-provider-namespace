#!/usr/bin/env bash

set -euo pipefail

PROVIDER_NAME="namespace"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ $# -gt 2 ]]; then
  echo "Usage: $0 [goreleaser-dist-directory] [staging-directory]" >&2
  exit 1
fi

source_dir="${1:-${REPO_ROOT}/dist}"
staging_dir="${2:-${RUNNER_TEMP:-/tmp}/namespace-snapshot-dist}"

if [[ ! -d "${source_dir}" ]]; then
  echo "Error: GoReleaser distribution directory '${source_dir}' does not exist." >&2
  exit 1
fi
source_dir="$(cd "${source_dir}" && pwd -P)"

mkdir -p "${staging_dir}"
staging_dir="$(cd "${staging_dir}" && pwd -P)"
if [[ "${source_dir}" == "${staging_dir}" ]]; then
  echo "Error: staging directory must differ from the GoReleaser distribution directory." >&2
  exit 1
fi
if [[ -n "$(find "${staging_dir}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "Error: staging directory '${staging_dir}' must be empty." >&2
  exit 1
fi

checksum_file=""
checksum_count=0
for candidate in "${source_dir}"/*_SHA256SUMS; do
  [[ -f "${candidate}" ]] || continue
  checksum_file="${candidate}"
  checksum_count=$((checksum_count + 1))
done
if [[ "${checksum_count}" -ne 1 ]]; then
  echo "Error: expected exactly one *_SHA256SUMS file in '${source_dir}', found ${checksum_count}." >&2
  exit 1
fi

checksum_basename="$(basename "${checksum_file}")"
case "${checksum_basename}" in
  "terraform-provider-${PROVIDER_NAME}_"*_SHA256SUMS) ;;
  *)
    echo "Error: unexpected checksum filename '${checksum_basename}'." >&2
    exit 1
    ;;
esac
version="${checksum_basename#terraform-provider-"${PROVIDER_NAME}"_}"
version="${version%_SHA256SUMS}"
if [[ "${version}" != *SNAPSHOT* ]]; then
  echo "Error: refusing to stage non-snapshot version '${version}' without release signatures." >&2
  exit 1
fi

manifest_name="terraform-provider-${PROVIDER_NAME}_${version}_manifest.json"
manifest_source="${REPO_ROOT}/terraform-registry-manifest.json"
if [[ ! -f "${manifest_source}" ]]; then
  echo "Error: source Registry manifest '${manifest_source}' is missing." >&2
  exit 1
fi

manifest_seen=false
archive_count=0
while read -r checksum filename extra; do
  [[ -n "${checksum:-}" ]] || continue
  if [[ -n "${extra:-}" ]] || [[ ! "${checksum}" =~ ^[[:xdigit:]]{64}$ ]]; then
    echo "Error: malformed checksum entry in '${checksum_basename}'." >&2
    exit 1
  fi
  filename="${filename#\*}"
  if [[ -z "${filename}" ]] || [[ "$(basename "${filename}")" != "${filename}" ]]; then
    echo "Error: unsafe artifact filename '${filename}' in '${checksum_basename}'." >&2
    exit 1
  fi

  if [[ "${filename}" == "${manifest_name}" ]]; then
    cp "${manifest_source}" "${staging_dir}/${filename}"
    manifest_seen=true
    continue
  fi

  if [[ "${filename}" != "terraform-provider-${PROVIDER_NAME}_${version}_"*.zip ]]; then
    echo "Error: unexpected snapshot artifact '${filename}' in '${checksum_basename}'." >&2
    exit 1
  fi
  if [[ ! -f "${source_dir}/${filename}" ]]; then
    echo "Error: checksum-listed artifact '${filename}' is missing from '${source_dir}'." >&2
    exit 1
  fi
  cp "${source_dir}/${filename}" "${staging_dir}/${filename}"
  archive_count=$((archive_count + 1))
done < "${checksum_file}"

if [[ "${manifest_seen}" != true ]] || [[ "${archive_count}" -eq 0 ]]; then
  echo "Error: snapshot checksums must cover the standalone manifest and at least one archive." >&2
  exit 1
fi

cp "${checksum_file}" "${staging_dir}/${checksum_basename}"
echo "Staged ${archive_count} snapshot archives, checksums, and manifest in ${staging_dir}."
