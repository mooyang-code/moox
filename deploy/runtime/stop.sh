#!/usr/bin/env bash
# 停止组件：不带参数时按启动顺序的倒序停止这台主机上的全部组件。停止不会写暂停标记，健康检查仍会拉起。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

components=("$@")
if [[ ${#components[@]} -eq 0 ]]; then
  mapfile -t components < <(runtime_components | sed '1!G;h;$!d')
fi
failed=0
for name in "${components[@]}"; do
  stop_component "${name}" || failed=1
done
exit "${failed}"
