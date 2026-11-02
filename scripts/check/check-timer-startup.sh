#!/usr/bin/env bash
set -euo pipefail

# 定时器配了 startAtOnce 时，首次执行返回错误会让服务退出。
# 用到 startAtOnce 的模块，定时任务都要经 timerjob.KeepRunning 注册。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

failed=0
while IFS= read -r config; do
  if [[ "${config}" != */config/* ]]; then
    echo "${config} 不在模块的 config 目录下，检查不到对应代码" >&2
    failed=1
    continue
  fi
  module="${config%/config/*}"
  while IFS= read -r line; do
    if [[ "${line}" != *timerjob.KeepRunning* ]]; then
      echo "${line}" >&2
      failed=1
    fi
  done < <(grep -rn --include='*.go' --exclude='*_test.go' 'timer.RegisterHandlerService(' "${module}" || true)
done < <(grep -rl --include='*.yaml' 'startAtOnce=1' modules)

if [[ "${failed}" != 0 ]]; then
  echo '上面的定时任务要经 timerjob.KeepRunning 注册' >&2
  exit 1
fi

echo '==> timer startup check passed'
