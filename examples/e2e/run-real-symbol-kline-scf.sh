#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="$(date -u +%Y%m%dt%H%M%Sz)"
GATEWAY="${MOOX_E2E_GATEWAY:-http://127.0.0.1:11000}"
CONTROL_URL="${MOOX_E2E_CONTROL_URL:-${GATEWAY}}"
WEB="${MOOX_E2E_WEB:-http://127.0.0.1:9527}"
SPACE_ID="${MOOX_E2E_SPACE_ID:-crypto}"
SYMBOL_DATASET="${MOOX_E2E_SYMBOL_DATASET:-e2e_binance_symbols}"
KLINE_DATASET="${MOOX_E2E_KLINE_DATASET:-e2e_binance_kline_1m}"
SYMBOL_RULE="${MOOX_E2E_SYMBOL_RULE:-e2e_symbols_${RUN_ID}}"
KLINE_RULE="${MOOX_E2E_KLINE_RULE:-e2e_kline_${RUN_ID}}"
CLOUD_ACCOUNT="${MOOX_E2E_CLOUD_ACCOUNT:-}"
PACKAGE_NAME="${MOOX_E2E_PACKAGE_NAME:-}"
PACKAGE_VERSION="${MOOX_E2E_PACKAGE_VERSION:-$(date -u +%Y%m%dT%H%M%SZ)}"
ZIP_PATH="${MOOX_E2E_ZIP:-}"
REGION="${MOOX_E2E_REGION:-}"
FLEET_PREFIX="${MOOX_E2E_FLEET_PREFIX:-moox-e2e-collector}"
SCF_COUNT="${MOOX_E2E_SCF_COUNT:-50}"
TIMEOUT_SECONDS="${MOOX_E2E_TIMEOUT_SECONDS:-600}"
USERNAME="${MOOX_E2E_ADMIN_USERNAME:-mooxe2eadmin}"
PASSWORD="${MOOX_E2E_ADMIN_PASSWORD:-}"
STATE_FILE="${MOOX_E2E_STATE_FILE:-${TMPDIR:-/tmp}/moox-symbol-kline-${RUN_ID}.json}"
LOG_FILE="${MOOX_E2E_LOG_FILE:-${TMPDIR:-/tmp}/moox-symbol-kline-${RUN_ID}.log}"
PUBLISH_SUMMARY_FILE="${MOOX_E2E_PUBLISH_SUMMARY_FILE:-${TMPDIR:-/tmp}/moox-symbol-kline-publish-${RUN_ID}.json}"
MOOX_CLI="${MOOX_E2E_MOOX_CLI:-}"

usage() {
  cat <<'EOF'
Usage:
  examples/e2e/run-real-symbol-kline-scf.sh [options]

Required:
  --cloud-account <id> --package-name <name> --region <region>

Fleet:
  --package-version <version> --zip <path> --fleet-prefix <prefix>
  --scf-count <n>

Fixture:
  --gateway <url> --web <url> --space <id>
  --control-url <url>  Service Gateway base used by moox-cli fleet publish.
  --symbol-dataset <id> --kline-dataset <id>
  --symbol-rule <id> --kline-rule <id>
  --timeout-seconds <n> --state-file <path> --log-file <path>
EOF
}

fail() {
  printf '[symbol-kline-scf-e2e] ERROR: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --gateway) GATEWAY="${2:-}"; shift 2 ;;
    --control-url) CONTROL_URL="${2:-}"; shift 2 ;;
    --web) WEB="${2:-}"; shift 2 ;;
    --space) SPACE_ID="${2:-}"; shift 2 ;;
    --symbol-dataset) SYMBOL_DATASET="${2:-}"; shift 2 ;;
    --kline-dataset) KLINE_DATASET="${2:-}"; shift 2 ;;
    --symbol-rule) SYMBOL_RULE="${2:-}"; shift 2 ;;
    --kline-rule) KLINE_RULE="${2:-}"; shift 2 ;;
    --cloud-account) CLOUD_ACCOUNT="${2:-}"; shift 2 ;;
    --package-name) PACKAGE_NAME="${2:-}"; shift 2 ;;
    --package-version) PACKAGE_VERSION="${2:-}"; shift 2 ;;
    --zip) ZIP_PATH="${2:-}"; shift 2 ;;
    --region) REGION="${2:-}"; shift 2 ;;
    --fleet-prefix) FLEET_PREFIX="${2:-}"; shift 2 ;;
    --scf-count) SCF_COUNT="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --state-file) STATE_FILE="${2:-}"; shift 2 ;;
    --log-file) LOG_FILE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

