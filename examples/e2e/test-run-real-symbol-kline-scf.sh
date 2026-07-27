#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNNER="${ROOT}/examples/e2e/run-real-symbol-kline-scf.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

fail() {
  printf '[runner-test] ERROR: %s\n' "$*" >&2
  exit 1
}

mkdir -p "${TMP}/bin"
REAL_NODE="$(command -v node)"
cat >"${TMP}/bin/moox-cli" <<'EOF'
#!/usr/bin/env bash
printf 'moox-cli %s\n' "$*" >>"${CALL_LOG}"
if [[ "$*" == *" publish status "* ]]; then
  if [[ "${MOOX_TEST_STATUS_MODE:-}" == "transient_once" ]]; then
    count="$(cat "${STATUS_COUNT_FILE}" 2>/dev/null || printf '0')"
    printf '%s\n' "$((count + 1))" >"${STATUS_COUNT_FILE}"
    if [[ "${count}" -eq 0 ]]; then
      exit 1
    fi
  fi
  if [[ "${MOOX_TEST_STATUS_MODE:-}" == "failed" ]]; then
    printf '{"job":{"job_id":"node-batch-1","status":"NODE_BATCH_STATUS_FAILED","total_count":50,"success_count":0,"failed_count":50},"items":[{"item_id":"item-1","node_id":"node-1","status":"NODE_BATCH_ITEM_STATUS_FAILED","error_message":"publish failed"}]}\n'
    exit 0
  fi
  if [[ "${MOOX_TEST_STATUS_MODE:-}" == "running" ]]; then
    printf '{"job":{"job_id":"node-batch-1","status":"NODE_BATCH_STATUS_RUNNING","total_count":50,"success_count":0,"failed_count":0},"items":[]}\n'
    exit 0
  fi
  printf '{"job":{"job_id":"node-batch-1","status":"NODE_BATCH_STATUS_SUCCESS","total_count":50,"pending_count":0,"running_count":0,"success_count":50,"failed_count":0,"progress_percent":100},"items":['
  for index in $(seq 1 50); do
    [[ "${index}" -gt 1 ]] && printf ','
    printf '{"item_id":"item-%s","node_id":"node-%s","status":"NODE_BATCH_ITEM_STATUS_SUCCESS"}' "${index}" "${index}"
  done
  printf ']}\n'
  exit 0
fi
printf '{"zip_path":"/tmp/collector.zip","package_id":"pkg-new","fleet_mode":"created","job_id":"node-batch-1","operation":"create_nodes","total_count":50}\n'
EOF
cat >"${TMP}/bin/node" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-e" ]]; then
  exec "${REAL_NODE}" "$@"
fi
printf 'node %s\n' "$*" >>"${CALL_LOG}"
EOF
chmod +x "${TMP}/bin/moox-cli" "${TMP}/bin/node"

export PATH="${TMP}/bin:${PATH}"
export REAL_NODE
export CALL_LOG="${TMP}/calls.log"
export MOOX_E2E_STATE_FILE="${TMP}/state.json"
export MOOX_E2E_LOG_FILE="${TMP}/run.log"
export MOOX_E2E_PUBLISH_SUMMARY_FILE="${TMP}/publish.json"
export MOOX_E2E_PUBLISH_STATUS_FILE="${TMP}/publish-status.json"
export MOOX_E2E_ADMIN_PASSWORD="test-password"
export MOOX_ACCESS_TOKEN="test-access-token"
export MOOX_E2E_MOOX_CLI="${TMP}/bin/moox-cli"
export STATUS_COUNT_FILE="${TMP}/status-count"

"${RUNNER}" \
  --cloud-account account-a \
  --package-name moox-collector \
  --package-version test \
  --region ap-guangzhou \
  --control-url https://service.example.test \
  --fleet-prefix e2e-collector

grep -q -- 'collector function publish submit' "${CALL_LOG}" || fail "publish submit was not called"
grep -q -- 'collector function publish status' "${CALL_LOG}" || fail "publish status was not called"
grep -q -- '--node-count 50' "${CALL_LOG}" || fail "default SCF count was not 50"
grep -q -- '--function-name-prefix e2e-collector' "${CALL_LOG}" || fail "fleet prefix missing"
grep -q -- '--control-url https://service.example.test' "${CALL_LOG}" || fail "service control URL missing"
if grep -Eq -- '--create-batch-size|--deploy-batch-size' "${CALL_LOG}"; then
  fail "runner must not use removed client batch flags"
