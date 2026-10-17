#!/usr/bin/env bash
# Installs moox-archive-mgr on the control host as a systemd user service that lives next to — not inside — the
# moox-runtime managed release (the deployed moox-runtime embeds an older service catalog and would reject the
# component). Run from an operator machine; it renders locally and applies over SSH.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TARGET=""
REMOTE_DIR="/data/moox/prod/archive"
HOST_GATEWAY_DIR="/data/moox/host/current/host-gateway"
RUNTIME_ENV_JSON="/data/moox/prod/current/secrets/runtime-factor-mgr.json"
BINARY=""
KEY_ID=""
KEY_FILE=""
STAGE_DIR=""
RENDER_ONLY=0
NO_START=0

usage() {
  cat <<'EOF2'
Usage:
  scripts/deploy/deploy-archive-mgr.sh --target user@host --binary PATH --key-id ID --key-file PATH [options]

Options:
  --target <user@host>        SSH target of the control host.
  --dir <path>                Remote install directory. Default: /data/moox/prod/archive.
  --binary <path>             Linux amd64 moox-archive-mgr built by scripts/build/build.sh archive-mgr.
  --key-id <id>               KeyID assigned by `moox-admin-cli keys export --caller archive-mgr`.
  --key-file <path>           Local file with the exported internal signing key (installed with mode 0600).
  --host-gateway-dir <path>   Remote host-gateway unit directory. Default: /data/moox/host/current/host-gateway.
  --runtime-env-json <path>   Remote runtime environment JSON to copy the shared health credentials and the Storage
                              Primary auth secret from. Default: /data/moox/prod/current/secrets/runtime-factor-mgr.json.
  --stage-dir <path>          Keep the rendered tree here instead of a temporary directory.
  --render-only               Render the tree and stop (no SSH).
  --no-start                  Install files only; do not (re)start the service.
EOF2
}

fail() { printf '[deploy-archive-mgr] ERROR: %s\n' "$*" >&2; exit 1; }
log() { printf '[deploy-archive-mgr] %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET="$2"; shift 2 ;;
    --dir) REMOTE_DIR="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --key-id) KEY_ID="$2"; shift 2 ;;
    --key-file) KEY_FILE="$2"; shift 2 ;;
    --host-gateway-dir) HOST_GATEWAY_DIR="$2"; shift 2 ;;
    --runtime-env-json) RUNTIME_ENV_JSON="$2"; shift 2 ;;
    --stage-dir) STAGE_DIR="$2"; shift 2 ;;
    --render-only) RENDER_ONLY=1; shift ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown option: $1" ;;
  esac
done

