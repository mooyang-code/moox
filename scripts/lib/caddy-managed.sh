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
PORTS_SET=0
ADMIN_ENDPOINT=${MOOX_CADDY_ADMIN_ENDPOINT:-127.0.0.1:2019}
ADMIN_PATH=${MOOX_CADDY_ADMIN_PATH:-/config/}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --deploy-dir) DEPLOY_DIR=${2:?}; shift 2;;
    --os) OS=${2:?}; shift 2;;
    --arch) ARCH=${2:?}; shift 2;;
    --config) CONFIG_SOURCE=${2:?}; shift 2;;
    --ports) [[ $# -ge 2 ]] || fail '--ports requires a value'; PORTS=${2-}; PORTS_SET=1; shift 2;;
    *) fail "unknown option: $1";;
  esac
done
OS=$(normalize_os "${OS}"); ARCH=$(normalize_arch "${ARCH}")
BIN="${DEPLOY_DIR}/bin/caddy"; CONFIG="${DEPLOY_DIR}/config/caddy/Caddyfile"; PIDFILE="${DEPLOY_DIR}/run/caddy.pid"
ENV_FILE="${DEPLOY_DIR}/config/caddy/edge.env"
ACTIVATION_ROLLBACK="${DEPLOY_DIR}/config/caddy/.activation.rollback"
BIN_ROLLBACK_CHANGED="${BIN}.rollback.changed"
if [[ -s "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
fi
if [[ "${PORTS_SET}" -eq 0 && -n "${MOOX_CADDY_PORTS:-}" ]]; then
  PORTS="${MOOX_CADDY_PORTS}"
fi
export XDG_DATA_HOME="${DEPLOY_DIR}/data/caddy" XDG_CONFIG_HOME="${DEPLOY_DIR}/config/caddy/runtime"

version_ok() { [[ -x "${BIN}" ]] && [[ $("${BIN}" version 2>/dev/null | awk '{print $1}') == "${CADDY_VERSION}" ]]; }
validate_ports() {
  [[ "${PORTS}" =~ ^[1-9][0-9]{0,4}(,[1-9][0-9]{0,4})*$ ]] || \
    fail "invalid Caddy ports: ${PORTS:-<empty>}; expected comma-separated canonical integers in 1..65535"
  local port
  local -a ports
  IFS=, read -ra ports <<<"${PORTS}"
  for port in "${ports[@]}"; do
    (( port <= 65535 )) || fail "invalid Caddy ports: ${PORTS}; every port must be in 1..65535"
  done
}
requires_privileged_port() {
  local port
  local -a ports
  IFS=, read -ra ports <<<"${PORTS}"
  for port in "${ports[@]}"; do
    (( port < 1024 )) && return 0
  done
  return 1
}
file_capabilities() {
  local output capabilities
  output=$(getcap "${BIN}" 2>/dev/null) || fail "could not read file capabilities from ${BIN} with getcap"
  capabilities=${output#* }
  printf '%s' "${capabilities}"
}
reconcile_bind_capability() {
  [[ "${OS}" == linux ]] || return 0
  command -v getcap >/dev/null 2>&1 || fail 'Linux managed Caddy requires getcap to enforce cap_net_bind_service least privilege'
  local capabilities
  capabilities=$(file_capabilities)
  if ! requires_privileged_port; then
    [[ -z "${capabilities}" ]] && return 0
    command -v sudo >/dev/null 2>&1 || fail 'removing stale Caddy capabilities requires passwordless sudo for setcap -r'
    sudo -n setcap -r -- "${BIN}" || \
      fail 'could not remove stale Caddy capabilities with sudo -n setcap -r'
    [[ -z "$(file_capabilities)" ]] || fail 'Caddy capability removal validation failed after sudo -n setcap -r'
    log "removed file capabilities from ${BIN} for unprivileged-only ports"
    return 0
  fi
  [[ "${capabilities}" == cap_net_bind_service=ep ]] && return 0
  command -v sudo >/dev/null 2>&1 || fail 'privileged Caddy ports require passwordless sudo for setcap cap_net_bind_service=+ep'
  sudo -n setcap cap_net_bind_service=+ep "${BIN}" || \
    fail 'could not grant cap_net_bind_service=+ep with sudo -n setcap; configure passwordless setcap or use a port >=1024'
  [[ "$(file_capabilities)" == cap_net_bind_service=ep ]] || fail 'cap_net_bind_service validation failed after sudo -n setcap'
  log "granted cap_net_bind_service=+ep to ${BIN}"
}
snapshot_file() {
  local active="$1" backup="${1}.rollback"
  mkdir -p "$(dirname "${backup}")"
  rm -f "${backup}" "${backup}.absent"
  if [[ -e "${active}" ]]; then cp -p "${active}" "${backup}"; else : >"${backup}.absent"; fi
}
restore_file() {
  local active="$1" backup="${1}.rollback"
  if [[ -e "${backup}.absent" ]]; then
    rm -f "${active}"
  elif [[ -e "${backup}" ]]; then
    mv "${backup}" "${active}"
  fi
  rm -f "${backup}.absent"
}
begin_activation() {
  snapshot_file "${CONFIG}"
  snapshot_file "${ENV_FILE}"
  snapshot_file "${BIN}"
  rm -f "${BIN_ROLLBACK_CHANGED}"
  : >"${ACTIVATION_ROLLBACK}"
}
load_active_ports() {
  unset MOOX_CADDY_PORTS
  if [[ -s "${ENV_FILE}" ]]; then
    # shellcheck disable=SC1090
    source "${ENV_FILE}"
  fi
  PORTS="${MOOX_CADDY_PORTS:-9527,11001}"
  validate_ports
}
restore_activation() {
  [[ -e "${ACTIVATION_ROLLBACK}" ]] || return 1
  restore_file "${CONFIG}"
  restore_file "${ENV_FILE}"
  if [[ -e "${BIN_ROLLBACK_CHANGED}" ]]; then
    restore_file "${BIN}"
  else
    rm -f "${BIN}.rollback" "${BIN}.rollback.absent"
  fi
  rm -f "${BIN_ROLLBACK_CHANGED}"
  rm -f "${CONFIG}.candidate" "${ENV_FILE}.candidate" "${ACTIVATION_ROLLBACK}"
  load_active_ports
  [[ ! -x "${BIN}" ]] || reconcile_bind_capability
}
write_candidate_env() {
  umask 077
  {
    printf 'MOOX_CADDY_PORTS=%q\n' "${PORTS}"
    if [[ -n "${MOOX_PUBLIC_HOST:-}" ]]; then
      printf 'MOOX_PUBLIC_HOST=%q\nMOOX_BROWSER_HTTPS_PORT=%q\nMOOX_SERVICE_HTTPS_PORT=%q\n' \
        "${MOOX_PUBLIC_HOST}" "${MOOX_BROWSER_HTTPS_PORT:-9527}" "${MOOX_SERVICE_HTTPS_PORT:-11001}"
    fi
  } >"${ENV_FILE}.candidate"
}
managed_process() {
  local pid="$1" exe command
  [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null || return 1
  [[ ${MOOX_CADDY_SKIP_PID_EXE_CHECK:-0} == 1 ]] && return 0
  if [[ -L "/proc/${pid}/exe" ]]; then
    exe=$(readlink "/proc/${pid}/exe")
    [[ "${exe}" == "${BIN}" || "${exe}" == "${BIN} (deleted)" ]] || return 1
  fi
  if [[ -r "/proc/${pid}/cmdline" ]]; then
    command=$(tr '\0' ' ' <"/proc/${pid}/cmdline"); command=${command% }
  else
    command=$(ps -p "${pid}" -o command= 2>/dev/null || true)
  fi
  [[ "${command}" == "${BIN} run --config ${CONFIG} --adapter caddyfile" ]]
}
pid_owned() {
  [[ -s "${PIDFILE}" ]] || return 1
  local pid
  pid=$(cat "${PIDFILE}")
  managed_process "${pid}"
}
clean_stale_pid() { [[ ! -e "${PIDFILE}" ]] || pid_owned || { log 'removing stale or mismatched PID file without signaling it'; rm -f "${PIDFILE}"; }; }
stop_recorded_pid() {
  local pid
  pid=$(cat "${PIDFILE}" 2>/dev/null || true)
  if [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}" 2>/dev/null || true
    for _ in $(seq 1 50); do kill -0 "${pid}" 2>/dev/null || break; sleep .1; done
  fi
  rm -f "${PIDFILE}"
}
restore_runtime_after_failure() {
  local previous_pid="$1"
  if [[ "${previous_pid}" =~ ^[0-9]+$ ]] && kill -0 "${previous_pid}" 2>/dev/null && [[ -x "${BIN}" && -s "${CONFIG}" ]]; then
    printf '%s\n' "${previous_pid}" >"${PIDFILE}"
    "${BIN}" reload --config "${CONFIG}" --adapter caddyfile >/dev/null 2>&1 || true
  else
    stop_recorded_pid
  fi
}
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

adopt_managed_process() {
  local port pid admin_port owners unique count
  local -a candidates=()
  IFS=, read -ra ports <<<"${PORTS}"
  for port in "${ports[@]}"; do
    while IFS= read -r pid; do [[ -z "${pid}" ]] || candidates+=("${pid}"); done < <(port_pids "${port}")
  done
  ((${#candidates[@]} > 0)) || return 1
  unique=$(printf '%s\n' "${candidates[@]}" | sort -u)
  count=$(printf '%s\n' "${unique}" | awk 'NF { count++ } END { print count + 0 }')
  ((count == 1)) || return 1
  pid=${unique}
  managed_process "${pid}" || return 1
  admin_port=${ADMIN_ENDPOINT##*:}
  owners=$(port_pids "${admin_port}")
  [[ " ${owners//$'\n'/ } " == *" ${pid} "* ]] || return 1
  curl --fail --silent --max-time 2 "http://${ADMIN_ENDPOINT}${ADMIN_PATH}" >/dev/null || return 1
  mkdir -p "${DEPLOY_DIR}/run"
  printf '%s\n' "${pid}" >"${PIDFILE}.new"
  mv "${PIDFILE}.new" "${PIDFILE}"
  log "adopted managed Caddy pid=${pid} after binary replacement"
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
  local asset archive expected actual tmp
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
  mv "${tmp}/caddy" "${BIN}.new"; mv "${BIN}.new" "${BIN}"
  [[ ! -e "${ACTIVATION_ROLLBACK}" ]] || : >"${BIN_ROLLBACK_CHANGED}"
  log "installed ${CADDY_VERSION} at ${BIN}"
}
start_caddy() {
  clean_stale_pid; reconcile_bind_capability; validate; check_ports; mkdir -p "${DEPLOY_DIR}/run" "${XDG_DATA_HOME}" "${XDG_CONFIG_HOME}"
  if pid_owned; then
    admin_healthy || fail "managed PID does not own a healthy Caddy admin endpoint at ${ADMIN_ENDPOINT}"
    if ! "${BIN}" reload --config "${CONFIG}" --adapter caddyfile; then
      fail 'managed Caddy reload failed'
    fi
    log 'reloaded managed Caddy'; publish_ca; return
  fi
  nohup "${BIN}" run --config "${CONFIG}" --adapter caddyfile >>"${DEPLOY_DIR}/run/caddy.log" 2>&1 &
  local pid=$!; printf '%s\n' "${pid}" >"${PIDFILE}.new"; mv "${PIDFILE}.new" "${PIDFILE}"
  sleep 0.1; kill -0 "${pid}" 2>/dev/null || { rm -f "${PIDFILE}"; fail 'managed Caddy failed to start'; }
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

validate_ports

case "${COMMAND}" in
  install) snapshot_file "${BIN}"; install_binary; reconcile_bind_capability;;
  check) version_ok || fail "managed Caddy is missing or not ${CADDY_VERSION}"; clean_stale_pid; reconcile_bind_capability; validate; check_ports; [[ ! -s "${PIDFILE}" ]] || admin_healthy || fail "managed admin endpoint is unhealthy or owned by another PID";;
  ensure)
    mkdir -p "${DEPLOY_DIR}/config/caddy" "${DEPLOY_DIR}/data/caddy" "${DEPLOY_DIR}/run"
    [[ -z "${CONFIG_SOURCE}" ]] || cp "${CONFIG_SOURCE}" "${CONFIG}.candidate"
    clean_stale_pid
    pid_owned || adopt_managed_process || true
    previous_pid=""
    if pid_owned; then previous_pid=$(cat "${PIDFILE}"); fi
    begin_activation
    version_ok || install_binary
    if [[ -e "${CONFIG}.candidate" ]] && ! "${BIN}" validate --config "${CONFIG}.candidate" --adapter caddyfile >/dev/null; then
      restore_activation || true
      fail 'candidate Caddy configuration validation failed; restored previous activation'
    fi
    if ! write_candidate_env; then
      restore_activation || true
      fail 'could not stage Caddy edge environment; restored previous activation'
    fi
    if ! (reconcile_bind_capability); then
      restore_activation || true
      fail 'Caddy capability activation failed; restored previous activation'
    fi
    if ! { mv "${ENV_FILE}.candidate" "${ENV_FILE}" && { [[ ! -e "${CONFIG}.candidate" ]] || mv "${CONFIG}.candidate" "${CONFIG}"; }; }; then
      restore_activation || true
      fail 'could not activate Caddy config and environment; restored previous activation'
    fi
    if [[ -s "${CONFIG}" ]]; then
      if [[ -n "${previous_pid}" ]] && ! pid_owned; then stop_recorded_pid; fi
      if ! (start_caddy); then
        restore_activation || fail 'Caddy activation failed and rollback could not restore the previous state'
        restore_runtime_after_failure "${previous_pid}"
        fail 'Caddy activation failed; restored previous config, binary, ports, environment, and capability'
      fi
    else
      log 'binary installed; waiting for managed Caddyfile'
    fi
    ;;
  start|reload) version_ok || fail "managed Caddy is missing or not ${CADDY_VERSION}"; start_caddy;;
  stop)
    clean_stale_pid
    if pid_owned; then pid=$(cat "${PIDFILE}"); kill "${pid}"; for _ in $(seq 1 50); do kill -0 "${pid}" 2>/dev/null || break; sleep .1; done; fi
    rm -f "${PIDFILE}";;
  rollback)
    clean_stale_pid
    if [[ -e "${ACTIVATION_ROLLBACK}" ]]; then
      rollback_pid=""
      if pid_owned; then rollback_pid=$(cat "${PIDFILE}"); fi
      restore_activation || fail 'managed Caddy rollback state is incomplete'
      if [[ -s "${CONFIG}" ]]; then
        if [[ "${rollback_pid}" =~ ^[0-9]+$ ]] && kill -0 "${rollback_pid}" 2>/dev/null; then
          printf '%s\n' "${rollback_pid}" >"${PIDFILE}"
          "${BIN}" reload --config "${CONFIG}" --adapter caddyfile
        else
          rm -f "${PIDFILE}"
          start_caddy
        fi
      else
        stop_recorded_pid
      fi
      log 'restored previous managed Caddy activation'
    else
      if pid_owned; then
        pid=$(cat "${PIDFILE}")
        kill "${pid}" 2>/dev/null || true
        for _ in $(seq 1 50); do kill -0 "${pid}" 2>/dev/null || break; sleep .1; done
      fi
      rm -f "${PIDFILE}"
      rm -f "${CONFIG}.candidate" "${ENV_FILE}.candidate"
      log 'no managed Caddy activation rollback was available'
    fi
    ;;
  status)
    installed=false; running=false; valid=false; admin=false; version=''; version_ok && { installed=true; version=${CADDY_VERSION}; }; pid_owned && running=true; admin_healthy && admin=true
    [[ "${installed}" == true && -s "${CONFIG}" ]] && "${BIN}" validate --config "${CONFIG}" --adapter caddyfile >/dev/null 2>&1 && valid=true
    printf '{"version":"%s","installed":%s,"running":%s,"admin_healthy":%s,"config_valid":%s,"deploy_dir":"%s"}\n' "${CADDY_VERSION}" "${installed}" "${running}" "${admin}" "${valid}" "${DEPLOY_DIR//\"/\\\"}";;
  *) fail "unknown command: ${COMMAND}";;
esac
