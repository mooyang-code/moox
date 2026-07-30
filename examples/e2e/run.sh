#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="${MOOX_E2E_TARGET:-localhost}"
DEPLOY_DIR="${MOOX_E2E_DEPLOY_DIR:-/tmp/moox-e2e}"
PUBLIC_HOST="${MOOX_E2E_PUBLIC_HOST:-}"
DEPLOY=1
RESET_DATA=1
TIMEOUT_SECONDS=120
SPACE_ID="crypto"
RULE_ID="binance_spot_kline_1h"
NODE_ID="e2e-scf-node"
PACKAGE_ID="moox-collector_dev"
DATASET_ID="spot_kline_1h"
SYSDEPLOY_ONLY=0
E2E_ADMIN_USERNAME="mooxe2eadmin"
E2E_ADMIN_PASSWORD="MooxE2E#20260704!"
E2E_NODE_ID="${MOOX_E2E_NODE_ID:-e2e-gateway}"
E2E_MONITOR_INSTANCE_ID="${MOOX_E2E_MONITOR_INSTANCE_ID:-e2e-monitor}"
E2E_GATEWAY_CONTROL_URL="${MOOX_E2E_GATEWAY_CONTROL_URL:-http://127.0.0.1:11000}"
E2E_GATEWAY_INPUT_DIR=""
E2E_STATE_FILE="$(mktemp "${TMPDIR:-/tmp}/moox-e2e-state.XXXXXX")"
SCF_RUNTIME_PID=""
SCF_RUNTIME_REMOTE=0

cleanup_e2e() {
  if [[ "${SCF_RUNTIME_REMOTE}" -eq 1 ]]; then
    ssh "${TARGET}" bash -s -- "${DEPLOY_DIR}" <<'EOF' >/dev/null 2>&1 || true
deploy=$1
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
pid_file="${deploy}/run/e2e-collector-scf.pid"
if [[ -r "${pid_file}" ]]; then
  pid="$(cat "${pid_file}")"
  kill "${pid}" 2>/dev/null || true
  rm -f "${pid_file}"
fi
EOF
  elif [[ -n "${SCF_RUNTIME_PID}" ]]; then
    kill "${SCF_RUNTIME_PID}" 2>/dev/null || true
    wait "${SCF_RUNTIME_PID}" 2>/dev/null || true
  fi
  [[ -z "${E2E_GATEWAY_INPUT_DIR}" ]] || rm -rf "${E2E_GATEWAY_INPUT_DIR}"
  rm -f "${E2E_STATE_FILE}"
}
trap cleanup_e2e EXIT

export MOOX_ADMIN_JWT_SECRET_KEY="${MOOX_ADMIN_JWT_SECRET_KEY:-moox-e2e-jwt-secret-key-20260713-safe}"
export MOOX_EVENTBUS_STREAM_MAX_BYTES="${MOOX_EVENTBUS_STREAM_MAX_BYTES:-104857600}"
export MOOX_EVENTBUS_ENABLE_TLS="${MOOX_EVENTBUS_ENABLE_TLS:-1}"

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
  --timeout-seconds <n>           Runtime/assert timeout. Default: 120.
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

prepare_gateway_inputs() {
  if ! is_local_target; then
    return
  fi
  command -v openssl >/dev/null 2>&1 || fail "openssl is required for local Gateway E2E inputs"
  E2E_GATEWAY_INPUT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/moox-e2e-gateway.XXXXXX")"
  umask 077
  openssl rand -hex 32 >"${E2E_GATEWAY_INPUT_DIR}/control.key"
  openssl rand -hex 32 >"${E2E_GATEWAY_INPUT_DIR}/service.key"
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=moox-e2e-peer-one \
    -keyout /dev/null -out "${E2E_GATEWAY_INPUT_DIR}/peer-one.pem" >/dev/null 2>&1
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=moox-e2e-peer-two \
    -keyout /dev/null -out "${E2E_GATEWAY_INPUT_DIR}/peer-two.pem" >/dev/null 2>&1
  cat "${E2E_GATEWAY_INPUT_DIR}/peer-one.pem" "${E2E_GATEWAY_INPUT_DIR}/peer-two.pem" >"${E2E_GATEWAY_INPUT_DIR}/peers.pem"
}

