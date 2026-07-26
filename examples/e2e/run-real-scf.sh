#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GATEWAY="${MOOX_E2E_GATEWAY:-http://127.0.0.1:11000}"
WEB="${MOOX_E2E_WEB:-http://127.0.0.1:9527}"
HOST="${MOOX_E2E_PUBLIC_HOST:-127.0.0.1}"
SPACE_ID="${MOOX_E2E_SPACE_ID:-crypto}"
RULE_ID="${MOOX_E2E_RULE_ID:-moox_real_scf_e2e_kline_1h}"
DATASET_ID="${MOOX_E2E_DATASET_ID:-binance_spot_kline_1h}"
E2E_NODE_ID="${MOOX_E2E_NODE_ID:-e2e-gateway}"
TIMEOUT_SECONDS="${MOOX_E2E_TIMEOUT_SECONDS:-240}"
USERNAME="${MOOX_E2E_ADMIN_USERNAME:-mooxe2eadmin}"
PASSWORD="${MOOX_E2E_ADMIN_PASSWORD:-MooxE2E#20260704!}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
STATE_FILE="${MOOX_E2E_STATE_FILE:-${TMPDIR:-/tmp}/moox-real-scf-${RUN_ID}.json}"
LOG_FILE="${MOOX_E2E_LOG_FILE:-${TMPDIR:-/tmp}/moox-real-scf-${RUN_ID}.log}"

usage() {
  cat <<'EOF'
Usage:
  examples/e2e/run-real-scf.sh [options]

Options:
  --gateway <url>          Admin gateway URL.
  --web <url>              Web host URL.
  --host <host>            Public host written to SysDeploy endpoints.
  --space <space_id>       Space ID. Default: crypto.
  --rule <rule_id>         Collector rule ID.
  --dataset <dataset_id>   Storage Dataset ID.
  --e2e-node <node_id>     SysDeploy node used by management checks.
  --state-file <path>      Preserved JSON run state.
  --log-file <path>        Preserved verification log.
  --timeout-seconds <n>    Per-assertion timeout. Default: 240.
  -h, --help               Show this help.

This runner uses already published Tencent SCF nodes. It never starts a local
collector SCF process. Publish and verify the real cloud nodes before running it.
EOF
}

fail() {
  printf '[real-scf-e2e] ERROR: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --gateway) GATEWAY="${2:-}"; shift 2 ;;
    --web) WEB="${2:-}"; shift 2 ;;
    --host) HOST="${2:-}"; shift 2 ;;
    --space) SPACE_ID="${2:-}"; shift 2 ;;
    --rule) RULE_ID="${2:-}"; shift 2 ;;
    --dataset) DATASET_ID="${2:-}"; shift 2 ;;
    --e2e-node) E2E_NODE_ID="${2:-}"; shift 2 ;;
    --state-file) STATE_FILE="${2:-}"; shift 2 ;;
    --log-file) LOG_FILE="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

[[ -n "${GATEWAY}" && -n "${WEB}" && -n "${HOST}" ]] ||
  fail "gateway, web, and host must be non-empty"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] ||
  fail "--timeout-seconds must be a positive integer"

mkdir -p "$(dirname "${STATE_FILE}")" "$(dirname "${LOG_FILE}")"
: >"${STATE_FILE}"
: >"${LOG_FILE}"
chmod 600 "${STATE_FILE}" "${LOG_FILE}"
printf '[real-scf-e2e] artifacts: state=%s log=%s\n' "${STATE_FILE}" "${LOG_FILE}"

run_phase() {
  local phase="$1"
  local verify_args=(
    --phase "${phase}"
    --gateway "${GATEWAY}"
    --web "${WEB}"
    --host "${HOST}"
    --space "${SPACE_ID}"
    --rule "${RULE_ID}"
    --dataset "${DATASET_ID}"
    --e2e-node "${E2E_NODE_ID}"
    --username "${USERNAME}"
    --password "${PASSWORD}"
    --state-file "${STATE_FILE}"
    --timeout-seconds "${TIMEOUT_SECONDS}"
  )
  if [[ "${phase}" == "setup" ]]; then
    verify_args+=(--skip-cloud-node-setup)
  fi
  printf '[real-scf-e2e] phase=%s\n' "${phase}" | tee -a "${LOG_FILE}"
  node "${ROOT}/examples/e2e/verify.mjs" "${verify_args[@]}" 2>&1 | tee -a "${LOG_FILE}"
}

cleanup_on_exit() {
  local original_status=$?
  trap - EXIT
  set +e
  run_phase cleanup
  local cleanup_status=$?
  if [[ "${original_status}" -eq 0 && "${cleanup_status}" -ne 0 ]]; then
    original_status="${cleanup_status}"
  fi
  if [[ "${original_status}" -eq 0 ]]; then
    printf '[real-scf-e2e] PASS state=%s log=%s\n' "${STATE_FILE}" "${LOG_FILE}"
    printf '[real-scf-e2e] Use the scheduled_job_ids, immediate_job_item_id, and failure_job_item_id in state for CLS queries.\n'
  fi
  exit "${original_status}"
}

trap cleanup_on_exit EXIT
run_phase setup
run_phase schedule
run_phase assert
