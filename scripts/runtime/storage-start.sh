#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$(basename "${SCRIPT_DIR}")" in
storage-view) APP_NAME="${APP_NAME:-moox-storage-view}" ;;
storage-node) APP_NAME="${APP_NAME:-moox-storage-node}" ;;
*) APP_NAME="${APP_NAME:-moox-storage-primary}" ;;
esac
PID_FILE="${SCRIPT_DIR}/${APP_NAME}.pid"
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-6}"

cd "${SCRIPT_DIR}"
mkdir -p logs database data var/storage

# The package-local file is the authority for the Primary/View credentials.
# Loading it here prevents a manually started storage process from retaining a
# stale shell environment after the control package rotates the secret.
if [[ -r "${SCRIPT_DIR}/secrets/storage-internal-auth.env" ]]; then
  set -a
  source "${SCRIPT_DIR}/secrets/storage-internal-auth.env"
  set +a
fi
if [[ -r "${SCRIPT_DIR}/secrets/storage-node-auth.env" ]]; then
  set -a
  source "${SCRIPT_DIR}/secrets/storage-node-auth.env"
  set +a
fi

if [[ -x "${SCRIPT_DIR}/stop.sh" ]]; then
  APP_NAME="${APP_NAME}" "${SCRIPT_DIR}/stop.sh" || true
fi

case "${APP_NAME}" in
  moox-storage-primary)
    STORAGE_FRAMEWORK_CONFIG="${SCRIPT_DIR}/config/trpc_go.yaml"
    export MOOX_STORAGE_ROLE=primary
    ;;
  moox-storage-view)
    STORAGE_FRAMEWORK_CONFIG="${SCRIPT_DIR}/config/trpc_go.yaml"
    export MOOX_STORAGE_ROLE=view
    ;;
  moox-storage-node)
    STORAGE_FRAMEWORK_CONFIG="${SCRIPT_DIR}/config/trpc_go.yaml"
    export MOOX_STORAGE_ROLE=node
    export MOOX_STORAGE_NODE_ID="${MOOX_STORAGE_NODE_ID:-storage-node-0}"
    ;;
  *) echo "unsupported storage role binary: ${APP_NAME}" >&2; exit 1 ;;
esac
export MOOX_STORAGE_NODE_AUTH_SECRET="${MOOX_STORAGE_NODE_AUTH_SECRET:?MOOX_STORAGE_NODE_AUTH_SECRET is required}"
if [[ "${MOOX_STORAGE_ROLE}" == "primary" || "${MOOX_STORAGE_ROLE}" == "view" ]]; then
  export MOOX_STORAGE_PRIMARY_AUTH_SECRET="${MOOX_STORAGE_PRIMARY_AUTH_SECRET:?MOOX_STORAGE_PRIMARY_AUTH_SECRET is required}"
fi
if [[ "${MOOX_STORAGE_ROLE}" == "primary" || "${MOOX_STORAGE_ROLE}" == "view" ]]; then
  export MOOX_STORAGE_VIEW_AUTH_SECRET="${MOOX_STORAGE_VIEW_AUTH_SECRET:?MOOX_STORAGE_VIEW_AUTH_SECRET is required}"
fi
export STORAGE_CONFIG_PATH="${SCRIPT_DIR}/config"
export STORAGE_DATABASE_PATH="${SCRIPT_DIR}/database"
if [[ "${APP_NAME}" == "moox-storage-node" ]]; then
  export MOOX_STORAGE_HOME="${MOOX_STORAGE_HOME:-${SCRIPT_DIR}/../data/storage-node}"
else
  export MOOX_STORAGE_HOME="${MOOX_STORAGE_HOME:-${SCRIPT_DIR}/../data/storage}"
fi
mkdir -p "${MOOX_STORAGE_HOME}"

echo "initializing metadata schema"
if [[ "${APP_NAME}" == "moox-storage-primary" ]]; then
  "./bin/${APP_NAME}-cli" init --storage-conf="${STORAGE_FRAMEWORK_CONFIG}" --schema-path=./schema/metadata.sql >> ./logs/${APP_NAME}.log 2>&1
fi

echo "starting ${APP_NAME}"
nohup "./bin/${APP_NAME}" -conf="${STORAGE_FRAMEWORK_CONFIG}" > ./logs/${APP_NAME}.log 2>&1 &
echo $! > "${PID_FILE}"
sleep "${STARTUP_WAIT_SECONDS}"

pid="$(cat "${PID_FILE}")"
if ! ps -p "${pid}" >/dev/null 2>&1; then
  echo "${APP_NAME} failed to start; see ${SCRIPT_DIR}/logs/${APP_NAME}.log" >&2
  tail -80 ./logs/${APP_NAME}.log >&2 || true
  exit 1
fi

echo "${APP_NAME} started pid=${pid}"
echo "logs: ${SCRIPT_DIR}/logs/${APP_NAME}.log ${SCRIPT_DIR}/logs/trpc.log"
