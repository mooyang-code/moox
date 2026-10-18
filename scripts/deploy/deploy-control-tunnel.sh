#!/usr/bin/env bash
# 在 control 访问不到的主机（例如家庭内网的 192）上安装到 control 的 SSH 反向隧道（systemd 用户服务 moox-control-tunnel）：
# 本机主动连到 control，把本机主机网关健康端口、跨主机 TLS 端口与 HostAgent 健康端口映射到 control 上的专用回环地址。
# 设计见 docs/计划/主机守护与告警高可用执行计划.md 第 4.6 节。
#
# control 侧的两步（需要 sudo，由运维确认后手工执行）不在本脚本内：
#   1. sshd 设置 GatewayPorts clientspecified 并重载 sshd；
#   2. 把本脚本输出的受限 authorized_keys 行加到 control 部署用户的 ~/.ssh/authorized_keys。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
DEPLOY_DIR="${HOME}/moox/control-tunnel"
CONTROL=""
SSH_PORT=22
FINGERPRINT=""
KNOWN_HOSTS_SOURCE=""
BIND="127.0.1.103"
PORTS="11012,11003,11425"
NO_START=0
SYSTEMD_USER_DIR="${MOOX_SYSTEMD_USER_DIR:-${HOME}/.config/systemd/user}"

usage() {
  cat <<'EOF'
Usage:
  scripts/deploy/deploy-control-tunnel.sh --control USER@HOST (--control-fingerprint SHA256:... | --known-hosts FILE) [options]

Options:
  --control <user@host>         control 的部署用户与地址，例如 ubuntu@106.53.107.122。
  --control-fingerprint <fp>    control 的 SSH 主机密钥指纹（ssh-keygen -lf 输出的 SHA256:...），用于核对扫描到的主机密钥。
  --known-hosts <file>          直接使用已核对过的 known_hosts 条目，不联网扫描。
  --ssh-port <port>             control 的 SSH 端口。默认 22。
  --bind <ip>                   control 上的专用回环地址（127.0.0.0/8，不能是 127.0.0.1）。默认 127.0.1.103。
  --ports <list>                要映射的本机端口，逗号分隔。默认 11012,11003,11425。
  --dir <path>                  隧道密钥与 known_hosts 的目录。默认 ~/moox/control-tunnel。
  --no-start                    只写文件，不启动服务。

输出：需要加到 control 上 ~/.ssh/authorized_keys 的受限行（只允许这几个反向转发，不能登录执行命令）。
EOF
}

fail() {
  printf '[deploy-control-tunnel] ERROR: %s\n' "$*" >&2
  exit 1
}

log() {
  printf '[deploy-control-tunnel] %s\n' "$*" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --control) CONTROL="$2"; shift 2 ;;
    --control-fingerprint) FINGERPRINT="$2"; shift 2 ;;
    --known-hosts) KNOWN_HOSTS_SOURCE="$2"; shift 2 ;;
    --ssh-port) SSH_PORT="$2"; shift 2 ;;
    --bind) BIND="$2"; shift 2 ;;
    --ports) PORTS="$2"; shift 2 ;;
    --dir) DEPLOY_DIR="$2"; shift 2 ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown option: $1" ;;
  esac
done

