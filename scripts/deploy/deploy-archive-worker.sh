#!/usr/bin/env bash
# Installs moox-archive-worker as a systemd user unit on the Linux machine that
# runs it. The worker only dials out: to the fixed native Access for archive-mgr
# and Storage. moox-cli unpacks the release package, uploads the credentials and
# runs this script (setup deploy-unit --profile archive-worker).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
DEPLOY_DIR="${HOME}/moox/archive-worker"
SECRETS_DIR=""
ROOT_DIR=""
WORKER_ID=""
ACCESS_ADDRESS=""
ACCESS_ID=""
ACCESS_KEY_ID=""
BINARY=""
HEALTH_ENV=""
SKIP_BUILD=0
NO_START=0
SYSTEMD_USER_DIR="${MOOX_SYSTEMD_USER_DIR:-${HOME}/.config/systemd/user}"

usage() {
  cat <<'EOF2'
Usage:
  scripts/deploy/deploy-archive-worker.sh --root-dir DIR --access-address HOST:PORT --access-id access@HOST \
    --access-key-id KEY_ID [options]

Options:
  --dir <path>              Install directory (binary, config, logs). Default: ~/moox/archive-worker.
  --root-dir <path>         Archive root: partition files and the .state staging database live here. Absolute path, required.
  --secrets-dir <path>      Directory holding the worker credentials. Default: <dir>/secrets.
  --worker-id <id>          Stable worker id. Default: archive-worker@<short hostname>. Keep it when moving the archive root to another machine.
  --access-address <addr>   Fixed native Access host:port for archive-mgr and Storage.
  --access-id <id>          Access identity, e.g. access@storage.
  --access-key-id <id>      KeyID assigned by Admin keys export --caller archive-worker.
  --binary <path>           Install this prebuilt moox-archive-worker (from the release package).
  --health-env <path>       Health credential file (MOOX_HEALTH_AUTH_* variables, mode 0600) rendered by moox-cli.
                            Required for the first deployment; later deployments keep the installed one.
  --skip-build              Reuse <dir>/bin/moox-archive-worker.
  --no-start                Install files only; do not (re)start the service.

Credentials expected in the secrets directory (regular files, mode 0600):
  access-archive-worker.key          External archive-worker signing key exported by Admin
  storage-primary-auth.secret        MOOX_STORAGE_PRIMARY_AUTH_SECRET of the Storage deployment
EOF2
}

fail() {
  printf '[deploy-archive-worker] ERROR: %s\n' "$*" >&2
  exit 1
}

log() {
  printf '[deploy-archive-worker] %s\n' "$*"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DEPLOY_DIR="$2"; shift 2 ;;
    --root-dir) ROOT_DIR="$2"; shift 2 ;;
    --secrets-dir) SECRETS_DIR="$2"; shift 2 ;;
    --worker-id) WORKER_ID="$2"; shift 2 ;;
    --access-address) ACCESS_ADDRESS="$2"; shift 2 ;;
    --access-id) ACCESS_ID="$2"; shift 2 ;;
    --access-key-id) ACCESS_KEY_ID="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --health-env) HEALTH_ENV="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown option: $1" ;;
  esac
done

