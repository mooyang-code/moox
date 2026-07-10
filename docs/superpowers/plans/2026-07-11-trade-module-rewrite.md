# Trade Module Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite `modules/trade` as an event-driven, ledger-backed trading kernel with replaceable execution algorithms, then add mandatory target-position rebalancing in phase two.

**Architecture:** Keep Trade as a modular monolith with one SQLite authority and transactionally persisted Inbox/Outbox records. Commands create durable intents; domain state machines, exchange events, and MooX EventBus consumers advance execution. Exchange adapters and algorithms depend on stable domain contracts, while scheduled work is restricted to timeout recovery and reconciliation.

**Tech Stack:** Go 1.24, tRPC-Go, SQLite/GORM, Decimal string values, MooX `packages/messagepb`, MooX `packages/jetstream`, NATS JetStream, Binance and OKX REST/private streams.

---

## Scope And Locked Decisions

1. Existing Trade Schema, data and APIs may be deleted; no migration or compatibility layer is required.
2. Trade SQLite is authoritative for commands, orders, executions, fills, ledger, balances, positions, Saga, Inbox and Outbox.
3. Storage is used only as a possible market-data provider, never as Trade's authoritative projection.
4. The primary path is event driven. Scheduled jobs only recover timeouts and reconcile exchange truth.
5. Space-shared ownership is sufficient; no administrator/member distinction is introduced.
6. Simulation remains fail closed until a dedicated simulated adapter exists.
7. Amend is modeled as native amend when supported, otherwise a persisted cancel-then-replace Saga.
8. Phase one delivers the generic trading kernel. Phase two target-position rebalancing is mandatory, not optional follow-up.
9. Algorithms are deterministic, versioned and replaceable; persisted plans never change when registry configuration changes.
10. Monetary and quantity arithmetic uses exact Decimal values only.

## Delivery Milestones

| Milestone | Tasks | Exit condition |
|---|---|---|
| M1 Contracts | 1-3 | Domain types, state machines and new protocol compile with contract tests. |
| M2 Persistence | 4-6 | Fresh SQLite schema, repositories, ledger, Inbox and Outbox pass transaction tests. |
| M3 Execution | 7-11 | Generic order, cancel, cancel-replace and pluggable execution plans work with fake exchanges. |
| M4 Event Flow | 12-14 | JetStream command/event flow and private exchange events are idempotent and recoverable. |
| M5 Reconciliation | 15-16 | Unknown submissions, disconnect gaps and drift are repaired without duplicate trades. |
| M6 Rebalancing | 17-20 | Target positions produce deterministic, risk-checked plans and execute through the same kernel. |
| M7 Productization | 21-23 | RPC, Admin integration, observability, documentation and end-to-end verification are complete. |

## Target File Map

The rewrite may delete current `internal/service`, `schema/*.sql`, generated Trade proto and timer sync code after replacement tests exist.

```text
modules/trade/
  internal/domain/{order,execution,ledger,position,instrument,rebalance}/
  internal/application/{command,query,consumer,reconciliation}/
  internal/algorithm/{split,pricing,execution,rebalance}/
  internal/exchange/{binance,okx}/
  internal/infra/{store,bus,clock}/
  schema/{core,ledger,execution,bus,rebalance}.sql
  proto/trade_service.proto
```

Tests live beside each package. Cross-component tests live in `modules/trade/internal/integration`.

---

## Phase One: Generic Trading Kernel

### Task 1: Freeze Domain Vocabulary And Decimal Contract

**Files:**
- Create: `modules/trade/internal/domain/shared/decimal.go`
- Create: `modules/trade/internal/domain/shared/decimal_test.go`
- Create: `modules/trade/internal/domain/shared/ids.go`
- Modify: `modules/trade/go.mod`

- [ ] Add table-driven tests rejecting exponent notation, NaN, Infinity, negative quantities where forbidden, and values exceeding configured precision.
- [ ] Select one exact decimal library already compatible with Go 1.24 and wrap it behind a domain `Decimal` type; no other Trade package may import the library directly.
- [ ] Define typed IDs for Order, Fill, ExecutionPlan, ExecutionSlice, Saga, LedgerTransaction and RebalanceRun.
- [ ] Run `go test -count=1 ./modules/trade/internal/domain/shared` and verify all parsing and arithmetic tests pass.
- [ ] Commit as `feat(trade): define exact domain primitives`.

