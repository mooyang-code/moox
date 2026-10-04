# tRPC Timer And Host Metrics Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace suitable process-owned tickers, including Gateway route refresh and HostAgent sampling, with observable, non-overlapping tRPC Timer jobs and rebuild Storage host-metrics cleanup so it deletes data older than 48 hours in bounded multi-batch runs.

**Architecture:** Keep retries, heartbeats, Factor event batching, outbox relays, and in-memory state loops as Go timers. Move independent database scans and maintenance jobs behind synchronous tRPC Timer handlers. Every migrated handler uses a shared guard that clones the timer context, applies an explicit timeout, prevents overlap, and emits bounded metrics. Storage owns host-metrics deletion because Pebble is the fact source; Monitor remains only a producer and reader of those datasets.

**Tech Stack:** Go 1.24, tRPC-Go, `trpc-database/timer`, robfig/cron, Prometheus, Pebble, SQLite, BadgerDB, COS, YAML, testify.

---

## Decisions And Scope

- There is no compatibility requirement. Delete superseded ticker loops and duration fields instead of supporting both mechanisms.
- `trpc.moox.monitor.sysdeploy.timer` is already migrated and is the reference registration pattern; do not reimplement it.
- Timer handlers must finish synchronously. A handler must not ACK success while work continues in an untracked goroutine.
- `timer.DefaultScheduler` is local and does not provide distributed exclusion. Database operations must remain idempotent; the shared guard only prevents overlap inside one process.
- Use fixed, low-cardinality job names in logs and metrics. Never label metrics by Space ID, Dataset ID, account, order, peer, message ID, or run ID.
- Cron is the only scheduling source after migration. Remove application fields that only expressed the old ticker interval.
- tRPC Timer v1.0.0 supports immediate startup execution through the cron query parameter `?startAtOnce=1`. The call is synchronous inside `ListenAndServe`; if this first handler call returns an error, that Timer service fails to start and the process startup path must surface the error.
- Use `startAtOnce=1` only for jobs explicitly marked "start immediately" in the inventory. Their first-run failure is intentionally fail-fast so the process supervisor retries startup instead of advertising a service whose mandatory initial maintenance/reconciliation/sampling pass failed. Do not set the package-global `timer.SetStartAtOnce`, because it would change unrelated Timer services.
- Storage host-metrics cleanup deletes rows strictly older than `now - 48h`. It never deletes exactly-at-cutoff or newer rows.
- One Storage run processes at most 10 batches per configured dataset and at most 1000 rows per resolved target per batch. It stops early when a batch deletes zero rows.
- A failure in one Storage dataset is recorded but does not prevent cleanup of the remaining configured datasets. The handler returns `errors.Join` after all datasets have been attempted.
- Factor's current `Debouncer` is a fixed-window event aggregator, not classic debounce: later events do not extend the bucket deadline. Rename it to `trigger.EventBatcher` and rename `debounce_window_ms` to `event_batch_window_ms` without a compatibility alias.
- Factor event batching remains a process-owned timer. Its buckets belong to one in-memory runtime instance, use a sub-second polling interval, and must stop with that instance; a service-level cron handler would not improve durability or ownership.
- Gateway route refresh is a module-level fixed-frequency `Runtime.Refresh(ctx)` operation. Keep mandatory startup initialization synchronous, then execute refresh every 15 seconds through `trpc.moox.gateway.route_refresh.timer`; remove the application-level refresh interval. Do not also configure `startAtOnce=1`, because `Runtime.Initialize` already performs the immediate pull with cache-aware startup semantics and a Timer invocation would duplicate it.
- HostAgent sampling is a module-level fixed-frequency `RunOnce(ctx)` operation. Execute it every 15 seconds through `trpc.moox.hostagent.sample.timer` with `startAtOnce=1`; keep the Agent's atomic guard so scheduled and manual runs cannot overlap. The immediate handler is the sole replacement for the old eager `Agent.Run` call, and an initial collection/publish error intentionally fails startup.

## Timer Migration Inventory

| Current loop | Decision | New timer service | Schedule |
| --- | --- | --- | --- |
| Admin Badger value-log GC | Migrate | `trpc.moox.admin.auth_cache_cleanup.timer` | every 5 minutes |
| Monitor result/alert cleanup | Migrate with metric dedupe cleanup | `trpc.moox.monitor.data_cleanup.timer` | every 6 hours, start immediately |
| Monitor metric-message dedupe cleanup | Migrate with history cleanup | `trpc.moox.monitor.data_cleanup.timer` | every 6 hours, start immediately |
| Storage host dataset cleanup | Migrate and redesign | `trpc.moox.storage.host_metrics_cleanup.timer` | hourly, start immediately |
| Archive dirty-partition materialization | Migrate | `trpc.moox.archive.materialize.timer` | every 10 minutes, start immediately |
| Archive COS synchronization | Migrate | `trpc.moox.archive.cos_sync.timer` | hourly, start immediately |
| Gateway route refresh | Migrate | `trpc.moox.gateway.route_refresh.timer` | every 15 seconds; startup initialization remains synchronous |
| HostAgent collection/publish | Migrate | `trpc.moox.hostagent.sample.timer` | every 15 seconds, start immediately |
| Monitor due-check scan | Migrate | `trpc.moox.monitor.check_schedule.timer` | every 30 seconds, start immediately |
| Monitor metric-rule evaluation | Migrate | `trpc.moox.monitor.metric_rule.timer` | every minute, start immediately |
| Monitor peer pull/stale marking | Migrate | `trpc.moox.monitor.peer_sync.timer` | every 10 seconds, start immediately |
| Trade fill reconciliation | Migrate | `trpc.moox.trade.fill_reconcile.timer` | every 5 seconds, start immediately |
| Trade recoverable-order scan | Migrate | `trpc.moox.trade.order_recovery.timer` | every 15 seconds, start immediately |

