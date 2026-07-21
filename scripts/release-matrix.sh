#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-$(git -C "${ROOT}" describe --tags --always --dirty 2>/dev/null || echo dev)}"
RELEASE_PLATFORMS="${RELEASE_PLATFORMS:-linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64}"
DRY_RUN=0

usage() {
  cat <<'EOF'
Usage: scripts/release-matrix.sh [options]

Build release archives for all requested platforms. GitHub Actions and CNB
publish the generated archives from their respective release pipelines.

Options:
  --platforms LIST  Comma-separated GOOS/GOARCH targets
  --dry-run         Print the build matrix without compiling
  -h, --help        Show this help

Environment:
  VERSION                     Release version/tag used in archive names
  RELEASE_PLATFORMS           Default: linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64
  STORAGE_CGO_ENABLED         Override Storage CGO setting for every target
EOF
}

while (($# > 0)); do
  case "$1" in
    --platforms)
      (($# >= 2)) || { echo "--platforms requires a value" >&2; exit 2; }
      RELEASE_PLATFORMS="$2"
      shift 2
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

platforms_value="${RELEASE_PLATFORMS//,/ }"
read -r -a platforms <<< "${platforms_value}"
[[ "${#platforms[@]}" -gt 0 ]] || { echo "release platform list is empty" >&2; exit 2; }

write_checksum() {
  local archive="$1"
  local checksum="${archive%.tar.gz}.sha256"
  local digest

  if command -v sha256sum >/dev/null 2>&1; then
    digest="$(sha256sum "${archive}" | awk '{print $1}')"
  else
    digest="$(shasum -a 256 "${archive}" | awk '{print $1}')"
  fi
  printf '%s  %s\n' "${digest}" "$(basename "${archive}")" >"${checksum}"
}

build_platform() {
  local platform="$1"
  local goos="${platform%/*}"
  local goarch="${platform#*/}"
  local storage_cgo="${STORAGE_CGO_ENABLED:-}"

  [[ "${goos}" != "${platform}" && -n "${goarch}" ]] || {
    echo "invalid release platform: ${platform}; expected GOOS/GOARCH" >&2
    exit 2
  }

	if [[ -z "${storage_cgo}" ]]; then
		# The View role requires DuckDB, including on cross-built targets. A
		# no-CGO artifact is valid only for deployments that do not start View.
		storage_cgo=1
	fi

  echo "==> release ${VERSION} (${platform}, STORAGE_CGO_ENABLED=${storage_cgo})"
  if ((DRY_RUN)); then
    return
  fi

  VERSION="${VERSION}" \
  TARGET_GOOS="${goos}" \
  TARGET_GOARCH="${goarch}" \
  STORAGE_CGO_ENABLED="${storage_cgo}" \
  SKIP_WEB_ASSETS="${SKIP_WEB_ASSETS:-0}" \
    "${ROOT}/scripts/release.sh"
  archive="${ROOT}/release/moox-${VERSION}-${goos}-${goarch}.tar.gz"
  [[ -s "${archive}" ]] || { echo "release archive was not created: ${archive}" >&2; exit 1; }
  write_checksum "${archive}"
  SKIP_WEB_ASSETS=1
}

for platform in "${platforms[@]}"; do
  build_platform "${platform}"
done

if ((DRY_RUN == 0)); then
  echo "==> release archives written to ${ROOT}/release"
fi
