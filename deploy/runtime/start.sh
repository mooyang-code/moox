#!/usr/bin/env bash
# 启动组件并等待就绪：不带参数时按启动顺序启动这台主机上的全部组件。暂停的组件不启动，--force 时忽略暂停标记。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

force=0
if [[ "${1:-}" == --force ]]; then
  force=1
  shift
fi
components=("$@")
if [[ ${#components[@]} -eq 0 ]]; then
  mapfile -t components < <(runtime_components)
fi
failed=0
for name in "${components[@]}"; do
  start_component "${name}" "${force}" || failed=1
done
exit "${failed}"
