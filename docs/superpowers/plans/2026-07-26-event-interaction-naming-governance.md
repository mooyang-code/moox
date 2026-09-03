# Event Interaction Naming Governance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move every governed-event adapter into a lifecycle-owned role package, remove ambiguous event names and paths, and enforce the new layout without changing the five event contracts or delivery behavior.

**Architecture:** Shared semantics remain in `packages/events`, shared transport remains in `packages/jetstream`, and Broker topology remains in `modules/eventbus`. Business code uses the fixed roles `outbox`, `inbox`, `eventpublisher`, `eventconsumer`, and `eventmapper`, placed below the lifecycle owner. Existing module tests and E2E flows prove that this physical refactor preserves Outbox, Inbox, ordering, retry, and ACK behavior.

**Tech Stack:** Go 1.25, Go workspaces, Protocol Buffers, NATS JetStream, Pebble, SQLite/GORM, Bash repository checks.

---

## File Structure

Create or move code into these final packages:

```text
modules/strategy/internal/outbox/
modules/storage/internal/eventmapper/
modules/storage/internal/service/datanode/outbox/
modules/storage/internal/service/view/eventconsumer/
modules/factor/internal/trigger/eventconsumer/
modules/archive/internal/eventconsumer/
modules/trade/internal/eventconsumer/
modules/monitor/internal/hostmetrics/eventconsumer/
modules/monitor/internal/metrics/eventconsumer/
modules/hostagent/internal/eventpublisher/
```

Keep owner-local persistence where it already belongs:

```text
modules/strategy/internal/domain/outbox.go
modules/strategy/internal/store/outbox.go
modules/factor/internal/store/event_inbox.go
modules/storage/internal/service/datanode/pebble/outbox_message.go
modules/trade/internal/infra/store/
```

Do not add `internal/eventing`, a generic shared Outbox/Inbox framework, old-path aliases, or transitional configuration keys.

### Task 1: Make the Naming Rules Fail on the Current Tree

**Files:**
- Modify: `scripts/check/check-package-boundaries.sh`
- Modify: `scripts/check/verify-event-contracts.sh`

- [ ] **Step 1: Add structural rejection checks**

Add these exact legacy path and symbol checks to `scripts/check/check-package-boundaries.sh`:

```bash
event_legacy_paths=(
  'modules/strategy/internal/bus'
  'modules/storage/internal/eventcontract'
  'modules/archive/internal/consumer'
)
for path in "${event_legacy_paths[@]}"; do
  [[ ! -e "${path}" ]] || violations+=("${path}: legacy event interaction path is not allowed")
done

while IFS= read -r match; do
  violations+=("${match}: transport-named business Consumer API is not allowed")
done < <(rg -n '\b(type|func New)NATSConsumer\b|\btype NATSConfig\b' \
  modules --glob '*.go' --glob '!**/*_test.go' || true)

while IFS= read -r match; do
  violations+=("${match}: eventconsumer packages must not declare Publisher implementations")
done < <(rg -n '^type [A-Za-z0-9_]*Publisher struct' \
  modules --glob '*/eventconsumer/*.go' --glob '!**/*_test.go' || true)
```

Update `scripts/check/verify-event-contracts.sh` test paths to the intended package
names before moving code:

```bash
(cd modules/archive && go test ./internal/config ./internal/eventconsumer)
(cd modules/factor && CGO_ENABLED=1 go test ./internal/store ./internal/bootstrap ./internal/trigger/eventconsumer)
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/store ./internal/outbox ./test)
(cd modules/trade && CGO_ENABLED=1 go test ./internal/bootstrap ./internal/eventconsumer ./internal/application/rebalance)
```

- [ ] **Step 2: Run the checks and verify RED**

Run:

```bash
bash scripts/check/check-package-boundaries.sh
```

Expected: FAIL listing `modules/strategy/internal/bus`,
`modules/storage/internal/eventcontract`, `modules/archive/internal/consumer`,
and `NATSConsumer`.

Run:

```bash
bash scripts/check/verify-event-contracts.sh
```

Expected: FAIL because the new package paths do not exist.

### Task 2: Rename the Strategy Outbox

