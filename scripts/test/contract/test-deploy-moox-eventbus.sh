#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"

# This regression check is deliberately filesystem-only: it does not require a
# running NATS server or a remote host, but verifies the package/lifecycle
# contract that is easy to break while editing deploy-moox.sh.
bash -n "${ROOT}/scripts/build/build.sh" "${ROOT}/scripts/release/release.sh" "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'moox-eventbus' "${ROOT}/scripts/build/build.sh"
grep -q -- '--no-eventbus' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q '/readyz' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'start_eventbus' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'trade_eventbus_preflight' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'eventbus-check' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'archive_eventbus_url' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'tls://127.0.0.1:4222' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'disable_conflicting_eventbus_supervisor' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'systemctl --user show -p ExecStart --value moox-eventbus.service' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'systemctl --user is-active moox-eventbus.service' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'systemctl --user is-enabled moox-eventbus.service' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'systemctl --user disable moox-eventbus.service' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'MOOX_EVENTBUS_NATS_URL=.*nats://127.0.0.1:4222' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'nats://127.0.0.1:4222' "${ROOT}/modules/cloudnode/config/app.yaml"
! grep -q '4322' "${ROOT}/modules/cloudnode/config/app.yaml"
! grep -q 'MOOX_FACTOR_NATS_URL' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'stop_service "eventbus"' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'MOOX_WITH_EVENTBUS="\${WITH_EVENTBUS}".*stop.sh' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'data/eventbus/jetstream' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'logs/eventbus' "${ROOT}/scripts/deploy/deploy-moox.sh"
for forbidden in apply_monitor_metadata config/setup/metadata.yaml 'metadata import'; do
  if grep -q "${forbidden}" "${ROOT}/scripts/deploy/deploy-moox.sh"; then
    echo "deploy-moox.sh must not import setup metadata outside setup init: ${forbidden}" >&2
    exit 1
  fi
done
! grep -Eq 'MOOX_(METRICS|HOST)_STORAGE_ROUTE_SEED|local-route' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'MOOX_METRICS_STORAGE_METADATA_URL' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'secrets/health-auth.env' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'moox-admin-cli.*random-secret.*--bytes 32' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'MOOX_HEALTH_AUTH_SECRET_KEY' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'sign_health_request' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'MOOX_SERVICE_GATEWAY_CA_FILE' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq \
  '[[ "${WITH_EVENTBUS}" == "1" && "${MOOX_EVENTBUS_ENABLE_TLS:-0}" == "1" && -x "${ROOT}/bin/moox-admin-cli" ]]' \
  "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'missing EventBus TLS credential' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_EVENTBUS_ENABLE_TLS=${quoted_eventbus_enable_tls}' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_EVENTBUS_NATS_TLS_CA_FILE=${eventbus_ca_file}' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_EVENTBUS_PUBLIC_IP=${quoted_eventbus_public_ip}' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_EVENTBUS_PORT="${MOOX_EVENTBUS_PORT:-4222}"' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_EVENTBUS_PORT=${quoted_eventbus_port}' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_EVENTBUS_ENABLE_TLS="${MOOX_EVENTBUS_ENABLE_TLS:-__EVENTBUS_ENABLE_TLS__}"' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 's#__EVENTBUS_ENABLE_TLS__#${MOOX_EVENTBUS_ENABLE_TLS:-0}#g' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq -- '--public-host "${PUBLIC_HOST:-127.0.0.1}"' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq '${MOOX_EVENTBUS_PUBLIC_IP}:${MOOX_EVENTBUS_PORT}' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'EVENTBUS_PORT="${MOOX_EVENTBUS_PORT}" perl -0pi' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_EVENTBUS_PUBLIC_IP requires MOOX_EVENTBUS_ENABLE_TLS=1' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'validate_eventbus_governance' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'remote Collector deployment requires MOOX_EVENTBUS_PUBLIC_IP' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'runtime governance acceptance passed' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'config/runtime.env' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'url="${url#tls://}"' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID=collector' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq 'gateway-moox-cli.env" "${deploy_dir}/secrets/gateway-moox-cli.env' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -Fq '"${STAGE_DIR}"/secrets/gateway-cloudnode.key' "${ROOT}/scripts/deploy/deploy-moox.sh"
for dataset in dataset_mooxsys_host_resource dataset_mooxsys_host_filesystem dataset_mooxsys_host_disk dataset_mooxsys_host_network; do
  grep -q "dataset_id: ${dataset}" "${ROOT}/config/setup/metadata.yaml"
done
for column in receive_errors_per_second transmit_errors_per_second error_rate_available; do
  grep -q "column_name: ${column}" "${ROOT}/config/setup/metadata.yaml"
done
grep -q 'data_node_id: storage-node-0' "${ROOT}/config/setup/metadata.yaml"

control_profile=$(sed -n '/^    control)/,/^    storage)/p' "${ROOT}/scripts/deploy/deploy-moox.sh")
grep -Fq 'WITH_EVENTBUS=1' <<<"${control_profile}"
grep -Fq 'WITH_CLOUDNODE=1' <<<"${control_profile}"
grep -Fq 'WITH_COLLECTOR=1' <<<"${control_profile}"

if MOOX_EVENTBUS_PORT=not-a-number "${ROOT}/scripts/deploy/deploy-moox.sh" --help >/dev/null 2>&1; then
  echo "non-numeric EventBus port must be rejected" >&2
  exit 1
fi
if MOOX_EVENTBUS_PORT=70000 "${ROOT}/scripts/deploy/deploy-moox.sh" --help >/dev/null 2>&1; then
  echo "out-of-range EventBus port must be rejected" >&2
  exit 1
fi
if MOOX_EVENTBUS_PORT=18446744073709551617 "${ROOT}/scripts/deploy/deploy-moox.sh" --help >/dev/null 2>&1; then
  echo "overflowing EventBus port must be rejected" >&2
  exit 1
fi

if ! awk '/start_eventbus\(\)/ { start=NR } /start_storage\(\)/ { storage=NR } END { exit !(start < storage) }' "${ROOT}/scripts/deploy/deploy-moox.sh"; then
  echo "eventbus must start before storage" >&2
  exit 1
fi
if ! awk '/stop_service "storage"/ { storage=NR } /stop_service "eventbus"/ { eventbus=NR } END { exit !(storage < eventbus) }' "${ROOT}/scripts/deploy/deploy-moox.sh"; then
  echo "eventbus must stop after storage" >&2
  exit 1
fi
echo "moox-eventbus deployment contract passed"