### Task 2: Implement Order Aggregate And State Machine

**Files:**
- Create: `modules/trade/internal/domain/order/order.go`
- Create: `modules/trade/internal/domain/order/state.go`
- Create: `modules/trade/internal/domain/order/event.go`
- Create: `modules/trade/internal/domain/order/order_test.go`

- [ ] Write transition tests for create, ready, submit, acknowledge, partial fill, fill, cancel, reject, expire and `SUBMIT_UNKNOWN`.
- [ ] Add negative tests proving terminal states cannot regress and cumulative fill cannot exceed quantity.
- [ ] Implement aggregate methods that return domain events instead of performing I/O.
- [ ] Add optimistic aggregate version increments for every accepted transition.
- [ ] Run `go test -count=1 ./modules/trade/internal/domain/order`.
- [ ] Commit as `feat(trade): add order state machine`.

### Task 3: Define Stable Algorithm And Exchange Contracts

**Files:**
- Create: `modules/trade/internal/domain/execution/contracts.go`
- Create: `modules/trade/internal/domain/instrument/rules.go`
- Replace: `modules/trade/internal/exchange/exchange.go`
- Replace: `modules/trade/internal/exchange/types.go`
- Create: `modules/trade/internal/exchange/contract_test.go`

- [ ] Define `SplitAlgorithm`, `PricingAlgorithm`, `ExecutionPolicy` and versioned algorithm descriptors.
- [ ] Define exchange-neutral place, cancel, amend, query, fill and private-stream contracts.
- [ ] Define normalized error categories including `TRANSPORT_UNCERTAIN`, `TIME_SKEW` and `RATE_LIMITED`.
- [ ] Define immutable Instrument Rules containing tick size, step size, minimum quantity, minimum notional, leverage brackets, STP and amend capabilities.
- [ ] Add compile-time fake implementations proving algorithms and adapters do not depend on application or infra packages.
- [ ] Run domain and exchange contract tests.
- [ ] Commit as `feat(trade): define execution and exchange contracts`.

### Task 4: Replace The SQLite Schema

**Files:**
- Replace: `modules/trade/schema/account.sql`
- Replace: `modules/trade/schema/order.sql`
- Replace: `modules/trade/schema/sync.sql`
- Create: `modules/trade/schema/ledger.sql`
- Create: `modules/trade/schema/execution.sql`
- Create: `modules/trade/schema/bus.sql`
- Create: `modules/trade/schema/rebalance.sql`
- Modify: `modules/trade/schema/schema.go`
- Modify: `modules/trade/schema/schema_test.go`

- [ ] Write schema contract tests for all primary keys, Space-scoped unique keys, foreign keys, aggregate versions and decimal TEXT columns.
- [ ] Create tables for accounts, channels, orders, fills, ledger accounts, ledger transactions, ledger entries, balances, positions, instrument-rule snapshots, execution plans, execution slices, Sagas, Inbox, Outbox, reconciliation runs and reconciliation differences.
- [ ] Include empty phase-two tables for rebalance runs, targets and legs so phase one repository contracts do not require another core schema redesign.
- [ ] Enable WAL, `foreign_keys=ON`, nonzero `busy_timeout` and a safe synchronous mode in database initialization tests.
- [ ] Delete timer cursor tables and old mutable fund-flow assumptions.
- [ ] Run `go test -count=1 ./modules/trade/schema ./modules/trade/cmd/cli` against a fresh temporary database.
- [ ] Commit as `feat(trade): replace trading schema`.

### Task 5: Build Storage-Neutral Unit Of Work, Repositories, Inbox And Outbox

**Files:**
- Create: `modules/trade/internal/infra/store/unit_of_work.go`
- Create: `modules/trade/internal/infra/store/order_repository.go`
- Create: `modules/trade/internal/infra/store/execution_repository.go`
- Create: `modules/trade/internal/infra/store/ledger_repository.go`
- Create: `modules/trade/internal/infra/store/bus_repository.go`
- Create: `modules/trade/internal/infra/store/repository_test.go`

