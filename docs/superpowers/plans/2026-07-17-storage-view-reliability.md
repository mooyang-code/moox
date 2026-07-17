# Storage View Reliability Implementation Plan

> **Worker requirement:** Use test-driven-development. Preserve the Pebble primary-write contract; make derived View delivery at-least-once and observable.

**Goal:** ACK a `rows_updated.v1` delivery only after every DuckDB/Bleve row derived from that event has completed successfully; Nak failures, heartbeat long work, bound concurrency, drain cleanly, and prove reconciliation under real JetStream redelivery.

**Boundary:** Delivery/Ack objects stay in `internal/infra/eventbus`. ViewBuilder sees only domain events and returns an error after all scheduled rows reach a terminal result.

---

### Task 1: Make delivery configuration explicit

**Files:**
- Modify: `modules/storage/internal/config/loader.go`, `loader_test.go`
- Modify: all `modules/storage/config/storage*.yaml`
- Modify: `modules/storage/internal/bootstrap/eventbus/factory.go`, `factory_test.go`
- Modify: `modules/storage/internal/infra/eventbus/producer_bus.go`, `producer_bus_test.go`

- [ ] Add RED tests for defaults and validation: `ack_wait_ms >= 3000`, `max_in_flight >= 1`, `max_ack_pending >= 1`, and `max_in_flight <= max_ack_pending`.
- [ ] Add `max_ack_pending` (default/config value 128) separately from handler concurrency. Keep it aligned with the existing durable topology; never derive durable MaxAckPending from a smaller concurrency value because `BindPullConsumer` validates exact durable configuration.
- [ ] Introduce typed `SubscriberOptions` carrying stream/subject prefix, AckWait, MaxDeliver, MaxInFlight, MaxAckPending, Nak delay, and action timeout. Pass config through factory and assert both consumer refs contain exact values.
- [ ] Run:

```bash
cd modules/storage
go test ./internal/config ./internal/bootstrap/eventbus ./internal/infra/eventbus -count=1
```

### Task 2: Implement an event-level completion tracker

**Files:**
- Create: `modules/storage/internal/service/view/builder/completion.go`, `completion_test.go`
- Modify: `modules/storage/internal/service/view/builder/service.go`, `service_test.go`

- [ ] Add RED tests proving: empty/nil rows return immediately; one event spanning batches waits for its final item; merged events all receive a batch error; an enqueue failure marks unqueued items failed but still waits for already queued items; cancellation and late completion never panic/block.
- [ ] Track remaining item count, first error, and a once-closed done channel. Do not wake on the first error. Add the tracker reference to each derive item.
- [ ] Each handler counts valid rows, creates one tracker, enqueues every item, accounts for unqueued items on error, then waits for all terminal completions.
- [ ] Include message ID/space/dataset/kind only as diagnostic fields, never as metric labels.

### Task 3: Report every batch result and prove idempotency

