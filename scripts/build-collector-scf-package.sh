#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-v$(date +%Y%m%d%H%M%S)}"
OUT_DIR="${OUT_DIR:-${ROOT}/release/scf}"
OUT_PATH="${OUT_PATH:-${OUT_DIR}/collector-scf-${VERSION}.zip}"
if [[ "${OUT_PATH}" != /* ]]; then
  OUT_PATH="${PWD}/${OUT_PATH}"
fi
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-scf.XXXXXX")"

python3 -c 'import yaml' >/dev/null 2>&1 || {
  echo "PyYAML is required to render the SCF runtime configuration" >&2
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
python3 - "${BUILD_DIR}/package/trpc_go.yaml" <<'PY'
import sys
import yaml

path = sys.argv[1]
with open(path, encoding="utf-8") as stream:
    document = yaml.safe_load(stream) or {}
plugins = document.setdefault("plugins", {})
logs = plugins.setdefault("log", {})
logs["default"] = [item for item in logs.get("default", []) if not isinstance(item, dict) or item.get("writer") != "cls"]
server = document.setdefault("server", {})
server["service"] = [item for item in server.get("service", []) if not isinstance(item, dict) or ".scf_observability." not in item.get("name", "")]
with open(path, "w", encoding="utf-8") as stream:
    yaml.safe_dump(document, stream, sort_keys=False)
PY

[[ -n "${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}" ]] || {
  echo "MOOX_STORAGE_PRIMARY_AUTH_SECRET is required to package Binance Storage credentials" >&2
  exit 1
}
python3 - "${BUILD_DIR}/package/sources/market/binance.yaml" <<'PY'
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

echo "==> package ${OUT_PATH}"
rm -f "${OUT_PATH}"
(
  umask 077
  cd "${BUILD_DIR}/package"
  zip -qr "${OUT_PATH}" .
)
chmod 0600 "${OUT_PATH}"

echo "==> SCF package written to ${OUT_PATH}"