The following timers stay as Go timers: Storage/Strategy/Trade outbox relays, Factor event-batch flush and binding refresh, CloudNode heartbeat-buffer flush, JetStream ACK heartbeats, exchange listen-key keepalive, retry backoff, request timeout, shutdown drain, and polling used only to wait for an in-memory condition.

## File Map

**Shared timer runtime**

- Create `packages/timerjob/job.go`: context cloning, timeout, overlap guard, run result.
- Create `packages/timerjob/metrics.go`: bounded Timer job counters and duration histogram.
- Create `packages/timerjob/job_test.go`: timeout, panic-free overlap, context-value, and metric-independent behavior tests.

**Storage host-metrics cleanup**

- Modify `modules/storage/internal/config/loader.go`: move host cleanup out of View maintenance and default `max_age` to `48h`.
- Modify `modules/storage/config/storage.yaml`: add `maintenance.host_metrics_cleanup` and remove the three `host_*` View fields.
- Modify `modules/storage/internal/config/loader_test.go`: assert the new structure and removal of interval ownership.
- Rename `modules/storage/internal/service/access/retention.go` to `host_metrics_cleanup.go`.
- Rename `modules/storage/internal/service/access/retention_test.go` to `host_metrics_cleanup_test.go`.
- Create `modules/storage/cmd/server/host_metrics_cleanup_timer.go`: parse config, construct guarded handler, and register the tRPC Timer.
- Create `modules/storage/cmd/server/host_metrics_cleanup_timer_test.go`: role, disabled, timeout, overlap, and registration tests.
- Modify `modules/storage/cmd/server/main.go`: remove `startHostRetention` and register the handler before `Serve`.
- Modify `modules/storage/config/trpc_go.yaml` and `modules/storage/config/trpc_go.access.yaml`: add the timer service on port `20308`.

**Other modules**

- Modify `modules/admin/internal/service/auth/dao/badger.go`, `modules/admin/internal/bootstrap/bootstrap.go`, and Admin tests/config for cache GC.
- Modify Monitor bootstrap, scheduler, metrics scheduler, configuration, tests, and `modules/monitor/config/trpc_go.yaml` for four Timer services.
- Refactor Archive bootstrap/writer ownership and `modules/archive/config/trpc_go.yaml` for two Timer services.
- Modify Trade bootstrap/kernel workers/tests and `modules/trade/config/trpc_go.yaml` for reconciliation and recovery timers.
- Refactor Gateway bootstrap so its manual HTTP servers coexist with a timer-only tRPC server; move the fixed 15-second route refresh schedule to `modules/gateway/config/trpc_go.yaml`.
- Register HostAgent collection as a start-at-once tRPC Timer and remove the Agent-owned sampling ticker and application interval field.
- Rename Factor's fixed-window trigger component and config to `EventBatcher` / `event_batch_window_ms`, while preserving its process-owned flush and binding-refresh tickers.
- Modify `docs/存储引擎架构.md`, `docs/架构总览.md`, and relevant module README files to describe ownership and Timer semantics.

### Task 1: Add A Shared Guarded Timer Handler

**Files:**
- Create: `packages/timerjob/job.go`
- Create: `packages/timerjob/metrics.go`
- Create: `packages/timerjob/job_test.go`

- [ ] **Step 1: Write failing unit tests for run, overlap, timeout, and cloned context**

Define tests around this public contract:

```go
type Result string

const (
    ResultSuccess Result = "success"
    ResultError   Result = "error"
    ResultTimeout Result = "timeout"
    ResultSkipped Result = "skipped_overlap"
)

type Job struct {
    // private state
}

func New(name string, timeout time.Duration, run func(context.Context) error) (*Job, error)
func (j *Job) Handle(context.Context) error
func (j *Job) Running() bool
```

Tests must prove that `New` rejects an empty name, non-positive timeout, and nil callback; two concurrent `Handle` calls execute the callback once; timeout returns `context.DeadlineExceeded`; and a value carried by the incoming tRPC context is visible to the callback.

- [ ] **Step 2: Run the package test and confirm RED**

```bash
env GOCACHE=/tmp/moox-gocache go test -count=1 ./packages/timerjob
```

Expected: FAIL because `packages/timerjob` has no implementation.

- [ ] **Step 3: Implement the guarded synchronous handler**

Implement `Handle` with this ordering:

```go
if !j.running.CompareAndSwap(false, true) {
    observe(j.name, ResultSkipped, 0)
    return nil
}
defer j.running.Store(false)

jobCtx := trpc.CloneContext(ctx)
jobCtx, cancel := context.WithTimeout(jobCtx, j.timeout)
defer cancel()

started := time.Now()
err := j.run(jobCtx)
observe(j.name, classify(err, jobCtx.Err()), time.Since(started))
return err
```

Use a `prometheus.CounterVec` named `moox_timer_job_runs_total` with labels `job,result`, and a `prometheus.HistogramVec` named `moox_timer_job_duration_seconds` with label `job`. Register both once from package initialization.

- [ ] **Step 4: Run focused tests and race detection**

```bash
env GOCACHE=/tmp/moox-gocache go test -count=1 ./packages/timerjob
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./packages/timerjob
```

Expected: PASS; the race run reports no data race.

- [ ] **Step 5: Commit the shared runtime**

```bash
git add packages/timerjob
git commit -m "feat(timer): add guarded tRPC timer jobs"
```

