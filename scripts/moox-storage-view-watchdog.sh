#!/usr/bin/env bash
set -euo pipefail

ROOT="${MOOX_STORAGE_ROOT:-/home/ubuntu/moox/storage}"
PID_FILE="${ROOT}/run/storage-view.pid"
BINARY="${ROOT}/bin/moox-storage-view"
LOCK_FILE="${ROOT}/run/storage-view-watchdog.lock"
ATTEMPT_FILE="${ROOT}/run/storage-view-watchdog.last-attempt"
LOG_FILE="${ROOT}/logs/storage-view-watchdog.log"
COOLDOWN_SECONDS="${MOOX_STORAGE_VIEW_WATCHDOG_COOLDOWN_SECONDS:-30}"
STARTING_GRACE_SECONDS="${MOOX_STORAGE_VIEW_WATCHDOG_STARTING_GRACE_SECONDS:-60}"

mkdir -p "${ROOT}/run" "${ROOT}/logs"
# A package installer holds this lock across stop/swap/start. Skipping one
# watchdog tick is safer than starting the old package midway through a swap.
exec 8>"${ROOT}.maintenance.lock"
flock -n 8 || exit 0
exec 9>"${LOCK_FILE}"
flock -n 9 || exit 0

now="$(date +%s)"
pid=""
if [[ -r "${PID_FILE}" ]]; then
  pid="$(tr -d '[:space:]' <"${PID_FILE}")"
fi

process_alive=0
process_age=0
if [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null &&
  [[ "$(readlink "/proc/${pid}/exe" 2>/dev/null || true)" == "${BINARY}" ]]; then
  process_alive=1
  process_age="$(ps -o etimes= -p "${pid}" 2>/dev/null | tr -d '[:space:]')"
  process_age="${process_age:-0}"
fi

port_open() {
  local port="$1"
  bash -c ": </dev/tcp/127.0.0.1/${port}" >/dev/null 2>&1
}

if [[ "${process_alive}" -eq 1 && "${process_age}" -lt "${STARTING_GRACE_SECONDS}" ]]; then
  exit 0
fi

if [[ "${process_alive}" -eq 1 ]] && port_open 20103 && port_open 20202; then
  exit 0
fi

if [[ -r "${ATTEMPT_FILE}" ]]; then
  last_attempt="$(tr -d '[:space:]' <"${ATTEMPT_FILE}")"
  if [[ "${last_attempt}" =~ ^[0-9]+$ ]] && (( now - last_attempt < COOLDOWN_SECONDS )); then
    exit 0
  fi
fi

printf '%s\n' "${now}" >"${ATTEMPT_FILE}"

{
  printf '[%s] storage-view is unhealthy; pid=%s alive=%s age=%ss\n' \
    "$(date -Is)" "${pid:-none}" "${process_alive}" "${process_age}"
  free -h || true
  MOOX_WITH_STORAGE=1 \
  MOOX_WITH_STORAGE_NODE=1 \
  MOOX_GATEWAY_NODE_ID="${MOOX_GATEWAY_NODE_ID:-control}" \
  MOOX_STORAGE_EVENTBUS_URL="${MOOX_STORAGE_EVENTBUS_URL:-tls://127.0.0.1:4222}" \
    "${ROOT}/start.sh" storage-view 8>&- 9>&-
  printf '[%s] storage-view restart command completed\n' "$(date -Is)"
} >>"${LOG_FILE}" 2>&1

if [[ ! -r "${PID_FILE}" ]]; then
  printf '[%s] storage-view restart did not create a pid file\n' "$(date -Is)" >>"${LOG_FILE}"
  exit 1
fi
