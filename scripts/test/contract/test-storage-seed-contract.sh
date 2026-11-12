#!/usr/bin/env bash
# Storage 元数据种子与主服务形态的契约：种子只放在 config/setup，数据集直接属于主服务，不再有 DataNode 表与节点 RPC 服务。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT}"

scan_paths=(README.md Makefile modules examples scripts skills web packages docs)
scan_globs=(
  --glob '!**/.git/**'
  --glob '!**/.worktrees/**'
  --glob '!**/build/**'
  --glob '!**/dist/**'
  --glob '!**/node_modules/**'
  --glob '!docs/superpowers/plans/**'
  --glob '!docs/superpowers/specs/**'
  --glob '!docs/**/代码审查报告*.md'
  --glob '!docs/**/性能基准报告/**'
  --glob '!**/test-storage-seed-contract.sh'
)

fail() {
  printf 'storage seed contract: %s\n' "$*" >&2
  exit 1
}

require_text() {
  local file="$1"
  local pattern="$2"
  local description="$3"
  if ! rg -q --fixed-strings -- "$pattern" "$file"; then
    fail "missing ${description} (${file})"
  fi
}

forbid_text() {
  local file="$1"
  local pattern="$2"
  local description="$3"
  if rg -q --fixed-strings -- "$pattern" "$file"; then
    fail "${description} remains (${file})"
  fi
}

for file in modules/storage/proto/metadata.proto modules/storage/schema/metadata.sql config/setup/metadata.yaml; do
  [[ -f "${file}" ]] || fail "missing required file ${file}"
done

require_text config/setup/metadata.yaml 'data_source_id: crypto' 'shared crypto logical DataSource binding'
require_text config/setup/metadata.yaml 'dataset_id: dataset_stockus_equity_kline_1d' 'seeded stockus equity Dataset'
require_text config/setup/metadata.yaml 'series_tag' 'tagged time-series grain'
require_text modules/storage/proto/metadata.proto 'int64 revision = 20;' 'Dataset revision CAS'
[[ ! -e modules/storage/config/metadata.seed.yaml ]] || fail 'duplicate storage metadata seed remains outside config/setup'

# 节点已并入主服务：没有 DataNode 表、节点 RPC 服务、节点进程配置与节点角色。
[[ ! -e modules/storage/proto/data_node.proto ]] || fail 'data_node.proto must be removed'
[[ ! -e modules/storage/config/storage.node.yaml ]] || fail 'storage.node.yaml must be removed'
[[ ! -e modules/storage/internal/service/datanode/service.go ]] || fail 'DataNode RPC service must be removed'
forbid_text modules/storage/schema/metadata.sql 't_data_nodes' 'DataNode table'
forbid_text modules/storage/schema/metadata.sql 'c_data_node_id' 'Dataset DataNode column'
forbid_text modules/storage/schema/metadata.sql 'c_binding_locked' 'Dataset binding lock column'
forbid_text modules/storage/cmd/server/main.go 'runDataNodeRole' 'DataNode process role'
forbid_text modules/storage/internal/service/primarystore/service.go 'Resolver' 'PrimaryStore DataNode resolver'

for forbidden_seed_id in \
  binance_spot_kline_1h \
  binance_perpetual_kline_1h \
  okx_spot_kline_1h \
  okx_perpetual_kline_1h \
  binance_spot_1h_view \
  binance_perpetual_1h_view \
  okx_spot_1h_view \
  okx_perpetual_1h_view; do
  if rg -q --fixed-strings -- "${forbidden_seed_id}" config/setup/metadata.yaml; then
    fail "obsolete crypto seed ID found: ${forbidden_seed_id}"
  fi
done

legacy_time_series_grains="$(
  rg -n --glob '*.yaml' --fixed-strings \
    'grain_keys: [subject_id, freq, data_time]' modules examples || true
)"
if [[ -n "${legacy_time_series_grains}" ]]; then
  fail "legacy three-column time-series grains found:
${legacy_time_series_grains}"
fi

forbidden_matches="$(rg -n -i --no-heading "${scan_globs[@]}" \
  -e 'primarystorenode' \
  -e 'primary_store_node' \
  -e 'primarystoreroute' \
  -e 'primary_store_route' \
  -e 't_dataset_topology_locks' \
  -e 'attributes\.service_target' \
  -e 'metadata-monitor-.*-local-route' \
  -e '/#/ops/storage/routes' \
  "${scan_paths[@]}" || true)"
if [[ -n "${forbidden_matches}" ]]; then
  fail "forbidden current symbols found:
${forbidden_matches}"
fi

printf 'storage seed contract: ok\n'
