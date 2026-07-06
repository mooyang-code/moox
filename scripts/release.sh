#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-dev}"
OS="${TARGET_GOOS:-${GOOS:-$(go env GOOS)}}"
ARCH="${TARGET_GOARCH:-${GOARCH:-$(go env GOARCH)}}"
RELEASE_ROOT="${ROOT}/release/moox-${VERSION}-${OS}-${ARCH}"
ARCHIVE="${RELEASE_ROOT}.tar.gz"

TARGET_GOOS="${OS}" TARGET_GOARCH="${ARCH}" "${ROOT}/scripts/build.sh"

rm -rf "${RELEASE_ROOT}"
mkdir -p \
  "${RELEASE_ROOT}/cli/bin" \
  "${RELEASE_ROOT}/admin/bin" \
  "${RELEASE_ROOT}/admin/config" \
  "${RELEASE_ROOT}/web-host/bin" \
  "${RELEASE_ROOT}/cloudnode/bin" \
  "${RELEASE_ROOT}/cloudnode/config" \
  "${RELEASE_ROOT}/collector/bin" \
  "${RELEASE_ROOT}/collector/config" \
  "${RELEASE_ROOT}/factor/bin" \
  "${RELEASE_ROOT}/trade/bin" \
  "${RELEASE_ROOT}/storage/bin" \
  "${RELEASE_ROOT}/storage/config" \
  "${RELEASE_ROOT}/storage/schema" \
  "${RELEASE_ROOT}/examples" \
  "${RELEASE_ROOT}/docs"

copy_binary() {
  local name="$1"
  local target_dir="$2"
  cp "${ROOT}/bin/${name}" "${target_dir}/"
}

copy_binary moox-cli "${RELEASE_ROOT}/cli/bin"
copy_binary moox-admin "${RELEASE_ROOT}/admin/bin"
copy_binary moox-admin-cli "${RELEASE_ROOT}/admin/bin"
copy_binary moox-web-host "${RELEASE_ROOT}/web-host/bin"
copy_binary moox-cloudnode "${RELEASE_ROOT}/cloudnode/bin"
copy_binary moox-cloudnode-cli "${RELEASE_ROOT}/cloudnode/bin"
copy_binary moox-collector "${RELEASE_ROOT}/collector/bin"
copy_binary moox-collector-cli "${RELEASE_ROOT}/collector/bin"
copy_binary moox-collector-scf "${RELEASE_ROOT}/collector/bin"
copy_binary moox-factor "${RELEASE_ROOT}/factor/bin"
copy_binary moox-trade "${RELEASE_ROOT}/trade/bin"
copy_binary moox-trade-cli "${RELEASE_ROOT}/trade/bin"
copy_binary moox-storage "${RELEASE_ROOT}/storage/bin"
copy_binary moox-storage-cli "${RELEASE_ROOT}/storage/bin"

cp -R "${ROOT}/modules/admin/config/." "${RELEASE_ROOT}/admin/config/"
cp -R "${ROOT}/modules/cloudnode/config/." "${RELEASE_ROOT}/cloudnode/config/"
cp -R "${ROOT}/modules/collector/config/." "${RELEASE_ROOT}/collector/config/"
cp -R "${ROOT}/modules/storage/config/." "${RELEASE_ROOT}/storage/config/"
cp -R "${ROOT}/modules/storage/schema/." "${RELEASE_ROOT}/storage/schema/"
cp "${ROOT}/scripts/storage-start.sh" "${RELEASE_ROOT}/storage/start.sh"
cp "${ROOT}/scripts/storage-stop.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp -R "${ROOT}/examples/." "${RELEASE_ROOT}/examples/"
cp -R "${ROOT}/docs/." "${RELEASE_ROOT}/docs/" 2>/dev/null || true
chmod +x "${RELEASE_ROOT}/storage/start.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp "${ROOT}/README.md" "${RELEASE_ROOT}/README.md" 2>/dev/null || true

tar -C "${ROOT}/release" -czf "${ARCHIVE}" "$(basename "${RELEASE_ROOT}")"
echo "==> release package: ${ARCHIVE}"
