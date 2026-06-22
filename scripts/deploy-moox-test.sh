#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

DEPLOY_DIR="${TMP_DIR}/moox"
STAGE_DIR="${TMP_DIR}/stage"

"${ROOT}/scripts/deploy-moox.sh" \
  --target localhost \
  --dir "${DEPLOY_DIR}" \
  --stage "${STAGE_DIR}" \
  --skip-build \
  --no-start \
  --no-web-host

assert_file() {
  local path="$1"
  if [[ ! -f "${path}" ]]; then
    echo "missing file: ${path}" >&2
    exit 1
  fi
}

assert_executable() {
  local path="$1"
  if [[ ! -x "${path}" ]]; then
    echo "not executable: ${path}" >&2
    exit 1
  fi
}

assert_contains() {
  local path="$1"
  local pattern="$2"
  if ! grep -Fq "${pattern}" "${path}"; then
    echo "missing pattern in ${path}: ${pattern}" >&2
    sed -n '1,160p' "${path}" >&2
    exit 1
  fi
}

assert_file "${DEPLOY_DIR}/bin/moox-server"
assert_file "${DEPLOY_DIR}/bin/moox-storage"
assert_file "${DEPLOY_DIR}/bin/moox-cli"
assert_file "${DEPLOY_DIR}/control/config/trpc_go.yaml"
assert_file "${DEPLOY_DIR}/control/config/app.yaml"
assert_file "${DEPLOY_DIR}/control/config/gateway.yaml"
assert_file "${DEPLOY_DIR}/storage/config/trpc_go.yaml"
assert_file "${DEPLOY_DIR}/storage/config/storage.yaml"
assert_file "${DEPLOY_DIR}/storage/schema/metadata.sql"
assert_file "${DEPLOY_DIR}/examples/metadata-crypto.seed.yaml"
assert_file "${DEPLOY_DIR}/examples/platform-local.seed.yaml"
assert_executable "${DEPLOY_DIR}/start.sh"
assert_executable "${DEPLOY_DIR}/stop.sh"
assert_executable "${DEPLOY_DIR}/status.sh"

assert_contains "${DEPLOY_DIR}/control/config/app.yaml" "path: ../data/moox.db"
assert_contains "${DEPLOY_DIR}/control/config/app.yaml" "output_path: ../logs/control/moox.log"
assert_contains "${DEPLOY_DIR}/control/config/gateway.yaml" "dbname: \"../data/moox.db\""
assert_contains "${DEPLOY_DIR}/control/config/gateway.yaml" "data_dir: \"../data/badger\""
assert_contains "${DEPLOY_DIR}/control/config/trpc_go.yaml" "log_path: ../logs/control"
assert_contains "${DEPLOY_DIR}/storage/config/storage.yaml" "root: ../data/storage"
assert_contains "${DEPLOY_DIR}/storage/config/storage.yaml" "path: ../data/storage/metadata/storage_metadata.db"
assert_contains "${DEPLOY_DIR}/storage/config/trpc_go.yaml" "log_path: ../logs/storage"
assert_contains "${DEPLOY_DIR}/start.sh" "moox-storage"
assert_contains "${DEPLOY_DIR}/start.sh" "moox-server"

echo "deploy-moox test passed: ${DEPLOY_DIR}"
