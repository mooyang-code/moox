#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-moox-storage}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="${SCRIPT_DIR}/${APP_NAME}.pid"

cd "${SCRIPT_DIR}"
mkdir -p log database data var/storage

if [[ -f "${PID_FILE}" ]]; then
  old_pid="$(cat "${PID_FILE}")"
  if ps -p "${old_pid}" >/dev/null 2>&1; then
    echo "stopping existing ${APP_NAME} pid=${old_pid}"
    kill "${old_pid}" 2>/dev/null || true
    sleep 2
  fi
  rm -f "${PID_FILE}"
fi

export STORAGE_CONFIG_PATH="${SCRIPT_DIR}/config"
export STORAGE_DATABASE_PATH="${SCRIPT_DIR}/database"
export MOOX_STORAGE_HOME="${SCRIPT_DIR}/var/storage"

echo "starting ${APP_NAME}"
nohup "./bin/${APP_NAME}" -conf=./config/trpc_go.yaml > ./log/app.log 2>&1 &
echo $! > "${PID_FILE}"
sleep 1

pid="$(cat "${PID_FILE}")"
if ! ps -p "${pid}" >/dev/null 2>&1; then
  echo "${APP_NAME} failed to start; see ${SCRIPT_DIR}/log/app.log" >&2
  tail -80 ./log/app.log >&2 || true
  exit 1
fi

echo "${APP_NAME} started pid=${pid}"
echo "logs: ${SCRIPT_DIR}/log/app.log ${SCRIPT_DIR}/log/trpc.log"
