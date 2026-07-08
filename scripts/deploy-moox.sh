#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="localhost"
DEPLOY_DIR="${MOOX_DEPLOY_DIR:-${HOME}/moox}"
STAGE_DIR=""
SKIP_BUILD=0
NO_START=0
WITH_STORAGE=1
WITH_WEB_HOST=1
WITH_CLOUDNODE=1
WITH_COLLECTOR=1
WITH_FACTOR=1
WITH_MONITOR=1
BUILD_WEB_ASSETS=1
RESET_DATA=0
TARGET_GOOS=""
TARGET_GOARCH=""

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
  --no-storage                    Do not package/stop/start moox-storage; preserve existing remote storage files.
  --no-web-host                   Do not package/start moox-web-host.
  --no-cloudnode                  Do not package/start moox-cloudnode.
  --no-collector                  Do not package/start moox-collector.
  --no-factor                     Do not package/start moox-factor.
  --no-monitor                    Do not package/start moox-monitor.
  --build-web-assets              Rebuild Vue dist and statik assets before building web-host. Default when web-host is enabled.
  --reuse-web-assets              Reuse current embedded statik assets when building web-host.
  --reset-data                    Remove target data directory before deploying. Use when rebuilding from examples.
  -h, --help                      Show this help.

Examples:
  scripts/deploy-moox.sh --target localhost --dir ~/moox/dev
  scripts/deploy-moox.sh --target user@host --dir ~/moox/prod --goos linux --goarch amd64
  scripts/deploy-moox.sh --target localhost --dir /tmp/moox --skip-build --no-start
EOF
}

log() {
  printf '[deploy-moox] %s\n' "$*"
}

fail() {
  printf '[deploy-moox] ERROR: %s\n' "$*" >&2
  exit 1
}

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
    --no-storage)
      WITH_STORAGE=0
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
    --no-monitor)
      WITH_MONITOR=0
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
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

[[ -n "${TARGET}" ]] || fail "--target cannot be empty"
[[ -n "${DEPLOY_DIR}" ]] || fail "--dir cannot be empty"

is_local_target() {
  [[ "${TARGET}" == "localhost" || "${TARGET}" == "127.0.0.1" || "${TARGET}" == "::1" ]]
}

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

TARGET_GOOS="${TARGET_GOOS:-$(detect_os)}"
TARGET_GOARCH="${TARGET_GOARCH:-$(detect_arch)}"
TARGET_GOOS="$(normalize_os "${TARGET_GOOS}")"
TARGET_GOARCH="$(normalize_arch "${TARGET_GOARCH}")"

HOST_GOOS="$(go env GOOS)"
HOST_GOARCH="$(go env GOARCH)"
STAGE_DIR="${STAGE_DIR:-${ROOT}/release/deploy-stage/moox}"

