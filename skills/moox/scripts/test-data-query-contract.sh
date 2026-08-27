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
