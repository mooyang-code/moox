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

if ! awk '/start_eventbus\(\)/ { start=NR } /start_storage\(\)/ { storage=NR } END { exit !(start < storage) }' "${ROOT}/scripts/deploy-moox.sh"; then
  echo "eventbus must start before storage" >&2
  exit 1
fi
if ! awk '/stop_service "storage"/ { storage=NR } /stop_service "eventbus"/ { eventbus=NR } END { exit !(storage < eventbus) }' "${ROOT}/scripts/deploy-moox.sh"; then
  echo "eventbus must stop after storage" >&2
  exit 1
fi

echo "moox-eventbus deployment contract passed"