**Files:**
- Move: `modules/strategy/internal/bus/outbox.go` -> `modules/strategy/internal/outbox/relay.go`
- Move: `modules/strategy/internal/bus/outbox_test.go` -> `modules/strategy/internal/outbox/relay_test.go`
- Move: `modules/strategy/internal/bus/publisher.go` -> `modules/strategy/internal/outbox/publisher.go`
- Move: `modules/strategy/internal/bus/publisher_test.go` -> `modules/strategy/internal/outbox/publisher_test.go`
- Move: `modules/strategy/internal/bus/runtime.go` -> `modules/strategy/internal/outbox/runtime.go`
- Move: `modules/strategy/internal/bus/runtime_test.go` -> `modules/strategy/internal/outbox/runtime_test.go`
- Modify: `modules/strategy/internal/bootstrap/bootstrap.go`
- Modify: `modules/strategy/internal/bootstrap/health_test.go`
- Modify: `modules/strategy/test/outbox_jetstream_e2e_test.go`
- Modify: `modules/strategy/test/strategy_trade_external_e2e_test.go`
- Modify: `docs/策略模块架构设计.md`

- [ ] **Step 1: Move files and rename the package**

Change every moved file to:

```go
package outbox
```

Keep `Relay`, `Runtime`, `RuntimeConfig`, `JetStreamPublisher`, and their
interfaces behaviorally unchanged. The package qualifier now supplies the
architectural role.

- [ ] **Step 2: Update imports and wiring**

Replace:

```go
strategybus "github.com/mooyang-code/moox/modules/strategy/internal/bus"
```

with:

```go
strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
```

Update all `strategybus.*` references to `strategyoutbox.*`.

- [ ] **Step 3: Run focused Strategy tests**

Run:

```bash
cd modules/strategy
CGO_ENABLED=1 go test -count=1 ./internal/outbox ./internal/bootstrap ./internal/store ./test
CGO_ENABLED=1 go test -race -count=1 ./internal/outbox ./internal/store
```

Expected: PASS. `rg -n 'internal/bus|strategybus' modules/strategy docs/策略模块架构设计.md`
must return no active references.

### Task 3: Split Storage Mapper and DataNode Outbox

**Files:**
- Move: `modules/storage/internal/eventcontract/rows.go` -> `modules/storage/internal/eventmapper/rows.go`
- Move: `modules/storage/internal/eventcontract/rows_test.go` -> `modules/storage/internal/eventmapper/rows_test.go`
- Move: `modules/storage/internal/service/datanode/outbox_relay.go` -> `modules/storage/internal/service/datanode/outbox/relay.go`
- Move: `modules/storage/internal/service/datanode/outbox_relay_test.go` -> `modules/storage/internal/service/datanode/outbox/relay_test.go`
- Move: `modules/storage/internal/service/view/eventconsumer/dataset_publisher.go` -> `modules/storage/internal/service/datanode/outbox/publisher.go`
- Move: `modules/storage/internal/service/view/eventconsumer/dataset_publisher_test.go` -> `modules/storage/internal/service/datanode/outbox/publisher_test.go`
- Move: `modules/storage/internal/service/datanode/pebble/event.go` -> `modules/storage/internal/service/datanode/pebble/outbox_message.go`
- Move: `modules/storage/internal/service/datanode/pebble/event_test.go` -> `modules/storage/internal/service/datanode/pebble/outbox_message_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/store.go`
- Modify: `modules/storage/internal/service/datanode/service.go`
- Modify: `modules/storage/cmd/server/main.go`

- [ ] **Step 1: Rename the mapper API**

The final mapper API is:

```go
package eventmapper

func ToEventRows(in *storagegen.RowsUpserted) (*storagepb.DatasetRowsUpserted, error)
func ToStorageRows(in *storagepb.DatasetRowsUpserted) (*storagegen.RowsUpserted, error)
```

Preserve the current nil, required identity, row-key identity, deterministic
typed-value, and default UPSERT behavior.

- [ ] **Step 2: Move the DataNode relay and publisher**

Use:

```go
package outbox

type Publisher interface {
    PublishMessage(context.Context, []byte) error
}

type Relay struct {
    // existing fields and behavior
}

func NewRelay(store *pebble.Store, publisher Publisher, opts RelayOptions) (*Relay, error)
```

