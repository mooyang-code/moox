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
  echo "Usage: SCF_SPACE_ID=<space> $0 --eventbus-ca-file <public-ca.pem>"
  echo "Requires: go, zip, python3 with PyYAML, openssl. Credentials are never read or packaged."
  exit 0
fi
SCF_SPACE_ID="${SCF_SPACE_ID:?SCF_SPACE_ID is required (for example: crypto)}"
SCF_ENTRYPOINT="${SCF_ENTRYPOINT:-market_data}"
[[ "${SCF_ENTRYPOINT}" == "market_data" ]] || { echo "unsupported SCF entrypoint: ${SCF_ENTRYPOINT}" >&2; exit 1; }
CONFIG_DIR="${ROOT}/modules/collector/configs/scf/${SCF_SPACE_ID}"
if [[ ! -d "${CONFIG_DIR}" ]]; then
  CONFIG_DIR="${ROOT}/modules/collector/configs/scf/market_data"
fi
EVENTBUS_CA_FILE=""
while (($#)); do
  case "$1" in
    --eventbus-ca-file)
      [[ $# -ge 2 ]] || { echo "--eventbus-ca-file requires a public PEM file" >&2; exit 1; }
      EVENTBUS_CA_FILE="$2"
      shift 2
      ;;
    *) echo "unsupported package parameter: $1" >&2; exit 1 ;;
  esac
done
[[ -f "${EVENTBUS_CA_FILE}" ]] || { echo "--eventbus-ca-file is required" >&2; exit 1; }
python3 -c 'import yaml' 2>/dev/null || { echo "SCF packaging requires python3 with PyYAML" >&2; exit 1; }
command -v openssl >/dev/null || { echo "SCF packaging requires openssl" >&2; exit 1; }
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
cp "${CONFIG_DIR}/config.yaml" "${BUILD_DIR}/package/config.yaml"
if [[ -d "${CONFIG_DIR}/sources" ]]; then
  cp -R "${CONFIG_DIR}/sources" "${BUILD_DIR}/package/sources"
fi
if [[ "${SCF_SPACE_ID}" == "stockcn" ]]; then
  mkdir -p "${BUILD_DIR}/package/markets/stockcn"
  cp "${ROOT}/modules/collector/config/markets/stockcn/calendar.yaml" "${BUILD_DIR}/package/markets/stockcn/calendar.yaml"
  cp "${ROOT}/modules/collector/config/markets/stockcn/route.yaml" "${BUILD_DIR}/package/markets/stockcn/route.yaml"
fi
rm -f "${BUILD_DIR}/package/trpc_go.yaml" "${BUILD_DIR}/package/example_trpc_go.yaml"

python3 - "${BUILD_DIR}/package" "${EVENTBUS_CA_FILE}" <<'PY'
import pathlib
import re
import subprocess
import sys

try:
    import yaml
except ImportError:
    raise SystemExit("credential-free SCF packaging requires Python PyYAML")

root = pathlib.Path(sys.argv[1])
ca = pathlib.Path(sys.argv[2]).read_bytes()
remaining = ca.strip()
count = 0
while remaining:
    match = re.match(br"-----BEGIN CERTIFICATE-----\r?\n[A-Za-z0-9+/=\r\n]+-----END CERTIFICATE-----", remaining)
    if not match:
        raise SystemExit("EventBus CA must contain only public CA certificates")
    cert = match.group(0)
    result = subprocess.run(["openssl", "x509", "-noout", "-text"], input=cert, capture_output=True)
    if result.returncode or b"CA:TRUE" not in result.stdout:
        raise SystemExit("EventBus CA contains an invalid CA certificate")
    count += 1
    remaining = remaining[match.end():].strip()
if not count:
    raise SystemExit("EventBus CA requires a public CA certificate")

class UniqueLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        keys = [self.construct_object(key, deep=deep) for key, _ in node.value]
        if len(set(keys)) != len(keys):
            raise ValueError("duplicate configuration key")
        return super().construct_mapping(node, deep=deep)

def check(value, path=""):
    if isinstance(value, dict):
        for key, child in value.items():
            name = str(key).lower().replace("-", "_")
            field_path = path + "." + str(key)
            sensitive = name in ("app_key", "key", "token") or any(part in name for part in ("secret", "password", "private_key")) or name.endswith(("api_key", "app_keys_json", "hmac_key_file", "_token")) or (name.endswith("access_key") and field_path != ".system.service_auth.access_key")
            if sensitive and child not in (None, ""):
                raise ValueError("nonempty credential field is not permitted")
            check(child, field_path)
    elif isinstance(value, list):
        for child in value:
            check(child, path)
    elif isinstance(value, str) and "PRIVATE KEY" in value.upper():
        raise ValueError("private key payload is not permitted")

for path in root.rglob("*"):
    if path.is_symlink():
        raise SystemExit("SCF package symlinks are not permitted")
    if not path.is_file() or path.name == "main":
        continue
    if path.suffix not in (".yaml", ".yml"):
        raise SystemExit("unexpected SCF configuration payload")
    try:
        content = path.read_text()
        if any(isinstance(token, (yaml.tokens.AliasToken, yaml.tokens.AnchorToken)) for token in yaml.scan(content)):
            raise ValueError("configuration aliases are not permitted")
        for document in yaml.load_all(content, Loader=UniqueLoader):
            check(document)
    except Exception:
        raise SystemExit("SCF configuration must be valid and credential-free: " + str(path.relative_to(root)))
(root / "certs").mkdir()
(root / "certs" / "eventbus-ca.pem").write_bytes(ca)
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
