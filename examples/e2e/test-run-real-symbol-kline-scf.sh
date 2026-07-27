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
cat >"${TMP}/bin/go" <<'EOF'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"${CALL_LOG}"
printf '{"zip_path":"/tmp/collector.zip","package_id":"pkg-new","fleet_mode":"created","create_batch_ids":["b1"],"create_processed_count":50}\n'
EOF
cat >"${TMP}/bin/node" <<'EOF'
#!/usr/bin/env bash
printf 'node %s\n' "$*" >>"${CALL_LOG}"
EOF
chmod +x "${TMP}/bin/go" "${TMP}/bin/node"

export PATH="${TMP}/bin:${PATH}"
export CALL_LOG="${TMP}/calls.log"
export MOOX_E2E_STATE_FILE="${TMP}/state.json"
export MOOX_E2E_LOG_FILE="${TMP}/run.log"
export MOOX_E2E_PUBLISH_SUMMARY_FILE="${TMP}/publish.json"
export MOOX_E2E_ADMIN_PASSWORD="test-password"
export MOOX_ACCESS_TOKEN="test-access-token"

"${RUNNER}" \
  --cloud-account account-a \
  --package-name moox-collector \
  --package-version test \
  --region ap-guangzhou \
  --control-url https://service.example.test \
  --fleet-prefix e2e-collector

grep -q -- '--node-count 50' "${CALL_LOG}" || fail "default SCF count was not 50"
grep -q -- '--create-batch-size 5' "${CALL_LOG}" || fail "create batch size was not 5"
grep -q -- '--deploy-batch-size 1' "${CALL_LOG}" || fail "deploy batch size was not 1"
grep -q -- '--function-name-prefix e2e-collector' "${CALL_LOG}" || fail "fleet prefix missing"
grep -q -- '--control-url https://service.example.test' "${CALL_LOG}" || fail "service control URL missing"
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

if grep -q 'BatchDeleteNodes' "${CALL_LOG}"; then
  fail "runner must not call BatchDeleteNodes"
fi

for file in "${MOOX_E2E_STATE_FILE}" "${MOOX_E2E_LOG_FILE}" "${MOOX_E2E_PUBLISH_SUMMARY_FILE}"; do
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

printf '[runner-test] PASS\n'
