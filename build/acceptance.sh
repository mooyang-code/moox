#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STORAGE_ROOT="${STORAGE_ROOT:-${ROOT}/var/storage/acceptance}"
WORKSPACE="${WORKSPACE:-default}"
EXCHANGE="${EXCHANGE:-BINANCE}"
DATASET="${DATASET:-binance_spot_kline_1m}"
FREQ="${FREQ:-1m}"
CSV_DIR="${CSV_DIR:-${HOME}/Downloads}"
CLI="${CLI:-${ROOT}/bin/moox-cli}"

if [[ ! -x "${CLI}" ]]; then
  echo "==> moox-cli not found, building binaries first"
  "${ROOT}/build/build.sh"
fi

rm -rf "${STORAGE_ROOT}"
mkdir -p "${STORAGE_ROOT}"

import_csv() {
  local symbol="$1"
  local file="${CSV_DIR}/${symbol}.csv"
  if [[ ! -f "${file}" ]]; then
    echo "missing acceptance csv: ${file}" >&2
    exit 1
  fi

  echo "==> import ${file}"
  "${CLI}" data csv import \
    --storage-root "${STORAGE_ROOT}" \
    --workspace "${WORKSPACE}" \
    --exchange "${EXCHANGE}" \
    --dataset "${DATASET}" \
    --instrument "${symbol}" \
    --freq "${FREQ}" \
    --file "${file}"

  local jsonl="${STORAGE_ROOT}/timeseries/${WORKSPACE}/${DATASET}/${EXCHANGE}/${symbol}/${FREQ}/default.jsonl"
  if [[ ! -s "${jsonl}" ]]; then
    echo "acceptance output not found or empty: ${jsonl}" >&2
    exit 1
  fi

  local rows
  rows="$(wc -l < "${jsonl}" | tr -d ' ')"
  if [[ "${rows}" -le 0 ]]; then
    echo "acceptance output has zero rows: ${jsonl}" >&2
    exit 1
  fi
  echo "==> ${symbol}: ${rows} rows written to ${jsonl}"
}

import_csv APT-USDT
import_csv AR-USDT

echo "==> acceptance passed; storage root: ${STORAGE_ROOT}"