- [ ] Write rollback tests proving aggregate state, ledger entries and Outbox messages commit or roll back together.
- [ ] Write unique-key tests for command idempotency, Fill idempotency and Inbox `message_id` idempotency.
- [ ] Implement repositories with compare-and-swap aggregate versions.
- [ ] Implement Outbox claiming with leases so a crashed relay does not lose or permanently lock messages.
- [ ] Verify concurrent updates produce one winner and a typed conflict for the loser.
- [ ] Keep database-driver types private to `infra/store`; domain and application contracts expose only UnitOfWork and repository interfaces.
- [ ] Run `go test -count=1 ./modules/trade/internal/infra/store`.
- [ ] Commit as `feat(trade): add transactional store`.

### Task 6: Implement The Double-Entry Ledger

**Files:**
- Create: `modules/trade/internal/domain/ledger/ledger.go`
- Create: `modules/trade/internal/domain/ledger/posting.go`
- Create: `modules/trade/internal/domain/ledger/ledger_test.go`
- Create: `modules/trade/internal/domain/position/projector.go`
- Create: `modules/trade/internal/domain/position/projector_test.go`

- [ ] Write tests for freeze, unfreeze, spot fill, fee, transfer, realized PnL, funding fee and adjustment postings.
- [ ] Assert every transaction balances and duplicate business references are no-ops.
- [ ] Implement balance materialization in the same transaction as ledger posting.
- [ ] Implement position updates from ordered Fill events with explicit realized PnL.
- [ ] Add rebuild tests deriving balances and positions from an empty projection.
- [ ] Run ledger and position tests.
- [ ] Commit as `feat(trade): add ledger and position projections`.

### Task 7: Implement Algorithm Registry And Baseline Algorithms

**Files:**
- Create: `modules/trade/internal/algorithm/registry.go`
- Create: `modules/trade/internal/algorithm/split/single.go`
- Create: `modules/trade/internal/algorithm/split/fixed_notional.go`
- Create: `modules/trade/internal/algorithm/pricing/passive_limit.go`
- Create: `modules/trade/internal/algorithm/pricing/ioc_slippage.go`
- Create: `modules/trade/internal/algorithm/execution/sequential.go`
- Create: `modules/trade/internal/algorithm/algorithm_test.go`

- [ ] Write deterministic golden tests for every algorithm and algorithm version.
- [ ] Test precision rounding, minimum notional, remainder preservation and maximum slice size.
- [ ] Implement registry lookup by exact `(name, version)` and reject unknown versions.
- [ ] Ensure algorithms return drafts only and have no Repository, EventBus or Exchange dependencies.
- [ ] Run `go test -count=1 ./modules/trade/internal/algorithm/...`.
- [ ] Commit as `feat(trade): add pluggable execution algorithms`.

### Task 8: Build Execution Plan Validation And Persistence

**Files:**
- Create: `modules/trade/internal/domain/execution/plan.go`
- Create: `modules/trade/internal/domain/execution/slice.go`
- Create: `modules/trade/internal/domain/execution/validator.go`
- Create: `modules/trade/internal/domain/execution/plan_test.go`
- Create: `modules/trade/internal/application/command/create_execution_plan.go`

- [ ] Test deterministic plan hashes for identical inputs and different hashes for changed snapshot, rules or algorithm versions.
- [ ] Test rejection of invalid dependencies, duplicate slice sequence, quantity mismatch and rules violations.
- [ ] Persist algorithm configuration, input snapshot hash, Instrument Rules version and all slices atomically.
- [ ] Emit `execution.plan_created.v1` and initial `slice_ready.v1` Outbox events.
- [ ] Verify restart loads the original plan without rerunning algorithms.
- [ ] Commit as `feat(trade): persist validated execution plans`.

### Task 9: Implement Place Order Command And Submission Worker

**Files:**
- Create: `modules/trade/internal/application/command/place_order.go`
- Create: `modules/trade/internal/application/consumer/submit_order.go`
- Create: `modules/trade/internal/application/consumer/submit_order_test.go`

- [ ] Test repeated command IDs and client order IDs return the original Order.
- [ ] Create Order, freeze funds, create execution plan and publish SliceReady atomically.
- [ ] Implement worker transition to `SUBMITTING` before the exchange call.
- [ ] Map acknowledged, rejected, deterministic failure and uncertain transport outcomes to distinct events.
- [ ] On uncertain outcome, store `SUBMIT_UNKNOWN` and publish a query command; never submit again first.
- [ ] Run command and consumer tests with a scripted fake exchange.
- [ ] Commit as `feat(trade): execute idempotent order submission`.

### Task 10: Implement Fill Settlement And Partial Retry

