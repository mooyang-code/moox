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
  "${RELEASE_ROOT}/control/bin" \
  "${RELEASE_ROOT}/collector/bin" \
  "${RELEASE_ROOT}/factor/bin" \
  "${RELEASE_ROOT}/order/bin" \
  "${RELEASE_ROOT}/account/bin" \
  "${RELEASE_ROOT}/storage/bin" \
  "${RELEASE_ROOT}/storage/config" \
  "${RELEASE_ROOT}/storage/data" \
  "${RELEASE_ROOT}/storage/database" \
  "${RELEASE_ROOT}/storage/logs" \
  "${RELEASE_ROOT}/storage/sample-data" \
  "${RELEASE_ROOT}/storage/schema" \
  "${RELEASE_ROOT}/storage/var/storage" \
  "${RELEASE_ROOT}/docs" \
  "${RELEASE_ROOT}/skills" \
  "${RELEASE_ROOT}/scripts"

cp "${ROOT}/bin/moox-cli" "${RELEASE_ROOT}/cli/bin/"
cp "${ROOT}/bin/moox-server" "${RELEASE_ROOT}/control/bin/"
cp "${ROOT}/bin/moox-collector" "${RELEASE_ROOT}/collector/bin/"
cp "${ROOT}/bin/moox-factor" "${RELEASE_ROOT}/factor/bin/"
cp "${ROOT}/bin/moox-order" "${RELEASE_ROOT}/order/bin/"
cp "${ROOT}/bin/moox-account" "${RELEASE_ROOT}/account/bin/"
cp "${ROOT}/bin/moox-storage" "${RELEASE_ROOT}/storage/bin/"
cp -R "${ROOT}/modules/storage/config/." "${RELEASE_ROOT}/storage/config/"
cp -R "${ROOT}/modules/storage/schema/." "${RELEASE_ROOT}/storage/schema/"
cp "${ROOT}/scripts/storage-start.sh" "${RELEASE_ROOT}/storage/start.sh"
cp "${ROOT}/scripts/storage-stop.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp -R "${ROOT}/docs/." "${RELEASE_ROOT}/docs/" 2>/dev/null || true
cp -R "${ROOT}/skills/." "${RELEASE_ROOT}/skills/" 2>/dev/null || true
cp -R "${ROOT}/scripts/." "${RELEASE_ROOT}/scripts/"
rm -rf "${RELEASE_ROOT}/scripts/node_exporter/build"
find "${RELEASE_ROOT}/scripts" -type f -name "*.sh" -exec chmod +x {} +
chmod +x "${RELEASE_ROOT}/storage/start.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp "${ROOT}/README.md" "${RELEASE_ROOT}/README.md" 2>/dev/null || true

tar -C "${ROOT}/release" -czf "${ARCHIVE}" "$(basename "${RELEASE_ROOT}")"
echo "==> release package: ${ARCHIVE}"
