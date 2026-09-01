#!/usr/bin/env bash
set -euo pipefail

# Resolve the repository root from both this implementation path and the
# public scripts/build-collector-scf-package.sh symlink.
SOURCE="${BASH_SOURCE[0]}"
while [[ -L "${SOURCE}" ]]; do
  SOURCE_DIR="$(cd -P "$(dirname "${SOURCE}")" >/dev/null 2>&1 && pwd)"
  SOURCE="$(readlink "${SOURCE}")"
  [[ "${SOURCE}" != /* ]] && SOURCE="${SOURCE_DIR}/${SOURCE}"
done
SCRIPT_DIR="$(cd -P "$(dirname "${SOURCE}")" >/dev/null 2>&1 && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SCF_SPACE_ID="${SCF_SPACE_ID:?SCF_SPACE_ID is required (for example: stock_cn)}"
SCF_ENTRYPOINT="${SCF_ENTRYPOINT:?SCF_ENTRYPOINT is required (market_data)}"
case "${SCF_ENTRYPOINT}" in
  market_data) ;;
  *) echo "unsupported SCF entrypoint: ${SCF_ENTRYPOINT}" >&2; exit 1 ;;
esac
case "${SCF_SPACE_ID}" in
  crypto|stock_cn|stock_hk|stock_us) CONFIG_SPACE_ID="market_data" ;;
  *) CONFIG_SPACE_ID="${SCF_SPACE_ID}" ;;
esac
CONFIG_DIR="${ROOT}/modules/collector/configs/scf/${CONFIG_SPACE_ID}"
VERSION="${VERSION:-v$(date +%Y%m%d%H%M%S)}"
OUT_DIR="${OUT_DIR:-${ROOT}/release/scf}"
OUT_PATH="${OUT_PATH:-${OUT_DIR}/collector-scf-${SCF_SPACE_ID}-${VERSION}.zip}"
if [[ "${OUT_PATH}" != /* ]]; then
  OUT_PATH="${PWD}/${OUT_PATH}"
fi
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-scf.XXXXXX")"

[[ -d "${CONFIG_DIR}" ]] || {
  echo "SCF config directory does not exist: ${CONFIG_DIR}" >&2
  exit 1
}

cleanup() {
  rm -rf "${BUILD_DIR}"
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
cp -R "${CONFIG_DIR}/." "${BUILD_DIR}/package/"
rm -f "${BUILD_DIR}/package/trpc_go.yaml" "${BUILD_DIR}/package/example_trpc_go.yaml"

echo "==> package ${OUT_PATH}"
rm -f "${OUT_PATH}"
(
  umask 077
  cd "${BUILD_DIR}/package"
  zip -qr "${OUT_PATH}" .
)
chmod 0600 "${OUT_PATH}"

echo "==> SCF package written to ${OUT_PATH}"
