#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

# 断言文件里不含某个模式：命中或文件读不到都算失败。
absent() {
  local pattern="$1" status=0
  shift
  rg -q -- "${pattern}" "$@" || status=$?
  if [[ "${status}" -eq 0 ]]; then
    echo "不应出现 ${pattern}：$*" >&2
    exit 1
  fi
  if [[ "${status}" -ne 1 ]]; then
    echo "检查 ${pattern} 时读取失败：$*" >&2
    exit 1
  fi
}

absent 'TradeDNSResolverService' "${ROOT}/modules/trade/proto/trade_service.proto" "${ROOT}/modules/trade/config/trpc_go.yaml"
absent 'ResolveDomains' "${ROOT}/modules/egressproxy/proto/egress.proto"
rg -q 'service Proxy' "${ROOT}/modules/egressproxy/proto/egress.proto"
rg -q 'func RenderEgressConfig' "${ROOT}/modules/cli/internal/setup/topology/render.go"
rg -q 'func RenderCollectorRuntimeConfig' "${ROOT}/modules/cli/internal/setup/topology/render.go"

# 渲染逻辑在 CLI 的配置包里：用随仓库发布的出口代理、Collector 模板和示例配置渲染一次，
# 断言白名单两边一致、不渲染 DNS 快照与旧路由字段、输出里没有任何密钥字段（渲染函数本来就拿不到秘密文件）。
(cd "${ROOT}/modules/cli" && go test -count=1 -run 'TestRenderedRuntimeConfigFromShippedTemplates' ./internal/setup/topology) >/dev/null
printf 'Egress policy and Collector runtime configuration contract passed\n'
