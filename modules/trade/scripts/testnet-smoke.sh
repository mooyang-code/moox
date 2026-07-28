#!/usr/bin/env bash
set -euo pipefail

die() {
  echo "trade testnet smoke: $*" >&2
  exit 1
}

[[ "${MOOX_TRADE_TESTNET:-}" == "1" ]] ||
  die "refusing to run without MOOX_TRADE_TESTNET=1"

exchange_name=${MOOX_TRADE_TESTNET_EXCHANGE:-}
case "$exchange_name" in
  binance|okx) ;;
  *) die "MOOX_TRADE_TESTNET_EXCHANGE must be binance or okx" ;;
esac

[[ -n "${MOOX_TRADE_TESTNET_API_KEY:-}" ]] ||
  die "MOOX_TRADE_TESTNET_API_KEY is required"
[[ -n "${MOOX_TRADE_TESTNET_API_SECRET:-}" ]] ||
  die "MOOX_TRADE_TESTNET_API_SECRET is required"
[[ -n "${MOOX_TRADE_TESTNET_SYMBOL:-}" ]] ||
  die "MOOX_TRADE_TESTNET_SYMBOL is required"
[[ -n "${MOOX_TRADE_TESTNET_SWAP_SYMBOL:-}" ]] ||
  die "MOOX_TRADE_TESTNET_SWAP_SYMBOL is required"
if [[ "$exchange_name" == "okx" ]]; then
  [[ -n "${MOOX_TRADE_TESTNET_PASSPHRASE:-}" ]] ||
    die "MOOX_TRADE_TESTNET_PASSPHRASE is required for OKX"
  [[ "${MOOX_TRADE_TESTNET_OKX_SIMULATED:-}" == "1" ]] ||
    die "MOOX_TRADE_TESTNET_OKX_SIMULATED=1 is required for OKX demo trading"
fi

endpoint=${MOOX_TRADE_TESTNET_ENDPOINT:-}
if [[ -n "$endpoint" ]]; then
  case "$exchange_name:$endpoint" in
    binance:https://testnet.binance.vision*|\
    binance:https://demo-fapi.binance.com*|\
    okx:https://www.okx.com*) ;;
    *) die "endpoint is not an allowlisted ${exchange_name} testnet endpoint" ;;
  esac
fi

client_prefix="moox-testnet-$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
created_orders=()

cleanup() {
  if ((${#created_orders[@]} != 0)); then
    echo "trade testnet smoke: cleanup is unavailable; manual cancellation required for ${created_orders[*]}" >&2
  fi
}
trap cleanup EXIT INT TERM

# The current Trade CLI/RPC has no credential-safe, testnet-endpoint-pinned
# command that can perform and clean up this sequence. Keep the harness closed
# until that interface exists; never improvise direct production-capable HTTP.
echo "trade testnet smoke: preflight passed for ${exchange_name} (${client_prefix})" >&2
die "order execution is disabled until a testnet-only CLI/RPC cleanup path is implemented"
