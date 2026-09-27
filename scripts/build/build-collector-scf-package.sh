#!/usr/bin/env bash
set -euo pipefail

SOURCE="${BASH_SOURCE[0]}"
while [[ -h "${SOURCE}" ]]; do
  SOURCE_DIR="$(cd -P "$(dirname "${SOURCE}")" >/dev/null 2>&1 && pwd)"
  SOURCE="$(readlink "${SOURCE}")"
  [[ "${SOURCE}" != /* ]] && SOURCE="${SOURCE_DIR}/${SOURCE}"
done
ROOT="$(cd -P "$(dirname "${SOURCE}")/../.." && pwd)"
SCF_SPACE_ID="${SCF_SPACE_ID:?SCF_SPACE_ID is required (for example: crypto)}"
SCF_ENTRYPOINT="${SCF_ENTRYPOINT:-market_data}"
[[ "${SCF_ENTRYPOINT}" == "market_data" ]] || { echo "unsupported SCF entrypoint: ${SCF_ENTRYPOINT}" >&2; exit 1; }
CONFIG_DIR="${ROOT}/modules/collector/configs/scf/${SCF_SPACE_ID}"
if [[ ! -d "${CONFIG_DIR}" ]]; then
  CONFIG_DIR="${ROOT}/modules/collector/configs/scf/market_data"
fi
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
if [[ "${SCF_SPACE_ID}" == "stockcn" ]]; then
  mkdir -p "${BUILD_DIR}/package/markets/stockcn"
  cp "${ROOT}/modules/collector/config/markets/stockcn/calendar.yaml" "${BUILD_DIR}/package/markets/stockcn/calendar.yaml"
  cp "${ROOT}/modules/collector/config/markets/stockcn/route.yaml" "${BUILD_DIR}/package/markets/stockcn/route.yaml"
fi
rm -f "${BUILD_DIR}/package/trpc_go.yaml" "${BUILD_DIR}/package/example_trpc_go.yaml"

render_storage_auth() {
  local file="$1"
  if [[ -z "${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}" ]]; then
    echo "MOOX_STORAGE_PRIMARY_AUTH_SECRET is required to package Binance Storage credentials" >&2
    exit 1
  fi
  python3 - "${file}" <<'PY'
import hmac
import hashlib
import os
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
secret = os.environ["MOOX_STORAGE_PRIMARY_AUTH_SECRET"]
text = path.read_text()
pattern = re.compile(r'(app_id:\s*")([^"]+)("\s*\n)([ \t]+app_key:\s*")[^"]*(")')


def key_for(app_id: str) -> str:
    return hmac.new(secret.encode(), app_id.encode(), hashlib.sha256).hexdigest()


rendered, count = pattern.subn(
    lambda match: f"{match.group(1)}{match.group(2)}{match.group(3)}{match.group(4)}{key_for(match.group(2))}{match.group(5)}",
    text,
)
if count == 0:
    raise SystemExit(f"{path}: Binance source config contains no Storage auth_info")
if 'binance-spot-collector' in rendered or 'binance-swap-collector' in rendered or re.search(r'app_key:\s*""', rendered):
    raise SystemExit(f"{path}: Storage Primary app_key is still a placeholder")
path.write_text(rendered)
print(f"rendered {count} Storage Primary app_key value(s) in {path}")
PY
}

if [[ -f "${BUILD_DIR}/package/sources/market/binance.yaml" ]]; then
  echo "==> render Storage Primary auth into sources/market/binance.yaml"
  render_storage_auth "${BUILD_DIR}/package/sources/market/binance.yaml"
fi

echo "==> package ${OUT_PATH}"
rm -f "${OUT_PATH}"
(
  umask 077
  cd "${BUILD_DIR}/package"
  zip -qr "${OUT_PATH}" .
)
chmod 0600 "${OUT_PATH}"

if unzip -p "${OUT_PATH}" sources/market/binance.yaml >"${BUILD_DIR}/binance-check.yaml" 2>/dev/null; then
  if grep -Eq 'app_key: ""|binance-spot-collector|binance-swap-collector' "${BUILD_DIR}/binance-check.yaml"; then
    echo "scf package still contains placeholder Storage Primary app_key" >&2
    exit 1
  fi
fi

echo "==> SCF package written to ${OUT_PATH}"