### Task 2: Redesign Storage Host Metrics Cleanup

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Rename: `modules/storage/internal/service/access/retention.go` to `modules/storage/internal/service/access/host_metrics_cleanup.go`
- Rename: `modules/storage/internal/service/access/retention_test.go` to `modules/storage/internal/service/access/host_metrics_cleanup_test.go`

- [ ] **Step 1: Write failing configuration tests**

Add assertions for this configuration model:

```go
type StorageMaintenance struct {
    HostMetricsCleanup HostMetricsCleanupConfig `yaml:"host_metrics_cleanup"`
}

type HostMetricsCleanupConfig struct {
    Enabled          *bool    `yaml:"enabled"`
    DatasetIDs       []string `yaml:"dataset_ids"`
    MaxAge           string   `yaml:"max_age"`
    BatchSize        uint32   `yaml:"batch_size"`
    MaxBatchesPerRun int      `yaml:"max_batches_per_run"`
}
```

`StorageConfig` owns `Maintenance StorageMaintenance`. Defaults are enabled, the four host datasets, `48h`, batch size `1000`, and 10 batches. Reject an invalid/non-positive duration, batch size outside `1..1000`, non-positive max batches, empty dataset list, and duplicate/blank dataset IDs.

- [ ] **Step 2: Run config tests and confirm RED**

```bash
cd modules/storage
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config
```

Expected: FAIL because the new maintenance types and validation do not exist.

- [ ] **Step 3: Implement the new config and delete old names**

Remove `HostDatasetIDs`, `HostRetention`, and `HostInterval` from `StorageViewMaintenance`. Do not add decoding aliases. Replace the YAML with:

```yaml
maintenance:
  host_metrics_cleanup:
    enabled: true
    dataset_ids: [dataset_mooxsys_host_resource, dataset_mooxsys_host_filesystem, dataset_mooxsys_host_disk, dataset_mooxsys_host_network]
    max_age: 48h
    batch_size: 1000
    max_batches_per_run: 10
```

The hourly schedule must not appear in `storage.yaml`.

- [ ] **Step 4: Write failing cleanup service tests**

Define the service API:

```go
type HostMetricsCleanupOptions struct {
    SpaceID          string
    DatasetIDs       []string
    MaxAge           time.Duration
    BatchSize        uint32
    MaxBatchesPerRun int
    Now              time.Time
}

type HostMetricsCleanupResult struct {
    Deleted uint32
    Batches int
}

func (s *Service) CleanupExpiredHostMetrics(
    ctx context.Context,
    opts HostMetricsCleanupOptions,
) (HostMetricsCleanupResult, error)
```

Tests must cover: the cutoff is exactly `Now.UTC().Add(-48*time.Hour).Add(-time.Nanosecond)`; a dataset with more rows runs 10 batches; a zero-row batch stops early; each request uses page 1 and size 1000; cancellation stops further batches; an error in dataset A still allows dataset B to run; returned errors identify the failed dataset; and total deleted/batch counts include successful partial work.

- [ ] **Step 5: Implement bounded multi-batch deletion**

For every configured dataset, call `DeleteTimeSeriesRows` repeatedly with the same cutoff. Stop that dataset when deleted is zero, the per-dataset batch budget is exhausted, or the context ends. Collect dataset-scoped errors with `errors.Join`; never start an unbounded drain loop.

- [ ] **Step 6: Run service tests and race detection**

```bash
cd modules/storage
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./internal/service/access
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -race -count=1 ./internal/service/access
```

Expected: PASS, including bounded backlog catch-up and partial-failure cases.

- [ ] **Step 7: Commit the domain/config redesign**

```bash
git add modules/storage/internal/config modules/storage/internal/service/access modules/storage/config/storage.yaml
git commit -m "refactor(storage): redesign host metrics cleanup"
```

### Task 3: Register Storage Cleanup As A tRPC Timer

**Files:**
- Create: `modules/storage/cmd/server/host_metrics_cleanup_timer.go`
- Create: `modules/storage/cmd/server/host_metrics_cleanup_timer_test.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/config/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go.access.yaml`
- Modify: `modules/storage/cmd/server/plugin_config_test.go`

- [ ] **Step 1: Add failing Timer registration/config tests**

Require exactly this service in the combined and access-role configurations:

```yaml
- name: trpc.moox.storage.host_metrics_cleanup.timer
  port: 20308
  network: "0 0 * * * *?startAtOnce=1"
  protocol: timer
  timeout: 60000
```

Assert it is absent from view-only configurations. Add handler tests proving disabled cleanup becomes a no-op, an access service is required when enabled, concurrent triggers execute one cleanup, and a run exceeding 60 seconds receives cancellation.

- [ ] **Step 2: Run server tests and confirm RED**

```bash
cd modules/storage
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./cmd/server
```

Expected: FAIL because the service and registration function do not exist.

- [ ] **Step 3: Implement registration and remove the ticker loop**

Create `registerHostMetricsCleanupTimer(s, access, cfg) error`. Parse `MaxAge` once at startup, construct a `timerjob.Job` with a 60-second timeout, and register its `Handle` using `timer.RegisterHandlerService`. The callback invokes `CleanupExpiredHostMetrics` for `mooxsys` and logs deleted rows and batch count. Delete `startHostRetention`, its goroutine, cancellation channel, and shutdown waiter from `main.go`.

- [ ] **Step 4: Run focused Storage verification**

```bash
cd modules/storage
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./cmd/server ./internal/config ./internal/service/access
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -race -count=1 ./cmd/server ./internal/service/access
```

Expected: PASS with no leaked retention goroutine and no overlapping cleanup.

- [ ] **Step 5: Commit the Storage Timer wiring**