[[ "${ROOT_DIR}" = /* && "${ROOT_DIR}" != *$'\n'* ]] || fail "--root-dir must be an absolute path"
[[ "${ACCESS_ADDRESS}" =~ ^(\[[0-9a-fA-F:]+\]|[A-Za-z0-9.-]+):[0-9]+$ ]] || fail "--access-address must be host:port"
[[ "${ACCESS_ID}" =~ ^access@[a-z][a-z0-9]*(-[a-z0-9]+)*$ ]] || fail "--access-id must be access@host"
[[ "${ACCESS_KEY_ID}" =~ ^[A-Za-z0-9._@:-]+$ && ${#ACCESS_KEY_ID} -le 128 ]] || fail "--access-key-id must be the assigned credential identifier"
mkdir -p "${DEPLOY_DIR}"
DEPLOY_DIR="$(cd "${DEPLOY_DIR}" && pwd -P)"
SECRETS_DIR="${SECRETS_DIR:-${DEPLOY_DIR}/secrets}"
if [[ -z "${WORKER_ID}" ]]; then
  WORKER_ID="archive-worker@$(hostname -s)"
fi
[[ "${WORKER_ID}" =~ ^[A-Za-z0-9@._-]+$ ]] || fail "worker id contains unsupported characters"

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then stat -f '%Lp' "$1"; else stat -c '%a' "$1"; fi
}

require_secret() {
  local path="${SECRETS_DIR}/$1"
  [[ -f "${path}" && ! -L "${path}" ]] || fail "missing credential ${path}"
  [[ "$(file_mode "${path}")" == 600 ]] || fail "credential ${path} must have mode 0600"
  grep -q '[^[:space:]]' "${path}" || fail "credential ${path} is empty"
}

mkdir -p "${DEPLOY_DIR}/bin" "${DEPLOY_DIR}/config" "${DEPLOY_DIR}/logs" "${ROOT_DIR}"
mkdir -p "${SECRETS_DIR}"
chmod 0700 "${SECRETS_DIR}"
for secret in access-archive-worker.key storage-primary-auth.secret; do
  require_secret "${secret}"
done

if [[ -n "${BINARY}" ]]; then
  [[ -f "${BINARY}" ]] || fail "missing binary ${BINARY}"
  install -m 0755 "${BINARY}" "${DEPLOY_DIR}/bin/moox-archive-worker.new"
  mv -f "${DEPLOY_DIR}/bin/moox-archive-worker.new" "${DEPLOY_DIR}/bin/moox-archive-worker"
fi
[[ -x "${DEPLOY_DIR}/bin/moox-archive-worker" ]] || fail "missing ${DEPLOY_DIR}/bin/moox-archive-worker"

umask 077
cat >"${DEPLOY_DIR}/config/app.yaml" <<EOF2
# Rendered by scripts/deploy/deploy-archive-worker.sh; edit the flags, not this file.
gateway_client:
  access_address: "${ACCESS_ADDRESS}"
  access_id: "${ACCESS_ID}"
  caller: archive-worker
  key_id: "${ACCESS_KEY_ID}"
  key_file: ${SECRETS_DIR}/access-archive-worker.key

storage_auth_file: ${SECRETS_DIR}/storage-primary-auth.secret

worker:
  worker_id: ${WORKER_ID}
  root_dir: ${ROOT_DIR}
  export_formats: [csv]
  min_free_bytes: 5GB
  heartbeat_interval: 10s
  pull_interval: 5s

staging:
  cache_size: 256MB
  rebuild_ratio: 2

pacing:
  request_seconds: [25, 55]
  minute_window: true
  hour_quiet: ["58:00", "04:00"]
  request_interval: 200ms
  max_bytes_per_second: 512KB
  max_concurrent_runs: 4
  max_runs_per_data_node: 2
  page_rows: 500

health:
  addr: 127.0.0.1:11418

maintenance:
  at: "04:17:43"
EOF2
umask 022

# 健康检查凭据由 moox-cli 按集群运行身份渲染后传入（Monitor 经中转探测时用同一把）；重复部署不带 --health-env 时沿用已有的。
health_env="${SECRETS_DIR}/health-auth.env"
if [[ -n "${HEALTH_ENV}" ]]; then
  [[ -f "${HEALTH_ENV}" && ! -L "${HEALTH_ENV}" ]] || fail "missing --health-env file ${HEALTH_ENV}"
  [[ "$(file_mode "${HEALTH_ENV}")" == 600 ]] || fail "--health-env file ${HEALTH_ENV} must have mode 0600"
  for var in MOOX_HEALTH_AUTH_VERSION MOOX_HEALTH_AUTH_ACCESS_KEY MOOX_HEALTH_AUTH_SECRET_KEY; do
    grep -Eq "^${var}=[^[:space:]]+$" "${HEALTH_ENV}" || fail "--health-env file lacks ${var}"
  done
  (umask 077; cp "${HEALTH_ENV}" "${health_env}.new" && mv -f "${health_env}.new" "${health_env}")
fi
[[ -s "${health_env}" ]] || fail "--health-env is required for the first deployment"
health_access="$(sed -n 's/^MOOX_HEALTH_AUTH_ACCESS_KEY=//p' "${health_env}")"
health_secret="$(sed -n 's/^MOOX_HEALTH_AUTH_SECRET_KEY=//p' "${health_env}")"
[[ -n "${health_access}" && -n "${health_secret}" ]] || fail "invalid ${health_env}"

render_template() {
  sed -e "s#__ROOT__#${DEPLOY_DIR}#g" -e "s#__SECRETS__#${SECRETS_DIR}#g" -e "s#__HEALTH_ACCESS_KEY__#${health_access}#g" \
    -e "s#__HEALTH_SECRET_KEY__#${health_secret}#g" "$1"
}

mkdir -p "${SYSTEMD_USER_DIR}"
render_template "${ROOT}/deploy/systemd/user/moox-archive-worker.service.tmpl" >"${SYSTEMD_USER_DIR}/moox-archive-worker.service"
if command -v loginctl >/dev/null 2>&1 && [[ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)" != yes ]]; then
  log "warning: user lingering is off; the worker stops at logout (enable with: loginctl enable-linger $(id -un))"
fi
if [[ "${NO_START}" -eq 0 ]]; then
  systemctl --user daemon-reload
  systemctl --user enable moox-archive-worker.service
  systemctl --user restart moox-archive-worker.service
fi

if [[ "${NO_START}" -eq 1 ]]; then
  log "installed to ${DEPLOY_DIR} (not started)"
  exit 0
fi

log "wait for moox-archive-worker readiness"
for _ in $(seq 1 90); do
  if env -i PATH=/usr/bin:/bin MOOX_HEALTH_AUTH_ACCESS_KEY="${health_access}" MOOX_HEALTH_AUTH_SECRET_KEY="${health_secret}" \
    "${DEPLOY_DIR}/bin/moox-archive-worker" health >/dev/null 2>&1; then
    log "moox-archive-worker is ready (${WORKER_ID})"
    exit 0
  fi
  sleep 2
done
env -i PATH=/usr/bin:/bin MOOX_HEALTH_AUTH_ACCESS_KEY="${health_access}" MOOX_HEALTH_AUTH_SECRET_KEY="${health_secret}" \
  "${DEPLOY_DIR}/bin/moox-archive-worker" health || true
tail -n 30 "${DEPLOY_DIR}/logs/archive-worker.log" >&2 || true
fail "moox-archive-worker did not become ready"
