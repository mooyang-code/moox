#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

require_file() {
  [[ -f "$1" ]] || {
    echo "missing required file: $1" >&2
    exit 1
  }
}

require_executable() {
  [[ -x "$1" ]] || {
    echo "missing required executable: $1" >&2
    exit 1
  }
}

require_file "${ROOT}/factor/config/app.yaml"
require_executable "${ROOT}/bin/moox-factor-cli"
require_executable "${ROOT}/data/factor/venv/bin/python"
require_file "${ROOT}/factor/pyworker/worker.py"
[[ -d "${ROOT}/factor/factors" ]] || {
  echo "missing factor directory: ${ROOT}/factor/factors" >&2
  exit 1
}
require_file "${ROOT}/secrets/gateway-factor.key"
require_file "${ROOT}/secrets/gateway-service.env"
require_file "${ROOT}/certs/gateway/peers.pem"

secret_raw="$(cat "${ROOT}/secrets/gateway-factor.key"; printf x)"
secret_raw="${secret_raw%x}"
if [[ "${secret_raw}" == *$'\n' ]]; then
  secret="${secret_raw%$'\n'}"
else
  secret="${secret_raw}"
fi
if [[ -z "${secret}" || "${secret}" == *$'\n'* || "${secret}" == *$'\r'* ]]; then
  echo "gateway factor secret must contain exactly one non-empty line" >&2
  exit 1
fi

gateway_node_id="$(
  sed -n 's/^MOOX_GATEWAY_NODE_ID=//p' "${ROOT}/secrets/gateway-service.env"
)"
if [[ -z "${gateway_node_id}" || "${gateway_node_id}" == *$'\n'* || "${gateway_node_id}" == *$'\r'* ]]; then
  echo "gateway-service.env must contain exactly one non-empty MOOX_GATEWAY_NODE_ID" >&2
  exit 1
fi

export MOOX_FACTOR_DB_PATH="${ROOT}/data/factor/factor.db"
export MOOX_FACTOR_ENGINE_PYTHON_BIN="${ROOT}/data/factor/venv/bin/python"
export MOOX_FACTOR_ENGINE_WORKER_PATH="${ROOT}/factor/pyworker/worker.py"
export MOOX_FACTOR_ENGINE_FACTORS_DIR="${ROOT}/factor/factors"
export MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET="ip://127.0.0.1:11003"
export MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID="${gateway_node_id}"
export MOOX_GATEWAY_SERVICE_KEY_ID="factor"
export MOOX_GATEWAY_CALLER="factor"
export MOOX_GATEWAY_SERVICE_SECRET_KEY="${secret}"
export MOOX_GATEWAY_CA_FILE="${ROOT}/certs/gateway/peers.pem"

exec "${ROOT}/bin/moox-factor-cli" run-once \
  --config "${ROOT}/factor/config/app.yaml" \
  "$@"
