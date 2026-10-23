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
  echo "Requires: go, zip. Credentials are never read or packaged."
  exit 0
fi
SCF_SPACE_ID="${SCF_SPACE_ID:?SCF_SPACE_ID is required (for example: crypto)}"
SCF_ENTRYPOINT="${SCF_ENTRYPOINT:-market_data}"
[[ "${SCF_ENTRYPOINT}" == "market_data" ]] || { echo "unsupported SCF entrypoint: ${SCF_ENTRYPOINT}" >&2; exit 1; }
if [[ -n "${SCF_CONFIG_DIR:-}" ]]; then
	CONFIG_DIR="${SCF_CONFIG_DIR}"
else
	CONFIG_DIR="${ROOT}/modules/collector/configs/scf/${SCF_SPACE_ID}"
	if [[ ! -d "${CONFIG_DIR}" ]]; then
    CONFIG_DIR="${ROOT}/modules/collector/configs/scf/market_data"
  fi
fi
while (($#)); do
  case "$1" in
    *) echo "unsupported package parameter: $1" >&2; exit 1 ;;
  esac
done
# 配置与凭据检查、路径规范化用 Go 实现（modules/collector/cmd/scfpackcheck），不再依赖 Python。
scf_check() { (cd "${ROOT}/modules/collector" && go run ./cmd/scfpackcheck "$@"); }
VERSION="${VERSION:-v$(date +%Y%m%d%H%M%S)}"
OUT_DIR="${OUT_DIR:-${ROOT}/release/scf}"
OUT_PATH="${OUT_PATH:-${OUT_DIR}/collector-scf-${SCF_SPACE_ID}-${VERSION}.zip}"
if [[ "${OUT_PATH}" != /* ]]; then
  OUT_PATH="${PWD}/${OUT_PATH}"
fi
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-scf.XXXXXX")"
OUT_TEMP_DIR=""

CONFIG_DIR="$(scf_check normpath "${CONFIG_DIR}")"
[[ -d "${CONFIG_DIR}" ]] || {
  echo "SCF config directory does not exist: ${CONFIG_DIR}" >&2
  exit 1
}
[[ ! -L "${CONFIG_DIR}" ]] || { echo "SCF config directory symlinks are not permitted" >&2; exit 1; }
[[ -d "${CONFIG_DIR}/sources" && ! -L "${CONFIG_DIR}/sources" ]] || {
  echo "SCF sources must be a regular non-symlink directory" >&2
  exit 1
}
if find "${CONFIG_DIR}/sources" -type l -print -quit | grep -q .; then
  echo "SCF source symlinks are not permitted" >&2
  exit 1
fi
if [[ "${SCF_SPACE_ID}" == "stockcn" ]]; then
  for source in "${ROOT}/modules/collector/config/markets/stockcn/calendar.yaml" "${ROOT}/modules/collector/config/markets/stockcn/route.yaml"; do
    [[ -f "${source}" && ! -L "${source}" ]] || {
      echo "SCF stockcn assets must be regular non-symlink files" >&2
      exit 1
    }
  done
fi

cleanup() {
  rm -rf "${BUILD_DIR}"
  [[ -z "${OUT_TEMP_DIR}" ]] || rm -rf "${OUT_TEMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${OUT_DIR}" "${BUILD_DIR}/package"

echo "==> build moox-collector-scf for linux/amd64"
(
  cd "${ROOT}/modules/collector"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o "${BUILD_DIR}/package/main" "./cmd/scf/${SCF_ENTRYPOINT}"
)

echo "==> copy ${SCF_SPACE_ID} SCF runtime configs"
cp -R "${CONFIG_DIR}/sources" "${BUILD_DIR}/package/sources"
if [[ "${SCF_SPACE_ID}" == "stockcn" ]]; then
  mkdir -p "${BUILD_DIR}/package/markets/stockcn"
  cp "${ROOT}/modules/collector/config/markets/stockcn/calendar.yaml" "${BUILD_DIR}/package/markets/stockcn/calendar.yaml"
  cp "${ROOT}/modules/collector/config/markets/stockcn/route.yaml" "${BUILD_DIR}/package/markets/stockcn/route.yaml"
fi

echo "==> check SCF package is credential-free"
scf_check check "${BUILD_DIR}/package"

echo "==> package ${OUT_PATH}"
mkdir -p "$(dirname "${OUT_PATH}")"
OUT_TEMP_DIR="$(mktemp -d "$(dirname "${OUT_PATH}")/.collector-scf-package.XXXXXX")"
TEMP_PACKAGE="${OUT_TEMP_DIR}/package.zip"
(
  umask 077
  cd "${BUILD_DIR}/package"
  zip -qr "${TEMP_PACKAGE}" .
)
chmod 0600 "${TEMP_PACKAGE}"
zip -T "${TEMP_PACKAGE}" >/dev/null
mv -f "${TEMP_PACKAGE}" "${OUT_PATH}"
rm -rf "${OUT_TEMP_DIR}"
OUT_TEMP_DIR=""

echo "==> SCF package written to ${OUT_PATH}"