Rename `OutboxRelayOptions` to `RelayOptions`, `OutboxRelay` to `Relay`, and
`NewOutboxRelay` to `NewRelay`. Move `DatasetPublisher` into this package and
rename it `JetStreamPublisher`.

Delete the unused method:

```go
func (p *DatasetPublisher) Publish(ctx context.Context, event *pb.RowsUpserted, outboxID uint64) error
```

Delete the ignored `nodeID` argument from the publisher constructor.

- [ ] **Step 3: Keep atomic message binding in Pebble**

Rename only the file to `outbox_message.go`. Keep
`BuildDatasetRowsUpsertedMessage`,
`BuildDatasetRowsUpsertedMessageForSource`, and `BindOutboxID` in package
`pebble` so Outbox-ID allocation remains inside the atomic storage boundary.
Update mapper calls to `eventmapper.ToEventRows`.

- [ ] **Step 4: Update server wiring**

Use:

```go
publisher := outbox.NewJetStreamPublisher(client)
relay, err := outbox.NewRelay(svc.Store(), publisher, outbox.RelayOptions{})
```

Keep startup, shutdown, metrics, contiguous-prefix deletion, and duplicate
PubAck handling unchanged.

- [ ] **Step 5: Run focused Storage producer tests**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -count=1 ./internal/eventmapper ./internal/service/datanode/... ./cmd/server
CGO_ENABLED=1 go test -race -count=1 ./internal/service/datanode/outbox ./internal/service/datanode/pebble
```

Expected: PASS.

### Task 4: Put the Real Storage View Consumer in `eventconsumer`

**Files:**
- Create: `modules/storage/internal/service/view/eventconsumer/consumer.go`
- Create: `modules/storage/internal/service/view/eventconsumer/handler.go`
- Move: `modules/storage/internal/service/view/delivery_policy.go` -> `modules/storage/internal/service/view/eventconsumer/delivery_policy.go`
- Move: `modules/storage/internal/service/view/delivery_policy_test.go` -> `modules/storage/internal/service/view/eventconsumer/delivery_policy_test.go`
- Move: `modules/storage/internal/service/view/delivery_heartbeat.go` -> `modules/storage/internal/service/view/eventconsumer/delivery_heartbeat.go`
- Move: `modules/storage/internal/service/view/subject_dispatcher.go` -> `modules/storage/internal/service/view/eventconsumer/subject_dispatcher.go`
- Move: `modules/storage/internal/service/view/subject_dispatcher_test.go` -> `modules/storage/internal/service/view/eventconsumer/subject_dispatcher_test.go`
- Modify: `modules/storage/internal/service/view/consume.go`
- Modify: `modules/storage/internal/service/view/event_apply.go`
- Delete: `modules/storage/internal/service/view/eventconsumer/subject.go`
- Delete: `modules/storage/internal/service/view/eventconsumer/subject_test.go`
- Modify: `modules/storage/internal/service/e2e/view_consumer_concurrency_e2e_test.go`

- [ ] **Step 1: Define a typed owner handler**

Create:

```go
package eventconsumer

type DatasetRowsHandler interface {
    HandleDatasetRows(
        context.Context,
        *eventpb.EventMessage,
        *storagepb.DatasetRowsUpserted,
    ) error
}

type DatasetRowsHandlerFunc func(
    context.Context,
    *eventpb.EventMessage,
    *storagepb.DatasetRowsUpserted,
) error

