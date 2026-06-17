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
VERSION="${VERSION:-dev}"
RELEASE_ROOT="${ROOT}/release/moox-${VERSION}-${REMOTE_GOOS}-${REMOTE_GOARCH}"

GOOS="${REMOTE_GOOS}" GOARCH="${REMOTE_GOARCH}" \
  TARGET_GOOS="${REMOTE_GOOS}" TARGET_GOARCH="${REMOTE_GOARCH}" \
  STORAGE_CGO_ENABLED="${REMOTE_STORAGE_CGO_ENABLED}" \
  STORAGE_BUILD_TAGS="${REMOTE_STORAGE_BUILD_TAGS}" \
  "${ROOT}/scripts/release.sh"

rm -rf "${LOCAL_STAGE}"
mkdir -p "${LOCAL_STAGE}"
cp -R "${RELEASE_ROOT}/." "${LOCAL_STAGE}/"

if [[ -f "${HOME}/Downloads/APT-USDT.csv" ]]; then
  mkdir -p "${LOCAL_STAGE}/storage/sample-data"
  cp "${HOME}/Downloads/APT-USDT.csv" "${LOCAL_STAGE}/storage/sample-data/"
fi
if [[ -f "${HOME}/Downloads/AR-USDT.csv" ]]; then
  mkdir -p "${LOCAL_STAGE}/storage/sample-data"
  cp "${HOME}/Downloads/AR-USDT.csv" "${LOCAL_STAGE}/storage/sample-data/"
fi

echo "==> deploy to ${REMOTE_SSH}:${REMOTE_ROOT}"
ssh -o BatchMode=yes -o ConnectTimeout=10 "${REMOTE_SSH}" "mkdir -p ${REMOTE_ROOT}"

ssh -o BatchMode=yes -o ConnectTimeout=10 "${REMOTE_SSH}" \
  "set -e
   if [ -x ${REMOTE_ROOT}/storage/stop.sh ]; then
     (cd ${REMOTE_ROOT}/storage && ./stop.sh) || true
   fi
   for pid in \$(pgrep -f moox-storage || true); do
     cwd=\$(readlink -f /proc/\$pid/cwd 2>/dev/null || true)
     exe=\$(readlink -f /proc/\$pid/exe 2>/dev/null || true)
     if [ \"\$cwd\" = ${REMOTE_ROOT}/storage ] || [ \"\$exe\" = ${REMOTE_ROOT}/storage/bin/moox-storage ] || [ \"\$exe\" = \"${REMOTE_ROOT}/storage/bin/moox-storage (deleted)\" ]; then
       kill \"\$pid\" 2>/dev/null || true
     fi
   done
   sleep 2
   for pid in \$(pgrep -f moox-storage || true); do
     cwd=\$(readlink -f /proc/\$pid/cwd 2>/dev/null || true)
     exe=\$(readlink -f /proc/\$pid/exe 2>/dev/null || true)
     if [ \"\$cwd\" = ${REMOTE_ROOT}/storage ] || [ \"\$exe\" = ${REMOTE_ROOT}/storage/bin/moox-storage ] || [ \"\$exe\" = \"${REMOTE_ROOT}/storage/bin/moox-storage (deleted)\" ]; then
       kill -9 \"\$pid\" 2>/dev/null || true
     fi
   done"

if command -v rsync >/dev/null 2>&1; then
  rsync -az --delete "${LOCAL_STAGE}/" "${REMOTE_SSH}:${REMOTE_ROOT}/"
else
  tar -C "${LOCAL_STAGE}" -czf "${ROOT}/release/deploy-stage.tar.gz" .
  scp "${ROOT}/release/deploy-stage.tar.gz" "${REMOTE_SSH}:${REMOTE_ROOT}/deploy-stage.tar.gz"
  ssh "${REMOTE_SSH}" "cd ${REMOTE_ROOT} && tar -xzf deploy-stage.tar.gz && rm -f deploy-stage.tar.gz"
fi

ssh -o BatchMode=yes -o ConnectTimeout=10 "${REMOTE_SSH}" \
  "set -e
   if [ -x \"\$HOME/xdata/storage/stop.sh\" ]; then
     (cd \"\$HOME/xdata/storage\" && ./stop.sh) || true
   fi
   for pid in \$(pgrep -f xdata-storage || true); do
     exe=\$(readlink -f /proc/\$pid/exe 2>/dev/null || true)
     if [ \"\$exe\" = \"\$HOME/xdata/storage/bin/xdata-storage\" ]; then
       kill \"\$pid\" 2>/dev/null || true
     fi
   done
   cd ${REMOTE_ROOT}
   chmod +x scripts/*.sh cli/bin/* control/bin/* collector/bin/* factor/bin/* order/bin/* account/bin/* storage/bin/* storage/*.sh
   (cd storage && ./stop.sh) || true
   (cd storage && ./start.sh)
   CSV_DIR=${REMOTE_ROOT}/storage/sample-data STORAGE_URL=http://127.0.0.1:19104 CLI=${REMOTE_ROOT}/cli/bin/moox-cli ./scripts/acceptance.sh"
echo "==> deploy, storage start, and remote acceptance passed"
