#!/usr/bin/env bash
# 对 go.work 里的每个模块运行 go test 与 go vet。
# 环境变量：
#   MOOX_GO_SHARD=<第几片>/<共几片>   只运行其中一片（按模块在 go.work 里的顺序轮转分配），例如 2/3；CI 用它并行。不设就全部运行。
#   MOOX_GO_SHARD_LIST=1             只打印本片包含的模块，不运行测试。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

shard_index=0
shard_total=1
if [[ -n "${MOOX_GO_SHARD:-}" ]]; then
  if [[ ! "${MOOX_GO_SHARD}" =~ ^([1-9][0-9]*)/([1-9][0-9]*)$ ]] || (( BASH_REMATCH[1] > BASH_REMATCH[2] )); then
    echo "MOOX_GO_SHARD 应为 <第几片>/<共几片>，例如 2/3；收到：${MOOX_GO_SHARD}" >&2
    exit 2
  fi
  shard_index=$(( BASH_REMATCH[1] - 1 ))
  shard_total=${BASH_REMATCH[2]}
fi
position=0

mapfile_compat() {
  local line
  while IFS= read -r line; do
    [[ -n "${line}" ]] && printf '%s\0' "${line}"
  done
}

while IFS= read -r -d '' module; do
  module="${module#./}"
  selected=$(( position % shard_total == shard_index ))
  position=$(( position + 1 ))
  if (( ! selected )); then continue; fi
  if [[ -n "${MOOX_GO_SHARD_LIST:-}" ]]; then echo "${module}"; continue; fi
  echo "==> go test ${module}"
  test_flags=(-count=1)
  case "${module}" in
    modules/admin|modules/hostagent|modules/trade)
      # goom relies on disabled inlining for its method interception tests.
      test_flags+=(-gcflags=all=-l -ldflags=-s=false)
      ;;
    modules/cli)
      # unitruntime、unitbootstrap 等包的测试会启动真实的 console-proxy，健康检查固定监听 127.0.0.1:19528；
      # 包之间并行会抢这个端口，偶发 "release process did not become ready"，所以这个模块的包依次运行。
      test_flags+=(-p 1)
      ;;
  esac
  (cd "${ROOT}/${module}" && go test "${test_flags[@]}" ./...)
  echo "==> go vet ${module}"
  (cd "${ROOT}/${module}" && go vet ./...)
done < <(
  awk '
    /^use \($/ { in_use=1; next }
    in_use && /^\)$/ { exit }
    in_use { gsub(/^[[:space:]]+|[[:space:]]+$/, ""); if ($0 != "") print }
  ' "${ROOT}/go.work" | mapfile_compat
)