build_core_binaries() {
  if [[ "${SKIP_BUILD}" -eq 1 ]]; then
    log "skip core build; reuse ./bin"
    return
  fi

  log "build core binaries (${TARGET_GOOS}/${TARGET_GOARCH})"
  if [[ "${WITH_STORAGE}" -eq 0 ]]; then
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" cli
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" admin
    if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" cloudnode
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
    if [[ "${WITH_MONITOR}" -eq 1 ]]; then
      TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
        "${ROOT}/scripts/build.sh" monitor
    fi
    return
  fi

  if [[ "${TARGET_GOOS}" != "${HOST_GOOS}" || "${TARGET_GOARCH}" != "${HOST_GOARCH}" ]]; then
    log "cross build detected; storage requires CGO-enabled DuckDB build"
    TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build.sh" all
    return
  fi

  TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
    "${ROOT}/scripts/build.sh" all
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
      if [[ ! -d node_modules ]]; then
        CI=true pnpm install --no-frozen-lockfile --config.confirmModulesPurge=false
      else
        log "reuse existing web/node_modules"
      fi
      npm run build:prod
    )
    if ! command -v statik >/dev/null 2>&1; then
      go install github.com/rakyll/statik@latest
    fi
    (cd "${ROOT}/web-host" && statik -src=../web/dist -dest=./internal)
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
  perl -0pi -e 's#path:\s*\./data/admin\.db#path: ../data/admin.db#g' \
    "${STAGE_DIR}/admin/config/app.yaml"
  perl -0pi -e 's#data_dir:\s*"\./data/badger"#data_dir: "../data/badger"#g' \
    "${STAGE_DIR}/admin/config/gateway.yaml"
  if [[ -f "${STAGE_DIR}/admin/config/trpc_go.yaml" ]]; then
    perl -0pi -e 's#log_path:\s*\./log#log_path: ../logs/admin#g' \
      "${STAGE_DIR}/admin/config/trpc_go.yaml"
  fi

  if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/moox_cloudnode\.db#path: ../data/cloudnode/moox_cloudnode.db#g' \
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
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    perl -0pi -e 's#path:\s*\./data/monitor/monitor\.db#path: ../data/monitor/monitor.db#g' \
      "${STAGE_DIR}/monitor/config/app.yaml"
  fi

  [[ "${WITH_STORAGE}" -eq 1 ]] || return 0
  perl -0pi -e 's#root:\s*\./var/storage#root: ../data/storage#g; s#path:\s*\./var/storage/metadata/storage_metadata\.db#path: ../data/storage/metadata/storage_metadata.db#g; s#pebble_path:\s*\./var/storage/pebble#pebble_path: ../data/storage/pebble#g; s#duckdb_path:\s*\./var/storage/duckdb/views\.duckdb#duckdb_path: ../data/storage/duckdb/views.duckdb#g; s#bleve_path:\s*\./var/storage/bleve#bleve_path: ../data/storage/bleve#g; s#parquet_path:\s*\./var/storage/archive#parquet_path: ../data/storage/archive#g' \
    "${STAGE_DIR}/storage/config/storage.yaml"
  # Production factor/view pipelines depend on Storage's embedded NATS/JetStream bus.
  perl -0pi -e 's#type:\s*memory#type: nats#g; s#enabled:\s*false#enabled: true#g' \
    "${STAGE_DIR}/storage/config/storage.yaml"
  perl -0pi -e 's#log_path:\s*\./logs#log_path: ../logs/storage#g; s#network:\s*"0 \*/10 \* \* \* \*\?disable=1&scheduler=viewBuilderSchedule&params=op=retry_failed"#network: "0 */10 * * * *?disable=0&scheduler=viewBuilderSchedule&params=op=retry_failed"#g' \
    "${STAGE_DIR}/storage/config/trpc_go.yaml"
}

write_runtime_scripts() {
  cat > "${STAGE_DIR}/start.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WITH_STORAGE="${MOOX_WITH_STORAGE:-__WITH_STORAGE__}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"
STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-3}"
mkdir -p "${ROOT}/run" "${ROOT}/data" "${ROOT}/data/cloudnode" "${ROOT}/data/cloudnode/jobs" "${ROOT}/data/collector" "${ROOT}/data/factor" "${ROOT}/data/monitor" "${ROOT}/logs/admin" "${ROOT}/logs/storage" "${ROOT}/logs/web-host" "${ROOT}/logs/cloudnode" "${ROOT}/logs/collector" "${ROOT}/logs/factor" "${ROOT}/logs/monitor"

