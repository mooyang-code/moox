#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ZIP="$(mktemp "${TMPDIR:-/tmp}/moox-market-release.XXXXXX.zip")"
trap 'rm -f "${ZIP}"' EXIT

MOOX_MARKET_ENVIRONMENT="${MOOX_MARKET_ENVIRONMENT:-development}" OUT_PATH="${ZIP}" VERSION="manifest-test" "${ROOT}/scripts/build-collector-scf-package.sh" >/dev/null
entries="$(unzip -Z1 "${ZIP}" | sort)"
[[ "$(printf '%s\n' "${entries}" | grep -c '^main$')" -eq 1 ]]
[[ "$(printf '%s\n' "${entries}" | grep -c '^market-readiness-lock.json$')" -eq 1 ]]
[[ "$(printf '%s\n' "${entries}" | grep -c '^config/markets/stock_cn/calendar.yaml$')" -eq 1 ]]
if printf '%s\n' "${entries}" | grep -Eiq 'metadata\.seed|provider-validation|secret|token'; then
  echo "SCF package contains control-plane metadata or secret-like files" >&2
  exit 1
fi
unzip -p "${ZIP}" market-readiness-lock.json | python3 -c 'import json,sys; d=json.load(sys.stdin); assert set(d["markets"]) == {"stock_cn","stock_us","crypto_binance","crypto_okx"}'
