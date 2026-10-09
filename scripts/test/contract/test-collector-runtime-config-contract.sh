#!/usr/bin/env bash
set -euo pipefail

# Collector 运行配置的契约：部署时 moox-cli 按 moox.toml 渲染 Collector 的 app.yaml，DNS 快照的域名交给
# 出口代理解析，交易服务不再参与；渲染结果不能带出任何密钥。

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

# set -e 不会因为 "! grep" 失败而退出，取反的断言必须显式退出。
refute() {
  if grep -Fq -- "$1" "$2"; then
    echo "$2 不应包含 $1" >&2
    exit 1
  fi
}

grep -Fq 'setup render-runtime-config' "${ROOT}/scripts/deploy/deploy-moox.sh"
refute '--trade-output' "${ROOT}/scripts/deploy/deploy-moox.sh"
refute 'trade_dns_resolver' "${ROOT}/scripts/deploy/deploy-moox.sh"
refute 'TradeDNSResolverService' "${ROOT}/modules/trade/proto/trade_service.proto"
refute 'TradeDNSResolverService' "${ROOT}/modules/trade/config/trpc_go.yaml"
refute 'dns_resolver' "${ROOT}/modules/trade/config/app.yaml"
grep -Fq 'rpc ResolveDomains(ResolveDomainsReq) returns (ResolveDomainsRsp);' "${ROOT}/modules/egressproxy/proto/egress.proto"

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-runtime-contract.XXXXXX")"
trap 'rm -rf "${tmp_root}"' EXIT
go build -o "${tmp_root}/moox-cli" "${ROOT}/modules/cli/cmd/moox-cli"
mkdir -p "${tmp_root}/collector"
cp "${ROOT}/modules/collector/config/app.yaml" "${tmp_root}/collector/app.yaml"
# 契约测试自带一份 moox.toml；仓库里真实的 moox.toml 不受版本管理，可能含有生产凭据，测试不能依赖或复制它。
cat >"${tmp_root}/moox.toml" <<'EOF'
[admin]
username = "contract-admin"
password = "contract-password"

[tencent_cloud]
secret_id = "contract-secret-id"
secret_key = "contract-secret-key"
region = "ap-guangzhou"

[eventbus]
host = "192.0.2.10"
port = 4222
tls_enabled = true

[hosts."192.0.2.10"]
port = 22
username = "ubuntu"
password = "control-host-password"

[hosts."43.132.204.177"]
port = 22
username = "ubuntu"
password = "compute-host-password"

[control_host]
name = "control"
host = "192.0.2.10"

[[other_hosts]]
name = "compute-1"
host = "43.132.204.177"

[dns_resolver]
enabled = true
trade_node = "compute-1"
refresh_interval_seconds = 300
request_timeout_ms = 3000
lookup_timeout_ms = 1500
probe_timeout_ms = 500
probe_port = 443
cache_ttl_seconds = 300
max_ips_per_domain = 4
domains = ["data-api.binance.vision", "api.binance.com", "fapi.binance.com"]
EOF
chmod 600 "${tmp_root}/moox.toml"
(cd "${tmp_root}" && ./moox-cli setup render-runtime-config \
  --file "${tmp_root}/moox.toml" \
  --collector-output "${tmp_root}/collector/app.yaml") >"${tmp_root}/render.json"

python3 - "${tmp_root}/collector/app.yaml" "${tmp_root}/render.json" <<'PY'
import json
import sys

# 不依赖 PyYAML：渲染结果由 yaml.v3 按固定缩进输出，按文本比对整段。
with open(sys.argv[1], encoding="utf-8") as handle:
    text = handle.read()
expected = """egress_proxy:
  domains: []
  dns:
    domains:
      - data-api.binance.vision
      - api.binance.com
      - fapi.binance.com
    refresh_interval: 300s
    request_timeout: 3000ms
    cache_ttl: 300s
"""
assert expected in text, text
assert "dns_resolver" not in text, text
with open(sys.argv[2], encoding="utf-8") as handle:
    output = json.load(handle)
assert output["status"] == "rendered", output
assert output["trade_console_host"] == "43.132.204.177", output
PY

for secret in contract-password contract-secret-id contract-secret-key control-host-password compute-host-password; do
  refute "${secret}" "${tmp_root}/collector/app.yaml"
  refute "${secret}" "${tmp_root}/render.json"
done

echo 'Collector runtime configuration contract passed'
