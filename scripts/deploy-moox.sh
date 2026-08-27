#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="localhost"
DEPLOY_DIR="${MOOX_DEPLOY_DIR:-/data/moox}"
STAGE_DIR=""
SKIP_BUILD=0
NO_START=0
PACKAGE_ONLY=0
PACKAGE_ARCHIVE=""
DEPLOY_PROFILE=""
AUTO_GATEWAY_INPUTS=0
WITH_STORAGE=1
WITH_STORAGE_NODE=1
WITH_ARCHIVE=1
WITH_EVENTBUS=1
WITH_WEB_HOST=1
STORAGE_EXTERNAL_LISTEN=0
WITH_CLOUDNODE=1
WITH_COLLECTOR=1
WITH_FACTOR=1
WITH_STRATEGY=1
WITH_TRADE=1
WITH_MONITOR=1
WITH_HOSTAGENT=1
WITH_ADMIN=1
WITH_GATEWAY=1
BUILD_WEB_ASSETS=1
RESET_DATA=0
COMPONENT_OVERLAY=0
TARGET_GOOS=""
TARGET_GOARCH=""
METRICS_METADATA_URL="${MOOX_METRICS_STORAGE_METADATA_URL:-http://127.0.0.1:20200}"
MOOX_EVENTBUS_PORT="${MOOX_EVENTBUS_PORT:-4222}"
if [[ ! "${MOOX_EVENTBUS_PORT}" =~ ^[0-9]+$ ]]; then
  echo "MOOX_EVENTBUS_PORT must be between 1 and 65535" >&2
  exit 2
fi
MOOX_EVENTBUS_PORT="$(printf '%s' "${MOOX_EVENTBUS_PORT}" | sed 's/^0*//')"
MOOX_EVENTBUS_PORT="${MOOX_EVENTBUS_PORT:-0}"
if (( ${#MOOX_EVENTBUS_PORT} > 5 )) ||
  (( 10#${MOOX_EVENTBUS_PORT} < 1 || 10#${MOOX_EVENTBUS_PORT} > 65535 )); then
  echo "MOOX_EVENTBUS_PORT must be between 1 and 65535" >&2
  exit 2
fi
if [[ -n "${MOOX_EVENTBUS_PUBLIC_IP:-}" && "${MOOX_EVENTBUS_ENABLE_TLS:-0}" != "1" ]]; then
  echo "MOOX_EVENTBUS_PUBLIC_IP requires MOOX_EVENTBUS_ENABLE_TLS=1" >&2
  exit 2
fi
if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
  EVENTBUS_SCHEME=tls
else
  EVENTBUS_SCHEME=nats
fi
if [[ -n "${MOOX_EVENTBUS_PUBLIC_IP:-}" ]]; then
  EVENTBUS_URL_ENV="${EVENTBUS_SCHEME}://${MOOX_EVENTBUS_PUBLIC_IP}:${MOOX_EVENTBUS_PORT}"
  MOOX_EVENTBUS_HOST=0.0.0.0
else
  EVENTBUS_URL_ENV="${EVENTBUS_SCHEME}://127.0.0.1:${MOOX_EVENTBUS_PORT}"
  MOOX_EVENTBUS_HOST=127.0.0.1
fi
export MOOX_EVENTBUS_NATS_URL="${EVENTBUS_URL_ENV}" MOOX_EVENTBUS_HOST MOOX_EVENTBUS_PORT
METRICS_EVENTBUS_URL_ENV="${MOOX_METRICS_EVENTBUS_URL:-}"
PUBLIC_HOST=""
TLS_MODE=auto
TLS_MODE_RESOLVED=internal
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
GATEWAY_ROLLBACK_ARCHIVE=""
GATEWAY_ROLLBACK_DEPLOY_DIR=""
GATEWAY_ROLLBACK_ACTIVE=0
REMOTE_GATEWAY_ROLLBACK_PENDING=0

usage() {
  cat <<'EOF'
Usage:
  scripts/deploy-moox.sh [options]

Options:
  --target <localhost|user@host>  Deploy target. Default: localhost.
  --dir <path>                    Deploy directory on target. Default: /data/moox.
  --goos <linux|darwin>           Target OS. Auto-detected by default.
  --goarch <amd64|arm64>          Target arch. Auto-detected by default.
  --stage <path>                  Local staging directory. Default: release/deploy-stage/moox.
  --skip-build                    Reuse binaries from ./bin.
  --no-start                      Deploy package only, do not start services.
  --profile <control|storage>     Package an initial setup deployment unit.
  --package-only                  Build the selected deployment archive without transport or install.
  --archive <path>                Output archive required by --package-only.
  --no-storage                    Do not package/stop/start moox-storage; preserve existing remote storage files.
  --with-storage-node            Package the optional independent DataNode process.
  --no-archive                    Do not package/start moox-archive.
  --with-archive                  Package/start moox-archive (overrides profile default).
  --no-eventbus                   Do not package/stop/start moox-eventbus; preserve existing remote EventBus files.
  --no-web-host                   Do not package/start moox-web-host.
  --no-cloudnode                  Do not package/start moox-cloudnode.
  --no-collector                  Do not package/start moox-collector.
  --no-factor                     Do not package/start moox-factor.
  --with-factor                   Package/start moox-factor (overrides profile default).
  --no-strategy                   Do not package/start moox-strategy.
  --no-trade                      Do not package/start moox-trade.
  --no-monitor                    Do not package/start moox-monitor.
  --no-hostagent                  Do not package/start moox-host-agent.
  --no-gateway                    Do not package/start moox-gateway; use an existing same-host gateway.
  --no-admin                      Build a data-plane node without Admin, browser assets, schema, or credentials.
  --build-web-assets              Rebuild Vue dist and statik assets before building web-host. Default when web-host is enabled.
  --reuse-web-assets              Reuse current embedded statik assets when building web-host.
  --reset-data                    Remove target data directory before deploying. Use when rebuilding from examples.
  --component-overlay             Update selected components in an existing deployment; preserve its control plane and lifecycle.
  --public-host <ip-or-dns>       Certificate SAN and public HTTPS host; enables managed Caddy.
  --tls-mode <auto|public|internal>
                                  TLS issuer. Auto uses public ACME except for private/loopback hosts.
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
  --gateway-ca-bundle <path>      PEM bundle containing the control endpoint Caddy root (required; verified before deploy).
  --gateway-control-key-file <p>  Local 0600 raw cluster control key file (required).
  --gateway-service-key-file <p>  Local 0600 raw cluster service key file (required).
  --monitor-instance-id <id>      Stable Monitor instance ID (required when Monitor is enabled).
  -h, --help                      Show this help.

Examples:
  scripts/deploy-moox.sh --target localhost --dir /data/moox/dev
  scripts/deploy-moox.sh --target user@host --dir /data/moox --goos linux --goarch amd64
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
      WITH_STORAGE_NODE=0
      WITH_ARCHIVE=0
      WITH_EVENTBUS=1
      WITH_CLOUDNODE=1
      WITH_COLLECTOR=1
      WITH_FACTOR=0
      WITH_STRATEGY=1
      WITH_TRADE=1
      WITH_MONITOR=1
      WITH_HOSTAGENT=1
      ;;
    storage)
      WITH_ADMIN=0
      # Keep the storage profile self-contained without adding the Admin process.
      WITH_GATEWAY=1
      WITH_WEB_HOST=0
      WITH_STORAGE=1
      WITH_STORAGE_NODE=1
      WITH_ARCHIVE=0
      WITH_EVENTBUS=0
      WITH_CLOUDNODE=0
      WITH_COLLECTOR=0
      WITH_FACTOR=0
      WITH_STRATEGY=0
      WITH_TRADE=0
      WITH_MONITOR=0
      WITH_HOSTAGENT=0
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
  if declare -F rollback_gateway_on_exit >/dev/null 2>&1; then
    rollback_gateway_on_exit
  fi
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
  if ! output=$("${cli}" random-secret --bytes 32 2>/dev/null); then
    openssl rand -hex 32
    return
  fi
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
      WITH_STORAGE_NODE=0
      shift
      ;;
    --with-storage-node)
      WITH_STORAGE_NODE=1
      shift
      ;;
    --no-archive)
      WITH_ARCHIVE=0
      shift
      ;;
    --with-archive)
      WITH_ARCHIVE=1
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
    --with-factor)
      WITH_FACTOR=1
      shift
      ;;
    --no-strategy)
      WITH_STRATEGY=0
      shift
      ;;
    --no-trade)
      WITH_TRADE=0
      shift
      ;;
    --no-monitor)
      WITH_MONITOR=0
      shift
      ;;
    --no-hostagent)
      WITH_HOSTAGENT=0
      shift
      ;;
    --no-gateway)
      WITH_GATEWAY=0
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
    --component-overlay)
      COMPONENT_OVERLAY=1
      shift
      ;;
    --public-host) PUBLIC_HOST="${2:-}"; shift 2 ;;
    --tls-mode) TLS_MODE="${2:-}"; shift 2 ;;
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

if [[ "${WITH_STORAGE_NODE}" -eq 1 && "${WITH_STORAGE}" -ne 1 ]]; then
  fail "--with-storage-node requires storage to be enabled"
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
[[ "${TLS_MODE}" =~ ^(auto|public|internal)$ ]] || fail '--tls-mode must be auto, public, or internal'

is_local_target() {
  [[ "${TARGET}" == "localhost" || "${TARGET}" == "127.0.0.1" || "${TARGET}" == "::1" ]]
}

