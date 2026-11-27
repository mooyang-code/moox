#!/usr/bin/env bash
set -euo pipefail

SOURCE="${BASH_SOURCE[0]}"
while [[ -h "${SOURCE}" ]]; do
  SOURCE_DIR="$(cd -P "$(dirname "${SOURCE}")" >/dev/null 2>&1 && pwd)"
  SOURCE="$(readlink "${SOURCE}")"
  [[ "${SOURCE}" != /* ]] && SOURCE="${SOURCE_DIR}/${SOURCE}"
done
ROOT="$(cd -P "$(dirname "${SOURCE}")/../.." && pwd)"
if [[ "${1:-}" == "--help" ]]; then
  echo "Usage: SCF_SPACE_ID=<space> $0"
  echo "Requires: go, zip. Packages only the single linux/amd64 main binary; all market configs are embedded."
  exit 0
fi
SCF_SPACE_ID="${SCF_SPACE_ID:?SCF_SPACE_ID is required (for example: crypto)}"
SCF_ENTRYPOINT="${SCF_ENTRYPOINT:-market_data}"
[[ "${SCF_ENTRYPOINT}" == "market_data" ]] || { echo "unsupported SCF entrypoint: ${SCF_ENTRYPOINT}" >&2; exit 1; }
while (($#)); do
  case "$1" in
    *) echo "unsupported package parameter: $1" >&2; exit 1 ;;
  esac
done

VERSION="${VERSION:-v$(date +%Y%m%d%H%M%S)}"
OUT_DIR="${OUT_DIR:-${ROOT}/release/scf}"
OUT_PATH="${OUT_PATH:-${OUT_DIR}/collector-scf-${SCF_SPACE_ID}-${VERSION}.zip}"
if [[ "${OUT_PATH}" != /* ]]; then
  OUT_PATH="${PWD}/${OUT_PATH}"
fi
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-scf.XXXXXX")"
OUT_TEMP_DIR=""

cleanup() {
  rm -rf "${BUILD_DIR}"
  [[ -z "${OUT_TEMP_DIR}" ]] || rm -rf "${OUT_TEMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${OUT_DIR}" "${BUILD_DIR}/package"

# 各市场的 market.yaml、route.yaml、数据源运行参数（含 binance.yaml）和交易日历都内嵌在二进制中，包里只有单一 main。
echo "==> build moox-collector-scf for linux/amd64"
(
  cd "${ROOT}/modules/collector"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o "${BUILD_DIR}/package/main" "./cmd/scf/${SCF_ENTRYPOINT}"
)

echo "==> package ${OUT_PATH}"
mkdir -p "$(dirname "${OUT_PATH}")"
OUT_TEMP_DIR="$(mktemp -d "$(dirname "${OUT_PATH}")/.collector-scf-package.XXXXXX")"
TEMP_PACKAGE="${OUT_TEMP_DIR}/package.zip"
(
  umask 077
  cd "${BUILD_DIR}/package"
  zip -qr "${TEMP_PACKAGE}" main
)
chmod 0600 "${TEMP_PACKAGE}"
zip -T "${TEMP_PACKAGE}" >/dev/null
mv -f "${TEMP_PACKAGE}" "${OUT_PATH}"
rm -rf "${OUT_TEMP_DIR}"
OUT_TEMP_DIR=""

echo "==> SCF package written to ${OUT_PATH}"
