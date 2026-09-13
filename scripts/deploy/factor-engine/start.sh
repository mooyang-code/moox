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

require_file() {
  [[ -f "$1" ]] || {
    echo "missing required file: $1" >&2
    exit 1
  }
}

require_dir() {
  [[ -d "$1" ]] || {
    echo "missing required directory: $1" >&2
    exit 1
  }
}

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

EVENTBUS_CREDENTIAL_FILE="${MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE:-${HOME}/.config/moox/eventbus/factor-engine-eventbus.yaml}"
EVENTBUS_CA_FILE="${MOOX_EVENTBUS_NATS_TLS_CA_FILE:-${HOME}/.config/moox/eventbus/ca.pem}"
SECRETS_DIR="${MOOX_FACTOR_ENGINE_SECRETS_DIR:-${HOME}/.config/moox/factor-engine}"

require_file "${ROOT}/bin/moox-factor-engine"
require_file "${ROOT}/config/engine-app.yaml"
require_file "${ROOT}/config/engine-trpc.yaml"
require_file "${ROOT}/pyworker/worker.py"
require_file "${ROOT}/pyworker/runtime-requirements.txt"
require_dir "${ROOT}/python-runtime"
require_file "${EVENTBUS_CREDENTIAL_FILE}"
require_file "${EVENTBUS_CA_FILE}"
require_file "${SECRETS_DIR}/storage-internal-auth.env"
require_file "${SECRETS_DIR}/gateway-factor.key"

credential_eventbus_url() {
  awk '
    BEGIN { in_urls = 0 }
    /^urls:[[:space:]]*$/ { in_urls = 1; next }
    in_urls && /^[[:space:]]*-[[:space:]]*/ {
      sub(/^[[:space:]]*-[[:space:]]*/, "")
      gsub(/[[:space:]]+$/, "")
      print
      exit
    }
    in_urls && NF && $0 !~ /^[[:space:]]/ { exit }
  ' "$1"
}

if [[ -z "${MOOX_EVENTBUS_NATS_URL:-}" ]]; then
  MOOX_EVENTBUS_NATS_URL="$(credential_eventbus_url "${EVENTBUS_CREDENTIAL_FILE}")"
