#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

production=(--glob '*.go' --glob '*.proto' --glob '*.yaml' --glob '!**/*_test.go' --glob '!docs/**' --glob '!outputs/**')

reject() {
  local pattern=$1
  local message=$2
  shift 2
  if rg -n "$pattern" "$@"; then
    echo "$message" >&2
    exit 1
  fi
}

reject 'streamcalc|TickReceived|MarketKlineClosed|MOOX_MARKET' \
  "legacy market event pipeline remains" "${production[@]}" modules packages
reject 'packages/dlqpb|PublishRejected|MOOX_DLQ|dlq\.message\.rejected' \
  "shared EventBus DLQ remains" "${production[@]}" modules packages
reject 'TradeOrder|TradeExecution|TradeFill|TradeReconciliation|TradeRebalanceCompleted|TradingSignal|t_trade_outbox|withTradeDLQ' \
  "Trade self-consumption contract remains" "${production[@]}" modules/trade modules/strategy packages/events packages/tradeeventpb
reject 'events\.EventType|EventDefinition|EventSchema|AllEventTypes' \
  "legacy event registry API remains" "${production[@]}" modules packages
reject 'NewPullConsumer|EnsurePullConsumer|BindPullConsumer|BindManagedPullConsumer|ConsumerBindRef|ConsumerRef|PullConsumer|PullConsumerAPI|NewConsumerFromPull' \
  "legacy Consumer lifecycle API remains" --glob '*.go' modules packages
reject '^consumers:|^consumer_templates:|ConsumerTemplates' \
  "EventBus still owns Consumer declarations" --glob '*.go' --glob '*.yaml' modules/eventbus
reject '(^|[[:space:]])([A-Za-z0-9_]*_durable|durable):' \
  "business YAML still uses durable as a configuration name" --glob '*.yaml' modules
reject 'PublishRaw\(|Client\.Publish\(' \
  "business modules bypass the typed Event API" --glob '*.go' --glob '!**/*_test.go' modules
reject 'MooxMessage|packages/messagepb|messagepb|moox_message|messagepb\.MessageKind' \
  "legacy MooxMessage symbols remain" "${production[@]}" .
reject 'wrapperspb\.BytesValue|google\.protobuf\.BytesValue' \
  "legacy BytesValue event wrappers remain" --glob '*.go' --glob '*.proto' --glob '!**/*_test.go' .
reject 'c_topic|c_payload' \
  "an outbox still persists split topic or payload columns" modules/strategy/schema/strategy.sql

(cd packages/events && go test ./...)
(cd packages/jetstream && go test ./...)
(cd modules/eventbus && go test ./...)
(cd modules/collector && go test ./internal/sources/binance)
(cd modules/storage && CGO_ENABLED=1 go test ./internal/service/datanode/... ./internal/service/view/...)
(cd modules/monitor && go test ./internal/metrics ./internal/hostmetrics)
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/store ./internal/bus)
(cd modules/trade && CGO_ENABLED=1 go test ./internal/bootstrap ./internal/application/rebalance)

echo "event contract verification passed"
