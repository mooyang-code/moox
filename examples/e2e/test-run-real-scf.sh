#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-real-scf-test.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/node" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${MOOX_TEST_NODE_CALLS}"
printf '[fake-verify] phase=%s\n' "$3"
if [[ "${MOOX_TEST_FAIL_PHASE:-}" == "$3" ]]; then
  exit 23
fi
EOF
chmod +x "${TMP}/bin/node"

state_file="${TMP}/state.json"
log_file="${TMP}/real-scf.log"
calls_file="${TMP}/node-calls"

PATH="${TMP}/bin:${PATH}" \
MOOX_TEST_NODE_CALLS="${calls_file}" \
"${ROOT}/examples/e2e/run-real-scf.sh" \
  --gateway "http://106.53.107.122:11000" \
  --web "http://106.53.107.122:9527" \
  --host "106.53.107.122" \
  --e2e-node "control" \
  --state-file "${state_file}" \
  --log-file "${log_file}" \
  --timeout-seconds 240

[[ -f "${state_file}" ]] || {
  echo "real SCF runner did not preserve its state file" >&2
  exit 1
}
[[ -f "${log_file}" ]] || {
  echo "real SCF runner did not preserve its log file" >&2
  exit 1
}

calls=()
call_count=0
while IFS= read -r line; do
  calls[call_count]="${line}"
  call_count=$((call_count + 1))
done <"${calls_file}"
[[ "${call_count}" -eq 4 ]] || {
  printf 'node calls=%s, want 4\n' "${#calls[@]}" >&2
  exit 1
}
for index in 0 1 2 3; do
  expected=(setup schedule assert cleanup)
  [[ "${calls[index]}" == *"--phase ${expected[index]}"* ]] || {
    printf 'call %s does not run phase %s: %s\n' "${index}" "${expected[index]}" "${calls[index]}" >&2
    exit 1
  }
  [[ "${calls[index]}" == *"--state-file ${state_file}"* ]] || {
    printf 'call %s does not share state file: %s\n' "${index}" "${calls[index]}" >&2
    exit 1
  }
  [[ "${calls[index]}" == *"--e2e-node control"* ]] || {
    printf 'call %s does not use the selected SysDeploy node: %s\n' "${index}" "${calls[index]}" >&2
    exit 1
  }
  [[ "${calls[index]}" == *"--rule moox_real_scf_e2e_kline_1h"* ]] || {
    printf 'call %s does not use the isolated real-SCF rule: %s\n' "${index}" "${calls[index]}" >&2
    exit 1
  }
done
[[ "${calls[0]}" == *"--skip-cloud-node-setup"* ]] || {
  echo "setup did not skip the synthetic CloudNode" >&2
  exit 1
}
if rg -q 'run-scf-(resident|once)\.sh|moox-collector-scf' \
  "${ROOT}/examples/e2e/run-real-scf.sh"; then
  echo "real SCF runner must not start a local SCF runtime" >&2
  exit 1
fi
rg -q '\[fake-verify\] phase=setup' "${log_file}"
rg -q '\[fake-verify\] phase=schedule' "${log_file}"
rg -q '\[fake-verify\] phase=assert' "${log_file}"

failed_state="${TMP}/failed-state.json"
failed_log="${TMP}/failed-real-scf.log"
failed_output="${TMP}/failed-output"
: >"${calls_file}"
if PATH="${TMP}/bin:${PATH}" \
  MOOX_TEST_NODE_CALLS="${calls_file}" \
  MOOX_TEST_FAIL_PHASE="schedule" \
  "${ROOT}/examples/e2e/run-real-scf.sh" \
    --gateway "http://106.53.107.122:11000" \
    --web "http://106.53.107.122:9527" \
    --host "106.53.107.122" \
    --state-file "${failed_state}" \
    --log-file "${failed_log}" >"${failed_output}" 2>&1; then
  echo "real SCF runner unexpectedly ignored a failed phase" >&2
  exit 1
fi
[[ -f "${failed_state}" && -f "${failed_log}" ]]
rg -q "artifacts: state=${failed_state} log=${failed_log}" "${failed_output}"
rg -q -- '--phase cleanup' "${calls_file}"
if rg -q '\[real-scf-e2e\] PASS' "${failed_output}"; then
  echo "failed real SCF run must not print PASS" >&2
  exit 1
fi

echo "run-real-scf contract: PASS"
