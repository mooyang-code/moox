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
grep -q 'MOOX_METRICS_STORAGE_ROUTE_SEED' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'MOOX_METRICS_STORAGE_METADATA_URL' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'apply_host_metadata' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'metadata-monitor-host.seed.yaml' "${ROOT}/scripts/deploy-moox.sh"
grep -q 'MOOX_HOST_STORAGE_ROUTE_SEED' "${ROOT}/scripts/deploy-moox.sh"

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