[[ "${CONTROL}" =~ ^[a-z_][a-z0-9_-]*@[A-Za-z0-9.-]+$ ]] || fail "--control must be user@host"
CONTROL_HOST="${CONTROL#*@}"
[[ "${SSH_PORT}" =~ ^[0-9]+$ && "${SSH_PORT}" -ge 1 && "${SSH_PORT}" -le 65535 ]] || fail "--ssh-port must be a TCP port"
[[ "${BIND}" =~ ^127\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ && "${BIND}" != 127.0.0.1 ]] || fail "--bind must be a dedicated loopback address in 127.0.0.0/8 other than 127.0.0.1"
[[ -n "${FINGERPRINT}" || -n "${KNOWN_HOSTS_SOURCE}" ]] || fail "pass --control-fingerprint or --known-hosts so the control host key is verified"
[[ -z "${FINGERPRINT}" || "${FINGERPRINT}" =~ ^SHA256:[A-Za-z0-9+/]{43}$ ]] || fail "--control-fingerprint must look like SHA256:<43 base64 chars>"
IFS=',' read -r -a port_list <<<"${PORTS}"
[[ ${#port_list[@]} -ge 1 ]] || fail "--ports must not be empty"
for port in "${port_list[@]}"; do
  [[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || fail "invalid port ${port}"
done

mkdir -p "${DEPLOY_DIR}"
chmod 0700 "${DEPLOY_DIR}"
DEPLOY_DIR="$(cd "${DEPLOY_DIR}" && pwd -P)"
KEY="${DEPLOY_DIR}/id_ed25519"
KNOWN_HOSTS="${DEPLOY_DIR}/known_hosts"

# 专用密钥：只用于隧道，不设口令（systemd 无人值守启动）；control 侧用 authorized_keys 选项把它限制为这几个转发。
if [[ ! -f "${KEY}" ]]; then
  log "generate dedicated tunnel key ${KEY}"
  ssh-keygen -q -t ed25519 -N '' -C "moox-control-tunnel@$(hostname -s)" -f "${KEY}"
fi
chmod 0600 "${KEY}"

# control 的主机密钥：用已核对的 known_hosts，或扫描后与指纹核对，避免首次连接盲目信任。
if [[ -n "${KNOWN_HOSTS_SOURCE}" ]]; then
  [[ -s "${KNOWN_HOSTS_SOURCE}" ]] || fail "missing ${KNOWN_HOSTS_SOURCE}"
  install -m 0600 "${KNOWN_HOSTS_SOURCE}" "${KNOWN_HOSTS}"
else
  scanned="$(mktemp "${DEPLOY_DIR}/known_hosts.XXXXXX")"
  trap 'rm -f "${scanned}"' EXIT
  ssh-keyscan -p "${SSH_PORT}" -t ed25519 "${CONTROL_HOST}" >"${scanned}" 2>/dev/null || true
  [[ -s "${scanned}" ]] || fail "cannot scan the ed25519 host key of ${CONTROL_HOST}:${SSH_PORT}"
  actual="$(ssh-keygen -lf "${scanned}" | awk '{print $2}')"
  [[ "${actual}" == "${FINGERPRINT}" ]] || fail "control host key fingerprint ${actual} does not match ${FINGERPRINT}"
  install -m 0600 "${scanned}" "${KNOWN_HOSTS}"
fi

forwards=""
permits=""
for port in "${port_list[@]}"; do
  forwards+="-R ${BIND}:${port}:127.0.0.1:${port} "
  permits+="permitlisten=\"${BIND}:${port}\","
done
forwards="${forwards% }"

mkdir -p "${SYSTEMD_USER_DIR}"
sed -e "s#__KEY__#${KEY}#g" -e "s#__KNOWN_HOSTS__#${KNOWN_HOSTS}#g" -e "s#__FORWARDS__#${forwards}#g" \
  -e "s#__SSH_PORT__#${SSH_PORT}#g" -e "s#__CONTROL__#${CONTROL}#g" \
  "${ROOT}/deploy/systemd/user/moox-control-tunnel.service.tmpl" >"${SYSTEMD_USER_DIR}/moox-control-tunnel.service"

if [[ "${NO_START}" -eq 0 ]]; then
  if command -v loginctl >/dev/null 2>&1 && [[ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)" != yes ]]; then
    log "warning: user lingering is off; the tunnel stops at logout (enable with: loginctl enable-linger $(id -un))"
  fi
  systemctl --user daemon-reload
  systemctl --user enable moox-control-tunnel.service
  systemctl --user restart moox-control-tunnel.service
  log "started moox-control-tunnel; check with: systemctl --user status moox-control-tunnel"
fi

log "add this line to ~/.ssh/authorized_keys of ${CONTROL} (after GatewayPorts clientspecified is enabled on control):"
printf 'restrict,port-forwarding,%scommand="/bin/false" %s\n' "${permits}" "$(cat "${KEY}.pub")"