```bash
git add modules/storage/cmd/server modules/storage/config/trpc_go.yaml modules/storage/config/trpc_go.access.yaml
git commit -m "refactor(storage): schedule host metrics cleanup with tRPC"
```

### Task 4: Move Admin Badger GC Into Bootstrap

**Files:**
- Modify: `modules/admin/internal/service/auth/dao/badger.go`
- Modify: `modules/admin/internal/service/auth/dao/badger_test.go`
- Modify: `modules/admin/internal/bootstrap/bootstrap.go`
- Modify: `modules/admin/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/admin/internal/bootstrap/config_test.go`
- Modify: `modules/admin/config/trpc_go.yaml`

- [ ] **Step 1: Add failing lifecycle and registration tests**

Prove `NewCacheDB` and `NewCacheDBFromBadger` create no goroutine, `Close` performs no race with GC, and Admin config contains:

```yaml
- name: trpc.moox.admin.auth_cache_cleanup.timer
  port: 11306
  network: "0 */5 * * * *"
  protocol: timer
  timeout: 60000
```

- [ ] **Step 2: Expose one synchronous GC operation**

Replace `runGC` with:

```go
func (c *CacheDB) RunValueLogGC(context.Context) error
```

Treat `badger.ErrNoRewrite` as successful no-work; return every other error. Remove both constructor-launched goroutines.

- [ ] **Step 3: Register a guarded Timer handler**

After auth initialization has made the shared Badger handle available, register a 60-second `timerjob.Job`. The handler must use the same shared database handle and must not close it.

- [ ] **Step 4: Verify and commit Admin**

```bash
cd modules/admin
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/auth/dao ./internal/bootstrap
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/auth/dao ./internal/bootstrap
git add modules/admin
git commit -m "refactor(admin): run auth cache cleanup with tRPC timer"
```

Expected: PASS and no background GC survives `CacheDB.Close`.

### Task 5: Consolidate Monitor Cleanup Timers

**Files:**
- Create: `modules/monitor/internal/bootstrap/data_cleanup_timer.go`
- Create: `modules/monitor/internal/bootstrap/data_cleanup_timer_test.go`
- Modify: `modules/monitor/internal/bootstrap/service_runtime.go`
- Modify: `modules/monitor/internal/bootstrap/metrics_runtime.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/monitor/config/trpc_go.yaml`

- [ ] **Step 1: Add failing combined-cleanup tests**

Configure `trpc.moox.monitor.data_cleanup.timer` on port `11500` with `0 0 */6 * * *?startAtOnce=1` and a 120-second timeout. Test that result cleanup, alert-event cleanup, and metric-message dedupe cleanup all run; one failure does not suppress the others; overlap is skipped; and disabled/unavailable stores use an explicit no-op handler so configured Timer startup remains valid.

- [ ] **Step 2: Implement one maintenance callback**

Move `pruneMonitorHistory` and dedupe pruning behind one `runMonitorDataCleanup(ctx)` callback. Preserve `ResultRetentionDays` as the data-policy setting. Delete `startRetentionCleaner`, `startMetricsDedupeCleaner`, both six-hour tickers, and their runtime goroutines.

- [ ] **Step 3: Verify and commit Monitor cleanup**

```bash
cd modules/monitor
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap ./internal/store ./internal/metrics
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/bootstrap
git add modules/monitor
git commit -m "refactor(monitor): consolidate cleanup in tRPC timer"
```

Expected: PASS and the only six-hour scheduling source is `trpc_go.yaml`.

### Task 6: Move Monitor Scans Into tRPC Timers

**Files:**
- Create: `modules/monitor/internal/bootstrap/schedule_timers.go`
- Create: `modules/monitor/internal/bootstrap/schedule_timers_test.go`
- Modify: `modules/monitor/internal/scheduler/scheduler.go`
- Modify: `modules/monitor/internal/scheduler/scheduler_test.go`
- Modify: `modules/monitor/internal/metrics/scheduler.go`
- Modify: `modules/monitor/internal/metrics/scheduler_test.go`
- Modify: `modules/monitor/internal/bootstrap/service_runtime.go`
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/internal/config/config_test.go`
- Modify: `modules/monitor/config/trpc_go.yaml`
- Modify: `modules/monitor/config/app.yaml`

- [ ] **Step 1: Add explicit run-once APIs and failing tests**

Keep `Scheduler.RunDueOnce(ctx)` as the check entry point. Replace `RuleScheduler.Start/Stop/run` with exported `EvaluateDueOnce(ctx) error`. Extract peer work into `PullPeersOnce(ctx) error`, which always attempts both `PullOnce` and `MarkStale` and joins their errors.

- [ ] **Step 2: Add Timer configuration tests**

Require these services:

```yaml
- name: trpc.moox.monitor.check_schedule.timer
  port: 11501
  network: "*/30 * * * * *?startAtOnce=1"
  protocol: timer
  timeout: 30000
- name: trpc.moox.monitor.metric_rule.timer
  port: 11502
  network: "0 * * * * *?startAtOnce=1"
  protocol: timer
  timeout: 30000
- name: trpc.moox.monitor.peer_sync.timer
  port: 11503
  network: "*/10 * * * * *?startAtOnce=1"
  protocol: timer
  timeout: 10000