stop_if_running() {
  local name="$1"
  local pid_file="${ROOT}/run/${name}.pid"
  local pattern="${ROOT}/bin/moox-${name}([[:space:]]|$)"
  if [[ ! -f "${pid_file}" ]]; then
    pkill -f -- "${pattern}" 2>/dev/null || true
    return
  fi
  local pid
  pid="$(cat "${pid_file}" 2>/dev/null || true)"
  if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
    echo "stopping existing ${name} pid=${pid}"
    kill "${pid}" 2>/dev/null || true
    sleep 1
  fi
  if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
    kill -9 "${pid}" 2>/dev/null || true
  fi
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

STORAGE_ENV=(
  "STORAGE_CONFIG_PATH=${ROOT}/storage/config"
  "MOOX_STORAGE_CONFIG=${ROOT}/storage/config/storage.yaml"
  "MOOX_STORAGE_HOME=${ROOT}/data/storage"
  "STORAGE_SCHEMA_FILE=${ROOT}/storage/schema/metadata.sql"
)

COLLECTOR_ENV=(
  "MOOX_COLLECTOR_ADMIN_GATEWAY_URL=${MOOX_COLLECTOR_ADMIN_GATEWAY_URL:-http://127.0.0.1:11000}"
  "MOOX_SERVICE_AUTH_VERSION=${MOOX_SERVICE_AUTH_VERSION:-moox-auth-v1}"
  "MOOX_SERVICE_AUTH_ACCESS_KEY=${MOOX_SERVICE_AUTH_ACCESS_KEY:-moox-service}"
  "MOOX_SERVICE_AUTH_SECRET_KEY=${MOOX_SERVICE_AUTH_SECRET_KEY:-moox-service-secret-change-me}"
  "MOOX_SERVICE_AUTH_EXPIRE_SECONDS=${MOOX_SERVICE_AUTH_EXPIRE_SECONDS:-1800}"
)

FACTOR_ENV=(
  "MOOX_FACTOR_ADMIN_GATEWAY_URL=${MOOX_FACTOR_ADMIN_GATEWAY_URL:-http://127.0.0.1:11000}"
  "MOOX_FACTOR_DB_PATH=${MOOX_FACTOR_DB_PATH:-../data/factor/factor.db}"
  "MOOX_FACTOR_NATS_URL=${MOOX_FACTOR_NATS_URL:-nats://127.0.0.1:4222}"
  "MOOX_SERVICE_AUTH_VERSION=${MOOX_SERVICE_AUTH_VERSION:-moox-auth-v1}"
  "MOOX_SERVICE_AUTH_ACCESS_KEY=${MOOX_SERVICE_AUTH_ACCESS_KEY:-moox-service}"
  "MOOX_SERVICE_AUTH_SECRET_KEY=${MOOX_SERVICE_AUTH_SECRET_KEY:-moox-service-secret-change-me}"
  "MOOX_SERVICE_AUTH_EXPIRE_SECONDS=${MOOX_SERVICE_AUTH_EXPIRE_SECONDS:-1800}"
)

MONITOR_ENV=(
  "MOOX_SERVICE_AUTH_VERSION=${MOOX_SERVICE_AUTH_VERSION:-moox-auth-v1}"
  "MOOX_SERVICE_AUTH_ACCESS_KEY=${MOOX_SERVICE_AUTH_ACCESS_KEY:-moox-service}"
  "MOOX_SERVICE_AUTH_SECRET_KEY=${MOOX_SERVICE_AUTH_SECRET_KEY:-moox-service-secret-change-me}"
  "MOOX_SERVICE_AUTH_EXPIRE_SECONDS=${MOOX_SERVICE_AUTH_EXPIRE_SECONDS:-1800}"
)

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

factor_nats_endpoint() {
  local url="${MOOX_FACTOR_NATS_URL:-nats://127.0.0.1:4222}"
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

wait_factor_nats() {
  local host port
  read -r host port < <(factor_nats_endpoint)
  local attempts="${MOOX_WAIT_FACTOR_NATS_SECONDS:-60}"
  echo "waiting for factor NATS ${host}:${port}"
  for _ in $(seq 1 "${attempts}"); do
    if bash -c ":</dev/tcp/${host}/${port}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "factor NATS ${host}:${port} not ready after ${attempts}s" >&2
  return 1
}

init_storage_schema() {
  echo "initializing storage metadata schema"
  mkdir -p "${ROOT}/logs/storage"
  (
    cd "${ROOT}/storage"
    env "${STORAGE_ENV[@]}" "${ROOT}/bin/moox-storage-cli" init \
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

start_storage() {
  start_service "storage" "${ROOT}/storage" \
    env "${STORAGE_ENV[@]}" "${ROOT}/bin/moox-storage" \
      -conf=config/trpc_go.yaml \
      -storage-conf=config/storage.yaml
}

start_admin() {
  init_admin_schema
  start_service "admin" "${ROOT}/admin" \
    "${ROOT}/bin/moox-admin" -conf=config/trpc_go.yaml
}

start_cloudnode() {
  if [[ "${WITH_CLOUDNODE}" != "1" ]]; then
    echo "cloudnode is disabled in this deployment package" >&2
    exit 2
  fi
  init_cloudnode_schema
  start_service "cloudnode" "${ROOT}/cloudnode" \
    env \
      "MOOX_CLOUDNODE_PPROF_ADDR=${MOOX_CLOUDNODE_PPROF_ADDR:-127.0.0.1:16001}" \
      "${ROOT}/bin/moox-cloudnode" -conf=config/trpc_go.yaml
}

start_collector() {
  if [[ "${WITH_COLLECTOR}" != "1" ]]; then
    echo "collector is disabled in this deployment package" >&2
    exit 2
  fi
  init_collector_schema
  start_service "collector" "${ROOT}/collector" \
    env "${COLLECTOR_ENV[@]}" "${ROOT}/bin/moox-collector" -conf=config/trpc_go.yaml
}

start_factor() {
  if [[ "${WITH_FACTOR}" != "1" ]]; then
    echo "factor is disabled in this deployment package" >&2
    exit 2
  fi
  wait_factor_nats
  ensure_factor_python
  start_service "factor" "${ROOT}/factor" \
    env "${FACTOR_ENV[@]}" "${ROOT}/bin/moox-factor" -conf=config/trpc_go.yaml
}

start_monitor() {
  if [[ "${WITH_MONITOR}" != "1" ]]; then
    echo "monitor is disabled in this deployment package" >&2
    exit 2
  fi
  init_monitor_schema
  start_service "monitor" "${ROOT}/monitor" \
    env "${MONITOR_ENV[@]}" "${ROOT}/bin/moox-monitor" -conf=config/trpc_go.yaml
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
  start_service "web-host" "${ROOT}" \
    env \
      "MOOX_WEB_HOST_ADDR=${MOOX_WEB_HOST_ADDR:-:9527}" \
      "${ROOT}/bin/moox-web-host"
}

SERVICE="${1:-}"
case "${SERVICE}" in
  "")
    if [[ "${WITH_STORAGE}" == "1" ]]; then
      init_storage_schema
      start_storage
    fi
    if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
      start_cloudnode
    fi
    start_admin
    if [[ "${WITH_MONITOR}" == "1" ]]; then
      start_monitor
    fi
    if [[ "${WITH_COLLECTOR}" == "1" ]]; then
      start_collector
    fi
    if [[ "${WITH_FACTOR}" == "1" ]]; then
      start_factor
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
  cloudnode) start_cloudnode ;;
  collector) start_collector ;;
  factor) start_factor ;;
  monitor) start_monitor ;;
  admin) start_admin ;;
  web-host) start_web_host ;;
  *)
    echo "unknown service: ${SERVICE}; valid: storage cloudnode collector factor monitor admin web-host" >&2
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
WITH_STORAGE="${MOOX_WITH_STORAGE:-__WITH_STORAGE__}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"

