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
PUBLISH_STATUS_FILE="${MOOX_E2E_PUBLISH_STATUS_FILE:-${TMPDIR:-/tmp}/moox-symbol-kline-publish-status-${RUN_ID}.json}"
PUBLISH_TIMEOUT_SECONDS="${MOOX_E2E_PUBLISH_TIMEOUT_SECONDS:-1800}"
MOOX_CLI="${MOOX_E2E_MOOX_CLI:-}"
EVENTBUS_CREDENTIAL_FILE="${MOOX_E2E_EVENTBUS_CREDENTIAL_FILE:-${HOME}/.config/moox/eventbus/cloudnode-worker.yaml}"
COMPLETED=0

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
if [[ -z "${MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID:-}" ||
      -z "${MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY:-}" ]]; then
  fail "SCF runtime requires MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID/MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY"
fi
[[ "${SCF_COUNT}" =~ ^[0-9]+$ && "${SCF_COUNT}" -gt 0 ]] ||
  fail "--scf-count must be a positive integer"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] ||
  fail "--timeout-seconds must be a positive integer"
[[ "${PUBLISH_TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${PUBLISH_TIMEOUT_SECONDS}" -gt 0 ]] ||
  fail "MOOX_E2E_PUBLISH_TIMEOUT_SECONDS must be a positive integer"

mkdir -p "$(dirname "${STATE_FILE}")" "$(dirname "${LOG_FILE}")" \
  "$(dirname "${PUBLISH_SUMMARY_FILE}")" "$(dirname "${PUBLISH_STATUS_FILE}")"
: >"${STATE_FILE}"
: >"${LOG_FILE}"
: >"${PUBLISH_SUMMARY_FILE}"
: >"${PUBLISH_STATUS_FILE}"
chmod 600 "${STATE_FILE}" "${LOG_FILE}" "${PUBLISH_SUMMARY_FILE}" "${PUBLISH_STATUS_FILE}"
printf '[symbol-kline-scf-e2e] artifacts: state=%s publish=%s publish_status=%s log=%s\n' \
  "${STATE_FILE}" "${PUBLISH_SUMMARY_FILE}" "${PUBLISH_STATUS_FILE}" "${LOG_FILE}" | tee -a "${LOG_FILE}"

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
    collector function publish submit
    --control-url "${CONTROL_URL}"
    --space-id "${SPACE_ID}"
    --cloud-account-id "${CLOUD_ACCOUNT}"
    --package-name "${PACKAGE_NAME}"
    --version "${PACKAGE_VERSION}"
    --region "${REGION}"
    --function-name-prefix "${FLEET_PREFIX}"
    --node-count "${SCF_COUNT}"
    --collector-root "${ROOT}/modules/collector"
    --eventbus-credential-file "${EVENTBUS_CREDENTIAL_FILE}"
    --env "MOOX_COLLECTOR_ADMIN_GATEWAY_URL=${CONTROL_URL}"
    --env "MOOX_SERVICE_GATEWAY_TARGET=${CONTROL_URL}"
  )
  if [[ -n "${ZIP_PATH}" ]]; then
    args+=(--zip "${ZIP_PATH}")
  fi
  printf '[symbol-kline-scf-e2e] phase=fleet-publish\n' | tee -a "${LOG_FILE}"
  run_moox_cli "${args[@]}" >"${PUBLISH_SUMMARY_FILE}"
  chmod 600 "${PUBLISH_SUMMARY_FILE}"
  local job_id
  job_id="$(node -e '
    const fs = require("node:fs");
    const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    if (!value.job_id) process.exit(2);
    process.stdout.write(value.job_id);
  ' "${PUBLISH_SUMMARY_FILE}")" || fail "publish submit did not return job_id"

  local deadline=$((SECONDS + PUBLISH_TIMEOUT_SECONDS))
  while (( SECONDS < deadline )); do
    if ! run_moox_cli collector function publish status \
      --control-url "${CONTROL_URL}" \
      --space-id "${SPACE_ID}" \
      --job-id "${job_id}" >"${PUBLISH_STATUS_FILE}"; then
      printf '[symbol-kline-scf-e2e] transient publish status error job_id=%s\n' "${job_id}" | tee -a "${LOG_FILE}"
      sleep 2
      continue
    fi
    chmod 600 "${PUBLISH_STATUS_FILE}"
    local status
    status="$(node -e '
      const fs = require("node:fs");
      const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
      process.stdout.write(String(value.job?.status || ""));
    ' "${PUBLISH_STATUS_FILE}")"
    case "${status}" in
      NODE_BATCH_STATUS_PENDING|NODE_BATCH_STATUS_RUNNING)
        sleep 2
        ;;
      NODE_BATCH_STATUS_SUCCESS)
        node -e '
          const fs = require("node:fs");
          const submit = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
          const status = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
          fs.writeFileSync(process.argv[1], `${JSON.stringify({ ...submit, ...status }, null, 2)}\n`, { mode: 0o600 });
        ' "${PUBLISH_SUMMARY_FILE}" "${PUBLISH_STATUS_FILE}"
        return
        ;;
      NODE_BATCH_STATUS_FAILED|NODE_BATCH_STATUS_PARTIAL)
        node -e '
          const fs = require("node:fs");
          const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
          for (const item of value.items || []) {
            if (item.status === "NODE_BATCH_ITEM_STATUS_FAILED") {
              console.error(`failed item_id=${item.item_id || ""} node_id=${item.node_id || ""} error=${item.error_message || ""}`);
            }
          }
        ' "${PUBLISH_STATUS_FILE}" 2>&1 | tee -a "${LOG_FILE}"
        fail "publish job ${job_id} ended with ${status}"
        ;;
      *)
        fail "publish status returned unknown job status: ${status}"
        ;;
    esac
  done
  fail "publish job ${job_id} did not finish within ${PUBLISH_TIMEOUT_SECONDS}s; query it with publish status"
}

run_moox_cli() {
  if [[ -n "${MOOX_CLI}" ]]; then
    "${MOOX_CLI}" "$@"
    return
  fi
  (
    cd "${ROOT}/modules/cli"
    go run ./cmd/moox-cli "$@"
  )
}

cleanup_on_exit() {
  local original_status=$?
  trap - EXIT
  set +e
  run_phase cleanup
  local cleanup_status=$?
  if [[ "${original_status}" -eq 0 && "${COMPLETED}" -ne 1 ]]; then
    original_status=1
  fi
  if [[ "${original_status}" -eq 0 && "${cleanup_status}" -ne 0 ]]; then
    original_status="${cleanup_status}"
  fi
  if [[ "${original_status}" -eq 0 ]]; then
    printf '[symbol-kline-scf-e2e] PASS state=%s log=%s\n' "${STATE_FILE}" "${LOG_FILE}" |
      tee -a "${LOG_FILE}"
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
COMPLETED=1
