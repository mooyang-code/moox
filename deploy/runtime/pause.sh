#!/usr/bin/env bash
# 暂停组件：写入暂停标记 run/paused/<组件> 并停止进程。标记放在发布目录之外，重新部署后仍然有效，
# 启动脚本、健康检查和安装器都不会拉起暂停的组件，直到执行 resume.sh。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

[[ $# -gt 0 ]] || { echo "用法：pause.sh <组件>..." >&2; exit 2; }
for name in "$@"; do
  runtime_has_component "${name}" || { echo "${name}: 这台主机上没有部署这个组件" >&2; exit 2; }
done

pause_all() {
  local name
  mkdir -p "${ROOT}/run/paused"
  for name in "$@"; do
    printf '%s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" >"${ROOT}/run/paused/${name}"
    stop_component "${name}"
    echo "${name}: 已暂停"
  done
}

with_maintenance_lock 120 pause_all "$@"
