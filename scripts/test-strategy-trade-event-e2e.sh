#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PORT=${MOOX_E2E_NATS_PORT:-44222}
WORK_DIR=$(mktemp -d)
NATS_URL="nats://127.0.0.1:${PORT}"
NATS_PID=""

cleanup() {
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
  exec go run github.com/nats-io/nats-server/v2 \
    -js -a 127.0.0.1 -p "$PORT" -sd "$WORK_DIR/jetstream"
) >"$WORK_DIR/nats.log" 2>&1 &
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

(
  cd "$ROOT/modules/strategy"
  CGO_ENABLED=1 go test -count=1 -run '^TestExternalStrategyCommitPublishesRebalance$' ./test
)
(
  cd "$ROOT/modules/trade"
  CGO_ENABLED=1 go test -count=1 -run '^TestExternalStrategyRebalanceEventCreatesOneRunAndWakesWorker$' ./internal/bootstrap
)

echo "strategy -> eventbus -> trade E2E passed"
