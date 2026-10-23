#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

! rg -q 'TradeDNSResolverService' "${ROOT}/modules/trade/proto/trade_service.proto" "${ROOT}/modules/trade/config/trpc_go.yaml"
! rg -q 'trade_dns_resolver' "${ROOT}/config/setup/service-deployments.yaml"
rg -q 'service Proxy' "${ROOT}/modules/egressproxy/proto/egress.proto"
rg -q 'RenderEgressConfig' "${ROOT}/modules/cli/internal/setup/config/runtime_config.go"
rg -q 'RenderCollectorRuntimeConfig' "${ROOT}/modules/cli/internal/setup/config/runtime_config.go"

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/moox-egress-contract.XXXXXX")"
trap 'rm -rf "${tmp_root}"' EXIT
go build -o "${tmp_root}/moox-cli" "${ROOT}/modules/cli/cmd/moox-cli"
mkdir -p "${tmp_root}/egress" "${tmp_root}/collector"
cp "${ROOT}/modules/egressproxy/config/app.yaml" "${tmp_root}/egress/app.yaml"
cp "${ROOT}/modules/collector/config/app.yaml" "${tmp_root}/collector/app.yaml"
# Keep this contract self-contained.  The repository's real moox.toml is
# intentionally ignored and may contain production credentials; contract
# tests must never depend on or copy it.
cat >"${tmp_root}/moox.toml" <<'EOF'
[admin]
username = "contract-admin"
password = "contract-password"

[tencent_cloud]
secret_id = "contract-secret-id"
secret_key = "contract-secret-key"
region = "ap-guangzhou"

[eventbus]
port = 4222
tls_enabled = true

[hosts.control]
address = "192.0.2.10"
tls_mode = "auto"
ssh = { port = 22, username = "ubuntu" }

[hosts.storage]
address = "192.0.2.11"
ssh = { port = 22, username = "ubuntu" }

[hosts.overseas]
address = "43.132.204.177"
ssh = { port = 22, username = "ubuntu" }

[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudfunction", "factor-mgr", "strategy"]
storage = ["storage-primary", "storage-node", "storage-view", "access"]
overseas = ["trade", "egress-proxy"]

[egress_proxy]
http_domains = ["*.binance.com", "data-api.binance.vision"]

[egress_proxy.dns]
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
  --egress-output "${tmp_root}/egress/app.yaml" \
  --collector-output "${tmp_root}/collector/app.yaml") >/dev/null

rg -q 'lookup_timeout_ms: 1500' "${tmp_root}/egress/app.yaml"
rg -q 'egress_proxy:' "${tmp_root}/collector/app.yaml"
! rg -q 'target:|node_id:|dns_resolver:|secret_id:|secret_key:' "${tmp_root}/collector/app.yaml" "${tmp_root}/egress/app.yaml"
printf 'Egress policy and Collector runtime configuration contract passed\n'
