#!/usr/bin/env bash
# 重启组件：先停止再启动；暂停的组件停止后不再启动，--force 时忽略暂停标记。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

force=0
if [[ "${1:-}" == --force ]]; then
  force=1
  shift
fi
[[ $# -gt 0 ]] || { echo "用法：restart.sh [--force] <组件>..." >&2; exit 2; }
failed=0
for name in "$@"; do
  stop_component "${name}" || failed=1
  start_component "${name}" "${force}" || failed=1
done
exit "${failed}"
