#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
PORT=${MOOX_E2E_NATS_PORT:-44222}
WORK_DIR=$(mktemp -d)
NATS_URL="nats://127.0.0.1:${PORT}"
NATS_PID=""
TRADE_PID=""

cleanup() {
  if [[ -n "$TRADE_PID" ]]; then
    kill "$TRADE_PID" 2>/dev/null || true
    wait "$TRADE_PID" 2>/dev/null || true
  fi
  if [[ -n "$NATS_PID" ]]; then
    kill "$NATS_PID" 2>/dev/null || true
    wait "$NATS_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

if nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
  echo "port ${PORT} is already in use" >&2
  exit 1
fi

(
  cd "$ROOT/modules/eventbus"
  go build -o "$WORK_DIR/nats-server" github.com/nats-io/nats-server/v2
)
"$WORK_DIR/nats-server" \
  -js -a 127.0.0.1 -p "$PORT" -sd "$WORK_DIR/jetstream" \
  >"$WORK_DIR/nats.log" 2>&1 &
NATS_PID=$!

for _ in $(seq 1 120); do
  if nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
    break
  fi
  if ! kill -0 "$NATS_PID" 2>/dev/null; then
    cat "$WORK_DIR/nats.log" >&2
    exit 1
  fi
  sleep 0.1
done
if ! nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
  cat "$WORK_DIR/nats.log" >&2
  echo "embedded NATS did not become ready" >&2
  exit 1
fi

export MOOX_STRATEGY_TRADE_E2E_NATS_URL="$NATS_URL"
export MOOX_STRATEGY_TRADE_E2E_COORD_DIR="$WORK_DIR"

STRATEGY_TEST='TestExternalStrategyCommitPublishesLogicalAccountTarget'
TRADE_TEST='TestExternalLogicalAccountTargetIsConsumedIntoTradeStore'

(
  cd "$ROOT/modules/strategy"
  CGO_ENABLED=1 go test -c -tags=e2e_external -o "$WORK_DIR/strategy.test" ./test
)
(
  cd "$ROOT/modules/trade"
  CGO_ENABLED=1 go test -c -tags=e2e_external -o "$WORK_DIR/trade.test" ./test
)

# Trade owns the account/session before Strategy's production authorization
# callback runs. Both test binaries share only the isolated broker and this
# temporary coordination directory, not a database or hand-built event.
"$WORK_DIR/trade.test" -test.v -test.timeout=60s -test.run="^${TRADE_TEST}$" \
  >"$WORK_DIR/trade-test.log" 2>&1 &
TRADE_PID=$!
for _ in $(seq 1 200); do
  if [[ -s "$WORK_DIR/trade-ready" ]]; then
    break
  fi
  if ! kill -0 "$TRADE_PID" 2>/dev/null; then
    cat "$WORK_DIR/trade-test.log" >&2
    exit 1
  fi
  sleep 0.1
done
if [[ ! -s "$WORK_DIR/trade-ready" ]]; then
  cat "$WORK_DIR/trade-test.log" >&2
  echo "Trade account/session did not become ready" >&2
  exit 1
fi
"$WORK_DIR/strategy.test" -test.v -test.timeout=30s -test.run="^${STRATEGY_TEST}$" \
  | tee "$WORK_DIR/strategy-test.log"
grep -Fq -- "--- PASS: ${STRATEGY_TEST}" "$WORK_DIR/strategy-test.log" || {
  echo "strategy external E2E target test did not run" >&2
  exit 1
}

trade_status=0
wait "$TRADE_PID" || trade_status=$?
TRADE_PID=""
cat "$WORK_DIR/trade-test.log"
if [[ "$trade_status" != 0 ]]; then
  exit "$trade_status"
fi
grep -Fq -- "--- PASS: ${TRADE_TEST}" "$WORK_DIR/trade-test.log" || {
  echo "trade external E2E target test did not run" >&2
  exit 1
}

echo "modern Strategy Processor -> NATS -> Trade session receipt -> Paper fill local E2E passed (upstream market data is a fixture)"
