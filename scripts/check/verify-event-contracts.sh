#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$ROOT"

bash scripts/test/contract/test-trade-exchange-terminology.sh

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

# Timer-triggered market fetch configuration deliberately uses the
# MOOX_MARKET_FETCH_* environment-variable prefix.  Do not treat that prefix
# as an EventBus stream name: the timer path writes Storage directly and does
# itself neither starts nor depends on the completion consumer. Keep the
# unrelated legacy market event symbols guarded here.
reject 'streamcalc|TickReceived|MarketKlineClosed' \
  "legacy market event pipeline remains" "${production[@]}" modules packages
# The bounded Invoke path for Symbol snapshots and catch-up still uses the
# completion consumer. Timer-triggered realtime K-lines skip publication and
# derive freshness from Storage, so this is not a realtime EventBus dependency.
reject 'packages/dlqpb|PublishRejected|MOOX_DLQ|dlq\.message\.rejected' \
  "shared EventBus DLQ remains" "${production[@]}" modules packages
reject 'TradeOrder|\bTradeExecution\b|TradeFill|TradeReconciliation|TradeRebalanceCompleted|TradeRebalanceRequested|RebalanceRequested|RebalanceTarget|trade\.rebalance\.requested|TradingSignal|t_trade_outbox|withTradeDLQ' \
  "Trade self-consumption contract remains" "${production[@]}" modules/trade modules/strategy packages/events packages/tradeeventpb
reject 'events\.EventType|EventDefinition|EventSchema|AllEventTypes' \
  "legacy event registry API remains" "${production[@]}" modules packages
reject 'NewPullConsumer|EnsurePullConsumer|BindPullConsumer|BindManagedPullConsumer|ConsumerBindRef|ConsumerRef|PullConsumer|PullConsumerAPI|NewConsumerFromPull' \
  "legacy Consumer lifecycle API remains" --glob '*.go' modules packages
reject '^consumers:|^consumer_templates:|ConsumerTemplates' \
  "EventBus still owns Consumer declarations" --glob '*.go' --glob '*.yaml' modules/eventbus
# Storage owns these static consumer partitions, whose durable identities are
# part of the Storage runtime topology rather than business YAML contracts.
reject '(^|[[:space:]])([A-Za-z0-9_]*_durable|durable):' \
  "business YAML still uses durable as a configuration name" \
  --glob '*.yaml' --glob '!modules/storage/config/storage.yaml' \
  --glob '!modules/storage/config/storage_view/trpc_go.yaml' modules
reject 'PublishRaw\(|Client\.Publish\(' \
  "business modules bypass the typed Event API" --glob '*.go' --glob '!**/*_test.go' modules
reject '\.NewConsumer\(' \
  "CloudNode bypasses the Registry-owned event Consumer API" \
  --glob '*.go' --glob '!**/*_test.go' modules/cloudnode
reject 'MooxMessage|packages/messagepb|messagepb|moox_message|messagepb\.MessageKind' \
  "legacy MooxMessage symbols remain" "${production[@]}" .
reject 'wrapperspb\.BytesValue|google\.protobuf\.BytesValue' \
  "legacy BytesValue event wrappers remain" --glob '*.go' --glob '*.proto' --glob '!**/*_test.go' .
reject 'c_topic|c_payload' \
  "an outbox still persists split topic or payload columns" modules/strategy/schema/strategy.sql
reject 'strategy_run_id|strategy_result_id|execution_id|execution_binding_id|exchange_account_id|data_revision|not_after|(^|[^A-Za-z0-9_])symbol([^A-Za-z0-9_]|$)|TargetIntent|TargetPosition|TradeTarget|TradeTargetRequested|target_quantity' \
  "Strategy target publisher or public event still uses the obsolete target contract" \
  "${production[@]}" \
  modules/strategy/internal/store/results.go \
  modules/strategy/internal/outbox \
  modules/strategy/internal/rpc/service.go \
  packages/events/registry.go \
  packages/events/validation.go \
  packages/tradeeventpb/trade_events.proto
reject 'strategy_run_id|strategy_result_id|execution_id|execution_binding_id|data_revision|not_after|TargetIntent|TargetPosition|TradeTarget|TradeTargetRequested|target_quantity' \
  "Trade target consumer or persisted LogicalAccount target still uses the obsolete target contract" \
  "${production[@]}" \
  modules/trade/internal/domain \
  modules/trade/internal/eventconsumer/target.go \
  modules/trade/internal/infra/store/target.go \
  modules/trade/internal/runtime/target_worker.go