gateway_ready_at() {
  local root="$1" timestamp nonce body_hash canonical signature auth
  set -a
  source "${root}/secrets/health-auth.env"
  set +a
  timestamp=$(date +%s)
  nonce=$(openssl rand -hex 32)
  body_hash=$(printf %s "" | openssl dgst -sha256); body_hash=${body_hash##* }
  canonical=$(printf "%s\nGET\n/readyz\n%s\n%s\n%s" moox-request-v1 "${body_hash}" "${timestamp}" "${nonce}")
  signature=$(printf "%s" "${canonical}" | openssl dgst -sha256 -hmac "${MOOX_HEALTH_AUTH_SECRET_KEY}"); signature=${signature##* }
  auth="${MOOX_HEALTH_AUTH_VERSION}/${MOOX_HEALTH_AUTH_ACCESS_KEY}/${timestamp}/${nonce}/${signature}"
  curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: ${auth}" http://127.0.0.1:11012/readyz >/dev/null
}

prepare_local_gateway_rollback() {
  [[ "${WITH_GATEWAY}" == "1" && "${NO_START}" == "0" && "${COMPONENT_OVERLAY}" == "1" ]] || return 0
  local deploy_dir="$1"
  [[ -d "${deploy_dir}/gateway" ]] || return 0
  # Keep the archive beside the deployment: local rsync uses --delete and
  # must not remove the only copy before an acceptance failure can restore it.
  GATEWAY_ROLLBACK_DEPLOY_DIR="${deploy_dir}"
  GATEWAY_ROLLBACK_ARCHIVE="${deploy_dir}.gateway-rollback.$$"
  rm -f "${GATEWAY_ROLLBACK_ARCHIVE}"
  local entries=(gateway)
  [[ -f "${deploy_dir}/bin/moox-gateway" ]] && entries+=(bin/moox-gateway)
  [[ -f "${deploy_dir}/bin/moox-gateway-cli" ]] && entries+=(bin/moox-gateway-cli)
  tar -C "${deploy_dir}" -czf "${GATEWAY_ROLLBACK_ARCHIVE}" "${entries[@]}"
  chmod 0600 "${GATEWAY_ROLLBACK_ARCHIVE}"
  GATEWAY_ROLLBACK_ACTIVE=1
}

rollback_local_gateway() {
  [[ "${GATEWAY_ROLLBACK_ACTIVE}" == "1" && -n "${GATEWAY_ROLLBACK_ARCHIVE}" && -s "${GATEWAY_ROLLBACK_ARCHIVE}" ]] || return 0
  local deploy_dir="${GATEWAY_ROLLBACK_DEPLOY_DIR}" status=0
  set +e
  if [[ -x "${deploy_dir}/stop.sh" ]]; then
    MOOX_WITH_GATEWAY=1 "${deploy_dir}/stop.sh" gateway >/dev/null 2>&1 || true
  fi
  rm -rf "${deploy_dir}/gateway"
  rm -f "${deploy_dir}/bin/moox-gateway" "${deploy_dir}/bin/moox-gateway-cli"
  tar -C "${deploy_dir}" -xzf "${GATEWAY_ROLLBACK_ARCHIVE}" || status=$?
  if [[ "${status}" -eq 0 && -x "${deploy_dir}/start.sh" ]]; then
    MOOX_WITH_GATEWAY=1 "${deploy_dir}/start.sh" gateway >/dev/null 2>&1 || status=$?
  fi
  if [[ "${status}" -eq 0 ]]; then
    for _ in $(seq 1 30); do
      if gateway_ready_at "${deploy_dir}" >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
    gateway_ready_at "${deploy_dir}" >/dev/null 2>&1 || status=$?
  fi
  if [[ "${status}" -eq 0 ]]; then
    rm -f "${GATEWAY_ROLLBACK_ARCHIVE}"
    GATEWAY_ROLLBACK_ACTIVE=0
  else
    printf '[deploy-moox] ERROR: Gateway rollback failed; preserve %s for manual recovery\n' "${GATEWAY_ROLLBACK_ARCHIVE}" >&2
  fi
  set -e
  return "${status}"
}

finalize_local_gateway_rollback() {
  [[ "${GATEWAY_ROLLBACK_ACTIVE}" == "1" ]] || return 0
  rm -f "${GATEWAY_ROLLBACK_ARCHIVE}"
  GATEWAY_ROLLBACK_ACTIVE=0
}

rollback_remote_gateway() {
  [[ "${REMOTE_GATEWAY_ROLLBACK_PENDING}" == "1" ]] || return 0
  local quoted_dir status=0
  quoted_dir="$(shell_quote "${DEPLOY_DIR%/}")"
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" "bash -s -- ${quoted_dir}" <<'EOF' >/dev/null 2>&1 || status=$?
set +e
root="$1"
archive="$root/.gateway-rollback.tgz"
[[ -s "$archive" ]] || exit 1
if [[ -x "$root/stop.sh" ]]; then MOOX_WITH_GATEWAY=1 "$root/stop.sh" gateway >/dev/null 2>&1 || true; fi
rm -rf "$root/gateway"
rm -f "$root/bin/moox-gateway" "$root/bin/moox-gateway-cli"
tar -C "$root" -xzf "$archive" || exit 1
if [[ -x "$root/start.sh" ]]; then MOOX_WITH_GATEWAY=1 "$root/start.sh" gateway >/dev/null 2>&1 || exit 1; fi
set -a
source "$root/secrets/health-auth.env"
set +a
for _ in $(seq 1 30); do
  timestamp=$(date +%s); nonce=$(openssl rand -hex 32)
  body_hash=$(printf %s "" | openssl dgst -sha256); body_hash=${body_hash##* }
  canonical=$(printf "%s\nGET\n/readyz\n%s\n%s\n%s" moox-request-v1 "$body_hash" "$timestamp" "$nonce")
  signature=$(printf "%s" "$canonical" | openssl dgst -sha256 -hmac "$MOOX_HEALTH_AUTH_SECRET_KEY"); signature=${signature##* }
  auth="$MOOX_HEALTH_AUTH_VERSION/$MOOX_HEALTH_AUTH_ACCESS_KEY/$timestamp/$nonce/$signature"
  curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $auth" http://127.0.0.1:11012/readyz >/dev/null 2>&1 && break
  sleep 1
done
curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $auth" http://127.0.0.1:11012/readyz >/dev/null 2>&1 || exit 1
rm -f "$archive"
EOF
  if [[ "${status}" -ne 0 ]]; then
    printf '[deploy-moox] ERROR: remote Gateway rollback failed; preserve %s/.gateway-rollback.tgz for manual recovery\n' "${DEPLOY_DIR%/}" >&2
    return "${status}"
  fi
  REMOTE_GATEWAY_ROLLBACK_PENDING=0
}

rollback_gateway_on_exit() {
  local status=$?
  if [[ "${GATEWAY_ROLLBACK_ACTIVE}" == "1" ]] && ! rollback_local_gateway; then
    printf '[deploy-moox] ERROR: local Gateway rollback did not complete\n' >&2
  fi
  if [[ "${REMOTE_GATEWAY_ROLLBACK_PENDING}" == "1" ]] && ! rollback_remote_gateway; then
    printf '[deploy-moox] ERROR: remote Gateway rollback did not complete\n' >&2
  fi
  # EXIT traps preserve the original process status; returning success here
  # lets the rest of cleanup remove transport archives and locks.
  return 0
}

# A remote Collector fleet is reached by Tencent SCF, not by the target host's
# loopback interface.  Refuse to produce a deployment which would publish a
# loopback-only EventBus and leave the failure to surface later as completion
# timeouts.  Operators can explicitly opt out for a private/VPN topology, but
# that escape hatch is intentionally named and never enabled implicitly.
validate_eventbus_governance() {
  [[ "${WITH_EVENTBUS}" == "1" && "${WITH_COLLECTOR}" == "1" ]] || return 0
  is_local_target && return 0
  [[ "${MOOX_EVENTBUS_ALLOW_LOOPBACK_REMOTE:-0}" == "1" ]] && return 0
  local public_ip="${MOOX_EVENTBUS_PUBLIC_IP:-}" public_ip_lc
  [[ -n "${public_ip}" ]] ||
    fail "remote Collector deployment requires MOOX_EVENTBUS_PUBLIC_IP; refusing loopback-only EventBus"
  public_ip_lc="$(printf '%s' "${public_ip}" | tr '[:upper:]' '[:lower:]')"
  case "${public_ip_lc}" in
    0.0.0.0|::|\[::\]|127.*|localhost|localhost.|::1|\[::1\]|::ffff:127.*|\[::ffff:127.*\])
      fail "remote Collector deployment cannot use loopback EventBus address ${public_ip}"
      ;;
  esac
  [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]] ||
    fail "remote Collector deployment requires MOOX_EVENTBUS_ENABLE_TLS=1"
}

validate_eventbus_governance

resolve_tls_mode() {
  if [[ "${TLS_MODE}" != auto ]]; then
    printf '%s\n' "${TLS_MODE}"
    return
  fi
  local host octet1 octet2 octet3 octet4
  host=$(printf '%s' "${PUBLIC_HOST}" | tr '[:upper:]' '[:lower:]')
  case "${host}" in
    ""|localhost|*.localhost|::1|fc*:*|fd*:*|fe8*:*|fe9*:*|fea*:*|feb*:*)
      printf 'internal\n'
      return
      ;;
  esac
  if [[ "${host}" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]]; then
    octet1=${BASH_REMATCH[1]}
    octet2=${BASH_REMATCH[2]}
    octet3=${BASH_REMATCH[3]}
    octet4=${BASH_REMATCH[4]}
    if ((10#${octet1} <= 255 && 10#${octet2} <= 255 && 10#${octet3} <= 255 && 10#${octet4} <= 255)); then
      if ((10#${octet1} == 10 || 10#${octet1} == 127 || \
          (10#${octet1} == 169 && 10#${octet2} == 254) || \
          (10#${octet1} == 172 && 10#${octet2} >= 16 && 10#${octet2} <= 31) || \
          (10#${octet1} == 192 && 10#${octet2} == 168))); then
        printf 'internal\n'
        return
      fi
    fi
  fi
  printf 'public\n'
}

remove_resettable_data() {
  local data_dir="$1"
  [[ -d "${data_dir}" ]] || return 0
  find "${data_dir}" -mindepth 1 -maxdepth 1 ! -name caddy -exec rm -rf -- {} +
}

TLS_MODE_RESOLVED="$(resolve_tls_mode)"

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

validate_gateway_control_endpoint() {
  [[ "${GATEWAY_CONTROL_URL}" == https://* ]] || return 0
  local endpoint="${GATEWAY_CONTROL_URL%/}/"
  # This preflight validates DNS, TCP, and TLS trust only. The control root
  # may legitimately return an application-level 4xx/5xx while Admin is
  # restarting; --fail would misdiagnose that as a bad CA bundle.
  if ! curl --silent --show-error --max-time 5 --cacert "${GATEWAY_CA_BUNDLE}" \
    --output /dev/null "${endpoint}"; then
    fail "cannot establish trusted TLS to --gateway-control-url; verify DNS, endpoint availability, and the Caddy root in --gateway-ca-bundle"
  fi
}

if [[ "${AUTO_GATEWAY_INPUTS}" -eq 0 ]]; then
  validate_gateway_ca_bundle "${GATEWAY_CA_BUNDLE}"
  # A package-only/no-start artifact may be prepared before the control
  # endpoint is reachable. The real activation path performs the live TLS
  # verification and the post-start control-plane acceptance below.
  [[ "${NO_START}" == "1" ]] || validate_gateway_control_endpoint
fi

HOST_GOOS="$(go env GOOS)"
HOST_GOARCH="$(go env GOARCH)"
STAGE_DIR="${STAGE_DIR:-${ROOT}/release/deploy-stage/moox}"

build_core_binaries() {
  if [[ "${SKIP_BUILD}" -eq 1 ]]; then
    log "skip core build; reuse ./bin"
    return
  fi

  log "build core binaries (${TARGET_GOOS}/${TARGET_GOARCH})"
  local cross_storage=0
  if [[ "${WITH_STORAGE}" -eq 1 ]] &&
    [[ "${TARGET_GOOS}" != "${HOST_GOOS}" || "${TARGET_GOARCH}" != "${HOST_GOARCH}" ]]; then
    [[ "${TARGET_GOOS}" == linux ]] || fail "cross-platform Storage build supports only Linux targets"
    [[ "${TARGET_GOARCH}" == amd64 ]] || fail "cross-platform Storage build supports only linux/amd64"
    cross_storage=1
    log "cross build detected; build CGO-enabled Storage on compile host"
    TARGET_GOOS="${HOST_GOOS}" TARGET_GOARCH="${HOST_GOARCH}" \
      "${ROOT}/scripts/build.sh" cli
    "${ROOT}/scripts/build-storage-linux.sh"
  fi

  if [[ "${WITH_STORAGE}" -eq 1 || "${WITH_ADMIN}" -eq 1 || "${WITH_MONITOR}" -eq 1 || "${WITH_HOSTAGENT}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" cli
  fi
  if [[ "${WITH_ADMIN}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" admin
  fi
  if [[ "${WITH_GATEWAY}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" gateway
  fi
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
  fi
  if [[ "${WITH_FACTOR}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" factor
  fi
  if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" strategy
  fi
  if [[ "${WITH_TRADE}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" trade
  fi
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" monitor
  fi
  if [[ "${WITH_HOSTAGENT}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" hostagent
  fi
  if [[ "${WITH_ARCHIVE}" -eq 1 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" archive
  fi

  if [[ "${WITH_STORAGE}" -eq 0 ]]; then
    return 0
  fi
  if [[ "${cross_storage}" -eq 1 ]]; then
    return 0
  fi
  TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
    "${ROOT}/scripts/build.sh" storage
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

storage_binary_sha256() {
  local path="$1"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${path}" | awk '{print $1}'
    return
  fi
  sha256sum "${path}" | awk '{print $1}'
}

write_storage_build_provenance() {
  [[ "${WITH_STORAGE}" -eq 1 ]] || return 0
  local commit="unknown" dirty=true primary_hash node_hash view_hash
  if commit_candidate=$(git -C "${ROOT}" rev-parse HEAD 2>/dev/null) && [[ "${commit_candidate}" =~ ^[0-9a-fA-F]{40}$ ]]; then
    commit="$(printf '%s' "${commit_candidate}" | tr '[:upper:]' '[:lower:]')"
    dirty=false
    if ! git -C "${ROOT}" diff --quiet --ignore-submodules -- || ! git -C "${ROOT}" diff --cached --quiet --ignore-submodules --; then
      dirty=true
    fi
  fi
  primary_hash="$(storage_binary_sha256 "${STAGE_DIR}/bin/moox-storage-primary")"
  node_hash="$(storage_binary_sha256 "${STAGE_DIR}/bin/moox-storage-node")"
  view_hash="$(storage_binary_sha256 "${STAGE_DIR}/bin/moox-storage-view")"
  cat >"${STAGE_DIR}/build-provenance.json" <<EOF
{"schema_version":1,"commit":"${commit}","dirty":${dirty},"binary_hashes":{"moox-storage-primary":"${primary_hash}","moox-storage-node":"${node_hash}","moox-storage-view":"${view_hash}"}}
EOF
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
  # A remote Storage gateway is the native RPC ingress used by short-lived
  # SCF calls. It must not be loopback-only just because CloudNode is absent.
  if [[ "${WITH_GATEWAY}" -eq 1 && ( "${WITH_CLOUDNODE}" -eq 1 || "${WITH_STORAGE}" -eq 1 || -n "${PUBLIC_HOST}" ) ]]; then
    perl -0pi -e 's#native_addr:\s*127\.0\.0\.1:11003#native_addr: 0.0.0.0:11003#; s#health_addr:\s*127\.0\.0\.1:11012#health_addr: 0.0.0.0:11012#' \
      "${STAGE_DIR}/gateway/config/app.yaml"
  fi
  if grep -q '^  ca_file:' "${STAGE_DIR}/gateway/config/app.yaml"; then
    perl -0pi -e 's#^  ca_file:.*#  ca_file: ../../certs/gateway/peers.pem#m' "${STAGE_DIR}/gateway/config/app.yaml"
  else
    perl -0pi -e 's#(  hmac_key_file: ../../secrets/gateway-control\.key\n)#$1  ca_file: ../../certs/gateway/peers.pem\n#' "${STAGE_DIR}/gateway/config/app.yaml"
  fi

  if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/moox_cloudnode\.db#path: ../data/cloudnode/moox_cloudnode.db#g' \
      "${STAGE_DIR}/cloudnode/config/app.yaml"
  fi
  if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/moox_collector\.db#path: ../data/collector/moox_collector.db#g' \
      "${STAGE_DIR}/collector/config/app.yaml"
    # Local collector config disables the timer for dev runs; deployments need it on.
    perl -0pi -e 's#scheduler=collectorSchedule&disable=1&params=[^"]*#scheduler=collectorSchedule&disable=0&params=space_id=crypto_market#g; s#scheduler=collectorSchedule&disable=0&params=(?=")#scheduler=collectorSchedule&disable=0&params=space_id=crypto_market#g' \
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
  if [[ "${WITH_TRADE}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/moox_trade\.db#path: ../data/trade/moox_trade.db#g' \
      "${STAGE_DIR}/trade/config/app.yaml"
    perl -0pi -e 's#log_path:\s*\./log#log_path: ../logs/trade#g' \
      "${STAGE_DIR}/trade/config/trpc_go.yaml"
  fi
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/monitor/monitor\.db#path: ../data/monitor/monitor.db#g' \
      "${STAGE_DIR}/monitor/config/app.yaml"
    if [[ "${COMPONENT_OVERLAY:-0}" -eq 0 && "${WITH_STORAGE}" -eq 0 && "${WITH_EVENTBUS}" -eq 0 ]]; then
      # A full client-only package has no metrics dependencies.  During a
      # component overlay, however, Storage/EventBus may already be installed
      # and must keep its existing metrics ingestion configuration.
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
    EVENTBUS_PORT="${MOOX_EVENTBUS_PORT}" perl -0pi -e \
      's#(^  port:)\s*\d+#$1 $ENV{EVENTBUS_PORT}#m' \
      "${STAGE_DIR}/eventbus/config/app.yaml"
  fi

  if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
    [[ "${WITH_ARCHIVE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/archive-eventbus.yaml#' "${STAGE_DIR}/archive/config/app.yaml"
    [[ "${WITH_CLOUDNODE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/cloudnode-eventbus.yaml#' "${STAGE_DIR}/cloudnode/config/app.yaml"
    [[ "${WITH_FACTOR}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/factor-eventbus.yaml#' "${STAGE_DIR}/factor/config/app.yaml"
    [[ "${WITH_STRATEGY}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/strategy-eventbus.yaml#' "${STAGE_DIR}/strategy/config/app.yaml"
    [[ "${WITH_TRADE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/trade-eventbus.yaml#' "${STAGE_DIR}/trade/config/app.yaml"
    [[ "${WITH_MONITOR}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/monitor-observability.yaml#' "${STAGE_DIR}/monitor/config/app.yaml"
  else
    [[ "${WITH_ARCHIVE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/archive/config/app.yaml"
    [[ "${WITH_CLOUDNODE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/cloudnode/config/app.yaml"
    [[ "${WITH_FACTOR}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/factor/config/app.yaml"
    [[ "${WITH_STRATEGY}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/strategy/config/app.yaml"
    [[ "${WITH_TRADE}" -eq 1 ]] && perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/trade/config/app.yaml"
    if [[ "${WITH_MONITOR}" -eq 1 && "${COMPONENT_OVERLAY:-0}" -eq 0 ]]; then
      perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/monitor/config/app.yaml"
    fi
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
    if [[ "${WITH_STORAGE_NODE}" -eq 1 ]]; then
      perl -pi -e 'if (/^server:/) { $server = 1 } if (/^(client|plugins):/) { $server = 0 } if ($server && /^      ip:\s*127\.0\.0\.1\s*$/) { s#127\.0\.0\.1#0.0.0.0# }' "${STAGE_DIR}/storage-node/config/trpc_go.yaml"
    fi
  fi
  perl -0pi -e 's#log_path:\s*\./logs#log_path: ../logs/storage-primary#g' \
    "${STAGE_DIR}/storage/config/trpc_go.yaml"
  perl -0pi -e 's#log_path:\s*\./logs#log_path: ../logs/storage-view#g' "${view_conf}"
  if [[ "${WITH_STORAGE_NODE}" -eq 1 ]]; then
    perl -0pi -e 's#(service_name:\s*)""#${1}trpc.moox.storage.DataNodeRuntime#g' \
      "${STAGE_DIR}/storage/config/storage.yaml" "${STAGE_DIR}/storage/config/trpc_go.yaml"
    perl -0pi -e 's#root:\s*\./var/storage#root: ../data/storage-node#g; s#pebble_path:\s*\./var/storage/pebble#pebble_path: ../data/storage-node/pebble#g; s#log_path:\s*\./logs#log_path: ../logs/storage-node#g' \
      "${STAGE_DIR}/storage-node/config/trpc_go.yaml" "${STAGE_DIR}/storage-node/config/storage.yaml"
    if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
      perl -0pi -e 's#credential_file:\s*.*#credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml#' "${STAGE_DIR}/storage-node/config/storage.yaml"
    else
      perl -0pi -e 's#credential_file:\s*.*#credential_file: ""#' "${STAGE_DIR}/storage-node/config/storage.yaml"
    fi
  fi
}

write_runtime_scripts() {
  local scf_service_gateway_target="http://127.0.0.1:11002"
  local scf_storage_rpc_gateway_target="ip://127.0.0.1:11003"
  # Storage route retention is an explicit topology decision.  Do not infer
  # it from an omitted profile: a no-storage full deployment may intentionally
  # remove an old local Storage placement, while an independent Storage root
  # must opt in with MOOX_PRESERVE_STORAGE_ROUTES=1.
  local preserve_storage_routes="${MOOX_PRESERVE_STORAGE_ROUTES:-0}"
  if [[ -n "${PUBLIC_HOST}" ]]; then
    scf_service_gateway_target="https://${PUBLIC_HOST}:${SERVICE_HTTPS_PORT}"
    scf_storage_rpc_gateway_target="ip://${PUBLIC_HOST}:11003"
  fi

  # Persist the listener contract in the release itself.  Previously this
  # could be supplied only through an ad-hoc shell environment, so a restart
  # could silently return EventBus to 127.0.0.1 while SCF still used the public
  # address.  Do not persist credentials or the client URL here; start.sh
  # renders the client URL from the deployment's topology and role files.
  cat >"${STAGE_DIR}/config/runtime.env" <<EOF
MOOX_EVENTBUS_HOST=$(printf '%q' "${MOOX_EVENTBUS_HOST}")
MOOX_EVENTBUS_PORT=$(printf '%q' "${MOOX_EVENTBUS_PORT}")
MOOX_EVENTBUS_ENABLE_TLS=$(printf '%q' "${MOOX_EVENTBUS_ENABLE_TLS:-0}")
EOF
  chmod 0600 "${STAGE_DIR}/config/runtime.env"

  cat > "${STAGE_DIR}/start.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -r "${ROOT}/config/components.env" ]]; then
  source "${ROOT}/config/components.env"
fi
if [[ -r "${ROOT}/config/runtime.env" ]]; then
  set -a
  source "${ROOT}/config/runtime.env"
  set +a
fi
if [[ -r "${ROOT}/config/resources.env" ]]; then
  set -a
  source "${ROOT}/config/resources.env"
  set +a
fi
HEALTH_AUTH_FILE="${ROOT}/secrets/health-auth.env"
[[ -r "${HEALTH_AUTH_FILE}" ]] || { echo "missing health credentials: ${HEALTH_AUTH_FILE}" >&2; exit 1; }
[[ -r "${ROOT}/secrets/gateway-control.env" ]] || { echo "missing Gateway control credentials" >&2; exit 1; }
[[ -r "${ROOT}/secrets/gateway-service.env" ]] || { echo "missing Gateway service credentials" >&2; exit 1; }
set -a
source "${ROOT}/secrets/health-auth.env"
if [[ -r "${ROOT}/secrets/storage-node-auth.env" ]]; then
  source "${ROOT}/secrets/storage-node-auth.env"
fi
if [[ -r "${ROOT}/secrets/storage-internal-auth.env" ]]; then
  source "${ROOT}/secrets/storage-internal-auth.env"
fi
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

disable_conflicting_eventbus_supervisor() {
  local runtime_dir unit_exec active_state enabled_state
  # MooX's generated lifecycle scripts are the single owner of packaged
  # services. A leftover user-level systemd unit for the same EventBus binary
  # would restart it immediately after stop_service kills the old process,
  # creating two owners that fight over 4222/12970.
  command -v systemctl >/dev/null 2>&1 || return 0
  runtime_dir="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  [[ -d "${runtime_dir}" ]] || return 0
  unit_exec="$(XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user show -p ExecStart --value moox-eventbus.service 2>/dev/null || true)"
  [[ "${unit_exec}" == *"${ROOT}/bin/moox-eventbus"* ]] || return 0
  echo "disabling conflicting user supervisor for ${ROOT}/bin/moox-eventbus" >&2
  active_state="$(XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user is-active moox-eventbus.service 2>/dev/null || true)"
  if [[ "${active_state}" == "active" || "${active_state}" == "activating" ]]; then
    XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user stop moox-eventbus.service >/dev/null 2>&1 || {
      echo "cannot stop conflicting EventBus user supervisor" >&2
      return 1
    }
  fi
  enabled_state="$(XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user is-enabled moox-eventbus.service 2>/dev/null || true)"
  if [[ "${enabled_state}" == "enabled" || "${enabled_state}" == "enabled-runtime" ]]; then
    XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user disable moox-eventbus.service >/dev/null 2>&1 || {
      echo "cannot disable conflicting EventBus user supervisor" >&2
      return 1
    }
  fi
}

read_env_value() {
  local file="$1" name="$2" value
  value=$(bash -c 'set -u; source "$1"; printf "%s" "${!2-}"' _ "${file}" "${name}")
  [[ -n "${value}" ]] || { echo "missing ${name} in ${file}" >&2; exit 1; }
  printf '%s' "${value}"
}

validate_storage_internal_auth() {
  local file="${ROOT}/secrets/storage-internal-auth.env"
  [[ -r "${file}" ]] || {
    echo "missing shared Storage internal auth file: ${file}" >&2
    exit 1
  }
  local primary_file view_file
  primary_file="$(read_env_value "${file}" MOOX_STORAGE_PRIMARY_AUTH_SECRET)"
  view_file="$(read_env_value "${file}" MOOX_STORAGE_VIEW_AUTH_SECRET)"
  [[ "${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}" == "${primary_file}" ]] || {
    echo "Storage Primary auth secret is not sourced from ${file}; restart the service from start.sh" >&2
    exit 1
  }
  [[ "${MOOX_STORAGE_VIEW_AUTH_SECRET:-}" == "${view_file}" ]] || {
    echo "Storage View auth secret is not sourced from ${file}; restart the service from start.sh" >&2
    exit 1
  }
}

NOTIFICATION_ENV=()
if [[ -r "${ROOT}/secrets/notification.env" ]]; then
  notification_channel_type=$(bash -c 'set -u; source "$1"; printf "%s" "${MOOX_NOTIFICATION_CHANNEL_TYPE-wecom}"' _ "${ROOT}/secrets/notification.env")
  notification_webhook_url=$(bash -c 'set -u; source "$1"; printf "%s" "${MOOX_NOTIFICATION_WEBHOOK_URL-}"' _ "${ROOT}/secrets/notification.env")
  NOTIFICATION_ENV+=("MOOX_NOTIFICATION_CHANNEL_TYPE=${notification_channel_type}" "MOOX_NOTIFICATION_WEBHOOK_URL=${notification_webhook_url}")
fi

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
WITH_STORAGE="${MOOX_WITH_STORAGE:-${MOOX_INSTALLED_WITH_STORAGE:-__WITH_STORAGE__}}"
WITH_STORAGE_NODE="${MOOX_WITH_STORAGE_NODE:-${MOOX_INSTALLED_WITH_STORAGE_NODE:-__WITH_STORAGE_NODE__}}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-${MOOX_INSTALLED_WITH_ARCHIVE:-__WITH_ARCHIVE__}}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-${MOOX_INSTALLED_WITH_EVENTBUS:-__WITH_EVENTBUS__}}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-${MOOX_INSTALLED_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-${MOOX_INSTALLED_WITH_COLLECTOR:-__WITH_COLLECTOR__}}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-${MOOX_INSTALLED_WITH_FACTOR:-__WITH_FACTOR__}}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-${MOOX_INSTALLED_WITH_STRATEGY:-__WITH_STRATEGY__}}"
WITH_TRADE="${MOOX_WITH_TRADE:-${MOOX_INSTALLED_WITH_TRADE:-__WITH_TRADE__}}"
WITH_HOSTAGENT="${MOOX_WITH_HOSTAGENT:-${MOOX_INSTALLED_WITH_HOSTAGENT:-__WITH_HOSTAGENT__}}"
if [[ "${WITH_EVENTBUS}" == "1" && "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
  ADMIN_SECRET_ENV+=("MOOX_EVENTBUS_CA_FILE=${HOME}/.config/moox/eventbus/ca.pem")
fi
WITH_MONITOR="${MOOX_WITH_MONITOR:-${MOOX_INSTALLED_WITH_MONITOR:-__WITH_MONITOR__}}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-${MOOX_INSTALLED_WITH_WEB_HOST:-__WITH_WEB_HOST__}}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-${MOOX_INSTALLED_WITH_ADMIN:-__WITH_ADMIN__}}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-${MOOX_INSTALLED_WITH_GATEWAY:-__WITH_GATEWAY__}}"
PRESERVE_STORAGE_ROUTES="${MOOX_PRESERVE_STORAGE_ROUTES:-__PRESERVE_STORAGE_ROUTES__}"
PUBLIC_HOST="${MOOX_PUBLIC_HOST:-__PUBLIC_HOST__}"
SCF_SERVICE_GATEWAY_TARGET="${MOOX_SCF_SERVICE_GATEWAY_TARGET:-__SCF_SERVICE_GATEWAY_TARGET__}"
SCF_STORAGE_RPC_GATEWAY_TARGET="${MOOX_SCF_STORAGE_RPC_GATEWAY_TARGET:-__SCF_STORAGE_RPC_GATEWAY_TARGET__}"
if [[ "${WITH_STORAGE_NODE}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-node requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_NODE}" == "1" && ! -d "${ROOT}/storage-node" ]]; then
  echo "storage-node is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE}" == "1" && "${WITH_STORAGE_NODE}" != "1" && -d "${ROOT}/storage-node" ]]; then
  echo "storage-node package is present but storage-node is disabled" >&2
  exit 2
fi
if [[ "${WITH_STORAGE}" == "1" || "${WITH_FACTOR}" == "1" || "${WITH_MONITOR}" == "1" ]]; then
  validate_storage_internal_auth
fi
if [[ "${WITH_STORAGE}" == "1" && -z "${MOOX_STORAGE_NODE_AUTH_SECRET:-}" ]]; then
  echo "missing storage DataNode authentication secret" >&2
  exit 1
fi
MOOX_GATEWAY_NODE_ID="${MOOX_GATEWAY_NODE_ID:-__NODE_ID__}"
export MOOX_GATEWAY_NODE_ID
if [[ "${WITH_ADMIN}" == "1" ]]; then
  # Seed routes under the same node identity the Gateway presents when it
  # pulls them. Operators can still separate the control-plane identity by
  # explicitly setting MOOX_RUNTIME_NODE_ID/MOOX_ADMIN_NODE_ID.
  MOOX_RUNTIME_NODE_ID="${MOOX_RUNTIME_NODE_ID:-${MOOX_ADMIN_NODE_ID:-${MOOX_GATEWAY_NODE_ID}}}"
else
  MOOX_RUNTIME_NODE_ID="${MOOX_RUNTIME_NODE_ID:-${MOOX_GATEWAY_NODE_ID}}"
fi
export MOOX_RUNTIME_NODE_ID
LOCAL_STORAGE_RPC_GATEWAY_TARGET="${MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET:-ip://127.0.0.1:11003}"
case "${LOCAL_STORAGE_RPC_GATEWAY_TARGET}" in
  ip://127.0.0.1:*|ip://localhost:*|ip://\[::1\]:*) ;;
  *)
    echo "MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET must use a loopback address" >&2
    exit 1
    ;;
esac
LOCAL_STORAGE_RPC_GATEWAY_ADDRESS="${LOCAL_STORAGE_RPC_GATEWAY_TARGET#ip://}"
LOCAL_STORAGE_GATEWAY_NODE_ID="${MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID:-${MOOX_GATEWAY_NODE_ID}}"
MOOX_MONITOR_INSTANCE_ID="${MOOX_MONITOR_INSTANCE_ID:-__MONITOR_INSTANCE_ID__}"
if [[ "${WITH_ADMIN}" == "1" ]]; then
  # The Admin SysDeploy registry identifies the local control-plane node.  It
  # must not inherit the Gateway routing node when those identities differ.
  MOOX_ADMIN_NODE_ID="${MOOX_ADMIN_NODE_ID:-${MOOX_RUNTIME_NODE_ID}}"
fi
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-3}"
mkdir -p "${ROOT}/run" "${ROOT}/data" "${ROOT}/data/gateway" "${ROOT}/data/eventbus/jetstream" "${ROOT}/data/cloudnode" "${ROOT}/data/cloudnode/jobs" "${ROOT}/data/collector" "${ROOT}/data/factor" "${ROOT}/data/strategy" "${ROOT}/data/trade" "${ROOT}/data/monitor" "${ROOT}/logs/admin" "${ROOT}/logs/gateway" "${ROOT}/logs/eventbus" "${ROOT}/logs/storage" "${ROOT}/logs/storage-primary" "${ROOT}/logs/storage-view" "${ROOT}/logs/web-host" "${ROOT}/logs/cloudnode" "${ROOT}/logs/collector" "${ROOT}/logs/factor" "${ROOT}/logs/strategy" "${ROOT}/logs/trade" "${ROOT}/logs/monitor"
chmod 0700 "${ROOT}/data/gateway"

source "${ROOT}/lib/loopback-listeners.sh"
validate_moox_loopback_listeners
stop_processes_by_binary() {
  local name="$1" expected="${ROOT}/bin/moox-${name}" proc pid exe
  [[ "${name}" == "hostagent" ]] && expected="${ROOT}/bin/moox-host-agent"
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
  [[ "${name}" == "hostagent" ]] && expected="${ROOT}/bin/moox-host-agent"
  command=$(ps -p "${pid}" -o command= 2>/dev/null || true)
  [[ "${command}" == "${expected}" || "${command}" == "${expected} "* ]]
}
stop_if_running() {
  local name="$1"
  local pid_file="${ROOT}/run/${name}.pid"
  local pattern="${ROOT}/bin/moox-${name}([[:space:]]|$)"
  [[ "${name}" == "hostagent" ]] && pattern="${ROOT}/bin/moox-host-agent([[:space:]]|$)"
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

sha256_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${path}" | awk '{print $1}'
  else
    shasum -a 256 "${path}" | awk '{print $1}'
  fi
}

start_service() {
  local name="$1"
  local work_dir="$2"
  shift 2
  local pid_file="${ROOT}/run/${name}.pid"
  local log_file="${ROOT}/logs/${name}/stdout.log"
	local binary="" binary_hash="" candidate
	for candidate in "$@"; do
		if [[ "${candidate}" == "${ROOT}/bin/"* && -f "${candidate}" && -x "${candidate}" ]]; then
			binary="${candidate}"
			break
		fi
	done
	[[ -n "${binary}" ]] || { echo "${name}: executable under ${ROOT}/bin not found in command" >&2; exit 1; }
	binary_hash="$(sha256_file "${binary}")"

  stop_if_running "${name}"
  mkdir -p "$(dirname "${log_file}")"
  echo "starting ${name}"
  (
    cd "${work_dir}"
    nohup env \
      "MOOX_BINARY_SHA256=sha256:${binary_hash}" \
      "MOOX_VERSION=sha256:${binary_hash:0:12}" \
      "$@" >> "${log_file}" 2>&1 &
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
	if [[ -r "/proc/${pid}/exe" ]]; then
		local running_hash
		running_hash="$(sha256_file "/proc/${pid}/exe")"
		if [[ "${running_hash}" != "${binary_hash}" ]]; then
			echo "${name}: running binary hash mismatch expected=${binary_hash} actual=${running_hash}" >&2
			kill "${pid}" 2>/dev/null || true
			exit 1
		fi
	fi
	printf 'sha256:%s\n' "${binary_hash}" >"${ROOT}/run/${name}.binary.sha256"
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
    "MOOX_SERVICE_NAME=${service_name}"
    "MOOX_INSTANCE_ID=${service_name}@${MOOX_RUNTIME_NODE_ID}"
    "MOOX_NODE_ID=${MOOX_RUNTIME_NODE_ID}"
    "MOOX_BOOT_ID=${boot_id}"
  )
  if [[ -n "${PUBLIC_HOST:-}" ]]; then
    RUNTIME_IDENTITY_ENV+=("MOOX_REPORT_IP=${PUBLIC_HOST}")
  fi
  if [[ -n "${config_file}" && -f "${config_file}" ]]; then
    RUNTIME_IDENTITY_ENV+=("MOOX_CONFIG_HASH=sha256:$(shasum -a 256 "${config_file}" | awk '{print $1}')")
  fi
  if [[ "${service_name}" == "moox_monitor" && -f "${ROOT}/config/dataset-health-policy.yaml" ]]; then
    RUNTIME_IDENTITY_ENV+=(
      "MOOX_DATASET_HEALTH_POLICY=${ROOT}/config/dataset-health-policy.yaml"
      "MOOX_DATASET_HEALTH_POLICY_HASH=sha256:$(shasum -a 256 "${ROOT}/config/dataset-health-policy.yaml" | awk '{print $1}')"
    )
  fi
  if [[ -f "${HOME}/.config/moox/eventbus/metrics-publisher.yaml" ]]; then
    RUNTIME_IDENTITY_ENV+=("MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE=${HOME}/.config/moox/eventbus/metrics-publisher.yaml")
  fi
  if [[ -n "${MOOX_EVENTBUS_NATS_URL:-}" ]]; then
    RUNTIME_IDENTITY_ENV+=("MOOX_METRICS_EVENTBUS_URL=${MOOX_EVENTBUS_NATS_URL}")
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
  "MOOX_EVENTBUS_CREDENTIAL_FILE=${MOOX_EVENTBUS_CREDENTIAL_FILE:-${HOME}/.config/moox/eventbus/collector-market-fetch-consumer.yaml}"
)

if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
	FACTOR_EVENTBUS_URL_ENV="tls://127.0.0.1:${MOOX_EVENTBUS_PORT:-4222}"
else
	FACTOR_EVENTBUS_URL_ENV="nats://127.0.0.1:${MOOX_EVENTBUS_PORT:-4222}"
fi
FACTOR_ENV=(
  "MOOX_FACTOR_ADMIN_GATEWAY_URL=${MOOX_FACTOR_ADMIN_GATEWAY_URL:-http://127.0.0.1:11002}"
  "MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=${LOCAL_STORAGE_RPC_GATEWAY_TARGET}"
  "MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID=${LOCAL_STORAGE_GATEWAY_NODE_ID}"
  "MOOX_FACTOR_DB_PATH=${MOOX_FACTOR_DB_PATH:-${ROOT}/data/factor/factor.db}"
  "MOOX_FACTOR_ENGINE_WORKER_PATH=${MOOX_FACTOR_ENGINE_WORKER_PATH:-${ROOT}/factor/pyworker/worker.py}"
	"MOOX_FACTOR_ENGINE_FACTORS_DIR=${MOOX_FACTOR_ENGINE_FACTORS_DIR:-${ROOT}/factor/factors}"
	"MOOX_FACTOR_ENGINE_PYTHON_WORKERS=${MOOX_FACTOR_ENGINE_PYTHON_WORKERS:-32}"
	"MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS=${MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS:-64}"
	"MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS=${MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS:-10000}"
	"MOOX_EVENTBUS_NATS_URL=${MOOX_FACTOR_EVENTBUS_URL:-${FACTOR_EVENTBUS_URL_ENV}}"
	  "MOOX_PYTHON_RUNTIME_PATH=${ROOT}/python-runtime"
	  "MOOX_STORAGE_PRIMARY_AUTH_SECRET=${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}"
	  "MOOX_STORAGE_VIEW_AUTH_SECRET=${MOOX_STORAGE_VIEW_AUTH_SECRET:-}"
)
if [[ -n "${MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE:-}" ]]; then
  FACTOR_ENV+=("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE=${MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE}")
elif [[ -r "${HOME}/.config/moox/eventbus/factor-eventbus.yaml" ]]; then
  # Do not let the process-wide collector credential leak into Factor. The
  # factor consumer has its own JetStream permissions and must be explicit
  # even when the shared EventBus URL is configured without TLS.
  FACTOR_ENV+=("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE=${HOME}/.config/moox/eventbus/factor-eventbus.yaml")
fi

MONITOR_ENV=(
  "MOOX_MONITOR_INSTANCE_ID=${MOOX_MONITOR_INSTANCE_ID}"
	"MOOX_STORAGE_PRIMARY_AUTH_SECRET=${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}"
)
# Observability credentials are a runtime concern rather than a checked-in
# monitor config detail.  Keep the override available even when a component
# overlay was produced without MOOX_EVENTBUS_ENABLE_TLS: an existing install
# may still use the TLS EventBus and its role credential file.
MONITOR_OBSERVABILITY_CREDENTIAL_FILE="${MOOX_MONITOR_OBSERVABILITY_CREDENTIAL_FILE:-${HOME}/.config/moox/eventbus/monitor-observability.yaml}"
if [[ -r "${MONITOR_OBSERVABILITY_CREDENTIAL_FILE}" ]]; then
  MONITOR_ENV+=("MOOX_OBSERVABILITY_CREDENTIAL_FILE=${MONITOR_OBSERVABILITY_CREDENTIAL_FILE}")
fi

METRICS_METADATA_URL="${MOOX_METRICS_STORAGE_METADATA_URL:-http://127.0.0.1:20200}"
EVENTBUS_URL_ENV="${MOOX_EVENTBUS_NATS_URL:-__EVENTBUS_URL__}"
MOOX_EVENTBUS_HOST="${MOOX_EVENTBUS_HOST:-__EVENTBUS_HOST__}"
MOOX_EVENTBUS_PORT="${MOOX_EVENTBUS_PORT:-__EVENTBUS_PORT__}"
MOOX_EVENTBUS_ENABLE_TLS="${MOOX_EVENTBUS_ENABLE_TLS:-__EVENTBUS_ENABLE_TLS__}"
STORAGE_EVENTBUS_URL_ENV="${MOOX_STORAGE_EVENTBUS_URL:-tls://127.0.0.1:${MOOX_EVENTBUS_PORT}}"
STORAGE_EVENTBUS_CREDENTIAL_FILE="${MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE:-${HOME}/.config/moox/eventbus/storage-eventbus.yaml}"
STORAGE_EVENTBUS_CREDENTIAL_ENV=()
if [[ -r "${STORAGE_EVENTBUS_CREDENTIAL_FILE}" ]]; then
  STORAGE_EVENTBUS_CREDENTIAL_ENV=("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE=${STORAGE_EVENTBUS_CREDENTIAL_FILE}")
fi
export MOOX_EVENTBUS_NATS_URL="${EVENTBUS_URL_ENV}" MOOX_EVENTBUS_HOST MOOX_EVENTBUS_PORT MOOX_EVENTBUS_ENABLE_TLS
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
  url="${url#tls://}"
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
  wait_nats factor "${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" "${MOOX_WAIT_FACTOR_NATS_SECONDS:-60}"
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

wait_storage_view_live() {
  local attempts="${1:-30}" body
  echo "waiting for storage-view restore and consumer binding"
  for _ in $(seq 1 "${attempts}"); do
    body=$(curl --fail --silent --max-time 2 \
      -H "X-Moox-Health-Auth: $(sign_health_request GET /healthz)" \
      http://127.0.0.1:20211/healthz 2>/dev/null || true)
    if grep -Eq '"process_alive"[[:space:]]*:[[:space:]]*true' <<<"${body}" &&
      grep -Eq '"consumer_bound"[[:space:]]*:[[:space:]]*true' <<<"${body}" &&
      grep -Eq '"restore_ready"[[:space:]]*:[[:space:]]*true' <<<"${body}"; then
      return 0
    fi
    sleep 1
  done
  echo "storage-view restore or consumer binding not ready after ${attempts}s" >&2
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
  local name="$1" url="" health_path=/healthz
  case "${name}" in
    admin) url=http://127.0.0.1:11010/readyz ;;
    gateway) url=http://127.0.0.1:11012/readyz ;;
    archive) url=http://127.0.0.1:11416/readyz ;;
    cloudnode) url=http://127.0.0.1:11411/readyz ;;
    collector) url=http://127.0.0.1:11412/readyz ;;
    eventbus) url=http://127.0.0.1:11419/readyz ;;
    hostagent) url=http://127.0.0.1:11425/readyz ;;
    factor) url=http://127.0.0.1:11414/readyz ;;
    strategy) url=http://127.0.0.1:11431/readyz ;;
    trade) url=http://127.0.0.1:11210/readyz ;;
    monitor) url=http://127.0.0.1:11409/readyz ;;
    web-host) url=http://127.0.0.1:19527/readyz ;;
    storage-primary) url=http://127.0.0.1:20210/readyz ;;
    storage-view) url=http://127.0.0.1:20211/readyz; health_path=/readyz ;;
    storage-node) url=http://127.0.0.1:20212/readyz ;;
    *) echo "unknown service health mapping: ${name}" >&2; return 1 ;;
  esac
  curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $(sign_health_request GET /readyz)" "${url}" >/dev/null
}

listener_open() {
  local name="$1" port
  case "${name}" in
    admin) port=11010 ;; gateway) port=11012 ;; archive) port=11416 ;;
    cloudnode) port=11411 ;; collector) port=11412 ;; eventbus) port=11419 ;;
    factor) port=11414 ;; strategy) port=11431 ;; trade) port=11210 ;;
    monitor) port=11409 ;; hostagent) port=11425 ;; web-host) port=19527 ;; storage-primary) port=20210 ;;
    storage-view) port=20211 ;; storage-node) port=20212 ;; *) return 1 ;;
  esac
  # Do not use --fail: a 401/503 proves that the listener accepts TCP while
  # readiness is still being established. Connection refusal is the only
  # state that should keep the startup grace from masking a bad bind.
  curl --silent --output /dev/null --connect-timeout 1 --max-time 1 "http://127.0.0.1:${port}/healthz"
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

register_storage_node() {
  [[ "${WITH_STORAGE_NODE}" == "1" ]] || { echo "storage DataNode is required for metadata bootstrap" >&2; exit 1; }
  echo "registering deployment-owned storage DataNode"
  "${ROOT}/bin/moox-storage-cli" register-node \
    --metadata-target "ip://127.0.0.1:20100" \
    --node-id "${MOOX_STORAGE_NODE_ID:-storage-node-0}" \
    --service-target "ip://127.0.0.1:20107" \
    --name "${MOOX_STORAGE_NODE_NAME:-数据节点}" \
    >>"${ROOT}/logs/storage/stdout.log" 2>&1
}

run_storage_doctor() {
  if [[ "${WITH_ADMIN}" != "1" || "${WITH_MONITOR}" != "1" || ! -x "${ROOT}/bin/moox-cli" ]]; then
    echo "defer Dataset activation: control-plane Doctor is not deployed"
    return 1
  fi
  local report="${ROOT}/logs/storage/doctor-bootstrap.json"
  echo "running read-only Doctor bootstrap before Dataset activation"
  if ! (
    cd "${ROOT}"
    "${ROOT}/bin/moox-cli" doctor bootstrap --format json --output "${report}"
  ); then
    echo "defer Dataset activation: Doctor bootstrap failed"
    return 1
  fi
  if ! grep -Eq '"conclusion"[[:space:]]*:[[:space:]]*"HEALTHY"' "${report}"; then
    echo "defer Dataset activation: Doctor conclusion is not HEALTHY"
    return 1
  fi
  return 0
}

activate_storage_datasets() {
  echo "explicitly activating healthy storage Datasets"
  "${ROOT}/bin/moox-storage-cli" activate-datasets \
    --metadata-target "ip://127.0.0.1:20100" \
    >>"${ROOT}/logs/storage/stdout.log" 2>&1
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
		"${ROOT}/bin/moox-collector-cli" init \
			--db-path ../data/collector/moox_collector.db \
			--seed-file ../examples/setup/default/collector-rules.yaml \
			>> "${ROOT}/logs/collector/stdout.log" 2>&1
	)
}

init_trade_schema() {
  echo "initializing trade schema"
  mkdir -p "${ROOT}/logs/trade"
  (
    cd "${ROOT}/trade"
    "${ROOT}/bin/moox-trade-cli" init --db-path ../data/trade/moox_trade.db >> "${ROOT}/logs/trade/stdout.log" 2>&1
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
		runtime_identity_env "${name}" "${ROOT}/storage/config/${trpc_conf}"
		local role="${name#storage-}"
		start_service "${name}" "${ROOT}/storage" \
	    env \
	      "${RUNTIME_IDENTITY_ENV[@]}" \
	      "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_OTEL_SERVICE_NAME=moox-${name}" \
      "STORAGE_CONFIG_PATH=${ROOT}/storage/config" \
      "MOOX_STORAGE_CONFIG=${ROOT}/storage/config/${storage_conf}" \
      "MOOX_STORAGE_HOME=${ROOT}/data/storage" \
      "MOOX_STORAGE_ROLE=${role}" \
      "MOOX_STORAGE_EVENTBUS_URL=${STORAGE_EVENTBUS_URL_ENV}" \
      "${STORAGE_EVENTBUS_CREDENTIAL_ENV[@]}" \
      "MOOX_STORAGE_NODE_AUTH_SECRET=${MOOX_STORAGE_NODE_AUTH_SECRET:?MOOX_STORAGE_NODE_AUTH_SECRET is required}" \
      "MOOX_STORAGE_PRIMARY_AUTH_SECRET=${MOOX_STORAGE_PRIMARY_AUTH_SECRET:?MOOX_STORAGE_PRIMARY_AUTH_SECRET is required}" \
      "MOOX_STORAGE_VIEW_AUTH_SECRET=${MOOX_STORAGE_VIEW_AUTH_SECRET:?MOOX_STORAGE_VIEW_AUTH_SECRET is required}" \
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
  if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
    for required in \
      users.yaml internal-admin.yaml ca.pem server.pem server-key.pem; do
      if [[ ! -r "${credential_dir}/${required}" ]]; then
        echo "missing EventBus TLS credential: ${credential_dir}/${required}" >&2
        exit 1
      fi
    done
    perl -0pi -e 's#enabled:\s*false\n    username:#enabled: true\n    username:#; s#users_file:\s*""#users_file: "'"${credential_dir}"'/users.yaml"#; s#enabled:\s*false\n    cert_file:#enabled: true\n    cert_file:#; s#cert_file:\s*""#cert_file: "'"${credential_dir}"'/server.pem"#; s#key_file:\s*""#key_file: "'"${credential_dir}"'/server-key.pem"#; s#ca_file:\s*""#ca_file: "'"${credential_dir}"'/ca.pem"#' \
      "${ROOT}/eventbus/config/app.yaml"
    perl -0pi -e 's#credential_file:\s*""#credential_file: "'"${credential_dir}"'/internal-admin.yaml"#; s#tls_ca_file:\s*""#tls_ca_file: "'"${credential_dir}"'/ca.pem"#' \
      "${ROOT}/eventbus/config/app.yaml"
  fi
  # Validate TLS/config prerequisites before disabling an existing supervisor;
  # a bad package must not turn a healthy EventBus into an avoidable outage.
  disable_conflicting_eventbus_supervisor
  runtime_identity_env eventbus "${ROOT}/eventbus/config/app.yaml"
  start_service "eventbus" "${ROOT}/eventbus" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${ROOT}/bin/moox-eventbus" -conf=config/trpc_go.yaml
  wait_http http://127.0.0.1:11419/readyz "${MOOX_WAIT_EVENTBUS_SECONDS:-60}"
}

start_hostagent() {
  if [[ "${WITH_HOSTAGENT}" != "1" ]]; then
    echo "hostagent is disabled in this deployment package" >&2
    exit 2
  fi
  local credential_dir="${HOME}/.config/moox/eventbus"
  mkdir -p "${credential_dir}"
  if [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]]; then
    [[ -r "${credential_dir}/hostagent-publisher.yaml" && -r "${credential_dir}/ca.pem" ]] || {
      echo "missing HostAgent EventBus credential: ${credential_dir}/hostagent-publisher.yaml" >&2
      exit 1
    }
  elif [[ ! -r "${credential_dir}/hostagent-publisher.yaml" ]]; then
    umask 077
    cat >"${credential_dir}/hostagent-publisher.yaml" <<HOSTAGENT_CONFIG_EOF
version: 1
urls:
  - nats://127.0.0.1:${MOOX_EVENTBUS_PORT}
username: hostagent-publisher
eventbus_token: insecure-local-development
ca_file: ""
HOSTAGENT_CONFIG_EOF
    chmod 0600 "${credential_dir}/hostagent-publisher.yaml"
  fi
  runtime_identity_env moox_hostagent "${ROOT}/hostagent/config/app.yaml"
  start_service "hostagent" "${ROOT}/hostagent" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "MOOX_HOST_AGENT_HEALTH_ADDR=127.0.0.1:11425" \
      "${ROOT}/bin/moox-host-agent" -conf=config/trpc_go.yaml
  wait_http http://127.0.0.1:11425/readyz "${MOOX_WAIT_HOSTAGENT_SECONDS:-30}"
}

start_archive() {
  if [[ "${WITH_ARCHIVE}" != "1" ]]; then
    echo "archive is disabled in this deployment package" >&2
    exit 2
  fi
  # Archive runs on the same host as EventBus.  Keep its consumer on the
  # local listener instead of sending JetStream fetch/ack traffic through the
  # public address (which is unreliable on hosts without public-IP hairpin
  # routing).  Other processes, notably SCF-facing Collector, continue to use
  # the deployment-wide public URL.
  local archive_eventbus_url="${MOOX_ARCHIVE_EVENTBUS_NATS_URL:-}"
  if [[ -z "${archive_eventbus_url}" ]]; then
    archive_eventbus_url="nats://127.0.0.1:4222"
    [[ "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" ]] && archive_eventbus_url="tls://127.0.0.1:4222"
  fi
  gateway_service_env_for archive
  runtime_identity_env moox_archive "${ROOT}/archive/config/app.yaml"
  start_service "archive" "${ROOT}/archive" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_EVENTBUS_NATS_URL=${archive_eventbus_url}" \
      "${ROOT}/bin/moox-archive" -config=config/app.yaml -conf=config/trpc_go.yaml
}

start_storage_primary() {
	start_storage_process "storage-primary" "moox-storage-primary" "trpc_go.yaml" "storage.yaml"
}

start_storage_view() {
  gateway_service_env_for storage-view
  runtime_identity_env storage-view "${ROOT}/storage-view/config/trpc_go.yaml"
  # Storage View creates one durable per configured consumer partition. The
  # policy is injected from the partition config so Kline, metrics and other
  # datasets have independent delivery lifecycles.
  start_service "storage-view" "${ROOT}/storage-view" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_OTEL_SERVICE_NAME=moox-storage-view" \
      "MOOX_GATEWAY_CALLER=storage-view" "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_SERVICE_GATEWAY_TARGET=ip://127.0.0.1:11003" \
      "MOOX_STORAGE_CONFIG=${ROOT}/storage-view/config/trpc_go.yaml" \
      "MOOX_STORAGE_HOME=${ROOT}/data/storage" \
      "MOOX_STORAGE_ROLE=view" \
      "MOOX_STORAGE_EVENTBUS_URL=${STORAGE_EVENTBUS_URL_ENV}" \
      "${STORAGE_EVENTBUS_CREDENTIAL_ENV[@]}" \
      "MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=${MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT:-256MB}" \
      "MOOX_STORAGE_NODE_AUTH_SECRET=${MOOX_STORAGE_NODE_AUTH_SECRET:?MOOX_STORAGE_NODE_AUTH_SECRET is required}" \
      "MOOX_STORAGE_PRIMARY_AUTH_SECRET=${MOOX_STORAGE_PRIMARY_AUTH_SECRET:?MOOX_STORAGE_PRIMARY_AUTH_SECRET is required}" \
      "MOOX_STORAGE_VIEW_AUTH_SECRET=${MOOX_STORAGE_VIEW_AUTH_SECRET:?MOOX_STORAGE_VIEW_AUTH_SECRET is required}" \
      "${ROOT}/bin/moox-storage-view" \
      -conf=config/trpc_go.yaml
}

start_storage_node() {
  if [[ "${WITH_STORAGE_NODE}" != "1" ]]; then
    echo "storage-node is disabled in this deployment package" >&2
    exit 2
  fi
  gateway_service_env_for storage-primary
	runtime_identity_env storage-node "${ROOT}/storage-node/config/trpc_go.yaml"
  start_service "storage-node" "${ROOT}/storage-node" \
    env \
		"${RUNTIME_IDENTITY_ENV[@]}" \
      "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_SERVICE_GATEWAY_TARGET=ip://127.0.0.1:11003" \
      "MOOX_OTEL_SERVICE_NAME=moox-storage-node" \
      "MOOX_STORAGE_CONFIG=${ROOT}/storage-node/config/storage.yaml" \
      "MOOX_STORAGE_HOME=${ROOT}/data/storage-node" \
      "MOOX_STORAGE_ROLE=node" \
      "MOOX_STORAGE_NODE_ID=${MOOX_STORAGE_NODE_ID:-storage-node-0}" \
      "MOOX_STORAGE_EVENTBUS_URL=${STORAGE_EVENTBUS_URL_ENV}" \
      "${STORAGE_EVENTBUS_CREDENTIAL_ENV[@]}" \
      "MOOX_STORAGE_NODE_AUTH_SECRET=${MOOX_STORAGE_NODE_AUTH_SECRET:?MOOX_STORAGE_NODE_AUTH_SECRET is required}" \
      "${ROOT}/bin/moox-storage-node" \
      -conf=config/trpc_go.yaml
}

start_storage() {
  [[ "${WITH_STORAGE_NODE}" == "1" ]] || { echo "storage deployment requires a DataNode" >&2; exit 1; }
  start_storage_node
  wait_tcp 127.0.0.1 20107 "${MOOX_WAIT_STORAGE_NODE_SECONDS:-30}"
  wait_http http://127.0.0.1:20212/healthz "${MOOX_WAIT_STORAGE_NODE_SECONDS:-30}"
  # Primary performs an initial DataNode cleanup during startup. Start it only
  # after the node is serving so that cleanup and history reads cannot race the
  # DataNode listener during a deployment restart.
  start_storage_primary
  wait_tcp 127.0.0.1 20201 "${MOOX_WAIT_STORAGE_ACCESS_SECONDS:-30}"
  wait_nats storage "${STORAGE_EVENTBUS_URL_ENV}" "${MOOX_WAIT_STORAGE_NATS_SECONDS:-30}"
  wait_http http://127.0.0.1:20210/healthz "${MOOX_WAIT_STORAGE_ACCESS_SECONDS:-30}"
  register_storage_node
}

complete_storage_bootstrap() {
  start_storage_view
  wait_tcp 127.0.0.1 20104 "${MOOX_WAIT_STORAGE_VIEW_SECONDS:-900}"
  wait_tcp 127.0.0.1 20202 "${MOOX_WAIT_STORAGE_VIEW_SECONDS:-900}"
  # A durable backlog makes /readyz return 503 even when the restored View is
  # correctly consuming. Deployment requires a healthy restore and a bound
  # consumer, not an already-drained backlog.
  wait_storage_view_live "${MOOX_WAIT_STORAGE_VIEW_SECONDS:-900}"
  if run_storage_doctor; then
    activate_storage_datasets
  else
    echo "Storage started without Dataset activation; run explicit activation after a HEALTHY Doctor result"
  fi
}

start_admin() {
  [[ "${WITH_ADMIN}" == "1" ]] || { echo "admin is disabled in this deployment package" >&2; exit 2; }
  local encryption_key_file="${HOME}/.config/moox/credentials/admin-encryption-key"
  [[ -f "${encryption_key_file}" ]] || { echo "missing Admin encryption key: ${encryption_key_file}" >&2; exit 1; }
  init_admin_schema
  if [[ -x "${ROOT}/bin/moox-admin-cli" && -f "${ROOT}/examples/setup/default/service-deployments.yaml" ]]; then
    local service_seed_args=(service-deployments import
      --db-path "${ROOT}/data/admin.db" \
      --file "${ROOT}/examples/setup/default/service-deployments.yaml" \
      --node-id "${MOOX_ADMIN_NODE_ID}" \
      --public-host "${PUBLIC_HOST:-127.0.0.1}" \
      --eventbus-nats-url "${MOOX_EVENTBUS_NATS_URL}")
    if [[ "${WITH_STORAGE_NODE}" == "1" ]]; then
      service_seed_args+=(--with-storage-shard)
    else
      service_seed_args+=(--disable-storage-shard)
    fi
    local resolver_enabled=0 resolver_node="" resolver_target=""
    local resolver_json="${ROOT}/config/render-runtime-config.json"
    local trade_console_host="" trade_console_port=""
    if [[ -r "${resolver_json}" ]]; then
      IFS=$'\t' read -r resolver_enabled resolver_node resolver_target trade_console_host trade_console_port < <(python3 - "${resolver_json}" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
print("\t".join([
    "1" if value.get("dns_resolver_enabled") else "0",
    value.get("dns_resolver_node_id", ""),
    value.get("dns_resolver_target", ""),
    value.get("trade_console_host", ""),
    str(value.get("trade_console_port", "")),
]))
PY
      )
    fi
    local disabled_services=()
    # A normal partial deployment preserves the existing Storage routes. A
    # control profile is intentionally isolated and disables its local routes.
    if [[ "${WITH_STORAGE}" != "1" && "${PRESERVE_STORAGE_ROUTES}" != "1" ]]; then
      disabled_services+=(storage-primary storage-view)
    fi
    [[ "${WITH_ARCHIVE}" == "1" ]] || disabled_services+=(moox_archive)
    [[ "${WITH_CLOUDNODE}" == "1" ]] || disabled_services+=(moox_cloudnode)
    [[ "${WITH_COLLECTOR}" == "1" ]] || disabled_services+=(moox_collector)
    [[ "${WITH_FACTOR}" == "1" ]] || disabled_services+=(moox_factor)
    [[ "${WITH_MONITOR}" == "1" ]] || disabled_services+=(moox_monitor)
    [[ "${WITH_HOSTAGENT}" == "1" ]] || disabled_services+=(moox_hostagent)
    [[ "${WITH_STRATEGY}" == "1" ]] || disabled_services+=(moox_strategy)
    [[ "${WITH_TRADE}" == "1" ]] || disabled_services+=(moox_trade)
    if [[ "${resolver_enabled}" != "1" || -z "${resolver_node}" || "${resolver_node}" != "${MOOX_ADMIN_NODE_ID}" ]]; then
      disabled_services+=(trade_dns_resolver)
    fi
    [[ "${WITH_WEB_HOST}" == "1" ]] || disabled_services+=(web_host)
    if [[ -n "${trade_console_host}" ]]; then
      service_seed_args+=(--trade-console-host "${trade_console_host}" --trade-console-port "${trade_console_port:-11200}")
    fi
    if (( ${#disabled_services[@]} > 0 )); then
      local disabled_services_csv
      disabled_services_csv=$(IFS=,; printf '%s' "${disabled_services[*]}")
      service_seed_args+=(--disabled-services "${disabled_services_csv}")
    fi
    "${ROOT}/bin/moox-admin-cli" "${service_seed_args[@]}" >>"${ROOT}/logs/admin/stdout.log" 2>&1 || {
        echo "Storage shard service deployment import failed" >&2
        exit 1
    }
    # A DNS resolver may live on a separate Trade node. Import only its route
    # for that node so the native Gateway does not advertise unrelated local
    # loopback services there.
    if [[ "${resolver_enabled}" == "1" && -n "${resolver_node}" && "${resolver_node}" != "${MOOX_ADMIN_NODE_ID}" ]]; then
        local resolver_public_host="${resolver_target#ip://}"
        resolver_public_host="${resolver_public_host%:*}"
        "${ROOT}/bin/moox-admin-cli" service-deployments import \
          --db-path "${ROOT}/data/admin.db" \
          --file "${ROOT}/examples/setup/default/service-deployments.yaml" \
          --node-id "${resolver_node}" \
          --public-host "${resolver_public_host}" \
          --eventbus-nats-url "${MOOX_EVENTBUS_NATS_URL}" \
          --only-services trade_dns_resolver \
          >>"${ROOT}/logs/admin/stdout.log" 2>&1 || {
            echo "Trade DNS resolver service deployment import failed" >&2
            exit 1
          }
    fi
  fi
  if [[ "${WITH_EVENTBUS}" == "1" && "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" && -x "${ROOT}/bin/moox-admin-cli" ]]; then
    local eventbus_credentials_dir="${HOME}/.config/moox/eventbus"
    local eventbus_credentials_complete=1 credential_name
    for credential_name in ca.pem server.pem server-key.pem users.yaml internal-admin.yaml \
      archive-eventbus.yaml cloudnode-eventbus.yaml cloudnode-worker.yaml \
      hostagent-publisher.yaml market-fetch-publisher.yaml metrics-publisher.yaml \
      collector-market-fetch-consumer.yaml factor-eventbus.yaml monitor-observability.yaml \
      storage-eventbus.yaml strategy-eventbus.yaml trade-eventbus.yaml; do
      [[ -s "${eventbus_credentials_dir}/${credential_name}" ]] || eventbus_credentials_complete=0
    done
    if [[ "${MOOX_EVENTBUS_ROTATE_CREDENTIALS:-0}" == "1" ]]; then
      # A reset with a changed public EventBus endpoint must mint a new TLS
      # bundle before EventBus starts; the old certificate SAN is no longer
      # valid for clients using the new endpoint.
      eventbus_credentials_complete=0
      echo "rotate EventBus identities after endpoint change in ${eventbus_credentials_dir}"
    fi
    if [[ "${eventbus_credentials_complete}" -eq 1 && ( "${MOOX_RESET_CONTROL_DATA:-0}" == "1" || "${MOOX_PRESERVE_EXTERNAL_EVENTBUS_CREDENTIALS:-0}" == "1" ) ]]; then
      # A destructive control reset removes admin.db, including the encrypted
      # EventBus records.  The exported role files are still authoritative for
      # the running EventBus and Storage peers, so keep them instead of
      # rotating credentials or failing the fresh bootstrap.
      echo "preserve EventBus identities after control data reset in ${eventbus_credentials_dir}"
    elif [[ "${eventbus_credentials_complete}" -eq 1 ]]; then
      echo "reuse EventBus identities and refresh exported endpoints in ${eventbus_credentials_dir}"
    else
      mkdir -p "${eventbus_credentials_dir}"
      "${ROOT}/bin/moox-admin-cli" eventbus-credentials ensure \
        --db-path "${ROOT}/data/admin.db" \
        --encryption-key-file "${encryption_key_file}" \
        --node-id "${MOOX_ADMIN_NODE_ID}" \
        >>"${ROOT}/logs/admin/stdout.log" 2>&1 ||
        { echo "EventBus credential provisioning failed" >&2; exit 1; }
    fi
    if [[ ( "${MOOX_RESET_CONTROL_DATA:-0}" != "1" && "${MOOX_PRESERVE_EXTERNAL_EVENTBUS_CREDENTIALS:-0}" != "1" ) || "${eventbus_credentials_complete}" -eq 0 ]]; then
      "${ROOT}/bin/moox-admin-cli" eventbus-credentials export \
        --db-path "${ROOT}/data/admin.db" \
        --encryption-key-file "${encryption_key_file}" \
        --node-id "${MOOX_ADMIN_NODE_ID}" \
        --output-dir "${eventbus_credentials_dir}" \
        >>"${ROOT}/logs/admin/stdout.log" 2>&1 ||
        { echo "EventBus credential export failed" >&2; exit 1; }
    fi
  fi
  gateway_service_env_for admin-gateway
  runtime_identity_env admin_gateway "${ROOT}/admin/config/trpc_go.yaml"
  start_service "admin" "${ROOT}/admin" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${ADMIN_SECRET_ENV[@]}" "${NOTIFICATION_ENV[@]+"${NOTIFICATION_ENV[@]}"}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_NODE_GATEWAY_URL=http://127.0.0.1:11002" "MOOX_NODE_GATEWAY_NATIVE_URL=${LOCAL_STORAGE_RPC_GATEWAY_ADDRESS}" "MOOX_NODE_GATEWAY_NODE_ID=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_ADMIN_NODE_ID=${MOOX_ADMIN_NODE_ID}" "MOOX_ADMIN_DB_PATH=${ROOT}/data/admin.db" \
      "MOOX_ADMIN_ENCRYPTION_KEY_FILE=${encryption_key_file}" "MOOX_OTEL_SERVICE_NAME=moox-admin" \
      "${ROOT}/bin/moox-admin" -conf=config/trpc_go.yaml
}

start_gateway() {
  [[ "${WITH_GATEWAY}" == "1" ]] || { echo "gateway is disabled in this deployment package" >&2; exit 2; }
	# The native listener is the data-plane ingress used by short-lived SCF
	# functions.  A binary-only/component deployment can leave an older
	# loopback-only app.yaml in place even though the current topology publishes
	# the native target on the public host.  Reconcile the listener from the
	# persisted SCF target immediately before starting Gateway so a restart cannot
	# silently bring the collector back to a connection-refused state.
		local gateway_config="${ROOT}/gateway/config/app.yaml"
		local expected_native="127.0.0.1:11003"
		local current_native
		# A non-loopback public host is the persisted control-plane topology. It
		# wins over a stale loopback SCF override because Collector resolves its
		# native target independently from SysDeploy.
		if [[ "${PUBLIC_HOST}" != "" && "${PUBLIC_HOST}" != localhost && "${PUBLIC_HOST}" != 127.* &&
		      "${PUBLIC_HOST}" != ::1 && "${PUBLIC_HOST}" != \[::1\] ]] ||
			[[ "${SCF_STORAGE_RPC_GATEWAY_TARGET}" != ip://127.0.0.1:* &&
		      "${SCF_STORAGE_RPC_GATEWAY_TARGET}" != ip://localhost:* &&
		      "${SCF_STORAGE_RPC_GATEWAY_TARGET}" != ip://\[::1\]:* ]]; then
		expected_native="0.0.0.0:11003"
	fi
	if [[ -r "${gateway_config}" ]]; then
		current_native="$(awk '/^[[:space:]]+native_addr:/ {print $2; exit}' "${gateway_config}")"
		if [[ "${expected_native}" == "0.0.0.0:11003" && "${current_native}" == "127.0.0.1:11003" ]]; then
			perl -0pi -e 's#native_addr:\s*127\.0\.0\.1:11003#native_addr: 0.0.0.0:11003#' "${gateway_config}"
			printf 'gateway: reconciled native listener to %s for SCF target %s\n' "${expected_native}" "${SCF_STORAGE_RPC_GATEWAY_TARGET}" >&2
		fi
		current_native="$(awk '/^[[:space:]]+native_addr:/ {print $2; exit}' "${gateway_config}")"
		# Never downgrade a public listener solely because a runtime override says
		# loopback. Collector resolves its native target from SysDeploy, so a
		# conflicting override must not recreate a public connection refusal.
		if [[ "${expected_native}" == "0.0.0.0:11003" && "${current_native}" != "0.0.0.0:11003" ]] ||
			[[ "${expected_native}" == "127.0.0.1:11003" && "${current_native}" != "127.0.0.1:11003" && "${current_native}" != "0.0.0.0:11003" ]]; then
			echo "gateway native listener ${current_native:-<missing>} does not match expected ${expected_native} for SCF target ${SCF_STORAGE_RPC_GATEWAY_TARGET}" >&2
			exit 1
		fi
	fi
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
  gateway_service_env_for cloudnode
  runtime_identity_env moox_cloudnode "${ROOT}/cloudnode/config/app.yaml"
  start_service "cloudnode" "${ROOT}/cloudnode" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "${NOTIFICATION_ENV[@]+"${NOTIFICATION_ENV[@]}"}" \
      "MOOX_EVENTBUS_NATS_URL=${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" \
      "MOOX_SERVICE_GATEWAY_HTTP_URL=http://127.0.0.1:11002" \
      "MOOX_SCF_SERVICE_GATEWAY_TARGET=${SCF_SERVICE_GATEWAY_TARGET}" \
      "MOOX_SCF_STORAGE_RPC_GATEWAY_TARGET=${SCF_STORAGE_RPC_GATEWAY_TARGET}" \
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
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET=${LOCAL_STORAGE_RPC_GATEWAY_TARGET}" \
      "MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID=${LOCAL_STORAGE_GATEWAY_NODE_ID}" \
      "${COLLECTOR_ENV[@]}" "${ROOT}/bin/moox-collector" -conf=config/trpc_go.yaml
}

start_factor() {
  if [[ "${WITH_FACTOR}" != "1" ]]; then
    echo "factor is disabled in this deployment package" >&2
    exit 2
  fi
  [[ -n "${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}" ]] || {
    echo "Factor requires MOOX_STORAGE_PRIMARY_AUTH_SECRET" >&2
    exit 1
  }
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

trade_eventbus_preflight() {
  [[ -x "${ROOT}/bin/moox-trade-cli" ]] || {
    echo "trade EventBus preflight requires moox-trade-cli" >&2
    return 1
  }
  local credential_file="${MOOX_TRADE_EVENTBUS_CREDENTIAL_FILE:-${HOME}/.config/moox/eventbus/trade-eventbus.yaml}"
  local result
  if ! result=$(
    MOOX_EVENTBUS_NATS_URL="${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" \
    MOOX_EVENTBUS_CREDENTIAL_FILE="${credential_file}" \
      "${ROOT}/bin/moox-trade-cli" eventbus-check \
        --config "${ROOT}/trade/config/app.yaml"
  ); then
    echo "trade EventBus preflight failed" >&2
    return 1
  fi
  echo "trade EventBus preflight: ${result}"
}

start_trade() {
  if [[ "${WITH_TRADE}" != "1" ]]; then
    echo "trade is disabled in this deployment package" >&2
    exit 2
  fi
  trade_eventbus_preflight
  init_trade_schema
  wait_nats trade "${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" "${MOOX_WAIT_TRADE_NATS_SECONDS:-60}"
  gateway_service_env_for trade
  runtime_identity_env moox_trade "${ROOT}/trade/config/app.yaml"
  start_service "trade" "${ROOT}/trade" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_GATEWAY_NODE_ID=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_EVENTBUS_NATS_URL=${MOOX_EVENTBUS_NATS_URL:-nats://127.0.0.1:4222}" \
      "${ROOT}/bin/moox-trade" -conf=config/trpc_go.yaml
}

start_monitor() {
  if [[ "${WITH_MONITOR}" != "1" ]]; then
    echo "monitor is disabled in this deployment package" >&2
    exit 2
  fi
  init_monitor_schema
  gateway_service_env_for monitor
  runtime_identity_env moox_monitor "${ROOT}/monitor/config/app.yaml"
  start_service "monitor" "${ROOT}/monitor" \
    env "${RUNTIME_IDENTITY_ENV[@]}" "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "${NOTIFICATION_ENV[@]+"${NOTIFICATION_ENV[@]}"}" "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_MONITOR_STORAGE_GATEWAY_TARGET=${LOCAL_STORAGE_RPC_GATEWAY_TARGET}" "MOOX_MONITOR_STORAGE_GATEWAY_NODE_ID=${MOOX_GATEWAY_NODE_ID}" \
      "${MONITOR_ENV[@]}" "${ROOT}/bin/moox-monitor" -conf=config/trpc_go.yaml
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

start_caddy() {
  [[ -x "${ROOT}/lib/caddy-managed.sh" && -s "${ROOT}/config/caddy/edge.env" ]] || return 0
  "${ROOT}/lib/caddy-managed.sh" start --deploy-dir "${ROOT}"
}

SERVICE="${1:-}"
case "${SERVICE}" in
  "")
    if [[ "${WITH_ADMIN}" == "1" ]]; then
      start_admin
    fi
    if [[ "${WITH_GATEWAY}" == "1" ]]; then
      start_gateway
    fi
    if [[ "${WITH_EVENTBUS}" == "1" ]]; then
      start_eventbus
    fi
    if [[ "${WITH_HOSTAGENT}" == "1" ]]; then
      start_hostagent
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
    if [[ "${WITH_STORAGE}" == "1" ]]; then
      complete_storage_bootstrap
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
    if [[ "${WITH_TRADE}" == "1" ]]; then
      start_trade
    fi
    if [[ "${WITH_WEB_HOST}" == "1" ]]; then
      start_web_host
    fi
    start_caddy
    ;;
  storage)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    init_storage_schema
    start_storage
    complete_storage_bootstrap
    ;;
  eventbus) start_eventbus ;;
  hostagent) start_hostagent ;;
  archive) start_archive ;;
  storage-primary)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    init_storage_schema
    wait_tcp 127.0.0.1 20107 "${MOOX_WAIT_STORAGE_NODE_SECONDS:-30}"
    wait_http http://127.0.0.1:20212/healthz "${MOOX_WAIT_STORAGE_NODE_SECONDS:-30}"
    start_storage_primary
    ;;
  storage-view)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    wait_tcp 127.0.0.1 20201 "${MOOX_WAIT_STORAGE_ACCESS_SECONDS:-30}"
    wait_nats storage "${STORAGE_EVENTBUS_URL_ENV}" "${MOOX_WAIT_STORAGE_NATS_SECONDS:-30}"
    start_storage_view
    ;;
  storage-node)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    start_storage_node
    wait_tcp 127.0.0.1 20107 "${MOOX_WAIT_STORAGE_NODE_SECONDS:-30}"
    wait_http http://127.0.0.1:20212/healthz "${MOOX_WAIT_STORAGE_NODE_SECONDS:-30}"
    ;;
  cloudnode) start_cloudnode ;;
  collector) start_collector ;;
  factor) start_factor ;;
  strategy) start_strategy ;;
  trade) start_trade ;;
  monitor) start_monitor ;;
  gateway) start_gateway ;;
  admin) start_admin ;;
  web-host) start_web_host ;;
  *)
    echo "unknown service: ${SERVICE}; valid: eventbus hostagent storage storage-primary storage-view storage-node cloudnode collector factor strategy trade monitor admin gateway web-host" >&2
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
if [[ -r "${ROOT}/config/components.env" ]]; then
  source "${ROOT}/config/components.env"
fi
set -a
source "${ROOT}/secrets/health-auth.env"
set +a
WITH_STORAGE="${MOOX_WITH_STORAGE:-${MOOX_INSTALLED_WITH_STORAGE:-__WITH_STORAGE__}}"
WITH_STORAGE_NODE="${MOOX_WITH_STORAGE_NODE:-${MOOX_INSTALLED_WITH_STORAGE_NODE:-__WITH_STORAGE_NODE__}}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-${MOOX_INSTALLED_WITH_EVENTBUS:-__WITH_EVENTBUS__}}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-${MOOX_INSTALLED_WITH_ARCHIVE:-__WITH_ARCHIVE__}}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-${MOOX_INSTALLED_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-${MOOX_INSTALLED_WITH_COLLECTOR:-__WITH_COLLECTOR__}}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-${MOOX_INSTALLED_WITH_FACTOR:-__WITH_FACTOR__}}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-${MOOX_INSTALLED_WITH_STRATEGY:-__WITH_STRATEGY__}}"
WITH_TRADE="${MOOX_WITH_TRADE:-${MOOX_INSTALLED_WITH_TRADE:-__WITH_TRADE__}}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-${MOOX_INSTALLED_WITH_MONITOR:-__WITH_MONITOR__}}"
WITH_HOSTAGENT="${MOOX_WITH_HOSTAGENT:-${MOOX_INSTALLED_WITH_HOSTAGENT:-__WITH_HOSTAGENT__}}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-${MOOX_INSTALLED_WITH_WEB_HOST:-__WITH_WEB_HOST__}}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-${MOOX_INSTALLED_WITH_ADMIN:-__WITH_ADMIN__}}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-${MOOX_INSTALLED_WITH_GATEWAY:-__WITH_GATEWAY__}}"
if [[ "${WITH_STORAGE_NODE}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-node requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_NODE}" == "1" && ! -d "${ROOT}/storage-node" ]]; then
  echo "storage-node is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE}" == "1" && "${WITH_STORAGE_NODE}" != "1" && -d "${ROOT}/storage-node" ]]; then
  echo "storage-node package is present but storage-node is disabled" >&2
  exit 2
fi

process_matches_service() {
  local pid="$1" name="$2" command expected
  expected="${ROOT}/bin/moox-${name}"
  [[ "${name}" == "hostagent" ]] && expected="${ROOT}/bin/moox-host-agent"
  command=$(ps -p "${pid}" -o command= 2>/dev/null || true)
  [[ "${command}" == "${expected}" || "${command}" == "${expected} "* ]]
}

disable_conflicting_eventbus_supervisor() {
  local runtime_dir unit_exec active_state enabled_state
  command -v systemctl >/dev/null 2>&1 || return 0
  runtime_dir="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  [[ -d "${runtime_dir}" ]] || return 0
  unit_exec="$(XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user show -p ExecStart --value moox-eventbus.service 2>/dev/null || true)"
  [[ "${unit_exec}" == *"${ROOT}/bin/moox-eventbus"* ]] || return 0
  echo "disabling conflicting user supervisor for ${ROOT}/bin/moox-eventbus" >&2
  active_state="$(XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user is-active moox-eventbus.service 2>/dev/null || true)"
  if [[ "${active_state}" == "active" || "${active_state}" == "activating" ]]; then
    XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user stop moox-eventbus.service >/dev/null 2>&1 || {
      echo "cannot stop conflicting EventBus user supervisor" >&2
      return 1
    }
  fi
  enabled_state="$(XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user is-enabled moox-eventbus.service 2>/dev/null || true)"
  if [[ "${enabled_state}" == "enabled" || "${enabled_state}" == "enabled-runtime" ]]; then
    XDG_RUNTIME_DIR="${runtime_dir}" systemctl --user disable moox-eventbus.service >/dev/null 2>&1 || {
      echo "cannot disable conflicting EventBus user supervisor" >&2
      return 1
    }
  fi
}

stop_processes_by_binary() {
  local name="$1" expected="${ROOT}/bin/moox-${name}" proc pid exe
  [[ "${name}" == "hostagent" ]] && expected="${ROOT}/bin/moox-host-agent"
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
  if [[ "${name}" == "eventbus" ]]; then
    disable_conflicting_eventbus_supervisor
  fi
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
    if [[ "${WITH_TRADE}" == "1" ]]; then
      stop_service "trade"
    fi
    if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
      stop_service "cloudnode"
    fi
    if [[ "${WITH_STORAGE}" == "1" ]]; then
      stop_service "storage-view"
      stop_service "storage-primary"
      if [[ "${WITH_STORAGE_NODE}" == "1" ]]; then
        stop_service "storage-node"
      fi
      stop_service "storage"
    fi
    if [[ "${WITH_EVENTBUS}" == "1" ]]; then
      stop_service "eventbus"
    fi
    if [[ "${WITH_HOSTAGENT}" == "1" ]]; then
      stop_service "hostagent"
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
    stop_service "storage-view"
    stop_service "storage-primary"
    if [[ "${WITH_STORAGE_NODE}" == "1" ]]; then
      stop_service "storage-node"
    fi
    stop_service "storage"
    ;;
  eventbus)
    if [[ "${WITH_EVENTBUS}" != "1" ]]; then
      echo "eventbus is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "eventbus"
    ;;
  hostagent)
    [[ "${WITH_HOSTAGENT}" == "1" ]] || { echo "hostagent is disabled in this deployment package" >&2; exit 2; }
    stop_service "hostagent"
    ;;
  archive)
    stop_service "archive"
    ;;
  storage-primary|storage-view|storage-node)
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
  trade)
    if [[ "${WITH_TRADE}" != "1" ]]; then
      echo "trade is disabled in this deployment package" >&2
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
    echo "unknown service: ${SERVICE}; valid: eventbus hostagent storage storage-primary storage-view storage-node cloudnode collector factor strategy trade monitor admin gateway web-host" >&2
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
if [[ -r "${ROOT}/config/components.env" ]]; then
  source "${ROOT}/config/components.env"
