#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
CONFIG="${ROOT}/deploy/caddy/Caddyfile"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require_text() {
  grep -Fq -- "$2" "$1" || fail "missing $3"
}

count_exactly_one() {
  local text="$1" description="$2" count
  count=$(grep -Fc -- "${text}" "${CONFIG}" || true)
  [[ "${count}" == 1 ]] || fail "${description} appears ${count} times"
}

[[ -f "${CONFIG}" ]] || fail "missing Caddyfile: ${CONFIG}"
require_text "${CONFIG}" 'admin 127.0.0.1:2019' 'loopback Caddy admin listener'
require_text "${CONFIG}" 'reverse_proxy 127.0.0.1:9528' 'loopback web-host upstream'
require_text "${CONFIG}" 'reverse_proxy 127.0.0.1:11000' 'loopback Admin control upstream'
require_text "${CONFIG}" 'reverse_proxy 127.0.0.1:11002' 'loopback Admin service upstream'
require_text "${CONFIG}" 'handle /api/admin/*' 'browser Admin route'
require_text "${CONFIG}" 'handle /api/service/*' 'service API route'
require_text "${CONFIG}" 'Content-Security-Policy' 'browser CSP'
count_exactly_one 'https://{$MOOX_PUBLIC_HOST}:{$MOOX_BROWSER_HTTPS_PORT:9527} {' 'browser HTTPS listener'
count_exactly_one 'https://{$MOOX_PUBLIC_HOST}:{$MOOX_SERVICE_HTTPS_PORT:11001} {' 'service HTTPS listener'

if grep -Eq '^[[:space:]]*trusted_proxies([[:space:]]|$)' "${CONFIG}"; then
  fail 'trusted proxies require a reviewed EdgeOne origin-pull address artifact'
fi

export MOOX_PUBLIC_HOST="${MOOX_PUBLIC_HOST:-127.0.0.1}"

if [[ -n "${CADDY_BIN:-}" ]]; then
  [[ -x "${CADDY_BIN}" ]] || fail "CADDY_BIN is not executable: ${CADDY_BIN}"
  "${CADDY_BIN}" validate --config "${CONFIG}" --adapter caddyfile
  printf 'PASS: EdgeOne origin contract and Caddy validation\n'
else
	printf 'SKIP: Caddy validation unavailable; static EdgeOne origin assertions passed (set CADDY_BIN to validate)\n'
fi
