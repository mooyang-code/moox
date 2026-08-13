#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ "$(basename "${SCRIPT_DIR}")" == "contract" ]]; then
  ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
else
  ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
fi

grep -Fq 'service TradeDNSResolverService' "${ROOT}/modules/trade/proto/trade_service.proto"
grep -Fq 'repeated string unresolved_domains' "${ROOT}/modules/trade/proto/trade_service.proto"
grep -Fq 'tcp_connect_latency_ms' "${ROOT}/modules/trade/proto/trade_service.proto"
grep -Fq 'TradeDNSResolverService.trpc' "${ROOT}/modules/trade/config/trpc_go.yaml"
grep -Fq 'trade_dns_resolver' "${ROOT}/examples/setup/default/service-deployments.yaml"
grep -Fq -- '--only-services' "${ROOT}/modules/admin/cmd/cli/service_deployments.go"
! grep -Fq 'resolved_at_unix_ms' "${ROOT}/modules/trade/proto/trade_service.proto"
! grep -Fq 'string error' "${ROOT}/modules/trade/proto/trade_service.proto"
grep -Fq 'setup render-runtime-config' "${ROOT}/scripts/deploy-moox.sh"
grep -Fq 'RenderTradeDNSResolverConfig' "${ROOT}/modules/cli/internal/setup/config/runtime_config.go"
grep -Fq 'RenderCollectorDNSResolverConfig' "${ROOT}/modules/cli/internal/setup/config/runtime_config.go"

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/moox-trade-dns-contract.XXXXXX")"
trap 'rm -rf "${tmp_root}"' EXIT
go build -o "${tmp_root}/moox-cli" "${ROOT}/modules/cli/cmd/moox-cli"
mkdir -p "${tmp_root}/trade" "${tmp_root}/collector"
cp "${ROOT}/modules/trade/config/app.yaml" "${tmp_root}/trade/app.yaml"
cp "${ROOT}/modules/collector/config/app.yaml" "${tmp_root}/collector/app.yaml"
# Keep this contract self-contained.  The repository's real custom.toml is
# intentionally ignored and may contain production credentials; contract
# tests must never depend on or copy it.
cat >"${tmp_root}/custom.toml" <<'EOF'
[admin]
username = "contract-admin"
password = "contract-password"

[tencent_cloud]
secret_id = "contract-secret-id"
secret_key = "contract-secret-key"
region = "ap-guangzhou"

[eventbus]
public_address = "eventbus.example.com"
port = 4222
tls_enabled = true

[control_host]
name = "control"
address = "127.0.0.1"
username = "ubuntu"
password = "control-host-password"

[[other_hosts]]
name = "compute-1"
address = "43.132.204.177"
username = "ubuntu"
password = "compute-host-password"

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
chmod 600 "${tmp_root}/custom.toml"
(cd "${tmp_root}" && ./moox-cli setup render-runtime-config \
  --file "${tmp_root}/custom.toml" \
  --trade-output "${tmp_root}/trade/app.yaml" \
  --collector-output "${tmp_root}/collector/app.yaml") >/dev/null

grep -Fq 'enabled: true' "${tmp_root}/trade/app.yaml"
grep -Fq 'lookup_timeout_ms: 1500' "${tmp_root}/trade/app.yaml"
grep -Fq 'target: ip://43.132.204.177:11003' "${tmp_root}/collector/app.yaml"
grep -Fq 'node_id: compute-1' "${tmp_root}/collector/app.yaml"
! grep -Fq 'secret_id:' "${tmp_root}/trade/app.yaml"
! grep -Fq 'secret_key:' "${tmp_root}/trade/app.yaml"
! grep -Fq 'secret_id:' "${tmp_root}/collector/app.yaml"
! grep -Fq 'secret_key:' "${tmp_root}/collector/app.yaml"

echo 'Trade DNS resolver configuration contract passed'
