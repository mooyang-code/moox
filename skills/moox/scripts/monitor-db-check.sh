#!/usr/bin/env bash
# 排查“首页提示有问题需要处理”时，moox-cli doctor alerts 之外的只读深挖：经 SSH 在控制机上只读检查 Monitor 数据库
# （未恢复的告警状态及最近一次检查结果、失效但还没清理的自动检查、孤儿告警规则、静默的视图序列、清理日志）。
# 流程与常见原因见 skills/moox/references/monitor-attention.md。
#
# 只读：不改任何数据库、配置或进程；只用 SSH 公钥（BatchMode）；不读取 moox.toml，不处理任何凭据。
set -euo pipefail

SSH_OPTS=(-o BatchMode=yes -o PasswordAuthentication=no)
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat >&2 <<'EOF'
用法：monitor-db-check.sh --host <user@host>

  --host  控制机的 SSH 目标（user@host），取 moox.toml 里 [hosts.<控制机>] 的 SSH 用户与地址。

数据库与日志路径默认是控制机部署根 /data/moox 下的标准位置（data/monitor/monitor.db、logs/monitor/trpc.log）；非标准部署时在本机设置环境变量
MOOX_MONITOR_DB、MOOX_MONITOR_LOG（控制机上的路径），脚本会带到控制机执行。
EOF
  exit 2
}

die() { echo "错误：$*" >&2; exit 1; }

target=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) target="${2:-}"; [[ -n "${target}" ]] || usage; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "${target}" ]] || usage
[[ "${target}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._:-]+$ ]] || die "--host 必须是 user@host 形式：${target}"
remote_env=""
for name in MOOX_MONITOR_DB MOOX_MONITOR_LOG; do
  value="${!name:-}"
  [[ -z "${value}" ]] && continue
  [[ "${value}" =~ ^/[A-Za-z0-9_./-]+$ ]] || die "${name} 必须是控制机上的绝对路径（只含字母、数字、下划线、点、斜杠、短横线）"
  remote_env="${remote_env}${name}='${value}' "
done
ssh "${SSH_OPTS[@]}" "${target}" "${remote_env}python3 -" <"${HERE}/monitor_db_check.py"
