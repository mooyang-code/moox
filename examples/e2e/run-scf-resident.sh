#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf '[e2e-scf] ERROR: %s\n' "$*" >&2
  exit 1
}

[[ $# -eq 3 ]] || fail "usage: run-scf-resident.sh <deploy-dir> <collector-node-id> <space-id>"
deploy=$1
collector_node_id=$2
MOOX_SPACE_ID=$3
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac

gateway_env="${deploy}/secrets/gateway-service.env"
collector_gateway_key="${deploy}/secrets/gateway-collector.key"
ca_file="${deploy}/certs/gateway/peers.pem"
[[ -r "${gateway_env}" ]] || fail "missing gateway-service.env"
[[ -r "${collector_gateway_key}" ]] || fail "missing gateway-collector.key"
[[ -r "${ca_file}" ]] || fail "missing Gateway CA bundle"
[[ -x "${deploy}/bin/moox-collector-scf" ]] || fail "missing moox-collector-scf"

set -a
# shellcheck disable=SC1090
source "${gateway_env}"
set +a
MOOX_GATEWAY_SERVICE_KEY_ID=collector
MOOX_GATEWAY_SERVICE_SECRET_KEY="$(tr -d '\r\n' <"${collector_gateway_key}")"
MOOX_GATEWAY_TARGET_NODE="${MOOX_GATEWAY_NODE_ID:-}"
MOOX_RUNTIME_NODE_ID="${collector_node_id}"
MOOX_SERVICE_GATEWAY_TARGET="http://127.0.0.1:11002"
MOOX_STORAGE_RPC_GATEWAY_TARGET="127.0.0.1:11003"
export MOOX_GATEWAY_SERVICE_KEY_ID MOOX_GATEWAY_SERVICE_SECRET_KEY MOOX_GATEWAY_TARGET_NODE
export MOOX_RUNTIME_NODE_ID MOOX_SERVICE_GATEWAY_TARGET MOOX_STORAGE_RPC_GATEWAY_TARGET
[[ -n "${MOOX_GATEWAY_NODE_ID:-}" ]] || fail "missing MOOX_GATEWAY_NODE_ID"
[[ -n "${MOOX_GATEWAY_SERVICE_SECRET_KEY:-}" ]] || fail "missing MOOX_GATEWAY_SERVICE_SECRET_KEY"

MOOX_GATEWAY_CA_PEM_B64=$(base64 <"${ca_file}" | tr -d '\r\n')
[[ -n "${MOOX_GATEWAY_CA_PEM_B64}" ]] || fail "Gateway CA bundle is empty"
unset MOOX_GATEWAY_CA_FILE
export MOOX_GATEWAY_CA_PEM_B64 MOOX_SPACE_ID

cd "${deploy}"
exec "${deploy}/bin/moox-collector-scf" \
  -resident \
  -service-gateway-target "${MOOX_SERVICE_GATEWAY_TARGET}" \
  -node-id "${collector_node_id}" \
  -storage-rpc-gateway-target "${MOOX_STORAGE_RPC_GATEWAY_TARGET}"