fi
[[ -n "${MOOX_EVENTBUS_NATS_URL}" ]] || {
  echo "factor-engine EventBus URL is missing from ${EVENTBUS_CREDENTIAL_FILE}" >&2
  exit 1
}
if [[ "${MOOX_FACTOR_ENGINE_ALLOW_LOCAL_EVENTBUS:-}" != "1" ]]; then
  case "${MOOX_EVENTBUS_NATS_URL}" in
    tls://127.0.0.1:*|tls://localhost:*|tls://[::1]:*|nats://127.0.0.1:*|nats://localhost:*)
      echo "factor-engine must use the public tls:// EventBus URL from factor-engine-eventbus.yaml" >&2
      exit 1
      ;;
  esac
  [[ "${MOOX_EVENTBUS_NATS_URL}" == tls://* ]] || {
    echo "factor-engine EventBus URL must be tls://" >&2
    exit 1
  }
fi
export MOOX_EVENTBUS_NATS_URL
if [[ -r "${SECRETS_DIR}/health-auth.env" ]]; then
  load_env_file "${SECRETS_DIR}/health-auth.env"
else
  echo "health-auth.env is required for engine readiness probes" >&2
  exit 1
fi
[[ -n "${MOOX_HEALTH_AUTH_ACCESS_KEY:-}" && -n "${MOOX_HEALTH_AUTH_SECRET_KEY:-}" ]] || {
  echo "MOOX_HEALTH_AUTH_ACCESS_KEY and MOOX_HEALTH_AUTH_SECRET_KEY are required" >&2
  exit 1
}

load_env_file "${SECRETS_DIR}/storage-internal-auth.env"
[[ -n "${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}" && -n "${MOOX_STORAGE_VIEW_AUTH_SECRET:-}" ]] || {
  echo "storage-internal-auth.env must set Primary and View secrets" >&2
  exit 1
}

gateway_secret="$(tr -d '\r\n' <"${SECRETS_DIR}/gateway-factor.key")"
[[ -n "${gateway_secret}" ]] || {
  echo "gateway-factor.key is empty" >&2
  exit 1
}

[[ -n "${MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET:-}" ]] || {
  echo "MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET is required" >&2
  exit 1
}
[[ -n "${MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID:-}" ]] || {
  echo "MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID is required" >&2
  exit 1
}

mkdir -p "${ROOT}/data/factor-engine/artifacts" "${ROOT}/logs/factor-engine" "${ROOT}/run"
if [[ -x "${ROOT}/stop.sh" ]]; then
  "${ROOT}/stop.sh"
fi
for _ in $(seq 1 20); do
  if ! (echo >/dev/tcp/127.0.0.1/11415) >/dev/null 2>&1; then
    break
  fi
  leftover="$(pgrep -f "${ROOT}/bin/moox-factor-engine" || true)"
  if [[ -n "${leftover}" ]]; then
    kill -9 ${leftover} 2>/dev/null || true
  fi
  sleep 1
done
VENV="${ROOT}/data/factor-engine/venv"
WHEELS="${ROOT}/pyworker/wheels"
[[ -d "${WHEELS}" ]] || {
  echo "missing offline Python wheels: ${WHEELS}" >&2
  exit 1
}
if [[ ! -x "${VENV}/bin/python" ]]; then
  python3 -m venv "${VENV}"
fi
if ! "${VENV}/bin/python" - <<'PY' >/dev/null 2>&1; then
import numpy  # noqa: F401
import pandas  # noqa: F401
PY
  "${VENV}/bin/python" -m pip install --upgrade pip
  "${VENV}/bin/python" -m pip install --no-index --find-links "${WHEELS}" -r "${ROOT}/pyworker/runtime-requirements.txt"
fi

PID_FILE="${ROOT}/run/factor-engine.pid"
LOG_FILE="${ROOT}/logs/factor-engine/stdout.log"

export MOOX_FACTOR_ENGINE_PYTHON_BIN="${VENV}/bin/python"
export MOOX_FACTOR_ENGINE_WORKER_PATH="${ROOT}/pyworker/worker.py"
export MOOX_FACTOR_ENGINE_FACTORS_DIR="${MOOX_FACTOR_ENGINE_FACTORS_DIR:-${ROOT}/data/factor-engine/artifacts}"
export MOOX_FACTOR_ENGINE_DB_PATH="${MOOX_FACTOR_ENGINE_DB_PATH:-${ROOT}/data/factor-engine/runtime.db}"
export MOOX_PYTHON_RUNTIME_PATH="${ROOT}/python-runtime"
export MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE="${EVENTBUS_CREDENTIAL_FILE}"
export MOOX_EVENTBUS_NATS_TLS_CA_FILE="${EVENTBUS_CA_FILE}"
export MOOX_EVENTBUS_NATS_URL
export MOOX_GATEWAY_SERVICE_KEY_ID="${MOOX_GATEWAY_SERVICE_KEY_ID:-factor}"
export MOOX_GATEWAY_CALLER="${MOOX_GATEWAY_CALLER:-factor}"
export MOOX_GATEWAY_SERVICE_SECRET_KEY="${gateway_secret}"
export MOOX_FACTOR_STORAGE_RPC_KEY_ID="${MOOX_FACTOR_STORAGE_RPC_KEY_ID:-factor}"
export MOOX_STORAGE_PRIMARY_AUTH_SECRET
export MOOX_STORAGE_VIEW_AUTH_SECRET
export MOOX_HEALTH_AUTH_VERSION="${MOOX_HEALTH_AUTH_VERSION:-moox-health-v1}"
export MOOX_HEALTH_AUTH_ACCESS_KEY
export MOOX_HEALTH_AUTH_SECRET_KEY

(
  cd "${ROOT}"
  nohup "${ROOT}/bin/moox-factor-engine" \
    -config=config/engine-app.yaml \
    -conf=config/engine-trpc.yaml \
    </dev/null >>"${LOG_FILE}" 2>&1 &
  echo $! >"${PID_FILE}"
)
sleep 1
pid="$(cat "${PID_FILE}")"
if ! ps -p "${pid}" >/dev/null 2>&1; then
  echo "factor-engine failed to start; see ${LOG_FILE}" >&2
  tail -80 "${LOG_FILE}" >&2 || true
  exit 1
fi
echo "factor-engine started pid=${pid}"
