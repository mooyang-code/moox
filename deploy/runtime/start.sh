#!/usr/bin/env bash
# 启动组件并等待就绪：不带参数时按启动顺序启动这台主机上的全部组件。暂停的组件不启动，--force 时忽略暂停标记。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

force=0 first_install=0
while [[ "${1:-}" == --force || "${1:-}" == --first-install ]]; do
  case "$1" in
    --force) force=1 ;;
    --first-install) first_install=1 ;;
  esac
  shift
done
components=("$@")
if [[ ${#components[@]} -eq 0 ]]; then
  mapfile -t components < <(runtime_components)
fi
failed=0
for name in "${components[@]}"; do
  if ! start_component "${name}" "${force}"; then
    # 空环境首次安装时，依赖元数据的组件要等 setup init 之后才起得来：不让整次安装失败，由健康检查每分钟重试。
    if [[ "${first_install}" == 1 && "${COMPONENT_NEEDS_METADATA:-0}" == 1 ]]; then
      echo "${name}: 依赖 setup init 导入的元数据，现在起不来是预期的；init 完成后由健康检查拉起" >&2
    else
      failed=1
    fi
  fi
done
exit "${failed}"
