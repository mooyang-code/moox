#!/usr/bin/env bash
# 按显式目录清单对单根 go.mod 下的全部 Go 模块目录运行 go test 与 go vet（含 modules/storage 的 CGO 测试）。
# 环境变量：
#   MOOX_GO_SHARD=<第几片>/<共几片>   只运行其中一片（按下方清单顺序轮转分配），例如 2/3；CI 用它并行。不设就全部运行。
#   MOOX_GO_SHARD_LIST=1             只打印本片包含的模块目录，不运行测试。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

# 显式目录清单覆盖根目录 go.mod 下的全部 Go 包（含各模块的 proto/*gen）；
# 仅对使用 goom 桩拦截的 admin/hostagent/trade 附加 -gcflags=all=-l -ldflags=-s=false。
MODULES=(
  modules/admin
  modules/archive
  modules/cli
  modules/collector
  modules/egressproxy
  modules/eventbus
  modules/factor
  modules/hostagent
  modules/hostgateway
  modules/monitor
  modules/storage
  modules/strategy
  modules/trade
  packages
  tools/zstddict
)

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

for module in "${MODULES[@]}"; do
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
  esac
  (cd "${ROOT}/${module}" && go test "${test_flags[@]}" ./...)
  echo "==> go vet ${module}"
  (cd "${ROOT}/${module}" && go vet ./...)
done
