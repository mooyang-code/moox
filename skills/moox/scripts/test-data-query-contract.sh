#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/data-query.md"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${REFERENCE}" ]] || fail "data query reference is missing"
grep -Fq 'references/data-query.md' "${SKILL}" || fail "SKILL.md does not route data queries to the reference"
description="$(sed -n 's/^description: //p' "${SKILL}" | head -1)"
for trigger in '采集数据' 'K-line' 'K线' '行情' 'BTC-USDT' 'crypto market' 'queries'; do
  grep -Fiq "${trigger}" <<<"${description}" || fail "skill description is missing the ${trigger} trigger"
done

grep -Fq 'SKILL_ROOT=' "${REFERENCE}" || fail "reference does not establish an absolute SKILL_ROOT"
grep -Fq '${SKILL_ROOT}/../../bin/moox-cli' "${REFERENCE}" || fail "reference does not resolve the repository CLI from SKILL_ROOT"
grep -Fq 'command -v moox-cli' "${REFERENCE}" || fail "reference does not define the PATH fallback"
grep -Fq 'CONFIG="${SKILL_ROOT}/config/data-access.yaml"' "${REFERENCE}" || fail "reference does not pin the packaged config"

awk '
  /"\$CLI" data kline get/ { invocation = 1; next }
  invocation && /--config "\$CONFIG"/ { invocation = 0; count++; next }
  invocation && /^[[:space:]]*$/ { exit 2 }
  END { if (invocation || count < 4) exit 1 }
' "${REFERENCE}" || fail "every documented query must use CLI and explicit packaged config variables"
for summary_field in 'data type' 'exchange' 'symbol' 'interval' 'row count' 'returned time range'; do
  grep -Fq "${summary_field}" "${REFERENCE}" || fail "reference summary is missing ${summary_field}"
done
grep -Fq 'Never include Gateway secrets' "${REFERENCE}" || fail "reference does not prohibit credential disclosure"

help="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli data kline get --help)"
for flag in --config --data-type --exchange --symbol --interval --limit --start-time --end-time --timeout --output; do
  grep -Fq -- "${flag}" <<<"${help}" || fail "CLI help is missing ${flag}"
done

if missing_data_type="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli data kline get --symbol BTC-USDT 2>&1)"; then
  fail "CLI accepted a query without data-type"
fi
grep -Fq 'required flag(s) "data-type" not set' <<<"${missing_data_type}" || fail "CLI did not require data-type"

if missing_symbol="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli data kline get --data-type crypto 2>&1)"; then
  fail "CLI accepted a query without symbol"
fi
grep -Fq 'required flag(s) "symbol" not set' <<<"${missing_symbol}" || fail "CLI did not require symbol"

echo "PASS: skill data query contract"