reject 'NATSURL|StreamName|SubjectPrefix|MaxMsgs|MaxInFlight|MaxDeliver|StorageEmbeddedEventBus' \
  "Storage still exposes EventBus topology or compatibility settings" \
  --glob '*.go' modules/storage/internal/config modules/storage/cmd/server/main.go
reject '^[[:space:]]+(type|urls|nats_url|stream_name|subject_prefix|max_msgs|max_in_flight|max_deliver|embedded):' \
  "Storage YAML still exposes EventBus topology or compatibility settings" \
  --glob '*.yaml' modules/storage/config
reject '^[[:space:]]+(stream|topic):' \
  "Monitor still exposes Registry-owned stream/topic settings" \
  --glob '*.yaml' modules/monitor/config
reject 'EventBus\.Stream|yaml:"stream"' \
  "Archive still exposes the Registry-owned stream setting" \
  --glob '*.go' modules/archive/internal
reject 'NATS\.Stream|NATS\.Consumer|NATS\.URL\b|yaml:"stream"|yaml:"consumer"|yaml:"url"' \
  "Factor still exposes duplicate or fixed live EventBus settings" \
  --glob '*.go' modules/factor/internal
reject '^[[:space:]]+(stream|consumer|url):' \
  "Factor YAML still exposes duplicate or fixed live EventBus settings" \
  --glob '*.yaml' modules/factor/config
# FactorTypeCrossSection remains a supported calculation type; only its retired
# persistence and recalculation APIs belong in this removal guard.
reject 'NewDurableEventBatcher|PendingEventStore|t_factor_event_inbox|arrow_mmap|GetRecalcProgress|ListFactorRuns' \
  "removed Factor capability remains" \
  --glob '*.go' --glob '*.proto' --glob '*.py' --glob '!**/*_test.go' \
  modules/factor/internal modules/factor/proto modules/factor/cmd modules/factor/pyworker
reject 'KLineColumns|DependsFromSource|DefaultLookback|signal_multi_params|c_periods_json|c_depends_json' \
  "legacy OHLCV/period Factor contract remains" \
  --glob '*.go' --glob '*.proto' --glob '*.py' --glob '*.sql' --glob '!**/*_test.go' \
  examples/factors modules/factor/internal modules/factor/proto modules/factor/schema \
  modules/factor/cmd modules/factor/pyworker
reject 'NATSURL|EmbeddedJetStreamConfig|yaml:"nats_url"|yaml:"embedded"' \
  "CloudNode still exposes duplicate or embedded JetStream settings" \
  --glob '*.go' modules/cloudnode/internal
reject '^[[:space:]]+(consumer|rebalance_consumer|max_ack_pending|ack_wait|ack_wait_ms):' \
  "code-owned Consumer identity or ack settings remain in business YAML" \
  --glob '*.yaml' \
  --glob '!modules/storage/config/storage.yaml' \
  --glob '!modules/storage/config/storage_view/trpc_go.yaml' \
  modules/archive/config modules/monitor/config modules/trade/config modules/storage/config

(cd packages/events && go test ./...)
(cd packages/jetstream && go test ./...)
(cd modules/eventbus && go test ./...)
(cd modules/collector && go test ./internal/sources/binance)
(cd modules/storage && CGO_ENABLED=1 go test ./internal/config ./cmd/server ./internal/eventmapper ./internal/service/datanode/... ./internal/service/view/... ./internal/service/e2e)
(cd modules/monitor && go test ./internal/config ./internal/metrics/... ./internal/hostmetrics/... ./test)
(cd modules/archive && go test ./internal/config ./internal/eventconsumer)
(cd modules/archive && go test ./internal/bootstrap -run '^TestAppRunConsumesStorageEventAndBecomesReadyE2E$' -count=1)
(cd modules/factor && CGO_ENABLED=1 go test ./internal/store ./internal/bootstrap ./internal/catalog ./internal/periodclock ./internal/trigger ./internal/pipeline ./internal/recalc ./internal/storageio)
(cd modules/cloudnode && CGO_ENABLED=1 go test ./internal/config ./internal/jobqueue ./internal/jobstate ./internal/rpc)
# Strategy E2E tests are build-tagged e2e_external and require deployed services;
# this local contract gate covers the package-level storage and outbox tests.
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/store ./internal/outbox)
(cd modules/trade && CGO_ENABLED=1 go test ./internal/bootstrap ./internal/eventconsumer ./internal/application/target)
(cd modules/hostagent && go test ./internal/eventpublisher ./internal/app)

echo "event contract verification passed"