```

- [ ] **Step 3: Register guarded handlers and delete ticker ownership**

Use one `timerjob.Job` per service. Delete scheduler `Start/Stop` goroutines, metric scheduler ticker/wait group/stop channel, and `startPeerPuller` ticker. Remove `scheduler.reload_interval_seconds` and `peer.pull_interval_seconds` from Go config and YAML; keep max concurrency, retention days, peer timeout, credentials, and peer list.

- [ ] **Step 4: Verify and commit Monitor scheduling**

```bash
cd modules/monitor
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/scheduler ./internal/metrics ./internal/bootstrap ./internal/config
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/scheduler ./internal/metrics ./internal/bootstrap
git add modules/monitor
git commit -m "refactor(monitor): schedule periodic scans with tRPC timers"
```

Expected: PASS; no Monitor scheduler owns a `time.NewTicker` for these three scans.

### Task 7: Refactor Archive Periodic Jobs

**Files:**
- Modify: `modules/archive/internal/writer/scheduler.go`
- Create: `modules/archive/internal/writer/scheduler_test.go`
- Create: `modules/archive/internal/bootstrap/timers.go`
- Create: `modules/archive/internal/bootstrap/timers_test.go`
- Modify: `modules/archive/internal/bootstrap/app.go`
- Modify: `modules/archive/internal/bootstrap/app_test.go`
- Modify: `modules/archive/internal/config/config.go`
- Modify: `modules/archive/internal/config/config_test.go`
- Modify: `modules/archive/config/app.yaml`
- Modify: `modules/archive/config/trpc_go.yaml`

- [ ] **Step 1: Split Archive periodic work from shutdown work**

Replace `writer.Scheduler.Run` with synchronous methods:

```go
func (s Scheduler) MaterializeOnce(context.Context) error
func (s Scheduler) FlushOnShutdown(context.Context) error
```

`MaterializeOnce` runs `WriteDirty` and receipt pruning. `FlushOnShutdown` only flushes dirty partitions under the configured shutdown timeout. Tests must prove transient materialization errors do not close the journal or terminate an unrelated consumer.

- [ ] **Step 2: Refactor bootstrap ownership before starting the server**

Construct the journal, Writer, optional COS client, and both guarded jobs before `s.Serve()`. Register:

```yaml
- name: trpc.moox.archive.materialize.timer
  port: 11510
  network: "0 */10 * * * *?startAtOnce=1"
  protocol: timer
  timeout: 120000
- name: trpc.moox.archive.cos_sync.timer
  port: 11511
  network: "0 0 * * * *?startAtOnce=1"
  protocol: timer
  timeout: 300000
```

When COS is disabled, register a no-op handler for the configured service. Remove `materialize.interval` and `cos.sync_interval`; keep pending rows, workers, row-group size, shutdown timeout, and COS transfer options.

- [ ] **Step 3: Keep final flush in shutdown flow**

After consumer cancellation and before closing the journal, call `FlushOnShutdown` with `trpc.CloneContext(ctx)` followed by `context.WithTimeout`. Timer callbacks must never own journal closure.

- [ ] **Step 4: Verify and commit Archive**

```bash
cd modules/archive
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./internal/writer ./internal/bootstrap ./internal/config
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -race -count=1 ./internal/writer ./internal/bootstrap
git add modules/archive
git commit -m "refactor(archive): run maintenance with tRPC timers"
```

Expected: PASS; Archive contains no materialization or COS `time.NewTicker`.

### Task 8: Move Trade Reconciliation And Recovery Scans

**Files:**
- Create: `modules/trade/internal/bootstrap/kernel_timers.go`
- Create: `modules/trade/internal/bootstrap/kernel_timers_test.go`
- Modify: `modules/trade/internal/bootstrap/kernel_workers.go`
- Modify: `modules/trade/internal/bootstrap/kernel_workers_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/trade/config/trpc_go.yaml`

- [ ] **Step 1: Extract synchronous run-once callbacks**

Keep `reconcileOrdersOnce` and add:

```go
func recoverOrdersOnce(context.Context, *store.Store, *command.Engine) error
```

It lists at most 100 recoverable orders, attempts every row, and returns `errors.Join` after processing the batch. Tests must cover READY, SUBMITTING, SUBMIT_UNKNOWN, CANCELING, CANCEL_UNKNOWN, partial failure, and cancellation.

- [ ] **Step 2: Add Timer configuration and registration tests**

Require:

```yaml
- name: trpc.moox.trade.fill_reconcile.timer
  port: 11213
  network: "*/5 * * * * *?startAtOnce=1"
  protocol: timer
  timeout: 5000
- name: trpc.moox.trade.order_recovery.timer
  port: 11214
  network: "*/15 * * * * *?startAtOnce=1"
  protocol: timer
  timeout: 15000
```

- [ ] **Step 3: Register guarded handlers and remove only the two tickers**

Delete `runFillReconciliation` and `runRecoveryLoop` goroutines from `startKernelWorkers`. Keep the 200ms outbox relay, private-stream supervisor, consumers, and exchange session timers unchanged.

- [ ] **Step 4: Verify and commit Trade**

```bash
cd modules/trade
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap ./internal/application/reconciliation
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/bootstrap
git add modules/trade
git commit -m "refactor(trade): schedule recovery scans with tRPC timers"
```

Expected: PASS; the outbox and session loops remain intact, while reconciliation/recovery no longer own tickers.

### Task 9: Rename Factor Debounce To EventBatcher

**Files:**
- Rename: `modules/factor/internal/trigger/debounce.go` to `modules/factor/internal/trigger/event_batcher.go`
- Rename: `modules/factor/internal/trigger/debounce_test.go` to `modules/factor/internal/trigger/event_batcher_test.go`
- Modify: `modules/factor/internal/trigger/nats.go`
- Modify: `modules/factor/internal/trigger/nats_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `modules/factor/README.md`

- [ ] **Step 1: Lock the actual fixed-window behavior with characterization tests**

