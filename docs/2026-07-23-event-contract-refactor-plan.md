# Event Contract Naming and Structured Storage Payload Implementation Plan

> **状态：历史计划，禁止作为当前实现依据。**
> 本文保留 2026-07-23 当时的设计与审查记录，其中的 Tick/Streamcalc、
> YAML Registry、旧事件词表和 Consumer/DLQ 描述已经过期。当前运行契约以
> [协议设计](协议设计.md)、[架构总览](架构总览.md)和
> [Event System CR Remediation](superpowers/plans/2026-07-24-event-system-cr-remediation.md)
> 为准。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the market and trading events according to their actual semantics, introduce a concise structured trading-signal contract, move the structured Storage row event contract into `packages/storagepb`, and rename the Event Registry `Spec` API to `Schema`.

**Architecture:** `TickReceived` represents raw exchange ticks and remains the input to K-line aggregation. `TradingSignal` represents a Factor/Strategy recommendation and is independent from order execution or trade execution. `DatasetRowsUpserted` becomes a self-describing structured event payload owned by `packages/storagepb`; the outer `EventMessage` remains the transport identity and routing contract. Storage module RPC models remain local `storagegen` types and are converted at event boundaries.

**Tech Stack:** Go 1.25, NATS JetStream, Protocol Buffers, `google.golang.org/protobuf`, embedded YAML event registry, Go workspaces.

---

## 1. Final Contract Decisions

### 1.1 Market Tick

Go event constant:

```go
TickReceived = EventType{Name: "market.tick.received", Version: 1}
```

Payload message:

```protobuf
message Tick {
  string exchange = 1;
  string trade_id = 2;
  string symbol = 3;
  double price = 4;
  double quantity = 5;
  bool buyer_maker = 6;
  google.protobuf.Timestamp trade_time = 7;
}
```

This is a raw exchange transaction tick. It is not a strategy recommendation and is consumed by Streamcalc for K-line aggregation.

### 1.2 Trading Signal

Go event constant:

```go
TradingSignal = EventType{Name: "trading.signal", Version: 1}
```

Subject template:

```text
moox.trading.signal.v1.<space>.<subject>
```

Payload messages:

```protobuf
enum SignalSide {
  SIGNAL_SIDE_UNSPECIFIED = 0;
  SIGNAL_SIDE_BUY = 1;
  SIGNAL_SIDE_SELL = 2;
  SIGNAL_SIDE_HOLD = 3;
}

enum SignalAction {
  SIGNAL_ACTION_UNSPECIFIED = 0;
  SIGNAL_ACTION_OPEN = 1;
  SIGNAL_ACTION_CLOSE = 2;
  SIGNAL_ACTION_INCREASE = 3;
  SIGNAL_ACTION_DECREASE = 4;
}

message TradingSignal {
  string strategy_id = 1;
  string signal_id = 2;
  string symbol = 3;
  SignalSide side = 4;
  SignalAction action = 5;
  optional double target_price = 6;
  optional double stop_loss_price = 7;
  optional double take_profit_price = 8;
  google.protobuf.Timestamp signal_time = 9;
  map<string, string> tags = 10;
}
```

`TradingSignal` is a recommendation emitted by Factor/Strategy. `OPEN`/`CLOSE` and `INCREASE`/`DECREASE` are the two symmetric action pairs. It does not contain confidence, strength, order status, fill status, exchange order ID, or execution result.

### 1.3 Structured Dataset Rows Event

Go event constant:

```go
DatasetRowsUpserted = EventType{Name: "storage.dataset.rows.upserted", Version: 1}
```

The event payload lives in `packages/storagepb` and is self-describing:

```protobuf
message DatasetRowsUpserted {
  string space_id = 1;
  string dataset_id = 2;
  repeated RowUpsert rows = 3;
}

message RowUpsert {
  RowKey key = 1;
  repeated FieldValue fields = 2;
  map<string, TypedValue> attributes = 3;
}
```

