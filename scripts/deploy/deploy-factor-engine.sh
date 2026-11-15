#!/usr/bin/env bash
# Installs moox-factor-engine as a systemd user unit on the Linux machine that
# runs it. The engine only dials out: to native Access for FactorMgr and Storage,
# and to EventBus. moox-cli unpacks the release package, uploads the credentials
# and runs this script (setup deploy-unit --profile factor-engine).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
DEPLOY_DIR="${HOME}/moox/factor-engine"
SECRETS_DIR=""
ENGINE_ID=""
ACCESS_ADDRESS=""
ACCESS_ID=""
ACCESS_KEY_ID=""
EVENTBUS_URL=""
BINARY=""
SKIP_BUILD=0
NO_START=0
SYSTEMD_USER_DIR="${MOOX_SYSTEMD_USER_DIR:-${HOME}/.config/systemd/user}"

usage() {
  cat <<'EOF'
Usage:
  scripts/deploy/deploy-factor-engine.sh --access-address HOST:PORT --access-id access@HOST \
    --access-key-id KEY_ID --eventbus-url URL [options]

Options:
  --dir <path>              Install directory. Default: ~/moox/factor-engine.
  --secrets-dir <path>      Directory holding the engine credentials. Default: <dir>/secrets.
  --engine-id <id>          Stable engine id. Default: factor-engine@<short hostname>.
  --access-address <addr>  Fixed native Access host:port for both FactorMgr and Storage.
  --access-id <id>         Access identity, e.g. access@storage.
  --access-key-id <id>     KeyID assigned by Admin keys export --caller factor-engine.
  --eventbus-url <url>      EventBus URL, e.g. tls://eventbus.example.com:4222.
  --binary <path>           Install this prebuilt moox-factor-engine (from the release package).
  --skip-build              Reuse <dir>/bin/moox-factor-engine.
  --no-start                Install files only; do not (re)start the service.

Credentials expected in the secrets directory (regular files, mode 0600):
  access-factor-engine.key           External factor-engine signing key exported by Admin
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
    --access-address) ACCESS_ADDRESS="$2"; shift 2 ;;
    --access-id) ACCESS_ID="$2"; shift 2 ;;
    --access-key-id) ACCESS_KEY_ID="$2"; shift 2 ;;
    --eventbus-url) EVENTBUS_URL="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown option: $1" ;;
  esac
done

[[ "${ACCESS_ADDRESS}" =~ ^(\[[0-9a-fA-F:]+\]|[A-Za-z0-9.-]+):[0-9]+$ ]] || fail "--access-address must be host:port"
[[ "${ACCESS_ID}" =~ ^access@[a-z][a-z0-9]*(-[a-z0-9]+)*$ ]] || fail "--access-id must be access@host"
[[ "${ACCESS_KEY_ID}" =~ ^[A-Za-z0-9._@:-]+$ && ${#ACCESS_KEY_ID} -le 128 ]] || fail "--access-key-id must be the assigned credential identifier"
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

mkdir -p "${DEPLOY_DIR}/bin" "${DEPLOY_DIR}/config" "${DEPLOY_DIR}/data/engine" "${DEPLOY_DIR}/logs"
mkdir -p "${SECRETS_DIR}"
chmod 0700 "${SECRETS_DIR}"
for secret in access-factor-engine.key factor-eventbus.yaml; do
  require_secret "${secret}"
done
if [[ -n "${BINARY}" ]]; then
  [[ -f "${BINARY}" ]] || fail "missing binary ${BINARY}"
  install -m 0755 "${BINARY}" "${DEPLOY_DIR}/bin/moox-factor-engine.new"
  mv -f "${DEPLOY_DIR}/bin/moox-factor-engine.new" "${DEPLOY_DIR}/bin/moox-factor-engine"
fi
[[ -x "${DEPLOY_DIR}/bin/moox-factor-engine" ]] || fail "missing ${DEPLOY_DIR}/bin/moox-factor-engine"

install -m 0644 "${ROOT}/modules/factor/config/trpc_go.engine.yaml" "${DEPLOY_DIR}/config/trpc_go.engine.yaml"

umask 077
cat >"${DEPLOY_DIR}/config/engine.yaml" <<EOF
# Rendered by scripts/deploy/deploy-factor-engine.sh; edit the flags, not this file.
engine:
  id: ${ENGINE_ID}
  heartbeat_interval: 10s

gateway_client:
  access_address: "${ACCESS_ADDRESS}"
  access_id: "${ACCESS_ID}"
  caller: factor-engine
  key_id: "${ACCESS_KEY_ID}"
  key_file: ${SECRETS_DIR}/access-factor-engine.key

manager:
  timeout: 30s

catalog_sync:
  interval: 1m
  offset: 45s
  state_file: ./data/engine/catalog.json

eventbus:
  urls:
    - ${EVENTBUS_URL}
  credential_file: ${SECRETS_DIR}/factor-eventbus.yaml
  fetch_max_wait: 10s

compute:
  max_memory: 4GiB

pipeline:
  write_batch_rows: 1000
  period_budget_min: 60s
  period_budget_max: 15m

recalc:
  chunk_periods: 500
  poll_interval: 5s
EOF
umask 022

# 窗口与进程内存预算按本机内存校准后写入（tune 只写还没设置的项）；失败只告警，引擎仍可按默认值（不限制）运行。
if ! "${DEPLOY_DIR}/bin/moox-factor-engine" tune --config "${DEPLOY_DIR}/config/engine.yaml" --write >/dev/null 2>&1; then
  log "WARN: moox-factor-engine tune failed; run it manually: moox-factor-engine tune --config ${DEPLOY_DIR}/config/engine.yaml --write"
fi

render_template() {
  sed -e "s#__ROOT__#${DEPLOY_DIR}#g" -e "s#__SECRETS__#${SECRETS_DIR}#g" "$1"
}

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

if [[ "${NO_START}" -eq 1 ]]; then
  log "installed to ${DEPLOY_DIR} (not started)"
  exit 0
fi

log "wait for moox-factor-engine readiness"
for _ in $(seq 1 90); do
  if env -i PATH=/usr/bin:/bin "${DEPLOY_DIR}/bin/moox-factor-engine" health >/dev/null 2>&1; then
    log "moox-factor-engine is ready (${ENGINE_ID})"
    exit 0
  fi
  sleep 2
done
env -i PATH=/usr/bin:/bin "${DEPLOY_DIR}/bin/moox-factor-engine" health || true
tail -n 30 "${DEPLOY_DIR}/logs/factor-engine.log" >&2 || true
fail "moox-factor-engine did not become ready"
