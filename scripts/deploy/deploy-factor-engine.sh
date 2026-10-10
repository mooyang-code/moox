#!/usr/bin/env bash
# 在运行因子引擎的机器上安装 moox-factor-engine（macOS 用 launchd，Linux 用 systemd 用户服务）。引擎只发起
# 出站连接：经外部接入调用 moox-factor-mgr 的 FactorEngine 服务、读写 Storage，以及连接 EventBus。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OS="$(uname -s)"
# Not under ~/Documents: macOS privacy protection blocks a launchd job from
# opening a rebuilt binary there until the user approves an invisible prompt.
DEPLOY_DIR="${HOME}/moox/factor-engine"
SECRETS_DIR=""
ENGINE_ID=""
ACCESS_ADDRESS=""
ACCESS_ID=""
EVENTBUS_URL=""
SKIP_BUILD=0
NO_START=0
LAUNCH_AGENTS_DIR="${MOOX_LAUNCH_AGENTS_DIR:-${HOME}/Library/LaunchAgents}"
SYSTEMD_USER_DIR="${MOOX_SYSTEMD_USER_DIR:-${HOME}/.config/systemd/user}"

usage() {
  cat <<'EOF'
用法：
  scripts/deploy/deploy-factor-engine.sh --access-address HOST:PORT --access-id access@HOST --eventbus-url URL [选项]

选项：
  --dir <path>              安装目录，默认 ~/moox/factor-engine；macOS 上不要放在 ~/Documents 下。
  --secrets-dir <path>      存放引擎密钥的目录，默认 <dir>/secrets。
  --engine-id <id>          稳定的引擎 ID，默认 factor-engine@<短主机名>。
  --access-address <addr>   外部接入地址，例如 146.56.196.204:11004。
  --access-id <id>          外部接入实例 ID，例如 access@storage。
  --eventbus-url <url>      EventBus 地址，例如 tls://106.53.107.122:4222。
  --skip-build              复用 <dir>/bin/moox-factor-engine。
  --no-start                只安装文件，不（重新）启动服务。

密钥目录中需要的文件（普通文件，权限 0600）：
  caller-factor-engine.key           factor-engine 外部调用方的签名密钥；在 control 的部署根目录下执行
                                     current/bin/moox-admin-cli keys ensure --db-path data/admin/admin.db \
                                       --encryption-key-file secrets/admin-encryption-key \
                                       --caller factor-engine --principal --out <文件>
                                     导出（已有就复用）
  storage-primary-auth.secret        Storage 部署的 MOOX_STORAGE_PRIMARY_AUTH_SECRET
  factor-eventbus.yaml               因子 EventBus 角色凭据（ca_file 放在同一目录）
EOF
}

fail() {
  printf '[deploy-factor-engine] ERROR: %s\n' "$*" >&2
  exit 1
}

log() {
  printf '[deploy-factor-engine] %s\n' "$*"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DEPLOY_DIR="$2"; shift 2 ;;
    --secrets-dir) SECRETS_DIR="$2"; shift 2 ;;
    --engine-id) ENGINE_ID="$2"; shift 2 ;;
    --access-address) ACCESS_ADDRESS="$2"; shift 2 ;;
    --access-id) ACCESS_ID="$2"; shift 2 ;;
    --eventbus-url) EVENTBUS_URL="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown option: $1" ;;
  esac
done