fi
grep -Eq -- '--symbol-rule e2e_symbols_[0-9]{8}t[0-9]{6}z' "${CALL_LOG}" ||
  fail "generated Symbol Rule ID must satisfy the lowercase E2E Rule contract"
grep -Eq -- '--kline-rule e2e_kline_[0-9]{8}t[0-9]{6}z' "${CALL_LOG}" ||
  fail "generated Kline Rule ID must satisfy the lowercase E2E Rule contract"
if grep -Eq -- '--password|test-password' "${CALL_LOG}"; then
  fail "admin password must not be exposed in process arguments"
fi

phase_lines="$(grep '^node ' "${CALL_LOG}")"
setup_line="$(printf '%s\n' "${phase_lines}" | grep -n -- '--phase setup' | cut -d: -f1)"
fleet_line="$(printf '%s\n' "${phase_lines}" | grep -n -- '--phase fleet' | cut -d: -f1)"
symbols_line="$(printf '%s\n' "${phase_lines}" | grep -n -- '--phase symbols' | cut -d: -f1)"
klines_line="$(printf '%s\n' "${phase_lines}" | grep -n -- '--phase klines' | cut -d: -f1)"
assert_line="$(printf '%s\n' "${phase_lines}" | grep -n -- '--phase assert' | cut -d: -f1)"
cleanup_line="$(printf '%s\n' "${phase_lines}" | grep -n -- '--phase cleanup' | tail -1 | cut -d: -f1)"
[[ "${setup_line}" -lt "${fleet_line}" && "${fleet_line}" -lt "${symbols_line}" &&
   "${symbols_line}" -lt "${klines_line}" && "${klines_line}" -lt "${assert_line}" &&
   "${assert_line}" -lt "${cleanup_line}" ]] || fail "phase order is incorrect"

for file in "${MOOX_E2E_STATE_FILE}" "${MOOX_E2E_LOG_FILE}" \
  "${MOOX_E2E_PUBLISH_SUMMARY_FILE}" "${MOOX_E2E_PUBLISH_STATUS_FILE}"; do
  [[ "$(stat -f '%Lp' "${file}")" == "600" ]] || fail "${file} must use mode 0600"
done

if "${RUNNER}" --package-name moox-collector --region ap-guangzhou >/dev/null 2>&1; then
  fail "missing cloud account must fail"
fi
if "${RUNNER}" --cloud-account account-a --region ap-guangzhou >/dev/null 2>&1; then
  fail "missing package name must fail"
fi
if "${RUNNER}" --cloud-account account-a --package-name moox-collector >/dev/null 2>&1; then
  fail "missing region must fail"
fi
if "${RUNNER}" --cloud-account account-a --package-name moox-collector --region ap-guangzhou --scf-count 0 >/dev/null 2>&1; then
  fail "non-positive SCF count must fail"
fi

reset_artifacts() {
  export MOOX_E2E_STATE_FILE="${TMP}/state-$1.json"
  export MOOX_E2E_LOG_FILE="${TMP}/run-$1.log"
  export MOOX_E2E_PUBLISH_SUMMARY_FILE="${TMP}/publish-$1.json"
  export MOOX_E2E_PUBLISH_STATUS_FILE="${TMP}/publish-status-$1.json"
  : >"${CALL_LOG}"
}

reset_artifacts failed
if MOOX_TEST_STATUS_MODE=failed "${RUNNER}" \
  --cloud-account account-a --package-name moox-collector --region ap-guangzhou >/dev/null 2>&1; then
  fail "failed publish job must fail the runner"
fi
grep -q -- '--phase cleanup' "${CALL_LOG}" || fail "failed publish must still run cleanup"

reset_artifacts transient
: >"${STATUS_COUNT_FILE}"
MOOX_TEST_STATUS_MODE=transient_once "${RUNNER}" \
  --cloud-account account-a --package-name moox-collector --region ap-guangzhou >/dev/null
[[ "$(cat "${STATUS_COUNT_FILE}")" -ge 2 ]] || fail "transient status error must be retried"

reset_artifacts timeout
if MOOX_TEST_STATUS_MODE=running MOOX_E2E_PUBLISH_TIMEOUT_SECONDS=1 "${RUNNER}" \
  --cloud-account account-a --package-name moox-collector --region ap-guangzhou >/dev/null 2>&1; then
  fail "publish status timeout must fail the runner"
fi

printf '[runner-test] PASS\n'