Rename the existing tests and add a case that ingests an event at `t0`, another event for the same bucket at `t0+window-1ms`, then flushes at `t0+window`. The batch must be emitted at the original deadline; the second event must not extend it. Keep coverage proving that batching is keyed by Space, source Dataset, target Dataset, Subject, and frequency; `BarTime` is the maximum event data time; Factor IDs are ordered and deduplicated; and include-mode Subject filtering is preserved.

- [ ] **Step 2: Rename the public and internal vocabulary**

Use this API:

```go
// EventBatcher groups Storage row-update events by calculation scope and
// emits one Factor task per scope after a bounded time window.
type EventBatcher struct {
    // existing mutex, window, binding snapshot, and buckets
}

func NewEventBatcher(window time.Duration, bindings []domain.FactorBinding) *EventBatcher
func (b *EventBatcher) SetBindings(bindings []domain.FactorBinding)
func (b *EventBatcher) Ingest(event *storagepb.TimeSeriesRowsUpdated, now time.Time)
func (b *EventBatcher) Flush(now time.Time) []Task
```

Rename `debounce` fields to `eventBatcher`, `debounceWindow` to `eventBatchWindow`, and `drainDebounced` to `drainEventBatch`. Rename test functions and comments so `Debouncer`, `debounce`, `debounced`, and `coalesces` no longer describe this implementation.

- [ ] **Step 3: Rename the configuration without compatibility decoding**

Replace:

```yaml
scheduler:
  debounce_window_ms: 2000
```

with:

```yaml
scheduler:
  event_batch_window_ms: 2000
```

Rename `SchedulerConfig.DebounceWindowMS` to `SchedulerConfig.EventBatchWindowMS`. Config tests must prove the checked-in YAML loads `2000`, a custom value is honored, and the old YAML key is absent from the file. Do not support both names because this project has no historical compatibility requirement.

- [ ] **Step 4: Keep event-batch flushing process-owned**

Retain the existing runtime loop with these names and semantics:

```go
flushInterval := deps.eventBatchWindow / 2
if flushInterval <= 0 {
    flushInterval = time.Second
}
if flushInterval < 200*time.Millisecond {
    flushInterval = 200 * time.Millisecond
}
eventBatchTicker := time.NewTicker(flushInterval)
bindingReloadTicker := time.NewTicker(30 * time.Second)
```

The event-batch ticker must call `drainEventBatch`; the binding ticker must refresh the same `EventBatcher` snapshot. Do not add a tRPC Timer service: the buckets are in-memory object state, the flush interval can be sub-second, and shutdown must stop both tickers with the Factor runtime context.

- [ ] **Step 5: Verify terminology and behavior**

```bash
cd modules/factor
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/trigger ./internal/bootstrap
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/trigger ./internal/bootstrap
if rg -n 'Debouncer|NewDebouncer|debounce_window_ms|debounceWindow|drainDebounced|debounced' . \
  --glob '*.go' --glob '*.yaml' --glob '*.md'; then
  exit 1
fi
```

Expected: both test commands PASS, race detection reports no race, and the terminology scan returns no matches.

- [ ] **Step 6: Commit the Factor naming correction**

```bash
git add modules/factor
git commit -m "refactor(factor): rename debounce to event batching"
```

### Task 10: Move Gateway Route Refresh To A tRPC Timer

**Files:**
- Create: `modules/gateway/internal/bootstrap/route_refresh_timer.go`
- Create: `modules/gateway/internal/bootstrap/route_refresh_timer_test.go`
- Modify: `modules/gateway/internal/bootstrap/bootstrap.go`
- Modify: `modules/gateway/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/gateway/internal/config/config.go`
- Modify: `modules/gateway/internal/config/config_test.go`
- Modify: `modules/gateway/config/app.yaml`
- Modify: `modules/gateway/config/trpc_go.yaml`
- Modify: `modules/gateway/cmd/server/main.go`
- Modify: `modules/gateway/README.md`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/release/release.sh`
- Modify: `scripts/test/contract/test-deploy-moox-gateway.sh`

- [ ] **Step 1: Lock startup and refresh behavior with tests**

Keep `Runtime.Initialize(ctx)` as the mandatory synchronous startup path. Tests must prove it loads the cached route, performs exactly one initial control-plane pull before either HTTP listener starts, and fails startup when no usable route exists. Keep `Runtime.Refresh(ctx)` as a synchronous run-once operation; a failed periodic refresh must retain the last valid route table and the existing readiness state.

Add configuration tests proving that `ControlPlaneConfig.RefreshInterval`, `DefaultRefreshInterval`, validation for that duration, and `control_plane.refresh_interval` no longer exist. The checked-in `app.yaml` must not own a scheduling frequency.

- [ ] **Step 2: Add a timer-only tRPC service configuration**

Change `modules/gateway/config/trpc_go.yaml` so it does not declare the Gateway service and health ports already owned by the manual HTTP servers. Register only:

```yaml
server:
  service:
    - name: trpc.moox.gateway.route_refresh.timer
      ip: 127.0.0.1
      port: 11013
      network: "*/15 * * * * *"
      protocol: timer
      timeout: 10000
