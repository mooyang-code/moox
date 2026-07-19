#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$(basename "${SCRIPT_DIR}")" in
storage-view) APP_NAME="${APP_NAME:-moox-storage-view}" ;;
storage-shard) APP_NAME="${APP_NAME:-moox-storage-shard}" ;;
*) APP_NAME="${APP_NAME:-moox-storage-primary}" ;;
esac
PID_FILE="${SCRIPT_DIR}/${APP_NAME}.pid"
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-6}"

cd "${SCRIPT_DIR}"
mkdir -p logs database data var/storage

if [[ -x "${SCRIPT_DIR}/stop.sh" ]]; then
  APP_NAME="${APP_NAME}" "${SCRIPT_DIR}/stop.sh" || true
fi

case "${APP_NAME}" in
  moox-storage-primary|moox-storage-view|moox-storage-shard) STORAGE_FRAMEWORK_CONFIG="${SCRIPT_DIR}/config/trpc_go.yaml"; STORAGE_BUSINESS_CONFIG="" ;;
  *) echo "unsupported storage role binary: ${APP_NAME}" >&2; exit 1 ;;
esac
export STORAGE_CONFIG_PATH="${SCRIPT_DIR}/config"
export STORAGE_DATABASE_PATH="${SCRIPT_DIR}/database"
if [[ "${APP_NAME}" == "moox-storage-shard" ]]; then
  export MOOX_STORAGE_HOME="${MOOX_STORAGE_HOME:-${SCRIPT_DIR}/../data/storage-shard}"
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