The shared package also defines the structured `RowKey`, time-series/record key variants, `FieldValue`, `TypedValue`, `ValueList`, and `NullValue` needed by `RowUpsert`.

There is intentionally no `operation` field because V1 only supports UPSERT. There are no `reserved` declarations because this is a pre-launch contract with no wire-compatibility requirement.

Identity validation is mandatory:

```text
payload.space_id == EventMessage.space_id
payload.dataset_id == EventMessage.subject_id
row.key.space_id == payload.space_id
row.key.dataset_id == payload.dataset_id
```

The outer event still owns `event_id`, event name/version, timestamps, producer, routing, and message metadata. The payload duplicates only Dataset identity because replay and diagnosis must be possible without guessing the Dataset from a subject.

### 1.4 Registry API

Rename the registry declaration type and lookup methods consistently:

```go
type EventSchema struct { ... }

func (r *Registry) Schema(event EventType) (EventSchema, bool)
func (r *Registry) Schemas() []EventSchema
```

There must be no `EventSpec`, `Spec`, or `Specs` compatibility aliases. This is a new project and the API should have one canonical name.

## 2. File Map

### Create

- `packages/storagepb/go.mod`: standalone shared storage event protocol module.
- `packages/storagepb/storage_events.proto`: structured Dataset row event and row value types.
- `packages/storagepb/storage_events.pb.go`: generated Go bindings.
- `packages/storagepb/Makefile`: reproducible protobuf generation command.
- `packages/events/proto/tradingpb/trading_payloads.proto`: TradingSignal payload and signal enums.
- `packages/events/tradingpb/trading_payloads.pb.go`: generated TradingSignal bindings.

### Modify

- `packages/events/proto/marketpb/market_payloads.proto`: rename `TradeReceived` payload to `Tick`; keep market payloads under the market namespace.
- `packages/events/marketpb/market_payloads.pb.go`: regenerate.
- `packages/events/registry/events.yaml`: register `market.tick.received`, `trading.signal`, and `storage.dataset.rows.upserted`.
- `packages/events/registry.go`: rename `EventSpec` to `EventSchema`, update payload factories, and rename `Spec`/`Specs`.
- `packages/events/message.go`: use `Schema` for encoding.
- `packages/events/decode.go`: use `DatasetRowsUpserted` and the new shared payload package.
- `packages/events/events_test.go`: update names and add structured identity tests.
- `packages/events/go.mod`: require and replace `packages/storagepb`.
- `modules/storage/go.mod`, `modules/archive/go.mod`, `modules/factor/go.mod`: require and replace `packages/storagepb` for event-boundary conversions.
- `go.work`: include `packages/storagepb`.
- `modules/eventbus/config/app.yaml`: update stream subjects, topic families, payload full names, and add the trading signal family.
- `modules/eventbus/internal/config/config_defaults.go`: update default governed topic definitions.
- `modules/eventbus/internal/config/config_validation.go`: validate the renamed registry schemas and new families.
- `modules/collector`: publish `TickReceived` with `marketpb.Tick`.
- `modules/streamcalc`: consume `TickReceived` and update all event references.
- `modules/storage/internal/service/datanode/pebble/event.go`: convert local storage rows to `storagepb.DatasetRowsUpserted` before publishing.
- `modules/storage/internal/service/view/eventconsumer/dataset_publisher.go`: convert local rows to the structured shared event payload.
- `modules/storage/internal/service/view/consume.go`: decode `storagepb.DatasetRowsUpserted`, validate identity, and convert to the local Storage row model.
- `modules/archive/internal/consumer/decode.go`: decode shared structured rows and convert them into Archive domain writes.
- `modules/factor/internal/trigger/nats.go`: decode shared structured rows and validate outer/payload identity.
- `modules/factor/internal/trigger/event_batcher.go`: use the shared event row type at the event boundary.
- `modules/factor/internal/store/event_inbox.go`: persist and restore the structured event type.
- `modules/factor/cmd/cli/replay.go`: read structured `DatasetRowsUpserted` payloads.
- affected tests and testkits under `modules/{collector,streamcalc,storage,archive,factor,eventbus}/**`.
- `docs/协议设计.md`, `docs/架构总览.md`, and the streaming repair plan: document final names and payload ownership.