func (f DatasetRowsHandlerFunc) HandleDatasetRows(
    ctx context.Context,
    message *eventpb.EventMessage,
    payload *storagepb.DatasetRowsUpserted,
) error {
    return f(ctx, message, payload)
}
```

The child package must not import its parent `view` package.

- [ ] **Step 2: Move transport lifecycle into the child package**

Create a `Consumer` with `Config` that owns:

- `events.NewConsumer`
- fetch loop
- subject-lane dispatcher
- queued delivery heartbeat
- retry/TERM policy
- metrics hooks

Decode with:

```go
message, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(
    registry,
    delivery.RawData,
    delivery.Subject,
    delivery.RawMessageID,
    delivery.ContentType,
)
```

Decode or identity failures are permanent. Handler failures retain the current
bounded retry policy.

- [ ] **Step 3: Keep View application in the owner**

`view.Service.StartEventConsumer` becomes a thin wiring method. Its handler
calls `eventmapper.ToStorageRows(payload)` and then the existing
`applyDatasetEvent` logic.

Delete:

```go
DatasetRowsUpsertedSubjectPrefix
DatasetRowsUpsertedSubject
ParseDatasetRowsUpsertedSubject
```

Tests derive subjects with `events.Registry`, not local helpers.

- [ ] **Step 4: Run View ordering and race tests**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -count=1 ./internal/service/view/eventconsumer ./internal/service/view ./internal/service/e2e
CGO_ENABLED=1 go test -race -count=1 ./internal/service/view/eventconsumer ./internal/service/view
```

Expected: PASS, including same-subject ordering, independent-subject
parallelism, heartbeat, retry, and backfill tests.

### Task 5: Rename Factor and Archive Consumers

**Files:**
- Move: `modules/factor/internal/trigger/nats.go` -> `modules/factor/internal/trigger/eventconsumer/consumer.go`
- Move: `modules/factor/internal/trigger/nats_test.go` -> `modules/factor/internal/trigger/eventconsumer/consumer_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/config/app.yaml`
- Move: `modules/archive/internal/consumer/` -> `modules/archive/internal/eventconsumer/`
- Modify: `modules/archive/internal/bootstrap/app.go`
- Modify: `modules/archive/internal/bootstrap/app_test.go`
- Modify: `modules/archive/test/archive_e2e_test.go`

- [ ] **Step 1: Extract Factor Event Consumer**

Use:

```go
package eventconsumer

type Config struct {
    URLs           []string
    FetchMaxWait   time.Duration
    CredentialFile string
}

type Consumer struct {
    cfg     Config
    batcher *trigger.EventBatcher
    // existing session and readiness fields
}

func New(cfg Config, batcher *trigger.EventBatcher) *Consumer
```

Keep the JetStream session private. Rename `LiveConsumer` to
`DatasetRowsConsumerName`. Remove the unused `LiveStream` constant.

- [ ] **Step 2: Rename Factor configuration**

Change:

```yaml
nats:
```

to:

```yaml
eventbus:
```

Rename bootstrap `NATSConfig` and field `NATS` to `EventBusConfig` and
`EventBus`. Do not accept the old YAML key.

- [ ] **Step 3: Rename Archive package**

Change all moved Archive files to:

```go
package eventconsumer
```

Update bootstrap imports to:

```go
eventconsumer "github.com/mooyang-code/moox/modules/archive/internal/eventconsumer"
```

Keep Decoder, Handler, Runner, journal-sync-before-ACK, quarantine, and retry
behavior unchanged.

- [ ] **Step 4: Run Factor and Archive tests**

Run:

```bash
cd modules/factor
CGO_ENABLED=1 go test -count=1 ./internal/store ./internal/trigger ./internal/trigger/eventconsumer ./internal/bootstrap ./test
CGO_ENABLED=1 go test -race -count=1 ./internal/trigger ./internal/trigger/eventconsumer ./internal/store

cd ../archive
go test -count=1 ./internal/config ./internal/eventconsumer ./internal/bootstrap ./test
go test -race -count=1 ./internal/eventconsumer
```

Expected: PASS.

### Task 6: Extract Trade, Monitor, and HostAgent Adapters

**Files:**
- Create: `modules/trade/internal/eventconsumer/rebalance.go`
- Create: `modules/trade/internal/eventconsumer/rebalance_test.go`
- Modify: `modules/trade/internal/bootstrap/kernel_workers.go`
- Modify: `modules/trade/internal/bootstrap/kernel_workers_test.go`
- Modify: `modules/trade/internal/bootstrap/strategy_rebalance_event_e2e_test.go`
- Create: `modules/monitor/internal/hostmetrics/eventconsumer/consumer.go`
- Create: `modules/monitor/internal/hostmetrics/eventconsumer/consumer_test.go`
- Modify: `modules/monitor/internal/hostmetrics/hostmetrics.go`
- Create: `modules/monitor/internal/metrics/eventconsumer/consumer.go`
- Create: `modules/monitor/internal/metrics/eventconsumer/consumer_test.go`
- Modify: `modules/monitor/internal/metrics/consumer.go`
- Modify: `modules/monitor/internal/bootstrap/host_runtime.go`
- Modify: `modules/monitor/internal/bootstrap/metrics_runtime.go`
- Create: `modules/hostagent/internal/eventpublisher/publisher.go`
- Create: `modules/hostagent/internal/eventpublisher/publisher_test.go`
- Modify: `modules/hostagent/internal/app/app.go`
- Modify: `modules/hostagent/internal/app/app_test.go`

