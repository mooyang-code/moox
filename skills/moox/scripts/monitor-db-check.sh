#!/usr/bin/env bash
# 排查“首页提示有问题需要处理”时，moox-cli doctor alerts 之外的只读深挖：经 SSH 在控制机上只读检查 Monitor 数据库
# （未恢复的告警状态及最近一次检查结果、失效但还没清理的自动检查、孤儿告警规则、静默的视图序列、清理日志）。
# 流程与常见原因见 skills/moox/references/monitor-attention.md。
#
# 只读：不改任何数据库、配置或进程；只用 SSH 公钥（BatchMode）；不读取 moox.toml，不处理任何凭据。
set -euo pipefail

CONFIG_FILE="${MOOX_RELEASE_ENV:-${HOME}/.moox-release.env}"
# shellcheck disable=SC1090
[[ -f "${CONFIG_FILE}" ]] && source "${CONFIG_FILE}"

SSH_OPTS=(-o BatchMode=yes -o PasswordAuthentication=no)
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat >&2 <<'EOF'
用法：monitor-db-check.sh [--host <别名>]

  --host  主机别名，默认 control（控制机）；对应 ~/.moox-release.env 里的 MOOX_RELEASE_HOST_CONTROL=user@host，与 native-release.sh 相同。

数据库与日志路径默认是控制机 /data/moox/prod/current/monitor/ 下的标准位置；非标准部署时在本机设置环境变量
MOOX_MONITOR_DB、MOOX_MONITOR_LOG（控制机上的路径），脚本会带到控制机执行。
EOF
  exit 2
}

die() { echo "错误：$*" >&2; exit 1; }

host_target() {
  local alias="$1" var value
  [[ "${alias}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || die "主机别名 ${alias} 不合法"
  var="MOOX_RELEASE_HOST_$(printf '%s' "${alias}" | tr 'a-z-' 'A-Z_')"
  value="${!var:-}"
  [[ -n "${value}" ]] || die "没有配置 ${var}（在 ${CONFIG_FILE} 里写 ${var}=user@host）"
  [[ "${value}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._:-]+$ ]] || die "${var} 必须是 user@host 形式"
  printf '%s\n' "${value}"
}

host_alias="control"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) host_alias="${2:-}"; [[ -n "${host_alias}" ]] || usage; shift 2 ;;
    *) usage ;;
  esac
done

target="$(host_target "${host_alias}")"
remote_env=""
for name in MOOX_MONITOR_DB MOOX_MONITOR_LOG; do
  value="${!name:-}"
  [[ -z "${value}" ]] && continue
  [[ "${value}" =~ ^/[A-Za-z0-9_./-]+$ ]] || die "${name} 必须是控制机上的绝对路径（只含字母、数字、下划线、点、斜杠、短横线）"
  remote_env="${remote_env}${name}='${value}' "
done
ssh "${SSH_OPTS[@]}" "${target}" "${remote_env}python3 -" <"${HERE}/monitor_db_check.py"
