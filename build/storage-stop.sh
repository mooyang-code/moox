#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-moox-storage}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="${SCRIPT_DIR}/${APP_NAME}.pid"

if [[ ! -f "${PID_FILE}" ]]; then
  echo "${APP_NAME} pid file not found"
  exit 0
fi

pid="$(cat "${PID_FILE}")"
if ps -p "${pid}" >/dev/null 2>&1; then
  echo "stopping ${APP_NAME} pid=${pid}"
  kill "${pid}" 2>/dev/null || true
  sleep 2
  if ps -p "${pid}" >/dev/null 2>&1; then
    echo "force stopping ${APP_NAME} pid=${pid}"
    kill -9 "${pid}" 2>/dev/null || true
  fi
fi

rm -f "${PID_FILE}"
echo "${APP_NAME} stopped"