[[ "${ACCESS_ADDRESS}" =~ ^[^/:@[:space:]]+:[0-9]+$ ]] || fail "--access-address 必须是 host:port"
[[ "${ACCESS_ID}" =~ ^access@[A-Za-z0-9_-]+$ ]] || fail "--access-id 必须是 access@<主机 ID>"
[[ "${EVENTBUS_URL}" =~ ^(nats|tls):// ]] || fail "--eventbus-url must be a nats:// or tls:// URL"
mkdir -p "${DEPLOY_DIR}"
DEPLOY_DIR="$(cd "${DEPLOY_DIR}" && pwd -P)"
SECRETS_DIR="${SECRETS_DIR:-${DEPLOY_DIR}/secrets}"
if [[ -z "${ENGINE_ID}" ]]; then
  ENGINE_ID="factor-engine@$(hostname -s)"
fi
[[ "${ENGINE_ID}" =~ ^[A-Za-z0-9@._-]+$ ]] || fail "engine id contains unsupported characters"

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then stat -f '%Lp' "$1"; else stat -c '%a' "$1"; fi
}

require_secret() {
  local path="${SECRETS_DIR}/$1"
  [[ -f "${path}" && ! -L "${path}" ]] || fail "missing credential ${path}"
  [[ "$(file_mode "${path}")" == 600 ]] || fail "credential ${path} must have mode 0600"
  grep -q '[^[:space:]]' "${path}" || fail "credential ${path} is empty"
}

mkdir -p "${DEPLOY_DIR}/bin" "${DEPLOY_DIR}/config" "${DEPLOY_DIR}/pyworker" "${DEPLOY_DIR}/data/engine" "${DEPLOY_DIR}/logs"
mkdir -p "${SECRETS_DIR}"
chmod 0700 "${SECRETS_DIR}"
for secret in caller-factor-engine.key storage-primary-auth.secret factor-eventbus.yaml; do
  require_secret "${secret}"
done

# Go module downloads stall behind an operator's local HTTP proxy; the build
# talks to the module mirror directly.
if [[ "${SKIP_BUILD}" -eq 0 ]]; then
  log "build moox-factor-engine"
  version="$(git -C "${ROOT}" describe --always --dirty 2>/dev/null || echo dev)"
  (cd "${ROOT}/modules/factor" && env -u HTTPS_PROXY -u HTTP_PROXY -u https_proxy -u http_proxy CGO_ENABLED=0 \
    go build -ldflags "-X main.Version=${version} -X main.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o "${DEPLOY_DIR}/bin/moox-factor-engine" ./cmd/engine)
fi
[[ -x "${DEPLOY_DIR}/bin/moox-factor-engine" ]] || fail "missing ${DEPLOY_DIR}/bin/moox-factor-engine"

install -m 0644 "${ROOT}/modules/factor/pyworker/worker.py" "${ROOT}/modules/factor/pyworker/codec.py" "${DEPLOY_DIR}/pyworker/"
# The worker imports the shared moox_pyruntime package; MOOX_PYTHON_RUNTIME_PATH
# in the service definition points it here.
rm -rf "${DEPLOY_DIR}/python-runtime"
mkdir -p "${DEPLOY_DIR}/python-runtime"
cp -R "${ROOT}/packages/pyruntime/python/." "${DEPLOY_DIR}/python-runtime/"
find "${DEPLOY_DIR}/python-runtime" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
install -m 0644 "${ROOT}/modules/factor/config/trpc_go.engine.yaml" "${DEPLOY_DIR}/config/trpc_go.engine.yaml"

python_bin="${DEPLOY_DIR}/venv/bin/python"
if ! "${python_bin}" -c 'import pandas, numpy' >/dev/null 2>&1; then
  log "create Python environment ${DEPLOY_DIR}/venv"
  if command -v uv >/dev/null 2>&1; then
    uv venv --python 3.12 "${DEPLOY_DIR}/venv"
    VIRTUAL_ENV="${DEPLOY_DIR}/venv" uv pip install -r "${ROOT}/modules/factor/pyworker/runtime-requirements.txt"
  else
    python3 -m venv "${DEPLOY_DIR}/venv"
    "${python_bin}" -m pip install -r "${ROOT}/modules/factor/pyworker/runtime-requirements.txt"
  fi
  "${python_bin}" -c 'import pandas, numpy' || fail "the engine Python environment cannot import pandas and numpy"
fi

umask 077
cat >"${DEPLOY_DIR}/config/engine.yaml" <<EOF
# Rendered by scripts/deploy/deploy-factor-engine.sh; edit the flags, not this file.
engine:
  id: ${ENGINE_ID}
  heartbeat_interval: 10s

gateway_client:
  mode: access
  caller: factor-engine
  key_file: ${SECRETS_DIR}/caller-factor-engine.key
  access_address: ${ACCESS_ADDRESS}
  access_id: ${ACCESS_ID}

manager:
  timeout: 30s

catalog_sync:
  interval: 1m
  offset: 45s
  state_file: ./data/engine/catalog.json

storage:
  auth_secret_file: ${SECRETS_DIR}/storage-primary-auth.secret

eventbus:
  urls:
    - ${EVENTBUS_URL}
  credential_file: ${SECRETS_DIR}/factor-eventbus.yaml
  fetch_max_wait: 10s

python:
  bin: ./venv/bin/python
  worker_path: ./pyworker/worker.py
  factors_dir: ./data/engine/factors
  workers: 8
  task_timeout: 30s

pipeline:
  read_batch_subjects: 100
  read_workers: 4
  read_timeout: 20s
  write_batch_rows: 1000
  period_budget_min: 60s
  period_budget_max: 15m

recalc:
  chunk_periods: 500
  poll_interval: 5s
EOF
umask 022

health_env="${SECRETS_DIR}/health-auth.env"
if [[ ! -s "${health_env}" ]]; then
  (umask 077; {
    printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\n'
    printf 'MOOX_HEALTH_AUTH_ACCESS_KEY=%s\n' "factor-engine-$(openssl rand -hex 8)"
    printf 'MOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$(openssl rand -hex 32)"
  } >"${health_env}")
fi
health_access="$(sed -n 's/^MOOX_HEALTH_AUTH_ACCESS_KEY=//p' "${health_env}")"
health_secret="$(sed -n 's/^MOOX_HEALTH_AUTH_SECRET_KEY=//p' "${health_env}")"
[[ -n "${health_access}" && -n "${health_secret}" ]] || fail "invalid ${health_env}"

render_template() {
  sed -e "s#__ROOT__#${DEPLOY_DIR}#g" -e "s#__SECRETS__#${SECRETS_DIR}#g" -e "s#__HEALTH_ACCESS_KEY__#${health_access}#g" \
    -e "s#__HEALTH_SECRET_KEY__#${health_secret}#g" "$1"
}

if [[ "${OS}" == Darwin ]]; then
  mkdir -p "${LAUNCH_AGENTS_DIR}"
  plist="${LAUNCH_AGENTS_DIR}/com.moox.factor-engine.plist"
  (umask 077; render_template "${ROOT}/deploy/launchd/com.moox.factor-engine.plist.tmpl" >"${plist}")
  plutil -lint "${plist}" >/dev/null || fail "rendered launchd plist is invalid"
  if [[ "${NO_START}" -eq 0 ]]; then
    service="gui/$(id -u)/com.moox.factor-engine"
    launchctl bootout "${service}" >/dev/null 2>&1 || true
    # bootout returns before the old job is gone; bootstrapping over it fails
    # with "Input/output error", so wait for the unload and retry briefly.
    for _ in $(seq 1 20); do
      launchctl print "${service}" >/dev/null 2>&1 || break
      sleep 0.5
    done
    bootstrapped=0
    for _ in 1 2 3 4 5; do
      if launchctl bootstrap "gui/$(id -u)" "${plist}" 2>/dev/null; then
        bootstrapped=1
        break
      fi
      sleep 1
    done
    [[ "${bootstrapped}" -eq 1 ]] || fail "launchctl bootstrap ${plist} failed"
  fi
else
  mkdir -p "${SYSTEMD_USER_DIR}"
  render_template "${ROOT}/deploy/systemd/user/moox-factor-engine.service.tmpl" >"${SYSTEMD_USER_DIR}/moox-factor-engine.service"
  if command -v loginctl >/dev/null 2>&1 && [[ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)" != yes ]]; then
    log "warning: user lingering is off; the engine stops at logout (enable with: loginctl enable-linger $(id -un))"
  fi
  if [[ "${NO_START}" -eq 0 ]]; then
    systemctl --user daemon-reload
    systemctl --user enable moox-factor-engine.service
    systemctl --user restart moox-factor-engine.service
  fi
fi

if [[ "${NO_START}" -eq 1 ]]; then
  log "installed to ${DEPLOY_DIR} (not started)"
  exit 0
fi

log "wait for moox-factor-engine readiness"
for _ in $(seq 1 90); do
  if env -i PATH=/usr/bin:/bin MOOX_HEALTH_AUTH_ACCESS_KEY="${health_access}" MOOX_HEALTH_AUTH_SECRET_KEY="${health_secret}" \
    "${DEPLOY_DIR}/bin/moox-factor-engine" health >/dev/null 2>&1; then
    log "moox-factor-engine is ready (${ENGINE_ID})"
    exit 0
  fi
  sleep 2
done
env -i PATH=/usr/bin:/bin MOOX_HEALTH_AUTH_ACCESS_KEY="${health_access}" MOOX_HEALTH_AUTH_SECRET_KEY="${health_secret}" \
  "${DEPLOY_DIR}/bin/moox-factor-engine" health || true
tail -n 30 "${DEPLOY_DIR}/logs/factor-engine.log" >&2 || true
fail "moox-factor-engine did not become ready"
