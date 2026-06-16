#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

DEFAULT_STORAGE_ROOT="${ROOT}/var/storage/acceptance"
if [[ -d "${ROOT}/storage" ]]; then
  DEFAULT_STORAGE_ROOT="${ROOT}/storage/var/storage/acceptance"
fi

STORAGE_ROOT="${STORAGE_ROOT:-${DEFAULT_STORAGE_ROOT}}"
SPACE="${SPACE:-${WORKSPACE:-crypto_acceptance}}"
DATA_SOURCE="${DATA_SOURCE:-binance}"
DATASET="${DATASET:-binance_spot_kline_1m}"
FREQ="${FREQ:-1m}"
OUTPUT="${OUTPUT:-${HOME}/Downloads/moox-storage-acceptance.json}"
PAGE_SIZE="${PAGE_SIZE:-200000}"
LOCAL_MODE=0
STORAGE_URL="${STORAGE_URL:-}"
CSV_FILES=()

DEFAULT_CLI="${ROOT}/bin/moox-cli"
if [[ -x "${ROOT}/cli/bin/moox-cli" ]]; then
  DEFAULT_CLI="${ROOT}/cli/bin/moox-cli"
fi
CLI="${CLI:-${DEFAULT_CLI}}"

DEFAULT_CSV_DIR="${HOME}/Downloads"
if [[ -d "${ROOT}/storage/sample-data" ]]; then
  DEFAULT_CSV_DIR="${ROOT}/storage/sample-data"
elif [[ -d "${ROOT}/sample-data" ]]; then
  DEFAULT_CSV_DIR="${ROOT}/sample-data"
fi
CSV_DIR="${CSV_DIR:-${DEFAULT_CSV_DIR}}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --local)
      LOCAL_MODE=1
      shift
      ;;
    --storage-url)
      STORAGE_URL="$2"
      shift 2
      ;;
    --space|--workspace)
      SPACE="$2"
      shift 2
      ;;
    --dataset)
      DATASET="$2"
      shift 2
      ;;
    --freq)
      FREQ="$2"
      shift 2
      ;;
    --csv)
      CSV_FILES+=("$2")
      shift 2
      ;;
    --output)
      OUTPUT="$2"
      shift 2
      ;;
    --storage-root)
      STORAGE_ROOT="$2"
      shift 2
      ;;
    --page-size)
      PAGE_SIZE="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

if [[ ${#CSV_FILES[@]} -eq 0 ]]; then
  CSV_FILES=("${CSV_DIR}/APT-USDT.csv" "${CSV_DIR}/AR-USDT.csv")
fi

if [[ -n "${STORAGE_URL}" && "${LOCAL_MODE}" -eq 0 ]]; then
  echo "==> storage-url=${STORAGE_URL} recorded; current acceptance uses local storage root ${STORAGE_ROOT}"
fi

if [[ ! -x "${CLI}" ]]; then
  echo "==> moox-cli not found, building binaries first"
  "${ROOT}/scripts/build.sh"
fi

rm -rf "${STORAGE_ROOT}"
mkdir -p "${STORAGE_ROOT}" "$(dirname "${OUTPUT}")"

subjects=()
for file in "${CSV_FILES[@]}"; do
  if [[ ! -f "${file}" ]]; then
    echo "missing acceptance csv: ${file}" >&2
    exit 1
  fi
  symbol="$(basename "${file}")"
  symbol="${symbol%.*}"
  subjects+=("${symbol}")

  echo "==> import ${file}"
  "${CLI}" data csv import \
    --storage-root "${STORAGE_ROOT}" \
    --space "${SPACE}" \
    --dataset "${DATASET}" \
    --subject "${symbol}" \
    --freq "${FREQ}" \
    --file "${file}"
done

echo "==> export readback ${OUTPUT}"
"${CLI}" data rows export \
  --storage-root "${STORAGE_ROOT}" \
  --space "${SPACE}" \
  --dataset "${DATASET}" \
  --freq "${FREQ}" \
  --page-size "${PAGE_SIZE}" \
  --output "${OUTPUT}"

for subject in "${subjects[@]}"; do
  if ! grep -q "${subject}" "${OUTPUT}"; then
    echo "readback output does not contain ${subject}: ${OUTPUT}" >&2
    exit 1
  fi
done

echo "==> acceptance passed"
echo "storage root: ${STORAGE_ROOT}"
echo "readback: ${OUTPUT}"