```

Do not add `startAtOnce=1`: `Initialize` has already completed the first control-plane pull synchronously with the special rule that a valid cached route permits startup after a pull failure. A Timer `startAtOnce` call would both duplicate that pull and fail `ListenAndServe` whenever `Refresh` returns an error, losing the cache-aware startup behavior. Tests must lock the service name, unique port, fixed 15-second cron, lack of `startAtOnce`, `timer` protocol, and 10-second framework timeout.

- [ ] **Step 3: Register the guarded synchronous refresh handler**

Construct one `timerjob.Job` named `gateway_route_refresh`, with a 10-second execution timeout and `runtime.Refresh` as its callback. Register `job.Handle` with `timer.RegisterHandlerService`. The handler must clone the incoming tRPC context through the shared job wrapper, return only after refresh finishes, and skip an overlapping invocation inside the process.

Tests must prove that two concurrent handler invocations execute one refresh, an invocation timeout is returned and measured, a refresh error is returned to the Timer scheduler, and the last valid routing snapshot remains usable after an error.

- [ ] **Step 4: Run tRPC Timer beside the existing HTTP lifecycle**

Create `trpc.NewServer()` after successful `Runtime.Initialize`, register the Timer service, and start `Serve` alongside the existing Gateway and health HTTP listeners. Add the tRPC serve result to the same fatal server-error path and close it during normal shutdown and partial-startup failure. Do not move the Gateway or health HTTP handlers onto tRPC in this change.

Delete the `time.NewTicker` refresh loop from `bootstrap.Run`. Startup order must be: load config, initialize routing synchronously, bind/register all servers, then serve. Shutdown must wait for the Timer server and both HTTP servers without leaking goroutines.

- [ ] **Step 5: Update deployment and release contracts**

Start the Gateway with both configurations:

```bash
bin/moox-gateway -config=config/app.yaml -conf=config/trpc_go.yaml
```

Ensure release staging copies `modules/gateway/config/trpc_go.yaml`, and deployment validation rejects a release missing it. Extend deploy/release contract tests to verify the Timer config is present and the obsolete `refresh_interval` key is absent. The frequency is intentionally static and changes only through configuration deployment plus process restart; do not implement file watching or hot reload.

- [ ] **Step 6: Verify and commit Gateway**

```bash
cd modules/gateway
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap ./internal/config ./cmd/server
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/bootstrap
if rg -n 'RefreshInterval|DefaultRefreshInterval|refresh_interval|time\.NewTicker' . \
  --glob '*.go' --glob '*.yaml'; then
  exit 1
fi
cd ../..
scripts/test/contract/test-deploy-moox-gateway.sh
git add modules/gateway scripts/deploy/deploy-moox.sh scripts/release/release.sh scripts/test/contract/test-deploy-moox-gateway.sh
git commit -m "refactor(gateway): schedule route refresh with tRPC timer"
```

Expected: all tests PASS; startup still performs one immediate pull, periodic refresh has exactly one scheduling owner, and Gateway production code contains no refresh ticker or application refresh interval.

### Task 11: Move HostAgent Sampling To A tRPC Timer

**Files:**
- Create: `modules/hostagent/internal/app/sample_timer.go`
- Create: `modules/hostagent/internal/app/sample_timer_test.go`
- Modify: `modules/hostagent/internal/app/app.go`
- Modify: `modules/hostagent/internal/app/app_test.go`
- Modify: `modules/hostagent/internal/config/config.go`
- Modify: `modules/hostagent/internal/config/config_test.go`
- Modify: `modules/hostagent/config/app.yaml`
- Modify: `modules/hostagent/config/trpc_go.yaml`
- Modify: `modules/hostagent/cmd/server/main.go`
- Modify: `modules/hostagent/README.md`
- Modify: `scripts/release/release.sh`

- [ ] **Step 1: Remove application-owned schedule configuration**

Delete `Config.Interval`, its 15-second default, parsing/validation, and the top-level `interval: 15s` YAML key. Tests must prove that the checked-in application configuration still loads all collection and publishing settings and that it contains no sampling frequency. Do not retain an alias or fallback because there is no historical compatibility requirement.

- [ ] **Step 2: Add the HostAgent Timer service**

Add the Timer to `modules/hostagent/config/trpc_go.yaml` beside the existing RPC and health services:

```yaml
- name: trpc.moox.hostagent.sample.timer
  ip: 127.0.0.1
  port: 11427
  network: "*/15 * * * * *?startAtOnce=1"
  protocol: timer
  timeout: 30000
```

Tests must lock the service name, unique port, fixed 15-second cron, `startAtOnce=1`, `timer` protocol, and 30-second framework timeout. Add an integration-level registration test proving that the immediate handler runs before the Timer listener reports successful startup, and that an immediate handler error is returned by the server startup path. The schedule is deployment-time configuration and does not need hot reload.

- [ ] **Step 3: Expose one synchronous scheduled-run entry point**

Create a Timer registration function in the `app` package. Its handler must clone the incoming tRPC context, apply an explicit 30-second execution timeout, call the existing guarded run-once path synchronously, and return collection or publishing errors to the Timer scheduler.

Keep the Agent's atomic `running` guard and skipped-run counter. Both `trpc.moox.hostagent.sample.timer` and the manual `RunOnce` RPC must enter through that same guard so a manual collection cannot overlap a scheduled collection. Do not wrap this handler with a second independent overlap guard that would bypass the Agent's existing skipped-run accounting.

- [ ] **Step 4: Delete the Agent ticker loop and register the Timer**

Remove `Agent.Run`, its immediate call, `time.NewTicker`, and the goroutine launched by `cmd/server/main.go`. Register the Timer handler on the existing tRPC server before `Serve`; `startAtOnce=1` now provides the immediate collection during Timer service startup, before `Serve` succeeds. An initial collection or publishing error must propagate out of `Serve`, trigger the existing shutdown path, and let the process supervisor retry; it must not be logged and converted to success.

Tests must prove that the scheduled handler waits for `RunOnce` completion, preserves values from the cloned invocation context, observes cancellation/timeout, returns execution errors, and skips/counts a concurrent scheduled or manual run. Server shutdown must cancel an active sampling context and wait for normal Agent close behavior.

- [ ] **Step 5: Update documentation and release packaging**

Document that HostAgent sampling is fixed at 15 seconds, starts immediately, is locally non-reentrant, and shares exclusion with manual collection. State plainly that tRPC Timer runs the `startAtOnce=1` handler synchronously and an initial handler failure prevents successful service startup. Ensure the release includes the updated `trpc_go.yaml` as the authoritative schedule. Do not describe or implement runtime frequency changes.

- [ ] **Step 6: Verify and commit HostAgent**

```bash
cd modules/hostagent
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/app ./internal/config ./cmd/server
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/app
if rg -n 'Config\.Interval|interval: 15s|time\.NewTicker|go a\.Run|func \(a \*Agent\) Run\(' . \
  --glob '*.go' --glob '*.yaml'; then
  exit 1
