#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

has_top_level_filter() {
  local config="$1" section="$2"
  awk -v target="${section}" '
    /^[[:alnum:]_]+:/ {
      current = $1
      sub(/:$/, "", current)
    }
    current == target && /^    - requestmetrics$/ { found++ }
    END { exit found == 1 ? 0 : 1 }
  ' "${config}"
}

plugin_configs=$(rg -l '^\s+prometheus:$' "${ROOT}/modules" --glob 'trpc_go*.yaml' || true)
if [[ -n "${plugin_configs}" ]]; then
  printf 'unauthenticated Prometheus listeners remain:\n%s\n' "${plugin_configs}" >&2
  exit 1
fi

legacy_filters=$(rg -l '^\s+- prometheus$' "${ROOT}/modules" --glob 'trpc_go*.yaml' || true)
if [[ -n "${legacy_filters}" ]]; then
  printf 'legacy Prometheus filters remain:\n%s\n' "${legacy_filters}" >&2
  exit 1
fi

plugin_references=$(rg -l 'trpc-metrics-prometheus' "${ROOT}/modules" --glob '*.go' --glob 'go.mod' || true)
if [[ -n "${plugin_references}" ]]; then
  printf 'Prometheus listener plugin references remain:\n%s\n' "${plugin_references}" >&2
  exit 1
fi

for config in \
  modules/admin/config/trpc_go.yaml \
  modules/cloudnode/config/trpc_go.yaml \
  modules/collector/config/trpc_go.yaml \
  modules/eventbus/config/trpc_go.yaml \
  modules/factor/config/trpc_go.yaml \
  modules/monitor/config/trpc_go.yaml \
  modules/storage/config/trpc_go.yaml \
  modules/storage/config/trpc_go.access.yaml \
  modules/storage/config/trpc_go.view.yaml \
  modules/storage/config/trpc_go.view_builder.yaml \
  modules/storage/config/trpc_go.view_index.yaml \
  modules/storage/config/trpc_go.view_query.yaml \
  modules/trade/config/trpc_go.yaml; do
  has_top_level_filter "${ROOT}/${config}" server && has_top_level_filter "${ROOT}/${config}" client || {
    printf 'server/client request metrics filters missing from %s\n' "${config}" >&2
    exit 1
  }
done
rg -q 'moox_trpc_server_requests_total' "${ROOT}/packages/healthz/metrics.go"
rg -q 'moox_trpc_client_requests_total' "${ROOT}/packages/healthz/metrics.go"

printf 'PASS: request metrics use only authenticated module health exporters\n'
