#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="localhost"
DEPLOY_DIR="${MOOX_DEPLOY_DIR:-${HOME}/moox}"
STAGE_DIR=""
SKIP_BUILD=0
NO_START=0
PACKAGE_ONLY=0
PACKAGE_ARCHIVE=""
DEPLOY_PROFILE=""
AUTO_GATEWAY_INPUTS=0
WITH_STORAGE=1
WITH_STORAGE_SHARD=0
WITH_ARCHIVE=1
WITH_EVENTBUS=1
WITH_WEB_HOST=1
STORAGE_EXTERNAL_LISTEN=0
WITH_CLOUDNODE=1
WITH_COLLECTOR=1
WITH_FACTOR=1
WITH_STRATEGY=1
WITH_MONITOR=1
WITH_ADMIN=1
WITH_GATEWAY=1
BUILD_WEB_ASSETS=1
RESET_DATA=0
TARGET_GOOS=""
TARGET_GOARCH=""
METRICS_METADATA_URL="${MOOX_METRICS_STORAGE_METADATA_URL:-http://127.0.0.1:20200}"
METRICS_ROUTE_SEED="${MOOX_METRICS_STORAGE_ROUTE_SEED:-}"
HOST_ROUTE_SEED="${MOOX_HOST_STORAGE_ROUTE_SEED:-}"
EVENTBUS_URL_ENV="${MOOX_EVENTBUS_NATS_URL:-${MOOX_EVENTBUS_URL:-}}"
METRICS_EVENTBUS_URL_ENV="${MOOX_METRICS_EVENTBUS_URL:-}"
PUBLIC_HOST=""
BROWSER_HTTPS_PORT=9527
SERVICE_HTTPS_PORT=11001
LOCAL_CA=auto
LOCAL_CA_OUTPUT=""
FETCHED_CA_FILE=""
TARGET_CA=auto
ENABLE_CLS=0
CLOUD_ACCOUNT_ID=""
NODE_ID=""
GATEWAY_CONTROL_URL=""
GATEWAY_CA_BUNDLE=""
GATEWAY_CONTROL_KEY_FILE=""
GATEWAY_SERVICE_KEY_FILE=""
MONITOR_INSTANCE_ID=""
STAGE_DEPLOY_LOCK=""
STAGE_DEPLOY_LOCK_TOKEN="$$.${RANDOM}.${RANDOM}"
STAGE_DEPLOY_LOCK_HELD=0
STAGE_DEPLOY_LOCK_LEASE_SECONDS="${MOOX_DEPLOY_STAGE_LOCK_LEASE_SECONDS:-3600}"
STAGE_DEPLOY_LOCK_OWNER_TOKEN=""
STAGE_DEPLOY_LOCK_OWNER_HOST=""
STAGE_DEPLOY_LOCK_OWNER_PID=""
STAGE_DEPLOY_LOCK_OWNER_CREATED_AT=""
LOCAL_DEPLOY_ARCHIVE=""
REMOTE_DEPLOY_ARCHIVE=""

usage() {
  cat <<'EOF'
Usage:
  scripts/deploy-moox.sh [options]

Options:
  --target <localhost|user@host>  Deploy target. Default: localhost.
  --dir <path>                    Deploy directory on target. Default: ~/moox.
  --goos <linux|darwin>           Target OS. Auto-detected by default.
  --goarch <amd64|arm64>          Target arch. Auto-detected by default.
  --stage <path>                  Local staging directory. Default: release/deploy-stage/moox.
  --skip-build                    Reuse binaries from ./bin.
  --no-start                      Deploy package only, do not start services.
  --profile <control|storage>     Package an initial setup deployment unit.
  --package-only                  Build the selected deployment archive without transport or install.
  --archive <path>                Output archive required by --package-only.
  --no-storage                    Do not package/stop/start moox-storage; preserve existing remote storage files.
  --with-storage-shard            Package the optional independent DataShard process.
  --no-archive                    Do not package/start moox-archive.
  --no-eventbus                   Do not package/stop/start moox-eventbus; preserve existing remote EventBus files.
  --no-web-host                   Do not package/start moox-web-host.
  --no-cloudnode                  Do not package/start moox-cloudnode.
  --no-collector                  Do not package/start moox-collector.
  --no-factor                     Do not package/start moox-factor.
  --no-strategy                   Do not package/start moox-strategy.
  --no-monitor                    Do not package/start moox-monitor.
  --no-admin                      Build a data-plane node without Admin, browser assets, schema, or credentials.
  --build-web-assets              Rebuild Vue dist and statik assets before building web-host. Default when web-host is enabled.
  --reuse-web-assets              Reuse current embedded statik assets when building web-host.
  --reset-data                    Remove target data directory before deploying. Use when rebuilding from examples.
  --public-host <ip-or-dns>       Certificate SAN and public HTTPS host; enables managed Caddy.
  --browser-https-port <port>     Browser HTTPS edge. Default: 9527.
  --service-https-port <port>     Service HTTPS edge. Default: 11001.
  --local-ca <auto|install|skip>  Operator CA workflow. Default: auto (check and install).
  --local-ca-output <path>        Fetched root CA path. Default: ~/.moox/certs/moox-caddy-root-<public-host>.crt.
  --target-ca <auto|skip>         Install the CA in the target trust store when permitted. Default: auto.
  --caddy-conflict <fail>         Refuse unrelated listeners (the only supported policy).
  --enable-cls                    Prepare fixed CLS resources and add production CLS writers.
  --cloud-account-id <id>         Tencent cloud account for CLS; default is the first account.
  --node-id <id>                  Stable Gateway node ID (required).
  --gateway-control-url <url>     Central Admin browser origin used by Gateway (required).
  --gateway-ca-bundle <path>      Public PEM bundle containing peer Caddy roots (required).
  --gateway-control-key-file <p>  Local 0600 raw cluster control key file (required).
  --gateway-service-key-file <p>  Local 0600 raw cluster service key file (required).
  --monitor-instance-id <id>      Stable Monitor instance ID (required when Monitor is enabled).
  -h, --help                      Show this help.

Examples:
  scripts/deploy-moox.sh --target localhost --dir ~/moox/dev
  scripts/deploy-moox.sh --target user@host --dir ~/moox/prod --goos linux --goarch amd64
  scripts/deploy-moox.sh --target localhost --dir /tmp/moox --skip-build --no-start
EOF
}

apply_profile() {
  case "$1" in
    control)
      WITH_ADMIN=1
      WITH_GATEWAY=1
      WITH_WEB_HOST=1
      WITH_STORAGE=0
      WITH_ARCHIVE=0
      WITH_EVENTBUS=0
      WITH_CLOUDNODE=0
      WITH_COLLECTOR=0
      WITH_FACTOR=0
      WITH_STRATEGY=0
      WITH_MONITOR=0
      ;;
    storage)
      WITH_ADMIN=0
      # Storage View still reaches PrimaryStore through the node Gateway.
      # Keep this profile self-contained without adding the Admin process.
      WITH_GATEWAY=1
      WITH_WEB_HOST=0
      WITH_STORAGE=1
      WITH_STORAGE_SHARD=0
      WITH_ARCHIVE=0
      WITH_EVENTBUS=0
      WITH_CLOUDNODE=0
      WITH_COLLECTOR=0
      WITH_FACTOR=0
      WITH_STRATEGY=0
      WITH_MONITOR=0
      STORAGE_EXTERNAL_LISTEN=1
      ;;
    *) fail "unsupported deployment profile: $1" ;;
  esac
}

log() {
  printf '[deploy-moox] %s\n' "$*"
}

fail() {
  printf '[deploy-moox] ERROR: %s\n' "$*" >&2
  exit 1
}

cleanup_stage_deploy_lock() {
  [[ "${STAGE_DEPLOY_LOCK_HELD}" -eq 1 && -n "${STAGE_DEPLOY_LOCK}" ]] || return 0
  local owner_token=""
  if [[ -f "${STAGE_DEPLOY_LOCK}/owner" ]]; then
    owner_token="$(sed -n 's/^token=//p' "${STAGE_DEPLOY_LOCK}/owner" | head -1)"
  fi
  if [[ "${owner_token}" == "${STAGE_DEPLOY_LOCK_TOKEN}" ]]; then
    rm -f "${STAGE_DEPLOY_LOCK}/owner"
    rmdir "${STAGE_DEPLOY_LOCK}" 2>/dev/null || true
  fi
  STAGE_DEPLOY_LOCK_HELD=0
}

cleanup_deploy_artifacts() {
  cleanup_stage_deploy_lock
  if [[ -n "${LOCAL_DEPLOY_ARCHIVE}" ]]; then
    rm -f "${LOCAL_DEPLOY_ARCHIVE}"
    LOCAL_DEPLOY_ARCHIVE=""
  fi
  if [[ -n "${REMOTE_DEPLOY_ARCHIVE}" && -n "${TARGET}" ]] && ! is_local_target; then
    ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" \
      "rm -f -- $(shell_quote "${REMOTE_DEPLOY_ARCHIVE}")" >/dev/null 2>&1 || true
    REMOTE_DEPLOY_ARCHIVE=""
  fi
}

# The lock lives beside (not inside) STAGE_DIR, so prepare_stage cannot remove
# it. Normal exits remove only the matching owner token; interrupted deploys
# leave a stale lock that operators must remove after confirming no owner runs.
trap cleanup_deploy_artifacts EXIT

read_stage_deploy_lock_owner() {
  local owner="$1" key value token_count=0 host_count=0 pid_count=0 created_count=0
  STAGE_DEPLOY_LOCK_OWNER_TOKEN=""
  STAGE_DEPLOY_LOCK_OWNER_HOST=""
  STAGE_DEPLOY_LOCK_OWNER_PID=""
  STAGE_DEPLOY_LOCK_OWNER_CREATED_AT=""
  [[ -f "${owner}" ]] || return 1
  while IFS='=' read -r key value; do
    case "${key}" in
      token) STAGE_DEPLOY_LOCK_OWNER_TOKEN="${value}"; token_count=$((token_count + 1)) ;;
      host) STAGE_DEPLOY_LOCK_OWNER_HOST="${value}"; host_count=$((host_count + 1)) ;;
      pid) STAGE_DEPLOY_LOCK_OWNER_PID="${value}"; pid_count=$((pid_count + 1)) ;;
      created_at) STAGE_DEPLOY_LOCK_OWNER_CREATED_AT="${value}"; created_count=$((created_count + 1)) ;;
      *) return 1 ;;
    esac
  done <"${owner}"
  [[ "${token_count}" -eq 1 && "${host_count}" -eq 1 && "${pid_count}" -eq 1 && "${created_count}" -eq 1 ]] || return 1
  [[ "${STAGE_DEPLOY_LOCK_OWNER_TOKEN}" =~ ^[A-Za-z0-9._-]+$ ]] || return 1
  [[ "${STAGE_DEPLOY_LOCK_OWNER_HOST}" =~ ^[A-Za-z0-9_.-]+$ ]] || return 1
  [[ "${STAGE_DEPLOY_LOCK_OWNER_PID}" =~ ^[0-9]+$ ]] || return 1
  [[ "${STAGE_DEPLOY_LOCK_OWNER_CREATED_AT}" =~ ^[0-9]+$ ]] || return 1
  (( STAGE_DEPLOY_LOCK_OWNER_PID > 0 )) || return 1
}

stage_deploy_lock_is_stale() {
  read_stage_deploy_lock_owner "${STAGE_DEPLOY_LOCK}/owner" || return 1
  if [[ "${STAGE_DEPLOY_LOCK_OWNER_HOST}" == "$(hostname)" ]]; then
    if kill -0 "${STAGE_DEPLOY_LOCK_OWNER_PID}" 2>/dev/null || \
      ps -p "${STAGE_DEPLOY_LOCK_OWNER_PID}" >/dev/null 2>&1; then
      return 1
    fi
    return 0
  fi
  local now age
  now="$(date +%s)"
  (( STAGE_DEPLOY_LOCK_OWNER_CREATED_AT <= now )) || return 1
  age=$((now - STAGE_DEPLOY_LOCK_OWNER_CREATED_AT))
  (( age >= STAGE_DEPLOY_LOCK_LEASE_SECONDS ))
}

write_stage_deploy_lock_owner() {
  local owner_tmp="${STAGE_DEPLOY_LOCK}/owner.next.${STAGE_DEPLOY_LOCK_TOKEN}"
  if ! (umask 077; printf 'token=%s\nhost=%s\npid=%s\ncreated_at=%s\n' \
    "${STAGE_DEPLOY_LOCK_TOKEN}" "$(hostname)" "$$" "$(date +%s)" >"${owner_tmp}"); then
    rm -f "${owner_tmp}"
    return 1
  fi
  mv -f "${owner_tmp}" "${STAGE_DEPLOY_LOCK}/owner"
}

takeover_stage_deploy_lock() {
  local stale_lock="${STAGE_DEPLOY_LOCK}.stale.${STAGE_DEPLOY_LOCK_TOKEN}"
  rm -rf "${stale_lock}"
  mv "${STAGE_DEPLOY_LOCK}" "${stale_lock}" 2>/dev/null || return 1
  if ! mkdir "${STAGE_DEPLOY_LOCK}" 2>/dev/null; then
    if [[ ! -e "${STAGE_DEPLOY_LOCK}" ]]; then
      mv "${stale_lock}" "${STAGE_DEPLOY_LOCK}" 2>/dev/null || true
    else
      rm -rf "${stale_lock}"
    fi
    return 1
  fi
  if ! write_stage_deploy_lock_owner; then
    rm -rf "${STAGE_DEPLOY_LOCK}"
    mv "${stale_lock}" "${STAGE_DEPLOY_LOCK}" 2>/dev/null || true
    return 1
  fi
  rm -rf "${stale_lock}"
  STAGE_DEPLOY_LOCK_HELD=1
}

acquire_stage_deploy_lock() {
  [[ "${ENABLE_CLS}" -eq 1 ]] || return 0
  [[ "${STAGE_DEPLOY_LOCK_LEASE_SECONDS}" =~ ^[0-9]+$ ]] && \
    (( STAGE_DEPLOY_LOCK_LEASE_SECONDS >= 3600 && STAGE_DEPLOY_LOCK_LEASE_SECONDS <= 86400 )) || \
    fail "MOOX_DEPLOY_STAGE_LOCK_LEASE_SECONDS must be 3600..86400"
  local lock_base="${STAGE_DIR}"
  while [[ "${lock_base}" != "/" && "${lock_base}" == */ ]]; do
    lock_base="${lock_base%/}"
  done
  [[ -n "${lock_base}" ]] || lock_base="/"
  STAGE_DEPLOY_LOCK="${lock_base}.deploy.lock"
  mkdir -p "$(dirname "${STAGE_DEPLOY_LOCK}")"
  if mkdir "${STAGE_DEPLOY_LOCK}" 2>/dev/null; then
    if ! write_stage_deploy_lock_owner; then
      rm -rf "${STAGE_DEPLOY_LOCK}"
      fail "failed to initialize CLS stage deployment lock: ${STAGE_DEPLOY_LOCK}"
    fi
    STAGE_DEPLOY_LOCK_HELD=1
    return 0
  fi
  if stage_deploy_lock_is_stale && takeover_stage_deploy_lock; then
    return 0
  fi
  fail "CLS stage deployment lock already held: ${STAGE_DEPLOY_LOCK}; remove it only after confirming no deployment is running"
}

generate_secret() {
  local cli="$1" purpose="$2" output secret
  if [[ ! -x "${cli}" ]]; then
    openssl rand -hex 32
    return
  fi
  output=$("${cli}" random-secret --bytes 32)
  secret=$(printf '%s' "${output}" | sed -n 's/.*"secret"[[:space:]]*:[[:space:]]*"\([0-9a-f]*\)".*/\1/p')
  [[ -n "${secret}" ]] || secret=$(printf '%s' "${output}" | tr -d '\r\n')
  [[ "${secret}" =~ ^[0-9a-f]{64}$ ]] || fail "moox-admin-cli returned an invalid ${purpose} secret"
  printf '%s' "${secret}"
}