stop_service() {
  local name="$1"
  local pid_file="${ROOT}/run/${name}.pid"
  local pattern="${ROOT}/bin/moox-${name}([[:space:]]|$)"
  if [[ ! -f "${pid_file}" ]]; then
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
    if pkill -f -- "${pattern}" 2>/dev/null; then
      echo "${name}: stopped stale process with empty pid file"
    else
      echo "${name}: empty pid file removed"
    fi
    return
  fi
  if ps -p "${pid}" >/dev/null 2>&1; then
    echo "stopping ${name} pid=${pid}"
    kill "${pid}" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      if ! ps -p "${pid}" >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
    if ps -p "${pid}" >/dev/null 2>&1; then
      kill -9 "${pid}" 2>/dev/null || true
    fi
  else
    echo "${name}: stale pid ${pid}"
  fi
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
    stop_service "admin"
    if [[ "${WITH_COLLECTOR}" == "1" ]]; then
      stop_service "collector"
    fi
    if [[ "${WITH_FACTOR}" == "1" ]]; then
      stop_service "factor"
    fi
    if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
      stop_service "cloudnode"
    fi
    if [[ "${WITH_STORAGE}" == "1" ]]; then
      stop_service "storage"
    fi
    ;;
  storage)
    if [[ "${WITH_STORAGE}" != "1" ]]; then
      echo "storage is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  admin) stop_service "${SERVICE}" ;;
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
  monitor)
    if [[ "${WITH_MONITOR}" != "1" ]]; then
      echo "monitor is disabled in this deployment package" >&2
      exit 2
    fi
    stop_service "${SERVICE}"
    ;;
  *)
    echo "unknown service: ${SERVICE}; valid: storage cloudnode collector factor monitor admin web-host" >&2
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
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"