### Delete

- `packages/events/proto/storagepb/storage_payloads.proto`.
- `packages/events/storagepb/storage_payloads.pb.go`.
- Any generated or handwritten `TradeReceived` and old `RowsUpserted` event-contract bindings under `packages/events` after all call sites are migrated. Internal `modules/storage/proto/storagegen.RowsUpserted` may remain as a local RPC/storage model until its event-boundary conversion is complete.

## 3. Implementation Tasks

### Task 1: Establish the new shared storage event module

**Files:**
- Create `packages/storagepb/go.mod`.
- Create `packages/storagepb/storage_events.proto`.
- Create `packages/storagepb/Makefile`.
- Modify `go.work`.

- [x] Create the module with only the protobuf runtime dependency and add it to every module that directly imports the shared event types.
- [x] Define `DatasetRowsUpserted`, `RowUpsert`, `RowKey`, `TimeSeriesRowKey`, `RecordRowKey`, `FieldValue`, `TypedValue`, `ValueList`, and `NullValue`.
- [x] Use `space_id=1`, `dataset_id=2`, `rows=3`; do not add reserved fields.
- [x] Generate bindings with `protoc --go_out=. --go_opt=paths=source_relative storage_events.proto`.
- [x] Add a protobuf round-trip test proving all key variants, typed values, attributes, and multiple rows survive marshal/unmarshal.
- [x] Run `go test ./...` from `packages/storagepb`.

### Task 2: Rename market payloads and add TradingSignal

**Files:**
- Modify `packages/events/proto/marketpb/market_payloads.proto`.
- Regenerate `packages/events/marketpb/market_payloads.pb.go`.
- Create `packages/events/proto/tradingpb/trading_payloads.proto`.
- Generate `packages/events/tradingpb/trading_payloads.pb.go`.

- [x] Rename `TradeReceived` to `Tick` without changing the seven raw tick fields.
- [x] Define `SignalSide`, `SignalAction`, and `TradingSignal` under the `trpc.moox.trading` protobuf namespace, not the market namespace; do not add confidence or strength fields.
- [x] Set the trading proto package and Go package explicitly:

```protobuf
package trpc.moox.trading;
option go_package = "github.com/mooyang-code/moox/packages/events/tradingpb;tradingpb";
```

- [x] Keep `KlineClosed` unchanged.
- [x] Regenerate Go bindings and verify that the generated full names are `trpc.moox.market.Tick` and `trpc.moox.trading.TradingSignal`.
- [x] Add payload tests for BUY/OPEN and SELL/CLOSE signals, including omitted optional prices.

### Task 3: Refactor the event registry API and event contracts

**Files:**
- Modify `packages/events/registry.go`.
- Modify `packages/events/message.go`.
- Modify `packages/events/decode.go`.
- Modify `packages/events/events_test.go`.
- Modify `packages/events/registry/events.yaml`.

- [x] Rename `EventSpec` to `EventSchema`, `Spec` to `Schema`, and `Specs` to `Schemas`.
- [x] Update all internal call sites and tests; do not leave aliases.
- [x] Register these exact contracts:

```yaml
- name: market.tick.received
  version: 1
  payload: trpc.moox.market.Tick
  subject: moox.market.tick.received.v1.<space>.<subject>
  stream: MOOX_MARKET
  partition_key: subject_id
  owner: collector

- name: trading.signal
  version: 1
  payload: trpc.moox.trading.TradingSignal
  subject: moox.trading.signal.v1.<space>.<subject>
  stream: MOOX_TRADE
  partition_key: subject_id
  owner: strategy

- name: storage.dataset.rows.upserted
  version: 1
  payload: trpc.moox.storage.event.DatasetRowsUpserted
  subject: moox.storage.dataset.rows.upserted.v1.<space>.<subject>
  stream: MOOX_STORAGE
  partition_key: subject_id
  owner: storage
```