**Files:**
- Create: `modules/trade/internal/application/consumer/apply_fill.go`
- Create: `modules/trade/internal/application/consumer/apply_fill_test.go`
- Create: `modules/trade/internal/domain/execution/retry.go`
- Create: `modules/trade/internal/domain/execution/retry_test.go`

- [ ] Test duplicate and out-of-order Fill events.
- [ ] Apply Fill, ledger postings, balance projection, position projection and order cumulative quantity in one transaction.
- [ ] Calculate remaining quantity exactly and create another slice only when the execution policy permits it.
- [ ] Treat below-minimum residue as explicit `EXHAUSTED_DUST`, never as a fabricated Fill.
- [ ] Test IOC partial fills retry only the remainder.
- [ ] Commit as `feat(trade): settle fills and retry remainders`.

### Task 11: Implement Cancel And Cancel-Then-Replace Sagas

**Files:**
- Create: `modules/trade/internal/domain/execution/saga.go`
- Create: `modules/trade/internal/application/command/cancel_order.go`
- Create: `modules/trade/internal/application/command/replace_order.go`
- Create: `modules/trade/internal/application/consumer/advance_saga.go`
- Create: `modules/trade/internal/application/consumer/advance_saga_test.go`

- [ ] Test cancel against open, partially filled, already closed and unknown orders.
- [ ] Persist cancel request before calling the exchange and unfreeze only confirmed remaining quantity.
- [ ] Implement native amend capability routing when Instrument Rules permit it.
- [ ] Otherwise run `CANCEL_REQUESTED -> CANCEL_CONFIRMED -> REPLACEMENT_CREATED -> REPLACEMENT_SUBMITTED`.
- [ ] Test and expose `REPLACE_FAILED_AFTER_CANCEL`, cancel-unknown and replacement-submit-unknown states.
- [ ] Commit as `feat(trade): add cancel and replace sagas`.

### Task 12: Register Trade Topics And Build The Public-Client-Based Outbox Relay

**Files:**
- Modify: `modules/eventbus/internal/registry/registry.go`
- Modify: `modules/eventbus/config/app.yaml`
- Create: `modules/trade/internal/infra/bus/topics.go`
- Create: `modules/trade/internal/infra/bus/outbox_relay.go`
- Create: `modules/trade/internal/infra/bus/outbox_relay_test.go`

- [ ] Add exact Trade Topic contracts and a `MOOX_TRADE` Limits/File stream.
- [ ] Declare durable consumers for execution, settlement and reconciliation workers.
- [ ] Reuse `packages/messagepb` and `packages/jetstream` directly; do not duplicate NATS connection, publish, consume, ACK, retry or message codec implementations in Trade.
- [ ] Publish stable `MooxMessage.message_id` values through `packages/jetstream`.
- [ ] Mark Outbox rows published only after PubAck; retry with the same message ID.
- [ ] Test broker outage, process crash after PubAck and duplicate relay delivery.
- [ ] Commit as `feat(trade): connect transactional outbox to eventbus`.

### Task 13: Add Idempotent Event Consumers

**Files:**
- Create: `modules/trade/internal/infra/bus/consumer.go`
- Create: `modules/trade/internal/application/consumer/router.go`
- Create: `modules/trade/internal/application/consumer/router_test.go`

- [ ] Decode and validate Topic-specific protobuf payloads.
- [ ] Insert Inbox record and apply domain changes in one transaction.
- [ ] ACK only after commit; ACK duplicate Inbox messages without reapplying them.
- [ ] NAK transient failures and route permanent invalid messages to the common DLQ with sanitized reasons.
- [ ] Test duplicate, redelivered, malformed and out-of-order messages.
- [ ] Commit as `feat(trade): consume trade events idempotently`.

### Task 14: Implement Binance And OKX Adapters With Private Streams

**Files:**
- Replace: `modules/trade/internal/exchange/binance/*`
- Replace: `modules/trade/internal/exchange/okx/*`
- Create: `modules/trade/internal/exchange/binance/adapter_test.go`
- Create: `modules/trade/internal/exchange/okx/adapter_test.go`
- Create: `modules/trade/internal/infra/clock/exchange_clock.go`

