#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

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
  --glob '!**/test-storage-datanode-management-contract.sh'
)

required_files=(
  modules/storage/proto/metadata.proto
  modules/storage/proto/data_node.proto
  modules/storage/schema/metadata.sql
  examples/setup/default/metadata.yaml
  modules/storage/internal/service/catalog/metadata_catalog.go
  modules/storage/internal/service/primarystore/service.go
  modules/storage/cmd/cli/main.go
  modules/cli/internal/doctor/storage_activation.go
  modules/cli/internal/command/setup_storage.go
  web/tests/storage-datanode-management.remote.e2e.spec.ts
  web/src/views/ops/storage/nodes.vue
  web/src/views/data/datasets/index.vue
)

for file in "${required_files[@]}"; do
  if [[ ! -f "${file}" ]]; then
    printf 'storage DataNode management contract: missing required file %s\n' "${file}" >&2
    exit 1
  fi
done

require_text() {
  local file="$1"
  local pattern="$2"
  local description="$3"
  if ! rg -q --fixed-strings -- "$pattern" "$file"; then
    printf 'storage DataNode management contract: missing %s (%s)\n' "$description" "$file" >&2
    exit 1
  fi
}

require_regex() {
  local file="$1"
  local pattern="$2"
  local description="$3"
  if ! rg -q -- "$pattern" "$file"; then
    printf 'storage DataNode management contract: missing %s (%s)\n' "$description" "$file" >&2
    exit 1
  fi
}

require_text modules/storage/proto/metadata.proto 'message DataNode {' 'metadata DataNode message'
require_text modules/storage/proto/metadata.proto 'string data_node_id = 17;' 'Dataset direct DataNode binding'
require_text modules/storage/proto/metadata.proto 'bool binding_locked = 19;' 'Dataset binding lock'
require_text modules/storage/proto/metadata.proto 'int64 revision = 20;' 'Dataset revision CAS'
require_text modules/storage/proto/metadata.proto 'rpc RegisterDataNode' 'deployment DataNode registration RPC'
require_text modules/storage/proto/metadata.proto 'rpc CheckDatasetActivation' 'read-only activation check RPC'
require_text modules/storage/proto/metadata.proto 'rpc ActivateDataset' 'explicit activation RPC'
require_text modules/storage/proto/data_node.proto 'service DataNodeRuntime {' 'DataNode runtime service'
require_text modules/storage/schema/metadata.sql "VALUES ('schema_version', '10')" 'Schema v10'
require_text examples/setup/default/metadata.yaml 'data_source_id: crypto' 'shared crypto logical DataSource binding'
require_text examples/setup/default/metadata.yaml 'dataset_id: spot_kline_1h' 'shared crypto spot Dataset'
require_text examples/setup/default/metadata.yaml 'dataset_id: perpetual_kline_1h' 'shared crypto perpetual Dataset'
require_text examples/setup/default/metadata.yaml 'view_id: spot_kline_1h_view' 'shared crypto spot View'
require_text examples/setup/default/metadata.yaml 'view_id: perpetual_kline_1h_view' 'shared crypto perpetual View'
require_text examples/setup/default/metadata.yaml 'series_tag' 'tagged time-series grain'
if [[ -e modules/storage/config/metadata.seed.yaml ]]; then
  echo 'storage DataNode management contract: duplicate storage metadata seed remains outside examples/setup/default' >&2
  exit 1
fi

for forbidden_seed_id in \
  binance_spot_kline_1h \
  binance_perpetual_kline_1h \
  okx_spot_kline_1h \
  okx_perpetual_kline_1h \
  binance_spot_1h_view \
  binance_perpetual_1h_view \
  okx_spot_1h_view \
  okx_perpetual_1h_view; do
  if rg -q --fixed-strings -- "${forbidden_seed_id}" examples/setup/default/metadata.yaml; then
    printf 'storage DataNode management contract: obsolete crypto seed ID found: %s\n' "${forbidden_seed_id}" >&2
    exit 1
  fi
done

legacy_time_series_grains="$(
  rg -n --glob '*.yaml' --fixed-strings \
    'grain_keys: [subject_id, freq, data_time]' modules examples || true
)"
if [[ -n "${legacy_time_series_grains}" ]]; then
  printf 'storage DataNode management contract: legacy three-column time-series grains found:\n%s\n' "${legacy_time_series_grains}" >&2
  exit 1
fi
require_text modules/storage/schema/metadata.sql 'CREATE TABLE IF NOT EXISTS t_data_nodes' 'DataNode table'
require_text modules/storage/schema/metadata.sql 'FOREIGN KEY (c_data_node_id) REFERENCES t_data_nodes (c_node_id) ON DELETE RESTRICT' 'Dataset DataNode foreign key'
require_text examples/setup/default/metadata.yaml 'data_node_id:' 'seed direct DataNode binding'
require_regex modules/storage/internal/service/catalog/metadata_catalog.go 'CheckDatasetActivation|ActivateDataset' 'activation lifecycle implementation'
require_regex modules/storage/internal/service/primarystore/service.go 'DataNode|data_node' 'snapshot DataNode resolution'
require_regex modules/storage/cmd/cli/main.go 'RegisterDataNode' 'deployment registration implementation'
require_regex modules/cli/internal/doctor/storage_activation.go 'CheckDatasetActivation' 'read-only Doctor activation check'
require_regex web/src/views/ops/storage/nodes.vue 'DataNode|data_node' 'DataNode management UI'
require_regex web/src/views/data/datasets/index.vue 'activateDataset|rebindDatasetDataNode|data_node_id' 'Dataset activation and binding UI'
require_regex modules/cli/internal/command/setup_storage.go 'createStorageBrowserFixture|MOOX_REMOTE_STORAGE_FIXTURE|browser_e2e_cleanup_failed' 'isolated remote browser fixture lifecycle'
require_regex web/tests/storage-datanode-management.remote.e2e.spec.ts 'MOOX_REMOTE_STORAGE_FIXTURE|ActivateDataset|remote fixture Dataset must be listed' 'remote browser lifecycle assertions'

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
  printf 'storage DataNode management contract: forbidden current symbols found:\n%s\n' "${forbidden_matches}" >&2
  exit 1
fi

metadata_context_matches="$(
  {
    sed -n '/message DataNode[[:space:]]*{/,/^}/p' modules/storage/proto/metadata.proto
    sed -n '/CREATE TABLE IF NOT EXISTS t_data_nodes/,/^);/p' modules/storage/schema/metadata.sql
    sed -n '/export interface DataNode[[:space:]]*{/,/^}/p' web/src/api/storage/types.ts
  } | rg -n -i 'subject_pattern|hash_rule|route[[:space:]_-]*priority|(^|[^[:alnum:]_])(weight|config_json|endpoint)([^[:alnum:]_]|$)' || true
)"
if [[ -n "${metadata_context_matches}" ]]; then
  printf 'storage DataNode management contract: route-only fields found in storage metadata context:\n%s\n' "${metadata_context_matches}" >&2
  exit 1
fi

printf 'storage DataNode management contract: ok\n'