- [x] Move the Storage payload factory from `packages/events/storagepb` to `packages/storagepb`.
- [x] Add a registry test that `Schema`, `Schemas`, `RenderSubject`, and `FamilyPattern` return the new names and subjects.
- [x] Delete the old event-contract protobuf and generated bindings from `packages/events`.

### Task 4: Update EventBus topology and configuration

**Files:**
- Modify `modules/eventbus/config/app.yaml`.
- Modify `modules/eventbus/internal/config/config_defaults.go`.
- Modify `modules/eventbus/internal/config/config_validation.go` and tests.

- [x] Change the MOOX_MARKET stream subjects to include `moox.market.tick.received.v1.>`.
- [x] Add `moox.trading.>` to the MOOX_TRADE stream subject list.
- [x] Add topic families for `market.tick.received`, `trading.signal`, and `storage.dataset.rows.upserted`.
- [x] Keep the broker `payload_content_type` as the governed EventMessage media type
  `application/vnd.moox.event+protobuf`; the generated payload full names are owned by
  `packages/events/registry/events.yaml` and validated by the Registry factory. Do not
  claim that the broker body is a bare Tick/TradingSignal/DatasetRowsUpserted payload.
- [x] Update config validation to iterate over `registry.Schemas()`.
- [x] Add tests proving a missing renamed family fails startup validation and all three families pass.
- [x] Run `go test ./...` from `modules/eventbus`.

### Task 5: Migrate Collector and Streamcalc to TickReceived

**Files:**
- Modify Collector market source/publisher files found by `rg "TradeReceived|market.trade.received" modules/collector`.
- Modify Streamcalc consumer and tests found by `rg "TradeReceived|market.trade.received" modules/streamcalc`.

- [x] Replace any existing `TradeReceived` payload construction with `marketpb.Tick`.
- [x] Replace any existing `events.MarketTradeReceived` reference with `events.TickReceived`.
- [x] Change all Tick subject strings to `moox.market.tick.received.v1.<space>.<subject>`.
- [x] Preserve the existing Streamcalc event-time ordering, deduplication, and K-line aggregation behavior.
- [x] Add a Collector Tick producer using Binance Spot/Futures cursorable aggregate-trade REST endpoints, deterministic trade IDs, per-space/instrument/symbol cursors, ascending ordering, and governed `TickReceived` publication; register the `collect.tick` job definition/planner/CloudNode handler so the source is reachable through normal task execution.
- [x] Add a contract test proving a Tick event is decoded by Streamcalc and contributes to the expected window.
- [x] Run `go test ./...` from `modules/collector` and `modules/streamcalc`.

### Task 6: Migrate Storage publishers to structured DatasetRowsUpserted

**Files:**
- Modify `modules/storage/internal/service/datanode/pebble/event.go`.
- Modify `modules/storage/internal/service/view/eventconsumer/dataset_publisher.go`.
- Add conversion helpers in the owning Storage packages, not in `packages/events`.
- Update related Storage tests.

- [x] Convert the local `storagegen.RowsUpserted` rows into `storagepb.DatasetRowsUpserted` at an explicit boundary without leaking the local `operation` field or losing structured values.
- [x] Populate `space_id` and `dataset_id` from the local event.
- [x] Validate every row key has the same `space_id` and `dataset_id` before publishing.
- [x] Publish using `events.DatasetRowsUpserted` and the new subject template.
- [x] Remove the old `bytes rows` wrapper and all `eventstoragepb.RowsUpserted` references.
- [x] Test multiple rows, time-series keys, record keys, typed values, and mismatched row identity rejection.

### Task 7: Migrate Storage View, Archive, and Factor consumers