- [ ] Add signed REST fixture tests for place, cancel, query, rules and leverage endpoints.
- [ ] Add error-classification fixtures for time skew, rate limits, insufficient balance, invalid rules and ambiguous transport failures.
- [ ] Implement private-stream lifecycle, reconnect and event normalization.
- [ ] Query by client order ID before retrying any ambiguous submission.
- [ ] Refresh exchange clock offset and Instrument Rules on their typed errors.
- [ ] Commit as `feat(trade): implement resilient exchange adapters`.

### Task 15: Implement Reconciliation

**Files:**
- Create: `modules/trade/internal/application/reconciliation/reconciler.go`
- Create: `modules/trade/internal/application/reconciliation/policy.go`
- Create: `modules/trade/internal/application/reconciliation/reconciler_test.go`
- Create: `modules/trade/internal/application/consumer/reconcile.go`

- [ ] Compare local open orders, recent Fills, balances and positions with exchange snapshots.
- [ ] Resolve `SUBMIT_UNKNOWN` by client order ID before any retry decision.
- [ ] Persist every difference and selected repair action.
- [ ] Apply missing Fills through the normal settlement path; use explicit adjustment ledger entries for irreducible balance differences.
- [ ] Emit reconciliation completed/failed events and metrics.
- [ ] Commit as `feat(trade): reconcile exchange truth`.

### Task 16: Restrict Scheduled Work To Recovery

**Files:**
- Replace: `modules/trade/internal/rpc/schedule.go`
- Replace: `modules/trade/internal/service/sync.go`
- Modify: `modules/trade/config/app.yaml`
- Create: `modules/trade/internal/application/reconciliation/scheduler_test.go`

- [ ] Remove periodic full sync as the primary ingestion design.
- [ ] Schedule only stale Saga scans, unknown submission queries, private-stream gap repair, Outbox lease recovery and low-frequency reconciliation.
- [ ] Test active healthy orders are not repeatedly queried on every tick.
- [ ] Test each recovery job is idempotent and bounded by account, time range and page size.
- [ ] Commit as `refactor(trade): make scheduled work recovery only`.

---

## Phase Two: Mandatory Target-Position Rebalancing

### Task 17: Implement Rebalance Domain And Target Intake

**Files:**
- Create: `modules/trade/internal/domain/rebalance/rebalance.go`
- Create: `modules/trade/internal/domain/rebalance/target.go`
- Create: `modules/trade/internal/domain/rebalance/rebalance_test.go`
- Create: `modules/trade/internal/application/command/create_rebalance.go`

- [ ] Define target positions by account, market, symbol, signed quantity and target mode.
- [ ] Require idempotency key, account snapshot, position snapshot, market snapshot and Instrument Rules snapshot.
- [ ] Reject stale, incomplete or internally inconsistent snapshots.
- [ ] Persist targets and emit `rebalance.requested.v1` atomically.
- [ ] Commit as `feat(trade): accept target position intents`.

### Task 18: Build The Target-Position Planner

**Files:**
- Create: `modules/trade/internal/algorithm/rebalance/target_position.go`
- Create: `modules/trade/internal/algorithm/rebalance/target_position_test.go`
- Create: `modules/trade/internal/domain/rebalance/leg.go`

- [ ] Calculate `target quantity - reconciled current quantity` with exact Decimal arithmetic.
- [ ] Classify each leg as close, reduce, open, increase or reverse.
- [ ] Set `reduceOnly` only where the leg cannot increase exposure.
- [ ] Add deterministic golden tests for long, short, flat, partial close and reversal cases.
- [ ] Preserve zero targets as meaningful close instructions.
- [ ] Commit as `feat(trade): plan target position deltas`.

### Task 19: Add Rebalance Risk And Dependency Planning

**Files:**
- Create: `modules/trade/internal/domain/rebalance/risk.go`
- Create: `modules/trade/internal/algorithm/execution/sell_reduce_first.go`
- Create: `modules/trade/internal/domain/rebalance/risk_test.go`

- [ ] Validate leverage brackets with a configured safety margin.
- [ ] Validate available funds, reserve ratio, min notional, max slice amount and account usability.
- [ ] Build a dependency DAG that executes close/reduce/sell before open/increase/buy.
- [ ] Detect dependency cycles and reject an unexecutable plan before saving it.
- [ ] Make required account transfers explicit Saga legs rather than hidden account-refresh side effects.
- [ ] Commit as `feat(trade): validate and order rebalance legs`.

### Task 20: Execute And Reconcile Rebalance Runs

