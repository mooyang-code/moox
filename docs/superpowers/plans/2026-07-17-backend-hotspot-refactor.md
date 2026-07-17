# Backend Hotspot Refactor Plan

> **Worker requirement:** Characterize lifecycle behavior before moving code. This is a responsibility extraction, not a behavior redesign.

**Goal:** Reduce Storage server and Monitor bootstrap hotspots into focused, testable runtime owners with explicit shutdown and no leaked background goroutines.

---

### Task 1: Characterize Storage server assembly

**Files:**
- Modify: `modules/storage/cmd/server/main_test.go`
- Modify existing bootstrap/role tests under `modules/storage/internal/bootstrap/`

- [ ] Add tests locking flag/config resolution, role selection, service names, health/reporting registration, view maintenance scheduling, EventBus assembly, retention start/stop, and reverse-order close.
- [ ] Prove the current lifetime defect: retention and View/EventBus shutdown must be owned by one context and cannot accept/ACK new events after View workers stop.
- [ ] Run RED/characterization tests before extraction:

```bash
cd modules/storage
go test ./cmd/server ./internal/bootstrap/... -count=1
```

### Task 2: Extract a Storage runtime owner

**Files:**
- Reduce: `modules/storage/cmd/server/main.go`
- Create: `modules/storage/internal/bootstrap/runtime.go`
- Create: `modules/storage/internal/bootstrap/roles.go`
- Create: `modules/storage/internal/bootstrap/config.go`
- Create: `modules/storage/internal/bootstrap/eventbus.go`
- Create: `modules/storage/internal/bootstrap/view_runtime.go`
- Create: `modules/storage/internal/bootstrap/health.go`
- Create matching focused `*_test.go` files

- [ ] Keep `main.go` limited to flags, config/bootstrap construction, Serve, Runtime.Close, and tracing shutdown.
- [ ] Give `Runtime` ownership of every closer, cancel function, and goroutine. Close intake/SubscriberBus before removing View handlers or cancelling batch workers; drain in-flight materializations before engine close.
- [ ] Move role predicates/names/summaries, config/path/deployment validation, EventBus/readers, View/query/maintenance/timers, and health/reporting into named files. Do not create `utils.go`.
- [ ] Make retention cancellable and waitable. Add idempotent Close and partial-initialize unwind tests.
- [ ] Run:

```bash
cd modules/storage
go test ./... -count=1
go test -race ./cmd/server ./internal/bootstrap/... -count=1
```

### Task 3: Characterize Monitor lifecycle

**Files:**
- Split tests from `modules/monitor/internal/bootstrap/bootstrap_test.go`

- [ ] Lock host metric ingestion, storage readiness gate, target normalization, metric dedupe/reporter/scheduler, probe result hook/scheduler, peer discovery/sync/pull, retention, health, partial init cleanup, context cancellation, and Close idempotency.
- [ ] Add a regression test preventing a Runtime-owned worker from synchronously calling `Runtime.Close()` and waiting on itself.

### Task 4: Extract Monitor responsibilities

**Files:**
- Reduce: `modules/monitor/internal/bootstrap/bootstrap.go`
- Create: `host_metrics.go`, `metric_pipeline.go`, `probe_runtime.go`, `peer_runtime.go`, `health.go`, `retention.go` in the same package
- Create matching focused tests

- [ ] Keep `Runtime`, `Initialize`, and top-level assembly in `bootstrap.go`.
- [ ] Make the lifecycle owner initiate Close; workers only observe runtime context. Ensure every scheduler/consumer/cleaner is in the wait group and every partial init error unwinds already-open resources.
- [ ] Run:

```bash
cd modules/monitor
go test ./... -count=1
go test -race ./internal/bootstrap ./test -count=1
```

### Task 5: Commit and independent review

- [ ] Commit Storage and Monitor separately. Review public behavior, ownership graph, close order, and race output with a fresh read-only Agent.

```bash
git commit -m "refactor(storage): move server assembly into bootstrap"
git commit -m "refactor(monitor): split runtime bootstrap responsibilities"
```