- [ ] **Step 1: Extract Trade rebalance consumption**

Expose:

```go
package eventconsumer

type RebalanceOptions struct {
    Client       *jetstream.Client
    ConsumerName string
    Store        *store.Store
    Engine       *command.Engine
    Wake         func()
}

func RunRebalance(ctx context.Context, opts RebalanceOptions) error
func HandleRebalance(
    ctx context.Context,
    delivery *jetstream.Delivery,
    opts RebalanceOptions,
) jetstream.HandlerResult
```

Bootstrap owns connection creation and goroutine startup. The new package owns
binding, decode, Inbox lookup, request planning, transactional application,
ACK classification, and reconnect.

- [ ] **Step 2: Extract Monitor transport lifecycle**

Host metrics child package accepts the parent Store:

```go
type Consumer struct {
    pull  *events.Consumer
    store *hostmetrics.Store
}

func Bind(ctx context.Context, client *jetstream.Client, store *hostmetrics.Store) (*Consumer, error)
```

Change `hostmetrics.Store` to accept typed EventMessage and HostMetric values
for validation and persistence; it must not accept `jetstream.Delivery`.

Metrics child package owns the current `ConsumerOptions`, `Consumer`,
`NewConsumer`, `RunWhenReady`, and ACK classification. It invokes typed parent
methods for authorization, storage, and message persistence.

- [ ] **Step 3: Extract HostAgent publishing**

Define:

```go
package eventpublisher

type Publisher interface {
    PublishHostMetric(
        context.Context,
        string,
        *hostmetricpb.HostMetric,
        time.Time,
    ) error
    Ready() bool
    Close() error
}
```

The JetStream implementation owns Registry encoding, connection, publish, and
close. Agent keeps collection cadence, message ID generation, counters, latest
snapshot, and status.

- [ ] **Step 4: Run focused tests and race tests**

Run:

```bash
cd modules/trade
CGO_ENABLED=1 go test -count=1 ./internal/eventconsumer ./internal/bootstrap ./internal/application/rebalance ./test
CGO_ENABLED=1 go test -race -count=1 ./internal/eventconsumer ./internal/application/rebalance

cd ../monitor
go test -count=1 ./internal/hostmetrics/... ./internal/metrics/... ./internal/bootstrap ./test
go test -race -count=1 ./internal/hostmetrics/... ./internal/metrics/...

cd ../hostagent
go test -count=1 ./internal/eventpublisher ./internal/app
go test -race -count=1 ./internal/eventpublisher ./internal/app
```

Expected: PASS.

### Task 7: Update Governance, Current Documentation, and Verification Paths

**Files:**
- Modify: `scripts/check/check-package-boundaries.sh`
- Modify: `scripts/check/verify-event-contracts.sh`
- Modify: `docs/架构总览.md`
- Modify: `docs/协议设计.md`
- Modify: `docs/存储层架构.md`
- Modify: `docs/策略模块架构设计.md`
- Modify: `docs/因子计算模块设计.md`
- Modify: `modules/archive/README.md`
- Modify: `modules/factor/README.md`
- Modify: `modules/trade/README.md`

- [ ] **Step 1: Finish structural checks**

Keep the RED checks from Task 1 and add exact removed-symbol checks:

```bash
event_legacy_symbols='DatasetRowsUpsertedSubjectPrefix|DatasetRowsUpsertedSubject|ParseDatasetRowsUpsertedSubject|ToSharedRows|ToLocalRows|NATSConsumer'
while IFS= read -r match; do
  violations+=("${match}: legacy event interaction symbol is not allowed")
done < <(rg -n "${event_legacy_symbols}" modules scripts \
  --glob '*.go' --glob '*.sh' || true)
```

