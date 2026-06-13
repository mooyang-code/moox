#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULT_STORAGE_ROOT="${ROOT}/var/storage/acceptance"
if [[ -d "${ROOT}/storage" ]]; then
  DEFAULT_STORAGE_ROOT="${ROOT}/storage/var/storage/acceptance"
fi
STORAGE_ROOT="${STORAGE_ROOT:-${DEFAULT_STORAGE_ROOT}}"
WORKSPACE="${WORKSPACE:-default}"
EXCHANGE="${EXCHANGE:-BINANCE}"
DATASET="${DATASET:-binance_spot_kline_1m}"
FREQ="${FREQ:-1m}"
DEFAULT_CSV_DIR="${HOME}/Downloads"
if [[ -d "${ROOT}/storage/sample-data" ]]; then
  DEFAULT_CSV_DIR="${ROOT}/storage/sample-data"
elif [[ -d "${ROOT}/sample-data" ]]; then
  DEFAULT_CSV_DIR="${ROOT}/sample-data"
fi
CSV_DIR="${CSV_DIR:-${DEFAULT_CSV_DIR}}"
DEFAULT_CLI="${ROOT}/bin/moox-cli"
if [[ -x "${ROOT}/cli/bin/moox-cli" ]]; then
  DEFAULT_CLI="${ROOT}/cli/bin/moox-cli"
fi
CLI="${CLI:-${DEFAULT_CLI}}"

if [[ ! -x "${CLI}" ]]; then
  echo "==> moox-cli not found, building binaries first"
  "${ROOT}/scripts/build.sh"
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
