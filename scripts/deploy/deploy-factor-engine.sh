#!/usr/bin/env bash
# Installs moox-factor-engine on the machine that runs it (macOS with launchd,
# Linux with a systemd user unit). The engine only dials out: to
# moox-factor-mgr through the control host's gateway HTTPS entry, to EventBus
# and to storage-access on the Storage host.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OS="$(uname -s)"
if [[ "${OS}" == Darwin ]]; then
  DEPLOY_DIR="${HOME}/Documents/moox-deploy"
else
  DEPLOY_DIR="${HOME}/moox-factor-engine"
fi
SECRETS_DIR=""
ENGINE_ID=""
MANAGER_URL=""
MANAGER_NODE_ID=""
MANAGER_CA=""
STORAGE_TARGET=""
STORAGE_NODE_ID=""
EVENTBUS_URL=""
SKIP_BUILD=0
NO_START=0
LAUNCH_AGENTS_DIR="${MOOX_LAUNCH_AGENTS_DIR:-${HOME}/Library/LaunchAgents}"
SYSTEMD_USER_DIR="${MOOX_SYSTEMD_USER_DIR:-${HOME}/.config/systemd/user}"

usage() {
  cat <<'EOF'
Usage:
  scripts/deploy/deploy-factor-engine.sh --manager-url URL --manager-node-id ID \
    --storage-target ip://HOST:PORT --storage-node-id ID --eventbus-url URL [options]

Options:
  --dir <path>              Install directory. Default: ~/Documents/moox-deploy (macOS), ~/moox-factor-engine (Linux).
  --secrets-dir <path>      Directory holding the engine credentials. Default: <dir>/secrets.
  --engine-id <id>          Stable engine id. Default: factor-engine@<short hostname>.
  --manager-url <url>       Control host service HTTPS entry, e.g. https://106.53.107.122:11001.
  --manager-node-id <id>    Gateway node id of the control host.
  --manager-ca <path>       Caddy root certificate of the control host (copied to secrets/).
  --storage-target <target> storage-access tRPC target, e.g. ip://146.56.196.204:11004.
  --storage-node-id <id>    storage-access inbound target node id.
  --eventbus-url <url>      EventBus URL, e.g. tls://106.53.107.122:4222.
  --skip-build              Reuse <dir>/bin/moox-factor-engine.
  --no-start                Install files only; do not (re)start the service.

Credentials expected in the secrets directory (regular files, mode 0600):
  gateway-factor-engine.key          factor-engine gateway service key (control host secrets/)
  storage-access-factor-engine.key   factor-engine storage-access key (Storage host secrets/)
  storage-primary-auth.secret        MOOX_STORAGE_PRIMARY_AUTH_SECRET of the Storage deployment
  factor-eventbus.yaml               factor EventBus role credential (its ca_file next to it)
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
    --manager-url) MANAGER_URL="$2"; shift 2 ;;
    --manager-node-id) MANAGER_NODE_ID="$2"; shift 2 ;;
    --manager-ca) MANAGER_CA="$2"; shift 2 ;;
    --storage-target) STORAGE_TARGET="$2"; shift 2 ;;
    --storage-node-id) STORAGE_NODE_ID="$2"; shift 2 ;;
    --eventbus-url) EVENTBUS_URL="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown option: $1" ;;
  esac
done

[[ "${MANAGER_URL}" =~ ^https?:// ]] || fail "--manager-url must be an http(s) URL"
[[ -n "${MANAGER_NODE_ID}" ]] || fail "--manager-node-id is required"
[[ "${STORAGE_TARGET}" =~ ^ip://[^/]+:[0-9]+$ ]] || fail "--storage-target must be ip://host:port"
[[ -n "${STORAGE_NODE_ID}" ]] || fail "--storage-node-id is required"
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
for secret in gateway-factor-engine.key storage-access-factor-engine.key storage-primary-auth.secret factor-eventbus.yaml; do
  require_secret "${secret}"
done
if [[ -n "${MANAGER_CA}" ]]; then
  [[ -s "${MANAGER_CA}" ]] || fail "--manager-ca ${MANAGER_CA} is missing"
  install -m 0600 "${MANAGER_CA}" "${SECRETS_DIR}/control-caddy-root.crt"
fi

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

manager_ca_line=""
if [[ -s "${SECRETS_DIR}/control-caddy-root.crt" ]]; then
  manager_ca_line="  ca_file: ${SECRETS_DIR}/control-caddy-root.crt"
fi
umask 077
cat >"${DEPLOY_DIR}/config/engine.yaml" <<EOF
# Rendered by scripts/deploy/deploy-factor-engine.sh; edit the flags, not this file.
engine:
  id: ${ENGINE_ID}
  heartbeat_interval: 10s

manager:
  url: ${MANAGER_URL}
  node_id: ${MANAGER_NODE_ID}
${manager_ca_line}
  key_id: factor-engine
  hmac_key_file: ${SECRETS_DIR}/gateway-factor-engine.key
  timeout: 30s

catalog_sync:
  interval: 1m
  offset: 45s
  state_file: ./data/engine/catalog.json

storage:
  gateway_target: "${STORAGE_TARGET}"
  gateway_node_id: "${STORAGE_NODE_ID}"
  key_id: factor-engine
  hmac_key_file: ${SECRETS_DIR}/storage-access-factor-engine.key
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