fi
WITH_STORAGE="${MOOX_WITH_STORAGE:-${MOOX_INSTALLED_WITH_STORAGE:-__WITH_STORAGE__}}"
WITH_STORAGE_NODE="${MOOX_WITH_STORAGE_NODE:-${MOOX_INSTALLED_WITH_STORAGE_NODE:-__WITH_STORAGE_NODE__}}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-${MOOX_INSTALLED_WITH_EVENTBUS:-__WITH_EVENTBUS__}}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-${MOOX_INSTALLED_WITH_ARCHIVE:-__WITH_ARCHIVE__}}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-${MOOX_INSTALLED_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-${MOOX_INSTALLED_WITH_COLLECTOR:-__WITH_COLLECTOR__}}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-${MOOX_INSTALLED_WITH_FACTOR:-__WITH_FACTOR__}}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-${MOOX_INSTALLED_WITH_STRATEGY:-__WITH_STRATEGY__}}"
WITH_TRADE="${MOOX_WITH_TRADE:-${MOOX_INSTALLED_WITH_TRADE:-__WITH_TRADE__}}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-${MOOX_INSTALLED_WITH_MONITOR:-__WITH_MONITOR__}}"
WITH_HOSTAGENT="${MOOX_WITH_HOSTAGENT:-${MOOX_INSTALLED_WITH_HOSTAGENT:-__WITH_HOSTAGENT__}}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-${MOOX_INSTALLED_WITH_WEB_HOST:-__WITH_WEB_HOST__}}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-${MOOX_INSTALLED_WITH_ADMIN:-__WITH_ADMIN__}}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-${MOOX_INSTALLED_WITH_GATEWAY:-__WITH_GATEWAY__}}"
if [[ "${WITH_STORAGE_NODE}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-node requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_NODE}" == "1" && ! -d "${ROOT}/storage-node" ]]; then
  echo "storage-node is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE}" == "1" && "${WITH_STORAGE_NODE}" != "1" && -d "${ROOT}/storage-node" ]]; then
  echo "storage-node package is present but storage-node is disabled" >&2
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
if [[ "${WITH_HOSTAGENT}" == "1" ]]; then
  services=(hostagent "${services[@]}")