**Files:**
- Modify `modules/storage/internal/service/view/consume.go`.
- Modify `modules/archive/internal/consumer/decode.go` and tests.
- Modify `modules/factor/internal/trigger/nats.go`.
- Modify `modules/factor/internal/trigger/event_batcher.go`.
- Modify `modules/factor/internal/store/event_inbox.go`.
- Modify `modules/factor/cmd/cli/replay.go`.
- Modify Factor testkits and fixtures.

- [x] Decode `storagepb.DatasetRowsUpserted` directly from the governed EventMessage payload.
- [x] Validate outer and payload identity before applying or persisting the event.
- [x] Convert shared `storagepb.RowUpsert` values into each module's internal Storage/domain model at the boundary.
- [x] Keep the existing Storage View subject-lane ordering, local retry counter, DLQ behavior, and Backfill gate unchanged.
- [x] Ensure Factor inbox persistence and replay restore the structured rows without lossy conversion.
- [x] Ensure Archive receives structured rows and continues producing the same domain row patches.
- [x] Add malformed payload, identity mismatch, empty rows, and mixed row-key tests.
- [x] Run focused tests from `modules/storage`, `modules/archive`, and `modules/factor`.

### Task 8: Add TradingSignal producer/consumer contract coverage

**Files:**
- Modify Factor/Strategy event integration files selected by `rg "TradingSignal|strategy|signal" modules/factor modules/strategy modules/trade`.
- Add or update contract tests under the producing and consuming modules.

- [x] Add a deterministic `TradingSignal` publisher helper with required `strategy_id`, `signal_id`, `symbol`, side, action, and signal time validation.
- [x] Ensure the published subject uses the symbol as `subject_id`.
- [x] Wire the Trade module's production signal consumer as a recommendation consumer: bind the predeclared durable read-only, decode the governed EventMessage, and atomically persist inbox plus structured recommendation with event/signal idempotency. It intentionally does not submit orders because the signal contract has no account/channel/quantity/execution policy.
- [x] Add Trade unit, idempotency, and embedded-NATS E2E coverage for the recommendation consumer.
- [x] Verify optional target/stop/take-profit values are preserved when present and absent when omitted.
- [x] Do not add order status or execution result fields to this event.

**Task 8 scope note:** Trade persists recommendations durably and idempotently. Mapping a recommendation to an order remains a separate explicit execution-policy feature, so this event cannot accidentally place an order without account/channel/quantity policy.

### Task 9: Remove old names and update documentation

**Files:**
- Modify `docs/协议设计.md`.
- Modify `docs/架构总览.md`.
- Modify `docs/2026-07-22-moox-streaming-repair-plan.md`.
- Update any active code comments and test descriptions.

- [x] Confirm there are no production references to `TradeReceived`, `market.trade.received`, the `packages/events` `RowsUpserted` event wrapper, `eventstoragepb`, `EventSpec`, `.Spec(`, or `.Specs(`. Do not treat the local `modules/storage/proto/storagegen.RowsUpserted` model as the old event wrapper.
- [x] Document the distinction between raw Tick, TradingSignal, and DatasetRowsUpserted.
- [x] Document that Dataset identity is intentionally duplicated between outer EventMessage and structured payload for validation/replay.
- [x] Document that V1 supports UPSERT only and intentionally has no reserved fields.
- [x] Document `packages/storagepb` as the shared structured event row contract.

 Full verification and review gate

**Files:**
- No source changes unless verification finds a concrete failure.

- [x] Run `git diff --check`.
- [x] Run `go test ./...` from `packages/storagepb`, `packages/events`, `modules/eventbus`, `modules/collector`, `modules/streamcalc`, `modules/storage`, `modules/archive`, `modules/factor`, `modules/strategy`, and `modules/trade`.
- [x] Run `go test -race ./...` for `packages/events` and the Storage View package.
- [x] Run `go vet ./...` from all changed Go modules.
- [x] Run targeted protobuf descriptor checks proving the new full names are registered exactly once.
- [x] Run repository searches for all old identifiers listed in Task 9.
- [x] Start a fresh independent code-review Agent and require findings with severity, file, line, control-flow reasoning, and test coverage.
- [x] Resolve every P0/P1/P2 finding before declaring the refactor complete.