services=(admin)
if [[ "${WITH_WEB_HOST}" == "1" ]]; then
  services+=(web-host)
fi
if [[ "${WITH_MONITOR}" == "1" ]]; then
  services=(monitor "${services[@]}")
fi
if [[ "${WITH_STORAGE}" == "1" ]]; then
  services=(storage "${services[@]}")
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
WITH_STORAGE="${MOOX_WITH_STORAGE:-__WITH_STORAGE__}"
WITH_CLOUDNODE="${MOOX_WITH_CLOUDNODE:-__WITH_CLOUDNODE__}"
WITH_COLLECTOR="${MOOX_WITH_COLLECTOR:-__WITH_COLLECTOR__}"
WITH_FACTOR="${MOOX_WITH_FACTOR:-__WITH_FACTOR__}"
WITH_MONITOR="${MOOX_WITH_MONITOR:-__WITH_MONITOR__}"
WITH_WEB_HOST="${MOOX_WITH_WEB_HOST:-__WITH_WEB_HOST__}"
LOG_FILE="${ROOT}/logs/healthcheck.log"

mkdir -p "${ROOT}/run" "$(dirname "${LOG_FILE}")"

default_services=()
if [[ "${WITH_STORAGE}" == "1" ]]; then
  default_services+=(storage)
fi
if [[ "${WITH_CLOUDNODE}" == "1" ]]; then
  default_services+=(cloudnode)
fi
default_services+=(admin)
if [[ "${WITH_MONITOR}" == "1" ]]; then
  default_services+=(monitor)
fi
if [[ "${WITH_COLLECTOR}" == "1" ]]; then
  default_services+=(collector)
fi
if [[ "${WITH_FACTOR}" == "1" ]]; then
  default_services+=(factor)
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
    echo "${name}: running pid=${pid}"
    return 0
  fi

  log_line "${name}: stopped or stale pid=${pid:-none}; restarting"
  echo "${name}: stopped; restarting"
  if STARTUP_WAIT_SECONDS="${STARTUP_WAIT_SECONDS:-3}" "${ROOT}/start.sh" "${name}" >> "${LOG_FILE}" 2>&1; then
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

  perl -0pi -e "s#__WITH_STORAGE__#${WITH_STORAGE}#g; s#__WITH_CLOUDNODE__#${WITH_CLOUDNODE}#g; s#__WITH_COLLECTOR__#${WITH_COLLECTOR}#g; s#__WITH_FACTOR__#${WITH_FACTOR}#g; s#__WITH_MONITOR__#${WITH_MONITOR}#g; s#__WITH_WEB_HOST__#${WITH_WEB_HOST}#g" \
    "${STAGE_DIR}/start.sh" "${STAGE_DIR}/stop.sh" "${STAGE_DIR}/status.sh" "${STAGE_DIR}/healthcheck.sh"
  chmod +x "${STAGE_DIR}/start.sh" "${STAGE_DIR}/stop.sh" "${STAGE_DIR}/status.sh" "${STAGE_DIR}/restart.sh" "${STAGE_DIR}/healthcheck.sh"
}