validate_cloud_account_id_arg() {
  [[ $# -ge 2 ]] || fail "--cloud-account-id requires a value"
  local value="$2"
  [[ "${value}" != -* ]] || fail "--cloud-account-id requires a value"
  [[ -n "${value//[[:space:]]/}" ]] || fail "--cloud-account-id cannot be empty"
  [[ "${value}" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ ]] || \
    fail "cloud account ID must match [A-Za-z0-9][A-Za-z0-9._:-]{0,127}"
}

validate_gateway_control_url() {
  command -v python3 >/dev/null 2>&1 || fail "python3 is required to validate --gateway-control-url"
  python3 - "$1" <<'PY'
import ipaddress
import sys
from urllib.parse import urlsplit

value = sys.argv[1]
if not value or any(ch.isspace() or ord(ch) < 32 or ord(ch) == 127 for ch in value):
    raise SystemExit(1)
try:
    parsed = urlsplit(value)
    port = parsed.port
except ValueError:
    raise SystemExit(1)
if parsed.scheme not in {"http", "https"} or not parsed.hostname:
    raise SystemExit(1)
if parsed.netloc.endswith(":") or "\\" in parsed.netloc or "%" in parsed.netloc:
    raise SystemExit(1)
if parsed.username is not None or parsed.password is not None:
    raise SystemExit(1)
if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
    raise SystemExit(1)
if port is not None and not 1 <= port <= 65535:
    raise SystemExit(1)
if parsed.scheme == "http":
    host = parsed.hostname
    if host.lower() != "localhost":
        try:
            if not ipaddress.ip_address(host).is_loopback:
                raise SystemExit(1)
        except ValueError:
            raise SystemExit(1)
PY
}

expect_profile=0
for argument in "$@"; do
  if [[ "${expect_profile}" -eq 1 ]]; then
    DEPLOY_PROFILE="${argument}"
    expect_profile=0
    continue
  fi
  [[ "${argument}" != "--profile" ]] || expect_profile=1
done
[[ "${expect_profile}" -eq 0 ]] || fail "--profile requires a value"
[[ -z "${DEPLOY_PROFILE}" ]] || apply_profile "${DEPLOY_PROFILE}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      TARGET="${2:-}"
      shift 2
      ;;
    --dir)
      DEPLOY_DIR="${2:-}"
      shift 2
      ;;
    --goos)
      TARGET_GOOS="${2:-}"
      shift 2
      ;;
    --goarch)
      TARGET_GOARCH="${2:-}"
      shift 2
      ;;
    --stage)
      STAGE_DIR="${2:-}"
      shift 2
      ;;
    --skip-build)
      SKIP_BUILD=1
      shift
      ;;
    --no-start)
      NO_START=1
      shift
      ;;
    --profile)
      shift 2
      ;;
    --package-only)
      PACKAGE_ONLY=1
      NO_START=1
      shift
      ;;
    --archive)
      PACKAGE_ARCHIVE="${2:-}"
      shift 2
      ;;
    --no-storage)
      WITH_STORAGE=0
      shift
      ;;
    --with-storage-shard)
      WITH_STORAGE_SHARD=1
      shift
      ;;
    --no-archive)
      WITH_ARCHIVE=0
      shift
      ;;
    --no-eventbus)
      WITH_EVENTBUS=0
      shift
      ;;
    --no-web-host)
      WITH_WEB_HOST=0
      shift
      ;;
    --no-cloudnode)
      WITH_CLOUDNODE=0
      shift
      ;;
    --no-collector)
      WITH_COLLECTOR=0
      shift
      ;;
    --no-factor)
      WITH_FACTOR=0
      shift
      ;;
    --no-strategy)
      WITH_STRATEGY=0
      shift
      ;;
    --no-monitor)
      WITH_MONITOR=0
      shift
      ;;
    --no-admin)
      WITH_ADMIN=0
      WITH_WEB_HOST=0
      shift
      ;;
    --build-web-assets)
      BUILD_WEB_ASSETS=1
      shift
      ;;
    --reuse-web-assets)
      BUILD_WEB_ASSETS=0
      shift
      ;;
    --reset-data)
      RESET_DATA=1
      shift
      ;;
    --public-host) PUBLIC_HOST="${2:-}"; shift 2 ;;
    --browser-https-port) BROWSER_HTTPS_PORT="${2:-}"; shift 2 ;;
    --service-https-port) SERVICE_HTTPS_PORT="${2:-}"; shift 2 ;;
    --local-ca) LOCAL_CA="${2:-}"; shift 2 ;;
    --local-ca-output) LOCAL_CA_OUTPUT="${2:-}"; shift 2 ;;
    --target-ca) TARGET_CA="${2:-}"; shift 2 ;;
    --caddy-conflict) [[ "${2:-}" == fail ]] || fail 'only --caddy-conflict fail is supported'; shift 2 ;;
    --enable-cls) ENABLE_CLS=1; shift ;;
    --cloud-account-id)
      validate_cloud_account_id_arg "$@"
      CLOUD_ACCOUNT_ID="${2}"
      shift 2
      ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --gateway-control-url) GATEWAY_CONTROL_URL="${2:-}"; shift 2 ;;
    --gateway-ca-bundle) GATEWAY_CA_BUNDLE="${2:-}"; shift 2 ;;
    --gateway-control-key-file) GATEWAY_CONTROL_KEY_FILE="${2:-}"; shift 2 ;;
    --gateway-service-key-file) GATEWAY_SERVICE_KEY_FILE="${2:-}"; shift 2 ;;
    --monitor-instance-id) MONITOR_INSTANCE_ID="${2:-}"; shift 2 ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

# An independent shard needs the Admin control plane to publish its restricted
# route. Keep the minimal storage profile minimal unless this explicit option
# is selected.
if [[ "${DEPLOY_PROFILE}" == "storage" && "${WITH_STORAGE_SHARD}" -eq 1 ]]; then
  WITH_ADMIN=1
fi
if [[ "${WITH_STORAGE_SHARD}" -eq 1 && "${WITH_STORAGE}" -ne 1 ]]; then
  fail "--with-storage-shard requires storage to be enabled"
fi

[[ -n "${TARGET}" ]] || fail "--target cannot be empty"
[[ -n "${DEPLOY_DIR}" ]] || fail "--dir cannot be empty"
if [[ "${PACKAGE_ONLY}" -eq 1 ]]; then
  [[ -n "${PACKAGE_ARCHIVE}" ]] || fail "--archive is required with --package-only"
  PACKAGE_ARCHIVE="$(cd "$(dirname "${PACKAGE_ARCHIVE}")" && pwd)/$(basename "${PACKAGE_ARCHIVE}")"
fi
[[ "${NODE_ID}" =~ ^[a-z0-9][a-z0-9_-]{0,127}$ ]] || fail "--node-id is required and must use lowercase letters, digits, dash, or underscore"
validate_gateway_control_url "${GATEWAY_CONTROL_URL}" || fail "--gateway-control-url must be HTTPS, or loopback HTTP, without credentials, path, query, fragment, or whitespace"
if [[ "${WITH_MONITOR}" -eq 1 ]]; then
  [[ "${MONITOR_INSTANCE_ID}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || \
    fail "--monitor-instance-id is required when Monitor is enabled and must be a stable identifier"
fi
[[ "${LOCAL_CA}" =~ ^(auto|install|skip)$ ]] || fail '--local-ca must be auto, install, or skip'
[[ "${TARGET_CA}" =~ ^(auto|skip)$ ]] || fail '--target-ca must be auto or skip'

is_local_target() {
  [[ "${TARGET}" == "localhost" || "${TARGET}" == "127.0.0.1" || "${TARGET}" == "::1" ]]
}

if [[ "${WITH_WEB_HOST}" -eq 1 && -z "${PUBLIC_HOST}" ]] && ! is_local_target; then
  fail "--public-host is required when deploying web-host to a remote target"
fi

normalize_os() {
  local raw
  raw="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "${raw}" in
    linux) echo "linux" ;;
    darwin|macos) echo "darwin" ;;
    *) fail "unsupported target os: ${raw}" ;;
  esac
}

normalize_arch() {
  local raw
  raw="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "${raw}" in
    amd64|x86_64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) fail "unsupported target arch: ${raw}" ;;
  esac
}

detect_os() {
  if is_local_target; then
    normalize_os "$(uname -s)"
    return
  fi
  normalize_os "$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" 'uname -s')"
}

detect_arch() {
  if is_local_target; then
    normalize_arch "$(uname -m)"
    return
  fi
  normalize_arch "$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" 'uname -m')"
}