fi
if [[ "${WITH_WEB_HOST}" == "1" ]]; then
  services+=(web-host)
fi
if [[ "${WITH_MONITOR}" == "1" ]]; then
  services=(monitor "${services[@]}")
fi
if [[ "${WITH_STORAGE}" == "1" ]]; then
  if [[ "${WITH_STORAGE_NODE}" == "1" ]]; then
    services+=(storage-node)
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
if [[ "${WITH_TRADE}" == "1" ]]; then
  services=(trade "${services[@]}")
fi

if [[ -x "${ROOT}/lib/caddy-managed.sh" && -s "${ROOT}/config/caddy/edge.env" ]]; then
  echo "caddy: $("${ROOT}/lib/caddy-managed.sh" status --deploy-dir "${ROOT}")"
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
if [[ -r "${ROOT}/config/components.env" ]]; then
  source "${ROOT}/config/components.env"
fi
if [[ -r "${ROOT}/config/log-rotation.env" ]]; then
  source "${ROOT}/config/log-rotation.env"
fi
set -a
source "${ROOT}/secrets/health-auth.env"
set +a
WITH_STORAGE="${MOOX_WITH_STORAGE:-${MOOX_INSTALLED_WITH_STORAGE:-__WITH_STORAGE__}}"
WITH_STORAGE_NODE="${MOOX_WITH_STORAGE_NODE:-${MOOX_INSTALLED_WITH_STORAGE_NODE:-__WITH_STORAGE_NODE__}}"
WITH_EVENTBUS="${MOOX_WITH_EVENTBUS:-${MOOX_INSTALLED_WITH_EVENTBUS:-__WITH_EVENTBUS__}}"
WITH_ARCHIVE="${MOOX_WITH_ARCHIVE:-${MOOX_INSTALLED_WITH_ARCHIVE:-__WITH_ARCHIVE__}}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-${MOOX_INSTALLED_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-${MOOX_INSTALLED_WITH_COLLECTOR:-__WITH_COLLECTOR__}}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-${MOOX_INSTALLED_WITH_FACTOR:-__WITH_FACTOR__}}"
WITH_STRATEGY="${MOOX_WITH_STRATEGY:-${MOOX_INSTALLED_WITH_STRATEGY:-__WITH_STRATEGY__}}"
WITH_TRADE="${MOOX_WITH_TRADE:-${MOOX_INSTALLED_WITH_TRADE:-__WITH_TRADE__}}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-${MOOX_INSTALLED_WITH_MONITOR:-__WITH_MONITOR__}}"
WITH_HOSTAGENT="${MOOX_WITH_HOSTAGENT:-${MOOX_INSTALLED_WITH_HOSTAGENT:-__WITH_HOSTAGENT__}}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-${MOOX_INSTALLED_WITH_WEB_HOST:-__WITH_WEB_HOST__}}"
WITH_ADMIN="${MOOX_WITH_ADMIN:-${MOOX_INSTALLED_WITH_ADMIN:-__WITH_ADMIN__}}"
WITH_GATEWAY="${MOOX_WITH_GATEWAY:-${MOOX_INSTALLED_WITH_GATEWAY:-__WITH_GATEWAY__}}"
if [[ "${WITH_STORAGE_NODE}" == "1" && "${WITH_STORAGE}" != "1" ]]; then
  echo "storage-node requires storage" >&2
  exit 2
fi
if [[ "${WITH_STORAGE_NODE}" == "1" && ! -d "${ROOT}/storage-node" ]]; then
  echo "storage-node is enabled but its package is missing" >&2
  exit 2
fi
if [[ "${WITH_STORAGE}" == "1" && "${WITH_STORAGE_NODE}" != "1" && -d "${ROOT}/storage-node" ]]; then
  echo "storage-node package is present but storage-node is disabled" >&2
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
  local name="$1" url="" health_path=/healthz
  case "${name}" in
    admin) url=http://127.0.0.1:11010/healthz ;;
    gateway) url=http://127.0.0.1:11012/healthz ;;
    archive) url=http://127.0.0.1:11416/healthz ;;
    cloudnode) url=http://127.0.0.1:11411/healthz ;;
    collector) url=http://127.0.0.1:11412/healthz ;;
    eventbus) url=http://127.0.0.1:11419/healthz ;;
    hostagent) url=http://127.0.0.1:11425/healthz ;;
    factor) url=http://127.0.0.1:11414/readyz; health_path=/readyz ;;
    strategy) url=http://127.0.0.1:11431/healthz ;;
    trade) url=http://127.0.0.1:11210/readyz; health_path=/readyz ;;
    monitor) url=http://127.0.0.1:11409/healthz ;;
    web-host) url=http://127.0.0.1:19527/healthz ;;
    storage-primary) url=http://127.0.0.1:20210/healthz ;;
    # View readiness is intentionally different from process liveness: the
    # listener opens before index restore, but a failed restore must not be
    # considered healthy after the startup grace window.
    storage-view) url=http://127.0.0.1:20211/readyz; health_path=/readyz ;;
    storage-node) url=http://127.0.0.1:20212/readyz; health_path=/readyz ;;
    *) echo "unknown service health mapping: ${name}" >&2; return 1 ;;
  esac
  curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $(sign_health_request GET "${health_path}")" "${url}" >/dev/null
}

probe_liveness() {
  local name="$1" url body
  case "${name}" in
    factor) url=http://127.0.0.1:11414/healthz ;;
    storage-node) url=http://127.0.0.1:20212/healthz ;;
    storage-view)
      url=http://127.0.0.1:20211/healthz
      body=$(curl --fail --silent --max-time 2 \
        -H "X-Moox-Health-Auth: $(sign_health_request GET /healthz)" "${url}") || return 1
      # A large durable backlog makes readiness false but remains recoverable.
      # /healthz is the liveness endpoint and intentionally does not expose every
      # /readyz detail (notably restore_ready). Do not turn that omitted detail
      # into a false liveness failure, or the supervisor will restart a draining
      # View after the startup grace window and discard its progress.
      grep -Eq '"process_alive"[[:space:]]*:[[:space:]]*true' <<<"${body}" &&
        grep -Eq '"consumer_bound"[[:space:]]*:[[:space:]]*true' <<<"${body}"
      return
    *)
      probe_service "${name}"
      return
  esac
  curl --fail --silent --max-time 2 \
    -H "X-Moox-Health-Auth: $(sign_health_request GET /healthz)" "${url}" >/dev/null
}

