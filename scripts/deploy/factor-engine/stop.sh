#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="${ROOT}/run/factor-engine.pid"
if [[ ! -f "${PID_FILE}" ]]; then
  exit 0
fi
pid="$(cat "${PID_FILE}" 2>/dev/null || true)"
if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
  kill "${pid}" 2>/dev/null || true
  for _ in 1 2 3 4 5; do
    ps -p "${pid}" >/dev/null 2>&1 || break
    sleep 1
  done
  if ps -p "${pid}" >/dev/null 2>&1; then
    kill -9 "${pid}" 2>/dev/null || true
  fi
fi
rm -f "${PID_FILE}"
