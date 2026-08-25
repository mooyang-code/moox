#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
WORK_DIR=$(mktemp -d)
CONSOLE_PORT=${MOOX_E2E_TRADE_CONSOLE_PORT:-45200}
HEALTH_PORT=${MOOX_E2E_TRADE_HEALTH_PORT:-45210}
TRADE_PID=""

dump_trade_logs() {
  cat "$WORK_DIR/trade.log" >&2 2>/dev/null || true
  cat "$WORK_DIR/log/trpc_trade.log" >&2 2>/dev/null || true
}

cleanup() {
  if [[ -n "$TRADE_PID" ]]; then
    kill "$TRADE_PID" 2>/dev/null || true
    wait "$TRADE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

for port in "$CONSOLE_PORT" "$HEALTH_PORT"; do
  if nc -z 127.0.0.1 "$port" 2>/dev/null; then
    echo "port ${port} is already in use" >&2
    exit 1
  fi
done

mkdir -p "$WORK_DIR/config"
cp "$ROOT/modules/trade/config/app.yaml" "$WORK_DIR/config/app.yaml"
perl -0pi -e \
  's#path: ./data/moox_trade.db#path: ./data/e2e_trade.db#; s/enabled: true/enabled: false/' \
  "$WORK_DIR/config/app.yaml"

cp "$ROOT/modules/trade/config/trpc_go.yaml" "$WORK_DIR/trpc_go.yaml"
perl -0pi -e \
  "s/port: 11200/port: ${CONSOLE_PORT}/; s/port: 11210/port: ${HEALTH_PORT}/g; s/port: 11920/port: 0/; s/port: 12920/port: 0/" \
  "$WORK_DIR/trpc_go.yaml"

(
  cd "$ROOT/modules/trade"
  go build -o "$WORK_DIR/trade-server" ./cmd/server
)
(
  cd "$WORK_DIR"
  export MOOX_HEALTH_AUTH_ACCESS_KEY=monitor
  export MOOX_HEALTH_AUTH_SECRET_KEY=e2e-health-secret
  exec "$WORK_DIR/trade-server" -conf "$WORK_DIR/trpc_go.yaml"
) >"$WORK_DIR/trade.log" 2>&1 &
TRADE_PID=$!

for _ in $(seq 1 120); do
  if nc -z 127.0.0.1 "$CONSOLE_PORT" 2>/dev/null; then
    break
  fi
  if ! kill -0 "$TRADE_PID" 2>/dev/null; then
    dump_trade_logs
    exit 1
  fi
  sleep 0.1
done
if ! nc -z 127.0.0.1 "$CONSOLE_PORT" 2>/dev/null; then
  dump_trade_logs
  echo "TradeConsoleService did not become ready" >&2
  exit 1
fi

export MOOX_STRATEGY_TRADE_RPC_E2E_TARGET="ip://127.0.0.1:${CONSOLE_PORT}"
(
  cd "$ROOT/modules/strategy"
  CGO_ENABLED=1 go test -v -tags=e2e_external -count=1 \
    -run '^TestExternalStrategyClaimsLogicalAccountFromTrade$' \
    ./internal/bootstrap
)

echo "strategy -> TradeConsole logical-account RPC E2E passed"