prepare_stage() {
  rm -rf "${STAGE_DIR}"
  mkdir -p \
    "${STAGE_DIR}/bin" \
    "${STAGE_DIR}/admin/config" \
    "${STAGE_DIR}/cloudnode/config" \
    "${STAGE_DIR}/collector/config" \
    "${STAGE_DIR}/collector/configs" \
    "${STAGE_DIR}/factor/config" \
    "${STAGE_DIR}/factor/factors" \
    "${STAGE_DIR}/factor/sections" \
    "${STAGE_DIR}/monitor/config" \
    "${STAGE_DIR}/examples" \
    "${STAGE_DIR}/data" \
    "${STAGE_DIR}/logs" \
    "${STAGE_DIR}/run"
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    mkdir -p "${STAGE_DIR}/storage/config" "${STAGE_DIR}/storage/schema"
  fi

  copy_required_binary "moox-admin"
  copy_required_binary "moox-admin-cli"
  copy_required_binary "moox-cli"
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
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    copy_required_binary "moox-monitor"
    copy_required_binary "moox-monitor-cli"
  fi
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    copy_required_binary "moox-storage"
    copy_required_binary "moox-storage-cli"
  fi
  copy_optional_web_host

  cp -R "${ROOT}/modules/admin/config/." "${STAGE_DIR}/admin/config/"
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
  if [[ "${WITH_MONITOR}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/monitor/config/." "${STAGE_DIR}/monitor/config/"
  fi
  if [[ "${WITH_STORAGE}" -eq 1 ]]; then
    cp -R "${ROOT}/modules/storage/config/." "${STAGE_DIR}/storage/config/"
    cp "${ROOT}/modules/storage/schema/metadata.sql" "${STAGE_DIR}/storage/schema/metadata.sql"
  fi
  cp -R "${ROOT}/examples/." "${STAGE_DIR}/examples/"

  patch_configs
  write_runtime_scripts
  chmod +x "${STAGE_DIR}/bin/"*
}

sync_local_stage() {
  local deploy_dir
  deploy_dir="$(expand_local_path "${DEPLOY_DIR}")"
  mkdir -p "${deploy_dir}"

  if [[ -x "${deploy_dir}/stop.sh" && "${NO_START}" -eq 0 ]]; then
    if [[ "${WITH_STORAGE}" -eq 1 ]]; then
      "${deploy_dir}/stop.sh" || true
    else
      if [[ "${WITH_COLLECTOR}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" collector || true
      fi
      if [[ "${WITH_FACTOR}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" factor || true
      fi
      if [[ "${WITH_MONITOR}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" monitor || true
      fi
      if [[ "${WITH_CLOUDNODE}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" cloudnode || true
      fi
      if [[ "${WITH_WEB_HOST}" -eq 1 ]]; then
        "${deploy_dir}/stop.sh" web-host || true
      fi
      "${deploy_dir}/stop.sh" admin || true
    fi
  fi

  if [[ "${RESET_DATA}" -eq 1 ]]; then
    rm -rf "${deploy_dir}/data"
  fi

  if command -v rsync >/dev/null 2>&1; then
    local rsync_excludes=(--exclude '/data/' --exclude '/logs/' --exclude '/run/')
    if [[ "${WITH_STORAGE}" -eq 0 ]]; then
      rsync_excludes+=(--exclude '/storage/' --exclude '/bin/moox-storage' --exclude '/bin/moox-storage-cli')
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
    if [[ "${WITH_MONITOR}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/monitor"
      rm -f "${deploy_dir}/bin/moox-monitor" "${deploy_dir}/bin/moox-monitor-cli"
    fi
    if [[ "${WITH_STORAGE}" -eq 1 ]]; then
      rm -rf "${deploy_dir}/storage"
      rm -f "${deploy_dir}/bin/moox-storage" "${deploy_dir}/bin/moox-storage-cli"
    fi
    cp -R "${STAGE_DIR}/." "${deploy_dir}/"
  fi

  chmod +x "${deploy_dir}/start.sh" "${deploy_dir}/stop.sh" "${deploy_dir}/status.sh" "${deploy_dir}/healthcheck.sh" "${deploy_dir}/bin/"*
  log "deployed to ${deploy_dir}"

  if [[ "${NO_START}" -eq 0 ]]; then
    "${deploy_dir}/start.sh"
  fi
}

sync_remote_stage() {
  local archive="${ROOT}/release/deploy-stage/moox-${TARGET_GOOS}-${TARGET_GOARCH}.tar.gz"
  mkdir -p "$(dirname "${archive}")"
  tar -C "${STAGE_DIR}" -czf "${archive}" .

  local remote_archive="/tmp/moox-deploy-${TARGET_GOOS}-${TARGET_GOARCH}.tar.gz"
  log "upload ${archive} to ${TARGET}:${remote_archive}"
  scp "${archive}" "${TARGET}:${remote_archive}"

  local quoted_dir quoted_archive quoted_no_start quoted_with_storage quoted_with_cloudnode quoted_with_collector quoted_with_factor quoted_with_monitor quoted_with_web_host quoted_reset_data
  quoted_dir="$(shell_quote "${DEPLOY_DIR}")"
  quoted_archive="$(shell_quote "${remote_archive}")"
  quoted_no_start="$(shell_quote "${NO_START}")"
  quoted_with_storage="$(shell_quote "${WITH_STORAGE}")"
  quoted_with_cloudnode="$(shell_quote "${WITH_CLOUDNODE}")"
  quoted_with_collector="$(shell_quote "${WITH_COLLECTOR}")"
  quoted_with_factor="$(shell_quote "${WITH_FACTOR}")"
  quoted_with_monitor="$(shell_quote "${WITH_MONITOR}")"
  quoted_with_web_host="$(shell_quote "${WITH_WEB_HOST}")"
  quoted_reset_data="$(shell_quote "${RESET_DATA}")"

  ssh "${TARGET}" "DEPLOY_DIR=${quoted_dir} ARCHIVE=${quoted_archive} NO_START=${quoted_no_start} WITH_STORAGE=${quoted_with_storage} WITH_CLOUDNODE=${quoted_with_cloudnode} WITH_COLLECTOR=${quoted_with_collector} WITH_FACTOR=${quoted_with_factor} WITH_MONITOR=${quoted_with_monitor} WITH_WEB_HOST=${quoted_with_web_host} RESET_DATA=${quoted_reset_data} bash -s" <<'EOF'
set -euo pipefail

if [[ "${DEPLOY_DIR}" == "~" ]]; then
  DEPLOY_DIR="${HOME}"
elif [[ "${DEPLOY_DIR}" == "~/"* ]]; then
  DEPLOY_DIR="${HOME}/${DEPLOY_DIR#\~/}"
fi

mkdir -p "${DEPLOY_DIR}"
if [[ -x "${DEPLOY_DIR}/stop.sh" && "${NO_START}" -eq 0 ]]; then
  if [[ "${WITH_STORAGE}" == "1" ]]; then
    "${DEPLOY_DIR}/stop.sh" || true
  else
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_COLLECTOR}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" collector || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_FACTOR}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" factor || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_MONITOR}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" monitor || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_CLOUDNODE}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" cloudnode || true
    fi
    if [[ -x "${DEPLOY_DIR}/stop.sh" && "${WITH_WEB_HOST}" == "1" ]]; then
      "${DEPLOY_DIR}/stop.sh" web-host || true
    fi
    "${DEPLOY_DIR}/stop.sh" admin || true
  fi
fi

if [[ "${RESET_DATA}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/data"
fi

rm -rf "${DEPLOY_DIR}/admin" "${DEPLOY_DIR}/examples" \
  "${DEPLOY_DIR}/start.sh" "${DEPLOY_DIR}/stop.sh" "${DEPLOY_DIR}/restart.sh" "${DEPLOY_DIR}/status.sh" "${DEPLOY_DIR}/healthcheck.sh"
rm -f "${DEPLOY_DIR}/bin/moox-admin" "${DEPLOY_DIR}/bin/moox-admin-cli" \
  "${DEPLOY_DIR}/bin/moox-cli"
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
if [[ "${WITH_STORAGE}" == "1" ]]; then
  rm -rf "${DEPLOY_DIR}/storage"
  rm -f "${DEPLOY_DIR}/bin/moox-storage" "${DEPLOY_DIR}/bin/moox-storage-cli"
fi
tar -C "${DEPLOY_DIR}" -xzf "${ARCHIVE}"
rm -f "${ARCHIVE}"
chmod +x "${DEPLOY_DIR}/start.sh" "${DEPLOY_DIR}/stop.sh" "${DEPLOY_DIR}/status.sh" "${DEPLOY_DIR}/healthcheck.sh" "${DEPLOY_DIR}/bin/"*

if [[ "${NO_START}" -eq 0 ]]; then
  "${DEPLOY_DIR}/start.sh"
fi
EOF
  log "deployed to ${TARGET}:${DEPLOY_DIR}"
}

log "target=${TARGET} dir=${DEPLOY_DIR} platform=${TARGET_GOOS}/${TARGET_GOARCH}"
build_core_binaries
build_web_host_binary
prepare_stage

if is_local_target; then
  sync_local_stage
else
  sync_remote_stage
fi

log "done"
