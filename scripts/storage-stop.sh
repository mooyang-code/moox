#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-moox-storage}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="${SCRIPT_DIR}/${APP_NAME}.pid"

is_managed_pid() {
  local pid="$1"
  local cwd=""
  local exe=""

  cwd="$(readlink -f "/proc/${pid}/cwd" 2>/dev/null || true)"
  exe="$(readlink -f "/proc/${pid}/exe" 2>/dev/null || true)"

  [[ "${cwd}" == "${SCRIPT_DIR}" ]] && return 0
  [[ "${exe}" == "${SCRIPT_DIR}/bin/${APP_NAME}" ]] && return 0
  [[ "${exe}" == "${SCRIPT_DIR}/bin/${APP_NAME} (deleted)" ]] && return 0
  return 1
}

stop_pid() {
  local pid="$1"
  if [[ -z "${pid}" ]] || ! ps -p "${pid}" >/dev/null 2>&1; then
    return 0
  fi

  echo "stopping ${APP_NAME} pid=${pid}"
  kill "${pid}" 2>/dev/null || true
  sleep 2
  if ps -p "${pid}" >/dev/null 2>&1; then
    echo "force stopping ${APP_NAME} pid=${pid}"
    kill -9 "${pid}" 2>/dev/null || true
  fi
}

stopped=0
if [[ -f "${PID_FILE}" ]]; then
  stop_pid "$(cat "${PID_FILE}")"
  stopped=1
fi

while IFS= read -r pid; do
  if is_managed_pid "${pid}"; then
    stop_pid "${pid}"
    stopped=1
  fi
done < <(pgrep -f "${APP_NAME}" 2>/dev/null || true)

rm -f "${PID_FILE}"
if [[ "${stopped}" -eq 0 ]]; then
  echo "${APP_NAME} pid file not found and no managed process found"
else
  echo "${APP_NAME} stopped"
fi