expand_local_path() {
  local path="$1"
  case "${path}" in
    "~") echo "${HOME}" ;;
    "~/"*) echo "${HOME}/${path#~/}" ;;
    /*) echo "${path}" ;;
    *) echo "${PWD}/${path}" ;;
  esac
}

shell_quote() {
  local value="$1"
  printf "'%s'" "$(printf '%s' "${value}" | sed "s/'/'\\\\''/g")"
}

default_local_ca_output() {
  local host_id
  host_id="$(printf '%s' "${PUBLIC_HOST}" | tr ':/' '__' | tr -c 'A-Za-z0-9._-' '_')"
  printf '%s/.moox/certs/moox-caddy-root-%s.crt\n' "${HOME}" "${host_id}"
}

local_file_mode() {
  local mode
  if mode=$(stat -f '%Lp' "$1" 2>/dev/null); then
    printf '%s\n' "${mode}"
    return
  fi
  stat -c '%a' "$1"
}

TARGET_GOOS="${TARGET_GOOS:-$(detect_os)}"
TARGET_GOARCH="${TARGET_GOARCH:-$(detect_arch)}"
TARGET_GOOS="$(normalize_os "${TARGET_GOOS}")"
TARGET_GOARCH="$(normalize_arch "${TARGET_GOARCH}")"

require_gateway_input_file() {
  local option="$1" path="$2" require_mode="${3:-1}"
  [[ -n "${path}" ]] || fail "${option} is required"
  path="$(expand_local_path "${path}")"
  [[ -f "${path}" && ! -L "${path}" ]] || fail "${option} must name a local regular file"
  if [[ "${require_mode}" == 1 && "$(local_file_mode "${path}")" != 600 ]]; then
    fail "${option} must have mode 0600"
  fi
}
if [[ "${PACKAGE_ONLY}" -eq 1 && -z "${GATEWAY_CONTROL_KEY_FILE}" && -z "${GATEWAY_SERVICE_KEY_FILE}" && -z "${GATEWAY_CA_BUNDLE}" ]]; then
  AUTO_GATEWAY_INPUTS=1
else
  require_gateway_input_file --gateway-control-key-file "${GATEWAY_CONTROL_KEY_FILE}"
  require_gateway_input_file --gateway-service-key-file "${GATEWAY_SERVICE_KEY_FILE}"
  require_gateway_input_file --gateway-ca-bundle "${GATEWAY_CA_BUNDLE}" 0
  GATEWAY_CONTROL_KEY_FILE="$(expand_local_path "${GATEWAY_CONTROL_KEY_FILE}")"
  GATEWAY_SERVICE_KEY_FILE="$(expand_local_path "${GATEWAY_SERVICE_KEY_FILE}")"
  GATEWAY_CA_BUNDLE="$(expand_local_path "${GATEWAY_CA_BUNDLE}")"
fi

validate_gateway_ca_bundle() {
  local bundle="$1" tmp count cert fingerprint distinct
  if grep -Eq -- '-----BEGIN ([^-]* )?PRIVATE KEY-----|-----END ([^-]* )?PRIVATE KEY-----' "${bundle}"; then
    fail "--gateway-ca-bundle must never contain private-key blocks"
  fi
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/moox-gateway-ca.XXXXXX")
  if ! awk -v dir="${tmp}" '
    /^-----BEGIN CERTIFICATE-----$/ {
      if (inside) exit 2
      inside=1; count++; file=sprintf("%s/cert.%06d.pem", dir, count); print > file; next
    }
    /^-----END CERTIFICATE-----$/ {
      if (!inside) exit 2
      print > file; close(file); inside=0; next
    }
    inside { print > file; next }
    /[^[:space:]]/ { exit 2 }
    END {
      if (inside || count < 2) exit 2
      print count > (dir "/count")
    }
  ' "${bundle}"; then
    rm -rf "${tmp}"
    fail "--gateway-ca-bundle must contain only complete PEM certificate blocks"
  fi
  count=$(cat "${tmp}/count")
  : >"${tmp}/fingerprints"
  for cert in "${tmp}"/cert.*.pem; do
    if ! fingerprint=$(openssl x509 -in "${cert}" -noout -fingerprint -sha256 2>/dev/null); then
      rm -rf "${tmp}"
      fail "--gateway-ca-bundle contains a malformed certificate"
    fi
    fingerprint=${fingerprint#*=}
    [[ -n "${fingerprint}" ]] || { rm -rf "${tmp}"; fail "--gateway-ca-bundle contains a certificate without a SHA-256 fingerprint"; }
    printf '%s\n' "${fingerprint}" >>"${tmp}/fingerprints"
  done
  distinct=$(sort -u "${tmp}/fingerprints" | wc -l | tr -d '[:space:]')
  rm -rf "${tmp}"
  [[ "${count}" -ge 2 && "${distinct}" -ge 2 ]] || \
    fail "--gateway-ca-bundle must contain at least two distinct public CA certificates"
}

HOST_GOOS="$(go env GOOS)"
HOST_GOARCH="$(go env GOARCH)"
STAGE_DIR="${STAGE_DIR:-${ROOT}/release/deploy-stage/moox}"

build_storage_shard_binary() {
  [[ "${WITH_STORAGE_SHARD}" -eq 1 ]] || return 0
  TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
    "${ROOT}/scripts/build.sh" storage-shard
}

build_core_binaries() {
  if [[ "${SKIP_BUILD}" -eq 1 ]]; then
    log "skip core build; reuse ./bin"
    return
  fi

  log "build core binaries (${TARGET_GOOS}/${TARGET_GOARCH})"
  if [[ "${WITH_STORAGE}" -eq 0 ]]; then
    if [[ "${WITH_ADMIN}" -eq 1 || "${WITH_MONITOR}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" cli
    fi
    if [[ "${WITH_ADMIN}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" admin
    fi
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" gateway
    if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" cloudnode
    fi
    if [[ "${WITH_EVENTBUS}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" eventbus
    fi
    if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" collector
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" collector-scf
    fi
    if [[ "${WITH_FACTOR}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" factor
    fi
    if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" strategy
    fi
    if [[ "${WITH_MONITOR}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" monitor
    fi
    if [[ "${WITH_ARCHIVE}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" archive
    fi
    return
  fi

  if [[ "${TARGET_GOOS}" != "${HOST_GOOS}" || "${TARGET_GOARCH}" != "${HOST_GOARCH}" ]]; then
    log "cross build detected; storage requires CGO-enabled DuckDB build"
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" all
    build_storage_shard_binary
    return
  fi

  TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
    "${ROOT}/scripts/build.sh" all
  build_storage_shard_binary
}

build_web_host_binary() {
  [[ "${WITH_WEB_HOST}" -eq 1 ]] || return 0
  if [[ "${SKIP_BUILD}" -eq 1 ]]; then
    log "skip web-host build; reuse existing web-host binary if present"
    return
  fi

  if [[ "${BUILD_WEB_ASSETS}" -eq 1 ]]; then
    log "build web assets and web-host (${TARGET_GOOS}/${TARGET_GOARCH})"
    (
      cd "${ROOT}/web"
      CI=true pnpm install --frozen-lockfile --config.confirmModulesPurge=false
      pnpm run build:prod
    )
    (cd "${ROOT}/web-host" && go run github.com/rakyll/statik@v0.1.7 -src=../web/dist -dest=./internal)
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" web-host
    return
  fi

  log "build web-host with current embedded statik assets (${TARGET_GOOS}/${TARGET_GOARCH})"
  TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
    "${ROOT}/scripts/build.sh" web-host
}

copy_required_binary() {
  local name="$1"
  local src="${ROOT}/bin/${name}"
  [[ -x "${src}" ]] || fail "missing executable ${src}; run without --skip-build first"
  cp "${src}" "${STAGE_DIR}/bin/${name}"
}

copy_optional_web_host() {
  [[ "${WITH_WEB_HOST}" -eq 1 ]] || return 0

  local candidates=(
    "${ROOT}/bin/moox-web-host"
    "${ROOT}/web-host/bin/moox-web-host"
  )
  local candidate
  for candidate in "${candidates[@]}"; do
    if [[ -x "${candidate}" ]]; then
      cp "${candidate}" "${STAGE_DIR}/bin/moox-web-host"
      return
    fi
  done

  fail "missing moox-web-host binary; use --no-web-host or build it without --skip-build"
}

patch_configs() {
	[[ -f "${STAGE_DIR}/gateway/config/trpc_go.yaml" ]] || fail "missing Gateway tRPC Timer config"
	if [[ "${WITH_ADMIN}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/admin\.db#path: ../data/admin.db#g' \
      "${STAGE_DIR}/admin/config/app.yaml"
    perl -0pi -e 's#data_dir:\s*"\./data/badger"#data_dir: "../data/badger"#g' \
      "${STAGE_DIR}/admin/config/gateway.yaml"
    perl -0pi -e 's#log_path:\s*\./log#log_path: ../logs/admin#g' \
      "${STAGE_DIR}/admin/config/trpc_go.yaml"
  fi

  local gateway_control_url_yaml
  gateway_control_url_yaml=$(python3 -c 'import json, sys; print(json.dumps(sys.argv[1]))' "${GATEWAY_CONTROL_URL}")
  GATEWAY_CONTROL_URL_YAML="${gateway_control_url_yaml}" perl -0pi -e 's#id:\s*gateway-gz-122#id: '"${NODE_ID}"'#; s#base_url:\s*https://admin\.example\.com#base_url: $ENV{GATEWAY_CONTROL_URL_YAML}#; s#hmac_key_file:\s*\./secrets/gateway-control\.key#hmac_key_file: ../../secrets/gateway-control.key#; s#hmac_key_file:\s*\./secrets/gateway-service\.key#hmac_key_file: ../../secrets/gateway-service.key#; s#path:\s*\./data/gateway#path: ../../data/gateway#' \
    "${STAGE_DIR}/gateway/config/app.yaml"
  if grep -q '^  ca_file:' "${STAGE_DIR}/gateway/config/app.yaml"; then
    perl -0pi -e 's#^  ca_file:.*#  ca_file: ../../certs/gateway/peers.pem#m' "${STAGE_DIR}/gateway/config/app.yaml"
  else
    perl -0pi -e 's#(  hmac_key_file: ../../secrets/gateway-control\.key\n)#$1  ca_file: ../../certs/gateway/peers.pem\n#' "${STAGE_DIR}/gateway/config/app.yaml"
  fi

  if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/moox_cloudnode\.db#path: ../data/cloudnode/moox_cloudnode.db#g' \
      "${STAGE_DIR}/cloudnode/config/app.yaml"
    local cloudnode_eventbus_url="${EVENTBUS_URL_ENV:-nats://127.0.0.1:4222}"
    perl -0pi -e 's#nats://127\.0\.0\.1:4322#'"${cloudnode_eventbus_url}"'#g' \
      "${STAGE_DIR}/cloudnode/config/app.yaml"
  fi
  if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/moox_collector\.db#path: ../data/collector/moox_collector.db#g' \
      "${STAGE_DIR}/collector/config/app.yaml"
    # Local collector config disables the timer for dev runs; deployments need it on.
    perl -0pi -e 's#scheduler=collectorSchedule&disable=1&params=[^"]*#scheduler=collectorSchedule&disable=0&params=space_id=crypto#g; s#scheduler=collectorSchedule&disable=0&params=(?=")#scheduler=collectorSchedule&disable=0&params=space_id=crypto#g' \
      "${STAGE_DIR}/collector/config/trpc_go.yaml"
  fi
  if [[ "${WITH_FACTOR}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/factor/factor\.db#path: ../data/factor/factor.db#g' \
      "${STAGE_DIR}/factor/config/app.yaml"
  fi
  if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
    perl -0pi -e 's#database:\s*\./data/strategy\.sqlite#database: ../data/strategy/strategy.sqlite#g; s#python_bin:\s*python3#python_bin: ../data/strategy/venv/bin/python#g' \
      "${STAGE_DIR}/strategy/config/app.yaml"
  fi
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/monitor/monitor\.db#path: ../data/monitor/monitor.db#g' \
      "${STAGE_DIR}/monitor/config/app.yaml"
    if [[ "${WITH_STORAGE}" -eq 0 && "${WITH_EVENTBUS}" -eq 0 ]]; then
      perl -0pi -e 's#(metrics:\n  enabled:) true#$1 false#' \
        "${STAGE_DIR}/monitor/config/app.yaml"
    fi
    MONITOR_INSTANCE_ID_VALUE="${MONITOR_INSTANCE_ID}" python3 - "${STAGE_DIR}/monitor/config/app.yaml" <<'PY'
import json
import os
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text()
instance_line = "  instance_id: " + json.dumps(os.environ["MONITOR_INSTANCE_ID_VALUE"])
if text.count('  instance_id: ""') != 1:
    raise SystemExit("Monitor config template does not have the expected instance field")
text = text.replace('  instance_id: ""', instance_line, 1)
path.write_text(text)
PY
  fi
  if [[ "${WITH_EVENTBUS}" -eq 1 ]]; then
    perl -0pi -e 's#store_dir:\s*\./data/eventbus/jetstream#store_dir: ../data/eventbus/jetstream#g' \
      "${STAGE_DIR}/eventbus/config/app.yaml"
  fi

  if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
    [[ "${WITH_CLOUDNODE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/cloudnode-eventbus.yaml#' "${STAGE_DIR}/cloudnode/config/app.yaml"
    [[ "${WITH_FACTOR}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/factor-eventbus.yaml#' "${STAGE_DIR}/factor/config/app.yaml"
    [[ "${WITH_STRATEGY}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/strategy-eventbus.yaml#' "${STAGE_DIR}/strategy/config/app.yaml"
    [[ "${WITH_MONITOR}" -eq 1 ]] && perl -0pi -e 's#eventbus_credential_file:\s*.*#eventbus_credential_file: ~/.config/moox/eventbus/monitor-metrics-consumer.yaml#' "${STAGE_DIR}/monitor/config/app.yaml"
  else
    [[ "${WITH_CLOUDNODE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/cloudnode/config/app.yaml"
    [[ "${WITH_FACTOR}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/factor/config/app.yaml"
    [[ "${WITH_STRATEGY}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/strategy/config/app.yaml"
    [[ "${WITH_MONITOR}" -eq 1 ]] && perl -0pi -e 's#eventbus_credential_file:\s*.*#eventbus_credential_file: ""#' "${STAGE_DIR}/monitor/config/app.yaml"
  fi

  [[ "${WITH_STORAGE}" -eq 1 ]] || return 0
  for conf in "${STAGE_DIR}"/storage/config/storage*.yaml; do
    perl -0pi -e 's#root:\s*\./var/storage#root: ../data/storage#g; s#path:\s*\./var/storage/metadata/storage_metadata\.db#path: ../data/storage/metadata/storage_metadata.db#g; s#pebble_path:\s*\./var/storage/pebble#pebble_path: ../data/storage/pebble#g; s#view_index_root:\s*\./var/storage/view-indexes#view_index_root: ../data/storage/view-indexes#g; s#parquet_path:\s*\./var/storage/archive#parquet_path: ../data/storage/archive#g' \
      "${conf}"
    if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
      perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml#' "${conf}"
    else
      perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${conf}"
    fi
  done
  local view_conf="${STAGE_DIR}/storage-view/config/trpc_go.yaml"
  perl -0pi -e 's#root:\s*\./var/storage#root: ../data/storage#g; s#path:\s*\./var/storage/metadata/storage_metadata\.db#path: ../data/storage/metadata/storage_metadata.db#g; s#pebble_path:\s*\./var/storage/pebble#pebble_path: ../data/storage/pebble#g; s#view_index_root:\s*\./var/storage/view-indexes#view_index_root: ../data/storage/view-indexes#g; s#parquet_path:\s*\./var/storage/archive#parquet_path: ../data/storage/archive#g' \
    "${view_conf}"
  if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
    perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml#' "${view_conf}"
  else
    perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${view_conf}"
  fi
  if [[ "${STORAGE_EXTERNAL_LISTEN}" -eq 1 ]]; then
    for conf in "${STAGE_DIR}/storage/config/trpc_go.yaml" "${STAGE_DIR}"/storage/config/trpc_go.*.yaml "${view_conf}"; do
      [[ -f "${conf}" ]] || continue
      perl -pi -e 'if (/^server:/) { $server = 1 } if (/^(client|plugins):/) { $server = 0 } if ($server && /^      ip:\s*127\.0\.0\.1\s*$/) { s#127\.0\.0\.1#0.0.0.0# }' "${conf}"
    done
    if [[ "${WITH_STORAGE_SHARD}" -eq 1 ]]; then
      perl -pi -e 'if (/^server:/) { $server = 1 } if (/^(client|plugins):/) { $server = 0 } if ($server && /^      ip:\s*127\.0\.0\.1\s*$/) { s#127\.0\.0\.1#0.0.0.0# }' "${STAGE_DIR}/storage-shard/config/trpc_go.yaml"
    fi
  fi
  perl -0pi -e 's#log_path:\s*\./logs#log_path: ../logs/storage-primary#g' \
    "${STAGE_DIR}/storage/config/trpc_go.yaml"
  perl -0pi -e 's#log_path:\s*\./logs#log_path: ../logs/storage-view#g' "${view_conf}"
  if [[ "${WITH_STORAGE_SHARD}" -eq 1 ]]; then
    perl -0pi -e 's#(service_name:\s*)""#${1}trpc.moox.storage.DataShard#g' \
      "${STAGE_DIR}/storage/config/storage.yaml" "${STAGE_DIR}/storage/config/trpc_go.yaml"
    perl -0pi -e 's#root:\s*\./var/storage#root: ../data/storage-shard#g; s#pebble_path:\s*\./var/storage/pebble#pebble_path: ../data/storage-shard/pebble#g; s#log_path:\s*\./logs#log_path: ../logs/storage-shard#g' \
      "${STAGE_DIR}/storage-shard/config/trpc_go.yaml" "${STAGE_DIR}/storage-shard/config/storage.yaml"
    if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
      perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml#' "${STAGE_DIR}/storage-shard/config/storage.yaml"
    else
      perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/storage-shard/config/storage.yaml"
    fi
  fi
}

write_runtime_scripts() {
  cat > "${STAGE_DIR}/start.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HEALTH_AUTH_FILE="${ROOT}/secrets/health-auth.env"
[[ -r "${HEALTH_AUTH_FILE}" ]] || { echo "missing health credentials: ${HEALTH_AUTH_FILE}" >&2; exit 1; }
[[ -r "${ROOT}/secrets/gateway-control.env" ]] || { echo "missing Gateway control credentials" >&2; exit 1; }
[[ -r "${ROOT}/secrets/gateway-service.env" ]] || { echo "missing Gateway service credentials" >&2; exit 1; }
set -a
source "${ROOT}/secrets/health-auth.env"
if [[ -r "${ROOT}/secrets/cls.env" ]]; then
  source "${ROOT}/secrets/cls.env"
fi
if [[ -r "${ROOT}/secrets/otel.env" ]]; then
  source "${ROOT}/secrets/otel.env"
fi
if [[ -r "${ROOT}/certs/caddy/root.crt" ]]; then
  MOOX_SERVICE_GATEWAY_CA_FILE="${ROOT}/certs/caddy/root.crt"
  MOOX_SERVICE_GATEWAY_CA_PEM_B64=$(base64 <"${ROOT}/certs/caddy/root.crt" | tr -d '\r\n')
fi
MOOX_GATEWAY_CA_FILE="${ROOT}/certs/gateway/peers.pem"
export MOOX_SERVICE_GATEWAY_CA_FILE MOOX_SERVICE_GATEWAY_CA_PEM_B64 MOOX_GATEWAY_CA_FILE
set +a

read_env_value() {
  local file="$1" name="$2" value
  value=$(bash -c 'set -u; source "$1"; printf "%s" "${!2-}"' _ "${file}" "${name}")
  [[ -n "${value}" ]] || { echo "missing ${name} in ${file}" >&2; exit 1; }
  printf '%s' "${value}"
}

GATEWAY_CONTROL_ENV=(
  "MOOX_GATEWAY_CONTROL_KEY_ID=$(read_env_value "${ROOT}/secrets/gateway-control.env" MOOX_GATEWAY_CONTROL_KEY_ID)"
  "MOOX_GATEWAY_CONTROL_SECRET_KEY=$(read_env_value "${ROOT}/secrets/gateway-control.env" MOOX_GATEWAY_CONTROL_SECRET_KEY)"
)
GATEWAY_SERVICE_ENV=(
  "MOOX_GATEWAY_SERVICE_KEY_ID=$(read_env_value "${ROOT}/secrets/gateway-service.env" MOOX_GATEWAY_SERVICE_KEY_ID)"
  "MOOX_GATEWAY_SERVICE_SECRET_KEY=$(read_env_value "${ROOT}/secrets/gateway-service.env" MOOX_GATEWAY_SERVICE_SECRET_KEY)"
  "MOOX_GATEWAY_CA_FILE=${MOOX_GATEWAY_CA_FILE}"
  "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID:-__NODE_ID__}"
  "MOOX_SERVICE_GATEWAY_TARGET=ip://127.0.0.1:11003"
)
gateway_service_env_for() {
  local caller="$1" secret_file secret key_id
  if [[ "${caller}" == "admin-gateway" ]]; then
    secret_file="${ROOT}/secrets/gateway-service.key"
    key_id="moox-gateway-service"
  else
    secret_file="${ROOT}/secrets/gateway-${caller}.key"
    key_id="${caller}"
  fi
  [[ -r "${secret_file}" ]] || { echo "missing Gateway credential for caller ${caller}: ${secret_file}" >&2; exit 1; }
  secret=$(tr -d '\r\n' <"${secret_file}")
  CALLER_GATEWAY_SERVICE_ENV=(
    "${GATEWAY_SERVICE_ENV[@]}"
    "MOOX_GATEWAY_SERVICE_KEY_ID=${key_id}"
    "MOOX_GATEWAY_SERVICE_SECRET_KEY=${secret}"
    "MOOX_GATEWAY_CALLER=${caller}"
  )
}
ADMIN_SECRET_ENV=("${GATEWAY_CONTROL_ENV[@]}")
if [[ -r "${ROOT}/secrets/admin-jwt.env" ]]; then
  ADMIN_SECRET_ENV+=("MOOX_ADMIN_JWT_SECRET_KEY=$(read_env_value "${ROOT}/secrets/admin-jwt.env" MOOX_ADMIN_JWT_SECRET_KEY)")
fi
WITH_STORAGE="${MOOX_WITH_STORAGE:-__WITH_STORAGE__}"
WITH_STORAGE_SHARD="${MOOX_WITH_STORAGE_SHARD:-__WITH_STORAGE_SHARD__}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-__WITH_ARCHIVE__}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-__WITH_EVENTBUS__}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-__WITH_STRATEGY__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-__WITH_ADMIN__}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-__WITH_GATEWAY__}"
if [[ "${WITH_STORAGE_SHARD}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-shard requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" == "1" && ! -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" != "1" && -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard package is present but storage-shard is disabled" >&2
  exit 2
fi
MOOX_GATEWAY_NODE_ID="${MOOX_GATEWAY_NODE_ID:-__NODE_ID__}"
export MOOX_GATEWAY_NODE_ID
MOOX_MONITOR_INSTANCE_ID="${MOOX_MONITOR_INSTANCE_ID:-__MONITOR_INSTANCE_ID__}"
if [[ "${WITH_ADMIN}" == "1" ]]; then
  MOOX_ADMIN_NODE_ID="${MOOX_ADMIN_NODE_ID:-__NODE_ID__}"
fi
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-3}"
mkdir -p "${ROOT}/run" "${ROOT}/data" "${ROOT}/data/gateway" "${ROOT}/data/eventbus/jetstream" "${ROOT}/data/cloudnode" "${ROOT}/data/cloudnode/jobs" "${ROOT}/data/collector" "${ROOT}/data/factor" "${ROOT}/data/strategy" "${ROOT}/data/monitor" "${ROOT}/logs/admin" "${ROOT}/logs/gateway" "${ROOT}/logs/eventbus" "${ROOT}/logs/storage" "${ROOT}/logs/storage-primary" "${ROOT}/logs/storage-view" "${ROOT}/logs/web-host" "${ROOT}/logs/cloudnode" "${ROOT}/logs/collector" "${ROOT}/logs/factor" "${ROOT}/logs/strategy" "${ROOT}/logs/monitor"
chmod 0700 "${ROOT}/data/gateway"

source "${ROOT}/lib/loopback-listeners.sh"
validate_moox_loopback_listeners
stop_processes_by_binary() {
  local name="$1" expected="${ROOT}/bin/moox-${name}" proc pid exe
  for proc in /proc/[0-9]*; do
    [[ -d "${proc}" ]] || continue
    pid="${proc##*/}"
    [[ "${pid}" =~ ^[0-9]+$ ]] || continue
    exe="$(readlink "${proc}/exe" 2>/dev/null || true)"
    case "${exe}" in
      "${expected}"|"${expected} (deleted)") ;;
      *) continue ;;
    esac
    echo "stopping stale ${name} process pid=${pid}"
    kill "${pid}" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    kill -9 "${pid}" 2>/dev/null || true
  done
}

process_matches_service() {
  local pid="$1" name="$2" command expected
  expected="${ROOT}/bin/moox-${name}"
  command=$(ps -p "${pid}" -o command= 2>/dev/null || true)
  [[ "${command}" == "${expected}" || "${command}" == "${expected} "* ]]
}
stop_if_running() {
  local name="$1"
  local pid_file="${ROOT}/run/${name}.pid"
  local pattern="${ROOT}/bin/moox-${name}([[:space:]]|$)"
  if [[ ! -f "${pid_file}" ]]; then
    stop_processes_by_binary "${name}"
    pkill -f -- "${pattern}" 2>/dev/null || true
    return
  fi
  local pid
  pid="$(cat "${pid_file}" 2>/dev/null || true)"
  if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1 && process_matches_service "${pid}" "${name}"; then
    echo "stopping existing ${name} pid=${pid}"
    kill "${pid}" 2>/dev/null || true
    sleep 1
  elif [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
    echo "${name}: stale pid ${pid} belongs to another process; removing pid file" >&2
    rm -f "${pid_file}"
    return
  fi
  if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1 && process_matches_service "${pid}" "${name}"; then
    kill -9 "${pid}" 2>/dev/null || true
  fi
  stop_processes_by_binary "${name}"
  pkill -f -- "${pattern}" 2>/dev/null || true
  rm -f "${pid_file}"
}

start_service() {
  local name="$1"
  local work_dir="$2"
  shift 2
  local pid_file="${ROOT}/run/${name}.pid"
  local log_file="${ROOT}/logs/${name}/stdout.log"

  stop_if_running "${name}"
  mkdir -p "$(dirname "${log_file}")"
  echo "starting ${name}"
  (
    cd "${work_dir}"
    nohup "$@" > "${log_file}" 2>&1 &
    echo $! > "${pid_file}"
  )
  sleep "${STARTUP_WAIT_SECONDS}"
  local pid
  pid="$(cat "${pid_file}")"
  if ! ps -p "${pid}" >/dev/null 2>&1; then
    echo "${name} failed to start; see ${log_file}" >&2
    tail -80 "${log_file}" >&2 || true
    exit 1
  fi
  echo "${name} started pid=${pid}"
}

runtime_identity_env() {
  local service_name="$1"
  local config_file="${2:-}"
  local boot_id
  if command -v uuidgen >/dev/null 2>&1; then
    boot_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  else
    boot_id="boot-$(date +%s)-$$-${RANDOM}"
  fi
  RUNTIME_IDENTITY_ENV=(
    "MOOX_INSTANCE_ID=${service_name}@${MOOX_GATEWAY_NODE_ID}"
    "MOOX_NODE_ID=${MOOX_GATEWAY_NODE_ID}"
    "MOOX_BOOT_ID=${boot_id}"
  )
  if [[ -n "${config_file}" && -f "${config_file}" ]]; then
    RUNTIME_IDENTITY_ENV+=("MOOX_CONFIG_HASH=sha256:$(shasum -a 256 "${config_file}" | awk '{print $1}')")
  fi
  if [[ -f "${ROOT}/config/monitor-pipelines.yaml" ]]; then
    RUNTIME_IDENTITY_ENV+=(
      "MOOX_PIPELINE_CONFIG=${ROOT}/config/monitor-pipelines.yaml"
      "MOOX_PIPELINE_CONFIG_HASH=sha256:$(shasum -a 256 "${ROOT}/config/monitor-pipelines.yaml" | awk '{print $1}')"
    )
  fi
  if [[ -f "${HOME}/.config/moox/eventbus/metrics-publisher.yaml" ]]; then
    RUNTIME_IDENTITY_ENV+=("MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE=${HOME}/.config/moox/eventbus/metrics-publisher.yaml")
  fi
}

STORAGE_SCHEMA_ENV=(
  "STORAGE_CONFIG_PATH=${ROOT}/storage/config"
  "MOOX_STORAGE_CONFIG=${ROOT}/storage/config/storage.yaml"
  "MOOX_STORAGE_HOME=${ROOT}/data/storage"
  "STORAGE_SCHEMA_FILE=${ROOT}/storage/schema/metadata.sql"
)

COLLECTOR_ENV=(
  "MOOX_COLLECTOR_ADMIN_GATEWAY_URL=${MOOX_COLLECTOR_ADMIN_GATEWAY_URL:-http://127.0.0.1:11002}"
)

FACTOR_ENV=(
  "MOOX_FACTOR_ADMIN_GATEWAY_URL=${MOOX_FACTOR_ADMIN_GATEWAY_URL:-http://127.0.0.1:11002}"
  "MOOX_FACTOR_DB_PATH=${MOOX_FACTOR_DB_PATH:-../data/factor/factor.db}"
  "MOOX_FACTOR_NATS_URL=${MOOX_FACTOR_NATS_URL:-nats://127.0.0.1:4222}"
  "MOOX_PYTHON_RUNTIME_PATH=${ROOT}/python-runtime"
)

MONITOR_ENV=(
  "MOOX_MONITOR_INSTANCE_ID=${MOOX_MONITOR_INSTANCE_ID}"
)

METRICS_METADATA_URL="${MOOX_METRICS_STORAGE_METADATA_URL:-http://127.0.0.1:20200}"
METRICS_ROUTE_SEED="${MOOX_METRICS_STORAGE_ROUTE_SEED:-}"
HOST_ROUTE_SEED="${MOOX_HOST_STORAGE_ROUTE_SEED:-}"
EVENTBUS_URL_ENV="${MOOX_EVENTBUS_NATS_URL:-${MOOX_EVENTBUS_URL:-}}"
METRICS_EVENTBUS_URL_ENV="${MOOX_METRICS_EVENTBUS_URL:-}"

ensure_factor_python() {
  local venv="${ROOT}/data/factor/venv"
  local python_bin="${MOOX_FACTOR_ENGINE_PYTHON_BIN:-}"
  if [[ -z "${python_bin}" ]]; then
    if [[ ! -x "${venv}/bin/python" ]]; then
      python3 -m venv "${venv}"
    fi
    python_bin="${venv}/bin/python"
  fi
  if ! "${python_bin}" - <<'PY' >/dev/null 2>&1; then
import numpy  # noqa: F401
import pandas  # noqa: F401
PY
    "${python_bin}" -m pip install --upgrade pip
    "${python_bin}" -m pip install -r "${ROOT}/factor/pyworker/runtime-requirements.txt"
  fi
  FACTOR_ENV+=("MOOX_FACTOR_ENGINE_PYTHON_BIN=${python_bin}")
}

ensure_strategy_python() {
  local venv="${ROOT}/data/strategy/venv"
  local python_bin="${venv}/bin/python"
  if [[ ! -x "${python_bin}" ]]; then
    python3 -m venv "${venv}"
  fi
  if ! "${python_bin}" - <<'PY' >/dev/null 2>&1; then
import numpy  # noqa: F401
import pandas  # noqa: F401
PY
    "${python_bin}" -m pip install --upgrade pip
    "${python_bin}" -m pip install -r "${ROOT}/strategy/pyworker/runtime-requirements.txt"
  fi
}

nats_endpoint() {
  local url="$1"
  url="${url#nats://}"
  url="${url%%,*}"
  url="${url%%/*}"
  local host="${url%%:*}"
  local port="${url##*:}"
  if [[ "${host}" == "${port}" ]]; then
    port="4222"
  fi
  printf '%s %s\n' "${host}" "${port}"
}

wait_nats() {
  local label="$1" url="$2" attempts="$3" host port
  read -r host port < <(nats_endpoint "${url}")
  echo "waiting for ${label} NATS ${host}:${port}"
  for _ in $(seq 1 "${attempts}"); do
    if bash -c ":</dev/tcp/${host}/${port}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "${label} NATS ${host}:${port} not ready after ${attempts}s" >&2
  return 1
}

wait_factor_nats() {
  wait_nats factor "${MOOX_FACTOR_NATS_URL:-nats://127.0.0.1:4222}" "${MOOX_WAIT_FACTOR_NATS_SECONDS:-60}"
}

wait_tcp() {
  local host="$1"
  local port="$2"
  local attempts="${3:-30}"
  echo "waiting for ${host}:${port}"
  for _ in $(seq 1 "${attempts}"); do
    if bash -c ":</dev/tcp/${host}/${port}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "${host}:${port} not ready after ${attempts}s" >&2
  return 1
}

wait_http() {
  local url="$1"
  local attempts="${2:-30}"
  echo "waiting for ${url}"
  for _ in $(seq 1 "${attempts}"); do
    if curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $(sign_health_request GET "${url#*://*/}")" "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "${url} not ready after ${attempts}s" >&2
  return 1
}

sign_health_request() {
  local method="$1" path="$2" timestamp nonce body_hash canonical signature
  path="/${path#/}"
  timestamp=$(date +%s)
  nonce=$(openssl rand -hex 32)
  body_hash=$(printf '' | openssl dgst -sha256 | awk '{print $NF}')
  canonical=$(printf 'moox-request-v1\n%s\n%s\n%s\n%s\n%s' "${method}" "${path}" "${body_hash}" "${timestamp}" "${nonce}")
  signature=$(printf '%s' "${canonical}" | openssl dgst -sha256 -hmac "${MOOX_HEALTH_AUTH_SECRET_KEY}" | awk '{print $NF}')
  printf '%s/%s/%s/%s/%s' "${MOOX_HEALTH_AUTH_VERSION}" "${MOOX_HEALTH_AUTH_ACCESS_KEY}" "${timestamp}" "${nonce}" "${signature}"
}

probe_service() {
  local name="$1" url=""
  case "${name}" in
    admin) url=http://127.0.0.1:11010/readyz ;;
    gateway) url=http://127.0.0.1:11012/readyz ;;
    archive) url=http://127.0.0.1:11416/readyz ;;
    cloudnode) url=http://127.0.0.1:11411/readyz ;;
    collector) url=http://127.0.0.1:11412/readyz ;;
    eventbus) url=http://127.0.0.1:11419/readyz ;;
    factor) url=http://127.0.0.1:11414/readyz ;;
    strategy) url=http://127.0.0.1:11431/readyz ;;
    monitor) url=http://127.0.0.1:11409/readyz ;;
    web-host) url=http://127.0.0.1:19527/readyz ;;
    storage-primary) url=http://127.0.0.1:20210/readyz ;;
    storage-view) url=http://127.0.0.1:20211/readyz ;;
    storage-shard) url=http://127.0.0.1:20212/readyz ;;
    *) echo "unknown service health mapping: ${name}" >&2; return 1 ;;
  esac
  curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $(sign_health_request GET /readyz)" "${url}" >/dev/null
}