[[ "${REMOTE_DIR}" = /* && "${REMOTE_DIR}" =~ ^[A-Za-z0-9/_.-]+$ ]] || fail "--dir must be a simple absolute path"
[[ "${KEY_ID}" =~ ^[A-Za-z0-9._@:-]+$ && ${#KEY_ID} -le 128 ]] || fail "--key-id must be the assigned credential identifier"
[[ -f "${BINARY}" ]] || fail "missing --binary ${BINARY}"
[[ -f "${KEY_FILE}" && ! -L "${KEY_FILE}" ]] || fail "missing --key-file ${KEY_FILE}"
if [[ "${RENDER_ONLY}" -eq 0 ]]; then
  [[ "${TARGET}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$ ]] || fail "--target must be user@host"
fi

own_stage=0
if [[ -z "${STAGE_DIR}" ]]; then
  STAGE_DIR="$(mktemp -d)"
  own_stage=1
fi
cleanup() { [[ "${own_stage}" -eq 1 ]] && rm -rf "${STAGE_DIR}"; return 0; }
trap cleanup EXIT

render_unit() {
  sed -e "s#__ROOT__#${REMOTE_DIR}#g" "${ROOT}/deploy/systemd/user/moox-archive-mgr.service.tmpl"
}

rm -rf "${STAGE_DIR:?}/tree"
mkdir -p "${STAGE_DIR}/tree/bin" "${STAGE_DIR}/tree/archive-mgr/config"
install -m 0755 "${BINARY}" "${STAGE_DIR}/tree/bin/moox-archive-mgr"
sed -e "s#^\( *key_id:\).*#\1 \"${KEY_ID}\"#" "${ROOT}/modules/archive/config/app.yaml" >"${STAGE_DIR}/tree/archive-mgr/config/app.yaml"
grep -q "key_id: \"${KEY_ID}\"" "${STAGE_DIR}/tree/archive-mgr/config/app.yaml" || fail "could not render key_id"
install -m 0644 "${ROOT}/modules/archive/config/trpc_go.yaml" "${STAGE_DIR}/tree/archive-mgr/config/trpc_go.yaml"
render_unit >"${STAGE_DIR}/moox-archive-mgr.service"
if grep -q '__[A-Z_]*__' "${STAGE_DIR}/moox-archive-mgr.service"; then fail "unrendered placeholder in the service definition"; fi

if [[ "${RENDER_ONLY}" -eq 1 ]]; then
  log "rendered to ${STAGE_DIR} (--render-only)"
  [[ "${own_stage}" -eq 0 ]] || log "(temporary stage removed on exit; pass --stage-dir to keep it)"
  exit 0
fi

ssh_opts=(-o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=15)
run_ssh() { ssh "${ssh_opts[@]}" "${TARGET}" "$@"; }

log "prepare ${TARGET}:${REMOTE_DIR}"
run_ssh "set -eu; umask 077; mkdir -p '${REMOTE_DIR}/bin' '${REMOTE_DIR}/archive-mgr/config' '${REMOTE_DIR}/archive-mgr/data' '${REMOTE_DIR}/archive-mgr/logs' '${REMOTE_DIR}/secrets' ~/.config/systemd/user; \
  [ -e '${HOST_GATEWAY_DIR}/config/app.yaml' ] || { echo 'host-gateway unit not found at ${HOST_GATEWAY_DIR}' >&2; exit 1; }; \
  ln -sfn '${HOST_GATEWAY_DIR}' '${REMOTE_DIR}/host-gateway'; \
  [ ! -f '${REMOTE_DIR}/bin/moox-archive-mgr' ] || cp -p '${REMOTE_DIR}/bin/moox-archive-mgr' '${REMOTE_DIR}/bin/moox-archive-mgr.prev'"

log "upload binary, configuration and signing key"
rsync -az -e "ssh ${ssh_opts[*]}" "${STAGE_DIR}/tree/bin/moox-archive-mgr" "${TARGET}:${REMOTE_DIR}/bin/.moox-archive-mgr.new"
rsync -az -e "ssh ${ssh_opts[*]}" "${STAGE_DIR}/tree/archive-mgr/config/" "${TARGET}:${REMOTE_DIR}/archive-mgr/config/"
rsync -az -e "ssh ${ssh_opts[*]}" "${STAGE_DIR}/moox-archive-mgr.service" "${TARGET}:.config/systemd/user/moox-archive-mgr.service"
ssh "${ssh_opts[@]}" "${TARGET}" "umask 077; cat > '${REMOTE_DIR}/secrets/caller-archive-mgr.key'" <"${KEY_FILE}"

log "write the service environment (shared health credentials and Storage auth secret, never printed)"
run_ssh "set -eu; umask 077; python3 - <<'PY'
import json, os
src = json.load(open('${RUNTIME_ENV_JSON}'))
keys = ['MOOX_HEALTH_AUTH_VERSION', 'MOOX_HEALTH_AUTH_ACCESS_KEY', 'MOOX_HEALTH_AUTH_SECRET_KEY', 'MOOX_STORAGE_PRIMARY_AUTH_SECRET']
missing = [k for k in keys if not src.get(k)]
if missing:
    raise SystemExit('runtime environment is missing ' + ', '.join(missing))
path = '${REMOTE_DIR}/secrets/archive-mgr.env'
tmp = path + '.new'
with open(tmp, 'w') as f:
    for k in keys:
        f.write('%s=%s\n' % (k, src[k]))
os.chmod(tmp, 0o600)
os.replace(tmp, path)
PY
chmod 0600 '${REMOTE_DIR}/secrets/caller-archive-mgr.key' '${REMOTE_DIR}/secrets/archive-mgr.env'; \
  mv -f '${REMOTE_DIR}/bin/.moox-archive-mgr.new' '${REMOTE_DIR}/bin/moox-archive-mgr'; chmod 0755 '${REMOTE_DIR}/bin/moox-archive-mgr'"

run_ssh "if [ \"\$(loginctl show-user \$(id -un) -p Linger --value 2>/dev/null)\" != yes ]; then loginctl enable-linger \$(id -un) 2>/dev/null || echo 'warning: user lingering is off; the service stops at logout (enable with: loginctl enable-linger \$(id -un))' >&2; fi"

if [[ "${NO_START}" -eq 1 ]]; then
  log "installed to ${TARGET}:${REMOTE_DIR} (not started)"
  exit 0
fi

log "start moox-archive-mgr"
run_ssh "systemctl --user daemon-reload && systemctl --user enable moox-archive-mgr.service >/dev/null 2>&1 && systemctl --user restart moox-archive-mgr.service"
log "wait for readiness"
for _ in $(seq 1 45); do
  if run_ssh "set -a; . '${REMOTE_DIR}/secrets/archive-mgr.env'; set +a; env -u HTTP_PROXY -u HTTPS_PROXY '${REMOTE_DIR}/bin/moox-archive-mgr' health" >/dev/null 2>&1; then
    log "moox-archive-mgr is ready"
    exit 0
  fi
  sleep 2
done
run_ssh "tail -n 30 '${REMOTE_DIR}/archive-mgr/logs/archive-mgr.log'; systemctl --user status moox-archive-mgr.service --no-pager | head -15" >&2 || true
fail "moox-archive-mgr did not become ready"
