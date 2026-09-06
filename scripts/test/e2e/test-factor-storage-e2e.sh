#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
DEPLOY_ROOT="${MOOX_DEPLOY_ROOT:-}"

fail() {
  printf 'factor real Storage E2E: %s\n' "$*" >&2
  exit 1
}

[[ -n "${DEPLOY_ROOT}" ]] || fail "MOOX_DEPLOY_ROOT must name a running local deployment"
[[ "${DEPLOY_ROOT}" = /* ]] || fail "MOOX_DEPLOY_ROOT must be absolute"
[[ -d "${DEPLOY_ROOT}" ]] || fail "deployment root does not exist: ${DEPLOY_ROOT}"
DEPLOY_ROOT="$(cd "${DEPLOY_ROOT}" && pwd -P)"

require_file() {
  [[ -f "$1" ]] || fail "missing required file: $1"
}

require_executable() {
  [[ -x "$1" ]] || fail "missing required executable: $1"
}

require_running_service() {
  local name="$1"
  local pid_file="${DEPLOY_ROOT}/run/${name}.pid"
  require_file "${pid_file}"
  local pid
  pid="$(tr -d '[:space:]' <"${pid_file}")"
  [[ "${pid}" =~ ^[0-9]+$ ]] || fail "${name} has invalid pid file: ${pid_file}"
  kill -0 "${pid}" 2>/dev/null || fail "${name} is not running (pid ${pid})"
}

# The integration test subscribes to the final ViewFactorPeriodReady event.
# Reuse the running Factor process' EventBus URL and the deployment-generated
# Factor credentials instead of relying on the checked-in local defaults.
factor_pid_file="${DEPLOY_ROOT}/run/factor.pid"
factor_eventbus_url=""
if [[ -r "/proc/$(tr -d '[:space:]' <"${factor_pid_file}")/environ" ]]; then
  factor_eventbus_url="$(tr '\0' '\n' <"/proc/$(tr -d '[:space:]' <"${factor_pid_file}")/environ" | sed -n 's/^MOOX_EVENTBUS_NATS_URL=//p' | head -1)"
fi
factor_eventbus_credentials="${HOME}/.config/moox/eventbus/factor-eventbus.yaml"
if [[ -r "${factor_eventbus_credentials}" ]]; then
  # macOS does not expose /proc, so fall back to the role credential file's
  # endpoint instead of silently attempting an unauthenticated nats:// URL.
  if [[ -z "${factor_eventbus_url}" ]]; then
    factor_eventbus_url="$(sed -n 's/^  -[[:space:]]*//p' "${factor_eventbus_credentials}" | head -1)"
  fi
  [[ -n "${factor_eventbus_url}" ]] && export MOOX_EVENTBUS_NATS_URL="${factor_eventbus_url}"
  export MOOX_EVENTBUS_NATS_CREDENTIALS="${factor_eventbus_credentials}"
  factor_eventbus_ca="$(sed -n 's/^ca_file:[[:space:]]*//p' "${factor_eventbus_credentials}" | head -1 | sed 's/[[:space:]]*$//')"
  [[ "${factor_eventbus_ca}" = /* ]] || factor_eventbus_ca="$(cd "$(dirname "${factor_eventbus_credentials}")" && pwd -P)/${factor_eventbus_ca}"
  [[ -n "${factor_eventbus_ca}" && -r "${factor_eventbus_ca}" ]] && export MOOX_EVENTBUS_NATS_TLS_CA_FILE="${factor_eventbus_ca}"
fi

# Preserve a custom EventBus endpoint even for deployments that authenticate
# with no role credential file (for example a local non-TLS broker).
[[ -n "${factor_eventbus_url}" ]] && export MOOX_EVENTBUS_NATS_URL="${factor_eventbus_url}"

for service in gateway storage-primary storage-node storage-view factor strategy; do
  require_running_service "${service}"
done

require_executable "${DEPLOY_ROOT}/bin/moox-factor-run-once"
require_executable "${DEPLOY_ROOT}/bin/moox-factor-cli"
require_file "${DEPLOY_ROOT}/factor/config/app.yaml"
require_file "${DEPLOY_ROOT}/secrets/gateway-factor.key"
require_file "${DEPLOY_ROOT}/secrets/gateway-moox-cli.key"
require_file "${DEPLOY_ROOT}/secrets/gateway-service.env"
require_file "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
require_file "${DEPLOY_ROOT}/secrets/storage-node-auth.env"
require_file "${DEPLOY_ROOT}/certs/gateway/peers.pem"

# Read deployment-owned storage credentials before an optional View restart.
# The package-local start script stops the old process first and otherwise
# cannot recover from a missing environment variable.
storage_primary_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_PRIMARY_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
)"
[[ -n "${storage_primary_secret}" && "${storage_primary_secret}" != *$'\n'* && "${storage_primary_secret}" != *$'\r'* ]] ||
  fail "storage-internal-auth.env must contain one MOOX_STORAGE_PRIMARY_AUTH_SECRET"
storage_view_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_VIEW_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
)"
[[ -n "${storage_view_secret}" && "${storage_view_secret}" != *$'\n'* && "${storage_view_secret}" != *$'\r'* ]] ||
  fail "storage-internal-auth.env must contain one MOOX_STORAGE_VIEW_AUTH_SECRET"
storage_node_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_NODE_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-node-auth.env"
)"
[[ -n "${storage_node_secret}" && "${storage_node_secret}" != *$'\n'* && "${storage_node_secret}" != *$'\r'* ]] ||
  fail "storage-node-auth.env must contain one MOOX_STORAGE_NODE_AUTH_SECRET"
original_storage_eventbus_url="${MOOX_STORAGE_EVENTBUS_URL:-}"
storage_eventbus_url="${original_storage_eventbus_url}"
if [[ -r "/proc/$(tr -d '[:space:]' <"${DEPLOY_ROOT}/run/storage-view.pid")/environ" ]]; then
  original_storage_eventbus_url="$(tr '\0' '\n' <"/proc/$(tr -d '[:space:]' <"${DEPLOY_ROOT}/run/storage-view.pid")/environ" | sed -n 's/^MOOX_STORAGE_EVENTBUS_URL=//p' | head -1)"
  if [[ -n "${original_storage_eventbus_url}" ]]; then
    storage_eventbus_url="${original_storage_eventbus_url}"
  fi
fi
if [[ -z "${storage_eventbus_url}" ]]; then
  storage_eventbus_url="${factor_eventbus_url:-}"
fi
storage_view_pid="$(tr -d '[:space:]' <"${DEPLOY_ROOT}/run/storage-view.pid")"
original_storage_allowed_spaces="${MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES:-}"
if [[ -r "/proc/${storage_view_pid}/environ" ]]; then
  original_storage_allowed_spaces="$(tr '\0' '\n' </proc/${storage_view_pid}/environ | sed -n 's/^MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES=//p' | head -1)"
fi

# View wildcard discovery is fixed at process startup. A caller that owns the
# local deployment may opt into a controlled storage-view restart so the
# temporary E2E space is in its explicit allow-list. Without that opt-in, the
# caller must provide a preconfigured Space already present in the running
# View allow-list; fail fast instead of creating metadata that cannot be
# consumed by the current process.
restart_storage_view="${MOOX_FACTOR_STORAGE_E2E_RESTART_STORAGE_VIEW:-0}"
configured_space_id="${MOOX_FACTOR_STORAGE_E2E_SPACE_ID:-}"
if [[ -n "${configured_space_id}" ]]; then
  e2e_space_id="${configured_space_id}"
elif [[ "${restart_storage_view}" == "1" ]]; then
  # BSD date does not implement GNU's %N nanosecond formatter, so combine
  # epoch seconds, PID and RANDOM for a sufficiently unique local test ID.
  e2e_space_id="factor_e2e_$(date +%s)_$$_${RANDOM}"
else
  fail "MOOX_FACTOR_STORAGE_E2E_SPACE_ID is required unless MOOX_FACTOR_STORAGE_E2E_RESTART_STORAGE_VIEW=1"
fi
e2e_allowed_spaces="${MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES:-${original_storage_allowed_spaces:-}}"
start_storage_view() {
  local allowed_spaces="$1" eventbus_url="$2"
  if [[ -r "${storage_view_credential_file}" ]]; then
    env -u MOOX_EVENTBUS_NATS_CREDENTIALS \
      MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE="${storage_view_credential_file}" \
      MOOX_STORAGE_EVENTBUS_URL="${eventbus_url}" \
      MOOX_STORAGE_PRIMARY_AUTH_SECRET="${storage_primary_secret}" \
      MOOX_STORAGE_VIEW_AUTH_SECRET="${storage_view_secret}" \
      MOOX_STORAGE_NODE_AUTH_SECRET="${storage_node_secret}" \
      MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES="${allowed_spaces}" \
      "${storage_view_command[@]}"
  else
    env -u MOOX_EVENTBUS_NATS_CREDENTIALS \
      MOOX_STORAGE_EVENTBUS_URL="${eventbus_url}" \
      MOOX_STORAGE_PRIMARY_AUTH_SECRET="${storage_primary_secret}" \
      MOOX_STORAGE_VIEW_AUTH_SECRET="${storage_view_secret}" \
      MOOX_STORAGE_NODE_AUTH_SECRET="${storage_node_secret}" \
      MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES="${allowed_spaces}" \
      "${storage_view_command[@]}"
  fi
}
storage_view_health_url="${MOOX_STORAGE_VIEW_HEALTH_URL:-http://127.0.0.1:20211/readyz}"
storage_view_health_auth() {
  [[ -r "${DEPLOY_ROOT}/secrets/health-auth.env" ]] || return 1
  (
    set -a
    source "${DEPLOY_ROOT}/secrets/health-auth.env"
    set +a
    local timestamp nonce body_hash canonical signature
    timestamp="$(date +%s)"
    nonce="$(openssl rand -hex 32)"
    body_hash="$(printf '' | openssl dgst -sha256 | awk '{print $NF}')"
    canonical="$(printf 'moox-request-v1\nGET\n/readyz\n%s\n%s\n%s' "${body_hash}" "${timestamp}" "${nonce}")"
    signature="$(printf '%s' "${canonical}" | openssl dgst -sha256 -hmac "${MOOX_HEALTH_AUTH_SECRET_KEY}" | awk '{print $NF}')"
    printf '%s/%s/%s/%s/%s' "${MOOX_HEALTH_AUTH_VERSION}" "${MOOX_HEALTH_AUTH_ACCESS_KEY}" "${timestamp}" "${nonce}" "${signature}"
  )
}
storage_view_ready() {
  local auth_header=""
  if [[ -r "${DEPLOY_ROOT}/secrets/health-auth.env" ]]; then
    auth_header="$(storage_view_health_auth)" || return 1
  fi
  if [[ -n "${auth_header}" ]]; then
    curl --fail --silent --show-error --max-time 2 -H "X-Moox-Health-Auth: ${auth_header}" "${storage_view_health_url}" >/dev/null
  else
    curl --fail --silent --show-error --max-time 2 "${storage_view_health_url}" >/dev/null
  fi
}
wait_storage_view_ready() {
  local timeout_seconds="${MOOX_FACTOR_STORAGE_VIEW_READY_TIMEOUT_SECONDS:-60}"
  for _ in $(seq 1 "${timeout_seconds}"); do
    if storage_view_ready; then
      return 0
    fi
    sleep 1
  done
  return 1
}
restore_storage_view() {
  local restore_url="${original_storage_eventbus_url:-${storage_eventbus_url}}"
  echo "restoring storage-view after failed E2E restart" >&2
  if ! start_storage_view "${original_storage_allowed_spaces}" "${restore_url}"; then
    return 1
  fi
  if ! wait_storage_view_ready; then
    return 1
  fi
  return 0
}
if [[ "${restart_storage_view}" == "1" ]]; then
  [[ -n "${storage_eventbus_url}" ]] || fail "MOOX_STORAGE_EVENTBUS_URL is required when restarting storage-view"
  if [[ -n "${e2e_allowed_spaces}" ]]; then
    # Preserve operator-provided entries while ensuring this run's Space is
    # actually consumed by the freshly started View process.
    e2e_allowed_spaces="${e2e_allowed_spaces},${e2e_space_id}"
  else
    e2e_allowed_spaces="${e2e_space_id}"
  fi
  storage_view_start="${DEPLOY_ROOT}/storage-view/start.sh"
  if [[ -x "${storage_view_start}" ]]; then
    storage_view_command=("${storage_view_start}")
  elif [[ -x "${DEPLOY_ROOT}/start.sh" ]]; then
    storage_view_command=("${DEPLOY_ROOT}/start.sh" storage-view)
  else
    fail "deployment has no storage-view start command"
  fi
  storage_view_credential_file="${HOME}/.config/moox/eventbus/storage-eventbus.yaml"
  restart_error=""
  if ! start_storage_view "${e2e_allowed_spaces}" "${storage_eventbus_url}"; then
    restart_error="storage-view start command failed"
  elif ! current_storage_view_pid="$(tr -d '[:space:]' <"${DEPLOY_ROOT}/run/storage-view.pid")"; then
    restart_error="storage-view pid file is unreadable"
  elif ! kill -0 "${current_storage_view_pid}" 2>/dev/null; then
    restart_error="storage-view did not leave a running process"
  elif ! wait_storage_view_ready; then
    restart_error="storage-view readiness probe failed"
  fi
  if [[ -n "${restart_error}" ]]; then
    if restore_storage_view; then
      fail "${restart_error}; original storage-view was restored"
    fi
    fail "${restart_error}; original storage-view could not be restored"
  fi
else
  [[ -n "${e2e_allowed_spaces}" ]] || fail "MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES must include ${e2e_space_id} when storage-view restart is disabled"
  space_is_allowed=0
  IFS=',' read -r -a configured_allowed_spaces <<<"${e2e_allowed_spaces}"
  for raw_space in "${configured_allowed_spaces[@]}"; do
    raw_space="${raw_space#${raw_space%%[![:space:]]*}}"
    raw_space="${raw_space%${raw_space##*[![:space:]]}}"
    if [[ "${raw_space}" == "${e2e_space_id}" ]]; then
      space_is_allowed=1
      break
    fi
  done
  [[ "${space_is_allowed}" == "1" ]] || fail "Space ${e2e_space_id} is not in MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES; preconfigure it or set MOOX_FACTOR_STORAGE_E2E_RESTART_STORAGE_VIEW=1"
fi

secret_raw="$(cat "${DEPLOY_ROOT}/secrets/gateway-factor.key"; printf x)"
secret_raw="${secret_raw%x}"
if [[ "${secret_raw}" == *$'\n' ]]; then
  secret="${secret_raw%$'\n'}"
else
  secret="${secret_raw}"
fi
[[ -n "${secret}" && "${secret}" != *$'\n'* && "${secret}" != *$'\r'* ]] ||
  fail "gateway factor secret must contain exactly one non-empty line"
factor_mgr_secret_raw="$(cat "${DEPLOY_ROOT}/secrets/gateway-moox-cli.key"; printf x)"
factor_mgr_secret_raw="${factor_mgr_secret_raw%x}"
if [[ "${factor_mgr_secret_raw}" == *$'\n' ]]; then
  factor_mgr_secret="${factor_mgr_secret_raw%$'\n'}"
else
  factor_mgr_secret="${factor_mgr_secret_raw}"
fi
[[ -n "${factor_mgr_secret}" && "${factor_mgr_secret}" != *$'\n'* && "${factor_mgr_secret}" != *$'\r'* ]] ||
  fail "gateway moox-cli secret must contain exactly one non-empty line"
gateway_node_id="$(
  sed -n 's/^MOOX_GATEWAY_NODE_ID=//p' "${DEPLOY_ROOT}/secrets/gateway-service.env"
)"
[[ -n "${gateway_node_id}" ]] || fail "gateway-service.env is missing MOOX_GATEWAY_NODE_ID"
[[ "${gateway_node_id}" != *$'\n'* && "${gateway_node_id}" != *$'\r'* ]] ||
  fail "gateway-service.env contains multiple or malformed MOOX_GATEWAY_NODE_ID values"
storage_primary_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_PRIMARY_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
)"
[[ -n "${storage_primary_secret}" && "${storage_primary_secret}" != *$'\n'* && "${storage_primary_secret}" != *$'\r'* ]] ||
  fail "storage-internal-auth.env must contain one MOOX_STORAGE_PRIMARY_AUTH_SECRET"
storage_view_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_VIEW_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
)"
[[ -n "${storage_view_secret}" && "${storage_view_secret}" != *$'\n'* && "${storage_view_secret}" != *$'\r'* ]] ||
  fail "storage-internal-auth.env must contain one MOOX_STORAGE_VIEW_AUTH_SECRET"
storage_node_secret="$(
  bash -c 'set -u; source "$1"; printf "%s" "${MOOX_STORAGE_NODE_AUTH_SECRET-}"' \
    _ "${DEPLOY_ROOT}/secrets/storage-node-auth.env"
)"
[[ -n "${storage_node_secret}" && "${storage_node_secret}" != *$'\n'* && "${storage_node_secret}" != *$'\r'* ]] ||
  fail "storage-node-auth.env must contain one MOOX_STORAGE_NODE_AUTH_SECRET"

(
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
  export MOOX_GATEWAY_SERVICE_SECRET_KEY="${secret}"
  export MOOX_GATEWAY_CA_FILE="${DEPLOY_ROOT}/certs/gateway/peers.pem"
  export MOOX_STORAGE_PRIMARY_AUTH_SECRET="${storage_primary_secret}"
  export MOOX_STORAGE_VIEW_AUTH_SECRET="${storage_view_secret}"
  export MOOX_STORAGE_NODE_AUTH_SECRET="${storage_node_secret}"
  export MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_KEY_ID="moox-cli"
  export MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_CALLER="moox-cli"
  export MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_SECRET="${factor_mgr_secret}"
  export MOOX_FACTOR_STORAGE_E2E_SPACE_ID="${e2e_space_id}"
  export MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES="${e2e_allowed_spaces}"

  cd "${REPO_ROOT}/modules/factor"
  go test -tags=integration ./test -run '^TestFactorRealStorageE2E$' -count=1 -v
)