wait_http_reachable() {
  local url="$1"
  local attempts="${2:-30}"
  echo "waiting for metadata HTTP ${url}"
  for _ in $(seq 1 "${attempts}"); do
    # MetadataService does not expose a generic health route. Any HTTP
    # response proves that the listener is ready; the apply command below is
    # the read/write contract check.
    if [[ "$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 2 "${url}" 2>/dev/null || true)" != "000" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "metadata HTTP ${url} not reachable after ${attempts}s" >&2
  return 1
}

apply_metrics_metadata() {
  if [[ "${WITH_MONITOR}" != "1" ]]; then
    return 0
  fi
  if [[ "${WITH_STORAGE}" != "1" && "${WITH_EVENTBUS}" != "1" ]]; then
    echo "skip metrics metadata for Monitor deployment without local metrics dependencies"
    return 0
  fi
  local route_seed="${METRICS_ROUTE_SEED}"
  if [[ -z "${route_seed}" && "${WITH_STORAGE}" == "1" ]]; then
    route_seed="${ROOT}/examples/metadata-monitor-metrics-local-route.seed.yaml"
  fi
  if [[ -z "${route_seed}" ]]; then
    echo "MOOX_METRICS_STORAGE_ROUTE_SEED is required when Monitor uses external or clustered Storage" >&2
    return 1
  fi
  if [[ ! -f "${route_seed}" ]]; then
    echo "metrics route seed not found: ${route_seed}" >&2
    return 1
  fi
  wait_http_reachable "${METRICS_METADATA_URL}" "${MOOX_WAIT_STORAGE_METADATA_SECONDS:-60}"
  if [[ "${WITH_STORAGE}" == "1" ]]; then
    "${ROOT}/bin/moox-cli" metadata apply --file "${ROOT}/examples/platform-local.seed.yaml" --metadata-url "${METRICS_METADATA_URL}"
  fi
  "${ROOT}/bin/moox-cli" metadata apply --file "${ROOT}/examples/metadata-monitor-metrics.seed.yaml" --metadata-url "${METRICS_METADATA_URL}"
  "${ROOT}/bin/moox-cli" metadata apply --file "${route_seed}" --metadata-url "${METRICS_METADATA_URL}"
}

apply_host_metadata() {
  if [[ "${WITH_MONITOR}" != "1" ]]; then
    return 0
  fi
  if [[ "${WITH_STORAGE}" != "1" && "${WITH_EVENTBUS}" != "1" ]]; then
    echo "skip host metadata for Monitor deployment without local metrics dependencies"
    return 0
  fi
  local route_seed="${HOST_ROUTE_SEED}"
  if [[ -z "${route_seed}" && "${WITH_STORAGE}" == "1" ]]; then
    route_seed="${ROOT}/examples/metadata-monitor-host-local-route.seed.yaml"
  fi
  if [[ -z "${route_seed}" ]]; then
    echo "MOOX_HOST_STORAGE_ROUTE_SEED is required when Monitor uses external or clustered Storage" >&2
    return 1
  fi
  if [[ ! -f "${route_seed}" ]]; then
    echo "host metrics route seed not found: ${route_seed}" >&2
    return 1
  fi
  wait_http_reachable "${METRICS_METADATA_URL}" "${MOOX_WAIT_STORAGE_METADATA_SECONDS:-60}"
  "${ROOT}/bin/moox-cli" metadata apply --file "${ROOT}/examples/metadata-monitor-host.seed.yaml" --metadata-url "${METRICS_METADATA_URL}"
  "${ROOT}/bin/moox-cli" metadata apply --file "${route_seed}" --metadata-url "${METRICS_METADATA_URL}"
}

init_storage_schema() {
  echo "initializing storage metadata schema"
  mkdir -p "${ROOT}/logs/storage"
  (
    cd "${ROOT}/storage"
    env "${STORAGE_SCHEMA_ENV[@]}" "${ROOT}/bin/moox-storage-cli" init \
      --storage-conf=config/storage.yaml \
      --schema-path=schema/metadata.sql >> "${ROOT}/logs/storage/stdout.log" 2>&1
  )
}

init_admin_schema() {
  echo "initializing admin schema"
  mkdir -p "${ROOT}/logs/admin"
  (
    cd "${ROOT}/admin"
    "${ROOT}/bin/moox-admin-cli" init --db-path ../data/admin.db >> "${ROOT}/logs/admin/stdout.log" 2>&1
  )
}

init_cloudnode_schema() {
  echo "initializing cloudnode schema"
  mkdir -p "${ROOT}/logs/cloudnode"
  (
    cd "${ROOT}/cloudnode"
    "${ROOT}/bin/moox-cloudnode-cli" init --db-path ../data/cloudnode/moox_cloudnode.db >> "${ROOT}/logs/cloudnode/stdout.log" 2>&1
  )
}

init_collector_schema() {
  echo "initializing collector schema"
  mkdir -p "${ROOT}/logs/collector"
  (
    cd "${ROOT}/collector"
    "${ROOT}/bin/moox-collector-cli" init --db-path ../data/collector/moox_collector.db >> "${ROOT}/logs/collector/stdout.log" 2>&1
  )
}

init_monitor_schema() {
  echo "initializing monitor schema"
  mkdir -p "${ROOT}/logs/monitor"
  (
    cd "${ROOT}/monitor"
    "${ROOT}/bin/moox-monitor-cli" init --db-path ../data/monitor/monitor.db >> "${ROOT}/logs/monitor/stdout.log" 2>&1
  )
}

start_storage_process() {
	local name="$1"
	local binary="$2"
	local trpc_conf="$3"
	local storage_conf="$4"
	gateway_service_env_for "${name}"
	start_service "${name}" "${ROOT}/storage" \
    env \
      "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_OTEL_SERVICE_NAME=moox-${name}" \
      "STORAGE_CONFIG_PATH=${ROOT}/storage/config" \
      "MOOX_STORAGE_CONFIG=${ROOT}/storage/config/${storage_conf}" \
      "MOOX_STORAGE_HOME=${ROOT}/data/storage" \
      "STORAGE_SCHEMA_FILE=${ROOT}/storage/schema/metadata.sql" \
      "${ROOT}/bin/${binary}" \
      -conf="config/${trpc_conf}"
}

start_eventbus() {
  if [[ "${WITH_EVENTBUS}" != "1" ]]; then
    echo "eventbus is disabled in this deployment package" >&2
    exit 2
  fi
  local credential_dir="${HOME}/.config/moox/eventbus"
  if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" && -f "${credential_dir}/users.yaml" ]]; then
    perl -0pi -e 's#enabled:\s*false\n    username:#enabled: true\n    username:#; s#users_file:\s*""#users_file: "'"${credential_dir}"'/users.yaml"#; s#enabled:\s*false\n    cert_file:#enabled: true\n    cert_file:#; s#cert_file:\s*""#cert_file: "'"${credential_dir}"'/server.pem"#; s#key_file:\s*""#key_file: "'"${credential_dir}"'/server-key.pem"#; s#ca_file:\s*""#ca_file: "'"${credential_dir}"'/ca.pem"#' \
      "${ROOT}/eventbus/config/app.yaml"
    perl -0pi -e 's#credential_file:\s*""#credential_file: "'"${credential_dir}"'/internal-admin.yaml"#; s#tls_ca_file:\s*""#tls_ca_file: "'"${credential_dir}"'/ca.pem"#' \
      "${ROOT}/eventbus/config/app.yaml"
  fi
  runtime_identity_env eventbus "${ROOT}/eventbus/config/app.yaml"
  start_service "eventbus" "${ROOT}/eventbus" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${ROOT}/bin/moox-eventbus" -conf=config/trpc_go.yaml
  wait_http http://127.0.0.1:11419/readyz "${MOOX_WAIT_EVENTBUS_SECONDS:-60}"
}

start_archive() {
  if [[ "${WITH_ARCHIVE}" != "1" ]]; then
    echo "archive is disabled in this deployment package" >&2
    exit 2
  fi
  gateway_service_env_for archive
  runtime_identity_env moox_archive "${ROOT}/archive/config/app.yaml"
  start_service "archive" "${ROOT}/archive" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_EVENTBUS_NATS_URL=${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" \
      "${ROOT}/bin/moox-archive" -config=config/app.yaml -conf=config/trpc_go.yaml
}

start_storage_primary() {
	start_storage_process "storage-primary" "moox-storage-primary" "trpc_go.yaml" "storage.yaml"
}

start_storage_view() {
  gateway_service_env_for storage-view
  start_service "storage-view" "${ROOT}/storage-view" \
    env "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_OTEL_SERVICE_NAME=moox-storage-view" \
      "MOOX_GATEWAY_CALLER=storage-view" "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_SERVICE_GATEWAY_TARGET=ip://127.0.0.1:11003" \
      "MOOX_STORAGE_CONFIG=${ROOT}/storage-view/config/trpc_go.yaml" \
      "MOOX_STORAGE_HOME=${ROOT}/data/storage" \
      "${ROOT}/bin/moox-storage-view" \
      -conf=config/trpc_go.yaml
}

start_storage_shard() {
  if [[ "${WITH_STORAGE_SHARD}" != "1" ]]; then
    echo "storage-shard is disabled in this deployment package" >&2
    exit 2
  fi
  gateway_service_env_for storage-primary
  start_service "storage-shard" "${ROOT}/storage-shard" \
    env \
      "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_SERVICE_GATEWAY_TARGET=ip://127.0.0.1:11003" \
      "MOOX_OTEL_SERVICE_NAME=moox-storage-shard" \
      "MOOX_STORAGE_CONFIG=${ROOT}/storage-shard/config/storage.yaml" \
      "MOOX_STORAGE_HOME=${ROOT}/data/storage-shard" \
      "${ROOT}/bin/moox-storage-shard" \
      -conf=config/trpc_go.yaml
}

start_storage() {
  start_storage_primary
  if [[ "${WITH_STORAGE_SHARD}" == "1" ]]; then
    start_storage_shard
    wait_tcp 127.0.0.1 20107 "${MOOX_WAIT_STORAGE_SHARD_SECONDS:-30}"
    wait_http http://127.0.0.1:20212/healthz "${MOOX_WAIT_STORAGE_SHARD_SECONDS:-30}"
  fi
  wait_tcp 127.0.0.1 20201 "${MOOX_WAIT_STORAGE_ACCESS_SECONDS:-30}"
  wait_tcp 127.0.0.1 4222 "${MOOX_WAIT_STORAGE_NATS_SECONDS:-30}"
  wait_http http://127.0.0.1:20210/healthz "${MOOX_WAIT_STORAGE_ACCESS_SECONDS:-30}"
  start_storage_view
  wait_tcp 127.0.0.1 20104 "${MOOX_WAIT_STORAGE_VIEW_SECONDS:-30}"
  wait_tcp 127.0.0.1 20202 "${MOOX_WAIT_STORAGE_VIEW_SECONDS:-30}"
  wait_http http://127.0.0.1:20211/healthz "${MOOX_WAIT_STORAGE_VIEW_SECONDS:-30}"
}

start_admin() {
  [[ "${WITH_ADMIN}" == "1" ]] || { echo "admin is disabled in this deployment package" >&2; exit 2; }
  local encryption_key_file="${HOME}/.config/moox/credentials/admin-encryption-key"
  [[ -f "${encryption_key_file}" ]] || { echo "missing Admin encryption key: ${encryption_key_file}" >&2; exit 1; }
  init_admin_schema
  if [[ -x "${ROOT}/bin/moox-admin-cli" && -f "${ROOT}/examples/service-deployments.seed.yaml" ]]; then
    local service_seed_args=(service-deployments import
      --db-path "${ROOT}/data/admin.db" \
      --file "${ROOT}/examples/service-deployments.seed.yaml" \
      --node-id "${MOOX_ADMIN_NODE_ID}")
    if [[ "${WITH_STORAGE_SHARD}" == "1" ]]; then
      service_seed_args+=(--with-storage-shard)
    else
      service_seed_args+=(--disable-storage-shard)
    fi
    "${ROOT}/bin/moox-admin-cli" "${service_seed_args[@]}" >>"${ROOT}/logs/admin/stdout.log" 2>&1 || {
        echo "Storage shard service deployment import failed" >&2
        exit 1
    }
  fi
  if [[ "${WITH_EVENTBUS}" == "1" && -x "${ROOT}/bin/moox-admin-cli" ]]; then
    mkdir -p "${HOME}/.config/moox/eventbus"
    "${ROOT}/bin/moox-admin-cli" eventbus-credentials ensure --db-path "${ROOT}/data/admin.db" --encryption-key-file "${encryption_key_file}" --public-ip "${MOOX_EVENTBUS_PUBLIC_IP:-}" >> "${ROOT}/logs/admin/stdout.log" 2>&1 || { echo "EventBus credential provisioning failed" >&2; exit 1; }
    "${ROOT}/bin/moox-admin-cli" eventbus-credentials export --db-path "${ROOT}/data/admin.db" --encryption-key-file "${encryption_key_file}" --public-ip "${MOOX_EVENTBUS_PUBLIC_IP:-}" --output-dir "${HOME}/.config/moox/eventbus" >> "${ROOT}/logs/admin/stdout.log" 2>&1 || { echo "EventBus credential export failed" >&2; exit 1; }
  fi
  gateway_service_env_for admin-gateway
  runtime_identity_env admin_gateway "${ROOT}/admin/config/trpc_go.yaml"
  start_service "admin" "${ROOT}/admin" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${ADMIN_SECRET_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_NODE_GATEWAY_URL=http://127.0.0.1:11002" "MOOX_NODE_GATEWAY_NATIVE_URL=127.0.0.1:11003" "MOOX_NODE_GATEWAY_NODE_ID=${MOOX_ADMIN_NODE_ID}" \
      "MOOX_ADMIN_NODE_ID=${MOOX_ADMIN_NODE_ID}" "MOOX_ADMIN_ENCRYPTION_KEY_FILE=${encryption_key_file}" "MOOX_OTEL_SERVICE_NAME=moox-admin" \
      "${ROOT}/bin/moox-admin" -conf=config/trpc_go.yaml
}

start_gateway() {
  [[ "${WITH_GATEWAY}" == "1" ]] || { echo "gateway is disabled in this deployment package" >&2; exit 2; }
	runtime_identity_env moox_gateway "${ROOT}/gateway/config/app.yaml"
	start_service "gateway" "${ROOT}/gateway" \
		env "${RUNTIME_IDENTITY_ENV[@]}" "MOOX_GATEWAY_NODE_ID=${MOOX_GATEWAY_NODE_ID}" "MOOX_OTEL_SERVICE_NAME=moox-gateway" \
			"${ROOT}/bin/moox-gateway" -config=config/app.yaml -conf=config/trpc_go.yaml
}

start_cloudnode() {
  if [[ "${WITH_CLOUDNODE}" != "1" ]]; then
    echo "cloudnode is disabled in this deployment package" >&2
    exit 2
  fi
  init_cloudnode_schema
  runtime_identity_env moox_cloudnode "${ROOT}/cloudnode/config/app.yaml"
  start_service "cloudnode" "${ROOT}/cloudnode" \
    env "${RUNTIME_IDENTITY_ENV[@]}" \
      "MOOX_CLOUDNODE_PPROF_ADDR=${MOOX_CLOUDNODE_PPROF_ADDR:-127.0.0.1:16001}" \
      "${ROOT}/bin/moox-cloudnode" -conf=config/trpc_go.yaml
}

start_collector() {
  if [[ "${WITH_COLLECTOR}" != "1" ]]; then
    echo "collector is disabled in this deployment package" >&2
    exit 2
  fi
  init_collector_schema
  gateway_service_env_for collector
  runtime_identity_env moox_collector "${ROOT}/collector/config/app.yaml"
  start_service "collector" "${ROOT}/collector" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" "${COLLECTOR_ENV[@]}" "${ROOT}/bin/moox-collector" -conf=config/trpc_go.yaml
}

start_factor() {
  if [[ "${WITH_FACTOR}" != "1" ]]; then
    echo "factor is disabled in this deployment package" >&2
    exit 2
  fi
  wait_factor_nats
  ensure_factor_python
  gateway_service_env_for factor
  runtime_identity_env moox_factor "${ROOT}/factor/config/app.yaml"
  start_service "factor" "${ROOT}/factor" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" "${FACTOR_ENV[@]}" "${ROOT}/bin/moox-factor" -conf=config/trpc_go.yaml
}

start_strategy() {
  if [[ "${WITH_STRATEGY}" != "1" ]]; then
    echo "strategy is disabled in this deployment package" >&2
    exit 2
  fi
  wait_nats strategy "${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" "${MOOX_WAIT_STRATEGY_NATS_SECONDS:-60}"
  ensure_strategy_python
  gateway_service_env_for strategy
  runtime_identity_env moox_strategy "${ROOT}/strategy/config/app.yaml"
  start_service "strategy" "${ROOT}/strategy" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" "MOOX_EVENTBUS_NATS_URL=${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" \
      "MOOX_PYTHON_RUNTIME_PATH=${ROOT}/python-runtime" \
      "${ROOT}/bin/moox-strategy" -conf=config/trpc_go.yaml
}

start_monitor() {
  if [[ "${WITH_MONITOR}" != "1" ]]; then
    echo "monitor is disabled in this deployment package" >&2
    exit 2
  fi
  apply_metrics_metadata
  apply_host_metadata
  init_monitor_schema
  gateway_service_env_for monitor
  runtime_identity_env moox_monitor "${ROOT}/monitor/config/app.yaml"
  start_service "monitor" "${ROOT}/monitor" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" "${MONITOR_ENV[@]}" "${ROOT}/bin/moox-monitor" -conf=config/trpc_go.yaml
}

start_web_host() {
  if [[ "${WITH_WEB_HOST}" != "1" ]]; then
    echo "web-host is disabled in this deployment package" >&2
    exit 2
  fi
  if [[ ! -x "${ROOT}/bin/moox-web-host" ]]; then
    echo "web-host binary missing; skip" >&2
    return 1
  fi
  runtime_identity_env web_host
  start_service "web-host" "${ROOT}" \
    env "${RUNTIME_IDENTITY_ENV[@]}" \
      "MOOX_WEB_HOST_ADDR=${MOOX_WEB_HOST_ADDR:-127.0.0.1:9528}" \
      "MOOX_WEB_HOST_HEALTH_ADDR=${MOOX_WEB_HOST_HEALTH_ADDR:-127.0.0.1:19527}" \
      "${ROOT}/bin/moox-web-host"
}

SERVICE="${1:-}"
case "${SERVICE}" in
  "")
    if [[ "${WITH_ADMIN}" == "1" ]]; then
      start_admin
    fi
    start_gateway
    if [[ "${WITH_EVENTBUS}" == "1" ]]; then
      start_eventbus
    fi
    if [[ "${WITH_ARCHIVE}" == "1" ]]; then
      start_archive
    fi
    if [[ "${WITH_STORAGE}" == "1" ]]; then
      init_storage_schema
      start_storage
    fi
    if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
      start_cloudnode
    fi
    if [[ "${WITH_MONITOR}" == "1" ]]; then
      start_monitor
    fi
    if [[ "${WITH_COLLECTOR}" == "1" ]]; then
      start_collector
    fi
    if [[ "${WITH_FACTOR}" == "1" ]]; then
      start_factor
    fi
    if [[ "${WITH_STRATEGY}" == "1" ]]; then
      start_strategy
    fi
    if [[ "${WITH_WEB_HOST}" == "1" ]]; then
      start_web_host
    fi
    ;;
  storage)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    init_storage_schema
    start_storage
    ;;
  eventbus) start_eventbus ;;
  archive) start_archive ;;
  storage-primary)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    init_storage_schema
    start_storage_primary
    ;;
  storage-view)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    wait_tcp 127.0.0.1 20201 "${MOOX_WAIT_STORAGE_ACCESS_SECONDS:-30}"
    wait_tcp 127.0.0.1 4222 "${MOOX_WAIT_STORAGE_NATS_SECONDS:-30}"
    start_storage_view
    ;;
  storage-shard)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    start_storage_shard
    wait_tcp 127.0.0.1 20107 "${MOOX_WAIT_STORAGE_SHARD_SECONDS:-30}"
    wait_http http://127.0.0.1:20212/healthz "${MOOX_WAIT_STORAGE_SHARD_SECONDS:-30}"
    ;;
  cloudnode) start_cloudnode ;;
  collector) start_collector ;;
  factor) start_factor ;;
  strategy) start_strategy ;;
  monitor) start_monitor ;;
  gateway) start_gateway ;;
  admin) start_admin ;;
  web-host) start_web_host ;;
  *)
    echo "unknown service: ${SERVICE}; valid: eventbus storage storage-primary storage-view storage-shard cloudnode collector factor strategy monitor admin gateway web-host" >&2
    exit 2
    ;;