**Files:**
- Create: `modules/trade/internal/application/consumer/execute_rebalance.go`
- Create: `modules/trade/internal/application/consumer/execute_rebalance_test.go`
- Create: `modules/trade/internal/application/reconciliation/rebalance.go`
- Create: `modules/trade/internal/integration/rebalance_test.go`

- [ ] Convert approved legs through the selected split, pricing and execution algorithms into normal ExecutionPlans.
- [ ] Advance ready legs only when their dependencies reach successful terminal states.
- [ ] Stop or continue independent legs according to persisted failure policy.
- [ ] Reconcile final positions and record residual delta without fabricating completion.
- [ ] Emit `rebalance.completed.v1`, `rebalance.partially_completed.v1` or `rebalance.failed.v1`.
- [ ] Run a restart test that kills execution mid-plan and resumes without duplicate orders.
- [ ] Commit as `feat(trade): execute target position rebalancing`.

---

## Productization And Verification

### Task 21: Replace Proto And RPC Surface

**Files:**
- Replace: `modules/trade/proto/trade_service.proto`
- Regenerate: `modules/trade/proto/tradegen/*`
- Replace: `modules/trade/internal/rpc/*`
- Modify: `modules/admin` Trade gateway registration files discovered during implementation.

- [ ] Design command APIs for place, cancel, replace and rebalance with required idempotency keys.
- [ ] Design query APIs for orders, executions, Fills, ledger transactions, balances, positions, Saga and rebalance runs.
- [ ] Keep secret values out of every response and log field.
- [ ] Add protobuf descriptor tests locking field numbers and enum values.
- [ ] Regenerate code and run Trade/Admin compile tests.
- [ ] Commit as `feat(trade): expose rewritten trading api`.

### Task 22: Add Observability And Operational Controls

**Files:**
- Create: `modules/trade/internal/observability/metrics.go`
- Create: `modules/trade/internal/observability/tracing.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/config/app.yaml`
- Create: `docs/运维/MooX-Trade运维.md`

- [ ] Add counters and latency histograms for commands, submissions, fills, duplicates, unknown states, reconciliation differences, Outbox lag and rebalance completion.
- [ ] Add bounded labels only; never label metrics with order IDs or symbols unless explicitly bounded.
- [ ] Propagate MooX TraceContext through EventBus and exchange operations.
- [ ] Add health checks for SQLite writeability, EventBus connectivity, Outbox lag and private-stream freshness.
- [ ] Document pause-account, pause-channel, reconcile-now and inspect-Saga procedures.
- [ ] Commit as `feat(trade): add trading operations visibility`.

### Task 23: End-To-End Verification And Documentation Cutover

**Files:**
- Create: `modules/trade/internal/integration/order_lifecycle_test.go`
- Create: `modules/trade/internal/integration/failure_recovery_test.go`
- Replace: `modules/trade/DESIGN.md`
- Replace: `modules/trade/README.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/SUMMARY.md`

- [ ] Verify place-to-fill, partial fill, cancel, cancel-replace, duplicate Fill and target-position rebalance flows against fake exchanges and a test JetStream.
- [ ] Verify crashes before/after SQLite commit, before/after PubAck and during ambiguous exchange submission.
- [ ] Verify ledger balance and projection rebuild after every scenario.
- [ ] Run `go test -count=1 ./modules/trade/...` and all affected EventBus/Admin packages.
- [ ] Run `go vet` for affected modules and build `moox-trade` plus generated protobuf modules.
- [ ] Replace old Trade documentation and explicitly remove scheduled-sync-primary-path descriptions.
- [ ] Commit as `docs(trade): complete event driven trade rewrite`.

## Final Acceptance Gate

Do not mark the rewrite complete until all conditions hold:

- No `float32` or `float64` represents business money, price or quantity.
- No ambiguous submission is retried before query-by-client-order-ID.
- No balance mutation exists outside ledger posting or explicit projection rebuild.
- No EventBus consumer changes state without Inbox idempotency.
- No domain transaction publishes directly without Outbox.
- No algorithm imports exchange, repository, tRPC or JetStream packages.
- No simulated channel can reach a live adapter.
- Cancel-then-replace exposes partial failure states.
- Rebalance planning is implemented, deterministic and restartable.
- Fresh `go test -count=1` evidence exists for Trade and affected shared modules.