### Independent Review Follow-up (2026-07-23)

- Fresh review found no P0/P1 findings.
- The P2 finding in `DatasetPublisher.PublishMessageWithAck` was fixed by routing outbox bytes through `events.DecodeDatasetRowsUpserted` before publishing; empty payloads, invalid timestamps, and outer/payload identity mismatches are now rejected, with focused tests.
- Boundary conversion tests were added for time-series keys, record keys, typed values, UPSERT defaulting, and identity mismatch rejection.
- A minor legacy `Archive.Decoder.Decode(*messagepb.MooxMessage)` path remains because the current Archive handler test seam and old internal fixtures still use it. The live governed path uses `DecodeEvent` and `EventMessage`; removing the test seam is a separate cleanup and is not a compatibility alias in the public event contract.

### Independent Review Follow-up (2026-07-23, Lorentz)

- Fresh review found three P1 issues and one P2: the Tick job was not registered with the CloudNode runtime, Binance recent-trade endpoints did not provide a reliable cursor, and structured row values were not fully validated at the shared decoder boundary.
- Fixed the CloudNode Tick handler registration and added Tick taskrunner coverage.
- Switched the public Binance Tick polling path to cursorable Spot/Futures aggregate-trade endpoints and added HTTPS query/path tests for `fromId`.
- Added shared structured-row validation for key kind, time-series/record key fields, field IDs, typed values, lists, JSON/time values, finite doubles, and explicit NULL values, with malformed-row coverage.
- Final independent re-review found one additional P1: the Streamcalc durable still filtered only closed K-lines, so Tick events could not reach the production consumer. Fixed the managed durable filter to `moox.market.>` and changed the embedded-NATS E2E to publish and aggregate Tick events.
- A second independent review found two P1 issues: a duplicate `signal_id` with a new envelope `event_id` could retry forever on the recommendation unique key, and unknown TradingSignal action enum values were accepted. Fixed recommendation insertion with idempotent `INSERT OR IGNORE`, added the different-event duplicate test, and changed action validation to an explicit `OPEN/CLOSE/INCREASE/DECREASE` whitelist with unknown-action coverage.
- Final independent re-review by Agent Herschel (2026-07-23) returned `APPROVED`; no P0/P1/P2 findings remain. Minor residuals are aggregate-trade semantics for the Binance Tick poller, limited CloudNode dispatch coverage, and no dedicated Streamcalc checkpoint-failure recovery test.

## 4. Explicit Non-Goals

- No backward compatibility aliases for old event names or Go APIs.
- No migration of historical messages or old JetStream subjects.
- No delete operation in `DatasetRowsUpserted` V1.
- No order execution state inside `TradingSignal`.
- No change to the existing EventMessage outer envelope shape.
- No import from `packages/events` into `modules/storage`; shared row contracts must live in `packages/storagepb`.

## 5. Acceptance Criteria

- `TickReceived` is the only raw exchange tick contract and Streamcalc consumes it.
- `TradingSignal` is a distinct structured recommendation contract.
- `DatasetRowsUpserted` is structured, self-describing, and contains `space_id`, `dataset_id`, and repeated row upserts.
- Every row event validates outer envelope identity, payload identity, and row-key identity.
- There is one canonical generated `DatasetRowsUpserted` type and no duplicate Protobuf full-name registration.
- Registry APIs are named `EventSchema`, `Schema`, and `Schemas`.
- No old event names or old `packages/events` wrapper remain in active source; the new `packages/storagepb` event contract contains no reserved declarations.
- All focused tests, race tests, vet checks, and independent review pass.