esac

echo "MooX services started"
echo "admin web: http://127.0.0.1:9527"
EOF

  cat > "${STAGE_DIR}/stop.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
set -a
source "${ROOT}/secrets/health-auth.env"
set +a
WITH_STORAGE="${MOOX_WITH_STORAGE:-__WITH_STORAGE__}"
WITH_STORAGE_SHARD="${MOOX_WITH_STORAGE_SHARD:-__WITH_STORAGE_SHARD__}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-__WITH_EVENTBUS__}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-__WITH_ARCHIVE__}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-__WITH_STRATEGY__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-__WITH_ADMIN__}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-__WITH_GATEWAY__}"
if [[ "${WITH_STORAGE_SHARD}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-shard requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" == "1" && ! -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" != "1" && -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard package is present but storage-shard is disabled" >&2
  exit 2
fi

process_matches_service() {
  local pid="$1" name="$2" command expected
  expected="${ROOT}/bin/moox-${name}"
  command=$(ps -p "${pid}" -o command= 2>/dev/null || true)
  [[ "${command}" == "${expected}" || "${command}" == "${expected} "* ]]
}

stop_processes_by_binary() {
  local name="$1" expected="${ROOT}/bin/moox-${name}" proc pid exe
  for proc in /proc/[0-9]*; do
    [[ -d "${proc}" ]] || continue
    pid="${proc##*/}"
    [[ "${pid}" =~ ^[0-9]+$ ]] || continue
    exe="$(readlink "${proc}/exe" 2>/dev/null || true)"
    case "${exe}" in
      "${expected}"|"${expected} (deleted)") ;;
      *) continue ;;
    esac
    echo "stopping stale ${name} process pid=${pid}"
    kill "${pid}" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    kill -9 "${pid}" 2>/dev/null || true
  done
}

stop_service() {
  local name="$1"
  local pid_file="${ROOT}/run/${name}.pid"
  local pattern="${ROOT}/bin/moox-${name}([[:space:]]|$)"
  if [[ ! -f "${pid_file}" ]]; then
    stop_processes_by_binary "${name}"
    if pkill -f -- "${pattern}" 2>/dev/null; then
      echo "${name}: stopped stale process without pid file"
    else
      echo "${name}: not running"
    fi
    return
  fi
  local pid
  pid="$(cat "${pid_file}" 2>/dev/null || true)"
  if [[ -z "${pid}" ]]; then
    rm -f "${pid_file}"
    stop_processes_by_binary "${name}"
    if pkill -f -- "${pattern}" 2>/dev/null; then
      echo "${name}: stopped stale process with empty pid file"
    else
      echo "${name}: empty pid file removed"
    fi
    return
  fi
  if ps -p "${pid}" >/dev/null 2>&1 && process_matches_service "${pid}" "${name}"; then
    echo "stopping ${name} pid=${pid}"
    kill "${pid}" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      if ! ps -p "${pid}" >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
    if ps -p "${pid}" >/dev/null 2>&1 && process_matches_service "${pid}" "${name}"; then
      kill -9 "${pid}" 2>/dev/null || true
    fi
  elif ps -p "${pid}" >/dev/null 2>&1; then
    echo "${name}: stale pid ${pid} belongs to another process; removing pid file"
    rm -f "${pid_file}"
    return
  else
    echo "${name}: stale pid ${pid}"
  fi
  stop_processes_by_binary "${name}"
  pkill -f -- "${pattern}" 2>/dev/null || true
  rm -f "${pid_file}"
}

SERVICE="${1:-}"
case "${SERVICE}" in
  "")
    if [[ "${WITH_WEB_HOST}" == "1" ]]; then
      stop_service "web-host"
    fi
    if [[ "${WITH_MONITOR}" == "1" ]]; then
      stop_service "monitor"
    fi
    stop_service "gateway"
    if [[ "${WITH_ADMIN}" == "1" ]]; then
      stop_service "admin"
    fi
    if [[ "${WITH_COLLECTOR}" == "1" ]]; then
      stop_service "collector"
    fi
    if [[ "${WITH_FACTOR}" == "1" ]]; then
      stop_service "factor"
    fi
    if [[ "${WITH_STRATEGY}" == "1" ]]; then
      stop_service "strategy"
    fi
    if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
      stop_service "cloudnode"
    fi
    if [[ "${WITH_STORAGE}" == "1" ]]; then
      if [[ "${WITH_STORAGE_SHARD}" == "1" ]]; then
        stop_service "storage-shard"
      fi
      stop_service "storage-view"
      stop_service "storage-primary"
      stop_service "storage"
    fi
    if [[ "${WITH_EVENTBUS}" == "1" ]]; then
      stop_service "eventbus"
    fi
    if [[ "${WITH_ARCHIVE}" == "1" ]]; then
      stop_service "archive"
    fi
    ;;
  storage)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    if [[ "${WITH_STORAGE_SHARD}" == "1" ]]; then
      stop_service "storage-shard"
    fi
    stop_service "storage-view"
    stop_service "storage-primary"
    stop_service "storage"
    ;;
  eventbus)
    if [[ "${WITH_EVENTBUS}" != "1" ]]; then
      echo "eventbus is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "eventbus"
    ;;
  archive)
    stop_service "archive"
    ;;
  storage-primary|storage-view|storage-shard)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  admin)
    [[ "${WITH_ADMIN}" == "1" ]] || { echo "admin is disabled in this deployment package" >&2; exit 2; }
    stop_service "${SERVICE}"
    ;;
  gateway) stop_service "gateway" ;;
  web-host)
    if [[ "${WITH_WEB_HOST}" != "1" ]]; then
      echo "web-host is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  cloudnode)
    if [[ "${WITH_CLOUDNODE}" != "1" ]]; then
      echo "cloudnode is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  collector)
    if [[ "${WITH_COLLECTOR}" != "1" ]]; then
      echo "collector is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  factor)
    if [[ "${WITH_FACTOR}" != "1" ]]; then
      echo "factor is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  strategy)
    if [[ "${WITH_STRATEGY}" != "1" ]]; then
      echo "strategy is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  monitor)
    if [[ "${WITH_MONITOR}" != "1" ]]; then
      echo "monitor is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  *)
    echo "unknown service: ${SERVICE}; valid: eventbus storage storage-primary storage-view storage-shard cloudnode collector factor strategy monitor admin gateway web-host" >&2
    exit 2
    ;;
esac
EOF

  cat > "${STAGE_DIR}/restart.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE="${1:-}"

if [[ -n "${SERVICE}" ]]; then
  echo "restarting ${SERVICE}"
else
  echo "restarting all MooX services"
fi

"${ROOT}/stop.sh" "${SERVICE}"
"${ROOT}/start.sh" "${SERVICE}"
EOF

  cat > "${STAGE_DIR}/status.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WITH_STORAGE="${MOOX_WITH_STORAGE:-__WITH_STORAGE__}"
WITH_STORAGE_SHARD="${MOOX_WITH_STORAGE_SHARD:-__WITH_STORAGE_SHARD__}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-__WITH_EVENTBUS__}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-__WITH_ARCHIVE__}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-__WITH_STRATEGY__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-__WITH_ADMIN__}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-__WITH_GATEWAY__}"
if [[ "${WITH_STORAGE_SHARD}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-shard requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" == "1" && ! -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" != "1" && -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard package is present but storage-shard is disabled" >&2
  exit 2
fi

services=()
if [[ "${WITH_ADMIN}" == "1" ]]; then
  services+=(admin)
fi
if [[ "${WITH_GATEWAY}" == "1" ]]; then
  services+=(gateway)
fi
if [[ "${WITH_ARCHIVE}" == "1" ]]; then
  services=(archive "${services[@]}" )
fi
if [[ "${WITH_EVENTBUS}" == "1" ]]; then
  services=(eventbus "${services[@]}")
fi
if [[ "${WITH_WEB_HOST}" == "1" ]]; then
  services+=(web-host)
fi
if [[ "${WITH_MONITOR}" == "1" ]]; then
  services=(monitor "${services[@]}")
fi
if [[ "${WITH_STORAGE}" == "1" ]]; then
  if [[ "${WITH_STORAGE_SHARD}" == "1" ]]; then
    services+=(storage-shard)
  fi
  services=(storage-primary storage-view "${services[@]}")
fi
if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
  services=(cloudnode "${services[@]}")
fi
if [[ "${WITH_COLLECTOR}" == "1" ]]; then
  services=(collector "${services[@]}")
fi
if [[ "${WITH_FACTOR}" == "1" ]]; then
  services=(factor "${services[@]}")
fi
if [[ "${WITH_STRATEGY}" == "1" ]]; then
  services=(strategy "${services[@]}")
fi

for name in "${services[@]}"; do
  pid_file="${ROOT}/run/${name}.pid"
  if [[ ! -f "${pid_file}" ]]; then
    echo "${name}: stopped"
    continue
  fi
  pid="$(cat "${pid_file}" 2>/dev/null || true)"
  if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
    echo "${name}: running pid=${pid}"
  else
    echo "${name}: stopped (stale pid=${pid:-none})"
  fi
done
EOF

  cat > "${STAGE_DIR}/healthcheck.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
set -a
source "${ROOT}/secrets/health-auth.env"
set +a
WITH_STORAGE="${MOOX_WITH_STORAGE:-__WITH_STORAGE__}"
WITH_STORAGE_SHARD="${MOOX_WITH_STORAGE_SHARD:-__WITH_STORAGE_SHARD__}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-__WITH_EVENTBUS__}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-__WITH_ARCHIVE__}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-__WITH_STRATEGY__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-__WITH_ADMIN__}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-__WITH_GATEWAY__}"
if [[ "${WITH_STORAGE_SHARD}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-shard requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" == "1" && ! -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_SHARD}" != "1" && -d "${ROOT}/storage-shard" ]]; then
  echo "storage-shard package is present but storage-shard is disabled" >&2
  exit 2
fi
LOG_FILE="${ROOT}/logs/healthcheck.log"

mkdir -p "${ROOT}/run" "$(dirname "${LOG_FILE}")"

sign_health_request() {
  local method="$1" path="$2" timestamp nonce body_hash canonical signature
  timestamp=$(date +%s); nonce=$(openssl rand -hex 32)
  body_hash=$(printf '' | openssl dgst -sha256 | awk '{print $NF}')
  canonical=$(printf 'moox-request-v1\n%s\n%s\n%s\n%s\n%s' "${method}" "${path}" "${body_hash}" "${timestamp}" "${nonce}")
  signature=$(printf '%s' "${canonical}" | openssl dgst -sha256 -hmac "${MOOX_HEALTH_AUTH_SECRET_KEY}" | awk '{print $NF}')
  printf '%s/%s/%s/%s/%s' "${MOOX_HEALTH_AUTH_VERSION}" "${MOOX_HEALTH_AUTH_ACCESS_KEY}" "${timestamp}" "${nonce}" "${signature}"
}

probe_service() {
  local name="$1" url=""
  case "${name}" in
    admin) url=http://127.0.0.1:11010/readyz ;;
    gateway) url=http://127.0.0.1:11012/readyz ;;
    archive) url=http://127.0.0.1:11416/readyz ;;
    cloudnode) url=http://127.0.0.1:11411/readyz ;;
    collector) url=http://127.0.0.1:11412/readyz ;;
    eventbus) url=http://127.0.0.1:11419/readyz ;;
    factor) url=http://127.0.0.1:11414/readyz ;;
    strategy) url=http://127.0.0.1:11431/readyz ;;
    monitor) url=http://127.0.0.1:11409/readyz ;;
    web-host) url=http://127.0.0.1:19527/readyz ;;
    storage-primary) url=http://127.0.0.1:20210/readyz ;;
    storage-view) url=http://127.0.0.1:20211/readyz ;;
    storage-shard) url=http://127.0.0.1:20212/readyz ;;
    *) echo "unknown service health mapping: ${name}" >&2; return 1 ;;
  esac
  curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $(sign_health_request GET /readyz)" "${url}" >/dev/null
}

