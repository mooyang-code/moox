#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REMOTE_HOST="${REMOTE_HOST:-43.132.204.177}"
REMOTE_ROOT="${REMOTE_ROOT:-~/moox}"
REMOTE_SSH="${REMOTE_SSH:-${REMOTE_HOST}}"
REMOTE_GOOS="${REMOTE_GOOS:-linux}"
REMOTE_GOARCH="${REMOTE_GOARCH:-amd64}"
REMOTE_STORAGE_CGO_ENABLED="${REMOTE_STORAGE_CGO_ENABLED:-0}"
REMOTE_STORAGE_BUILD_TAGS="${REMOTE_STORAGE_BUILD_TAGS:-noduckdb}"
LOCAL_STAGE="${ROOT}/release/deploy-stage"

GOOS="${REMOTE_GOOS}" GOARCH="${REMOTE_GOARCH}" \
  TARGET_GOOS="${REMOTE_GOOS}" TARGET_GOARCH="${REMOTE_GOARCH}" \
  STORAGE_CGO_ENABLED="${REMOTE_STORAGE_CGO_ENABLED}" \
  STORAGE_BUILD_TAGS="${REMOTE_STORAGE_BUILD_TAGS}" \
  "${ROOT}/build/release.sh"

rm -rf "${LOCAL_STAGE}"
mkdir -p "${LOCAL_STAGE}"
cp -R "${ROOT}/bin" "${LOCAL_STAGE}/bin"
cp -R "${ROOT}/docs" "${LOCAL_STAGE}/docs" 2>/dev/null || true
cp -R "${ROOT}/skills" "${LOCAL_STAGE}/skills" 2>/dev/null || true
cp -R "${ROOT}/build" "${LOCAL_STAGE}/build"
mkdir -p "${LOCAL_STAGE}/var/storage"

if [[ -f "${HOME}/Downloads/APT-USDT.csv" ]]; then
  mkdir -p "${LOCAL_STAGE}/sample-data"
  cp "${HOME}/Downloads/APT-USDT.csv" "${LOCAL_STAGE}/sample-data/"
fi
if [[ -f "${HOME}/Downloads/AR-USDT.csv" ]]; then
  mkdir -p "${LOCAL_STAGE}/sample-data"
  cp "${HOME}/Downloads/AR-USDT.csv" "${LOCAL_STAGE}/sample-data/"
fi

echo "==> deploy to ${REMOTE_SSH}:${REMOTE_ROOT}"
ssh -o BatchMode=yes -o ConnectTimeout=10 "${REMOTE_SSH}" "mkdir -p ${REMOTE_ROOT}"

if command -v rsync >/dev/null 2>&1; then
  rsync -az --delete "${LOCAL_STAGE}/" "${REMOTE_SSH}:${REMOTE_ROOT}/"
else
  tar -C "${LOCAL_STAGE}" -czf "${ROOT}/release/deploy-stage.tar.gz" .
  scp "${ROOT}/release/deploy-stage.tar.gz" "${REMOTE_SSH}:${REMOTE_ROOT}/deploy-stage.tar.gz"
  ssh "${REMOTE_SSH}" "cd ${REMOTE_ROOT} && tar -xzf deploy-stage.tar.gz && rm -f deploy-stage.tar.gz"
fi

ssh -o BatchMode=yes -o ConnectTimeout=10 "${REMOTE_SSH}" \
  "cd ${REMOTE_ROOT} && chmod +x bin/* build/*.sh && CSV_DIR=${REMOTE_ROOT}/sample-data ./build/acceptance.sh"
echo "==> deploy and remote acceptance passed"
