#!/usr/bin/env bash
set -euo pipefail

CADDY_VERSION=v2.11.4
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
CHECKSUMS_DEFAULT="${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
RELEASE_URL=https://github.com/caddyserver/caddy/releases/download/v2.11.4

fail() { printf '[caddy-managed] ERROR: %s\n' "$*" >&2; exit 1; }
log() { printf '[caddy-managed] %s\n' "$*" >&2; }
normalize_os() { local value; value=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]'); case "${value}" in linux) echo linux;; darwin|macos) echo mac;; *) fail "unsupported OS: $1";; esac; }
normalize_arch() { local value; value=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]'); case "${value}" in amd64|x86_64) echo amd64;; arm64|aarch64) echo arm64;; *) fail "unsupported architecture: $1";; esac; }
sha512() { if command -v sha512sum >/dev/null; then sha512sum "$1" | awk '{print $1}'; else shasum -a 512 "$1" | awk '{print $1}'; fi; }

COMMAND=${1:-}; [[ -n "${COMMAND}" ]] || fail 'command required: check|install|ensure|start|reload|stop|rollback|status'
shift
DEPLOY_DIR=${MOOX_DEPLOY_DIR:-${HOME}/moox}
OS=${MOOX_CADDY_OS:-$(uname -s)}
ARCH=${MOOX_CADDY_ARCH:-$(uname -m)}
CONFIG_SOURCE=
PORTS=9527,11001
ADMIN_ENDPOINT=${MOOX_CADDY_ADMIN_ENDPOINT:-127.0.0.1:2019}
ADMIN_PATH=${MOOX_CADDY_ADMIN_PATH:-/config/}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --deploy-dir) DEPLOY_DIR=${2:?}; shift 2;;
    --os) OS=${2:?}; shift 2;;
    --arch) ARCH=${2:?}; shift 2;;
    --config) CONFIG_SOURCE=${2:?}; shift 2;;
    --ports) PORTS=${2:?}; shift 2;;
    *) fail "unknown option: $1";;
  esac
done
OS=$(normalize_os "${OS}"); ARCH=$(normalize_arch "${ARCH}")
BIN="${DEPLOY_DIR}/bin/caddy"; CONFIG="${DEPLOY_DIR}/config/caddy/Caddyfile"; PIDFILE="${DEPLOY_DIR}/run/caddy.pid"
export XDG_DATA_HOME="${DEPLOY_DIR}/data/caddy" XDG_CONFIG_HOME="${DEPLOY_DIR}/config/caddy/runtime"

