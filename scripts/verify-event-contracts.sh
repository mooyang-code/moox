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
reject '\.NewConsumer\(' \
  "CloudNode bypasses the Registry-owned event Consumer API" \
  --glob '*.go' --glob '!**/*_test.go' modules/cloudnode
reject 'MooxMessage|packages/messagepb|messagepb|moox_message|messagepb\.MessageKind' \
  "legacy MooxMessage symbols remain" "${production[@]}" .
reject 'wrapperspb\.BytesValue|google\.protobuf\.BytesValue' \
  "legacy BytesValue event wrappers remain" --glob '*.go' --glob '*.proto' --glob '!**/*_test.go' .
reject 'c_topic|c_payload' \
  "an outbox still persists split topic or payload columns" modules/strategy/schema/strategy.sql
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
reject 'NATSURL|EmbeddedJetStreamConfig|yaml:"nats_url"|yaml:"embedded"' \
  "CloudNode still exposes duplicate or embedded JetStream settings" \
  --glob '*.go' modules/cloudnode/internal
reject '^[[:space:]]+(consumer|rebalance_consumer|max_ack_pending|ack_wait|ack_wait_ms):' \
  "code-owned Consumer identity or ack settings remain in business YAML" \
  --glob '*.yaml' modules/archive/config modules/monitor/config modules/trade/config modules/storage/config

for permission in \
  '$JS.API.CONSUMER.INFO.MOOX_CLOUDNODE_EXEC.>' \
  '$JS.API.CONSUMER.MSG.NEXT.MOOX_CLOUDNODE_EXEC.>' \
  '$JS.ACK.MOOX_CLOUDNODE_EXEC.>'; do
  rg -Fq "$permission" modules/admin/cmd/cli/eventbus_credentials.go || {
    echo "cloudnode-worker ACL is missing ${permission}" >&2
    exit 1
  }
done

(cd packages/events && go test ./...)
(cd packages/jetstream && go test ./...)
(cd modules/eventbus && go test ./...)
(cd modules/collector && go test ./internal/sources/binance)
(cd modules/storage && CGO_ENABLED=1 go test ./internal/config ./cmd/server ./internal/service/datanode/... ./internal/service/view/... ./internal/service/e2e)
(cd modules/monitor && go test ./internal/config ./internal/metrics ./internal/hostmetrics ./test)
(cd modules/archive && go test ./internal/config ./internal/consumer)
(cd modules/archive && go test ./internal/bootstrap -run '^TestAppRunConsumesStorageEventAndBecomesReadyE2E$' -count=1)
(cd modules/factor && CGO_ENABLED=1 go test ./internal/store ./internal/bootstrap)
(cd modules/factor && CGO_ENABLED=1 go test ./internal/trigger -run '^(TestNATSConsumerReopensFailedSessionAndRestoresReadiness|TestNATSConsumerReceivesRealEventBusDeliveryE2E)$' -count=1)
(cd modules/cloudnode && CGO_ENABLED=1 go test ./internal/config ./internal/jobqueue ./internal/jobstate ./internal/rpc)
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/store ./internal/bus ./test)
(cd modules/trade && CGO_ENABLED=1 go test ./internal/bootstrap ./internal/application/rebalance)

echo "event contract verification passed"
