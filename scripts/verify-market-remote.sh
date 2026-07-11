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
remote_prefix="cd '${MOOX_DEPLOY_DIR}' && test -r ./env.sh && set -a && . ./env.sh && set +a"

run_remote() {
  "${SSH_BIN}" "${MOOX_DEV_SSH_TARGET}" "${remote_prefix} && $1"
}

run_remote "test -f ./data/collector/moox_collector_market_v2.db"
run_remote "test -f ./data/collector/moox_collector.db.backup || test ! -f ./data/collector/moox_collector.db"

for market in stock_cn stock_us crypto_binance crypto_okx; do
  run_remote "${REMOTE_CLI} market status --control-url '${CONTROL_URL}' --market '${market}'" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"]["code"] in ("SUCCESS",0); assert d["module"]["market_id"]'
done

query_market() {
  run_remote "${REMOTE_CLI} market kline query --control-url '${CONTROL_URL}' --market '${MOOX_VERIFY_MARKET_ID}' --subjects '${MOOX_VERIFY_SUBJECT_ID}' --frequency '${MOOX_VERIFY_FREQUENCY}' --start '${MOOX_VERIFY_START}' --end '${MOOX_VERIFY_END}' --page-size 100"
}

before="$(query_market)"
printf '%s' "${before}" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"]["code"] in ("SUCCESS",0); assert "coverage_status" in d; assert all(x.get("open") and x.get("close") and x.get("source_provider") for x in d.get("rows",[]))'

run_remote "${REMOTE_CLI} market kline refresh --control-url '${CONTROL_URL}' --market '${MOOX_VERIFY_MARKET_ID}' --subjects '${MOOX_VERIFY_SUBJECT_ID}' --frequency '${MOOX_VERIFY_FREQUENCY}' --start '${MOOX_VERIFY_START}' --end '${MOOX_VERIFY_END}'" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"]["code"] in ("SUCCESS",0); assert d.get("task_ids")'

sleep "${MOOX_VERIFY_WAIT_SECONDS:-10}"
after="$(query_market)"
printf '%s' "${after}" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ret_info"]["code"] in ("SUCCESS",0); assert d.get("freshness") in ("fresh","stale","empty"); assert d.get("coverage_status") in ("complete","incomplete","unknown")'

echo '{"status":"passed","market_id":"'"${MOOX_VERIFY_MARKET_ID}"'","subject_id":"'"${MOOX_VERIFY_SUBJECT_ID}"'"}'
