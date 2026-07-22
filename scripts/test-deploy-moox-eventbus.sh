#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# This regression check is deliberately filesystem-only: it does not require a
# running NATS server or a remote host, but verifies the package/lifecycle
# contract that is easy to break while editing deploy-moox.sh.
bash -n "${ROOT}/scripts/build.sh" "${ROOT}/scripts/release.sh" "${ROOT}/scripts/deploy-moox.sh"
grep -q 'moox-eventbus' "${ROOT}/scripts/build.sh"
grep -q -- '--no-eventbus' "${ROOT}/scripts/deploy-moox.sh"
grep -q '/readyz' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'start_eventbus' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'stop_service "eventbus"' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'MOOX_WITH_EVENTBUS="\${WITH_EVENTBUS}".*stop.sh' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'data/eventbus/jetstream' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'logs/eventbus' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'apply_metrics_metadata' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'metadata-monitor-metrics.seed.yaml' "${ROOT}/scripts/deploy-moox.sh"
! grep -Eq 'MOOX_(METRICS|HOST)_STORAGE_ROUTE_SEED|local-route' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'MOOX_METRICS_STORAGE_METADATA_URL' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'apply_host_metadata' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'metadata-monitor-host.seed.yaml' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'secrets/health-auth.env' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'moox-admin-cli.*random-secret.*--bytes 32' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'MOOX_HEALTH_AUTH_SECRET_KEY' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'sign_health_request' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'MOOX_SERVICE_GATEWAY_CA_FILE' "${ROOT}/scripts/deploy-moox.sh"
for dataset in host_resource_v1 host_fs_v1 host_disk_v1 host_net_v1; do
  grep -q "dataset_id: ${dataset}" "${ROOT}/examples/metadata-monitor-host.seed.yaml"
  grep -q "dataset_id: ${dataset}.*status: disabled" "${ROOT}/examples/metadata-monitor-host.seed.yaml"
done
grep -q 'data_node_id: storage-node-0' "${ROOT}/examples/metadata-monitor-host.seed.yaml"
grep -q 'data_node_id: storage-node-0' "${ROOT}/examples/metadata-monitor-metrics.seed.yaml"

if ! awk '/start_eventbus\(\)/ { start=NR } /start_storage\(\)/ { storage=NR } END { exit !(start < storage) }' "${ROOT}/scripts/deploy-moox.sh"; then
  echo "eventbus must start before storage" >&2
  exit 1
fi
if ! awk '/stop_service "storage"/ { storage=NR } /stop_service "eventbus"/ { eventbus=NR } END { exit !(storage < eventbus) }' "${ROOT}/scripts/deploy-moox.sh"; then
  echo "eventbus must stop after storage" >&2
  exit 1
fi
if ! awk '/^apply_metrics_metadata\(\)/ { metadata=NR } /^start_monitor\(\)/ { monitor=NR } END { exit !(metadata < monitor) }' "${ROOT}/scripts/deploy-moox.sh"; then
  echo "metadata preflight must run before monitor" >&2
  exit 1
fi

echo "moox-eventbus deployment contract passed"
