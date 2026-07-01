#!/usr/bin/env bash
# [dev-helper] Pack MooX → remote CGO build moox-storage → optional deploy.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
REPO_NAME="$(basename "${ROOT}")"
REPO_PARENT="$(dirname "${ROOT}")"

TARGET=""
DEPLOY_DIR="/home/ubuntu/moox"
REMOTE_GO="/home/ubuntu/.local/go1.24.0/bin/go"
REMOTE_BUILD_DIR="/home/ubuntu/moox-build-remote"
ZIP_LOCAL="/tmp/moox-build.zip"
ZIP_REMOTE="/tmp/moox-build.zip"
OUTPUT_REMOTE="/tmp/moox-storage-built"
DO_DEPLOY=0
SKIP_PACK=0
NON_INTERACTIVE=0
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-15}"

usage() {
  cat <<EOF
Usage: $(basename "$0") --target user@host [options]

Internal dev-helper: build moox-storage on remote Linux (CGO).

Options:
  --target HOST           SSH target (or MOOX_DEV_SSH_TARGET / infra.local.yaml)
  --deploy-dir PATH       Remote MooX deploy root (default: ${DEPLOY_DIR})
  --remote-go PATH        Remote go binary (default: ${REMOTE_GO})
  --remote-build-dir DIR  Remote unpack/build dir (default: ${REMOTE_BUILD_DIR})
  --zip-local PATH        Local zip path (default: ${ZIP_LOCAL})
  --deploy                Install binary and restart storage
  --skip-pack             Skip local zip; reuse remote zip
  --non-interactive       Fail instead of prompting for SSH target
  -h, --help              Show help
EOF
}

log() { printf '[dev-helper:storage] %s\n' "$*"; }
fail() { log "ERROR: $*"; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET="${2:-}"; shift 2 ;;
    --deploy-dir) DEPLOY_DIR="${2:-}"; shift 2 ;;
    --remote-go) REMOTE_GO="${2:-}"; shift 2 ;;
    --remote-build-dir) REMOTE_BUILD_DIR="${2:-}"; shift 2 ;;
    --zip-local) ZIP_LOCAL="${2:-}"; shift 2 ;;
    --deploy) DO_DEPLOY=1; shift ;;
    --skip-pack) SKIP_PACK=1; shift ;;
    --non-interactive) NON_INTERACTIVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

# shellcheck source=../../../scripts/lib/dev-ssh-target.sh
source "${ROOT}/scripts/lib/dev-ssh-target.sh"
moox_dev_load_local_env

if ! moox_dev_resolve_ssh_target "${ROOT}" "${TARGET}" TARGET "${NON_INTERACTIVE}"; then
  fail "SSH target required: --target, MOOX_DEV_SSH_TARGET, infra/infra.local.yaml, or interactive input (use SSH keys; never commit passwords)"
fi

log "using SSH target: ${TARGET}"

if [[ "${SKIP_PACK}" -eq 0 ]]; then
  log "packing ${REPO_NAME} -> ${ZIP_LOCAL}"
  rm -f "${ZIP_LOCAL}"
  (
    cd "${REPO_PARENT}"
    COPYFILE_DISABLE=1 zip -r "${ZIP_LOCAL}" "${REPO_NAME}" \
      -x "${REPO_NAME}/.git/*" \
      -x "${REPO_NAME}/web/node_modules/*" \
      -x "${REPO_NAME}/bin/*" \
      -x "${REPO_NAME}/**/data/*" \
      -x "${REPO_NAME}/release/*" \
      -x "${REPO_NAME}/**/.DS_Store" \
      -x "${REPO_NAME}/**/pebble/main/*" \
      >/dev/null
  )
  unzip -l "${ZIP_LOCAL}" | grep 'modules/storage/internal/infra/device/pebble/store.go' >/dev/null || fail "pebble/store.go missing in zip"
  ls -lh "${ZIP_LOCAL}"

  log "upload ${ZIP_LOCAL} -> ${TARGET}:${ZIP_REMOTE}"
  scp "${ZIP_LOCAL}" "${TARGET}:${ZIP_REMOTE}"
else
  log "skip pack; using existing ${ZIP_REMOTE} on remote"
fi

log "remote CGO build on ${TARGET}"
ssh "${TARGET}" "set -e
export PATH=\"$(dirname "${REMOTE_GO}"):\$PATH\"
test -x \"${REMOTE_GO}\" || { echo 'go not found: ${REMOTE_GO}' >&2; exit 1; }
rm -rf \"${REMOTE_BUILD_DIR}\"
mkdir -p \"${REMOTE_BUILD_DIR}\"
cd \"${REMOTE_BUILD_DIR}\"
unzip -q \"${ZIP_REMOTE}\"
test -f \"${REPO_NAME}/modules/storage/internal/infra/device/pebble/store.go\"
cd \"${REPO_NAME}\"
CGO_ENABLED=1 \"${REMOTE_GO}\" build -ldflags '-s -w' -o \"${OUTPUT_REMOTE}\" ./modules/storage/cmd/moox-storage
ls -la \"${OUTPUT_REMOTE}\"
file \"${OUTPUT_REMOTE}\"
"

if [[ "${DO_DEPLOY}" -eq 1 ]]; then
  log "deploy to ${DEPLOY_DIR} and restart storage"
  ssh "${TARGET}" "set -e
cd \"${DEPLOY_DIR}\"
test -x ./stop.sh && ./stop.sh storage || true
cp \"${OUTPUT_REMOTE}\" ./bin/moox-storage
chmod +x ./bin/moox-storage
export STARTUP_WAIT_SECONDS=${STARTUP_WAIT_SECONDS}
./start.sh storage
./status.sh
ss -tlnp | grep -E '20200|20201|20202' || true
"
  log "deploy complete"
else
  log "binary at ${TARGET}:${OUTPUT_REMOTE} (use --deploy to install)"
fi