default_services=()
if [[ "${WITH_ARCHIVE}" == "1" ]]; then
  default_services+=(archive)
fi
if [[ "${WITH_EVENTBUS}" == "1" ]]; then
  default_services+=(eventbus)
fi
if [[ "${WITH_STORAGE}" == "1" ]]; then
	default_services+=(storage-primary storage-view)
  if [[ "${WITH_STORAGE_SHARD}" == "1" ]]; then
    default_services+=(storage-shard)
  fi
fi
if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
  default_services+=(cloudnode)
fi
if [[ "${WITH_ADMIN}" == "1" ]]; then
  default_services+=(admin)
fi
if [[ "${WITH_GATEWAY}" == "1" ]]; then
  default_services+=(gateway)
fi
if [[ "${WITH_MONITOR}" == "1" ]]; then
  default_services+=(monitor)
fi
if [[ "${WITH_COLLECTOR}" == "1" ]]; then
  default_services+=(collector)
fi
if [[ "${WITH_FACTOR}" == "1" ]]; then
  default_services+=(factor)
fi
if [[ "${WITH_STRATEGY}" == "1" ]]; then
  default_services+=(strategy)
fi
if [[ "${WITH_WEB_HOST}" == "1" ]]; then
  default_services+=(web-host)
fi

services=("${default_services[@]}")
if [[ "$#" -gt 0 ]]; then
  services=("$@")
fi

log_line() {
  echo "$(date -Is) $*" >> "${LOG_FILE}"
}

ensure_service() {
  local name="$1"
  local pid_file="${ROOT}/run/${name}.pid"
  local pid=""
  if [[ -f "${pid_file}" ]]; then
    pid="$(cat "${pid_file}" 2>/dev/null || true)"
  fi

  if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
    if probe_service "${name}"; then
      echo "${name}: running pid=${pid} ready"
      return 0
    fi
    log_line "${name}: health probe failed pid=${pid}; restarting"
    echo "${name}: health probe failed; restarting"
    "${ROOT}/stop.sh" "${name}" >> "${LOG_FILE}" 2>&1 || true
  fi

  log_line "${name}: stopped or stale pid=${pid:-none}; restarting"
  echo "${name}: stopped; restarting"
  if STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-3}" "${ROOT}/start.sh" "${name}" 9>&- >> "${LOG_FILE}" 2>&1 && probe_service "${name}"; then
    log_line "${name}: restart success"
    return 0
  fi
  log_line "${name}: restart failed"
  return 1
}

(
  flock -n 9 || exit 0
  failed=0
  for name in "${services[@]}"; do
    ensure_service "${name}" || failed=1
  done
  exit "${failed}"
) 9>"${ROOT}/run/healthcheck.lock"
EOF

  perl -0pi -e "s#__WITH_STORAGE__#${WITH_STORAGE}#g; s#__WITH_STORAGE_SHARD__#${WITH_STORAGE_SHARD}#g; s#__WITH_ARCHIVE__#${WITH_ARCHIVE}#g; s#__WITH_EVENTBUS__#${WITH_EVENTBUS}#g; s#__WITH_CLOUDNODE__#${WITH_CLOUDNODE}#g; s#__WITH_COLLECTOR__#${WITH_COLLECTOR}#g; s#__WITH_FACTOR__#${WITH_FACTOR}#g; s#__WITH_STRATEGY__#${WITH_STRATEGY}#g; s#__WITH_MONITOR__#${WITH_MONITOR}#g; s#__WITH_WEB_HOST__#${WITH_WEB_HOST}#g; s#__WITH_ADMIN__#${WITH_ADMIN}#g; s#__WITH_GATEWAY__#${WITH_GATEWAY}#g; s#__NODE_ID__#${NODE_ID}#g; s#__MONITOR_INSTANCE_ID__#${MONITOR_INSTANCE_ID}#g" \
    "${STAGE_DIR}/start.sh" "${STAGE_DIR}/stop.sh" "${STAGE_DIR}/status.sh" "${STAGE_DIR}/healthcheck.sh"
  chmod +x "${STAGE_DIR}/start.sh" "${STAGE_DIR}/stop.sh" "${STAGE_DIR}/status.sh" "${STAGE_DIR}/restart.sh" "${STAGE_DIR}/healthcheck.sh"
}

