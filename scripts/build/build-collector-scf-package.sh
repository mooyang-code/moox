#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCF_SPACE_ID="${SCF_SPACE_ID:?SCF_SPACE_ID is required (for example: crypto_market)}"
SCF_ENTRYPOINT="${SCF_ENTRYPOINT:?SCF_ENTRYPOINT is required (crypto_market)}"
[[ "${SCF_ENTRYPOINT}" == "crypto_market" ]] || { echo "unsupported SCF entrypoint: ${SCF_ENTRYPOINT}" >&2; exit 1; }
CONFIG_DIR="${ROOT}/modules/collector/configs/scf/${SCF_SPACE_ID}"
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

BINANCE_CONFIG="${BUILD_DIR}/package/sources/market/binance.yaml"
if [[ -f "${BINANCE_CONFIG}" ]]; then
[[ -n "${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}" ]] || {
  echo "MOOX_STORAGE_PRIMARY_AUTH_SECRET is required to package Binance Storage credentials" >&2
  exit 1
}
python3 - "${BINANCE_CONFIG}" <<'PY'
import hashlib
import hmac
import os
import sys
import yaml

path = sys.argv[1]
with open(path, encoding="utf-8") as stream:
    document = yaml.safe_load(stream) or {}

rendered = 0

def render(value):
    global rendered
    if isinstance(value, dict):
        auth = value.get("auth_info")
        if isinstance(auth, dict):
            app_id = str(auth.get("app_id") or "").strip()
            if not app_id or "app_key" not in auth:
                raise ValueError("Binance Storage auth_info requires app_id and app_key")
            auth["app_key"] = hmac.new(
                os.environ["MOOX_STORAGE_PRIMARY_AUTH_SECRET"].encode(),
                app_id.encode(),
                hashlib.sha256,
            ).hexdigest()
            rendered += 1
        for child in value.values():
            render(child)
    elif isinstance(value, list):
        for child in value:
            render(child)

render(document)
if rendered == 0:
    raise ValueError("Binance source config contains no Storage auth_info")
with open(path, "w", encoding="utf-8") as stream:
    yaml.safe_dump(document, stream, sort_keys=False)
PY
fi

echo "==> package ${OUT_PATH}"
rm -f "${OUT_PATH}"
(
  umask 077
  cd "${BUILD_DIR}/package"
  zip -qr "${OUT_PATH}" .
)
chmod 0600 "${OUT_PATH}"

echo "==> SCF package written to ${OUT_PATH}"
