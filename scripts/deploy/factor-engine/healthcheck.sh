#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE="${1:-factor-engine}"
case "${SERVICE}" in
  factor-engine|moox-factor-engine|moox_factor_engine) ;;
  *)
    echo "unsupported service ${SERVICE}" >&2
    exit 2
    ;;
esac

load_env_file() {
  local path="$1"
  [[ -r "${path}" ]] || return 0
  set -a
  # shellcheck disable=SC1090
  source "${path}"
  set +a
}

load_env_file "${ROOT}/config/runtime.env"
load_env_file "${HOME}/.config/moox/factor-engine/runtime.env"
SECRETS_DIR="${MOOX_FACTOR_ENGINE_SECRETS_DIR:-${HOME}/.config/moox/factor-engine}"
if [[ -r "${SECRETS_DIR}/health-auth.env" ]]; then
  load_env_file "${SECRETS_DIR}/health-auth.env"
fi
[[ -n "${MOOX_HEALTH_AUTH_ACCESS_KEY:-}" && -n "${MOOX_HEALTH_AUTH_SECRET_KEY:-}" ]] || {
  echo "health auth is required" >&2
  exit 1
}

PID_FILE="${ROOT}/run/factor-engine.pid"
[[ -f "${PID_FILE}" ]] || {
  echo "factor-engine is not running" >&2
  exit 1
}
pid="$(cat "${PID_FILE}")"
ps -p "${pid}" >/dev/null 2>&1 || {
  echo "factor-engine pid ${pid} is not running" >&2
  exit 1
}

timestamp=$(date +%s)
nonce=$(openssl rand -hex 32)
body_hash=$(printf %s "" | openssl dgst -sha256)
body_hash=${body_hash##* }
canonical=$(printf "%s\nGET\n/readyz\n%s\n%s\n%s" moox-request-v1 "${body_hash}" "${timestamp}" "${nonce}")
signature=$(printf "%s" "${canonical}" | openssl dgst -sha256 -hmac "${MOOX_HEALTH_AUTH_SECRET_KEY}")
signature=${signature##* }
auth="${MOOX_HEALTH_AUTH_VERSION:-moox-health-v1}/${MOOX_HEALTH_AUTH_ACCESS_KEY}/${timestamp}/${nonce}/${signature}"
curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: ${auth}" "http://127.0.0.1:11415/readyz" >/dev/null