version_ok() { [[ -x "${BIN}" ]] && [[ $("${BIN}" version 2>/dev/null | awk '{print $1}') == "${CADDY_VERSION}" ]]; }
pid_owned() {
  [[ -s "${PIDFILE}" ]] || return 1
  local pid exe
  pid=$(cat "${PIDFILE}"); [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null || return 1
  [[ ${MOOX_CADDY_SKIP_PID_EXE_CHECK:-0} == 1 ]] && return 0
  if [[ -e "/proc/${pid}/exe" ]]; then exe=$(readlink "/proc/${pid}/exe"); [[ "${exe}" == "${BIN}" ]]; return; fi
  exe=$(ps -p "${pid}" -o command= 2>/dev/null || true); [[ "${exe}" == "${BIN} "* || "${exe}" == "${BIN}" ]]
}
clean_stale_pid() { [[ ! -e "${PIDFILE}" ]] || pid_owned || { log 'removing stale or mismatched PID file without signaling it'; rm -f "${PIDFILE}"; }; }
validate() { [[ -s "${CONFIG}" ]] || fail "managed config missing: ${CONFIG}"; "${BIN}" validate --config "${CONFIG}" --adapter caddyfile >/dev/null; }
check_ports() {
  local port owners pid
  IFS=, read -ra ports <<<"${PORTS}"
  for port in "${ports[@]}"; do
    owners=$(port_pids "${port}")
    [[ -z "${owners}" ]] && continue
    if pid_owned; then pid=$(cat "${PIDFILE}"); [[ " ${owners//$'\n'/ } " == *" ${pid} "* ]] && continue; fi
    fail "edge port ${port} is occupied by unrelated PID(s): ${owners//$'\n'/,}; refusing takeover"
  done
}

port_pids() {
  local port="$1" hex inode fd pid
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null | sort -u || true
    return
  fi
  if command -v ss >/dev/null 2>&1; then
    ss -H -ltnp "sport = :${port}" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u || true
    return
  fi
  [[ -r /proc/net/tcp ]] || fail 'cannot inspect listener ownership: install ss or lsof'
  hex=$(printf '%04X' "${port}")
  for inode in $(awk -v p=":${hex}" '$2 ~ p"$" && $4=="0A" {print $10}' /proc/net/tcp /proc/net/tcp6 2>/dev/null); do
    for fd in /proc/[0-9]*/fd/*; do
      [[ $(readlink "${fd}" 2>/dev/null || true) == "socket:[${inode}]" ]] || continue
      pid=${fd#/proc/}; pid=${pid%%/*}; printf '%s\n' "${pid}"
    done
  done | sort -u
}

admin_healthy() {
  pid_owned || return 1
  local pid admin_port owners
  pid=$(cat "${PIDFILE}"); admin_port=${ADMIN_ENDPOINT##*:}
  owners=$(port_pids "${admin_port}")
  [[ " ${owners//$'\n'/ } " == *" ${pid} "* ]] || return 1
  curl --fail --silent --max-time 2 "http://${ADMIN_ENDPOINT}${ADMIN_PATH}" >/dev/null
}
install_binary() {
  local asset archive expected actual tmp old
  asset="caddy_2.11.4_${OS}_${ARCH}.tar.gz"
  archive=${MOOX_CADDY_ARCHIVE:-}
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/moox-caddy-install.XXXXXX"); trap 'rm -rf "${tmp}"' RETURN
  if [[ -z "${archive}" ]]; then archive="${tmp}/${asset}"; curl -fL --retry 3 --connect-timeout 10 --max-time 180 -o "${archive}" "${RELEASE_URL}/${asset}"; fi
  [[ $(basename "${archive}") == "${asset}" ]] || fail "archive mismatch: expected ${asset}"
  expected=$(awk -v n="${asset}" '$2==n {print $1}' "${MOOX_CADDY_CHECKSUMS:-${CHECKSUMS_DEFAULT}}")
  [[ -n "${expected}" ]] || fail "official checksum missing for ${asset}"
  actual=$(sha512 "${archive}"); [[ "${actual}" == "${expected}" ]] || fail "checksum mismatch for ${asset}"
  tar -C "${tmp}" -xzf "${archive}" caddy
  chmod 0755 "${tmp}/caddy"; [[ $("${tmp}/caddy" version | awk '{print $1}') == "${CADDY_VERSION}" ]] || fail 'downloaded binary version mismatch'
  mkdir -p "${DEPLOY_DIR}/bin"
  old="${BIN}.rollback"; [[ ! -e "${BIN}" ]] || cp -p "${BIN}" "${old}"
  mv "${tmp}/caddy" "${BIN}.new"; mv "${BIN}.new" "${BIN}"
  log "installed ${CADDY_VERSION} at ${BIN}"
}
start_caddy() {
  clean_stale_pid; validate; check_ports; mkdir -p "${DEPLOY_DIR}/run" "${XDG_DATA_HOME}" "${XDG_CONFIG_HOME}"
  if pid_owned; then
    admin_healthy || fail "managed PID does not own a healthy Caddy admin endpoint at ${ADMIN_ENDPOINT}"
    if ! "${BIN}" reload --config "${CONFIG}" --adapter caddyfile; then
      [[ ! -e "${CONFIG}.rollback" ]] || mv "${CONFIG}.rollback" "${CONFIG}"
      [[ ! -e "${BIN}.rollback" ]] || mv "${BIN}.rollback" "${BIN}"
      "${BIN}" reload --config "${CONFIG}" --adapter caddyfile >/dev/null 2>&1 || true
      fail 'reload failed; restored previous managed configuration and binary'
    fi
    log 'reloaded managed Caddy'; publish_ca; return
  fi
  nohup "${BIN}" run --config "${CONFIG}" --adapter caddyfile >>"${DEPLOY_DIR}/run/caddy.log" 2>&1 &
  local pid=$!; printf '%s\n' "${pid}" >"${PIDFILE}.new"; mv "${PIDFILE}.new" "${PIDFILE}"
  sleep 0.1; kill -0 "${pid}" 2>/dev/null || { rm -f "${PIDFILE}"; [[ ! -e "${CONFIG}.rollback" ]] || mv "${CONFIG}.rollback" "${CONFIG}"; [[ ! -e "${BIN}.rollback" ]] || mv "${BIN}.rollback" "${BIN}"; fail 'managed Caddy failed to start'; }
  log "started managed Caddy pid=${pid}"
  publish_ca
}

publish_ca() {
  [[ ${MOOX_CADDY_SKIP_CA_WAIT:-0} == 1 ]] && return 0
  local root="${XDG_DATA_HOME}/caddy/pki/authorities/local/root.crt" fingerprint old
  for _ in $(seq 1 100); do [[ -s "${root}" ]] && break; sleep .1; done
  [[ -s "${root}" ]] || fail 'Caddy root CA was not created'
  openssl x509 -in "${root}" -noout -text | grep -Eq 'CA:TRUE' || fail 'Caddy root certificate is not a CA'
  fingerprint=$(openssl x509 -in "${root}" -noout -fingerprint -sha256 | cut -d= -f2)
  mkdir -p "${DEPLOY_DIR}/certs/caddy"
  if [[ -s "${DEPLOY_DIR}/certs/caddy/root.sha256" ]]; then
    old=$(cat "${DEPLOY_DIR}/certs/caddy/root.sha256")
    [[ "${old}" == "${fingerprint}" ]] || fail "Caddy CA changed (${old} -> ${fingerprint}); explicit rotation is required"
  fi
  install -m 0644 "${root}" "${DEPLOY_DIR}/certs/caddy/root.crt"
  printf '%s\n' "${fingerprint}" >"${DEPLOY_DIR}/certs/caddy/root.sha256"
  log "Caddy CA SHA-256 fingerprint: ${fingerprint}"
}

case "${COMMAND}" in
  install) install_binary;;
  check) version_ok || fail "managed Caddy is missing or not ${CADDY_VERSION}"; clean_stale_pid; validate; check_ports; [[ ! -s "${PIDFILE}" ]] || admin_healthy || fail "managed admin endpoint is unhealthy or owned by another PID";;
  ensure)
    mkdir -p "${DEPLOY_DIR}/config/caddy" "${DEPLOY_DIR}/data/caddy" "${DEPLOY_DIR}/run"
    [[ -z "${CONFIG_SOURCE}" ]] || cp "${CONFIG_SOURCE}" "${CONFIG}.candidate"
    version_ok || install_binary
    [[ ! -e "${CONFIG}.candidate" ]] || { "${BIN}" validate --config "${CONFIG}.candidate" --adapter caddyfile >/dev/null; [[ ! -e "${CONFIG}" ]] || cp -p "${CONFIG}" "${CONFIG}.rollback"; mv "${CONFIG}.candidate" "${CONFIG}"; }
    [[ -s "${CONFIG}" ]] && start_caddy || log 'binary installed; waiting for managed Caddyfile'
    ;;
  start|reload) version_ok || fail "managed Caddy is missing or not ${CADDY_VERSION}"; start_caddy;;
  stop)
    clean_stale_pid
    if pid_owned; then pid=$(cat "${PIDFILE}"); kill "${pid}"; for _ in $(seq 1 50); do kill -0 "${pid}" 2>/dev/null || break; sleep .1; done; fi
    rm -f "${PIDFILE}";;
  rollback)
    clean_stale_pid
    if [[ -e "${CONFIG}.rollback" ]]; then
      mv "${CONFIG}.rollback" "${CONFIG}"
      [[ ! -e "${BIN}.rollback" ]] || mv "${BIN}.rollback" "${BIN}"
      if pid_owned; then
        "${BIN}" reload --config "${CONFIG}" --adapter caddyfile
      else
        start_caddy
      fi
      log 'restored previous managed Caddy configuration'
    else
      if pid_owned; then
        pid=$(cat "${PIDFILE}")
        kill "${pid}" 2>/dev/null || true
      fi
      rm -f "${PIDFILE}"
      log 'stopped first-install Caddy after failed acceptance'
    fi
    ;;
  status)
    installed=false; running=false; valid=false; admin=false; version=''; version_ok && { installed=true; version=${CADDY_VERSION}; }; pid_owned && running=true; admin_healthy && admin=true
    [[ "${installed}" == true && -s "${CONFIG}" ]] && "${BIN}" validate --config "${CONFIG}" --adapter caddyfile >/dev/null 2>&1 && valid=true
    printf '{"version":"%s","installed":%s,"running":%s,"admin_healthy":%s,"config_valid":%s,"deploy_dir":"%s"}\n' "${CADDY_VERSION}" "${installed}" "${running}" "${admin}" "${valid}" "${DEPLOY_DIR//\"/\\\"}";;
  *) fail "unknown command: ${COMMAND}";;
esac
