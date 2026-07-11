#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-market-verify.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

cat > "${TMP}/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
command="${*:2}"
printf '%s\n' "${command}" >> "${FAKE_SSH_LOG}"
if [[ "${command}" == *"market status"* ]]; then
  market="$(printf '%s' "${command}" | sed -n "s/.*--market '\([^']*\)'.*/\1/p")"
  printf '{"ret_info":{"code":"SUCCESS"},"module":{"market_id":"%s"}}\n' "${market}"
elif [[ "${command}" == *"legacy-cutover"* ]]; then
  printf '%s\n' '{"mode":"preflight","legacy_space":"crypto","pending_canceled":0,"running_job_items":[]}'
elif [[ "${command}" == *" init "* ]]; then
  printf '%s\n' '{"total":{"applied":0,"failed":0}}'
elif [[ "${command}" == *"market kline refresh"* ]]; then
  printf '%s\n' '{"ret_info":{"code":"SUCCESS"},"task_ids":["task"]}'
elif [[ "${command}" == *"market kline query"* ]]; then
  printf '%s\n' '{"ret_info":{"code":"SUCCESS"},"rows":[{"open":"1","close":"1","source_provider":"fixture"}],"freshness":"fresh","coverage_status":"complete"}'
fi
SH
chmod +x "${TMP}/ssh"
export FAKE_SSH_LOG="${TMP}/ssh.log"
export SSH_BIN="${TMP}/ssh"
export MOOX_DEV_SSH_TARGET="fixture"
export MOOX_DEPLOY_DIR="/srv/moox"
export MOOX_VERIFY_MARKET_ID="crypto_binance"
export MOOX_VERIFY_SUBJECT_ID="BTC-USDT"
export MOOX_VERIFY_FREQUENCY="1m"
export MOOX_VERIFY_START="2026-07-11T00:00:00Z"
export MOOX_VERIFY_END="2026-07-11T00:05:00Z"
export MOOX_VERIFY_WAIT_SECONDS=0
export MOOX_VERIFY_REQUIRE_GAP=0
"${ROOT}/scripts/verify-market-remote.sh" >/dev/null
grep -q 'moox_collector_market_v2.db' "${FAKE_SSH_LOG}"
grep -q 'market kline refresh' "${FAKE_SSH_LOG}"
if grep -Eq 'reset|rm -rf|token=|secret=' "${FAKE_SSH_LOG}"; then
  echo "remote verifier contains destructive or secret-bearing command" >&2
  exit 1
fi
