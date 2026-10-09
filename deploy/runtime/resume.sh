#!/usr/bin/env bash
# 恢复组件：删除暂停标记并启动。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

[[ $# -gt 0 ]] || { echo "用法：resume.sh <组件>..." >&2; exit 2; }
for name in "$@"; do
  runtime_has_component "${name}" || { echo "${name}: 这台主机上没有部署这个组件" >&2; exit 2; }
done

resume_all() {
  local name failed=0
  for name in "$@"; do
    rm -f "${ROOT}/run/paused/${name}"
    start_component "${name}" || failed=1
  done
  return "${failed}"
}

with_maintenance_lock 120 resume_all "$@"
