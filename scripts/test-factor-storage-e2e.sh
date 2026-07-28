#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
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

for service in gateway storage-primary storage-node storage-view factor; do
  require_running_service "${service}"
done

require_executable "${DEPLOY_ROOT}/bin/moox-factor-run-once"
require_executable "${DEPLOY_ROOT}/bin/moox-factor-cli"
require_file "${DEPLOY_ROOT}/factor/config/app.yaml"
require_file "${DEPLOY_ROOT}/secrets/gateway-factor.key"
require_file "${DEPLOY_ROOT}/secrets/gateway-service.env"
require_file "${DEPLOY_ROOT}/secrets/storage-internal-auth.env"
require_file "${DEPLOY_ROOT}/certs/gateway/peers.pem"

secret_raw="$(cat "${DEPLOY_ROOT}/secrets/gateway-factor.key"; printf x)"
secret_raw="${secret_raw%x}"
if [[ "${secret_raw}" == *$'\n' ]]; then
  secret="${secret_raw%$'\n'}"
else
  secret="${secret_raw}"
fi
[[ -n "${secret}" && "${secret}" != *$'\n'* && "${secret}" != *$'\r'* ]] ||
  fail "gateway factor secret must contain exactly one non-empty line"

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

(
  export MOOX_DEPLOY_ROOT="${DEPLOY_ROOT}"
  export MOOX_FACTOR_STORAGE_E2E=1
  export MOOX_FACTOR_STORAGE_E2E_DATA_NODE_ID="${MOOX_STORAGE_NODE_ID:-storage-node-0}"
  export MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET="ip://127.0.0.1:11003"
  export MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID="${gateway_node_id}"
  export MOOX_SERVICE_GATEWAY_TARGET="ip://127.0.0.1:11003"
  export MOOX_GATEWAY_TARGET_NODE="${gateway_node_id}"
  export MOOX_GATEWAY_SERVICE_KEY_ID="factor"
  export MOOX_GATEWAY_CALLER="factor"
  export MOOX_GATEWAY_SERVICE_SECRET_KEY="${secret}"
  export MOOX_GATEWAY_CA_FILE="${DEPLOY_ROOT}/certs/gateway/peers.pem"
  export MOOX_STORAGE_PRIMARY_AUTH_SECRET="${storage_primary_secret}"

  cd "${REPO_ROOT}/modules/factor"
  go test -tags=integration ./test -run '^TestFactorRealStorageE2E$' -count=1 -v
)