fi
cd ../..
git add modules/hostagent scripts/release/release.sh
git commit -m "refactor(hostagent): schedule sampling with tRPC timer"
```

Expected: all tests PASS; HostAgent has one static Timer schedule, immediate sampling is owned by `startAtOnce=1`, initial failure is fail-fast, and scheduled/manual runs share one overlap guard.

### Task 12: Documentation, Static Audit, And Full Verification

**Files:**
- Modify: `docs/存储引擎架构.md`
- Modify: `docs/架构总览.md`
- Modify: `modules/storage/README.md`
- Modify: `modules/monitor/README.md`
- Create: `modules/archive/README.md`
- Modify: `modules/trade/README.md`
- Modify: `modules/admin/README.md`
- Modify: `modules/gateway/README.md`
- Modify: `modules/hostagent/README.md`

- [ ] **Step 1: Document Timer ownership and Storage cleanup policy**

Record the service names, cron schedules, per-run timeout, overlap behavior, `DefaultScheduler` single-process limitation, 48-hour cutoff, four datasets, 1000-row target batch, 10-batch dataset budget, and partial-failure semantics. State explicitly that Monitor does not delete Storage facts. Document that `startAtOnce=1` executes synchronously and a returned error prevents that Timer service from starting. Gateway performs one cache-aware synchronous initialization and therefore omits the flag; HostAgent uses the flag as its sole eager sampling path and deliberately fails startup on an initial sampling error. Both schedules are fixed deployment configuration and do not support hot reload.

- [ ] **Step 2: Audit remaining production timers**

```bash
rg -n 'time\.(NewTicker|Tick|NewTimer|AfterFunc|After)\(' modules packages \
  --glob '*.go' --glob '!**/*_test.go' --glob '!**/*_mock.go'
```

Expected: every remaining occurrence belongs to the explicit keep list in this plan. There must be no ticker for Admin GC, Monitor cleanup/check/rule/peer scans, Storage host cleanup, Archive materialization/COS sync, Trade fill/recovery scans, Gateway route refresh, or HostAgent sampling.

- [ ] **Step 3: Run focused repository tests**

```bash
env GOCACHE=/tmp/moox-gocache go test -count=1 ./packages/timerjob
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/admin/...
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/monitor/...
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./modules/storage/...
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./modules/archive/...
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/trade/...
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/gateway/...
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/hostagent/...
```

Expected: all commands PASS.

- [ ] **Step 4: Run the complete repository gate**

```bash
make verify
git status --short
```

Expected: `make verify` exits 0. `git status --short` contains only the intended documentation changes before the final commit.

- [ ] **Step 5: Commit documentation and push the completed series**

```bash
git add docs modules/*/README.md
git commit -m "docs: describe tRPC timer maintenance jobs"
git push
git status --short
git rev-parse HEAD
git rev-parse origin/main
```

Expected: push succeeds, the worktree is clean, and `HEAD` equals `origin/main`.

## Acceptance Checklist

- [ ] No migrated job has both a tRPC Timer and a legacy ticker.
- [ ] Every migrated handler clones its invocation context, owns an explicit timeout, runs synchronously, and skips local overlap.
- [ ] Storage defaults to deleting host metrics older than 48 hours.
- [ ] Storage cleanup is bounded to 10 batches per dataset per run and 1000 rows per resolved target per batch.
- [ ] Cleanup continues across dataset-specific errors and reports partial progress.
- [ ] No schedule interval is duplicated between application YAML and `trpc_go.yaml`.
- [ ] Timer service configuration tests lock service name, port, cron, protocol, and timeout.
- [ ] Factor uses `trigger.EventBatcher` and `event_batch_window_ms`; no Debouncer/debounce terminology remains.
- [ ] Factor event-batch flush and binding reload remain process-owned timers with runtime-context shutdown.
- [ ] Gateway performs one synchronous initial route pull, then refreshes through `trpc.moox.gateway.route_refresh.timer` every 15 seconds without `startAtOnce` or an application interval field.
- [ ] Gateway's manual service/health listeners coexist with a timer-only tRPC server and shut down as one lifecycle.
- [ ] HostAgent samples through `trpc.moox.hostagent.sample.timer` every 15 seconds with `startAtOnce=1`; no Agent ticker or application interval field remains.
- [ ] HostAgent tests prove the `startAtOnce=1` handler completes before successful startup and an initial handler error fails startup.
- [ ] HostAgent scheduled and manual runs share the same atomic overlap guard and skipped-run accounting.
- [ ] Gateway and HostAgent Timer frequencies are static deployment configuration; no hot-reload mechanism is added.
- [ ] Remaining Go timers are restricted to lifecycle, retry, heartbeat, event batching, outbox, or in-memory wait semantics.
- [ ] Focused tests, race tests, `make verify`, commit, push, clean-worktree, and `HEAD == origin/main` checks all pass.
