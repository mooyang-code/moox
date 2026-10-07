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
if [[ -n "${SCF_CONFIG_DIR:-}" ]]; then
	CONFIG_DIR="${SCF_CONFIG_DIR}"
else
	CONFIG_DIR="${ROOT}/modules/collector/configs/scf/${SCF_SPACE_ID}"
	if [[ ! -d "${CONFIG_DIR}" ]]; then
    CONFIG_DIR="${ROOT}/modules/collector/configs/scf/market_data"
  fi
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
OUT_TEMP_DIR=""

CONFIG_DIR="$(python3 -c 'import os,sys; print(os.path.normpath(sys.argv[1]))' "${CONFIG_DIR}")"
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

python3 - "${BUILD_DIR}/package" "${EVENTBUS_CA_FILE}" <<'PY'
import pathlib
import re
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

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
    with tempfile.NamedTemporaryFile() as cert_file:
        cert_file.write(cert)
        cert_file.flush()
        result = subprocess.run(["openssl", "x509", "-in", cert_file.name, "-noout", "-text"], capture_output=True, text=True)
    if result.returncode:
        raise SystemExit("EventBus CA contains an invalid CA certificate")
    text = result.stdout
    if not re.search(r"X509v3 Basic Constraints(?:: critical)?\s*\n\s*CA:TRUE\b", text):
        raise SystemExit("EventBus CA certificate is not a CA")
    before = re.search(r"Not Before:\s*(.+)", text)
    after = re.search(r"Not After\s*:\s*(.+)", text)
    if not before or not after:
        raise SystemExit("EventBus CA certificate has invalid validity dates")
    parse_date = lambda value: datetime.strptime(value.strip(), "%b %d %H:%M:%S %Y %Z").replace(tzinfo=timezone.utc)
    now = datetime.now(timezone.utc)
    try:
        if now < parse_date(before.group(1)) or now > parse_date(after.group(1)):
            raise SystemExit("EventBus CA certificate is outside its validity period")
    except ValueError:
        raise SystemExit("EventBus CA certificate has invalid validity dates")
    usage = re.search(r"X509v3 Key Usage:[^\n]*\n((?:[ \t]+[^\n]*\n)+)", text)
    if usage and "Certificate Sign" not in usage.group(1):
        raise SystemExit("EventBus CA certificate key usage does not permit certificate signing")
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

credential_assignment = re.compile(r"(^|[^A-Za-z0-9_])[\"']?(?:MOOX_[A-Z0-9_]*(?:SECRET(?:_KEY)?|PASSWORD|TOKEN|PRIVATE_KEY|APP_KEYS?_JSON|APP_KEY|API_KEY|JWT|NKEY(?:_SEED)?|ACCESS_KEY)|TENCENTCLOUD_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN)|TENCENT_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN))[\"']?[ \t]*[:=][ \t]*", re.IGNORECASE | re.MULTILINE)

def has_nonempty_credential_assignment(content):
    for match in credential_assignment.finditer(content):
        tail = content[match.end():]
        value = tail.splitlines()[0].strip() if tail else ""
        if not value or value.startswith("#"):
            continue
        if value[0] in ("'", '"'):
            closing = value.find(value[0], 1)
            if closing < 0:
                return True
            remainder = value[closing + 1:].strip()
            if value[1:closing].strip() or remainder and not remainder.startswith("#"):
                return True
            continue
        fields = value.split(None, 1)
        if not fields:
            continue
        if fields[0] == "~" or fields[0].casefold() == "null":
            remainder = value[len(fields[0]):].strip()
            if remainder and not remainder.startswith("#"):
                return True
            continue
        return True
    return False

def empty_value(value):
    return value is None or isinstance(value, str) and not value.strip()
credential_environment_name = re.compile(r"^(?:MOOX_[A-Z0-9_]*(?:SECRET(?:_KEY)?|PASSWORD|TOKEN|PRIVATE_KEY|APP_KEYS?_JSON|APP_KEY|API_KEY|JWT|NKEY(?:_SEED)?|ACCESS_KEY)|TENCENTCLOUD_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN)|TENCENT_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN))$", re.IGNORECASE)

def check(value, path=""):
    if isinstance(value, dict):
        def environment_field(field):
            matches = [child for key, child in value.items() if str(key).casefold() == field]
            if len(matches) > 1:
                raise ValueError("duplicate credential environment field")
            return matches[0] if matches else None

        env_name = environment_field("name")
        env_value = environment_field("value")
        if isinstance(env_name, str) and credential_environment_name.fullmatch(env_name.strip()) and not empty_value(env_value):
            raise ValueError("credential environment variable value is not permitted")
        for key, child in value.items():
            name = str(key).lower().replace("-", "_")
            field_path = path + "." + str(key)
            sensitive = name in ("app_key", "key", "token", "seed") or any(part in name for part in ("secret", "password", "private_key", "jwt", "nkey", "credential")) or name.endswith(("api_key", "app_keys_json", "hmac_key_file", "_token", "_seed")) or (name.endswith("access_key") and field_path != ".system.service_auth.access_key")
            if sensitive and not empty_value(child):
                raise ValueError("nonempty credential field is not permitted")
            check(child, field_path)
    elif isinstance(value, list):
        for child in value:
            check(child, path)
    elif isinstance(value, str) and "PRIVATE KEY" in value.upper():
        raise ValueError("private key payload is not permitted")
    elif isinstance(value, str) and re.search(r"-----BEGIN (?:NATS USER JWT|USER NKEY SEED)-----", value, re.IGNORECASE):
        raise ValueError("NATS credential material is not permitted")

for path in root.rglob("*"):
    if path.is_symlink():
        raise SystemExit("SCF package symlinks are not permitted")
    if not path.is_file() or path.name == "main":
        continue
    if path.suffix not in (".yaml", ".yml"):
        raise SystemExit("unexpected SCF configuration payload")
    try:
        content = path.read_text()
        if "PRIVATE KEY" in content.upper():
            raise ValueError("private key payload is not permitted")
        if re.search(r"-----BEGIN (?:NATS USER JWT|USER NKEY SEED)-----", content, re.IGNORECASE):
            raise ValueError("NATS credential material is not permitted")
        if has_nonempty_credential_assignment(content):
            raise ValueError("credential assignment is not permitted")
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