[[ -n "${CLOUD_ACCOUNT}" ]] || fail "--cloud-account is required"
[[ -n "${PACKAGE_NAME}" ]] || fail "--package-name is required"
[[ -n "${REGION}" ]] || fail "--region is required"
[[ -n "${PASSWORD}" ]] || fail "MOOX_E2E_ADMIN_PASSWORD is required"
if [[ -z "${MOOX_ACCESS_TOKEN:-}" &&
      ( -z "${MOOX_GATEWAY_SERVICE_KEY_ID:-}" || -z "${MOOX_GATEWAY_SERVICE_SECRET_KEY:-}" ) ]]; then
  fail "fleet publish requires MOOX_ACCESS_TOKEN or MOOX_GATEWAY_SERVICE_KEY_ID/MOOX_GATEWAY_SERVICE_SECRET_KEY"
fi
[[ "${SCF_COUNT}" =~ ^[0-9]+$ && "${SCF_COUNT}" -gt 0 ]] ||
  fail "--scf-count must be a positive integer"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] ||
  fail "--timeout-seconds must be a positive integer"

mkdir -p "$(dirname "${STATE_FILE}")" "$(dirname "${LOG_FILE}")" "$(dirname "${PUBLISH_SUMMARY_FILE}")"
: >"${STATE_FILE}"
: >"${LOG_FILE}"
: >"${PUBLISH_SUMMARY_FILE}"
chmod 600 "${STATE_FILE}" "${LOG_FILE}" "${PUBLISH_SUMMARY_FILE}"
printf '[symbol-kline-scf-e2e] artifacts: state=%s publish=%s log=%s\n' \
  "${STATE_FILE}" "${PUBLISH_SUMMARY_FILE}" "${LOG_FILE}" | tee -a "${LOG_FILE}"

phase_args() {
  local phase="$1"
  PHASE_ARGS=(
    "${ROOT}/examples/e2e/collector-symbol-kline.mjs"
    --phase "${phase}"
    --gateway "${GATEWAY}"
    --web "${WEB}"
    --space "${SPACE_ID}"
    --symbol-dataset "${SYMBOL_DATASET}"
    --kline-dataset "${KLINE_DATASET}"
    --symbol-rule "${SYMBOL_RULE}"
    --kline-rule "${KLINE_RULE}"
    --cloud-account "${CLOUD_ACCOUNT}"
    --package-name "${PACKAGE_NAME}"
    --package-version "${PACKAGE_VERSION}"
    --region "${REGION}"
    --fleet-prefix "${FLEET_PREFIX}"
    --scf-count "${SCF_COUNT}"
    --timeout-seconds "${TIMEOUT_SECONDS}"
    --state-file "${STATE_FILE}"
    --username "${USERNAME}"
  )
  if [[ -n "${ZIP_PATH}" ]]; then
    PHASE_ARGS+=(--zip "${ZIP_PATH}")
  fi
  if [[ "${phase}" == "fleet" ]]; then
    PHASE_ARGS+=(--publish-summary "${PUBLISH_SUMMARY_FILE}")
  fi
}

run_phase() {
  local phase="$1"
  phase_args "${phase}"
  printf '[symbol-kline-scf-e2e] phase=%s\n' "${phase}" | tee -a "${LOG_FILE}"
  MOOX_E2E_ADMIN_PASSWORD="${PASSWORD}" node "${PHASE_ARGS[@]}" 2>&1 | tee -a "${LOG_FILE}"
}

publish_fleet() {
  local args=(
    collector function publish
    --control-url "${CONTROL_URL}"
    --space-id "${SPACE_ID}"
    --cloud-account-id "${CLOUD_ACCOUNT}"
    --package-name "${PACKAGE_NAME}"
    --version "${PACKAGE_VERSION}"
    --region "${REGION}"
    --function-name-prefix "${FLEET_PREFIX}"
    --node-count "${SCF_COUNT}"
    --create-batch-size 5
    --collector-root "${ROOT}/modules/collector"
  )
  if [[ -n "${ZIP_PATH}" ]]; then
    args+=(--zip "${ZIP_PATH}")
  fi
  printf '[symbol-kline-scf-e2e] phase=fleet-publish\n' | tee -a "${LOG_FILE}"
  if [[ -n "${MOOX_CLI}" ]]; then
    "${MOOX_CLI}" "${args[@]}" >"${PUBLISH_SUMMARY_FILE}"
  else
    (
    cd "${ROOT}/modules/cli"
      go run ./cmd/moox-cli "${args[@]}"
    ) >"${PUBLISH_SUMMARY_FILE}"
  fi
  chmod 600 "${PUBLISH_SUMMARY_FILE}"
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
    printf '[symbol-kline-scf-e2e] PASS state=%s log=%s\n' "${STATE_FILE}" "${LOG_FILE}"
  fi
  exit "${original_status}"
}

trap cleanup_on_exit EXIT
run_phase setup
publish_fleet
run_phase fleet
run_phase symbols
run_phase klines
run_phase assert
