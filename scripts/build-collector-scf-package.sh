#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-v$(date +%Y%m%d%H%M%S)}"
OUT_DIR="${OUT_DIR:-${ROOT}/release/scf}"
OUT_PATH="${OUT_PATH:-${OUT_DIR}/collector-scf-${VERSION}.zip}"
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-scf.XXXXXX")"

cleanup() {
  rm -rf "${BUILD_DIR}"
}
trap cleanup EXIT

mkdir -p "${OUT_DIR}" "${BUILD_DIR}/package"

echo "==> build moox-collector-scf for linux/amd64"
(
  cd "${ROOT}/modules/collector"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
    -ldflags "-X main.Version=${VERSION}" \
    -o "${BUILD_DIR}/package/main" ./cmd/scf
)

echo "==> copy SCF runtime configs"
cp -R "${ROOT}/modules/collector/configs/." "${BUILD_DIR}/package/"
rm -f "${BUILD_DIR}/package/trpc_go.yaml"
if [[ -f "${BUILD_DIR}/package/example_trpc_go.yaml" ]]; then
  cp "${BUILD_DIR}/package/example_trpc_go.yaml" "${BUILD_DIR}/package/trpc_go.yaml"
  rm -f "${BUILD_DIR}/package/example_trpc_go.yaml"
fi

echo "==> generate market readiness lock"
(
  cd "${ROOT}/modules/collector"
  go run ./cmd/cli readiness-lock --markets-dir ./config/markets --output "${BUILD_DIR}/package/market-readiness-lock.json"
)

echo "==> package ${OUT_PATH}"
(
  cd "${BUILD_DIR}/package"
  zip -qr "${OUT_PATH}" .
)

echo "==> SCF package written to ${OUT_PATH}"