listener_open() {
  local name="$1" port
  case "${name}" in
    admin) port=11010 ;; gateway) port=11012 ;; archive) port=11416 ;;
    cloudnode) port=11411 ;; collector) port=11412 ;; eventbus) port=11419 ;;
    factor) port=11414 ;; strategy) port=11431 ;; trade) port=11210 ;;
    monitor) port=11409 ;; hostagent) port=11425 ;; web-host) port=19527 ;; storage-primary) port=20210 ;;
    storage-view) port=20211 ;; storage-node) port=20212 ;; *) return 1 ;;
  esac
  curl --silent --output /dev/null --connect-timeout 1 --max-time 1 "http://127.0.0.1:${port}/healthz"
}

default_services=()
if [[ "${WITH_ARCHIVE}" == "1" ]]; then
  default_services+=(archive)
fi
if [[ "${WITH_EVENTBUS}" == "1" ]]; then
  default_services+=(eventbus)
fi
if [[ "${WITH_HOSTAGENT}" == "1" ]]; then
  default_services+=(hostagent)
fi
if [[ "${WITH_STORAGE}" == "1" ]]; then
	default_services+=(storage-primary storage-view)
  if [[ "${WITH_STORAGE_NODE}" == "1" ]]; then
    default_services+=(storage-node)
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
if [[ "${WITH_TRADE}" == "1" ]]; then
  default_services+=(trade)
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

check_storage_auth_files() {
  local control_auth storage_auth
  if [[ "${MOOX_INSTALLED_WITH_STORAGE:-0}" == "1" && "${MOOX_INSTALLED_WITH_ADMIN:-0}" == "0" ]]; then
      control_auth="${MOOX_CONTROL_ROOT:-${MOOX_INSTALLED_CONTROL_ROOT:-$(dirname "${ROOT}")/prod}}/secrets/storage-internal-auth.env"
      storage_auth="${ROOT}/secrets/storage-internal-auth.env"
  elif [[ "${MOOX_INSTALLED_WITH_ADMIN:-0}" == "1" && "${MOOX_INSTALLED_WITH_STORAGE:-0}" == "0" ]]; then
      control_auth="${ROOT}/secrets/storage-internal-auth.env"
      storage_auth="${MOOX_STORAGE_ROOT:-${MOOX_INSTALLED_STORAGE_ROOT:-$(dirname "${ROOT}")/storage}}/secrets/storage-internal-auth.env"
  elif [[ "${MOOX_INSTALLED_WITH_ADMIN:-0}" == "1" && "${MOOX_INSTALLED_WITH_STORAGE:-0}" == "1" ]]; then
    # A monolithic package owns both sides of the contract in one file.
    return 0
  elif [[ "${MOOX_INSTALLED_WITH_ADMIN-}" == "0" && "${MOOX_INSTALLED_WITH_STORAGE-}" == "0" ]]; then
    # Explicit component-only packages (for example Factor/Monitor) consume
    # the shared credential but do not own either side of the Admin/Storage
    # signing contract, so there is no local counterpart to compare.
    return 0
  else
    # A legacy or partially written inventory must not silently bypass the
    # shared-auth guard.  If either side is present, fail closed until the
    # deployment is refreshed with an explicit component inventory.
    local sibling_prod sibling_storage
    sibling_prod="${MOOX_CONTROL_ROOT:-${MOOX_INSTALLED_CONTROL_ROOT:-$(dirname "${ROOT}")/prod}}/secrets/storage-internal-auth.env"
    sibling_storage="${MOOX_STORAGE_ROOT:-${MOOX_INSTALLED_STORAGE_ROOT:-$(dirname "${ROOT}")/storage}}/secrets/storage-internal-auth.env"
    if [[ -e "${ROOT}/secrets/storage-internal-auth.env" || -e "${sibling_prod}" || -e "${sibling_storage}" ]]; then
      log_line "storage-auth: component inventory is missing or ambiguous; refusing to report a healthy deployment"
      echo "storage-auth unavailable: refresh the deployment component inventory before health checks" >&2
      return 1
    fi
    return 0
  fi
  if [[ ! -r "${control_auth}" || ! -r "${storage_auth}" ]]; then
    log_line "storage-auth: control or storage credentials are missing or unreadable; refusing to report a healthy deployment"
    echo "storage-auth unavailable: control and Storage credentials must both be readable" >&2
    return 1
  fi
  if ! cmp -s "${control_auth}" "${storage_auth}"; then
    log_line "storage-auth: control and storage credentials differ; refusing to report a healthy deployment"
    echo "storage-auth mismatch: control and Storage credentials differ; run moox-storage-auth-rotate" >&2
    return 1
  fi
}

startup_grace_seconds() {
  local name="$1" configured default
  default=60
  # View restore may open and validate large DuckDB indexes before it starts
  # listening. Do not kill that healthy process just because /healthz is not
  # available during the restore window.
  [[ "${name}" == "storage-view" ]] && default=900
  configured="${MOOX_HEALTHCHECK_STARTUP_GRACE_SECONDS:-${default}}"
  if [[ "${configured}" =~ ^[0-9]+$ ]]; then
    printf '%s' "${configured}"
  else
    printf '%s' "${default}"
  fi
}

ensure_service() {
  local name="$1"
  local pid_file="${ROOT}/run/${name}.pid"
  local pid="" age grace failures threshold failure_file
  failure_file="${ROOT}/run/${name}.health-failures"
  if [[ -f "${pid_file}" ]]; then
    pid="$(cat "${pid_file}" 2>/dev/null || true)"
  fi

  if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
    if probe_service "${name}"; then
      rm -f "${failure_file}"
      echo "${name}: running pid=${pid} ready"
      return 0
    fi
    # Readiness is an operator signal, not a restart trigger. Storage View can
    # legitimately remain unready while a durable backlog drains for longer
    # than the startup grace; restarting it would discard progress and create
    # an endless recovery loop. Restart only when liveness also fails.
    if probe_liveness "${name}"; then
      rm -f "${failure_file}"
      log_line "${name}: process is live but readiness is degraded; leaving it running"
      echo "${name}: running pid=${pid} not ready"
      return 0
    fi
    age="$(ps -o etimes= -p "${pid}" 2>/dev/null | tr -d ' ' || true)"
    grace="$(startup_grace_seconds "${name}")"
    if [[ "${age}" =~ ^[0-9]+$ ]] && (( age < grace )) && listener_open "${name}"; then
      log_line "${name}: health probe unavailable during startup pid=${pid} age=${age}s grace=${grace}s"
      echo "${name}: starting pid=${pid} (${age}s/${grace}s grace)"
      return 0
    fi
    failures=0
    if [[ -r "${failure_file}" ]]; then
      failures="$(cat "${failure_file}" 2>/dev/null || printf '0')"
    fi
    [[ "${failures}" =~ ^[0-9]+$ ]] || failures=0
    failures=$((failures + 1))
    printf '%s\n' "${failures}" > "${failure_file}.tmp"
    mv -f "${failure_file}.tmp" "${failure_file}"
    threshold="${MOOX_HEALTHCHECK_FAILURE_THRESHOLD:-3}"
    [[ "${threshold}" =~ ^[1-9][0-9]*$ ]] || threshold=3
    if (( failures < threshold )); then
      log_line "${name}: health probe failed pid=${pid}; holding restart (${failures}/${threshold})"
      echo "${name}: health probe failed; holding restart (${failures}/${threshold})"
      return 0
    fi
    rm -f "${failure_file}"
    log_line "${name}: health probe failed pid=${pid}; restarting after ${failures} consecutive failures"
    echo "${name}: health probe failed; restarting after ${failures} consecutive failures"
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

ensure_caddy() {
  [[ -x "${ROOT}/lib/caddy-managed.sh" && -s "${ROOT}/config/caddy/edge.env" ]] || return 0
  local status
  status=$("${ROOT}/lib/caddy-managed.sh" status --deploy-dir "${ROOT}" 2>/dev/null || true)
  if [[ "${status}" != *'"running":true'* || "${status}" != *'"admin_healthy":true'* || "${status}" != *'"config_valid":true'* ]]; then
    log_line "caddy: edge process unhealthy; restarting"
    "${ROOT}/lib/caddy-managed.sh" start --deploy-dir "${ROOT}" 9>&- >>"${LOG_FILE}" 2>&1
  fi
  # Public mode uses the operating-system trust store. Internal mode keeps the
  # existing deployment CA workflow for private and loopback installations.
  set -a
  source "${ROOT}/config/caddy/edge.env"
  set +a
  local -a ca_args=()
  [[ "${MOOX_TLS_MODE:-internal}" != internal ]] || ca_args=(--cacert "${ROOT}/certs/caddy/root.crt")
  local edge_port="${MOOX_BROWSER_HTTPS_PORT:-9527}"
  [[ "${WITH_ADMIN}" == "1" ]] || edge_port="${MOOX_SERVICE_HTTPS_PORT:-11001}"
  curl --silent --show-error --max-time 5 "${ca_args[@]}" \
    --resolve "${MOOX_PUBLIC_HOST}:${edge_port}:127.0.0.1" \
    --output /dev/null "https://${MOOX_PUBLIC_HOST}:${edge_port}/"
  if [[ "${MOOX_TLS_MODE:-internal}" == public ]]; then
    local certificate
    certificate=$(openssl s_client -connect "127.0.0.1:${edge_port}" \
      -servername "${MOOX_PUBLIC_HOST}" -showcerts </dev/null 2>/dev/null |
      sed -n '/-----BEGIN CERTIFICATE-----/,/-----END CERTIFICATE-----/p' | sed -n '1,/-----END CERTIFICATE-----/p')
    [[ -n "${certificate}" ]] || return 1
    if ! printf '%s\n' "${certificate}" | openssl x509 -checkend 86400 -noout >/dev/null; then
      log_line "caddy: public certificate has less than 24 hours remaining"
      return 1
    fi
  fi
}

(
  flock -n 9 || exit 0
  failed=0
  if ! "${ROOT}/bin/moox-log-rotate" --root "${ROOT}" \
    --max-size-mb "${MOOX_LOCAL_LOG_MAX_SIZE_MB:-50}" \
    --backup-count "${MOOX_LOCAL_LOG_BACKUP_COUNT:-5}"; then
    log_line "local log rotation failed"
    failed=1
  fi
  check_storage_auth_files || failed=1
  ensure_caddy || failed=1
  for name in "${services[@]}"; do
    ensure_service "${name}" || failed=1
  done
  exit "${failed}"
) 9>"${ROOT}.maintenance.lock"
EOF

  perl -0pi -e "s#__WITH_STORAGE__#${WITH_STORAGE}#g; s#__WITH_STORAGE_NODE__#${WITH_STORAGE_NODE}#g; s#__WITH_ARCHIVE__#${WITH_ARCHIVE}#g; s#__WITH_EVENTBUS__#${WITH_EVENTBUS}#g; s#__WITH_CLOUDNODE__#${WITH_CLOUDNODE}#g; s#__WITH_COLLECTOR__#${WITH_COLLECTOR}#g; s#__WITH_FACTOR__#${WITH_FACTOR}#g; s#__WITH_STRATEGY__#${WITH_STRATEGY}#g; s#__WITH_TRADE__#${WITH_TRADE}#g; s#__WITH_MONITOR__#${WITH_MONITOR}#g; s#__WITH_WEB_HOST__#${WITH_WEB_HOST}#g; s#__WITH_ADMIN__#${WITH_ADMIN}#g; s#__WITH_GATEWAY__#${WITH_GATEWAY}#g; s#__PRESERVE_STORAGE_ROUTES__#${preserve_storage_routes}#g; s#__NODE_ID__#${NODE_ID}#g; s#__MONITOR_INSTANCE_ID__#${MONITOR_INSTANCE_ID}#g; s#__EVENTBUS_URL__#${EVENTBUS_URL_ENV}#g; s#__EVENTBUS_HOST__#${MOOX_EVENTBUS_HOST}#g; s#__EVENTBUS_PORT__#${MOOX_EVENTBUS_PORT}#g; s#__EVENTBUS_ENABLE_TLS__#${MOOX_EVENTBUS_ENABLE_TLS:-0}#g; s#__PUBLIC_HOST__#${PUBLIC_HOST}#g; s#__SCF_SERVICE_GATEWAY_TARGET__#${scf_service_gateway_target}#g; s#__SCF_STORAGE_RPC_GATEWAY_TARGET__#${scf_storage_rpc_gateway_target}#g" \
    "${STAGE_DIR}/start.sh" "${STAGE_DIR}/stop.sh" "${STAGE_DIR}/status.sh" "${STAGE_DIR}/healthcheck.sh"
  perl -0pi -e "s#__WITH_HOSTAGENT__#${WITH_HOSTAGENT}#g" \
    "${STAGE_DIR}/start.sh" "${STAGE_DIR}/stop.sh" "${STAGE_DIR}/status.sh" "${STAGE_DIR}/healthcheck.sh"
  chmod +x "${STAGE_DIR}/start.sh" "${STAGE_DIR}/stop.sh" "${STAGE_DIR}/status.sh" "${STAGE_DIR}/restart.sh" "${STAGE_DIR}/healthcheck.sh"
}

