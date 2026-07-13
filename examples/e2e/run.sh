#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="${MOOX_E2E_TARGET:-localhost}"
DEPLOY_DIR="${MOOX_E2E_DEPLOY_DIR:-/tmp/moox-e2e}"
PUBLIC_HOST="${MOOX_E2E_PUBLIC_HOST:-}"
DEPLOY=1
RESET_DATA=1
TIMEOUT_SECONDS=180
SPACE_ID="crypto"
RULE_ID="binance_spot_kline_1m"
NODE_ID="e2e-scf-node"
PACKAGE_ID="moox-collector_dev"
DATASET_ID="binance_spot_kline"
SYSDEPLOY_ONLY=0
E2E_ADMIN_USERNAME="mooxe2eadmin"
E2E_ADMIN_PASSWORD="MooxE2E#20260704!"
ADMIN_PASSWORD_FILE=""

export MOOX_ADMIN_JWT_SECRET_KEY="${MOOX_ADMIN_JWT_SECRET_KEY:-moox-e2e-jwt-secret-key-20260713-safe}"
export MOOX_EVENTBUS_STREAM_MAX_BYTES="${MOOX_EVENTBUS_STREAM_MAX_BYTES:-104857600}"

usage() {
  cat <<'EOF'
Usage:
  examples/e2e/run.sh [options]

Options:
  --target <localhost|user@host>  Deploy and run target. Default: localhost.
  --dir <path>                    Deploy directory on target. Default: /tmp/moox-e2e.
  --host <host>                   Public host used by browser/gateway checks.
  --skip-deploy                   Reuse an already running deployment.
  --preserve-data                 Do not pass --reset-data to deploy-moox.sh.
  --timeout-seconds <n>           SCF/assert timeout. Default: 180.
  --sysdeploy-only                Stop after the service-directory browser/API lifecycle.
  -h, --help                      Show this help.

Examples:
  examples/e2e/run.sh --target localhost --dir /tmp/moox-e2e
  examples/e2e/run.sh --target root@106.53.107.122 --dir ~/moox/prod --host 106.53.107.122
EOF
}

log() {
  printf '[e2e] %s\n' "$*"
}

fail() {
  printf '[e2e] ERROR: %s\n' "$*" >&2
  exit 1
}

is_local_target() {
  [[ "${TARGET}" == "localhost" || "${TARGET}" == "127.0.0.1" || "${TARGET}" == "::1" ]]
}

shell_quote() {
  local value="$1"
  printf "'%s'" "$(printf '%s' "${value}" | sed "s/'/'\\\\''/g")"
}