**Files:**
- Modify: `modules/storage/internal/service/view/builder/service.go`, `time_series.go`, `record.go` and tests
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_test.go`
- Modify: `modules/storage/internal/infra/device/bleve/index_test.go`

- [ ] Add RED tests for successful batch, DuckDB failure, Bleve failure, and multi-item completion timing.
- [ ] Stop discarding batch errors. Decorate errors with fixed diagnostic context (engine, view ID, batch row count) and complete every batch item with the shared batch result.
- [ ] Replay identical DuckDB rows and Bleve documents twice; assert stored values, row/search count, and EntryCount do not duplicate.
- [ ] Run:

```bash
cd modules/storage
go test ./internal/service/view/builder -count=1
CGO_ENABLED=1 go test ./internal/infra/device/duckdb ./internal/infra/device/bleve -count=1
```

### Task 4: Implement the delivery state machine

**Files:**
- Modify: `modules/storage/internal/infra/eventbus/producer_bus.go`, `producer_bus_test.go`

- [ ] Extract a narrow internal delivery interface and deterministic `processDelivery` test surface.
- [ ] Add RED tests for handler-success-before-AckSync ordering, error-to-NakWithDelay, ACK failure without a follow-up Nak, periodic `InProgress`, heartbeat failure without changing the terminal action, heartbeat stopped/joined before ACK/Nak, and deadlines on every transport action context.
- [ ] Use a fresh bounded timeout context for Ack/Nak/InProgress, not the cancelled fetch context. ACK failure is logged/metriced and left for natural redelivery.
- [ ] Add bounded handler concurrency using a semaphore and handler wait group. Prove peak work never exceeds `max_in_flight` and multiple deliveries can enter ViewBuilder batching.
- [ ] If fetch cancellation occurs before a fetched delivery acquires capacity, do not dispatch or ACK it.
- [ ] Run race tests:

```bash
cd modules/storage
go test -race ./internal/infra/eventbus -count=1
```

### Task 5: Close intake before workers and drain safely

**Files:**
- Modify Subscriber/Subscription close logic in `modules/storage/internal/infra/eventbus/producer_bus.go`
- Modify builder/batcher close logic under `modules/storage/internal/service/view/builder/`
- Modify runtime assembly/close ordering in `modules/storage/cmd/server/main.go` or the backend refactor runtime owner
- Add focused close tests

- [ ] Add tests for a blocked handler during close, tail-batch flush, timeout/cancel, idempotent close, and no delivery accepted/ACKed after the last handler is removed.
- [ ] Closing the last subscription for a message kind must stop that consumer fetch and wait for already-dispatched handlers before unregistering the View handler.
- [ ] Close order: stop primary/outbox publishing, stop/finalize SubscriberBus deliveries, close/flush builder, then close derived engines and JetStream client.
- [ ] Prevent concurrent `add` versus cancel-drain: first block intake and wait handlers, only then cancel/drain batchers. Never rely on a momentarily empty channel.
- [ ] Run:

```bash
cd modules/storage
go test -race ./internal/infra/eventbus ./internal/service/view/builder ./cmd/server -count=1
```

### Task 6: Add low-cardinality metrics and structured logs

**Files:**
- Create: `modules/storage/internal/observability/view_metrics.go`, `view_metrics_test.go`
- Inject metrics into builder and SubscriberBus

- [ ] Use an injectable Prometheus Registerer in tests and the production registry in bootstrap. Avoid duplicate registration across tests/runtimes.
- [ ] Cover: derive event terminal total (`success|error`), batch duration/result, in-flight event gauge, delivery action/result (`ack|nak|in_progress`), and redelivery count (`DeliveryCount > 1`). Labels must be fixed enums only.
- [ ] Log message ID/delivery count/space/dataset/kind and engine/view/batch rows as fields; never log row contents or use identifiers as labels.

### Task 7: Preserve primary-write behavior and MemoryBus semantics

**Files:**
- Modify tests: `modules/storage/internal/core/eventbus/bus_test.go`, `modules/storage/internal/service/access/data_test.go`
- Modify: `modules/storage/README.md`

- [ ] Prove MemoryBus publish waits for builder terminal completion and returns its error.
- [ ] Prove Access records/reports a derived failure but a successful Pebble primary write remains successful.
- [ ] Document that primary-write success does not promise immediate View query visibility; durable JetStream retries drive convergence.

### Task 8: Add real failure/retry E2E

**Files:**
- Create: `modules/storage/test/view_derivation_reliability_test.go`

- [ ] Boot embedded NATS with real JetStream durable consumers and real DuckDB/Bleve; wrap each derived engine with deterministic fail-once behavior.
- [ ] Cover DuckDB fail -> Nak/redelivery -> success/Ack, Bleve same, merged events retry without duplicates, long processing with InProgress/no delivery-count increase, and shutdown yielding completion or redelivery but never silent loss.
- [ ] Keep ACK/heartbeat transport-failure injection in narrow unit tests; use broker disconnect/reconnect for E2E unconfirmed-redelivery proof.
- [ ] Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -tags=integration ./test -run 'TestViewDerivationReliability' -count=2
go test -race ./internal/core/eventbus ./internal/infra/eventbus ./internal/service/view/builder -count=1
go test ./... -count=1
```

### Task 9: Commit and independent task review

- [ ] Commit the complete Storage behavior as one coherent unit or narrowly separated config/builder/transport commits, record the base SHA, and generate a whole-range review package.
- [ ] Require a fresh read-only reviewer to focus on zero-handler ACK windows, fetch-context cancellation, heartbeat terminal races, ACK-failure behavior, batcher tail loss, idempotency, and close order.
- [ ] Fix every Critical and Important finding, rerun all commands above, and update `.superpowers/sdd/progress.md`.

```bash
git commit -m "fix(storage): acknowledge view events after materialization"
```