prepare_stage() {
  local storage_view_duckdb_memory_limit="${MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT:-256MB}"
  local storage_view_maintenance_policy_b64="${MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64:-}"
  local local_log_max_size_mb="${MOOX_LOCAL_LOG_MAX_SIZE_MB:-50}"
  local local_log_backup_count="${MOOX_LOCAL_LOG_BACKUP_COUNT:-5}"
  # Keep the storage route placement durable across lifecycle restarts.  A
  # control-only package may share a host with an independently deployed
  # Storage root; in that layout its generated start.sh must not disable the
  # already-active local Storage routes on the next Admin restart.
  local preserve_storage_routes="${MOOX_PRESERVE_STORAGE_ROUTES:-0}"
  [[ "${storage_view_duckdb_memory_limit}" =~ ^[1-9][0-9]*(KB|MB|GB|TB)$ ]] || \
    fail "MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT must be a positive KB/MB/GB/TB value"
  [[ "${local_log_max_size_mb}" =~ ^[1-9][0-9]*$ && "${local_log_max_size_mb}" -le 10240 ]] || \
    fail "MOOX_LOCAL_LOG_MAX_SIZE_MB must be between 1 and 10240"
  [[ "${local_log_backup_count}" =~ ^[1-9][0-9]*$ && "${local_log_backup_count}" -le 100 ]] || \
    fail "MOOX_LOCAL_LOG_BACKUP_COUNT must be between 1 and 100"
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    [[ -n "${storage_view_maintenance_policy_b64}" ]] || fail "MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64 is required for Storage"
    command -v python3 >/dev/null 2>&1 || fail "python3 is required to validate Storage View maintenance policy"
  fi
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
    "${STAGE_DIR}/strategy/config" \
    "${STAGE_DIR}/strategy/pyworker" \
    "${STAGE_DIR}/strategy/pysdk" \
    "${STAGE_DIR}/strategy/strategies/example" \
    "${STAGE_DIR}/trade/config" \
    "${STAGE_DIR}/python-runtime" \
    "${STAGE_DIR}/monitor/config" \
    "${STAGE_DIR}/examples" \
    "${STAGE_DIR}/data" \
    "${STAGE_DIR}/logs" \
    "${STAGE_DIR}/run"
  mkdir -p "${STAGE_DIR}/secrets" "${STAGE_DIR}/certs/gateway"
  if [[ -n "${MOOX_NOTIFICATION_WEBHOOK_URL+x}" ]]; then
    (umask 077; printf 'MOOX_NOTIFICATION_CHANNEL_TYPE=%q\nMOOX_NOTIFICATION_WEBHOOK_URL=%q\n' "${MOOX_NOTIFICATION_CHANNEL_TYPE-wecom}" "${MOOX_NOTIFICATION_WEBHOOK_URL}" >"${STAGE_DIR}/secrets/notification.env.next")
  fi
  if [[ -n "${MOOX_HEALTH_AUTH_VERSION:-}${MOOX_HEALTH_AUTH_ACCESS_KEY:-}${MOOX_HEALTH_AUTH_SECRET_KEY:-}" ]]; then
    [[ -n "${MOOX_HEALTH_AUTH_VERSION:-}" && -n "${MOOX_HEALTH_AUTH_ACCESS_KEY:-}" && -n "${MOOX_HEALTH_AUTH_SECRET_KEY:-}" ]] || \
      fail "health auth requires version, access key, and secret key"
    [[ "${MOOX_HEALTH_AUTH_VERSION}" != *$'\n'* && "${MOOX_HEALTH_AUTH_ACCESS_KEY}" != *$'\n'* && "${MOOX_HEALTH_AUTH_SECRET_KEY}" != *$'\n'* ]] || \
      fail "health auth values must contain exactly one line"
    (umask 077; {
      printf 'MOOX_HEALTH_AUTH_VERSION=%q\n' "${MOOX_HEALTH_AUTH_VERSION}"
      printf 'MOOX_HEALTH_AUTH_ACCESS_KEY=%q\n' "${MOOX_HEALTH_AUTH_ACCESS_KEY}"
      printf 'MOOX_HEALTH_AUTH_SECRET_KEY=%q\n' "${MOOX_HEALTH_AUTH_SECRET_KEY}"
    } >"${STAGE_DIR}/secrets/health-auth.env")
    chmod 0600 "${STAGE_DIR}/secrets/health-auth.env"
  fi
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
  for caller in collector factor monitor archive storage-view storage-primary strategy trade cloudnode moox-cli; do
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
    printf 'MOOX_GATEWAY_CALLER=admin-gateway\n'
    printf 'MOOX_GATEWAY_SERVICE_SECRET_KEY=%q\n' "${gateway_service_secret}"
  } >"${STAGE_DIR}/secrets/gateway-service.env"
  if [[ "${WITH_STORAGE}" -eq 1 || "${WITH_FACTOR}" -eq 1 || "${WITH_MONITOR}" -eq 1 || "${DEPLOY_PROFILE}" == "control" ]]; then
    local storage_primary_auth_secret="${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}"
    if [[ -z "${storage_primary_auth_secret}" ]]; then
      if [[ "${WITH_STORAGE}" -eq 1 || "${DEPLOY_PROFILE}" == "control" ]]; then
        storage_primary_auth_secret="$(generate_secret "${ROOT}/bin/moox-admin-cli" storage-primary-auth)"
      else
        fail "MOOX_STORAGE_PRIMARY_AUTH_SECRET is required when packaging Factor or Monitor without Storage"
      fi
    fi
    [[ "${storage_primary_auth_secret}" != *$'\n'* && "${storage_primary_auth_secret}" != *$'\r'* ]] || \
      fail "storage Primary auth secret must contain exactly one line"
    {
      printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=%q\n' "${storage_primary_auth_secret}"
    } >"${STAGE_DIR}/secrets/storage-internal-auth.env"
    if [[ "${WITH_STORAGE}" -eq 1 ]]; then
      local storage_node_auth_secret="${MOOX_STORAGE_NODE_AUTH_SECRET:-}"
      if [[ -z "${storage_node_auth_secret}" ]]; then
        storage_node_auth_secret="$(generate_secret "${ROOT}/bin/moox-admin-cli" storage-node-auth)"
      fi
      [[ "${storage_node_auth_secret}" != *$'\n'* && "${storage_node_auth_secret}" != *$'\r'* ]] || \
        fail "storage DataNode auth secret must contain exactly one line"
      (umask 077; printf 'MOOX_STORAGE_NODE_AUTH_SECRET=%q\n' "${storage_node_auth_secret}" >"${STAGE_DIR}/secrets/storage-node-auth.env")
    fi
    local storage_view_auth_secret="${MOOX_STORAGE_VIEW_AUTH_SECRET:-}"
    if [[ -z "${storage_view_auth_secret}" ]]; then
      if [[ "${WITH_STORAGE}" -eq 1 || "${DEPLOY_PROFILE}" == "control" ]]; then
        storage_view_auth_secret="$(generate_secret "${ROOT}/bin/moox-admin-cli" storage-view-auth)"
      else
        fail "MOOX_STORAGE_VIEW_AUTH_SECRET is required when packaging Factor or Monitor without Storage"
      fi
    fi
    [[ "${storage_view_auth_secret}" != *$'\n'* && "${storage_view_auth_secret}" != *$'\r'* ]] || \
      fail "storage View auth secret must contain exactly one line"
    printf 'MOOX_STORAGE_VIEW_AUTH_SECRET=%q\n' "${storage_view_auth_secret}" >>"${STAGE_DIR}/secrets/storage-internal-auth.env"
    chmod 0600 "${STAGE_DIR}/secrets/storage-internal-auth.env"
  fi
  cat >"${STAGE_DIR}/secrets/gateway-credentials.json" <<'EOF'
{"version":1,"credentials":[{"key_id":"moox-gateway-service","caller":"admin-gateway","secret_file":"gateway-service.key"},{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"},{"key_id":"factor","caller":"factor","secret_file":"gateway-factor.key"},{"key_id":"monitor","caller":"monitor","secret_file":"gateway-monitor.key"},{"key_id":"archive","caller":"archive","secret_file":"gateway-archive.key"},{"key_id":"storage-view","caller":"storage-view","secret_file":"gateway-storage-view.key"},{"key_id":"storage-primary","caller":"storage-primary","secret_file":"gateway-storage-primary.key"},{"key_id":"strategy","caller":"strategy","secret_file":"gateway-strategy.key"},{"key_id":"trade","caller":"trade","secret_file":"gateway-trade.key"},{"key_id":"cloudnode","caller":"cloudnode","secret_file":"gateway-cloudnode.key"},{"key_id":"moox-cli","caller":"moox-cli","secret_file":"gateway-moox-cli.key"}]}
EOF
  {
    printf 'MOOX_GATEWAY_SERVICE_KEY_ID=moox-cli\n'
    printf 'MOOX_GATEWAY_CALLER=moox-cli\n'
    printf 'MOOX_GATEWAY_SERVICE_SECRET_KEY=%q\n' "$(tr -d '\r\n' <"${STAGE_DIR}/secrets/gateway-moox-cli.key")"
    printf 'MOOX_GATEWAY_TARGET_NODE=%q\n' "${NODE_ID}"
    printf 'MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID=collector\n'
    printf 'MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY=%q\n' "$(tr -d '\r\n' <"${STAGE_DIR}/secrets/gateway-collector.key")"
    printf 'MOOX_SERVICE_GATEWAY_TARGET=ip://127.0.0.1:11003\n'
    printf 'MOOX_GATEWAY_CA_FILE=%q\n' "${DEPLOY_DIR}/certs/gateway/peers.pem"
  } >"${STAGE_DIR}/secrets/gateway-moox-cli.env"
  chmod 0600 "${STAGE_DIR}/secrets/gateway-control.env" "${STAGE_DIR}/secrets/gateway-service.env" "${STAGE_DIR}/secrets/gateway-moox-cli.env" "${STAGE_DIR}/secrets/gateway-credentials.json"
  mkdir -p "${STAGE_DIR}/lib" "${STAGE_DIR}/config/caddy"
  cat >"${STAGE_DIR}/config/log-rotation.env" <<EOF
MOOX_LOCAL_LOG_MAX_SIZE_MB=${local_log_max_size_mb}
MOOX_LOCAL_LOG_BACKUP_COUNT=${local_log_backup_count}
EOF
  chmod 0600 "${STAGE_DIR}/config/log-rotation.env"
  cat >"${STAGE_DIR}/config/components.env" <<EOF
MOOX_INSTALLED_WITH_STORAGE=${WITH_STORAGE}
MOOX_INSTALLED_WITH_STORAGE_NODE=${WITH_STORAGE_NODE}
MOOX_INSTALLED_WITH_ARCHIVE=${WITH_ARCHIVE}
MOOX_INSTALLED_WITH_EVENTBUS=${WITH_EVENTBUS}
MOOX_INSTALLED_WITH_CLOUDNODE=${WITH_CLOUDNODE}
MOOX_INSTALLED_WITH_COLLECTOR=${WITH_COLLECTOR}
MOOX_INSTALLED_WITH_FACTOR=${WITH_FACTOR}
MOOX_INSTALLED_WITH_STRATEGY=${WITH_STRATEGY}
MOOX_INSTALLED_WITH_TRADE=${WITH_TRADE}
MOOX_INSTALLED_WITH_MONITOR=${WITH_MONITOR}
MOOX_INSTALLED_WITH_HOSTAGENT=${WITH_HOSTAGENT}
MOOX_INSTALLED_WITH_WEB_HOST=${WITH_WEB_HOST}
MOOX_INSTALLED_WITH_ADMIN=${WITH_ADMIN}
MOOX_INSTALLED_WITH_GATEWAY=${WITH_GATEWAY}
MOOX_INSTALLED_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=${storage_view_duckdb_memory_limit}
MOOX_INSTALLED_PRESERVE_STORAGE_ROUTES=${preserve_storage_routes}
MOOX_INSTALLED_CONTROL_ROOT=$(printf '%q' "${MOOX_CONTROL_ROOT:-}")
MOOX_INSTALLED_STORAGE_ROOT=$(printf '%q' "${MOOX_STORAGE_ROOT:-}")
MOOX_WITH_STORAGE=\${MOOX_WITH_STORAGE:-\${MOOX_INSTALLED_WITH_STORAGE}}
MOOX_WITH_STORAGE_NODE=\${MOOX_WITH_STORAGE_NODE:-\${MOOX_INSTALLED_WITH_STORAGE_NODE}}
MOOX_WITH_ARCHIVE=\${MOOX_WITH_ARCHIVE:-\${MOOX_INSTALLED_WITH_ARCHIVE}}
MOOX_WITH_EVENTBUS=\${MOOX_WITH_EVENTBUS:-\${MOOX_INSTALLED_WITH_EVENTBUS}}
MOOX_WITH_CLOUDNODE=\${MOOX_WITH_CLOUDNODE:-\${MOOX_INSTALLED_WITH_CLOUDNODE}}
MOOX_WITH_COLLECTOR=\${MOOX_WITH_COLLECTOR:-\${MOOX_INSTALLED_WITH_COLLECTOR}}
MOOX_WITH_FACTOR=\${MOOX_WITH_FACTOR:-\${MOOX_INSTALLED_WITH_FACTOR}}
MOOX_WITH_STRATEGY=\${MOOX_WITH_STRATEGY:-\${MOOX_INSTALLED_WITH_STRATEGY}}
MOOX_WITH_TRADE=\${MOOX_WITH_TRADE:-\${MOOX_INSTALLED_WITH_TRADE}}
MOOX_WITH_MONITOR=\${MOOX_WITH_MONITOR:-\${MOOX_INSTALLED_WITH_MONITOR}}
MOOX_WITH_HOSTAGENT=\${MOOX_WITH_HOSTAGENT:-\${MOOX_INSTALLED_WITH_HOSTAGENT}}
MOOX_WITH_WEB_HOST=\${MOOX_WITH_WEB_HOST:-\${MOOX_INSTALLED_WITH_WEB_HOST}}
MOOX_WITH_ADMIN=\${MOOX_WITH_ADMIN:-\${MOOX_INSTALLED_WITH_ADMIN}}
MOOX_WITH_GATEWAY=\${MOOX_WITH_GATEWAY:-\${MOOX_INSTALLED_WITH_GATEWAY}}
MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=\${MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT:-\${MOOX_INSTALLED_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT}}
MOOX_PRESERVE_STORAGE_ROUTES=\${MOOX_PRESERVE_STORAGE_ROUTES:-\${MOOX_INSTALLED_PRESERVE_STORAGE_ROUTES}}
EOF
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
    if [[ -n "${MOOX_CADDY_ARCHIVE_CACHE:-}" ]]; then
      [[ -f "${MOOX_CADDY_ARCHIVE_CACHE}" && ! -L "${MOOX_CADDY_ARCHIVE_CACHE}" ]] || \
        fail "MOOX_CADDY_ARCHIVE_CACHE must name a regular file"
      log "use cached pinned Caddy ${caddy_asset}"
      cp "${MOOX_CADDY_ARCHIVE_CACHE}" "${caddy_archive}"
    else
      log "download pinned Caddy ${caddy_asset} for deployment bundle"
      curl -fL --retry 3 --connect-timeout 10 --max-time 180 \
        -o "${caddy_archive}" "https://github.com/caddyserver/caddy/releases/download/v2.11.4/${caddy_asset}"
    fi
    expected_caddy_sha=$(awk -v asset="${caddy_asset}" '$2 == asset {print $1}' "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt")
    [[ -n "${expected_caddy_sha}" ]] || fail "missing checksum for ${caddy_asset}"
    actual_caddy_sha=$(shasum -a 512 "${caddy_archive}" | awk '{print $1}')
    [[ "${actual_caddy_sha}" == "${expected_caddy_sha}" ]] || fail "Caddy archive checksum mismatch for ${caddy_asset}"
  fi
  if [[ "${WITH_ADMIN}" -eq 1 ]]; then
    mkdir -p "${STAGE_DIR}/admin/config"
    if [[ "${TLS_MODE_RESOLVED}" == public ]]; then
      cp "${ROOT}/deploy/caddy/Caddyfile.public" "${STAGE_DIR}/config/caddy/Caddyfile.next"
    else
      cp "${ROOT}/deploy/caddy/Caddyfile" "${STAGE_DIR}/config/caddy/Caddyfile.next"
    fi
  else
    if [[ "${TLS_MODE_RESOLVED}" == public ]]; then
      cp "${ROOT}/deploy/caddy/Caddyfile.public.no-admin" "${STAGE_DIR}/config/caddy/Caddyfile.next"
    else
      cp "${ROOT}/deploy/caddy/Caddyfile.no-admin" "${STAGE_DIR}/config/caddy/Caddyfile.next"
    fi
  fi
  chmod +x "${STAGE_DIR}/lib/caddy-managed.sh" "${STAGE_DIR}/lib/loopback-listeners.sh" "${STAGE_DIR}/lib/install-caddy-ca.sh"
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    mkdir -p "${STAGE_DIR}/storage/config" "${STAGE_DIR}/storage/schema" "${STAGE_DIR}/storage-view/config"
  if [[ "${WITH_STORAGE_NODE}" -eq 1 ]]; then
      mkdir -p "${STAGE_DIR}/storage-node/config"
    fi
  fi

  if [[ "${WITH_GATEWAY}" -eq 1 ]]; then
    copy_required_binary "moox-gateway"
    copy_required_binary "moox-gateway-cli"
  fi
  if [[ "${WITH_ADMIN}" -eq 1 || "${WITH_MONITOR}" -eq 1 || "${WITH_STORAGE}" -eq 1 || \
        "${WITH_COLLECTOR}" -eq 1 || "${WITH_TRADE}" -eq 1 ]]; then
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
  fi
  if [[ "${WITH_FACTOR}" -eq 1 ]]; then
    copy_required_binary "moox-factor"
    copy_required_binary "moox-factor-cli"
    install -m 0755 "${ROOT}/scripts/moox-factor-run-once.sh" "${STAGE_DIR}/bin/moox-factor-run-once"
  fi
  install -m 0755 "${ROOT}/scripts/moox-storage-auth-check.sh" "${STAGE_DIR}/bin/moox-storage-auth-check"
  install -m 0755 "${ROOT}/scripts/moox-storage-auth-rotate.sh" "${STAGE_DIR}/bin/moox-storage-auth-rotate"
  install -m 0755 "${ROOT}/scripts/moox-log-rotate.sh" "${STAGE_DIR}/bin/moox-log-rotate"
  if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
    copy_required_binary "moox-strategy"
    copy_required_binary "moox-strategy-cli"
  fi
  if [[ "${WITH_TRADE}" -eq 1 ]]; then
    copy_required_binary "moox-trade"
    copy_required_binary "moox-trade-cli"
  fi
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    copy_required_binary "moox-monitor"
    copy_required_binary "moox-monitor-cli"
  fi
  if [[ "${WITH_HOSTAGENT}" -eq 1 ]]; then
    copy_required_binary "moox-host-agent"
  fi
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    copy_required_binary "moox-storage-primary"
    copy_required_binary "moox-storage-view"
    copy_required_binary "moox-storage-cli"
    if [[ "${WITH_STORAGE_NODE}" -eq 1 ]]; then
      copy_required_binary "moox-storage-node"
    fi
  fi
  write_storage_build_provenance
  copy_optional_web_host

  cp -R "${ROOT}/modules/gateway/config/." "${STAGE_DIR}/gateway/config/"
  cp "${ROOT}/modules/cli/config/cli.yaml" "${STAGE_DIR}/config/cli.yaml"
  mkdir -p "${STAGE_DIR}/config/doctor"
  cp "${ROOT}/packages/doctor/components.yaml" "${STAGE_DIR}/config/doctor/components.yaml"
  shasum -a 256 "${STAGE_DIR}/config/doctor/components.yaml" | awk '{print "sha256:" $1}' > "${STAGE_DIR}/config/doctor/components.yaml.sha256"
	cp "${ROOT}/packages/doctor/report.schema.json" "${STAGE_DIR}/config/doctor/report.schema.json"
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
  if [[ "${WITH_TRADE}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/trade/config/." "${STAGE_DIR}/trade/config/"
  fi
  if [[ -f "${ROOT}/custom.toml" && -x "${STAGE_DIR}/bin/moox-cli" && \
        ("${WITH_TRADE}" -eq 1 || "${WITH_COLLECTOR}" -eq 1) ]]; then
    render_args=(--file "${ROOT}/custom.toml")
    if [[ "${WITH_TRADE}" -eq 1 ]]; then
      render_args+=(--trade-output "${STAGE_DIR}/trade/config/app.yaml")
    fi
    if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
      render_args+=(--collector-output "${STAGE_DIR}/collector/config/app.yaml")
    fi
    render_args+=(--node-id "${NODE_ID}")
    log "render Trade/Collector DNS resolver runtime config from custom.toml"
    # The staged CLI is built for the deployment target. When packaging a
    # Linux bundle on macOS, execute the host tool instead of trying to run
    # the staged ELF binary locally; the staged target CLI remains in the
    # archive for operators and remote lifecycle scripts.
    runtime_config_cli=("${STAGE_DIR}/bin/moox-cli")
    if [[ "${TARGET_GOOS}" != "${HOST_GOOS}" || "${TARGET_GOARCH}" != "${HOST_GOARCH}" ]]; then
      runtime_config_cli=(go run ./modules/cli/cmd/moox-cli)
    fi
    (cd "${ROOT}" && "${runtime_config_cli[@]}" setup render-runtime-config "${render_args[@]}") \
      >"${STAGE_DIR}/config/render-runtime-config.json"
  fi
  if [[ "${WITH_FACTOR}" -eq 1 || "${WITH_STRATEGY}" -eq 1 ]]; then
    cp -R "${ROOT}/packages/pyruntime/python/." "${STAGE_DIR}/python-runtime/"
    find "${STAGE_DIR}/python-runtime" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
    find "${STAGE_DIR}/python-runtime" -type f -name '*.pyc' -delete
  fi
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/monitor/config/." "${STAGE_DIR}/monitor/config/"
    if [[ -n "${PUBLIC_HOST}" ]]; then
      perl -pi -e 'if (/name:\s*trpc\.moox\.monitor\.Health/) { $health = 1 } elsif ($health && /ip:\s*127\.0\.0\.1/) { s#127\.0\.0\.1#0.0.0.0#; $health = 0 }' \
        "${STAGE_DIR}/monitor/config/trpc_go.yaml"
    fi
  fi
  if [[ "${WITH_HOSTAGENT}" -eq 1 ]]; then
    mkdir -p "${STAGE_DIR}/hostagent/config"
    cp -R "${ROOT}/modules/hostagent/config/." "${STAGE_DIR}/hostagent/config/"
    # HostAgent normally reports the operating-system hostname. When it differs
    # from the SSH host name, the Host Workbench cannot associate the live
    # resource sample with its configured host row and renders a duplicate
    # monitor-only entry. Use the deployment node identity as the default
    # display/matching name; an explicitly configured host_name remains intact.
    HOST_AGENT_NAME="${NODE_ID}" perl -0pi -e 's#(?m)^host_name:\s*""\s*$#host_name: $ENV{HOST_AGENT_NAME}#' \
      "${STAGE_DIR}/hostagent/config/app.yaml"
    # The central deployment exports the role credential outside the release
    # tree. Point HostAgent at that durable file so credentials are never
    # copied into the package or process arguments.
    perl -0pi -e 's#eventbus_config:\s*.*#eventbus_config: ~/.config/moox/eventbus/hostagent-publisher.yaml#' \
      "${STAGE_DIR}/hostagent/config/app.yaml"
  fi
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/storage/config/." "${STAGE_DIR}/storage/config/"
    # The bundled primary process owns the primary role only. The View role
    # has its own process and its own storage business config.
    cp "${ROOT}/modules/storage/config/storage.primary.yaml" "${STAGE_DIR}/storage/config/storage.yaml"
    if [[ "${WITH_STORAGE_NODE}" -eq 1 ]]; then
      cp "${ROOT}/modules/storage/config/trpc_go.node.yaml" "${STAGE_DIR}/storage-node/config/trpc_go.yaml"
      cp "${ROOT}/modules/storage/config/storage.node.yaml" "${STAGE_DIR}/storage-node/config/storage.yaml"
    fi
    rm -f "${STAGE_DIR}/storage/config/trpc_go.node.yaml" "${STAGE_DIR}/storage/config/storage.node.yaml"
    cp "${STAGE_DIR}/storage/config/trpc_go.primary.yaml" "${STAGE_DIR}/storage/config/trpc_go.yaml"
    printf '\n' >> "${STAGE_DIR}/storage/config/trpc_go.yaml"
    cat "${STAGE_DIR}/storage/config/storage.primary.yaml" >> "${STAGE_DIR}/storage/config/trpc_go.yaml"
    rm -f "${STAGE_DIR}/storage/config/trpc_go.primary.yaml" "${STAGE_DIR}/storage/config/storage.primary.yaml"
    cp "${ROOT}/modules/storage/config/storage_view/trpc_go.yaml" "${STAGE_DIR}/storage-view/config/trpc_go.yaml"
    policy_tmp="${STAGE_DIR}/storage-view/config/maintenance.json.tmp"
    printf '%s' "${storage_view_maintenance_policy_b64}" | python3 -c 'import base64, json, sys; raw = base64.b64decode(sys.stdin.read(), validate=True); obj = json.loads(raw); print(json.dumps(obj, sort_keys=True, indent=2))' >"${policy_tmp}" || fail "invalid Storage View maintenance policy"
    python3 -m json.tool "${policy_tmp}" >/dev/null || fail "invalid Storage View maintenance policy JSON"
    mv "${policy_tmp}" "${STAGE_DIR}/storage-view/config/maintenance.json"
    chmod 0600 "${STAGE_DIR}/storage-view/config/maintenance.json"
    rm -rf "${STAGE_DIR}/storage/config/storage_view"
    cp "${ROOT}/modules/storage/schema/metadata.sql" "${STAGE_DIR}/storage/schema/metadata.sql"
  fi
  cp -R "${ROOT}/examples/." "${STAGE_DIR}/examples/"
  cp "${ROOT}/examples/setup/default/dataset-health-policy.yaml" "${STAGE_DIR}/config/dataset-health-policy.yaml"
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

persist_selected_components() {
  local file="$1" assignment key enabled
  shift
  mkdir -p "$(dirname "${file}")"
  touch "${file}"
  for assignment in "$@"; do
    key="${assignment%%:*}"
    enabled="${assignment#*:}"
    [[ "${enabled}" == "1" ]] || continue
    if grep -q "^${key}=" "${file}"; then
      perl -0pi -e "s#^${key}=.*\$#${key}=1#m" "${file}"
    else
      printf '%s=1\n' "${key}" >>"${file}"
    fi
  done
  chmod 0600 "${file}"
}

stop_foreign_gateway() {
  [[ "${WITH_GATEWAY}" == "1" ]] || return 0
  local proc pid exe cwd start_time current_start
  for proc in /proc/[0-9]*; do
    [[ -d "${proc}" ]] || continue
    pid="${proc##*/}"
    [[ "${pid}" != "$$" ]] || continue
    exe="$(readlink "${proc}/exe" 2>/dev/null || true)"
    [[ "${exe}" == *"/moox-gateway" || "${exe}" == *"/moox-gateway (deleted)" ]] || continue
    [[ "${exe}" == "${DEPLOY_DIR}/bin/moox-gateway" || "${exe}" == "${DEPLOY_DIR}/bin/moox-gateway (deleted)" ]] && continue
    cwd="$(readlink "${proc}/cwd" 2>/dev/null || true)"
    if [[ -r "${cwd}/config/app.yaml" ]] && ! grep -Eq "^  id: ${NODE_ID}$" "${cwd}/config/app.yaml"; then
      continue
    fi
    start_time="$(awk '{print $22}' "${proc}/stat" 2>/dev/null || true)"
    if [[ "${NO_START}" == "1" ]]; then
      echo "foreign Gateway process is running from ${exe}; refuse --no-start deployment" >&2
      exit 1
    fi
    echo "stopping foreign Gateway process pid=${pid} exe=${exe}" >&2
    kill "${pid}" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    current_start="$(awk '{print $22}' "/proc/${pid}/stat" 2>/dev/null || true)"
    [[ -n "${start_time}" && "${current_start}" == "${start_time}" ]] || continue
    kill -9 "${pid}" 2>/dev/null || true
  done
}

sync_local_stage() {
  local deploy_dir component_overlay="${COMPONENT_OVERLAY}"
  deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
  mkdir -p "${deploy_dir}"
  if command -v flock >/dev/null 2>&1; then
    exec 8>"${deploy_dir}.maintenance.lock"
    flock 8
  fi

  # The control package signs browser-facing Storage BFF requests, while the
  # independent storage package verifies them.  A storage deployment must use
  # the control package's exact credential file; otherwise the deployment can
  # look healthy but every View query fails with "invalid view HMAC".  Check
  # this before stopping any existing service or mutating the deployment.
  if [[ -r "${STAGE_DIR}/secrets/storage-internal-auth.env" ]]; then
    local storage_root counterpart_auth
    if [[ "${WITH_STORAGE}" == "1" && "${WITH_ADMIN}" == "0" ]]; then
        counterpart_auth="${MOOX_CONTROL_ROOT:-$(dirname "${deploy_dir}")/prod}/secrets/storage-internal-auth.env"
    elif [[ "${WITH_ADMIN}" == "1" && "${WITH_STORAGE}" == "0" ]]; then
        storage_root="${MOOX_STORAGE_ROOT:-$(dirname "${deploy_dir}")/storage}"
        counterpart_auth="${storage_root}/secrets/storage-internal-auth.env"
    else
      counterpart_auth=""
    fi
    if [[ -n "${counterpart_auth}" ]]; then
      [[ -r "${counterpart_auth}" ]] ||
        fail "storage_internal_auth_missing_preflight: counterpart credentials are missing at ${counterpart_auth}"
      cmp -s "${STAGE_DIR}/secrets/storage-internal-auth.env" "${counterpart_auth}" ||
        fail "storage_internal_auth_mismatch_preflight: staged credentials differ from ${counterpart_auth}; synchronize credentials before deployment"
    fi
  fi
  local has_selected_workload=0
	  if [[ "${WITH_ARCHIVE}" -eq 1 || "${WITH_EVENTBUS}" -eq 1 || "${WITH_CLOUDNODE}" -eq 1 || \
	    "${WITH_COLLECTOR}" -eq 1 || "${WITH_FACTOR}" -eq 1 || "${WITH_STRATEGY}" -eq 1 || \
	    "${WITH_TRADE}" -eq 1 || "${WITH_MONITOR}" -eq 1 || "${WITH_WEB_HOST}" -eq 1 || \
	    "${WITH_HOSTAGENT}" -eq 1 || "${WITH_GATEWAY}" -eq 1 ]]; then
    has_selected_workload=1
  fi
  if [[ "${component_overlay}" -eq 1 ]]; then
    [[ "${WITH_ADMIN}" -eq 0 && "${WITH_STORAGE}" -eq 0 && "${has_selected_workload}" -eq 1 ]] ||
      fail "--component-overlay requires --no-admin, --no-storage, and at least one selected component"
    [[ -x "${deploy_dir}/start.sh" ]] || fail "--component-overlay requires an existing executable ${deploy_dir}/start.sh"
    [[ -r "${deploy_dir}/config/components.env" ]] || fail "--component-overlay requires lifecycle component inventory at ${deploy_dir}/config/components.env"
    grep -Fq 'MOOX_INSTALLED_WITH_' "${deploy_dir}/config/components.env" || fail "installed component inventory is too old for --component-overlay; run a full deployment first"
    grep -Fq 'MOOX_INSTALLED_WITH_' "${deploy_dir}/start.sh" || fail "installed lifecycle is too old for --component-overlay; run a full deployment first"
    [[ "${RESET_DATA}" -eq 0 ]] || fail "--reset-data cannot be used with --component-overlay"
    log "component overlay requested; preserve the installed control plane, shared credentials, and lifecycle scripts"
  elif [[ "${WITH_ADMIN}" -eq 0 && "${WITH_STORAGE}" -eq 0 && "${has_selected_workload}" -eq 1 && \
    ( -d "${deploy_dir}/admin" || -x "${deploy_dir}/bin/moox-admin" ) ]]; then
    fail "existing control plane detected; use --component-overlay for a partial update"
  fi

  if [[ "${component_overlay}" -eq 0 && ( -e "${deploy_dir}/config/caddy/edge.env" || -e "${deploy_dir}/config/caddy/Caddyfile" || -e "${deploy_dir}/run/caddy.pid" ) ]]; then
    [[ "${NO_START}" -eq 0 ]] || fail "--no-start refuses to replace an existing managed Caddy deployment"
    [[ -n "${PUBLIC_HOST}" ]] || fail "existing managed Caddy deployment requires --public-host"
  fi

  # All preflight checks above must pass before we stop or replace a running
  # Gateway. This keeps a rejected package from causing avoidable downtime.
  prepare_local_gateway_rollback "${deploy_dir}"
  DEPLOY_DIR="${deploy_dir}" stop_foreign_gateway

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
      if [[ "${WITH_TRADE}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" trade || true
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
      if [[ "${WITH_HOSTAGENT}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" hostagent || true
      fi
      if [[ "${WITH_GATEWAY}" -eq 1 ]]; then
        MOOX_WITH_GATEWAY=1 "${deploy_dir}/stop.sh" gateway || true
      fi
      if [[ "${WITH_ARCHIVE}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" archive || true
      fi
      if [[ "${WITH_WEB_HOST}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" web-host || true
      fi
      if [[ "${WITH_ADMIN}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" admin || true
      fi
    fi
  fi

  if [[ "${RESET_DATA}" -eq 1 ]]; then
    remove_resettable_data "${deploy_dir}/data"
  fi

  if command -v rsync >/dev/null 2>&1; then
    local rsync_excludes=(--exclude '/data/' --exclude '/logs/' --exclude '/run/' --exclude '/secrets/' --exclude '/certs/' --exclude '/config/caddy/Caddyfile' --exclude '/config/caddy/Caddyfile.rollback')
    if [[ "${component_overlay}" -eq 1 ]]; then
      rsync_excludes+=(--exclude '/admin/' --exclude '/examples/' \
        --exclude '/config/caddy/' --exclude '/config/components.env' \
        --exclude '/bin/moox-admin' --exclude '/bin/moox-admin-cli' --exclude '/bin/moox-cli')
      if [[ "${ENABLE_CLS}" -eq 0 ]]; then
        # A non-CLS component overlay must preserve the resource IDs used by
        # already-installed services. Only an explicit CLS preflight may
        # replace this generated file.
        rsync_excludes+=(--exclude '/config/resources.env')
      fi
      if [[ "${WITH_GATEWAY}" -eq 0 ]]; then
        rsync_excludes+=(--exclude '/gateway/' --exclude '/bin/moox-gateway' --exclude '/bin/moox-gateway-cli')
      fi
    fi
    if [[ "${WITH_STORAGE}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/storage/' --exclude '/storage-view/' --exclude '/storage-node/' \
        --exclude '/build-provenance.json' --exclude '/reset-storage-view-indexes.sh' \
        --exclude '/bin/moox-storage' --exclude '/bin/moox-storage-cli' --exclude '/bin/moox-storage-primary' --exclude '/bin/moox-storage-view' --exclude '/bin/moox-storage-node')
    fi
  if [[ "${WITH_EVENTBUS}" -eq 0 ]]; then
    rsync_excludes+=(--exclude '/eventbus/' --exclude '/bin/moox-eventbus')
    if [[ "${component_overlay}" -eq 1 ]]; then
      # A component overlay which does not own EventBus must preserve the
      # installed listener contract for the next restart.
      rsync_excludes+=(--exclude '/config/runtime.env')
    fi
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
      rsync_excludes+=(--exclude '/factor/' --exclude '/bin/moox-factor' --exclude '/bin/moox-factor-cli' --exclude '/bin/moox-factor-run-once')
    fi
    if [[ "${WITH_STRATEGY}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/strategy/' --exclude '/bin/moox-strategy' --exclude '/bin/moox-strategy-cli')
    fi
    if [[ "${WITH_TRADE}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/trade/' --exclude '/bin/moox-trade' --exclude '/bin/moox-trade-cli')
    fi
    if [[ "${WITH_FACTOR}" -eq 0 && "${WITH_STRATEGY}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/python-runtime/')
    fi
	    if [[ "${WITH_MONITOR}" -eq 0 ]]; then
	      rsync_excludes+=(--exclude '/monitor/' --exclude '/bin/moox-monitor' --exclude '/bin/moox-monitor-cli')
	    fi
	    if [[ "${WITH_HOSTAGENT}" -eq 0 ]]; then
	      rsync_excludes+=(--exclude '/hostagent/' --exclude '/bin/moox-host-agent' --exclude '/bin/moox-host-agent-cli')
	    fi
    if [[ "${WITH_WEB_HOST}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/bin/moox-web-host')
    fi
    rsync -a --delete \
      "${rsync_excludes[@]}" \
      "${STAGE_DIR}/" "${deploy_dir}/"
  else
    if [[ "${component_overlay}" -eq 0 ]]; then
      rm -rf "${deploy_dir}/admin" "${deploy_dir}/examples" \
        "${deploy_dir}/start.sh" "${deploy_dir}/stop.sh" "${deploy_dir}/restart.sh" "${deploy_dir}/status.sh" "${deploy_dir}/healthcheck.sh"
      rm -f "${deploy_dir}/bin/moox-admin" "${deploy_dir}/bin/moox-admin-cli" \
        "${deploy_dir}/bin/moox-cli"
    fi
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
      rm -f "${deploy_dir}/bin/moox-factor" "${deploy_dir}/bin/moox-factor-cli" "${deploy_dir}/bin/moox-factor-run-once"
    fi
    if [[ "${WITH_STRATEGY}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/strategy"
      rm -f "${deploy_dir}/bin/moox-strategy" "${deploy_dir}/bin/moox-strategy-cli"
    fi
    if [[ "${WITH_TRADE}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/trade"
      rm -f "${deploy_dir}/bin/moox-trade" "${deploy_dir}/bin/moox-trade-cli"
    fi
    if [[ "${WITH_MONITOR}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/monitor"
      rm -f "${deploy_dir}/bin/moox-monitor" "${deploy_dir}/bin/moox-monitor-cli"
    fi
    if [[ "${component_overlay}" -eq 1 && "${WITH_GATEWAY}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/gateway"
      rm -f "${deploy_dir}/bin/moox-gateway" "${deploy_dir}/bin/moox-gateway-cli"
    fi
    if [[ "${WITH_STORAGE}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/storage" "${deploy_dir}/storage-view" "${deploy_dir}/storage-node"
      rm -f "${deploy_dir}/bin/moox-storage" "${deploy_dir}/bin/moox-storage-cli" \
        "${deploy_dir}/bin/moox-storage-primary" \
        "${deploy_dir}/bin/moox-storage-view" \
        "${deploy_dir}/bin/moox-storage-node"
    fi
    if [[ "${component_overlay}" -eq 1 ]]; then
      local overlay_excludes=(
        --exclude='./admin' --exclude='./examples'
        --exclude='./bin/moox-admin' --exclude='./bin/moox-admin-cli' --exclude='./bin/moox-cli'
        --exclude='./secrets' --exclude='./certs' --exclude='./config/caddy' --exclude='./config/components.env'
      )
      if [[ "${WITH_GATEWAY}" -eq 0 ]]; then
        overlay_excludes+=(--exclude='./gateway' --exclude='./bin/moox-gateway' --exclude='./bin/moox-gateway-cli')
      fi
      if [[ "${WITH_EVENTBUS}" -eq 0 ]]; then
        overlay_excludes+=(--exclude='./config/runtime.env')
      fi
      tar -C "${STAGE_DIR}" -cf - "${overlay_excludes[@]}" . | tar -C "${deploy_dir}" -xf -
    else
      cp -R "${STAGE_DIR}/." "${deploy_dir}/"
    fi
  fi

  if [[ "${component_overlay}" -eq 1 ]]; then
    persist_selected_components "${deploy_dir}/config/components.env" \
      "MOOX_INSTALLED_WITH_ARCHIVE:${WITH_ARCHIVE}" "MOOX_INSTALLED_WITH_EVENTBUS:${WITH_EVENTBUS}" \
      "MOOX_INSTALLED_WITH_CLOUDNODE:${WITH_CLOUDNODE}" "MOOX_INSTALLED_WITH_COLLECTOR:${WITH_COLLECTOR}" \
      "MOOX_INSTALLED_WITH_FACTOR:${WITH_FACTOR}" "MOOX_INSTALLED_WITH_STRATEGY:${WITH_STRATEGY}" \
      "MOOX_INSTALLED_WITH_TRADE:${WITH_TRADE}" "MOOX_INSTALLED_WITH_MONITOR:${WITH_MONITOR}" \
      "MOOX_INSTALLED_WITH_HOSTAGENT:${WITH_HOSTAGENT}" \
      "MOOX_INSTALLED_WITH_WEB_HOST:${WITH_WEB_HOST}" "MOOX_INSTALLED_WITH_GATEWAY:${WITH_GATEWAY}"
  fi

  mkdir -p "${deploy_dir}/secrets" "${deploy_dir}/certs/gateway"
  for shared_gateway_file in gateway-control.key gateway-service.key gateway-control.env gateway-service.env; do
    if [[ "${component_overlay}" -eq 0 || ! -s "${deploy_dir}/secrets/${shared_gateway_file}" ]]; then
      install -m 0600 "${STAGE_DIR}/secrets/${shared_gateway_file}" "${deploy_dir}/secrets/${shared_gateway_file}"
    fi
  done
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    install -m 0600 "${STAGE_DIR}/secrets/storage-node-auth.env" "${deploy_dir}/secrets/storage-node-auth.env"
  fi
  if [[ -f "${STAGE_DIR}/secrets/storage-internal-auth.env" ]]; then
    if [[ "${component_overlay}" -eq 0 || ! -s "${deploy_dir}/secrets/storage-internal-auth.env" ]]; then
      install -m 0600 "${STAGE_DIR}/secrets/storage-internal-auth.env" "${deploy_dir}/secrets/storage-internal-auth.env"
    fi
  elif [[ "${component_overlay}" -eq 0 ]]; then
    rm -f "${deploy_dir}/secrets/storage-internal-auth.env"
  fi
  if [[ -f "${STAGE_DIR}/secrets/health-auth.env" ]]; then
    if [[ "${component_overlay}" -eq 0 || ! -s "${deploy_dir}/secrets/health-auth.env" ]]; then
      install -m 0600 "${STAGE_DIR}/secrets/health-auth.env" "${deploy_dir}/secrets/health-auth.env"
    fi
  fi
  for credential_file in "${STAGE_DIR}"/secrets/gateway-collector.key "${STAGE_DIR}"/secrets/gateway-factor.key "${STAGE_DIR}"/secrets/gateway-monitor.key "${STAGE_DIR}"/secrets/gateway-archive.key "${STAGE_DIR}"/secrets/gateway-storage-view.key "${STAGE_DIR}"/secrets/gateway-storage-primary.key "${STAGE_DIR}"/secrets/gateway-strategy.key "${STAGE_DIR}"/secrets/gateway-trade.key "${STAGE_DIR}"/secrets/gateway-cloudnode.key "${STAGE_DIR}"/secrets/gateway-moox-cli.key; do
    if [[ "${component_overlay}" -eq 0 || ! -s "${deploy_dir}/secrets/$(basename "${credential_file}")" ]]; then
      install -m 0600 "${credential_file}" "${deploy_dir}/secrets/$(basename "${credential_file}")"
    fi
  done
  if [[ "${component_overlay}" -eq 0 || ! -s "${deploy_dir}/secrets/gateway-moox-cli.env" || ! -s "${deploy_dir}/secrets/gateway-credentials.json" ]]; then
    install -m 0600 "${STAGE_DIR}/secrets/gateway-moox-cli.env" "${deploy_dir}/secrets/gateway-moox-cli.env"
    install -m 0600 "${STAGE_DIR}/secrets/gateway-credentials.json" "${deploy_dir}/secrets/gateway-credentials.json"
  fi
  if [[ -f "${STAGE_DIR}/secrets/notification.env.next" ]]; then
    install -m 0600 "${STAGE_DIR}/secrets/notification.env.next" "${deploy_dir}/secrets/notification.env"
  fi
  if [[ "${component_overlay}" -eq 0 || ! -s "${deploy_dir}/certs/gateway/peers.pem" ]]; then
    install -m 0644 "${STAGE_DIR}/certs/gateway/peers.pem" "${deploy_dir}/certs/gateway/peers.pem"
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
    if [[ -n "${PUBLIC_HOST}" && "${component_overlay}" -eq 0 ]]; then
      local caddy_ports="${SERVICE_HTTPS_PORT}"
      [[ "${WITH_ADMIN}" -eq 0 ]] || caddy_ports="${BROWSER_HTTPS_PORT},${SERVICE_HTTPS_PORT}"
      MOOX_PUBLIC_HOST="${PUBLIC_HOST}" MOOX_BROWSER_HTTPS_PORT="${BROWSER_HTTPS_PORT}" MOOX_SERVICE_HTTPS_PORT="${SERVICE_HTTPS_PORT}" \
        MOOX_TLS_MODE="${TLS_MODE_RESOLVED}" \
        MOOX_CADDY_CHECKSUMS="${deploy_dir}/lib/caddy-v2.11.4-checksums.txt" \
        MOOX_CADDY_ARCHIVE="${deploy_dir}/lib/caddy_2.11.4_$([[ "${TARGET_GOOS}" == darwin ]] && printf mac || printf '%s' "${TARGET_GOOS}")_${TARGET_GOARCH}.tar.gz" \
        "${deploy_dir}/lib/caddy-managed.sh" ensure --deploy-dir "${deploy_dir}" --os "${TARGET_GOOS}" --arch "${TARGET_GOARCH}" --ports "${caddy_ports}" --config "${deploy_dir}/config/caddy/Caddyfile.next" 8>&-
    fi
    if [[ "${component_overlay}" -eq 1 ]]; then
      [[ "${WITH_GATEWAY}" -eq 0 ]] || MOOX_WITH_GATEWAY=1 "${deploy_dir}/start.sh" gateway 8>&-
      [[ "${WITH_EVENTBUS}" -eq 0 ]] || "${deploy_dir}/start.sh" eventbus 8>&-
      [[ "${WITH_HOSTAGENT}" -eq 0 ]] || "${deploy_dir}/start.sh" hostagent 8>&-
      [[ "${WITH_ARCHIVE}" -eq 0 ]] || "${deploy_dir}/start.sh" archive 8>&-
      [[ "${WITH_CLOUDNODE}" -eq 0 ]] || "${deploy_dir}/start.sh" cloudnode 8>&-
      [[ "${WITH_COLLECTOR}" -eq 0 ]] || "${deploy_dir}/start.sh" collector 8>&-
      [[ "${WITH_FACTOR}" -eq 0 ]] || MOOX_WITH_FACTOR=1 "${deploy_dir}/start.sh" factor 8>&-
      [[ "${WITH_STRATEGY}" -eq 0 ]] || "${deploy_dir}/start.sh" strategy 8>&-
      [[ "${WITH_TRADE}" -eq 0 ]] || "${deploy_dir}/start.sh" trade 8>&-
      [[ "${WITH_MONITOR}" -eq 0 ]] || "${deploy_dir}/start.sh" monitor 8>&-
      [[ "${WITH_WEB_HOST}" -eq 0 ]] || "${deploy_dir}/start.sh" web-host 8>&-
    else
      "${deploy_dir}/start.sh" 8>&-
    fi
  fi
}

sync_remote_stage() {
  local archive remote_archive
  umask 077
  LOCAL_DEPLOY_ARCHIVE=$(mktemp "${TMPDIR:-/tmp}/moox-deploy-${TARGET_GOOS}-${TARGET_GOARCH}.XXXXXX")
  archive="${LOCAL_DEPLOY_ARCHIVE}"
  COPYFILE_DISABLE=1 tar --no-xattrs -C "${STAGE_DIR}" -czf "${archive}" .
  chmod 0600 "${archive}"

  remote_archive=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" \
    'umask 077; archive=$(mktemp /tmp/moox-deploy.XXXXXX); chmod 0600 "$archive"; printf "%s\n" "$archive"')
  [[ "${remote_archive}" =~ ^/tmp/moox-deploy\.[A-Za-z0-9]+$ ]] || \
    fail "remote host returned an invalid deployment archive path"
  REMOTE_DEPLOY_ARCHIVE="${remote_archive}"
  log "upload secure deployment archive to ${TARGET}:${remote_archive}"
  scp -p "${archive}" "${TARGET}:${remote_archive}"
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" "chmod 0600 -- $(shell_quote "${remote_archive}")"

  local quoted_dir quoted_archive quoted_node_id quoted_no_start quoted_component_overlay quoted_with_storage quoted_with_storage_node quoted_with_archive quoted_with_eventbus quoted_with_cloudnode quoted_with_collector quoted_with_factor quoted_with_strategy quoted_with_trade quoted_with_monitor quoted_with_hostagent quoted_with_web_host quoted_with_admin quoted_with_gateway quoted_reset_data quoted_metrics_metadata_url quoted_eventbus_url quoted_eventbus_host quoted_eventbus_port quoted_metrics_eventbus_url quoted_eventbus_enable_tls quoted_eventbus_public_ip quoted_public_host quoted_tls_mode quoted_browser_https_port quoted_service_https_port quoted_target_goos quoted_target_goarch quoted_local_storage_gateway_target quoted_local_storage_gateway_node_id quoted_storage_view_duckdb_memory_limit quoted_factor_python_workers quoted_factor_view_read_workers quoted_factor_view_read_timeout_ms quoted_control_root quoted_storage_root
  quoted_dir="$(shell_quote "${DEPLOY_DIR}")"
  quoted_archive="$(shell_quote "${remote_archive}")"
  quoted_node_id="$(shell_quote "${NODE_ID}")"
  quoted_no_start="$(shell_quote "${NO_START}")"
  quoted_component_overlay="$(shell_quote "${COMPONENT_OVERLAY}")"
  quoted_with_storage="$(shell_quote "${WITH_STORAGE}")"
  quoted_with_storage_node="$(shell_quote "${WITH_STORAGE_NODE}")"
  quoted_with_archive="$(shell_quote "${WITH_ARCHIVE}")"
  quoted_with_eventbus="$(shell_quote "${WITH_EVENTBUS}")"
  quoted_with_cloudnode="$(shell_quote "${WITH_CLOUDNODE}")"
  quoted_with_collector="$(shell_quote "${WITH_COLLECTOR}")"
  quoted_with_factor="$(shell_quote "${WITH_FACTOR}")"
  quoted_with_strategy="$(shell_quote "${WITH_STRATEGY}")"
  quoted_with_trade="$(shell_quote "${WITH_TRADE}")"
  quoted_with_monitor="$(shell_quote "${WITH_MONITOR}")"
  quoted_with_hostagent="$(shell_quote "${WITH_HOSTAGENT}")"
  quoted_with_web_host="$(shell_quote "${WITH_WEB_HOST}")"
  quoted_with_admin="$(shell_quote "${WITH_ADMIN}")"
  quoted_with_gateway="$(shell_quote "${WITH_GATEWAY}")"
  quoted_reset_data="$(shell_quote "${RESET_DATA}")"
  quoted_metrics_metadata_url="$(shell_quote "${METRICS_METADATA_URL}")"
  quoted_eventbus_url="$(shell_quote "${EVENTBUS_URL_ENV}")"
  quoted_eventbus_host="$(shell_quote "${MOOX_EVENTBUS_HOST}")"
  quoted_eventbus_port="$(shell_quote "${MOOX_EVENTBUS_PORT}")"
  quoted_metrics_eventbus_url="$(shell_quote "${METRICS_EVENTBUS_URL_ENV}")"
  quoted_eventbus_enable_tls="$(shell_quote "${MOOX_EVENTBUS_ENABLE_TLS:-0}")"
  quoted_eventbus_public_ip="$(shell_quote "${MOOX_EVENTBUS_PUBLIC_IP:-}")"
  quoted_public_host="$(shell_quote "${PUBLIC_HOST}")"
  quoted_tls_mode="$(shell_quote "${TLS_MODE_RESOLVED}")"
  quoted_browser_https_port="$(shell_quote "${BROWSER_HTTPS_PORT}")"
  quoted_service_https_port="$(shell_quote "${SERVICE_HTTPS_PORT}")"
  quoted_target_goos="$(shell_quote "${TARGET_GOOS}")"
  quoted_target_goarch="$(shell_quote "${TARGET_GOARCH}")"
  quoted_local_storage_gateway_target="$(shell_quote "${MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET:-}")"
  quoted_local_storage_gateway_node_id="$(shell_quote "${MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID:-}")"
  quoted_storage_view_duckdb_memory_limit="$(shell_quote "${MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT:-}")"
  quoted_storage_view_maintenance_policy_b64="$(shell_quote "${MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64:-}")"
  quoted_factor_python_workers="$(shell_quote "${MOOX_FACTOR_ENGINE_PYTHON_WORKERS:-}")"
  quoted_factor_view_read_workers="$(shell_quote "${MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS:-}")"
  quoted_factor_view_read_timeout_ms="$(shell_quote "${MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS:-}")"
  quoted_control_root="$(shell_quote "${MOOX_CONTROL_ROOT:-}")"
  quoted_storage_root="$(shell_quote "${MOOX_STORAGE_ROOT:-}")"

  ssh "${TARGET}" "DEPLOY_DIR=${quoted_dir} ARCHIVE=${quoted_archive} NODE_ID=${quoted_node_id} NO_START=${quoted_no_start} COMPONENT_OVERLAY=${quoted_component_overlay} WITH_STORAGE=${quoted_with_storage} WITH_STORAGE_NODE=${quoted_with_storage_node} WITH_ARCHIVE=${quoted_with_archive} WITH_EVENTBUS=${quoted_with_eventbus} WITH_CLOUDNODE=${quoted_with_cloudnode} WITH_COLLECTOR=${quoted_with_collector} WITH_FACTOR=${quoted_with_factor} WITH_STRATEGY=${quoted_with_strategy} WITH_TRADE=${quoted_with_trade} WITH_MONITOR=${quoted_with_monitor} WITH_HOSTAGENT=${quoted_with_hostagent} WITH_WEB_HOST=${quoted_with_web_host} WITH_ADMIN=${quoted_with_admin} WITH_GATEWAY=${quoted_with_gateway} RESET_DATA=${quoted_reset_data} MOOX_METRICS_STORAGE_METADATA_URL=${quoted_metrics_metadata_url} MOOX_EVENTBUS_NATS_URL=${quoted_eventbus_url} MOOX_EVENTBUS_HOST=${quoted_eventbus_host} MOOX_EVENTBUS_PORT=${quoted_eventbus_port} MOOX_METRICS_EVENTBUS_URL=${quoted_metrics_eventbus_url} MOOX_EVENTBUS_ENABLE_TLS=${quoted_eventbus_enable_tls} MOOX_EVENTBUS_PUBLIC_IP=${quoted_eventbus_public_ip} MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET=${quoted_local_storage_gateway_target} MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID=${quoted_local_storage_gateway_node_id} MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=${quoted_storage_view_duckdb_memory_limit} MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64=${quoted_storage_view_maintenance_policy_b64} MOOX_FACTOR_ENGINE_PYTHON_WORKERS=${quoted_factor_python_workers} MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS=${quoted_factor_view_read_workers} MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS=${quoted_factor_view_read_timeout_ms} MOOX_CONTROL_ROOT=${quoted_control_root} MOOX_STORAGE_ROOT=${quoted_storage_root} PUBLIC_HOST=${quoted_public_host} TLS_MODE_RESOLVED=${quoted_tls_mode} BROWSER_HTTPS_PORT=${quoted_browser_https_port} SERVICE_HTTPS_PORT=${quoted_service_https_port} TARGET_GOOS=${quoted_target_goos} TARGET_GOARCH=${quoted_target_goarch} bash -s" <<'EOF'
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

persist_selected_components() {
  local file="$1" assignment key enabled
  shift
  mkdir -p "$(dirname "${file}")"
  touch "${file}"
  for assignment in "$@"; do
    key="${assignment%%:*}"
    enabled="${assignment#*:}"
    [[ "${enabled}" == "1" ]] || continue
    if grep -q "^${key}=" "${file}"; then
      sed -i "s/^${key}=.*/${key}=1/" "${file}"
    else
      printf '%s=1\n' "${key}" >>"${file}"
    fi
  done
  chmod 0600 "${file}"
}

stop_foreign_gateway() {
  [[ "${WITH_GATEWAY}" == "1" ]] || return 0
  local proc pid exe cwd start_time current_start
  for proc in /proc/[0-9]*; do
    [[ -d "${proc}" ]] || continue
    pid="${proc##*/}"
    [[ "${pid}" != "$$" ]] || continue
    exe="$(readlink "${proc}/exe" 2>/dev/null || true)"
    [[ "${exe}" == *"/moox-gateway" || "${exe}" == *"/moox-gateway (deleted)" ]] || continue
    [[ "${exe}" == "${DEPLOY_DIR}/bin/moox-gateway" || "${exe}" == "${DEPLOY_DIR}/bin/moox-gateway (deleted)" ]] && continue
    cwd="$(readlink "${proc}/cwd" 2>/dev/null || true)"
    if [[ -r "${cwd}/config/app.yaml" ]] && ! grep -Eq "^  id: ${NODE_ID}$" "${cwd}/config/app.yaml"; then
      continue
    fi
    start_time="$(awk '{print $22}' "${proc}/stat" 2>/dev/null || true)"
    if [[ "${NO_START}" == "1" ]]; then
      echo "foreign Gateway process is running from ${exe}; refuse --no-start deployment" >&2
      exit 1
    fi
    echo "stopping foreign Gateway process pid=${pid} exe=${exe}" >&2
    kill "${pid}" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    current_start="$(awk '{print $22}' "/proc/${pid}/stat" 2>/dev/null || true)"
    [[ -n "${start_time}" && "${current_start}" == "${start_time}" ]] || continue
    kill -9 "${pid}" 2>/dev/null || true
  done
}

GATEWAY_ROLLBACK_ACTIVE=0
prepare_gateway_rollback() {
  [[ "${WITH_GATEWAY}" == "1" && "${NO_START}" == "0" && "${COMPONENT_OVERLAY}" == "1" ]] || return 0
  [[ -d "${DEPLOY_DIR}/gateway" ]] || return 0
  rm -f "${GATEWAY_ROLLBACK_ARCHIVE}"
  local entries=(gateway)
  [[ -f "${DEPLOY_DIR}/bin/moox-gateway" ]] && entries+=(bin/moox-gateway)
  [[ -f "${DEPLOY_DIR}/bin/moox-gateway-cli" ]] && entries+=(bin/moox-gateway-cli)
  tar -C "${DEPLOY_DIR}" -czf "${GATEWAY_ROLLBACK_ARCHIVE}" "${entries[@]}"
  chmod 0600 "${GATEWAY_ROLLBACK_ARCHIVE}"
  GATEWAY_ROLLBACK_ACTIVE=1
}

rollback_gateway() {
  [[ "${GATEWAY_ROLLBACK_ACTIVE}" == "1" && -s "${GATEWAY_ROLLBACK_ARCHIVE}" ]] || return 0
  local status=0
  set +e
  if [[ -x "${DEPLOY_DIR}/stop.sh" ]]; then MOOX_WITH_GATEWAY=1 "${DEPLOY_DIR}/stop.sh" gateway >/dev/null 2>&1 || true; fi
  rm -rf "${DEPLOY_DIR}/gateway"
  rm -f "${DEPLOY_DIR}/bin/moox-gateway" "${DEPLOY_DIR}/bin/moox-gateway-cli"
  tar -C "${DEPLOY_DIR}" -xzf "${GATEWAY_ROLLBACK_ARCHIVE}" || status=$?
  if [[ "${status}" -eq 0 && -x "${DEPLOY_DIR}/start.sh" ]]; then
    MOOX_WITH_GATEWAY=1 "${DEPLOY_DIR}/start.sh" gateway >/dev/null 2>&1 || status=$?
  fi
  if [[ "${status}" -eq 0 ]]; then
    set -a
    source "${DEPLOY_DIR}/secrets/health-auth.env"
    set +a
    ready=0
    for _ in $(seq 1 30); do
      timestamp=$(date +%s); nonce=$(openssl rand -hex 32)
      body_hash=$(printf %s "" | openssl dgst -sha256); body_hash=${body_hash##* }
      canonical=$(printf "%s\nGET\n/readyz\n%s\n%s\n%s" moox-request-v1 "${body_hash}" "${timestamp}" "${nonce}")
      signature=$(printf "%s" "${canonical}" | openssl dgst -sha256 -hmac "${MOOX_HEALTH_AUTH_SECRET_KEY}"); signature=${signature##* }
      auth="${MOOX_HEALTH_AUTH_VERSION}/${MOOX_HEALTH_AUTH_ACCESS_KEY}/${timestamp}/${nonce}/${signature}"
      if curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: ${auth}" http://127.0.0.1:11012/readyz >/dev/null 2>&1; then
        ready=1
        break
      fi
      sleep 1
    done
    [[ "${ready}" == "1" ]] || status=1
  fi
  if [[ "${status}" -eq 0 ]]; then
    rm -f "${GATEWAY_ROLLBACK_ARCHIVE}"
    GATEWAY_ROLLBACK_ACTIVE=0
  else
    echo "Gateway rollback failed; preserve ${GATEWAY_ROLLBACK_ARCHIVE} for manual recovery" >&2
  fi
  set -e
  return "${status}"
}

gateway_rollback_on_exit() {
  local status=$?
  if [[ "${status}" -ne 0 && "${GATEWAY_ROLLBACK_ACTIVE}" == "1" ]] && ! rollback_gateway; then
    echo "automatic Gateway rollback did not complete" >&2
  fi
  exit "${status}"
}

if [[ "${DEPLOY_DIR}" == "~" ]]; then
  DEPLOY_DIR="${HOME}"
elif [[ "${DEPLOY_DIR}" == "~/"* ]]; then
  DEPLOY_DIR="${HOME}/${DEPLOY_DIR#\~/}"
fi
GATEWAY_ROLLBACK_ARCHIVE="${DEPLOY_DIR}/.gateway-rollback.tgz"

mkdir -p "${DEPLOY_DIR}"
if command -v flock >/dev/null 2>&1; then
  exec 8>"${DEPLOY_DIR}.maintenance.lock"
  flock 8
fi
# Keep the control package and the independent Storage package on the same
# internal auth material. Inspect the uploaded archive before stopping the
# existing deployment so a mismatch fails without causing downtime.
COUNTERPART_AUTH_FOR_DEPLOY=""
if [[ "${WITH_STORAGE}" == "1" && "${WITH_ADMIN}" == "0" ]]; then
    COUNTERPART_AUTH_FOR_DEPLOY="${MOOX_CONTROL_ROOT:-$(dirname "${DEPLOY_DIR}")/prod}/secrets/storage-internal-auth.env"
elif [[ "${WITH_ADMIN}" == "1" && "${WITH_STORAGE}" == "0" ]]; then
    COUNTERPART_AUTH_FOR_DEPLOY="${MOOX_STORAGE_ROOT:-$(dirname "${DEPLOY_DIR}")/storage}/secrets/storage-internal-auth.env"
fi
if [[ -n "${COUNTERPART_AUTH_FOR_DEPLOY}" ]]; then
  STAGED_STORAGE_AUTH="$(mktemp "${TMPDIR:-/tmp}/moox-storage-auth.XXXXXX")"
  trap 'rm -f "${STAGED_STORAGE_AUTH:-}"' EXIT
  if ! tar -xOzf "${ARCHIVE}" ./secrets/storage-internal-auth.env >"${STAGED_STORAGE_AUTH}" 2>/dev/null; then
    if [[ -e "${COUNTERPART_AUTH_FOR_DEPLOY}" ]]; then
      echo "storage_internal_auth_missing_preflight: uploaded package has no storage credentials while ${COUNTERPART_AUTH_FOR_DEPLOY} exists" >&2
      exit 1
    fi
  elif [[ ! -r "${COUNTERPART_AUTH_FOR_DEPLOY}" ]]; then
    echo "storage_internal_auth_missing_preflight: counterpart credentials are missing or unreadable at ${COUNTERPART_AUTH_FOR_DEPLOY}" >&2
    exit 1
  elif ! cmp -s "${STAGED_STORAGE_AUTH}" "${COUNTERPART_AUTH_FOR_DEPLOY}"; then
    echo "storage_internal_auth_mismatch_preflight: uploaded credentials differ from ${COUNTERPART_AUTH_FOR_DEPLOY}; synchronize credentials before deployment" >&2
    exit 1
  fi
  rm -f "${STAGED_STORAGE_AUTH}"
  trap - EXIT
fi
HAS_SELECTED_WORKLOAD=0
if [[ "${WITH_ARCHIVE}" == "1" || "${WITH_EVENTBUS}" == "1" || "${WITH_CLOUDNODE}" == "1" || \
  "${WITH_COLLECTOR}" == "1" || "${WITH_FACTOR}" == "1" || "${WITH_STRATEGY}" == "1" || \
  "${WITH_TRADE}" == "1" || "${WITH_MONITOR}" == "1" || "${WITH_WEB_HOST}" == "1" || \
  "${WITH_HOSTAGENT}" == "1" || \
  "${WITH_GATEWAY}" == "1" ]]; then
  HAS_SELECTED_WORKLOAD=1
fi
if [[ "${COMPONENT_OVERLAY}" == "1" ]]; then
  [[ "${WITH_ADMIN}" == "0" && "${WITH_STORAGE}" == "0" && "${HAS_SELECTED_WORKLOAD}" == "1" ]] || {
    echo "--component-overlay requires --no-admin, --no-storage, and at least one selected component" >&2
    exit 1
  }
  [[ -x "${DEPLOY_DIR}/start.sh" ]] || { echo "--component-overlay requires an existing executable ${DEPLOY_DIR}/start.sh" >&2; exit 1; }
  [[ -r "${DEPLOY_DIR}/config/components.env" ]] || { echo "--component-overlay requires lifecycle component inventory at ${DEPLOY_DIR}/config/components.env" >&2; exit 1; }
  grep -Fq 'MOOX_INSTALLED_WITH_' "${DEPLOY_DIR}/config/components.env" || { echo "installed component inventory is too old for --component-overlay; run a full deployment first" >&2; exit 1; }
  grep -Fq 'MOOX_INSTALLED_WITH_' "${DEPLOY_DIR}/start.sh" || { echo "installed lifecycle is too old for --component-overlay; run a full deployment first" >&2; exit 1; }
  [[ "${RESET_DATA}" == "0" ]] || { echo "--reset-data cannot be used with --component-overlay" >&2; exit 1; }
  echo "component overlay requested; preserve the installed control plane, shared credentials, and lifecycle scripts"
elif [[ "${WITH_ADMIN}" == "0" && "${WITH_STORAGE}" == "0" && "${HAS_SELECTED_WORKLOAD}" == "1" && \
  ( -d "${DEPLOY_DIR}/admin" || -x "${DEPLOY_DIR}/bin/moox-admin" ) ]]; then
  echo "existing control plane detected; use --component-overlay for a partial update" >&2
  exit 1
fi
KEY_FILE="${HOME}/.config/moox/credentials/admin-encryption-key"
if [[ "${WITH_ADMIN}" == "1" && ! -f "${KEY_FILE}" ]]; then
  mkdir -p "${HOME}/.config/moox/credentials"
  if [[ -f "${DEPLOY_DIR}/data/admin.db" ]]; then echo "Admin DB exists but encryption key is missing" >&2; exit 1; fi
  umask 077; head -c 32 /dev/urandom | base64 | tr -d '\n' > "${KEY_FILE}"; chmod 600 "${KEY_FILE}"
fi
if [[ "${COMPONENT_OVERLAY}" == "0" && ( -e "${DEPLOY_DIR}/config/caddy/edge.env" || -e "${DEPLOY_DIR}/config/caddy/Caddyfile" || -e "${DEPLOY_DIR}/run/caddy.pid" ) ]]; then
  if [[ "${NO_START}" -eq 1 ]]; then
    echo "--no-start refuses to replace an existing managed Caddy deployment" >&2
    exit 1
  fi
  if [[ -z "${PUBLIC_HOST}" ]]; then
    echo "existing managed Caddy deployment requires --public-host" >&2
    exit 1
  fi
fi

# All preflight checks above must pass before we stop or replace a running
# Gateway. This keeps a rejected package from causing avoidable downtime.
prepare_gateway_rollback
trap gateway_rollback_on_exit EXIT
stop_foreign_gateway

if [[ -x "${DEPLOY_DIR}/stop.sh" && "${NO_START}" -eq 0 ]]; then
  if [[ "${WITH_STORAGE}" == "1" ]]; then
    MOOX_WITH_EVENTBUS="${WITH_EVENTBUS}" MOOX_WITH_ARCHIVE="${WITH_ARCHIVE}" "${DEPLOY_DIR}/stop.sh" || true
  else
    [[ "${WITH_ARCHIVE}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" archive || true
    [[ "${WITH_COLLECTOR}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" collector || true
    [[ "${WITH_FACTOR}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" factor || true
    [[ "${WITH_STRATEGY}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" strategy || true
    [[ "${WITH_TRADE}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" trade || true
    [[ "${WITH_MONITOR}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" monitor || true
    [[ "${WITH_CLOUDNODE}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" cloudnode || true
    [[ "${WITH_EVENTBUS}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" eventbus || true
    [[ "${WITH_HOSTAGENT}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" hostagent || true
    [[ "${WITH_GATEWAY}" == "1" ]] && MOOX_WITH_GATEWAY=1 "${DEPLOY_DIR}/stop.sh" gateway || true
    [[ "${WITH_WEB_HOST}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" web-host || true
    [[ "${WITH_ADMIN}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" admin || true
  fi
fi

if [[ "${RESET_DATA}" == "1" ]]; then
  if [[ -d "${DEPLOY_DIR}/data" ]]; then
    find "${DEPLOY_DIR}/data" -mindepth 1 -maxdepth 1 ! -name caddy -exec rm -rf -- {} +
  fi
fi

if [[ "${COMPONENT_OVERLAY}" == "0" ]]; then
  rm -rf "${DEPLOY_DIR}/admin" "${DEPLOY_DIR}/gateway" "${DEPLOY_DIR}/examples" \
    "${DEPLOY_DIR}/start.sh" "${DEPLOY_DIR}/stop.sh" "${DEPLOY_DIR}/restart.sh" "${DEPLOY_DIR}/status.sh" "${DEPLOY_DIR}/healthcheck.sh"
  rm -f "${DEPLOY_DIR}/bin/moox-admin" "${DEPLOY_DIR}/bin/moox-admin-cli" \
    "${DEPLOY_DIR}/bin/moox-cli" "${DEPLOY_DIR}/bin/moox-gateway" "${DEPLOY_DIR}/bin/moox-gateway-cli"
fi
if [[ "${COMPONENT_OVERLAY}" == "1" && "${WITH_GATEWAY}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/gateway"
  rm -f "${DEPLOY_DIR}/bin/moox-gateway" "${DEPLOY_DIR}/bin/moox-gateway-cli"
fi
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
if [[ "${WITH_HOSTAGENT}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/hostagent"
  rm -f "${DEPLOY_DIR}/bin/moox-host-agent" "${DEPLOY_DIR}/bin/moox-host-agent-cli"
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
  rm -f "${DEPLOY_DIR}/bin/moox-factor" "${DEPLOY_DIR}/bin/moox-factor-cli" "${DEPLOY_DIR}/bin/moox-factor-run-once"
fi
if [[ "${WITH_STRATEGY}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/strategy"
  rm -f "${DEPLOY_DIR}/bin/moox-strategy" "${DEPLOY_DIR}/bin/moox-strategy-cli"
fi
if [[ "${WITH_TRADE}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/trade"
  rm -f "${DEPLOY_DIR}/bin/moox-trade" "${DEPLOY_DIR}/bin/moox-trade-cli"
fi
if [[ "${WITH_STORAGE}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/storage" "${DEPLOY_DIR}/storage-view" "${DEPLOY_DIR}/storage-node"
  rm -f "${DEPLOY_DIR}/bin/moox-storage" "${DEPLOY_DIR}/bin/moox-storage-cli" \
    "${DEPLOY_DIR}/bin/moox-storage-primary" \
    "${DEPLOY_DIR}/bin/moox-storage-view" \
    "${DEPLOY_DIR}/bin/moox-storage-node"
fi
TAR_EXCLUDES=()
if [[ "${COMPONENT_OVERLAY}" == "1" ]]; then
  TAR_EXCLUDES+=(
    --exclude='./admin' --exclude='./examples'
    --exclude='./bin/moox-admin' --exclude='./bin/moox-admin-cli' --exclude='./bin/moox-cli'
    --exclude='./config/caddy' --exclude='./config/components.env' --exclude='./certs/caddy'
    --exclude='./secrets/admin-jwt.env' --exclude='./secrets/health-auth.env'
    --exclude='./secrets/storage-node-auth.env' --exclude='./secrets/storage-internal-auth.env'
    --exclude='./certs/gateway' --exclude='./secrets/gateway-*'
  )
  if [[ "${WITH_GATEWAY}" == "0" ]]; then
    TAR_EXCLUDES+=(
      --exclude='./gateway' --exclude='./bin/moox-gateway' --exclude='./bin/moox-gateway-cli'
    )
  fi
  if [[ "${WITH_EVENTBUS}" == "0" ]]; then
    # A remote component overlay which does not own EventBus must preserve
    # the installed listener contract for the next restart.
    TAR_EXCLUDES+=(--exclude='./config/runtime.env')
  fi
fi
tar "${TAR_EXCLUDES[@]}" -C "${DEPLOY_DIR}" -xzf "${ARCHIVE}"
rm -f "${ARCHIVE}"
if [[ "${COMPONENT_OVERLAY}" == "1" ]]; then
  persist_selected_components "${DEPLOY_DIR}/config/components.env" \
    "MOOX_INSTALLED_WITH_ARCHIVE:${WITH_ARCHIVE}" "MOOX_INSTALLED_WITH_EVENTBUS:${WITH_EVENTBUS}" \
    "MOOX_INSTALLED_WITH_CLOUDNODE:${WITH_CLOUDNODE}" "MOOX_INSTALLED_WITH_COLLECTOR:${WITH_COLLECTOR}" \
    "MOOX_INSTALLED_WITH_FACTOR:${WITH_FACTOR}" "MOOX_INSTALLED_WITH_STRATEGY:${WITH_STRATEGY}" \
      "MOOX_INSTALLED_WITH_TRADE:${WITH_TRADE}" "MOOX_INSTALLED_WITH_MONITOR:${WITH_MONITOR}" \
      "MOOX_INSTALLED_WITH_HOSTAGENT:${WITH_HOSTAGENT}" \
    "MOOX_INSTALLED_WITH_WEB_HOST:${WITH_WEB_HOST}" "MOOX_INSTALLED_WITH_GATEWAY:${WITH_GATEWAY}"
fi
if [[ -f "${DEPLOY_DIR}/secrets/notification.env.next" ]]; then
  mv -f "${DEPLOY_DIR}/secrets/notification.env.next" "${DEPLOY_DIR}/secrets/notification.env"
  chmod 0600 "${DEPLOY_DIR}/secrets/notification.env"
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
  # A standalone Storage package using the control-plane Gateway does not
  # own a public HTTPS edge.  Starting a second Caddy on the same host would
  # contend with the control package's listener and leave Storage deployed
  # but with a misleading activation failure.  Only packages that own Admin
  # or Gateway expose an edge listener here.
  if [[ -n "${PUBLIC_HOST}" && "${COMPONENT_OVERLAY}" == "0" && ( "${WITH_ADMIN}" == "1" || "${WITH_GATEWAY}" == "1" ) ]]; then
    CADDY_OS_NAME="${TARGET_GOOS}"
    [[ "${CADDY_OS_NAME}" != darwin ]] || CADDY_OS_NAME=mac
    CADDY_PORTS="${SERVICE_HTTPS_PORT}"
    [[ "${WITH_ADMIN}" == "0" ]] || CADDY_PORTS="${BROWSER_HTTPS_PORT},${SERVICE_HTTPS_PORT}"
    MOOX_PUBLIC_HOST="${PUBLIC_HOST}" MOOX_BROWSER_HTTPS_PORT="${BROWSER_HTTPS_PORT}" MOOX_SERVICE_HTTPS_PORT="${SERVICE_HTTPS_PORT}" \
      MOOX_TLS_MODE="${TLS_MODE_RESOLVED}" \
      MOOX_CADDY_CHECKSUMS="${DEPLOY_DIR}/lib/caddy-v2.11.4-checksums.txt" \
      MOOX_CADDY_ARCHIVE="${DEPLOY_DIR}/lib/caddy_2.11.4_${CADDY_OS_NAME}_${TARGET_GOARCH}.tar.gz" \
      "${DEPLOY_DIR}/lib/caddy-managed.sh" ensure --deploy-dir "${DEPLOY_DIR}" --os "${TARGET_GOOS}" --arch "${TARGET_GOARCH}" --ports "${CADDY_PORTS}" --config "${DEPLOY_DIR}/config/caddy/Caddyfile.next" 8>&-
  fi
  if [[ "${COMPONENT_OVERLAY}" == "1" ]]; then
    [[ "${WITH_GATEWAY}" == "0" ]] || MOOX_WITH_GATEWAY=1 "${DEPLOY_DIR}/start.sh" gateway 8>&-
    [[ "${WITH_EVENTBUS}" == "0" ]] || "${DEPLOY_DIR}/start.sh" eventbus 8>&-
    [[ "${WITH_HOSTAGENT}" == "0" ]] || "${DEPLOY_DIR}/start.sh" hostagent 8>&-
    [[ "${WITH_ARCHIVE}" == "0" ]] || "${DEPLOY_DIR}/start.sh" archive 8>&-
    [[ "${WITH_CLOUDNODE}" == "0" ]] || "${DEPLOY_DIR}/start.sh" cloudnode 8>&-
    [[ "${WITH_COLLECTOR}" == "0" ]] || "${DEPLOY_DIR}/start.sh" collector 8>&-
    [[ "${WITH_FACTOR}" == "0" ]] || MOOX_WITH_FACTOR=1 "${DEPLOY_DIR}/start.sh" factor 8>&-
    [[ "${WITH_STRATEGY}" == "0" ]] || "${DEPLOY_DIR}/start.sh" strategy 8>&-
    [[ "${WITH_TRADE}" == "0" ]] || "${DEPLOY_DIR}/start.sh" trade 8>&-
    [[ "${WITH_MONITOR}" == "0" ]] || "${DEPLOY_DIR}/start.sh" monitor 8>&-
    [[ "${WITH_WEB_HOST}" == "0" ]] || "${DEPLOY_DIR}/start.sh" web-host 8>&-
  else
    "${DEPLOY_DIR}/start.sh" 8>&-
  fi
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
  COPYFILE_DISABLE=1 tar --no-xattrs -C "${STAGE_DIR}" -czf "${PACKAGE_ARCHIVE}" .
  chmod 0600 "${PACKAGE_ARCHIVE}"
  log "wrote deployment archive ${PACKAGE_ARCHIVE}"
  exit 0
fi

if is_local_target; then
  sync_local_stage
else
  sync_remote_stage
  if [[ "${WITH_GATEWAY}" == "1" && "${NO_START}" == "0" && "${COMPONENT_OVERLAY}" == "1" ]]; then
    REMOTE_GATEWAY_ROLLBACK_PENDING=1
  fi
fi

verify_gateway_control_plane() {
  [[ "${WITH_GATEWAY}" == "1" && "${NO_START}" == "0" ]] || return 0
  local verify_script='set -euo pipefail
root=$1
node_id=$2
gateway_ready() {
  local timestamp nonce body_hash canonical signature auth
  set -a
  source "$root/secrets/health-auth.env"
  set +a
  timestamp=$(date +%s)
  nonce=$(openssl rand -hex 32)
  body_hash=$(printf %s "" | openssl dgst -sha256)
  body_hash=${body_hash##* }
  canonical=$(printf "%s\nGET\n/readyz\n%s\n%s\n%s" "moox-request-v1" "$body_hash" "$timestamp" "$nonce")
  signature=$(printf "%s" "$canonical" | openssl dgst -sha256 -hmac "$MOOX_HEALTH_AUTH_SECRET_KEY")
  signature=${signature##* }
  auth="$MOOX_HEALTH_AUTH_VERSION/$MOOX_HEALTH_AUTH_ACCESS_KEY/$timestamp/$nonce/$signature"
  curl --fail --silent --max-time 2 -H "X-Moox-Health-Auth: $auth" \
    http://127.0.0.1:11012/readyz >/dev/null
}
for _ in $(seq 1 60); do
  if gateway_ready &&
    grep -Eq "^  id: ${node_id}$" "$root/gateway/config/app.yaml" &&
    test -s "$root/data/gateway/routes.json" &&
    grep -q "\"node_id\": \"${node_id}\"" "$root/data/gateway/routes.json"; then
    echo "gateway control-plane readiness accepted: node_id=${node_id}"
    exit 0
  fi
  sleep 1
done
echo "gateway control-plane readiness failed: node_id=${node_id}" >&2
tail -80 "$root/logs/gateway/stdout.log" >&2 || true
exit 1'
  if is_local_target; then
    if ! bash -s -- "$(expand_local_path "${DEPLOY_DIR}")" "${NODE_ID}" <<<"${verify_script}"; then
      if rollback_local_gateway; then
        fail "Gateway control-plane acceptance failed; previous Gateway restored"
      fi
      fail "Gateway control-plane acceptance failed; automatic rollback failed, preserve ${GATEWAY_ROLLBACK_ARCHIVE} for manual recovery"
    fi
    finalize_local_gateway_rollback
  else
    local quoted_dir quoted_node_id
    quoted_dir="$(shell_quote "${DEPLOY_DIR%/}")"
    quoted_node_id="$(shell_quote "${NODE_ID}")"
    if ! ssh -o BatchMode=yes "${TARGET}" "bash -s -- ${quoted_dir} ${quoted_node_id}" <<<"${verify_script}"; then
      if rollback_remote_gateway; then
        fail "Gateway control-plane acceptance failed; previous Gateway restored"
      fi
      fail "Gateway control-plane acceptance failed; automatic rollback failed, preserve ${DEPLOY_DIR%/}/.gateway-rollback.tgz for manual recovery"
    fi
    if [[ "${REMOTE_GATEWAY_ROLLBACK_PENDING}" == "1" ]]; then
      ssh -o BatchMode=yes "${TARGET}" "rm -f -- $(shell_quote "${DEPLOY_DIR%/}/.gateway-rollback.tgz")" >/dev/null 2>&1 || true
      REMOTE_GATEWAY_ROLLBACK_PENDING=0
    fi
  fi
  log "Gateway control-plane acceptance passed"
}

verify_gateway_control_plane

configure_target_ca() {
  [[ "${TLS_MODE_RESOLVED}" == internal && -n "${PUBLIC_HOST}" && "${NO_START}" -eq 0 && "${TARGET_CA}" != skip ]] || return 0
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
  [[ "${TLS_MODE_RESOLVED}" == internal && -n "${PUBLIC_HOST}" && "${NO_START}" -eq 0 && "${LOCAL_CA}" != skip ]] || return 0
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
ca=$1; browser=$2; service=$3; service_auth_file=$4; with_admin=$5; node_id=$6; tls_mode=$7
case "$ca" in "~/"*) ca="$HOME/${ca#\~/}";; esac
case "$service_auth_file" in "~/"*) service_auth_file="$HOME/${service_auth_file#\~/}";; esac
browser_authority=${browser#https://}; service_authority=${service#https://}
status() {
  ca_args=()
  [[ "$tls_mode" != internal ]] || ca_args=(--cacert "$ca")
  curl --silent --show-error --max-time 5 "${ca_args[@]}" \
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
canonical=$(printf "moox-gateway-auth-v1\n%s\nPOST\n%s\n\n\n%s\n%s\n%s\n%s" "$MOOX_GATEWAY_CALLER" "$path" "$body_hash" "$timestamp" "$nonce" "$node_id")
signature=$(printf %s "$canonical" | openssl dgst -sha256 -hmac "$MOOX_GATEWAY_SERVICE_SECRET_KEY" | awk "{print \$NF}")
expected=404; [[ "$with_admin" == 0 ]] || expected=200
expect_status "$expected" -X POST -H "Content-Type: application/json" \
  -H "X-Moox-Key-Id: $MOOX_GATEWAY_SERVICE_KEY_ID" -H "X-Moox-Caller: $MOOX_GATEWAY_CALLER" -H "X-Moox-Timestamp: $timestamp" \
  -H "X-Moox-Nonce: $nonce" -H "X-Moox-Target-Node: $node_id" -H "X-Moox-Signature: $signature" \
  --data "{}" "$service$path"'
  if is_local_target; then
    bash -c "${verify_script}" _ "$(expand_local_path "${DEPLOY_DIR}")/certs/caddy/root.crt" "${browser}" "${service}" "$(expand_local_path "${DEPLOY_DIR}")/secrets/gateway-service.env" "${WITH_ADMIN}" "${NODE_ID}" "${TLS_MODE_RESOLVED}" || {
      "$(expand_local_path "${DEPLOY_DIR}")/lib/caddy-managed.sh" rollback --deploy-dir "$(expand_local_path "${DEPLOY_DIR}")" || true
      fail "public HTTPS acceptance failed"
    }
  elif [[ -n "${FETCHED_CA_FILE}" ]]; then
    ssh -o BatchMode=yes "${TARGET}" bash -s -- "${DEPLOY_DIR%/}/certs/caddy/root.crt" "${browser}" "${service}" "${DEPLOY_DIR%/}/secrets/gateway-service.env" "${WITH_ADMIN}" "${NODE_ID}" "${TLS_MODE_RESOLVED}" <<<"${verify_script}" || {
      ssh -o BatchMode=yes "${TARGET}" "$(shell_quote "${DEPLOY_DIR%/}/lib/caddy-managed.sh") rollback --deploy-dir $(shell_quote "${DEPLOY_DIR}")" || true
      fail "public HTTPS acceptance failed"
    }
  else
    ssh -o BatchMode=yes "${TARGET}" bash -s -- "${DEPLOY_DIR%/}/certs/caddy/root.crt" "${browser}" "${service}" "${DEPLOY_DIR%/}/secrets/gateway-service.env" "${WITH_ADMIN}" "${NODE_ID}" "${TLS_MODE_RESOLVED}" <<<"${verify_script}" || {
      ssh -o BatchMode=yes "${TARGET}" "$(shell_quote "${DEPLOY_DIR%/}/lib/caddy-managed.sh") rollback --deploy-dir $(shell_quote "${DEPLOY_DIR}")" || true
      fail "remote public HTTPS acceptance failed"
    }
  fi
  log "HTTPS acceptance passed: ${browser} and ${service}"
}

verify_public_https

verify_runtime_governance() {
  [[ "${NO_START}" -eq 0 && "${WITH_EVENTBUS}" == "1" ]] || return 0
  local governance_script='set -euo pipefail
root=$1; expected_host=$2; expected_port=$3; expected_tls=$4; require_public=$5
test -r "$root/config/runtime.env"
set -a; . "$root/config/runtime.env"; set +a
[[ "${MOOX_EVENTBUS_HOST:-}" == "$expected_host" ]]
[[ "${MOOX_EVENTBUS_PORT:-}" == "$expected_port" ]]
[[ "${MOOX_EVENTBUS_ENABLE_TLS:-}" == "$expected_tls" ]]
pgrep -f -- "$root/bin/moox-eventbus" >/dev/null
if [[ "$require_public" == 1 ]]; then
  command -v ss >/dev/null 2>&1
  ss -ltnH | grep -Eq "(0\\.0\\.0\\.0|\\*):${expected_port}([[:space:]]|$)"
fi'
  local require_public=0
  if [[ "${WITH_COLLECTOR}" == "1" && "${MOOX_EVENTBUS_ALLOW_LOOPBACK_REMOTE:-0}" != "1" ]] && ! is_local_target; then
    require_public=1
  fi
  if is_local_target; then
    bash -c "${governance_script}" _ "$(expand_local_path "${DEPLOY_DIR}")" "${MOOX_EVENTBUS_HOST}" "${MOOX_EVENTBUS_PORT}" "${MOOX_EVENTBUS_ENABLE_TLS:-0}" "${require_public}" ||
      fail "runtime governance acceptance failed"
  else
    ssh -o BatchMode=yes "${TARGET}" bash -s -- "${DEPLOY_DIR%/}" "${MOOX_EVENTBUS_HOST}" "${MOOX_EVENTBUS_PORT}" "${MOOX_EVENTBUS_ENABLE_TLS:-0}" "${require_public}" <<<"${governance_script}" ||
      fail "runtime governance acceptance failed"
  fi
  log "runtime governance acceptance passed"
}

verify_runtime_governance

log "done"
