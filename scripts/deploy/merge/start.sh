#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE="${1:-merge}"
case "${SERVICE}" in
  merge|moox-merge|moox_merge) ;;
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

load_env_file() {
  local path="$1"
  [[ -r "${path}" ]] || return 0
  set -a
  # shellcheck disable=SC1090
  source "${path}"
  set +a
}

load_env_file "${ROOT}/config/runtime.env"
load_env_file "${HOME}/.config/moox/merge/runtime.env"

EVENTBUS_CREDENTIAL_FILE="${MOOX_MERGE_EVENTBUS_CREDENTIAL_FILE:-${HOME}/.config/moox/eventbus/merge-eventbus.yaml}"
EVENTBUS_CA_FILE="${MOOX_EVENTBUS_NATS_TLS_CA_FILE:-${HOME}/.config/moox/eventbus/ca.pem}"
SECRETS_DIR="${MOOX_MERGE_SECRETS_DIR:-${HOME}/.config/moox/merge}"

require_file "${ROOT}/bin/moox-merge"
require_file "${ROOT}/config/merge-app.yaml"
require_file "${ROOT}/config/merge-trpc.yaml"
require_file "${EVENTBUS_CREDENTIAL_FILE}"
require_file "${EVENTBUS_CA_FILE}"
require_file "${SECRETS_DIR}/storage-internal-auth.env"
require_file "${SECRETS_DIR}/gateway-merge.key"

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
  echo "merge EventBus URL is missing from ${EVENTBUS_CREDENTIAL_FILE}" >&2
  exit 1
}
if [[ "${MOOX_MERGE_ALLOW_LOCAL_EVENTBUS:-}" != "1" ]]; then
  case "${MOOX_EVENTBUS_NATS_URL}" in
    tls://127.0.0.1:*|tls://localhost:*|tls://[::1]:*|nats://127.0.0.1:*|nats://localhost:*)
      echo "merge must use the public tls:// EventBus URL from merge-eventbus.yaml" >&2
      exit 1
      ;;
  esac
  [[ "${MOOX_EVENTBUS_NATS_URL}" == tls://* ]] || {
    echo "merge EventBus URL must be tls://" >&2
    exit 1
  }
fi
export MOOX_EVENTBUS_NATS_URL
if [[ -r "${SECRETS_DIR}/health-auth.env" ]]; then
  load_env_file "${SECRETS_DIR}/health-auth.env"
else
  echo "health-auth.env is required for merge readiness probes" >&2
  exit 1
fi
[[ -n "${MOOX_HEALTH_AUTH_ACCESS_KEY:-}" && -n "${MOOX_HEALTH_AUTH_SECRET_KEY:-}" ]] || {
  echo "MOOX_HEALTH_AUTH_ACCESS_KEY and MOOX_HEALTH_AUTH_SECRET_KEY are required" >&2
  exit 1
}

load_env_file "${SECRETS_DIR}/storage-internal-auth.env"
[[ -n "${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}" ]] || {
  echo "storage-internal-auth.env must set Primary secret" >&2
  exit 1
}

gateway_secret="$(tr -d '\r\n' <"${SECRETS_DIR}/gateway-merge.key")"
[[ -n "${gateway_secret}" ]] || {
  echo "gateway-merge.key is empty" >&2
  exit 1
}

[[ -n "${MOOX_MERGE_STORAGE_RPC_GATEWAY_TARGET:-}" ]] || {
  echo "MOOX_MERGE_STORAGE_RPC_GATEWAY_TARGET is required" >&2
  exit 1
}
[[ -n "${MOOX_MERGE_STORAGE_RPC_GATEWAY_NODE_ID:-}" ]] || {
  echo "MOOX_MERGE_STORAGE_RPC_GATEWAY_NODE_ID is required" >&2
  exit 1
}

mkdir -p "${ROOT}/data/merge" "${ROOT}/logs/merge" "${ROOT}/run"
if [[ -x "${ROOT}/stop.sh" ]]; then
  "${ROOT}/stop.sh"
fi
for _ in $(seq 1 20); do
  if ! (echo >/dev/tcp/127.0.0.1/11416) >/dev/null 2>&1; then
    break
  fi
  leftover="$(pgrep -f "${ROOT}/bin/moox-merge" || true)"
  if [[ -n "${leftover}" ]]; then
    kill -9 ${leftover} 2>/dev/null || true
  fi
  sleep 1
done

PID_FILE="${ROOT}/run/merge.pid"
LOG_FILE="${ROOT}/logs/merge/stdout.log"

export MOOX_MERGE_DB_PATH="${MOOX_MERGE_DB_PATH:-${ROOT}/data/merge/merge.db}"
export MOOX_MERGE_EVENTBUS_CREDENTIAL_FILE="${EVENTBUS_CREDENTIAL_FILE}"
export MOOX_EVENTBUS_NATS_TLS_CA_FILE="${EVENTBUS_CA_FILE}"
export MOOX_EVENTBUS_NATS_URL
export MOOX_GATEWAY_SERVICE_KEY_ID="${MOOX_GATEWAY_SERVICE_KEY_ID:-merge}"
export MOOX_GATEWAY_CALLER="${MOOX_GATEWAY_CALLER:-merge}"
export MOOX_GATEWAY_SERVICE_SECRET_KEY="${gateway_secret}"
export MOOX_MERGE_STORAGE_RPC_KEY_ID="${MOOX_MERGE_STORAGE_RPC_KEY_ID:-merge}"
export MOOX_MERGE_STORAGE_RPC_HMAC_KEY_FILE="${SECRETS_DIR}/gateway-merge.key"
export MOOX_STORAGE_PRIMARY_AUTH_SECRET
export MOOX_HEALTH_AUTH_VERSION="${MOOX_HEALTH_AUTH_VERSION:-moox-health-v1}"
export MOOX_HEALTH_AUTH_ACCESS_KEY
export MOOX_HEALTH_AUTH_SECRET_KEY

cd "${ROOT}"
python3 - "${ROOT}" "${LOG_FILE}" "${PID_FILE}" <<'PY'
import os
import subprocess
import sys

root, log_path, pid_path = sys.argv[1], sys.argv[2], sys.argv[3]
os.chdir(root)
os.makedirs(os.path.dirname(log_path), exist_ok=True)
os.makedirs(os.path.dirname(pid_path), exist_ok=True)
with open(log_path, "ab") as log:
    proc = subprocess.Popen(
        [os.path.join(root, "bin/moox-merge"), "-config=config/merge-app.yaml", "-conf=config/merge-trpc.yaml"],
        stdin=subprocess.DEVNULL,
        stdout=log,
        stderr=subprocess.STDOUT,
        start_new_session=True,
        close_fds=True,
    )
with open(pid_path, "w", encoding="ascii") as fh:
    fh.write(str(proc.pid))
PY
sleep 1
pid="$(cat "${PID_FILE}")"
if ! ps -p "${pid}" >/dev/null 2>&1; then
  echo "merge failed to start; see ${LOG_FILE}" >&2
  tail -80 "${LOG_FILE}" >&2 || true
  exit 1
fi
echo "merge started pid=${pid}"
