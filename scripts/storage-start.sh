#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-moox-storage}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="${SCRIPT_DIR}/${APP_NAME}.pid"
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-6}"

cd "${SCRIPT_DIR}"
mkdir -p logs database data var/storage

if [[ -x "${SCRIPT_DIR}/stop.sh" ]]; then
  APP_NAME="${APP_NAME}" "${SCRIPT_DIR}/stop.sh" || true
fi

export STORAGE_CONFIG_PATH="${SCRIPT_DIR}/config"
export STORAGE_DATABASE_PATH="${SCRIPT_DIR}/database"
export MOOX_STORAGE_HOME="${SCRIPT_DIR}/var/storage"

echo "starting ${APP_NAME}"
nohup "./bin/${APP_NAME}" -conf=./config/trpc_go.yaml > ./logs/${APP_NAME}.log 2>&1 &
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
