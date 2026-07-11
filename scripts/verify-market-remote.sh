#!/usr/bin/env bash
set -euo pipefail

: "${MOOX_DEV_SSH_TARGET:?set user@host or SSH alias}"
: "${MOOX_DEPLOY_DIR:?set remote deploy directory}"
: "${MOOX_VERIFY_MARKET_ID:?set logical market id}"
: "${MOOX_VERIFY_SUBJECT_ID:?set verification subject id}"
: "${MOOX_VERIFY_FREQUENCY:?set verification frequency}"
: "${MOOX_VERIFY_START:?set RFC3339 verification start}"
: "${MOOX_VERIFY_END:?set RFC3339 verification end}"

SSH_BIN="${SSH_BIN:-ssh}"
REMOTE_CLI="${MOOX_REMOTE_CLI:-./bin/moox-cli}"
CONTROL_URL="${MOOX_REMOTE_CONTROL_URL:-http://127.0.0.1:11000}"
MARKET_CONTROL_URL="${MOOX_REMOTE_MARKET_CONTROL_URL:-http://127.0.0.1:11402}"
remote_prefix="cd '${MOOX_DEPLOY_DIR}' && test -r ./env.sh && set -a && . ./env.sh && set +a"

run_remote() {
  "${SSH_BIN}" "${MOOX_DEV_SSH_TARGET}" "${remote_prefix} && $1"
}

run_remote "test -f ./data/collector/moox_collector_market_v2.db"
run_remote "test -f ./data/collector/moox_collector.db.market-v2.backup || test ! -f ./data/collector/moox_collector.db"
run_remote "${REMOTE_CLI} collector legacy-cutover --mode preflight --legacy-space crypto --control-url '${CONTROL_URL}'" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert not d.get("running_job_items"); assert d.get("pending_canceled",0) == 0'

run_remote ": \"\${MOOX_CLOUD_ACCOUNT_ID:?}\"; : \"\${TENCENTCLOUD_REGION:?}\"; ${REMOTE_CLI} collector function publish-markets --manifest-dir ./collector/config/markets --environment development --zip ./collector/moox-collector-market.zip --control-url '${CONTROL_URL}' --cloud-account-id \"\${MOOX_CLOUD_ACCOUNT_ID}\" --region \"\${TENCENTCLOUD_REGION}\"" >/dev/null
run_remote "${REMOTE_CLI} collector function verify-markets --manifest-dir ./collector/config/markets --control-url '${CONTROL_URL}'" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d and all(x["status"] == "verified" and x["function_name"].endswith("-scf") for x in d)'

init_once() {
  run_remote "MOOX_COLLECTOR_MARKETS_DIR=./collector/config/markets ${REMOTE_CLI} init markets --markets all --metadata-url '${MOOX_REMOTE_METADATA_URL:-http://127.0.0.1:20200}'"
}

init_once >/dev/null
init_once | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["total"]["applied"] == 0 and d["total"]["failed"] == 0'

for market in stock_cn stock_us crypto_binance crypto_okx; do
  run_remote "${REMOTE_CLI} market status --control-url '${MARKET_CONTROL_URL}' --market '${market}'" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"].get("code",0) in ("SUCCESS",0); assert d["module"]["market_id"]'
done

query_market() {
  run_remote "${REMOTE_CLI} market kline query --control-url '${MARKET_CONTROL_URL}' --market '${MOOX_VERIFY_MARKET_ID}' --subjects '${MOOX_VERIFY_SUBJECT_ID}' --frequency '${MOOX_VERIFY_FREQUENCY}' --start '${MOOX_VERIFY_START}' --end '${MOOX_VERIFY_END}' --page-size 100"
}

before="$(query_market)"
printf '%s' "${before}" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"].get("code",0) in ("SUCCESS",0); assert "coverage_status" in d; assert all(x.get("open") and x.get("close") and x.get("source_provider") for x in d.get("rows",[]))'
if [[ "${MOOX_VERIFY_REQUIRE_GAP:-1}" == "1" ]]; then
  printf '%s' "${before}" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d.get("coverage_status") == "incomplete" and d.get("missing_ranges"), "verification range must begin with a controlled gap"'
fi

run_remote "${REMOTE_CLI} market kline refresh --control-url '${MARKET_CONTROL_URL}' --market '${MOOX_VERIFY_MARKET_ID}' --subjects '${MOOX_VERIFY_SUBJECT_ID}' --frequency '${MOOX_VERIFY_FREQUENCY}' --start '${MOOX_VERIFY_START}' --end '${MOOX_VERIFY_END}'" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"].get("code",0) in ("SUCCESS",0); assert d.get("task_ids")'

sleep "${MOOX_VERIFY_WAIT_SECONDS:-10}"
after="$(query_market)"
printf '%s' "${after}" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"].get("code",0) in ("SUCCESS",0); assert d.get("freshness") in ("fresh","stale","empty"); assert d.get("coverage_status") in ("complete","incomplete","unknown")'
if [[ "${MOOX_VERIFY_REQUIRE_GAP:-1}" == "1" ]]; then
  printf '%s' "${after}" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d.get("coverage_status") == "complete" and not d.get("missing_ranges"), "controlled gap was not repaired"'
fi

echo '{"status":"passed","market_id":"'"${MOOX_VERIFY_MARKET_ID}"'","subject_id":"'"${MOOX_VERIFY_SUBJECT_ID}"'"}'
