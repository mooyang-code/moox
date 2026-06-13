#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-dev}"
OS="$(go env GOOS)"
ARCH="$(go env GOARCH)"
RELEASE_ROOT="${ROOT}/release/moox-${VERSION}-${OS}-${ARCH}"
ARCHIVE="${RELEASE_ROOT}.tar.gz"

"${ROOT}/build/build.sh"

rm -rf "${RELEASE_ROOT}"
mkdir -p "${RELEASE_ROOT}/bin" "${RELEASE_ROOT}/docs" "${RELEASE_ROOT}/skills" "${RELEASE_ROOT}/build" "${RELEASE_ROOT}/var/storage"

cp -R "${ROOT}/bin/." "${RELEASE_ROOT}/bin/"
cp -R "${ROOT}/docs/." "${RELEASE_ROOT}/docs/" 2>/dev/null || true
cp -R "${ROOT}/skills/." "${RELEASE_ROOT}/skills/" 2>/dev/null || true
cp "${ROOT}/build/"*.sh "${RELEASE_ROOT}/build/"
cp "${ROOT}/README.md" "${RELEASE_ROOT}/README.md" 2>/dev/null || true

tar -C "${ROOT}/release" -czf "${ARCHIVE}" "$(basename "${RELEASE_ROOT}")"
echo "==> release package: ${ARCHIVE}"