expand_local_path() {
  local path="$1"
  case "${path}" in
    "~") echo "${HOME}" ;;
    "~/"*) echo "${HOME}/${path#~/}" ;;
    /*) echo "${path}" ;;
    *) echo "${PWD}/${path}" ;;
  esac
}

infer_public_host() {
  if [[ -n "${PUBLIC_HOST}" ]]; then
    return
  fi
  if is_local_target; then
    PUBLIC_HOST="127.0.0.1"
    return
  fi
  PUBLIC_HOST="${TARGET#*@}"
  PUBLIC_HOST="${PUBLIC_HOST%%:*}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      TARGET="${2:-}"
      shift 2
      ;;
    --dir)
      DEPLOY_DIR="${2:-}"
      shift 2
      ;;
    --host)
      PUBLIC_HOST="${2:-}"
      shift 2
      ;;
    --skip-deploy)
      DEPLOY=0
      shift
      ;;
    --preserve-data)
      RESET_DATA=0
      shift
      ;;
    --timeout-seconds)
      TIMEOUT_SECONDS="${2:-}"
      shift 2
      ;;
    --sysdeploy-only)
      SYSDEPLOY_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

[[ -n "${TARGET}" ]] || fail "--target cannot be empty"
[[ -n "${DEPLOY_DIR}" ]] || fail "--dir cannot be empty"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--timeout-seconds must be an integer"
infer_public_host

run_remote() {
  local quoted_dir
  quoted_dir="$(shell_quote "${DEPLOY_DIR}")"
  ssh "${TARGET}" "cd ${quoted_dir} && $*"
}

import_seed() {
  local seed="$1"
  if is_local_target; then
    local deploy_dir
    deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
    "${deploy_dir}/bin/moox-cli" metadata import \
      --metadata-url "http://127.0.0.1:20200" \
      --file "${deploy_dir}/examples/${seed}" \
      --if-not-exists
    return
  fi
  run_remote "./bin/moox-cli metadata import --metadata-url http://127.0.0.1:20200 --file ./examples/${seed} --if-not-exists"
}

run_scf_once() {
  local timeout="${TIMEOUT_SECONDS}s"
  local envs=(
    "MOOX_SPACE_ID=${SPACE_ID}"
    "MOOX_SERVICE_AUTH_VERSION=moox-auth-v2"
    "MOOX_SERVICE_AUTH_ACCESS_KEY=moox-service"
    "MOOX_SERVICE_AUTH_SECRET_KEY="
    "MOOX_SERVICE_AUTH_EXPIRE_SECONDS=60"
  )
  local cmd
  cmd="env ${envs[*]} ./bin/moox-collector-scf -once -service-gateway-target http://127.0.0.1:11000 -node-id ${NODE_ID} -storage-metadata-target 127.0.0.1:20100 -storage-access-target 127.0.0.1:20102 -timeout ${timeout}"
  if is_local_target; then
    local deploy_dir
    deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
    (cd "${deploy_dir}" && ${cmd})
    return
  fi
  run_remote "${cmd}"
}

verify_phase() {
  local phase="$1"
  local web_url="http://${PUBLIC_HOST}:9527"
  if is_local_target; then
    web_url="http://127.0.0.1:9528"
  fi
  node "${ROOT}/examples/e2e/verify.mjs" \
    --phase "${phase}" \
    --gateway "http://${PUBLIC_HOST}:11000" \
    --web "${web_url}" \
    --host "${PUBLIC_HOST}" \
    --space "${SPACE_ID}" \
    --rule "${RULE_ID}" \
    --node "${NODE_ID}" \
    --package "${PACKAGE_ID}" \
    --dataset "${DATASET_ID}" \
    --timeout-seconds "${TIMEOUT_SECONDS}"
}

log "target=${TARGET} dir=${DEPLOY_DIR} public_host=${PUBLIC_HOST} reset_data=${RESET_DATA}"

if [[ "${DEPLOY}" -eq 1 ]]; then
  deploy_args=(--target "${TARGET}" --dir "${DEPLOY_DIR}")
  if [[ "${RESET_DATA}" -eq 1 ]]; then
    ADMIN_PASSWORD_FILE="$(mktemp "${TMPDIR:-/tmp}/moox-e2e-admin-password.XXXXXX")"
    chmod 600 "${ADMIN_PASSWORD_FILE}"
    printf '%s\n' "${E2E_ADMIN_PASSWORD}" >"${ADMIN_PASSWORD_FILE}"
    trap 'rm -f "${ADMIN_PASSWORD_FILE}"' EXIT
    deploy_args+=(--reset-data --admin-username "${E2E_ADMIN_USERNAME}" --admin-password-file "${ADMIN_PASSWORD_FILE}")
  fi
  "${ROOT}/scripts/deploy-moox.sh" "${deploy_args[@]}"
else
  log "skip deploy; reuse existing services"
fi

log "import storage metadata seeds"
if [[ "${SYSDEPLOY_ONLY}" -eq 1 ]]; then
  verify_phase "sysdeploy"
  log "end-to-end test passed"
  exit 0
fi
import_seed "platform-local.seed.yaml"
import_seed "metadata-crypto.seed.yaml"
import_seed "metadata-crypto-spot-kline-1m-view.seed.yaml"

log "prepare management/backend state"
verify_phase "prepare"

log "run collector SCF runtime once"
run_scf_once

log "assert collector/cloudnode/storage results"
verify_phase "assert"

log "end-to-end test passed"
