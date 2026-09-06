#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
CALLER_DEPLOY_ROOT="${MOOX_DEPLOY_ROOT:-}"
if [[ -n "${CALLER_DEPLOY_ROOT}" ]]; then
  printf 'series-tag E2E: MOOX_DEPLOY_ROOT is not supported; this destructive E2E always owns an isolated deployment\n' >&2
  exit 1
fi
DEPLOY_ROOT=""
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-series-tag-e2e.XXXXXX")"
OWN_DEPLOY=0
export HOME="${TMP_ROOT}/home"
# The Factor integration test exercises the real Factor -> Storage -> Strategy
# path, so this deployment always includes Strategy.
# Storage requires an explicit maintenance policy even for an isolated test
# deployment. Keep the default small and deterministic; callers may override
# it when exercising a different View workload.
if [[ -z "${MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64:-}" ]]; then
  export MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64="$(printf '%s' '{"maintenance_check_interval":"1m","rebuild_lookback_periods":1000,"max_periods_per_series":2000,"max_view_file_bytes":1073741824,"system_monitor":{"max_periods_per_series":2000},"views":[]}' | base64 | tr -d '\n')"
fi

cleanup() {
  if [[ "${OWN_DEPLOY}" -eq 1 && -n "${DEPLOY_ROOT}" ]]; then
    if [[ "${MOOX_KEEP_SERIES_TAG_E2E:-0}" != "1" && -x "${DEPLOY_ROOT}/stop.sh" ]]; then
      MOOX_WITH_EVENTBUS=1 MOOX_WITH_STORAGE_NODE=1 MOOX_WITH_ARCHIVE=1 MOOX_WITH_FACTOR=1 \
        MOOX_WITH_MONITOR=1 MOOX_WITH_STRATEGY=1 "${DEPLOY_ROOT}/stop.sh" >/dev/null 2>&1 || true
    fi
    # stop.sh intentionally treats an ownership mismatch as stale. This
    # fallback is still narrowly scoped to commands containing this unique
    # mktemp deployment root, including Python worker children.
    leaked_pids=()
    if [[ "${MOOX_KEEP_SERIES_TAG_E2E:-0}" == "1" ]]; then
      printf 'series-tag E2E: leaving processes running for inspection at %s\n' "${DEPLOY_ROOT}" >&2
      return
    fi
    while IFS= read -r pid; do
      [[ -n "${pid}" ]] && leaked_pids+=("${pid}")
    done < <(ps -axo pid=,command= |
      awk -v root="${DEPLOY_ROOT}" 'index($0, root) {print $1}')
    if (( ${#leaked_pids[@]} > 0 )); then
      kill "${leaked_pids[@]}" 2>/dev/null || true
      sleep 1
      for pid in "${leaked_pids[@]}"; do
        kill -0 "${pid}" 2>/dev/null && kill -9 "${pid}" 2>/dev/null || true
      done
    fi
  fi
	if [[ "${MOOX_KEEP_SERIES_TAG_E2E:-0}" == "1" ]]; then
		printf 'series-tag E2E: keeping failed deployment at %s\n' "${TMP_ROOT}" >&2
	else
		rm -rf "${TMP_ROOT}"
	fi
}
trap cleanup EXIT

fail() {
  printf 'series-tag E2E: %s\n' "$*" >&2
  exit 1
}

if [[ -z "${DEPLOY_ROOT}" ]]; then
  DEPLOY_ROOT="${TMP_ROOT}/deploy"
  OWN_DEPLOY=1
  printf 'series-tag E2E: building isolated local deployment at %s\n' "${DEPLOY_ROOT}"
  control_key="${TMP_ROOT}/gateway-control.key"
  service_key="${TMP_ROOT}/gateway-service.key"
  ca_bundle="${TMP_ROOT}/gateway-peers.pem"
  (umask 077
    openssl rand -hex 32 >"${control_key}"
    openssl rand -hex 32 >"${service_key}"
  )
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -subj /CN=series-tag-e2e-one -keyout /dev/null \
    -out "${TMP_ROOT}/peer-one.pem" >/dev/null 2>&1
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -subj /CN=series-tag-e2e-two -keyout /dev/null \
    -out "${TMP_ROOT}/peer-two.pem" >/dev/null 2>&1
  cat "${TMP_ROOT}/peer-one.pem" "${TMP_ROOT}/peer-two.pem" >"${ca_bundle}"
  deploy_args=(
    --target localhost \
    --dir "${DEPLOY_ROOT}" \
    --stage "${TMP_ROOT}/stage" \
    --reset-data \
    --node-id series-tag-e2e \
    --gateway-control-url http://127.0.0.1:11000 \
    --gateway-control-key-file "${control_key}" \
    --gateway-service-key-file "${service_key}" \
    --gateway-ca-bundle "${ca_bundle}" \
    --monitor-instance-id series-tag-e2e \
    --no-web-host \
    --no-cloudnode \
    --no-collector \
    --no-hostagent \
    --no-trade \
    --local-ca skip \
    --target-ca skip
  )
  if [[ "${MOOX_SERIES_TAG_E2E_SKIP_BUILD:-0}" == "1" ]]; then
    deploy_args+=(--skip-build)
  fi
  env -u GOOS -u GOARCH MOOX_EVENTBUS_ENABLE_TLS=1 \
    "${REPO_ROOT}/scripts/deploy/deploy-moox.sh" "${deploy_args[@]}"
fi
[[ "${DEPLOY_ROOT}" = /* ]] || fail "MOOX_DEPLOY_ROOT must be absolute"
[[ -d "${DEPLOY_ROOT}" ]] || fail "deployment root does not exist: ${DEPLOY_ROOT}"
DEPLOY_ROOT="$(cd "${DEPLOY_ROOT}" && pwd -P)"

required_services=(gateway storage-primary storage-node storage-view factor archive strategy)
for service in "${required_services[@]}"; do
  pid_file="${DEPLOY_ROOT}/run/${service}.pid"
  [[ -f "${pid_file}" ]] || fail "missing ${service} pid file: ${pid_file}"
  pid="$(tr -d '[:space:]' <"${pid_file}")"
  [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null ||
    fail "${service} is not running"
done

for path in \
  "${DEPLOY_ROOT}/bin/moox-factor-run-once" \
  "${DEPLOY_ROOT}/bin/moox-archive" \
  "${DEPLOY_ROOT}/archive/config/app.yaml" \
  "${DEPLOY_ROOT}/secrets/gateway-factor.key" \
  "${DEPLOY_ROOT}/secrets/gateway-moox-cli.key" \
  "${DEPLOY_ROOT}/secrets/gateway-service.env" \
  "${DEPLOY_ROOT}/secrets/storage-internal-auth.env" \
  "${DEPLOY_ROOT}/secrets/storage-node-auth.env" \
  "${DEPLOY_ROOT}/certs/gateway/peers.pem"; do
  [[ -e "${path}" ]] || fail "missing required deployment artifact: ${path}"
done

[[ -e "${DEPLOY_ROOT}/secrets/gateway-strategy.key" ]] ||
  fail "missing required deployment artifact: ${DEPLOY_ROOT}/secrets/gateway-strategy.key"

read_one_line_secret() {
  local path="$1"
  local value
  value="$(cat "${path}"; printf x)"
  value="${value%x}"
  value="${value%$'\n'}"
  [[ -n "${value}" && "${value}" != *$'\n'* && "${value}" != *$'\r'* ]] ||
    fail "${path} must contain exactly one non-empty line"
  printf '%s' "${value}"
}

factor_secret="$(read_one_line_secret "${DEPLOY_ROOT}/secrets/gateway-factor.key")"
cli_secret="$(read_one_line_secret "${DEPLOY_ROOT}/secrets/gateway-moox-cli.key")"
strategy_secret="$(read_one_line_secret "${DEPLOY_ROOT}/secrets/gateway-strategy.key")"
gateway_node_id="$(sed -n 's/^MOOX_GATEWAY_NODE_ID=//p' "${DEPLOY_ROOT}/secrets/gateway-service.env")"
[[ -n "${gateway_node_id}" && "${gateway_node_id}" != *$'\n'* ]] ||
  fail "gateway-service.env has no unique MOOX_GATEWAY_NODE_ID"
storage_primary_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_PRIMARY_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
)"
storage_view_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_VIEW_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
)"
storage_node_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_NODE_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-node-auth.env"
)"
[[ -n "${storage_primary_secret}" && -n "${storage_view_secret}" && -n "${storage_node_secret}" ]] ||
  fail "Storage auth secrets are incomplete"

export MOOX_SERIES_TAG_E2E=1
export MOOX_SERIES_TAG_E2E_TMP_ROOT="${TMP_ROOT}"
export MOOX_DEPLOY_ROOT="${DEPLOY_ROOT}"
export MOOX_FACTOR_STORAGE_E2E=1
export MOOX_FACTOR_STORAGE_E2E_DATA_NODE_ID="${MOOX_STORAGE_NODE_ID:-storage-node-0}"
export MOOX_FACTOR_STORAGE_E2E_DATA_NODE_TARGET="${MOOX_STORAGE_NODE_TARGET:-ip://127.0.0.1:20107}"
export MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET="ip://127.0.0.1:11003"
export MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID="${gateway_node_id}"
export MOOX_SERVICE_GATEWAY_TARGET="ip://127.0.0.1:11003"
export MOOX_GATEWAY_TARGET_NODE="${gateway_node_id}"
export MOOX_GATEWAY_SERVICE_KEY_ID="factor"
export MOOX_GATEWAY_CALLER="factor"
export MOOX_GATEWAY_SERVICE_SECRET_KEY="${factor_secret}"
export MOOX_GATEWAY_CA_FILE="${DEPLOY_ROOT}/certs/gateway/peers.pem"
export MOOX_STORAGE_PRIMARY_AUTH_SECRET="${storage_primary_secret}"
export MOOX_STORAGE_VIEW_AUTH_SECRET="${storage_view_secret}"
export MOOX_STORAGE_NODE_AUTH_SECRET="${storage_node_secret}"
# The isolated deployment enables EventBus TLS. Export the same endpoint and
# Factor credentials to the integration test so its final-ready consumer uses
# the authenticated connection used by the Factor process.
# This script always provisions a TLS EventBus on the isolated loopback port;
# do not inherit a caller's plain-NATS URL from another deployment.
export MOOX_EVENTBUS_NATS_URL="tls://127.0.0.1:4222"
factor_eventbus_credentials="${HOME}/.config/moox/eventbus/factor-eventbus.yaml"
if [[ -r "${factor_eventbus_credentials}" ]]; then
  export MOOX_EVENTBUS_NATS_CREDENTIALS="${factor_eventbus_credentials}"
  factor_eventbus_ca="$(sed -n 's/^ca_file:[[:space:]]*//p' "${factor_eventbus_credentials}" | head -1 | sed 's/[[:space:]]*$//')"
  [[ "${factor_eventbus_ca}" = /* ]] || factor_eventbus_ca="$(cd "$(dirname "${factor_eventbus_credentials}")" && pwd -P)/${factor_eventbus_ca}"
  [[ -n "${factor_eventbus_ca}" && -r "${factor_eventbus_ca}" ]] && export MOOX_EVENTBUS_NATS_TLS_CA_FILE="${factor_eventbus_ca}"
fi
export MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_KEY_ID="moox-cli"
export MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_CALLER="moox-cli"
export MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_SECRET="${cli_secret}"
export MOOX_FACTOR_STORAGE_E2E_STRATEGY_GATEWAY_KEY_ID="strategy"
export MOOX_FACTOR_STORAGE_E2E_STRATEGY_GATEWAY_CALLER="strategy"
export MOOX_FACTOR_STORAGE_E2E_STRATEGY_GATEWAY_SECRET="${strategy_secret}"
export MOOX_ARCHIVE_E2E_ROOT="${DEPLOY_ROOT}/data/archive"
export MOOX_ARCHIVE_E2E_PID_FILE="${DEPLOY_ROOT}/run/archive.pid"

# A fresh deployment may defer activation when Doctor observes the deliberate
# startup race between the empty View and its first reconcile. Activation is
# explicit here and still targets only this script's temporary Metadata v6 DB.
if [[ "${OWN_DEPLOY}" -eq 1 ]]; then
  "${DEPLOY_ROOT}/bin/moox-storage-cli" activate-datasets \
    --metadata-target "ip://127.0.0.1:20100"
fi

printf 'series-tag E2E: isolated temp root %s\n' "${TMP_ROOT}"
(
  cd "${REPO_ROOT}/modules/factor"
  go test -tags=integration ./test \
    -run '^(TestFactorRealStorageE2E|TestFactorViewReadyTrustsUpstreamEvent)$' \
    -count=1 -v
)
(
  cd "${REPO_ROOT}/modules/archive"
  go test ./test \
    -run '^(TestArchiveConsumesUpdatesAndMaterializesMonthlyParquet|TestDeployedArchiveConsumesRealStorageOutbox)$' \
    -count=1 -v
)
if [[ "${MOOX_SERIES_TAG_E2E_WITH_MONITOR:-0}" == "1" ]]; then
  (
    cd "${REPO_ROOT}/modules/monitor"
    go test -tags=integration ./test -run '^TestHostMetricDirectStorageRoundTrip$' -count=1 -v
  )
else
  printf 'series-tag E2E: monitor check skipped (set MOOX_SERIES_TAG_E2E_WITH_MONITOR=1 with a seeded host-metric catalog)\n'
fi
printf 'series-tag E2E: PASS\n'