prepare_stage() {
  rm -rf "${STAGE_DIR}"
  mkdir -p \
    "${STAGE_DIR}/bin" \
    "${STAGE_DIR}/gateway/config" \
    "${STAGE_DIR}/archive/config" \
    "${STAGE_DIR}/eventbus/config" \
    "${STAGE_DIR}/cloudnode/config" \
    "${STAGE_DIR}/collector/config" \
    "${STAGE_DIR}/collector/configs" \
    "${STAGE_DIR}/factor/config" \
    "${STAGE_DIR}/factor/factors" \
    "${STAGE_DIR}/factor/sections" \
    "${STAGE_DIR}/strategy/config" \
    "${STAGE_DIR}/strategy/pyworker" \
    "${STAGE_DIR}/strategy/pysdk" \
    "${STAGE_DIR}/strategy/strategies/example" \
    "${STAGE_DIR}/python-runtime" \
    "${STAGE_DIR}/monitor/config" \
    "${STAGE_DIR}/examples" \
    "${STAGE_DIR}/data" \
    "${STAGE_DIR}/logs" \
    "${STAGE_DIR}/run"
  mkdir -p "${STAGE_DIR}/secrets" "${STAGE_DIR}/certs/gateway"
  local gateway_control_secret gateway_service_secret
  if [[ "${AUTO_GATEWAY_INPUTS}" -eq 1 ]]; then
    gateway_control_secret="$(generate_secret "${ROOT}/bin/moox-admin-cli" gateway-control)"
    gateway_service_secret="$(generate_secret "${ROOT}/bin/moox-admin-cli" gateway-service)"
    command -v openssl >/dev/null 2>&1 || fail "openssl is required to generate the control package trust bundle"
    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
      -subj /CN=moox-control-package-one -keyout /dev/null \
      -out "${STAGE_DIR}/certs/gateway/peer-one.pem" >/dev/null 2>&1
    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
      -subj /CN=moox-control-package-two -keyout /dev/null \
      -out "${STAGE_DIR}/certs/gateway/peer-two.pem" >/dev/null 2>&1
    cat "${STAGE_DIR}/certs/gateway/peer-one.pem" "${STAGE_DIR}/certs/gateway/peer-two.pem" \
      >"${STAGE_DIR}/certs/gateway/peers.pem"
    rm -f "${STAGE_DIR}/certs/gateway/peer-one.pem" "${STAGE_DIR}/certs/gateway/peer-two.pem"
  else
    gateway_control_secret="$(cat "${GATEWAY_CONTROL_KEY_FILE}")"
    gateway_service_secret="$(cat "${GATEWAY_SERVICE_KEY_FILE}")"
    install -m 0644 "${GATEWAY_CA_BUNDLE}" "${STAGE_DIR}/certs/gateway/peers.pem"
  fi
  [[ -n "${gateway_control_secret}" && -n "${gateway_service_secret}" ]] || fail "Gateway key files cannot be empty"
  [[ "${gateway_control_secret}" != *$'\n'* && "${gateway_control_secret}" != *$'\r'* && \
     "${gateway_service_secret}" != *$'\n'* && "${gateway_service_secret}" != *$'\r'* ]] || \
    fail "Gateway key files must contain exactly one line"
  [[ "${gateway_control_secret}" == "$(printf '%s' "${gateway_control_secret}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')" && \
     "${gateway_service_secret}" == "$(printf '%s' "${gateway_service_secret}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')" ]] || \
    fail "Gateway keys cannot have leading or trailing whitespace"
  validate_gateway_ca_bundle "${STAGE_DIR}/certs/gateway/peers.pem"
  (umask 077; printf '%s\n' "${gateway_control_secret}" >"${STAGE_DIR}/secrets/gateway-control.key")
  (umask 077; printf '%s\n' "${gateway_service_secret}" >"${STAGE_DIR}/secrets/gateway-service.key")
  command -v openssl >/dev/null 2>&1 || fail "openssl is required to derive Gateway service credentials"
  local caller derived_secret
  for caller in collector factor monitor archive storage-view storage-primary strategy; do
    derived_secret=$(printf 'moox-gateway-service-v1:%s' "${caller}" | openssl dgst -sha256 -hmac "${gateway_service_secret}" -r | awk '{print $1}')
    [[ -n "${derived_secret}" ]] || fail "failed to derive Gateway credential for ${caller}"
    (umask 077; printf '%s\n' "${derived_secret}" >"${STAGE_DIR}/secrets/gateway-${caller}.key")
  done
  {
    printf 'MOOX_GATEWAY_CONTROL_KEY_ID=moox-gateway-control\n'
    printf 'MOOX_GATEWAY_CONTROL_SECRET_KEY=%q\n' "${gateway_control_secret}"
  } >"${STAGE_DIR}/secrets/gateway-control.env"
  {
    printf 'MOOX_GATEWAY_NODE_ID=%q\n' "${NODE_ID}"
    printf 'MOOX_GATEWAY_SERVICE_KEY_ID=moox-gateway-service\n'
    printf 'MOOX_GATEWAY_SERVICE_SECRET_KEY=%q\n' "${gateway_service_secret}"
  } >"${STAGE_DIR}/secrets/gateway-service.env"
  cat >"${STAGE_DIR}/secrets/gateway-credentials.json" <<'EOF'
{"version":1,"credentials":[{"key_id":"moox-gateway-service","caller":"admin-gateway","secret_file":"gateway-service.key"},{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"},{"key_id":"factor","caller":"factor","secret_file":"gateway-factor.key"},{"key_id":"monitor","caller":"monitor","secret_file":"gateway-monitor.key"},{"key_id":"archive","caller":"archive","secret_file":"gateway-archive.key"},{"key_id":"storage-view","caller":"storage-view","secret_file":"gateway-storage-view.key"},{"key_id":"storage-primary","caller":"storage-primary","secret_file":"gateway-storage-primary.key"},{"key_id":"strategy","caller":"strategy","secret_file":"gateway-strategy.key"}]}
EOF
  chmod 0600 "${STAGE_DIR}/secrets/gateway-control.env" "${STAGE_DIR}/secrets/gateway-service.env" "${STAGE_DIR}/secrets/gateway-credentials.json"
  mkdir -p "${STAGE_DIR}/lib" "${STAGE_DIR}/config/caddy"
  cp "${ROOT}/scripts/lib/caddy-managed.sh" "${STAGE_DIR}/lib/caddy-managed.sh"
  cp "${ROOT}/scripts/lib/loopback-listeners.sh" "${STAGE_DIR}/lib/loopback-listeners.sh"
  cp "${ROOT}/scripts/install-caddy-ca.sh" "${STAGE_DIR}/lib/install-caddy-ca.sh"
  cp "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${STAGE_DIR}/lib/caddy-v2.11.4-checksums.txt"
  if [[ -n "${PUBLIC_HOST}" ]]; then
    local caddy_os caddy_asset caddy_archive
    case "${TARGET_GOOS}" in
      linux) caddy_os=linux ;;
      darwin) caddy_os=mac ;;
      *) fail "managed Caddy does not support target OS ${TARGET_GOOS}" ;;
    esac
    caddy_asset="caddy_2.11.4_${caddy_os}_${TARGET_GOARCH}.tar.gz"
    caddy_archive="${STAGE_DIR}/lib/${caddy_asset}"
    log "download pinned Caddy ${caddy_asset} for deployment bundle"
    curl -fL --retry 3 --connect-timeout 10 --max-time 180 \
      -o "${caddy_archive}" "https://github.com/caddyserver/caddy/releases/download/v2.11.4/${caddy_asset}"
  fi
  if [[ "${WITH_ADMIN}" -eq 1 ]]; then
    mkdir -p "${STAGE_DIR}/admin/config"
    cp "${ROOT}/deploy/caddy/Caddyfile" "${STAGE_DIR}/config/caddy/Caddyfile.next"
  else
    cp "${ROOT}/deploy/caddy/Caddyfile.no-admin" "${STAGE_DIR}/config/caddy/Caddyfile.next"
  fi
  chmod +x "${STAGE_DIR}/lib/caddy-managed.sh" "${STAGE_DIR}/lib/loopback-listeners.sh" "${STAGE_DIR}/lib/install-caddy-ca.sh"
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    mkdir -p "${STAGE_DIR}/storage/config" "${STAGE_DIR}/storage/schema" "${STAGE_DIR}/storage-view/config"
  if [[ "${WITH_STORAGE_SHARD}" -eq 1 ]]; then
      mkdir -p "${STAGE_DIR}/storage-shard/config"
    fi
  fi

  copy_required_binary "moox-gateway"
  copy_required_binary "moox-gateway-cli"
  if [[ "${WITH_ADMIN}" -eq 1 || "${WITH_MONITOR}" -eq 1 ]]; then
    copy_required_binary "moox-cli"
  fi
  if [[ "${WITH_ADMIN}" -eq 1 ]]; then
    copy_required_binary "moox-admin"
    copy_required_binary "moox-admin-cli"
  fi
  if [[ "${WITH_ARCHIVE}" -eq 1 ]]; then
    copy_required_binary "moox-archive"
    copy_required_binary "moox-archive-cli"
  fi
  if [[ "${WITH_EVENTBUS}" -eq 1 ]]; then
    copy_required_binary "moox-eventbus"
  fi
  if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
    copy_required_binary "moox-cloudnode"
    copy_required_binary "moox-cloudnode-cli"
  fi
  if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
    copy_required_binary "moox-collector"
    copy_required_binary "moox-collector-cli"
    copy_required_binary "moox-collector-scf"
  fi
  if [[ "${WITH_FACTOR}" -eq 1 ]]; then
    copy_required_binary "moox-factor"
    copy_required_binary "moox-factor-cli"
  fi
  if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
    copy_required_binary "moox-strategy"
    copy_required_binary "moox-strategy-cli"
  fi
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    copy_required_binary "moox-monitor"
    copy_required_binary "moox-monitor-cli"
  fi
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    copy_required_binary "moox-storage-primary"
    copy_required_binary "moox-storage-view"
    copy_required_binary "moox-storage-cli"
    if [[ "${WITH_STORAGE_SHARD}" -eq 1 ]]; then
      copy_required_binary "moox-storage-shard"
    fi
  fi
  copy_optional_web_host

  cp -R "${ROOT}/modules/gateway/config/." "${STAGE_DIR}/gateway/config/"
  perl -0pi -e 's#hmac_key_file:\s*\./secrets/gateway-service\.key#credentials_file: ../../secrets/gateway-credentials.json#' "${STAGE_DIR}/gateway/config/app.yaml"
  if [[ "${WITH_ADMIN}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/admin/config/." "${STAGE_DIR}/admin/config/"
  fi
  if [[ "${WITH_ARCHIVE}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/archive/config/." "${STAGE_DIR}/archive/config/"
  fi
  if [[ "${WITH_EVENTBUS}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/eventbus/config/." "${STAGE_DIR}/eventbus/config/"
  fi
  if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/cloudnode/config/." "${STAGE_DIR}/cloudnode/config/"
  fi
  if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/collector/config/." "${STAGE_DIR}/collector/config/"
    cp -R "${ROOT}/modules/collector/configs/." "${STAGE_DIR}/collector/configs/"
  fi
  if [[ "${WITH_FACTOR}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/factor/config/." "${STAGE_DIR}/factor/config/"
    cp -R "${ROOT}/modules/factor/factors/." "${STAGE_DIR}/factor/factors/"
    cp -R "${ROOT}/modules/factor/sections/." "${STAGE_DIR}/factor/sections/"
    cp -R "${ROOT}/modules/factor/pyworker" "${STAGE_DIR}/factor/pyworker"
    find "${STAGE_DIR}/factor/pyworker" -type d -name __pycache__ -prune -exec rm -rf {} +
  fi
  if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/strategy/config/." "${STAGE_DIR}/strategy/config/"
    cp -R "${ROOT}/modules/strategy/pyworker/." "${STAGE_DIR}/strategy/pyworker/"
    cp -R "${ROOT}/modules/strategy/pysdk/." "${STAGE_DIR}/strategy/pysdk/"
    cp -R "${ROOT}/modules/strategy/strategies/example/." "${STAGE_DIR}/strategy/strategies/example/"
    find "${STAGE_DIR}/strategy" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
    find "${STAGE_DIR}/strategy" -type f \( -name '*.pyc' -o -name '*.sqlite' -o -name '*.db' \) -delete
  fi
  if [[ "${WITH_FACTOR}" -eq 1 || "${WITH_STRATEGY}" -eq 1 ]]; then
    cp -R "${ROOT}/packages/pyruntime/python/." "${STAGE_DIR}/python-runtime/"
    find "${STAGE_DIR}/python-runtime" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
    find "${STAGE_DIR}/python-runtime" -type f -name '*.pyc' -delete
  fi
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/monitor/config/." "${STAGE_DIR}/monitor/config/"
  fi
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/storage/config/." "${STAGE_DIR}/storage/config/"
    # The bundled primary process owns the primary role only. The View role
    # has its own process and its own storage business config.
    cp "${ROOT}/modules/storage/config/storage.primary.yaml" "${STAGE_DIR}/storage/config/storage.yaml"
    if [[ "${WITH_STORAGE_SHARD}" -eq 1 ]]; then
      cp "${ROOT}/modules/storage/config/trpc_go.shard.yaml" "${STAGE_DIR}/storage-shard/config/trpc_go.yaml"
      cp "${ROOT}/modules/storage/config/storage.shard.yaml" "${STAGE_DIR}/storage-shard/config/storage.yaml"
    fi
    rm -f "${STAGE_DIR}/storage/config/trpc_go.shard.yaml" "${STAGE_DIR}/storage/config/storage.shard.yaml"
    cp "${STAGE_DIR}/storage/config/trpc_go.primary.yaml" "${STAGE_DIR}/storage/config/trpc_go.yaml"
    printf '\n' >> "${STAGE_DIR}/storage/config/trpc_go.yaml"
    cat "${STAGE_DIR}/storage/config/storage.primary.yaml" >> "${STAGE_DIR}/storage/config/trpc_go.yaml"
    rm -f "${STAGE_DIR}/storage/config/trpc_go.primary.yaml" "${STAGE_DIR}/storage/config/storage.primary.yaml"
    cp "${ROOT}/modules/storage/config/storage_view/trpc_go.yaml" "${STAGE_DIR}/storage-view/config/trpc_go.yaml"
    rm -rf "${STAGE_DIR}/storage/config/storage_view"
    cp "${ROOT}/modules/storage/schema/metadata.sql" "${STAGE_DIR}/storage/schema/metadata.sql"
  fi
  cp -R "${ROOT}/examples/." "${STAGE_DIR}/examples/"
  cp "${ROOT}/examples/monitor-pipelines.yaml" "${STAGE_DIR}/config/monitor-pipelines.yaml"
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    cp "${ROOT}/scripts/reset-storage-view-indexes.sh" "${STAGE_DIR}/reset-storage-view-indexes.sh"
  fi

  patch_configs
  write_runtime_scripts
  chmod +x "${STAGE_DIR}/bin/"*
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    chmod +x "${STAGE_DIR}/reset-storage-view-indexes.sh"
  fi
}

prepare_cls_preflight() {
  [[ "${ENABLE_CLS}" -eq 1 ]] || return 0
  local args=(
    --target "${TARGET}"
    --deploy-dir "${DEPLOY_DIR}"
    --stage-dir "${STAGE_DIR}"
    --admin-url http://127.0.0.1:11002
  )
  [[ -z "${CLOUD_ACCOUNT_ID}" ]] || args+=(--cloud-account-id "${CLOUD_ACCOUNT_ID}")
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" "${args[@]}"
}

sync_local_stage() {
  local deploy_dir caddy_data_tmp=""
  deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
  mkdir -p "${deploy_dir}"

  if [[ -e "${deploy_dir}/config/caddy/edge.env" || -e "${deploy_dir}/config/caddy/Caddyfile" || -e "${deploy_dir}/run/caddy.pid" ]]; then
    [[ "${NO_START}" -eq 0 ]] || fail "--no-start refuses to replace an existing managed Caddy deployment"
    [[ -n "${PUBLIC_HOST}" ]] || fail "existing managed Caddy deployment requires --public-host"
  fi

  if [[ -x "${deploy_dir}/stop.sh" && "${NO_START}" -eq 0 ]]; then
    if [[ "${WITH_STORAGE}" -eq 1 ]]; then
      MOOX_WITH_EVENTBUS="${WITH_EVENTBUS}" MOOX_WITH_ARCHIVE="${WITH_ARCHIVE}" "${deploy_dir}/stop.sh" || true
    else
      if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" collector || true
      fi
      if [[ "${WITH_FACTOR}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" factor || true
      fi
      if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" strategy || true
      fi
      if [[ "${WITH_MONITOR}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" monitor || true
      fi
      if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" cloudnode || true
      fi
      if [[ "${WITH_EVENTBUS}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" eventbus || true
      fi
      if [[ "${WITH_ARCHIVE}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" archive || true
      fi
      if [[ "${WITH_WEB_HOST}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" web-host || true
      fi
      "${deploy_dir}/stop.sh" admin || true
    fi
  fi

  if [[ "${RESET_DATA}" -eq 1 ]]; then
    if [[ -d "${deploy_dir}/data/caddy" ]]; then
      caddy_data_tmp=$(mktemp -d "${TMPDIR:-/tmp}/moox-caddy-data.XXXXXX")
      mv "${deploy_dir}/data/caddy" "${caddy_data_tmp}/caddy"
    fi
    rm -rf "${deploy_dir}/data"
    if [[ -n "${caddy_data_tmp}" ]]; then
      mkdir -p "${deploy_dir}/data"
      mv "${caddy_data_tmp}/caddy" "${deploy_dir}/data/caddy"
      rmdir "${caddy_data_tmp}"
    fi
  fi

  if command -v rsync >/dev/null 2>&1; then
    local rsync_excludes=(--exclude '/data/' --exclude '/logs/' --exclude '/run/' --exclude '/secrets/' --exclude '/certs/' --exclude '/config/caddy/Caddyfile' --exclude '/config/caddy/Caddyfile.rollback')
    if [[ "${WITH_STORAGE}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/storage/' --exclude '/storage-view/' --exclude '/storage-shard/' --exclude '/bin/moox-storage' --exclude '/bin/moox-storage-cli' --exclude '/bin/moox-storage-primary' --exclude '/bin/moox-storage-view' --exclude '/bin/moox-storage-shard')
    fi
    if [[ "${WITH_EVENTBUS}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/eventbus/' --exclude '/bin/moox-eventbus')
    fi
    if [[ "${WITH_ARCHIVE}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/archive/' --exclude '/bin/moox-archive' --exclude '/bin/moox-archive-cli')
    fi
    if [[ "${WITH_CLOUDNODE}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/cloudnode/' --exclude '/bin/moox-cloudnode' --exclude '/bin/moox-cloudnode-cli')
    fi
    if [[ "${WITH_COLLECTOR}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/collector/' --exclude '/bin/moox-collector' --exclude '/bin/moox-collector-cli' --exclude '/bin/moox-collector-scf')
    fi
    if [[ "${WITH_FACTOR}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/factor/' --exclude '/bin/moox-factor' --exclude '/bin/moox-factor-cli')
    fi
    if [[ "${WITH_STRATEGY}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/strategy/' --exclude '/bin/moox-strategy' --exclude '/bin/moox-strategy-cli')
    fi
    if [[ "${WITH_FACTOR}" -eq 0 && "${WITH_STRATEGY}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/python-runtime/')
    fi
    if [[ "${WITH_MONITOR}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/monitor/' --exclude '/bin/moox-monitor' --exclude '/bin/moox-monitor-cli')
    fi
    if [[ "${WITH_WEB_HOST}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/bin/moox-web-host')
    fi
    rsync -a --delete \
      "${rsync_excludes[@]}" \
      "${STAGE_DIR}/" "${deploy_dir}/"
  else
    rm -rf "${deploy_dir}/admin" "${deploy_dir}/examples" \
      "${deploy_dir}/start.sh" "${deploy_dir}/stop.sh" "${deploy_dir}/restart.sh" "${deploy_dir}/status.sh" "${deploy_dir}/healthcheck.sh"
    rm -f "${deploy_dir}/bin/moox-admin" "${deploy_dir}/bin/moox-admin-cli" \
      "${deploy_dir}/bin/moox-cli"
    if [[ "${WITH_EVENTBUS}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/eventbus"
      rm -f "${deploy_dir}/bin/moox-eventbus"
    fi
    if [[ "${WITH_ARCHIVE}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/archive"
      rm -f "${deploy_dir}/bin/moox-archive" "${deploy_dir}/bin/moox-archive-cli"
    fi
    if [[ "${WITH_WEB_HOST}" -eq 1 ]]; then
      rm -f "${deploy_dir}/bin/moox-web-host"
    fi
    if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/cloudnode"
      rm -f "${deploy_dir}/bin/moox-cloudnode" "${deploy_dir}/bin/moox-cloudnode-cli"
    fi
    if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/collector"
      rm -f "${deploy_dir}/bin/moox-collector" "${deploy_dir}/bin/moox-collector-cli" "${deploy_dir}/bin/moox-collector-scf"
    fi
    if [[ "${WITH_FACTOR}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/factor"
      rm -f "${deploy_dir}/bin/moox-factor" "${deploy_dir}/bin/moox-factor-cli"
    fi
    if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/strategy"
      rm -f "${deploy_dir}/bin/moox-strategy" "${deploy_dir}/bin/moox-strategy-cli"
    fi
    if [[ "${WITH_MONITOR}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/monitor"
      rm -f "${deploy_dir}/bin/moox-monitor" "${deploy_dir}/bin/moox-monitor-cli"
    fi
    if [[ "${WITH_STORAGE}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/storage" "${deploy_dir}/storage-view" "${deploy_dir}/storage-shard"
      rm -f "${deploy_dir}/bin/moox-storage" "${deploy_dir}/bin/moox-storage-cli" \
        "${deploy_dir}/bin/moox-storage-primary" \
        "${deploy_dir}/bin/moox-storage-view" \
        "${deploy_dir}/bin/moox-storage-shard"
    fi
    cp -R "${STAGE_DIR}/." "${deploy_dir}/"
  fi

  mkdir -p "${deploy_dir}/secrets" "${deploy_dir}/certs/gateway"
  install -m 0600 "${STAGE_DIR}/secrets/gateway-control.key" "${deploy_dir}/secrets/gateway-control.key"
  install -m 0600 "${STAGE_DIR}/secrets/gateway-service.key" "${deploy_dir}/secrets/gateway-service.key"
  install -m 0600 "${STAGE_DIR}/secrets/gateway-control.env" "${deploy_dir}/secrets/gateway-control.env"
  install -m 0600 "${STAGE_DIR}/secrets/gateway-service.env" "${deploy_dir}/secrets/gateway-service.env"
  for credential_file in "${STAGE_DIR}"/secrets/gateway-collector.key "${STAGE_DIR}"/secrets/gateway-factor.key "${STAGE_DIR}"/secrets/gateway-monitor.key "${STAGE_DIR}"/secrets/gateway-archive.key "${STAGE_DIR}"/secrets/gateway-storage-view.key "${STAGE_DIR}"/secrets/gateway-storage-primary.key "${STAGE_DIR}"/secrets/gateway-strategy.key; do
    install -m 0600 "${credential_file}" "${deploy_dir}/secrets/$(basename "${credential_file}")"
  done
  install -m 0600 "${STAGE_DIR}/secrets/gateway-credentials.json" "${deploy_dir}/secrets/gateway-credentials.json"
  install -m 0644 "${STAGE_DIR}/certs/gateway/peers.pem" "${deploy_dir}/certs/gateway/peers.pem"
  if [[ "${WITH_ADMIN}" -eq 0 ]]; then
    rm -f "${deploy_dir}/secrets/admin-jwt.env"
  fi
  chmod +x "${deploy_dir}/start.sh" "${deploy_dir}/stop.sh" "${deploy_dir}/status.sh" "${deploy_dir}/healthcheck.sh" "${deploy_dir}/bin/"*
  mkdir -p "${deploy_dir}/secrets"
  if [[ ! -s "${deploy_dir}/secrets/health-auth.env" ]]; then
    secret=$(generate_secret "${MOOX_HEALTH_SECRET_CLI:-${deploy_dir}/bin/moox-admin-cli}" health)
    umask 077
    printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "${secret}" >"${deploy_dir}/secrets/health-auth.env"
  fi
  chmod 0600 "${deploy_dir}/secrets/health-auth.env"
  if [[ "${WITH_ADMIN}" -eq 1 && ! -s "${deploy_dir}/secrets/admin-jwt.env" ]]; then
    umask 077
    printf 'MOOX_ADMIN_JWT_SECRET_KEY=%s\n' "$(generate_secret "${MOOX_SECURITY_SECRET_CLI:-${MOOX_HEALTH_SECRET_CLI:-${deploy_dir}/bin/moox-admin-cli}}" admin-jwt)" >"${deploy_dir}/secrets/admin-jwt.env"
  fi
  [[ "${WITH_ADMIN}" -eq 0 ]] || chmod 0600 "${deploy_dir}/secrets/admin-jwt.env"
  log "deployed to ${deploy_dir}"

  local key_file="${HOME}/.config/moox/credentials/admin-encryption-key"
  if [[ "${WITH_ADMIN}" -eq 1 && ! -f "${key_file}" ]]; then
    mkdir -p "${HOME}/.config/moox/credentials"
    if [[ -f "${deploy_dir}/data/admin.db" ]]; then
      fail "Admin DB exists but ${key_file} is missing"
    fi
    umask 077; head -c 32 /dev/urandom | base64 | tr -d '\n' > "${key_file}"; chmod 600 "${key_file}"
  fi

  if [[ "${NO_START}" -eq 0 ]]; then
    if [[ -n "${PUBLIC_HOST}" ]]; then
      local caddy_ports="${SERVICE_HTTPS_PORT}"
      [[ "${WITH_ADMIN}" -eq 0 ]] || caddy_ports="${BROWSER_HTTPS_PORT},${SERVICE_HTTPS_PORT}"
      MOOX_PUBLIC_HOST="${PUBLIC_HOST}" MOOX_BROWSER_HTTPS_PORT="${BROWSER_HTTPS_PORT}" MOOX_SERVICE_HTTPS_PORT="${SERVICE_HTTPS_PORT}" \
        MOOX_CADDY_CHECKSUMS="${deploy_dir}/lib/caddy-v2.11.4-checksums.txt" \
        MOOX_CADDY_ARCHIVE="${deploy_dir}/lib/caddy_2.11.4_$([[ "${TARGET_GOOS}" == darwin ]] && printf mac || printf '%s' "${TARGET_GOOS}")_${TARGET_GOARCH}.tar.gz" \
        "${deploy_dir}/lib/caddy-managed.sh" ensure --deploy-dir "${deploy_dir}" --os "${TARGET_GOOS}" --arch "${TARGET_GOARCH}" --ports "${caddy_ports}" --config "${deploy_dir}/config/caddy/Caddyfile.next"
    fi
    "${deploy_dir}/start.sh"
  fi
}

sync_remote_stage() {
  local archive remote_archive
  umask 077
  LOCAL_DEPLOY_ARCHIVE=$(mktemp "${TMPDIR:-/tmp}/moox-deploy-${TARGET_GOOS}-${TARGET_GOARCH}.XXXXXX")
  archive="${LOCAL_DEPLOY_ARCHIVE}"
  tar -C "${STAGE_DIR}" -czf "${archive}" .
  chmod 0600 "${archive}"

  remote_archive=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" \
    'umask 077; archive=$(mktemp /tmp/moox-deploy.XXXXXX); chmod 0600 "$archive"; printf "%s\n" "$archive"')
  [[ "${remote_archive}" =~ ^/tmp/moox-deploy\.[A-Za-z0-9]+$ ]] || \
    fail "remote host returned an invalid deployment archive path"
  REMOTE_DEPLOY_ARCHIVE="${remote_archive}"
  log "upload secure deployment archive to ${TARGET}:${remote_archive}"
  scp -p "${archive}" "${TARGET}:${remote_archive}"
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" "chmod 0600 -- $(shell_quote "${remote_archive}")"

  local quoted_dir quoted_archive quoted_no_start quoted_with_storage quoted_with_storage_shard quoted_with_archive quoted_with_eventbus quoted_with_cloudnode quoted_with_collector quoted_with_factor quoted_with_strategy quoted_with_monitor quoted_with_web_host quoted_with_admin quoted_reset_data quoted_metrics_metadata_url quoted_metrics_route_seed quoted_host_route_seed quoted_eventbus_url quoted_metrics_eventbus_url quoted_public_host quoted_browser_https_port quoted_service_https_port quoted_target_goos quoted_target_goarch
  quoted_dir="$(shell_quote "${DEPLOY_DIR}")"
  quoted_archive="$(shell_quote "${remote_archive}")"
  quoted_no_start="$(shell_quote "${NO_START}")"
  quoted_with_storage="$(shell_quote "${WITH_STORAGE}")"
  quoted_with_storage_shard="$(shell_quote "${WITH_STORAGE_SHARD}")"
  quoted_with_archive="$(shell_quote "${WITH_ARCHIVE}")"
  quoted_with_eventbus="$(shell_quote "${WITH_EVENTBUS}")"
  quoted_with_cloudnode="$(shell_quote "${WITH_CLOUDNODE}")"
  quoted_with_collector="$(shell_quote "${WITH_COLLECTOR}")"
  quoted_with_factor="$(shell_quote "${WITH_FACTOR}")"
  quoted_with_strategy="$(shell_quote "${WITH_STRATEGY}")"
  quoted_with_monitor="$(shell_quote "${WITH_MONITOR}")"
  quoted_with_web_host="$(shell_quote "${WITH_WEB_HOST}")"
  quoted_with_admin="$(shell_quote "${WITH_ADMIN}")"
  quoted_reset_data="$(shell_quote "${RESET_DATA}")"
  quoted_metrics_metadata_url="$(shell_quote "${METRICS_METADATA_URL}")"
  quoted_metrics_route_seed="$(shell_quote "${METRICS_ROUTE_SEED}")"
  quoted_host_route_seed="$(shell_quote "${HOST_ROUTE_SEED}")"
  quoted_eventbus_url="$(shell_quote "${EVENTBUS_URL_ENV}")"
  quoted_metrics_eventbus_url="$(shell_quote "${METRICS_EVENTBUS_URL_ENV}")"
  quoted_public_host="$(shell_quote "${PUBLIC_HOST}")"
  quoted_browser_https_port="$(shell_quote "${BROWSER_HTTPS_PORT}")"
  quoted_service_https_port="$(shell_quote "${SERVICE_HTTPS_PORT}")"
  quoted_target_goos="$(shell_quote "${TARGET_GOOS}")"
  quoted_target_goarch="$(shell_quote "${TARGET_GOARCH}")"

  ssh "${TARGET}" "DEPLOY_DIR=${quoted_dir} ARCHIVE=${quoted_archive} NO_START=${quoted_no_start} WITH_STORAGE=${quoted_with_storage} WITH_STORAGE_SHARD=${quoted_with_storage_shard} WITH_ARCHIVE=${quoted_with_archive} WITH_EVENTBUS=${quoted_with_eventbus} WITH_CLOUDNODE=${quoted_with_cloudnode} WITH_COLLECTOR=${quoted_with_collector} WITH_FACTOR=${quoted_with_factor} WITH_STRATEGY=${quoted_with_strategy} WITH_MONITOR=${quoted_with_monitor} WITH_WEB_HOST=${quoted_with_web_host} WITH_ADMIN=${quoted_with_admin} RESET_DATA=${quoted_reset_data} MOOX_METRICS_STORAGE_METADATA_URL=${quoted_metrics_metadata_url} MOOX_METRICS_STORAGE_ROUTE_SEED=${quoted_metrics_route_seed} MOOX_HOST_STORAGE_ROUTE_SEED=${quoted_host_route_seed} MOOX_EVENTBUS_NATS_URL=${quoted_eventbus_url} MOOX_METRICS_EVENTBUS_URL=${quoted_metrics_eventbus_url} PUBLIC_HOST=${quoted_public_host} BROWSER_HTTPS_PORT=${quoted_browser_https_port} SERVICE_HTTPS_PORT=${quoted_service_https_port} TARGET_GOOS=${quoted_target_goos} TARGET_GOARCH=${quoted_target_goarch} bash -s" <<'EOF'
set -euo pipefail

generate_secret() {
  local purpose="$1" output secret
  if [[ ! -x "${DEPLOY_DIR}/bin/moox-admin-cli" ]]; then
    openssl rand -hex 32
    return
  fi
  output=$("${DEPLOY_DIR}/bin/moox-admin-cli" random-secret --bytes 32)
  secret=$(printf '%s' "${output}" | sed -n 's/.*"secret"[[:space:]]*:[[:space:]]*"\([0-9a-f]*\)".*/\1/p')
  [[ -n "${secret}" ]] || secret=$(printf '%s' "${output}" | tr -d '\r\n')
  [[ "${secret}" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid ${purpose} secret" >&2; exit 1; }
  printf '%s' "${secret}"
}

if [[ "${DEPLOY_DIR}" == "~" ]]; then
  DEPLOY_DIR="${HOME}"
elif [[ "${DEPLOY_DIR}" == "~/"* ]]; then
  DEPLOY_DIR="${HOME}/${DEPLOY_DIR#\~/}"
fi

mkdir -p "${DEPLOY_DIR}"
CADDY_DATA_TMP=""
KEY_FILE="${HOME}/.config/moox/credentials/admin-encryption-key"
if [[ "${WITH_ADMIN}" == "1" && ! -f "${KEY_FILE}" ]]; then
  mkdir -p "${HOME}/.config/moox/credentials"
  if [[ -f "${DEPLOY_DIR}/data/admin.db" ]]; then echo "Admin DB exists but encryption key is missing" >&2; exit 1; fi
  umask 077; head -c 32 /dev/urandom | base64 | tr -d '\n' > "${KEY_FILE}"; chmod 600 "${KEY_FILE}"
fi
if [[ -e "${DEPLOY_DIR}/config/caddy/edge.env" || -e "${DEPLOY_DIR}/config/caddy/Caddyfile" || -e "${DEPLOY_DIR}/run/caddy.pid" ]]; then
  if [[ "${NO_START}" -eq 1 ]]; then
    echo "--no-start refuses to replace an existing managed Caddy deployment" >&2
    exit 1
  fi
  if [[ -z "${PUBLIC_HOST}" ]]; then
    echo "existing managed Caddy deployment requires --public-host" >&2
    exit 1
  fi
fi
if [[ -x "${DEPLOY_DIR}/stop.sh" && "${NO_START}" -eq 0 ]]; then
  if [[ "${WITH_STORAGE}" == "1" ]]; then
    MOOX_WITH_EVENTBUS="${WITH_EVENTBUS}" MOOX_WITH_ARCHIVE="${WITH_ARCHIVE}" "${DEPLOY_DIR}/stop.sh" || true
  else
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_COLLECTOR}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" collector || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_FACTOR}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" factor || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_STRATEGY}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" strategy || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_MONITOR}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" monitor || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_CLOUDNODE}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" cloudnode || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_EVENTBUS}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" eventbus || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_WEB_HOST}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" web-host || true
    fi
    "${DEPLOY_DIR}/stop.sh" admin || true
  fi
fi

if [[ "${RESET_DATA}" == "1" ]]; then
  if [[ -d "${DEPLOY_DIR}/data/caddy" ]]; then
    CADDY_DATA_TMP=$(mktemp -d /tmp/moox-caddy-data.XXXXXX)
    mv "${DEPLOY_DIR}/data/caddy" "${CADDY_DATA_TMP}/caddy"
  fi
  rm -rf "${DEPLOY_DIR}/data"
  if [[ -n "${CADDY_DATA_TMP}" ]]; then
    mkdir -p "${DEPLOY_DIR}/data"
    mv "${CADDY_DATA_TMP}/caddy" "${DEPLOY_DIR}/data/caddy"
    rmdir "${CADDY_DATA_TMP}"
  fi
fi

rm -rf "${DEPLOY_DIR}/admin" "${DEPLOY_DIR}/gateway" "${DEPLOY_DIR}/examples" \
  "${DEPLOY_DIR}/start.sh" "${DEPLOY_DIR}/stop.sh" "${DEPLOY_DIR}/restart.sh" "${DEPLOY_DIR}/status.sh" "${DEPLOY_DIR}/healthcheck.sh"
rm -f "${DEPLOY_DIR}/bin/moox-admin" "${DEPLOY_DIR}/bin/moox-admin-cli" \
  "${DEPLOY_DIR}/bin/moox-cli" "${DEPLOY_DIR}/bin/moox-gateway" "${DEPLOY_DIR}/bin/moox-gateway-cli"
if [[ "${WITH_ARCHIVE}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/archive"
  rm -f "${DEPLOY_DIR}/bin/moox-archive" "${DEPLOY_DIR}/bin/moox-archive-cli"
fi
if [[ "${WITH_EVENTBUS}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/eventbus"
  rm -f "${DEPLOY_DIR}/bin/moox-eventbus"
fi
if [[ "${WITH_WEB_HOST}" == "1" ]]; then
  rm -f "${DEPLOY_DIR}/bin/moox-web-host"
fi
if [[ "${WITH_MONITOR}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/monitor"
  rm -f "${DEPLOY_DIR}/bin/moox-monitor" "${DEPLOY_DIR}/bin/moox-monitor-cli"
fi
if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/cloudnode"
  rm -f "${DEPLOY_DIR}/bin/moox-cloudnode" "${DEPLOY_DIR}/bin/moox-cloudnode-cli"
fi
if [[ "${WITH_COLLECTOR}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/collector"
  rm -f "${DEPLOY_DIR}/bin/moox-collector" "${DEPLOY_DIR}/bin/moox-collector-cli" "${DEPLOY_DIR}/bin/moox-collector-scf"
fi
if [[ "${WITH_FACTOR}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/factor"
  rm -f "${DEPLOY_DIR}/bin/moox-factor" "${DEPLOY_DIR}/bin/moox-factor-cli"
fi
if [[ "${WITH_STRATEGY}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/strategy"
  rm -f "${DEPLOY_DIR}/bin/moox-strategy" "${DEPLOY_DIR}/bin/moox-strategy-cli"
fi
if [[ "${WITH_STORAGE}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/storage" "${DEPLOY_DIR}/storage-view" "${DEPLOY_DIR}/storage-shard"
  rm -f "${DEPLOY_DIR}/bin/moox-storage" "${DEPLOY_DIR}/bin/moox-storage-cli" \
    "${DEPLOY_DIR}/bin/moox-storage-primary" \
    "${DEPLOY_DIR}/bin/moox-storage-view" \
    "${DEPLOY_DIR}/bin/moox-storage-shard"
fi
tar -C "${DEPLOY_DIR}" -xzf "${ARCHIVE}"
rm -f "${ARCHIVE}"
if [[ "${WITH_ADMIN}" == "0" ]]; then
  rm -f "${DEPLOY_DIR}/secrets/admin-jwt.env"
fi
chmod +x "${DEPLOY_DIR}/start.sh" "${DEPLOY_DIR}/stop.sh" "${DEPLOY_DIR}/status.sh" "${DEPLOY_DIR}/healthcheck.sh" "${DEPLOY_DIR}/bin/"*
mkdir -p "${DEPLOY_DIR}/secrets"
if [[ ! -s "${DEPLOY_DIR}/secrets/health-auth.env" ]]; then
  secret=$(generate_secret health)
  umask 077
  printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "${secret}" >"${DEPLOY_DIR}/secrets/health-auth.env"
fi
chmod 0600 "${DEPLOY_DIR}/secrets/health-auth.env"
if [[ "${WITH_ADMIN}" == "1" && ! -s "${DEPLOY_DIR}/secrets/admin-jwt.env" ]]; then
  umask 077
  printf 'MOOX_ADMIN_JWT_SECRET_KEY=%s\n' "$(generate_secret admin-jwt)" >"${DEPLOY_DIR}/secrets/admin-jwt.env"
fi
chmod 0600 "${DEPLOY_DIR}/secrets/gateway-control.env" "${DEPLOY_DIR}/secrets/gateway-service.env" "${DEPLOY_DIR}/secrets/gateway-control.key" "${DEPLOY_DIR}/secrets/gateway-service.key"
[[ "${WITH_ADMIN}" == "0" ]] || chmod 0600 "${DEPLOY_DIR}/secrets/admin-jwt.env"

  if [[ "${NO_START}" -eq 0 ]]; then
  if [[ -n "${PUBLIC_HOST}" ]]; then
    CADDY_OS_NAME="${TARGET_GOOS}"
    [[ "${CADDY_OS_NAME}" != darwin ]] || CADDY_OS_NAME=mac
    CADDY_PORTS="${SERVICE_HTTPS_PORT}"
    [[ "${WITH_ADMIN}" == "0" ]] || CADDY_PORTS="${BROWSER_HTTPS_PORT},${SERVICE_HTTPS_PORT}"
    MOOX_PUBLIC_HOST="${PUBLIC_HOST}" MOOX_BROWSER_HTTPS_PORT="${BROWSER_HTTPS_PORT}" MOOX_SERVICE_HTTPS_PORT="${SERVICE_HTTPS_PORT}" \
      MOOX_CADDY_CHECKSUMS="${DEPLOY_DIR}/lib/caddy-v2.11.4-checksums.txt" \
      MOOX_CADDY_ARCHIVE="${DEPLOY_DIR}/lib/caddy_2.11.4_${CADDY_OS_NAME}_${TARGET_GOARCH}.tar.gz" \
      "${DEPLOY_DIR}/lib/caddy-managed.sh" ensure --deploy-dir "${DEPLOY_DIR}" --os "${TARGET_GOOS}" --arch "${TARGET_GOARCH}" --ports "${CADDY_PORTS}" --config "${DEPLOY_DIR}/config/caddy/Caddyfile.next"
  fi
  "${DEPLOY_DIR}/start.sh"
fi
EOF
  log "deployed to ${TARGET}:${DEPLOY_DIR}"
  rm -f "${LOCAL_DEPLOY_ARCHIVE}"
  LOCAL_DEPLOY_ARCHIVE=""
  REMOTE_DEPLOY_ARCHIVE=""
}

log "target=${TARGET} dir=${DEPLOY_DIR} platform=${TARGET_GOOS}/${TARGET_GOARCH}"
build_core_binaries
build_web_host_binary
acquire_stage_deploy_lock
prepare_stage
prepare_cls_preflight

if [[ "${PACKAGE_ONLY}" -eq 1 ]]; then
  umask 077
  tar -C "${STAGE_DIR}" -czf "${PACKAGE_ARCHIVE}" .
  chmod 0600 "${PACKAGE_ARCHIVE}"
  log "wrote deployment archive ${PACKAGE_ARCHIVE}"
  exit 0
fi

if is_local_target; then
  sync_local_stage
else
  sync_remote_stage
fi

configure_target_ca() {
  [[ -n "${PUBLIC_HOST}" && "${NO_START}" -eq 0 && "${TARGET_CA}" != skip ]] || return 0
  local args=(install-target --target "${TARGET}" --deploy-dir "${DEPLOY_DIR}")
  local status
  args+=(--non-interactive)
  set +e
  "${ROOT}/skills/moox/scripts/caddy-ca.sh" "${args[@]}"
  status=$?
  set -e
  case "${status}" in
    0) return 0 ;;
    77)
      log "target CA trust installation needs elevated permission; backend clients still use the deployed CA file"
      return 0
      ;;
    *) fail "target CA trust installation failed with status ${status}" ;;
  esac
}

configure_target_ca

configure_local_ca() {
  [[ -n "${PUBLIC_HOST}" && "${NO_START}" -eq 0 && "${LOCAL_CA}" != skip ]] || return 0
  local output source status
  output="${LOCAL_CA_OUTPUT:-$(default_local_ca_output)}"
  output="$(expand_local_path "${output}")"
  mkdir -p "$(dirname "${output}")"
  if is_local_target; then
    source="$(expand_local_path "${DEPLOY_DIR}")/certs/caddy/root.crt"
    cp "${source}" "${output}"
  else
    scp -o BatchMode=yes "${TARGET}:$(shell_quote "${DEPLOY_DIR%/}/certs/caddy/root.crt")" "${output}"
  fi
  chmod 0644 "${output}"
  FETCHED_CA_FILE="${output}"
  "${ROOT}/skills/moox/scripts/caddy-ca.sh" inspect --ca-file "${output}"
  if "${ROOT}/scripts/install-caddy-ca.sh" --ca-file "${output}" --check >/dev/null 2>&1; then
    log "local CA already trusted: ${output}"
    return 0
  fi

  log "local CA is not trusted; installing: ${output}"
  set +e
  if [[ -t 0 ]]; then
    "${ROOT}/scripts/install-caddy-ca.sh" --ca-file "${output}"
  else
    MOOX_CA_SUDO_NONINTERACTIVE=1 "${ROOT}/scripts/install-caddy-ca.sh" --ca-file "${output}"
  fi
  status=$?
  set -e
  case "${status}" in
    0) log "local CA trust installed: ${output}" ;;
    77) fail "local CA is not trusted and automatic installation needs local administrator permission; run scripts/moox/scripts/caddy-ca.sh install --ca-file ${output}, or use --local-ca skip explicitly" ;;
    *) fail "local CA installation failed with status ${status}" ;;
  esac
}

configure_local_ca

verify_public_https() {
  [[ -n "${PUBLIC_HOST}" && "${NO_START}" -eq 0 ]] || return 0
  local browser="https://${PUBLIC_HOST}:${BROWSER_HTTPS_PORT}"
  local service="https://${PUBLIC_HOST}:${SERVICE_HTTPS_PORT}"
  local verify_script='set -euo pipefail
ca=$1; browser=$2; service=$3; service_auth_file=$4; with_admin=$5; node_id=$6
case "$ca" in "~/"*) ca="$HOME/${ca#\~/}";; esac
case "$service_auth_file" in "~/"*) service_auth_file="$HOME/${service_auth_file#\~/}";; esac
browser_authority=${browser#https://}; service_authority=${service#https://}
status() {
  curl --silent --show-error --max-time 5 --cacert "$ca" \
    --resolve "$browser_authority:127.0.0.1" --resolve "$service_authority:127.0.0.1" \
    --output /dev/null --write-out "%{http_code}" "$@"
}
expect_status() {
  expected=$1; shift
  for _ in $(seq 1 30); do
    actual=$(status "$@" 2>/dev/null || true)
    [[ "$actual" == "$expected" ]] && return 0
    sleep 1
  done
  printf "expected HTTP %s, got %s for %s\n" "$expected" "${actual:-curl-error}" "$*" >&2
  return 1
}
if [[ "$with_admin" == 1 ]]; then
  expect_status 200 "$browser/"
  expect_status 404 "$browser/healthz"
  expect_status 404 "$browser/api/service/test/Ping"
fi
expect_status 404 "$service/api/admin/auth/Login"
path=/api/service/sysdeploy/ListActiveServiceDeployments
expect_status 401 -X POST -H "Content-Type: application/json" --data "{}" "$service$path"
set -a; source "$service_auth_file"; set +a
timestamp=$(date +%s); nonce=$(openssl rand -hex 32); body_hash=$(printf "{}" | openssl dgst -sha256 | awk "{print \$NF}")
canonical=$(printf "moox-gateway-auth-v1\nPOST\n%s\n%s\n%s\n%s\n%s" "$path" "$body_hash" "$timestamp" "$nonce" "$node_id")
signature=$(printf %s "$canonical" | openssl dgst -sha256 -hmac "$MOOX_GATEWAY_SERVICE_SECRET_KEY" | awk "{print \$NF}")
expected=404; [[ "$with_admin" == 0 ]] || expected=200
expect_status "$expected" -X POST -H "Content-Type: application/json" \
  -H "X-Moox-Key-Id: $MOOX_GATEWAY_SERVICE_KEY_ID" -H "X-Moox-Timestamp: $timestamp" \
  -H "X-Moox-Nonce: $nonce" -H "X-Moox-Target-Node: $node_id" -H "X-Moox-Signature: $signature" \
  --data "{}" "$service$path"'
  if is_local_target; then
    bash -c "${verify_script}" _ "$(expand_local_path "${DEPLOY_DIR}")/certs/caddy/root.crt" "${browser}" "${service}" "$(expand_local_path "${DEPLOY_DIR}")/secrets/gateway-service.env" "${WITH_ADMIN}" "${NODE_ID}" || {
      "$(expand_local_path "${DEPLOY_DIR}")/lib/caddy-managed.sh" rollback --deploy-dir "$(expand_local_path "${DEPLOY_DIR}")" || true
      fail "public HTTPS acceptance failed"
    }
  elif [[ -n "${FETCHED_CA_FILE}" ]]; then
    ssh -o BatchMode=yes "${TARGET}" bash -s -- "${DEPLOY_DIR%/}/certs/caddy/root.crt" "${browser}" "${service}" "${DEPLOY_DIR%/}/secrets/gateway-service.env" "${WITH_ADMIN}" "${NODE_ID}" <<<"${verify_script}" || {
      ssh -o BatchMode=yes "${TARGET}" "$(shell_quote "${DEPLOY_DIR%/}/lib/caddy-managed.sh") rollback --deploy-dir $(shell_quote "${DEPLOY_DIR}")" || true
      fail "public HTTPS acceptance failed"
    }
  else
    ssh -o BatchMode=yes "${TARGET}" bash -s -- "${DEPLOY_DIR%/}/certs/caddy/root.crt" "${browser}" "${service}" "${DEPLOY_DIR%/}/secrets/gateway-service.env" "${WITH_ADMIN}" "${NODE_ID}" <<<"${verify_script}" || {
      ssh -o BatchMode=yes "${TARGET}" "$(shell_quote "${DEPLOY_DIR%/}/lib/caddy-managed.sh") rollback --deploy-dir $(shell_quote "${DEPLOY_DIR}")" || true
      fail "remote public HTTPS acceptance failed"
    }
  fi
  log "HTTPS acceptance passed: ${browser} and ${service}"
}

verify_public_https

log "done"
