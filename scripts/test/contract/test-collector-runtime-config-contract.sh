#!/usr/bin/env bash
set -euo pipefail

# Collector 运行配置的契约：部署时 moox-cli 按 moox.toml 渲染 Collector 的 app.yaml（见 release 包的快照测试），
# 出口代理部署在表中时写入经出口代理访问的域名和 DNS 快照的域名；交易服务不再参与 DNS 解析；渲染结果不能带出任何密钥。

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
SNAPSHOT="${ROOT}/modules/cli/internal/setup/release/testdata/snapshot"

# set -e 不会因为 "! grep" 失败而退出，取反的断言必须显式退出。
refute() {
  if grep -rFq -- "$1" "$2"; then
    echo "$2 不应包含 $1" >&2
    exit 1
  fi
}

refute 'TradeDNSResolverService' "${ROOT}/modules/trade/proto/trade_service.proto"
refute 'TradeDNSResolverService' "${ROOT}/modules/trade/config/trpc_go.yaml"
refute 'dns_resolver' "${ROOT}/modules/trade/config/app.yaml"
grep -Fq 'rpc ResolveDomains(ResolveDomainsReq) returns (ResolveDomainsRsp);' "${ROOT}/modules/egressproxy/proto/egress.proto"

# 快照由 moox.toml.example 渲染，与当前渲染规则一致。
(cd "${ROOT}/modules/cli" && go test -count=1 ./internal/setup/release -run TestRenderSnapshot)

python3 - "${SNAPSHOT}/control/collector/config/app.yaml" <<'PY'
import sys

# 不依赖 PyYAML：渲染结果由 yaml.v3 按固定缩进输出，按文本比对整段。
with open(sys.argv[1], encoding="utf-8") as handle:
    text = handle.read()
expected = """egress_proxy:
  domains:
    - '*.binance.com'
    - data-api.binance.vision
  dns:
    domains:
      - data-api.binance.vision
      - api-gcp.binance.com
      - api.binance.com
      - fapi.binance.com
    refresh_interval: 300s
    request_timeout: 3000ms
    cache_ttl: 300s
"""
assert expected in text, text
assert "dns_resolver" not in text, text
PY

# 快照测试给 moox.toml.example 填的口令和云凭据不能出现在任何渲染结果里。
for secret in test-password test-secret-id test-secret-key; do
  refute "${secret}" "${SNAPSHOT}"
done

echo 'Collector runtime configuration contract passed'