append_gateway_deploy_args() {
  deploy_args+=(--node-id "${E2E_NODE_ID}" --gateway-control-url "${E2E_GATEWAY_CONTROL_URL}" --monitor-instance-id "${E2E_MONITOR_INSTANCE_ID}")
  if is_local_target; then
    deploy_args+=(
      --gateway-control-key-file "${E2E_GATEWAY_INPUT_DIR}/control.key"
      --gateway-service-key-file "${E2E_GATEWAY_INPUT_DIR}/service.key"
      --gateway-ca-bundle "${E2E_GATEWAY_INPUT_DIR}/peers.pem"
    )
  else
    : "${MOOX_E2E_GATEWAY_CONTROL_KEY_FILE:?set MOOX_E2E_GATEWAY_CONTROL_KEY_FILE for remote E2E}"
    : "${MOOX_E2E_GATEWAY_SERVICE_KEY_FILE:?set MOOX_E2E_GATEWAY_SERVICE_KEY_FILE for remote E2E}"
    : "${MOOX_E2E_GATEWAY_CA_BUNDLE:?set MOOX_E2E_GATEWAY_CA_BUNDLE for remote E2E}"
    deploy_args+=(
      --gateway-control-key-file "${MOOX_E2E_GATEWAY_CONTROL_KEY_FILE}"
      --gateway-service-key-file "${MOOX_E2E_GATEWAY_SERVICE_KEY_FILE}"
      --gateway-ca-bundle "${MOOX_E2E_GATEWAY_CA_BUNDLE}"
    )
  fi
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
prepare_gateway_inputs
if [[ "${SYSDEPLOY_ONLY}" -eq 1 ]] && ! is_local_target; then
  fail "--sysdeploy-only currently supports localhost targets only"
fi

run_remote() {
  local quoted_dir
  quoted_dir="$(shell_quote "${DEPLOY_DIR}")"
  ssh "${TARGET}" "cd ${quoted_dir} && $*"
}

import_seed() {
  local seed="$1"
  local space="${2:-}"
  local attempt
  for attempt in 1 2 3 4 5; do
    if is_local_target; then
      local deploy_dir
      deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
      if [[ -n "${space}" ]]; then
        if "${deploy_dir}/bin/moox-cli" metadata import \
          --metadata-url "http://127.0.0.1:20200" \
          --file "${deploy_dir}/examples/${seed}" \
          --spaces "${space}" \
          --if-not-exists; then
          return 0
        fi
      elif "${deploy_dir}/bin/moox-cli" metadata import \
        --metadata-url "http://127.0.0.1:20200" \
        --file "${deploy_dir}/examples/${seed}" \
        --if-not-exists; then
        return 0
      fi
    else
      local remote_space=""
      if [[ -n "${space}" ]]; then
        remote_space=" --spaces $(shell_quote "${space}")"
      fi
      if run_remote "./bin/moox-cli metadata import --metadata-url http://127.0.0.1:20200 --file ./examples/${seed}${remote_space} --if-not-exists"; then
        return 0
      fi
    fi
    [[ "${attempt}" -eq 5 ]] || sleep 1
  done
  return 1
}

activate_storage_datasets() {
  if is_local_target; then
    local deploy_dir
    deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
    [[ -r "${deploy_dir}/secrets/storage-node-auth.env" ]] ||
      fail "missing storage-node-auth.env"
    (
      set -a
      # shellcheck disable=SC1091
      source "${deploy_dir}/secrets/storage-node-auth.env"
      set +a
      "${deploy_dir}/bin/moox-storage-cli" activate-datasets \
        --metadata-target "ip://127.0.0.1:20100"
    )
    return
  fi
  ssh "${TARGET}" bash -s -- "${DEPLOY_DIR}" <<'EOF'
set -euo pipefail
deploy=$1
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
[[ -r "${deploy}/secrets/storage-node-auth.env" ]]
set -a
source "${deploy}/secrets/storage-node-auth.env"
set +a
"${deploy}/bin/moox-storage-cli" activate-datasets \
  --metadata-target "ip://127.0.0.1:20100"
EOF
}

ensure_admin_user() {
  if is_local_target; then
    local deploy_dir
    deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
    printf '%s\n' "${E2E_ADMIN_PASSWORD}" | "${deploy_dir}/bin/moox-admin-cli" user ensure \
      --db-path "${deploy_dir}/data/admin.db" \
      --username "${E2E_ADMIN_USERNAME}" \
      --password-stdin
    return
  fi
  run_remote "printf '%s\\n' $(shell_quote "${E2E_ADMIN_PASSWORD}") | ./bin/moox-admin-cli user ensure --db-path ./data/admin.db --username $(shell_quote "${E2E_ADMIN_USERNAME}") --password-stdin"
}

start_scf_runtime() {
  if is_local_target; then
    local deploy_dir
    deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
    mkdir -p "${deploy_dir}/log"
    "${ROOT}/examples/e2e/run-scf-resident.sh" "${deploy_dir}" "${NODE_ID}" "${SPACE_ID}" \
      >"${deploy_dir}/log/e2e-collector-scf.log" 2>&1 &
    SCF_RUNTIME_PID=$!
    sleep 1
    kill -0 "${SCF_RUNTIME_PID}" 2>/dev/null || fail "resident collector SCF runtime exited during startup"
    return
  fi
  ssh "${TARGET}" bash -s -- "${DEPLOY_DIR}" "${NODE_ID}" "${SPACE_ID}" <<'EOF'
deploy=$1
node_id=$2
space_id=$3
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
mkdir -p "${deploy}/run" "${deploy}/log"
nohup "${deploy}/examples/e2e/run-scf-resident.sh" "${deploy}" "${node_id}" "${space_id}" \
  >"${deploy}/log/e2e-collector-scf.log" 2>&1 &
echo "$!" >"${deploy}/run/e2e-collector-scf.pid"
sleep 1
kill -0 "$(cat "${deploy}/run/e2e-collector-scf.pid")"
EOF
  SCF_RUNTIME_REMOTE=1
}

scf_log_has() {
  local item_id="$1"
  local event="$2"
  local decision="${3:-}"
  if is_local_target; then
    local deploy_dir
    deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
    awk -v item_id="${item_id}" -v event="${event}" -v decision="${decision}" '
      index($0, "job_item_id=\"" item_id "\"") &&
      index($0, "event=\"" event "\"") &&
      (decision == "" || index($0, "decision=\"" decision "\"")) {
        found = 1
        exit
      }
      END { exit(found ? 0 : 1) }
    ' "${deploy_dir}/log/e2e-collector-scf.log" 2>/dev/null
    return
  fi
  ssh "${TARGET}" bash -s -- "${DEPLOY_DIR}" "${item_id}" "${event}" "${decision}" <<'EOF'
deploy=$1
item_id=$2
event=$3
decision=$4
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
awk -v item_id="${item_id}" -v event="${event}" -v decision="${decision}" '
  index($0, "job_item_id=\"" item_id "\"") &&
  index($0, "event=\"" event "\"") &&
  (decision == "" || index($0, "decision=\"" decision "\"")) {
    found = 1
    exit
  }
  END { exit(found ? 0 : 1) }
' "${deploy}/log/e2e-collector-scf.log" 2>/dev/null
EOF
}

assert_scf_deferred() {
  local item_id
  item_id="$(node -e 'const fs=require("node:fs"); const s=JSON.parse(fs.readFileSync(process.argv[1], "utf8")); process.stdout.write(s.scheduled_job_ids?.[0] || "")' "${E2E_STATE_FILE}")"
  [[ -n "${item_id}" ]] || fail "scheduled job id missing from E2E state"
  local deadline=$((SECONDS + 15))
  while (( SECONDS < deadline )); do
    if scf_log_has "${item_id}" "collector_job_deferred" &&
      scf_log_has "${item_id}" "collector_job_delivery_action" "RETRY"; then
      log "scheduled job deferred by resident SCF before execute_at: ${item_id}"
      return
    fi
    sleep 0.25
  done
  fail "resident SCF did not log deferred/RETRY before execute_at for ${item_id}"
}

verify_phase() {
  local phase="$1"
  local web_url="http://${PUBLIC_HOST}:9527"
  local health_url="http://${PUBLIC_HOST}:11000/api/admin/health"
  if is_local_target; then
    web_url="http://127.0.0.1:9528"
    health_url="http://127.0.0.1:11010/healthz"
  fi
  node "${ROOT}/examples/e2e/verify.mjs" \
    --phase "${phase}" \
    --gateway "http://${PUBLIC_HOST}:11000" \
    --health "${health_url}" \
    --web "${web_url}" \
    --host "${PUBLIC_HOST}" \
    --space "${SPACE_ID}" \
    --rule "${RULE_ID}" \
    --node "${NODE_ID}" \
    --e2e-node "${E2E_NODE_ID}" \
    --package "${PACKAGE_ID}" \
    --dataset "${DATASET_ID}" \
    --state-file "${E2E_STATE_FILE}" \
    --timeout-seconds "${TIMEOUT_SECONDS}"
}

log "target=${TARGET} dir=${DEPLOY_DIR} public_host=${PUBLIC_HOST} reset_data=${RESET_DATA}"

if [[ "${DEPLOY}" -eq 1 ]]; then
  deploy_args=(--target "${TARGET}" --dir "${DEPLOY_DIR}")
  append_gateway_deploy_args
  if [[ "${RESET_DATA}" -eq 1 ]]; then
    deploy_args+=(--reset-data)
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
import_seed "metadata-quant-initial.seed.yaml" "${SPACE_ID}"

log "activate imported storage Datasets"
activate_storage_datasets

log "prepare management/backend state and queue topology"
ensure_admin_user
verify_phase "setup"

log "start resident collector SCF runtime"
start_scf_runtime

log "schedule future collector jobs"
verify_phase "schedule"
assert_scf_deferred

log "assert collector/cloudnode/storage results"
verify_phase "assert"

log "end-to-end test passed"
