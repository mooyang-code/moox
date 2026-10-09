#!/usr/bin/env bash
# 查看组件状态：不带参数时列出这台主机上的全部组件；有组件未运行或未就绪时退出码为 1（已暂停的除外）。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

components=("$@")
if [[ ${#components[@]} -eq 0 ]]; then
  mapfile -t components < <(runtime_components)
fi
echo "主机 ${HOST_ID}，发布 ${RELEASE##*/}（${RELEASE_VERSION}）"
failed=0
for name in "${components[@]}"; do
  if ! status_component "${name}" && ! component_paused "${name}"; then
    failed=1
  fi
done
exit "${failed}"