Scan current documentation but exclude dated execution history:

```bash
current_event_docs=(
  docs/架构总览.md
  docs/协议设计.md
  docs/存储层架构.md
  docs/策略模块架构设计.md
  docs/因子计算模块设计.md
  modules/archive/README.md
  modules/factor/README.md
  modules/trade/README.md
)
```

- [ ] **Step 2: Update current architecture text**

Document the fixed vocabulary and final package tree. Preserve the distinction:

```text
packages/events  = governed business event semantics
packages/jetstream = transport
modules/eventbus = Broker topology and control
outbox/inbox/eventpublisher/eventconsumer/eventmapper = owner-local roles
```

Do not rewrite dated files in `docs/superpowers/plans` or old review reports.

- [ ] **Step 3: Run governance checks and verify GREEN**

Run:

```bash
bash scripts/check/check-package-boundaries.sh
bash scripts/check/verify-event-contracts.sh
```

Expected: both PASS.

### Task 8: Full Acceptance, Independent Review, and Branch Closure

**Files:**
- Test: all touched packages and module E2E directories
- Inspect: `git diff`, active paths, current docs, configuration, and remote ref

- [ ] **Step 1: Run cross-module acceptance**

Run:

```bash
bash scripts/check/check-package-boundaries.sh
bash scripts/check/verify-event-contracts.sh
bash scripts/test/contract/test-go-workspace.sh
make verify-pr
git diff --check
```

Expected: PASS.

- [ ] **Step 2: Run focused race and E2E proving set**

Run:

```bash
(cd modules/storage && CGO_ENABLED=1 go test -race -count=1 ./internal/eventmapper ./internal/service/datanode/... ./internal/service/view/... ./internal/service/e2e)
(cd modules/factor && CGO_ENABLED=1 go test -race -count=1 ./internal/store ./internal/trigger/... ./test)
(cd modules/archive && go test -race -count=1 ./internal/eventconsumer ./internal/bootstrap ./test)
(cd modules/strategy && CGO_ENABLED=1 go test -race -count=1 ./internal/store ./internal/outbox ./test)
(cd modules/trade && CGO_ENABLED=1 go test -race -count=1 ./internal/eventconsumer ./internal/bootstrap ./internal/application/rebalance ./test)
(cd modules/monitor && go test -race -count=1 ./internal/hostmetrics/... ./internal/metrics/... ./internal/bootstrap ./test)
(cd modules/hostagent && go test -race -count=1 ./internal/eventpublisher ./internal/app)
```

Expected: PASS.

- [ ] **Step 3: Search for structural residue**

Run:

```bash
find modules -type d | rg '/(bus|eventcontract)$'
rg -n '\bNATSConsumer\b|internal/bus|eventcontract|DatasetRowsUpsertedSubjectPrefix|ToSharedRows|ToLocalRows' \
  modules scripts docs/架构总览.md docs/协议设计.md docs/存储层架构.md \
  docs/策略模块架构设计.md docs/因子计算模块设计.md
```

Expected: no active-code or current-document matches.

- [ ] **Step 4: Start a fresh Agent for independent review**

Give the reviewer the approved design, implementation plan, complete diff, and
verification results. Require review of:

- role ownership and package cycles
- every module named in the design
- preserved Outbox/Inbox/ACK/order behavior
- config and bootstrap wiring
- boundary-check coverage and false negatives
- old paths, symbols, tests, and current docs

Fix every actionable finding and rerun the affected focused tests plus the
governance checks.

- [ ] **Step 5: Commit and push**

Use focused Conventional Commits:

```bash
git commit -m "refactor(events): align producer and storage event roles"
git commit -m "refactor(events): align module consumer roles"
git commit -m "test(events): enforce interaction package governance"
```

Push the completed commits to `feature/mooyang`, then verify:

```bash
git rev-parse HEAD
git ls-remote --heads origin feature/mooyang
git status --short --branch
```

Expected: local HEAD equals the remote branch SHA. Only pre-existing,
user-owned worktree modifications may remain outside this implementation.
