#!/usr/bin/env bash
# 旧称扫描：网关重构后，代码、配置、脚本、文档和 skills 中不应再出现旧的网关名称和已删除的组件、配置键。
# 排除本重构的两份计划文档（记录了历史名称）、本脚本、部署时才重新生成的控制台静态资源（web-host 的 statik.go），
# 以及专门断言旧名称已删除或会被拒绝的文件（见 exempt）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

command -v rg >/dev/null 2>&1 || { echo "check-gateway-terms: 需要 rg" >&2; exit 1; }

terms=(
  # 旧的网关称呼
  '节点网关' '管理网关' '服务网关' 'service gateway' 'Service Gateway' 'Storage Gateway' 'storage gateway'
  'Trade Gateway' 'trade gateway'
  'admin_gateway' 'admin-gateway' 'service_gateway' 'moox_gateway' 'storage-gateway' 'storage-access-storage'
  'trpc.moox.gateway.control' 'gateway-control' 'gateway_control_url'
  # 旧模块、二进制与包
  'modules/gateway/' 'moox-gateway' 'moox-storage-access' 'MOOX_STORAGE_ACCESS_' 'gatewayproxy'
  'ServiceGatewayTarget' 'ServiceGatewayNodeID' 'CredentialsFromEnv'
  # 已删除的配置键与环境变量
  'gateway_target' 'gateway_node_id' 'admin_gateway_url' 'MOOX_NODE_GATEWAY_' 'MOOX_GATEWAY_TARGET_NODE'
  'MOOX_SERVICE_GATEWAY_' 'MOOX_COLLECTOR_ADMIN_GATEWAY_URL' 'MOOX_GATEWAY_CONTROL_'
  # 已删除的组件与服务
  'moox-collector-subject' 'collector-subject' 'TradeDNSResolverService' 'trade_dns_resolver' 'trade_console'
  'collector_market_runtime' 'storage_access_targets' 'collector_rpc_gateway_target' 'storage_gateway_host'
  # 旧部署脚本
  'deploy-moox.sh' 'caddy-managed.sh'
)

# 这些文件专门校验旧名称已删除或会被拒绝（例如 moox.toml 旧键的迁移提示），必须提到旧名称。
exempt=(
  modules/cli/internal/setup/config/topology.go
  modules/cli/internal/setup/config/topology_test.go
  modules/collector/internal/bootstrap/config_test.go
  modules/collector/internal/bootstrap/bootstrap_test.go
  scripts/test/contract/test-collector-runtime-config-contract.sh
  scripts/test/contract/test-storage-boundary-contract.sh
  scripts/test/contract/test-deploy-factor-engine.sh
)

args=()
for term in "${terms[@]}"; do
  args+=(-e "${term}")
done
for file in "${exempt[@]}"; do
  args+=(--glob "!${file}")
done
matches="$(rg -n -F --no-heading "${args[@]}" \
  --glob '!docs/计划/网关与服务部署重构设计.md' \
  --glob '!docs/计划/网关与服务部署重构执行计划.md' \
  --glob '!scripts/check/check-gateway-terms.sh' \
  --glob '!web-host/internal/statik.go' \
  --glob '!**/node_modules/**' \
  --glob '!web/dist/**' \
  --glob '!release/**' \
  . || true)"

if [[ -n "${matches}" ]]; then
  echo "${matches}"
  echo "check-gateway-terms: 发现 $(printf '%s\n' "${matches}" | wc -l | tr -d ' ') 处旧称" >&2
  exit 1
fi
echo "check-gateway-terms: 没有旧称"
