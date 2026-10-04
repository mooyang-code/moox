#!/usr/bin/env bash
set -euo pipefail

# Three isolated processes, ephemeral loopback listeners and test-local TLS CA.
# TLS verification stays enabled; no deployment or NATS is involved.
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/moox-gateway-owner-e2e.XXXXXX")
pids=()
cleanup() {
  local pid
  for pid in "${pids[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  for pid in "${pids[@]:-}"; do
    [[ -n "$pid" ]] && wait "$pid" 2>/dev/null || true
  done
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export MOOX_GATEWAY_OWNER_E2E_COORD="$WORK"
export MOOX_GATEWAY_SERVICE_KEY_ID="gateway-owner-e2e"
export MOOX_GATEWAY_CALLER="strategy"
export MOOX_GATEWAY_SERVICE_SECRET_KEY="isolated-local-gateway-owner-e2e-secret"

(cd "$ROOT/modules/trade" && go test -tags=e2e_external -c -o "$WORK/trade.test" ./test)
(cd "$ROOT/modules/gateway" && go test -tags=e2e_external -c -o "$WORK/gateway.test" ./internal/router)
(cd "$ROOT/modules/strategy" && go test -tags=e2e_external -c -o "$WORK/strategy.test" ./internal/bootstrap)

wait_ready() {
  local file=$1 pid=$2 log=$3
  for ((i=0; i<400; i++)); do
    [[ -s "$WORK/$file" ]] && return 0
    if ! kill -0 "$pid" 2>/dev/null; then cat "$log"; return 1; fi
    sleep 0.05
  done
  cat "$log"
  printf 'Timed out waiting for %s\n' "$file" >&2
  return 1
}
"$WORK/trade.test" -test.run '^TestExternalGatewayOwnerTrade$' -test.v -test.timeout 60s >"$WORK/trade.log" 2>&1 &
trade_pid=$!
pids+=("$trade_pid")
wait_ready trade-ready "$trade_pid" "$WORK/trade.log"
"$WORK/gateway.test" -test.run '^TestExternalGatewayOwnerHandler$' -test.v -test.timeout 60s >"$WORK/gateway.log" 2>&1 &
gateway_pid=$!
pids+=("$gateway_pid")
wait_ready gateway-ready "$gateway_pid" "$WORK/gateway.log"
"$WORK/strategy.test" -test.run '^TestExternalStrategyGatewayOwnerClient$' -test.v -test.timeout 30s >"$WORK/strategy.log" 2>&1 &
strategy_pid=$!
pids+=("$strategy_pid")
status=0
wait "$strategy_pid" || status=1
pids=("$trade_pid" "$gateway_pid")
cat "$WORK/strategy.log"
if [[ "$status" != 0 ]]; then
  cat "$WORK/trade.log" "$WORK/gateway.log"
  exit 1
fi
status=0
wait "$gateway_pid" || status=1
pids=("$trade_pid")
wait "$trade_pid" || status=1
pids=()
cat "$WORK/trade.log" "$WORK/gateway.log"
[[ "$status" == 0 ]] || exit "$status"
printf 'PASS: local production Strategy client -> trusted TLS Gateway handler/Forward -> Trade HTTP RPC + Paper Store ownership. No order execution or deployment claimed.\n'
