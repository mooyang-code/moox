# tRPC Metrics Reporting And Monitoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Report every registered MooX tRPC service instance's Prometheus metrics through `moox-eventbus`, persist queryable history in MooX Storage, provide a MooX metrics dashboard, and evaluate flat multi-metric threshold rules with alert notifications.

**Architecture:** Every tRPC process keeps the stable Prometheus exporter for local `/metrics` debugging and adds one local tRPC timer that gathers the same default Prometheus registry every 30 seconds. The timer encodes a bounded compressed snapshot, wraps it in `MooxMessage`, and publishes it to `MOOX_METRICS`; `moox-monitor` durably consumes, deduplicates, parses, projects latest series into SQLite, writes historical samples into MooX Storage, serves metric queries, and runs a structured rule evaluator. No Prometheus server, Pushgateway, central HTTP scraper, manually configured target, or shared `packages/metricreporter` is introduced.

**Tech Stack:** Go 1.24, tRPC-Go, `trpc.group/trpc-go/trpc-database/timer` v1.0.0, `trpc.group/trpc-go/trpc-metrics-prometheus` v1.0.0, Prometheus `client_golang`, `client_model`, and `common/expfmt`, MooX Message Protocol, `packages/jetstream`, NATS JetStream, MooX Storage TimeSeries APIs, SQLite/GORM, Vue 3, Arco Design, and VChart.

---

## Prerequisite

Complete Milestones M1-M3 of `docs/superpowers/plans/2026-07-10-moox-eventbus-service.md` first. This plan depends on:

- `packages/messagepb`
- `packages/jetstream`
- deployed `moox-eventbus`
- configured `MOOX_METRICS` Stream covering `moox.metrics.>`
- read-only EventBus management and readiness surfaces

Tests in this plan may start a test-only NATS server, but production code must connect to `moox-eventbus`.

## Reading Summary

- The tRPC-Go Prometheus plugin is listed as stable, exposes a `promhttp.Handler()`-backed `/metrics`, and explicitly does not provide a Prometheus server. It also has an optional Pushgateway mode; this plan leaves `enablepush: false`.
- Because the plugin uses the default Prometheus registry, a timer handler can call `prometheus.DefaultGatherer.Gather()` and encode the same metrics that `/metrics` serves. It does not need to issue HTTP requests to itself.
- MooX modules already use the tRPC timer service pattern. Most existing timers register a scheduler, but metric reporting must be a local timer: every replica must report its own process registry.
- `startAtOnce=1` can block service startup when the first handler call fails. Metric timers omit it so EventBus outages never prevent business service startup.
- `modules/admin/internal/service/monitor/scraper.go` already proves `prometheus/common/expfmt` v0.67.5 can parse Prometheus text safely with UTF-8 validation. The new ingestion path reuses that parser but receives bytes from JetStream rather than HTTP.
- `modules/monitor` already owns SQLite state, peer ownership, webhook notification, check scheduling, tRPC APIs, Admin gateway routing, build/release wiring, and the `/ops/service-monitor` page. Metric monitoring is a separate bounded context inside the same service.
- `docs/superpowers/plans/2026-07-10-host-agent-resource-monitoring.md` covers host CPU/memory/filesystem collection. This plan covers tRPC application and business metrics only and does not modify that file.

## Locked Decisions

| # | Decision |
|---|---|
| D1 | Services actively push snapshots through `moox-eventbus`; Monitor does not scrape `/metrics`. |
| D2 | `/metrics` remains available for local debugging and compatibility. Prometheus server and Pushgateway are not deployed. |
| D3 | Scheduling is provided by each service's tRPC timer configuration. Do not create `packages/metricreporter` or an additional ticker/scheduler abstraction. |
| D4 | Every replica runs the timer. Metric timers do not contain `scheduler`, `startAtOnce`, or `disable=1`. |
| D5 | The only shared metrics package is the payload contract `packages/metricspb`. Each service owns its small `internal/report` timer handler. |
| D6 | Snapshots contain absolute Counter, Gauge, Histogram, and Summary state. Monitor derives rate/increase from historical values. |
| D7 | Services are authorized from Monitor's SysDeploy-synchronized registry. There is no UI or API for manually adding scrape/report targets. |
| D8 | High-volume history lives in MooX Storage. Monitor SQLite stores the service/series catalog, latest value, dedupe records, rules, and rule state. |
| D9 | Metric rules are distinct from existing check-bound `AlertRule`. V1 supports one flat condition group joined entirely by AND or entirely by OR, with no nesting and no free-text PromQL. |
| D10 | Each condition reduces selected time series to one scalar before comparison. Per-condition no-data policy and rule-level consecutive trigger/recovery counts are explicit. |
| D11 | Metrics are best effort at the producer: a failed publication is logged and counted, and the next timer snapshot repairs current state. Services do not maintain a metrics outbox. |
| D12 | Ingestion is at least once. Monitor writes idempotent Storage rows and commits its dedupe/latest transaction before ACKing JetStream delivery. |
| D13 | Storage metadata is a deployment-time control-plane concern. Root `moox-cli metadata apply` registers and verifies the metrics schema before Monitor ingestion starts; `moox-monitor` never creates or mutates Storage metadata at runtime. |
| D14 | A metric `series_id` is the Storage `subject_id`. Dynamic series do not create `Subject` or `DatasetSubject` records; wildcard PrimaryStore routes partition them by `subject_id`. |
| D15 | Storage Space IDs beginning with `moox_` are reserved for MooX-managed internal data. Metrics use `moox_system`; the prefix is a classification convention, not an authorization boundary. |

## End-To-End Data Flow

```text
tRPC metrics API / RPC filters
  -> Prometheus default registry
  -> local /metrics exporter (debug only)
  -> local tRPC metrics timer (every instance)
  -> MetricSnapshot protobuf, gzip
  -> MooxMessage topic=moox.metrics.snapshot.reported.v1
  -> MOOX_METRICS
  -> Monitor durable pull consumer
  -> expfmt parse + limits + flatten
  -> MooX Storage history + Monitor SQLite latest/catalog
  -> metrics query API + dashboard
  -> structured rule evaluator + existing webhook delivery
```

## Shared Snapshot Contract

```protobuf
syntax = "proto3";

package trpc.moox.metrics;

option go_package = "github.com/mooyang-code/moox/packages/metricspb;metricspb";

enum ExpositionFormat {
  EXPOSITION_FORMAT_UNSPECIFIED = 0;
  EXPOSITION_FORMAT_PROMETHEUS_TEXT = 1;
}

enum Compression {
  COMPRESSION_UNSPECIFIED = 0;
  COMPRESSION_NONE = 1;
  COMPRESSION_GZIP = 2;
}

message MetricSnapshot {
  uint32 schema_version = 1;
  uint32 collection_interval_seconds = 2;
  ExpositionFormat format = 3;
  Compression compression = 4;
  bytes data = 5;
  uint32 metric_family_count = 6;
  uint32 sample_count = 7;
  bytes uncompressed_sha256 = 8;
}
```

Producer identity, instance/boot ID, sequence, occurrence/publication times, `space_id`, trace, and payload content type belong to `MooxMessage` and are not duplicated here.

## Reporter Limits

Each module uses explicit defaults that can be overridden by environment variables:

```text
interval:                  30s (owned by tRPC timer YAML)
max_uncompressed_bytes:    4 MiB
max_compressed_bytes:      1 MiB
max_metric_families:       2,000
max_samples:               20,000
max_labels_per_sample:     20
max_label_name_bytes:      128
max_label_value_bytes:     512
gzip_level:                1
include_regex:             ^.*$
exclude_regex:             ^(go_gc_.*debug.*)$
```

The first exceeded limit fails that snapshot and increments a local reporter error metric. It never truncates a metric family into an invalid exposition.

## Metrics History Model

The deployment registers one Storage dataset before Monitor starts:

```text
space_id:   moox_system
dataset_id: moox_service_metrics
data_kind:  TIME_SERIES
```

Each flattened Prometheus sample becomes one `TimeSeriesRow`:

```text
subject_id = sha256(service_name, instance_id, metric_name, canonical_labels)
freq       = reporter interval such as 30s
data_time  = MooxMessage.occurred_at
dimensions = service_name, instance_id, metric_name, metric_type
columns    = value, labels_json, producer_node_id, producer_version, message_id
```

Histogram buckets become samples with an `le` label; histogram sum/count and summary sum/count use their Prometheus suffixes; summary quantiles retain the `quantile` label. Counters remain absolute. The query/evaluator layer calculates rate and increase using ordered historical points.

## Storage Metadata Registration

Storage rejects writes when the Dataset is absent, inactive, not TimeSeries, or a supplied column is not registered with the matching type. Routing also requires at least one active PrimaryStore route whose node has an active Pebble device. Register the schema through root `moox-cli`; do not put this logic in `moox-monitor-cli` or Monitor startup.

Reserve the lower-snake prefix `moox_` for internal Storage Spaces. It begins with a lowercase letter, so it remains compatible if Space IDs later adopt the same `^[a-z][a-z0-9_]*$` rule already used by Dataset/View IDs. Ordinary business Spaces must not use this prefix. Internal seeds must also declare `scope=internal`, `owner_module`, and `managed_by`; consumers use these attributes rather than the name alone when they need to identify or hide internal Spaces.

`examples/metadata-monitor-metrics.seed.yaml` is the canonical logical schema:

```yaml
spaces:
  - space_id: moox_system
    name: MooX System
    description: MooX internal platform data
    owner: moox
    status: active
    attributes:
      scope: internal
      owner_module: monitor
      managed_by: moox-cli

data_sources:
  - space_id: moox_system
    data_source_id: moox_monitor
    name: MooX Monitor
    kind: internal
    timezone: UTC
    status: active

datasets:
  - space_id: moox_system
    dataset_id: moox_service_metrics
    data_source_id: moox_monitor
    name: MooX Service Metrics
    description: Historical tRPC application and business metrics
    data_kind: time_series
    freqs: ["30s"]
    status: active

fields:
  - {space_id: moox_system, field_id: monitor_metric_value, name: Metric value, value_type: double, status: active}
  - {space_id: moox_system, field_id: monitor_metric_labels, name: Canonical labels, value_type: json, status: active}
  - {space_id: moox_system, field_id: monitor_metric_producer_node_id, name: Producer node ID, value_type: string, status: active}
  - {space_id: moox_system, field_id: monitor_metric_producer_version, name: Producer version, value_type: string, status: active}
  - {space_id: moox_system, field_id: monitor_metric_message_id, name: Message ID, value_type: string, status: active}

dataset_columns:
  - {space_id: moox_system, dataset_id: moox_service_metrics, column_name: value, origin_type: field, origin_id: monitor_metric_value, value_type: double, required: true, status: active}
  - {space_id: moox_system, dataset_id: moox_service_metrics, column_name: labels_json, origin_type: field, origin_id: monitor_metric_labels, value_type: json, required: true, status: active}
  - {space_id: moox_system, dataset_id: moox_service_metrics, column_name: producer_node_id, origin_type: field, origin_id: monitor_metric_producer_node_id, value_type: string, status: active}
  - {space_id: moox_system, dataset_id: moox_service_metrics, column_name: producer_version, origin_type: field, origin_id: monitor_metric_producer_version, value_type: string, status: active}
  - {space_id: moox_system, dataset_id: moox_service_metrics, column_name: message_id, origin_type: field, origin_id: monitor_metric_message_id, value_type: string, required: true, status: active}
```

The schema deliberately contains no `subjects` or `dataset_subjects`. Storage accepts any non-empty `subject_id`, and `series_id` is already the stable identity of one metric/label set.

Routes remain deployment topology rather than logical schema. The bundled single-node deployment applies `examples/metadata-monitor-metrics-local-route.seed.yaml`:

```yaml
primary_store_routes:
  - space_id: moox_system
    route_id: route-monitor-metrics-local
    dataset_id: moox_service_metrics
    subject_pattern: "*"
    hash_rule: subject_id
    node_id: local
    priority: 100
    status: active
```

A clustered deployment supplies its own route seed with one same-priority wildcard route per active PrimaryStore node. Storage's weighted rendezvous selection then partitions series across nodes by `subject_id`; the logical schema seed stays unchanged.

The registration command is create-or-verify, not blind upsert:

```bash
moox-cli metadata apply \
  --file examples/metadata-monitor-metrics.seed.yaml \
  --metadata-url http://127.0.0.1:20200

moox-cli metadata apply \
  --file examples/metadata-monitor-metrics-local-route.seed.yaml \
  --metadata-url http://127.0.0.1:20200
```

Missing resources are created in dependency order. Existing resources are reported as unchanged only when identity, Dataset kind/frequencies, Field types, DatasetColumn origin/types/required flags, and route matching/hash/node fields are compatible. A mismatch fails without overwriting the existing contract. The existing `metadata import` behavior remains backward compatible.

## Structured Rule DSL

```protobuf
enum LogicalOperator { LOGICAL_OPERATOR_UNSPECIFIED = 0; LOGICAL_OPERATOR_AND = 1; LOGICAL_OPERATOR_OR = 2; }
enum CompareOperator { COMPARE_OPERATOR_UNSPECIFIED = 0; COMPARE_OPERATOR_GT = 1; COMPARE_OPERATOR_GTE = 2; COMPARE_OPERATOR_LT = 3; COMPARE_OPERATOR_LTE = 4; COMPARE_OPERATOR_EQ = 5; COMPARE_OPERATOR_NEQ = 6; }
enum TimeReducer { TIME_REDUCER_UNSPECIFIED = 0; TIME_REDUCER_CURRENT = 1; TIME_REDUCER_AVG = 2; TIME_REDUCER_MIN = 3; TIME_REDUCER_MAX = 4; TIME_REDUCER_SUM = 5; TIME_REDUCER_RATE = 6; TIME_REDUCER_INCREASE = 7; }
enum SeriesReducer { SERIES_REDUCER_UNSPECIFIED = 0; SERIES_REDUCER_AVG = 1; SERIES_REDUCER_MIN = 2; SERIES_REDUCER_MAX = 3; SERIES_REDUCER_SUM = 4; }
enum NoDataPolicy { NO_DATA_POLICY_UNSPECIFIED = 0; NO_DATA_POLICY_KEEP_STATE = 1; NO_DATA_POLICY_OK = 2; NO_DATA_POLICY_FIRING = 3; }

message LabelMatcher { string name = 1; string value = 2; bool negate = 3; }
message MetricSelector { string service_name = 1; string metric_name = 2; repeated LabelMatcher matchers = 3; }
message MetricQuery { MetricSelector selector = 1; TimeReducer time_reducer = 2; uint32 window_seconds = 3; SeriesReducer series_reducer = 4; }
message MetricCondition { string condition_id = 1; MetricQuery query = 2; CompareOperator compare = 3; double threshold = 4; NoDataPolicy no_data_policy = 5; }
message MetricRule { string space_id = 1; string rule_id = 2; string name = 3; repeated MetricCondition conditions = 4; LogicalOperator connector = 5; uint32 consecutive_trigger_count = 6; uint32 consecutive_recovery_count = 7; uint32 evaluation_interval_seconds = 8; repeated string webhook_ids = 9; bool enabled = 10; string description = 11; string created_at = 12; string updated_at = 13; }
```

V1 validation requires 1-8 conditions named `A` through `H`, one connector for the entire group, a positive window for non-current reducers, a positive evaluation interval, and at least one enabled webhook. `MAX > threshold` expresses “any matching series exceeds”; `MIN > threshold` expresses “all matching series exceed”.

## Target File Map

### Shared Metrics Protocol

- Create `packages/metricspb/go.mod`, `Makefile`, `metrics.proto`, generated Go, and contract tests.
- Modify `go.work` and root `Makefile`.

### Monitor Metrics Context

- Modify `modules/monitor/go.mod`, `config/app.yaml`, `config/trpc_go.yaml`, `internal/config/config.go`, `internal/bootstrap/bootstrap.go`, `proto/monitor.proto`, and `schema/monitor.sql`.
- Create `modules/monitor/internal/metrics/domain.go`, `parser.go`, `series.go`, `repository.go`, `storage.go`, `consumer.go`, `catalog.go`, `query.go`, `rule.go`, `evaluator.go`, `scheduler.go`, `notification.go`, and focused tests.
- Modify `modules/monitor/internal/rpc/service.go` and `convert.go`.

### Storage Metadata Control Plane

- Create `examples/metadata-monitor-metrics.seed.yaml` and `examples/metadata-monitor-metrics-local-route.seed.yaml`.
- Add create-or-verify `metadata apply` behavior and focused tests to root `modules/cli`; keep the existing `metadata import` command compatible.
- Make deployment apply and verify the logical schema plus the environment-specific route seed after Storage becomes ready and before Monitor metric ingestion starts.

### Service Reporters

- Create one `internal/report` package in EventBus, Admin, Collector, CloudNode, Factor, Monitor, Trade, and Storage.
- Modify each module's bootstrap/main, `go.mod`, `trpc_go.yaml`, and application configuration as listed in Task 9.
- Keep scheduling in tRPC timer services; do not create a common reporter package.

### Frontend

- Create `web/src/api/metric-monitor/index.ts` and `types.ts`.
- Create `web/src/views/ops/metric-monitor/index.vue`, `metric-chart.vue`, `metric-rule-editor.vue`, and `metric-condition-row.vue`.
- Modify `web/src/router/route.ts`, `web/src/api/modules/system/static-menu.ts`, `web/src/lang/modules/zhCN.ts`, and `web/src/lang/modules/enUS.ts`.
- Add a deterministic Node-based contract check because this frontend currently has no unit-test framework.

## Delivery Order

| Milestone | Tasks | Exit condition |
|---|---|---|
| M1 Contracts and parser | 1-3 | Bounded Prometheus text snapshots round-trip through protobuf/gzip and flatten deterministically. |
| M2 Durable ingestion and history | 4-6 | Monitor consumes `MOOX_METRICS`, deduplicates, writes Storage history, and serves catalog/latest/history queries. |
| M3 Structured alerting | 7-8 | Flat AND/OR multi-metric rules evaluate deterministically and reuse webhook notification/state behavior. |
| M4 Service rollout | 9-10 | Every tRPC process runs a local metric timer and continues operating when EventBus is unavailable. |
| M5 Product experience | 11-12 | MooX provides a metrics explorer/dashboard and structured rule editor with no free-text DSL. |
| M6 Verification | 13 | Cardinality, restart, no-data, duplicate, load, build, and deployment checks pass. |

---

### Task 1: Add The Metrics Snapshot Protocol

**Files:**
- Create: `packages/metricspb/go.mod`
- Create: `packages/metricspb/Makefile`
- Create: `packages/metricspb/metrics.proto`
- Generate: `packages/metricspb/metrics.pb.go`
- Create: `packages/metricspb/contract_test.go`
- Modify: `go.work`
- Modify: `Makefile`

- [ ] **Step 1: Write the descriptor contract test**

Lock every field number and enum value shown in **Shared Snapshot Contract**. Assert that service name, instance ID, sequence, timestamps, and `space_id` are absent because `MooxMessage` owns them.

- [ ] **Step 2: Define and generate the protocol**

Use the exact protobuf contract above and the generation pattern from `packages/commonpb`.

```bash
make -C packages/metricspb
go test -count=1 ./packages/metricspb
```

Expected: descriptor test passes and generated Go compiles.

- [ ] **Step 3: Commit**

```bash
git add go.work Makefile packages/metricspb
git commit -m "feat(monitor): define metrics snapshot protocol"
```

---

### Task 2: Build Snapshot Encoding And Parsing Fixtures

**Files:**
- Create: `modules/monitor/internal/metrics/parser.go`
- Create: `modules/monitor/internal/metrics/parser_test.go`
- Create: `modules/monitor/internal/metrics/testdata/counter_gauge.prom`
- Create: `modules/monitor/internal/metrics/testdata/histogram_summary.prom`
- Create: `modules/monitor/internal/metrics/testdata/invalid.prom`
- Modify: `modules/monitor/go.mod`

- [ ] **Step 1: Write parser limit tests**

Test valid Counter/Gauge/Histogram/Summary parsing, deterministic label ordering, duplicate label rejection, invalid UTF-8/name rejection, gzip corruption, checksum mismatch, compressed and decompressed byte limits, family/sample/label limits, and unsupported snapshot versions.

- [ ] **Step 2: Define the flattened domain type**

```go
type Sample struct {
    SeriesID       string
    ServiceName    string
    InstanceID     string
    MetricName     string
    MetricType     string
    Labels         map[string]string
    LabelsJSON     string
    Value          float64
    ObservedAt     time.Time
    Interval       time.Duration
    MessageID      string
    ProducerNodeID string
    ProducerVersion string
}
```

`SeriesID` is SHA-256 over length-prefixed service, instance, metric name, and sorted label pairs; never concatenate with an ambiguous delimiter.

- [ ] **Step 3: Implement bounded decode and flatten**

Decompress through `io.LimitReader`, verify SHA-256 over uncompressed bytes, parse with:

```go
parser := expfmt.NewTextParser(model.UTF8Validation)
families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
```

Flatten all supported metric family types and reject NaN/Inf values instead of writing them into Storage.

- [ ] **Step 4: Run and commit**

```bash
go test -count=1 ./modules/monitor/internal/metrics -run 'TestParse|TestFlatten'
git add modules/monitor/internal/metrics modules/monitor/go.mod modules/monitor/go.sum
git commit -m "feat(monitor): parse bounded prometheus snapshots"
```

---

### Task 3: Add Monitor Metrics Schema And Repositories

**Files:**
- Modify: `modules/monitor/schema/monitor.sql`
- Modify: `modules/monitor/schema/schema_test.go`
- Create: `modules/monitor/internal/metrics/domain.go`
- Create: `modules/monitor/internal/metrics/repository.go`
- Create: `modules/monitor/internal/metrics/repository_test.go`

- [ ] **Step 1: Write schema assertions**

Require tables and indexes for:

```text
t_monitor_metric_services
t_monitor_metric_series
t_monitor_metric_latest
t_monitor_metric_ingest_messages
t_monitor_metric_rules
t_monitor_metric_rule_states
t_monitor_metric_rule_evaluations
t_monitor_metric_rule_channels
```

- [ ] **Step 2: Add exact persistence semantics**

- Service uniqueness: `(service_name, instance_id, boot_id)`.
- Series uniqueness: `(service_name, instance_id, series_id)`.
- Latest uniqueness: `series_id` with observed-time compare-and-set so older/out-of-order snapshots cannot overwrite newer state.
- Ingest uniqueness: `message_id`, retaining processed time for seven days.
- Rule uniqueness: `(space_id, rule_id, is_deleted)`.
- Rule state uniqueness: `(space_id, rule_id)`.
- Channel uniqueness: `(space_id, rule_id, webhook_id)`.

- [ ] **Step 3: Implement one ingestion transaction**

```go
func (r *Repository) CommitIngest(ctx context.Context, msg *messagepb.MooxMessage, samples []Sample) (duplicate bool, err error)
```

The transaction inserts the dedupe row, upserts service/series catalog, and updates latest values. If `message_id` already exists it returns `duplicate=true` without changing state.

- [ ] **Step 4: Run and commit**

```bash
go test -count=1 ./modules/monitor/schema ./modules/monitor/internal/metrics -run 'TestRepository|TestSchema'
git add modules/monitor/schema modules/monitor/internal/metrics
git commit -m "feat(monitor): persist metrics catalog and state"
```

---

### Task 4: Register Storage Metadata And Add The History Adapter

**Files:**
- Create: `examples/metadata-monitor-metrics.seed.yaml`
- Create: `examples/metadata-monitor-metrics-local-route.seed.yaml`
- Modify: `modules/cli/cmd/metadata.go`
- Create: `modules/cli/cmd/metadata_test.go`
- Modify: `modules/cli/README.md`
- Create: `modules/monitor/internal/metrics/storage.go`
- Create: `modules/monitor/internal/metrics/storage_test.go`
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/internal/config/config_test.go`
- Modify: `modules/monitor/config/app.yaml`
- Modify: `modules/monitor/go.mod`

- [ ] **Step 1: Write failing `metadata apply` tests**

Use a fake Metadata HTTP server and cover:

- missing Space/DataSource/Dataset/Field/DatasetColumn/Route resources are created in dependency order;
- an identical existing resource is counted as `unchanged` and is not updated;
- incompatible Dataset kind/frequencies, Field type, DatasetColumn origin/type/required flag, route subject pattern/hash rule/node, or inactive dependency fails before later calls;
- a `moox_` Space in apply mode must carry the internal ownership attributes, while a non-internal seed cannot claim or overwrite that reserved Space;
- `metadata import --if-not-exists` retains its current behavior;
- list-only resources such as DatasetColumn are found with bounded pagination rather than an unbounded request.

- [ ] **Step 2: Implement generic create-or-verify apply and the seeds**

Add a sibling command without changing `metadata import`:

```bash
moox-cli metadata apply --file <seed.yaml> --metadata-url <url>
```

Reuse `loadMetadataSeed` and seed-to-protobuf conversion. Build read probes for every supported resource, normalize server-owned timestamps and empty/default status values, compare only contract fields, then create missing resources. Never use Update/Upsert to repair a mismatch in apply mode. Print `planned`, `applied`, `unchanged`, and resource counts as JSON.

Create the two exact seeds from **Storage Metadata Registration**. The local route seed references the existing `local` PrimaryStore node but does not create storage topology. Document that clustered installations provide a route seed with one wildcard route per node.

- [ ] **Step 3: Write fake Storage adapter and schema-verifier tests**

Assert read-only schema validation, deterministic TimeSeries keys, bounded write batches, idempotent duplicate writes, ordered history queries, label/series filtering, and clear errors when Storage returns a non-success `RetInfo`. Validation must reject a missing/inactive/non-TimeSeries Dataset, missing or mismatched columns, unsupported frequency, and absence of an active wildcard route. Assert that no Create/Update/Upsert Metadata RPC is called by Monitor.

- [ ] **Step 4: Add Storage configuration**

```yaml
metrics:
  enabled: true
  stream: MOOX_METRICS
  topic: moox.metrics.snapshot.reported.v1
  consumer: monitor_metrics_ingest_v1
  fetch_batch_size: 64
  fetch_max_wait: 1s
  ack_wait: 60s
  max_ack_pending: 256
  no_data_intervals: 2
  storage:
    access_target: ip://127.0.0.1:20102
    metadata_target: ip://127.0.0.1:20100
    space_id: moox_system
    dataset_id: moox_service_metrics
    frequency: 30s
    metadata_validation_interval: 30s
    write_batch_size: 1000
    history_retention_days: 30
```

- [ ] **Step 5: Implement read-only validation, history write, and query**

Use `storagepb.NewAccessClientProxy` and `NewMetadataClientProxy` with the same target normalization pattern as `modules/factor/internal/storageio`. The Metadata proxy only calls Get/List methods. Write `value` as DOUBLE and labels as canonical JSON. Use `series_id` as `subject_id`, the configured reporter interval as `freq`, and the dimensions from **Metrics History Model**. Query exact catalog-resolved series keys with explicit ascending/descending sort and limit; never issue an unbounded dataset scan.

Expose schema status separately from process health. Until validation succeeds, metrics ingestion is not ready and the consumer does not fetch messages; the existing HTTP/TCP check subsystem continues running. Retry read-only validation at `metadata_validation_interval` so applying the seed later recovers ingestion without restarting Monitor.

- [ ] **Step 6: Run and commit**

```bash
go test -count=1 ./modules/cli/cmd -run 'TestMetadataApply|TestMetadataImport'
go test -count=1 ./modules/monitor/internal/config ./modules/monitor/internal/metrics -run 'TestStorage|TestStorageSchema'
git add examples/metadata-monitor-metrics.seed.yaml examples/metadata-monitor-metrics-local-route.seed.yaml modules/cli modules/monitor
git commit -m "feat(monitor): register and store metric history"
```

---

### Task 5: Consume `MOOX_METRICS` Durably

**Files:**
- Create: `modules/monitor/internal/metrics/consumer.go`
- Create: `modules/monitor/internal/metrics/consumer_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/monitor/go.mod`

- [ ] **Step 1: Write ACK decision tests**

Cover valid message, duplicate, unregistered producer, unsupported Topic, malformed `MooxMessage`, unsupported content type, checksum failure, metadata not registered, metadata mismatch, transient Storage failure, transient SQLite failure, permanent cardinality violation, DLQ PubAck failure, and graceful drain.

- [ ] **Step 2: Implement the processing order**

```text
1. Decode and validate MooxMessage and Topic.
2. Check message_id dedupe record.
3. Validate producer service against SysDeploy-synchronized registry.
4. Decode/parse/flatten MetricSnapshot.
5. Write idempotent MooX Storage history.
6. Commit SQLite dedupe/catalog/latest transaction.
7. AckSync JetStream delivery.
```

If the process crashes between steps 5 and 6, redelivery repeats the same idempotent Storage rows. Invalid messages are copied to `moox.dlq.message.rejected.v1` with original message ID and rejection reason before `Term`; if DLQ PubAck fails, NAK the original.

- [ ] **Step 3: Use a durable pull consumer**

Create/bind `monitor_metrics_ingest_v1` with `DeliverAll`, `AckExplicit`, configured `AckWait`/`MaxAckPending`, and unlimited transient redelivery. Bound worker concurrency by `max_ack_pending` and keep fetching responsive during shutdown.

- [ ] **Step 4: Bootstrap without making EventBus a process-start dependency**

Monitor starts even when EventBus is unavailable or the Storage metrics schema is not ready, reports the exact degraded reason for metrics ingestion, retries both dependencies with backoff, and leaves HTTP/TCP checks operational. Bind the durable consumer only after read-only Storage schema validation succeeds; do not fetch and NAK-loop messages while metadata is absent. Shutdown stops fetch, waits for active handlers, then drains the client.

- [ ] **Step 5: Run and commit**

```bash
go test -count=1 ./modules/monitor/internal/metrics ./modules/monitor/internal/bootstrap
git add modules/monitor
git commit -m "feat(monitor): ingest metrics from eventbus"
```

---

### Task 6: Expose Metric Catalog, Latest, And History APIs

**Files:**
- Modify: `modules/monitor/proto/monitor.proto`
- Regenerate: `modules/monitor/proto/monitorgen`
- Create: `modules/monitor/internal/metrics/catalog.go`
- Create: `modules/monitor/internal/metrics/query.go`
- Create: `modules/monitor/internal/metrics/query_test.go`
- Modify: `modules/monitor/internal/rpc/service.go`
- Modify: `modules/monitor/internal/rpc/convert.go`
- Modify: `modules/monitor/internal/rpc/service_test.go`

- [ ] **Step 1: Add RPC contract tests**

Lock methods:

```text
ListMetricServices
ListMetricNames
ListMetricSeries
GetMetricLatest
QueryMetricHistory
```

All list/history methods require bounded pagination or point limit; none exposes a raw unbounded Storage query.

- [ ] **Step 2: Define API messages**

Expose service/instance freshness, metric type/help text, canonical labels, series ID, latest value/time, stale flag, and history points. Reuse `common.Page` and `PageResult`.

- [ ] **Step 3: Implement freshness and automatic discovery**

There is no CreateMetricService RPC. Services appear only from valid incoming snapshots associated with SysDeploy. Mark an instance stale after `no_data_intervals * reported_interval`, but retain its last value and report `stale=true`.

- [ ] **Step 4: Run and commit**

```bash
make -C modules/monitor/proto
go test -count=1 ./modules/monitor/internal/metrics ./modules/monitor/internal/rpc
git add modules/monitor
git commit -m "feat(monitor): expose metric catalog and history"
```

---

### Task 7: Add The Structured Metric Rule Contract And Repository

**Files:**
- Modify: `modules/monitor/proto/monitor.proto`
- Regenerate: `modules/monitor/proto/monitorgen`
- Modify: `modules/monitor/schema/monitor.sql`
- Create: `modules/monitor/internal/metrics/rule.go`
- Create: `modules/monitor/internal/metrics/rule_test.go`
- Modify: `modules/monitor/internal/rpc/service.go`
- Modify: `modules/monitor/internal/rpc/convert.go`
- Modify: `modules/monitor/internal/rpc/service_test.go`

- [ ] **Step 1: Write rule validation tests**

Cover 1-8 unique condition IDs, all enum values, one group connector, current/window reducer rules, selector requirements, label matcher duplicates, finite thresholds, positive consecutive counts, positive evaluation interval, no-data policy, webhook existence, and rejection of nested/free-text expressions.

- [ ] **Step 2: Add the exact structured DSL**

Add the enums and messages from **Structured Rule DSL**, plus CRUD/list requests and responses for `MetricRule`. Do not extend the existing check-bound `AlertRule`.

Lock RPC methods `ListMetricRules`, `GetMetricRule`, `CreateMetricRule`, `UpdateMetricRule`, `DeleteMetricRule`, `PreviewMetricRule`, `ListMetricRuleEvaluations`, and `GetMetricRuleState`. `PreviewMetricRule` accepts a complete unsaved `MetricRule`, performs bounded read-only evaluation, and never changes counters or sends notifications.

- [ ] **Step 3: Persist protobuf JSON as the canonical definition**

Store validated deterministic `protojson` in `t_monitor_metric_rules.c_definition`, with searchable columns for space/rule/name/enabled/evaluation interval. Store webhook bindings in the join table. Decode and validate again when loading rules.

- [ ] **Step 4: Implement CRUD and commit**

```bash
make -C modules/monitor/proto
go test -count=1 ./modules/monitor/schema ./modules/monitor/internal/metrics ./modules/monitor/internal/rpc -run 'TestMetricRule'
git add modules/monitor
git commit -m "feat(monitor): define structured metric alert rules"
```

---

### Task 8: Evaluate Multi-Metric Rules And Send Alerts

**Files:**
- Create: `modules/monitor/internal/metrics/evaluator.go`
- Create: `modules/monitor/internal/metrics/evaluator_test.go`
- Create: `modules/monitor/internal/metrics/scheduler.go`
- Create: `modules/monitor/internal/metrics/scheduler_test.go`
- Create: `modules/monitor/internal/metrics/notification.go`
- Create: `modules/monitor/internal/metrics/notification_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: Write reducer and comparison tests**

Use fixed timestamps and samples to test CURRENT/AVG/MIN/MAX/SUM/RATE/INCREASE, counter reset, multiple series reduction, every comparison operator, AND/OR truth tables, stale samples, each no-data policy, and exact boundary behavior.

- [ ] **Step 2: Implement deterministic evaluation**

For each condition, query the bounded window, reduce time per series, reduce series to one scalar, then compare. Return a structured evaluation containing selected series count, value, threshold, no-data reason, and boolean result. Evaluate conditions in ID order for stable payloads.

- [ ] **Step 3: Implement state transitions**

Maintain `OK -> FIRING -> RESOLVED/OK` using rule-level consecutive trigger/recovery counts. A no-data `KEEP_STATE` evaluation does not advance either counter. Persist every state transition and bounded recent evaluation detail.

- [ ] **Step 4: Reuse webhook delivery**

Adapt metric alert events into the existing webhook sender without changing old check alerts. Send one notification per bound webhook for triggered/reminder/resolved events. Deduplicate by `(rule_id, transition, triggered_at)` and preserve existing retry/timeouts.

- [ ] **Step 5: Add ownership-aware scheduling**

Use the existing Monitor owner/peer model so only one instance evaluates a rule in a given interval. Reload enabled rules on the existing configured cadence; do not add a tRPC timer for central evaluation.

- [ ] **Step 6: Run and commit**

```bash
go test -count=1 ./modules/monitor/internal/metrics ./modules/monitor/internal/alerting ./modules/monitor/internal/bootstrap
git add modules/monitor
git commit -m "feat(monitor): evaluate multi-metric alert rules"
```

---

### Task 9: Add A Reference Local Reporter To `moox-monitor`

**Files:**
- Create: `modules/monitor/internal/report/config.go`
- Create: `modules/monitor/internal/report/handler.go`
- Create: `modules/monitor/internal/report/handler_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/cmd/server/main.go`
- Modify: `modules/monitor/config/trpc_go.yaml`
- Modify: `modules/monitor/go.mod`

- [ ] **Step 1: Write gather/encode/publish tests**

Use a private Prometheus registry fixture to test include/exclude filters, HELP/TYPE preservation, family/sample limits, gzip/checksum, stable producer/boot/sequence fields, exact Topic, retry ID stability, and EventBus unavailable behavior.

- [ ] **Step 2: Implement the timer handler without a shared reporter package**

The handler calls the configured `prometheus.Gatherer`, encodes complete families with:

```go
encoder := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
for _, family := range families {
    if err := encoder.Encode(family); err != nil { return err }
}
```

It then gzips, creates `MetricSnapshot`, creates `MooxMessage`, and publishes through `packages/jetstream`. Connection is lazy/retryable; publication failure returns an error to the timer and updates local metrics but never exits the process.

- [ ] **Step 3: Configure Prometheus and the local timer**

Add the blank import:

```go
_ "trpc.group/trpc-go/trpc-metrics-prometheus"
```

Add Prometheus plugin port `12950`, path `/metrics`, `enablepush: false`, and the timer:

```yaml
- name: trpc.moox.monitor.metrics.timer
  port: 11415
  network: "*/30 * * * * *?params="
  protocol: timer
  timeout: 10000
```

Do not register a scheduler for this service.

- [ ] **Step 4: Register safely**

Resolve the timer service, warn and skip when absent in tests, then call `timer.RegisterHandlerService`. Do not use `startAtOnce`; the first report occurs after one interval.

- [ ] **Step 5: Run and commit**

```bash
go test -count=1 ./modules/monitor/internal/report ./modules/monitor/internal/bootstrap
git add modules/monitor
git commit -m "feat(monitor): report local prometheus snapshots"
```

---

### Task 10: Roll The Local Reporter Out To Every tRPC Process

**Files:**
- Create: `modules/eventbus/internal/report/config.go`
- Create: `modules/eventbus/internal/report/handler.go`
- Create: `modules/eventbus/internal/report/handler_test.go`
- Create: `modules/admin/internal/report/config.go`
- Create: `modules/admin/internal/report/handler.go`
- Create: `modules/admin/internal/report/handler_test.go`
- Create: `modules/collector/internal/report/config.go`
- Create: `modules/collector/internal/report/handler.go`
- Create: `modules/collector/internal/report/handler_test.go`
- Create: `modules/cloudnode/internal/report/config.go`
- Create: `modules/cloudnode/internal/report/handler.go`
- Create: `modules/cloudnode/internal/report/handler_test.go`
- Create: `modules/factor/internal/report/config.go`
- Create: `modules/factor/internal/report/handler.go`
- Create: `modules/factor/internal/report/handler_test.go`
- Create: `modules/trade/internal/report/config.go`
- Create: `modules/trade/internal/report/handler.go`
- Create: `modules/trade/internal/report/handler_test.go`
- Create: `modules/storage/internal/report/config.go`
- Create: `modules/storage/internal/report/handler.go`
- Create: `modules/storage/internal/report/handler_test.go`
- Modify: `modules/eventbus/cmd/server/main.go`
- Modify: `modules/eventbus/internal/bootstrap/bootstrap.go`
- Modify: `modules/eventbus/config/trpc_go.yaml`
- Modify: `modules/eventbus/go.mod`
- Modify: `modules/admin/cmd/server/main.go`
- Modify: `modules/admin/internal/bootstrap/bootstrap.go`
- Modify: `modules/admin/config/trpc_go.yaml`
- Modify: `modules/admin/go.mod`
- Modify: `modules/collector/cmd/server/main.go`
- Modify: `modules/collector/internal/app/control/bootstrap.go`
- Modify: `modules/collector/config/trpc_go.yaml`
- Modify: `modules/collector/go.mod`
- Modify: `modules/cloudnode/cmd/server/main.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/config/trpc_go.yaml`
- Modify: `modules/cloudnode/go.mod`
- Modify: `modules/factor/cmd/server/main.go`
- Modify: `modules/factor/internal/app/control/bootstrap.go`
- Modify: `modules/factor/config/trpc_go.yaml`
- Modify: `modules/factor/go.mod`
- Modify: `modules/trade/cmd/server/main.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/config/trpc_go.yaml`
- Modify: `modules/trade/go.mod`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/config/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go.access.yaml`
- Modify: `modules/storage/config/trpc_go.view_index.yaml`
- Modify: `modules/storage/config/trpc_go.view_builder.yaml`
- Modify: `modules/storage/config/trpc_go.view_query.yaml`
- Modify: `modules/storage/go.mod`

- [ ] **Step 1: Add exact service identities and ports**

| Process/config | Timer service | Timer port | `/metrics` port |
|---|---|---:|---:|
| EventBus | `trpc.moox.eventbus.metrics.timer` | 11421 | 12970 |
| Admin | `trpc.moox.admin.metrics.timer` | 11305 | 12900 |
| Collector | `trpc.moox.collector.metrics.timer` | 11412 | 12942 |
| CloudNode | `trpc.moox.cloudnode.metrics.timer` | 11413 | 12941 |
| Factor | `trpc.moox.factor.metrics.timer` | 11414 | 12944 |
| Trade | `trpc.moox.trade.metrics.timer` | 11210 | 12920 |
| Storage monolith/access | `trpc.moox.storage.access.metrics.timer` | 20303 | 12960 |
| Storage view-index | `trpc.moox.storage.view_index.metrics.timer` | 20304 | 12961 |
| Storage view-builder | `trpc.moox.storage.view_builder.metrics.timer` | 20305 | 12962 |
| Storage view-query | `trpc.moox.storage.view_query.metrics.timer` | 20306 | 12963 |

Every timer uses `*/30 * * * * *?params=`, `protocol: timer`, and `timeout: 10000`, with no `scheduler`, `startAtOnce`, or `disable` field.

- [ ] **Step 2: Implement each module-owned handler**

Each handler follows the tested Monitor reference implementation but declares its own fixed `service_name` and bootstrap integration. Share only `metricspb`, `messagepb`, and `jetstream`; do not move scheduling/gathering into `packages/`.

- [ ] **Step 3: Enable the stable Prometheus plugin**

Add the plugin dependency/blank import, exporter config, and `prometheus` RPC filter without removing existing filters. Set `enablepush: false`. Verify `curl http://127.0.0.1:<port>/metrics` contains the configured namespace/subsystem.

- [ ] **Step 4: Add configuration contract tests**

Parse every `trpc_go*.yaml` and assert unique ports on a shared host, enabled local timer, exporter path, Pushgateway disabled, and exact service identity. Storage split-role configs must report different producer identities.

- [ ] **Step 5: Run module tests and commit**

```bash
go test -count=1 ./modules/eventbus/internal/report ./modules/admin/internal/report ./modules/collector/internal/report ./modules/cloudnode/internal/report ./modules/factor/internal/report ./modules/trade/internal/report ./modules/storage/internal/report
git add modules
git commit -m "feat(metrics): report every trpc service through eventbus"
```

---

### Task 11: Build The Metrics Explorer And Dashboard

**Files:**
- Create: `web/src/api/metric-monitor/index.ts`
- Create: `web/src/api/metric-monitor/types.ts`
- Create: `web/src/views/ops/metric-monitor/index.vue`
- Create: `web/src/views/ops/metric-monitor/metric-chart.vue`
- Create: `web/scripts/check-metric-monitor.mjs`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: Add a failing contract checker**

The Node script must assert the route/menu, RPC method names, bounded history limit, stale/no-data rendering states, VChart series mapping, and absence of manual target creation API calls.

- [ ] **Step 2: Add typed API methods**

Expose service/metric/series lists, latest values, and bounded history queries through the existing `callControl` pattern targeting `monitor`.

- [ ] **Step 3: Build the operational layout**

Use a dense full-width page with a service/instance selector, metric and label filters, latest/stale status table, and an unframed VChart history region. Avoid marketing layout, nested cards, oversized headings, and explanatory in-app text.

- [ ] **Step 4: Handle complete states**

Implement loading, empty catalog, no matching series, stale instance, partial series, API error, and chart retry. Cap selected chart series and surface cardinality limits before fetching data.

- [ ] **Step 5: Verify and commit**

```bash
cd web
node scripts/check-metric-monitor.mjs
pnpm build:prod
git add src scripts
git commit -m "feat(web): add service metrics dashboard"
```

---

### Task 12: Build The Structured Metric Rule Editor

**Files:**
- Create: `web/src/views/ops/metric-monitor/metric-rule-editor.vue`
- Create: `web/src/views/ops/metric-monitor/metric-condition-row.vue`
- Modify: `web/src/views/ops/metric-monitor/index.vue`
- Modify: `web/src/api/metric-monitor/index.ts`
- Modify: `web/src/api/metric-monitor/types.ts`
- Modify: `web/scripts/check-metric-monitor.mjs`

- [ ] **Step 1: Extend the contract checker**

Assert flat A-H condition IDs, one AND/OR segmented control, no nested-group/free-text expression control, all reducers/comparators/no-data policies, positive consecutive counts, multi-webhook selection, and exact API serialization.

- [ ] **Step 2: Implement the rule interaction**

Each condition row contains metric selector, label matchers, time reducer/window, series reducer, comparator, numeric threshold, and no-data policy. Adding/removing rows renumbers condition IDs deterministically. Use select/segmented/stepper controls rather than raw DSL text.

- [ ] **Step 3: Add evaluation preview**

Before save, call a bounded preview RPC using the current structured definition and show each condition's scalar/no-data result plus the final AND/OR result. Preview does not mutate rule state or send notifications.

- [ ] **Step 4: Add CRUD and alert state views**

Show enabled state, last evaluation, consecutive count, FIRING/OK state, bound webhooks, and recent transition events. Reuse existing webhook management instead of introducing email/WeCom implementations in this plan.

- [ ] **Step 5: Verify and commit**

```bash
cd web
node scripts/check-metric-monitor.mjs
pnpm build:prod
git add src scripts
git commit -m "feat(web): add structured metric alert rules"
```

---

### Task 13: End-To-End, Failure, Cardinality, And Deployment Verification

**Files:**
- Create: `modules/monitor/test/metrics_eventbus_e2e_test.go`
- Create: `docs/运维/MooX指标监控.md`
- Modify: `modules/monitor/README.md`
- Modify: root `README.md`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/test-deploy-moox-eventbus.sh`

- [ ] **Step 1: Verify complete local flow**

Start test EventBus and Storage, apply the logical schema plus a test route seed through `moox-cli metadata apply`, then start Monitor and one instrumented tRPC fixture. Wait for two timer intervals, then assert service discovery, series catalog, latest values, ordered history, dashboard APIs, and no manually created target.

- [ ] **Step 2: Verify alert behavior**

Publish controlled A/B metrics and test AND, OR, every comparator, current/window reducers, counter reset, consecutive trigger/recovery, each no-data policy, reminders, resolution, and webhook deduplication.

- [ ] **Step 3: Verify failures and duplicates**

Cover EventBus unavailable at service start, Monitor restart, missing metrics metadata, incompatible DatasetColumn, missing route, late metadata registration and recovery, Storage timeout, malformed/gzip-bomb snapshots, unknown producer, duplicate `message_id`, out-of-order snapshots, DLQ outage, and stale service recovery. Business service health and Monitor's existing HTTP/TCP checks must remain operational while metric ingestion is degraded.

- [ ] **Step 4: Run a bounded cardinality/load test**

Model at least 100 services, 10 instances each, 100 series per instance, and 30-second snapshots. Record ingest throughput, p95 ingest-to-query latency, Monitor RSS, SQLite growth, Storage write rate, consumer pending/redelivery, and rule evaluation duration. Enforce test byte/time limits and never scan production datasets.

- [ ] **Step 5: Wire the deployment metadata preflight**

In the generated `start.sh`, wait for Storage Metadata HTTP readiness before `start_monitor`. Use `MOOX_METRICS_STORAGE_METADATA_URL` with default `http://127.0.0.1:20200`. For bundled Storage, apply these files in order:

```text
examples/platform-local.seed.yaml
examples/metadata-monitor-metrics.seed.yaml
examples/metadata-monitor-metrics-local-route.seed.yaml
```

For an external or clustered Storage, always apply the logical seed and require `MOOX_METRICS_STORAGE_ROUTE_SEED` to name the deployment-owned route seed; never apply `platform-local.seed.yaml` to that cluster. Each command uses `moox-cli metadata apply`. Any missing topology, incompatible schema, or invalid route aborts Monitor start with the CLI error. `moox-monitor-cli init` remains responsible only for Monitor's SQLite schema.

- [ ] **Step 6: Verify all builds and configs**

```bash
go test -count=1 ./modules/cli/...
go test -count=1 ./modules/monitor/... ./packages/metricspb
go test -count=1 ./modules/admin/... ./modules/collector/... ./modules/cloudnode/... ./modules/factor/... ./modules/trade/... ./modules/storage/...
./scripts/build.sh all
cd web && node scripts/check-metric-monitor.mjs && pnpm build:prod
```

Expected: all commands pass; no service imports or enables Prometheus Pushgateway; all metric timers are local and enabled. The generated deployment waits for Storage Metadata HTTP readiness, runs `moox-cli metadata apply` for the logical metrics seed and configured route seed, and only then starts Monitor. A schema mismatch aborts Monitor startup instead of mutating metadata.

- [ ] **Step 7: Document operations and commit**

Document Topic/payload versions, reporter environment variables, cardinality limits, `/metrics` debugging, `moox-cli metadata apply`, the default local route seed, clustered route-seed requirements, EventBus/Storage failure semantics, consumer lag, stale/no-data diagnosis, rule semantics, and the absence of Prometheus server/Pushgateway dependencies.

```bash
git add modules docs web README.md scripts
git commit -m "docs(monitor): add metrics reporting operations guide"
```

---

## Final Acceptance Criteria

- Every running tRPC replica reports its own default Prometheus registry through a local tRPC timer.
- Metric timer configs contain no scheduler, `startAtOnce`, or disabled flag.
- `/metrics` remains reachable for debugging, while Prometheus Pushgateway mode remains disabled.
- No central component performs HTTP scraping and no manual monitoring target API exists.
- `MetricSnapshot` has no version suffix; wire compatibility is carried by `schema_version` and the versioned Topic.
- `moox_` is reserved for MooX-managed Storage Spaces; the monitoring history lives in `moox_system` and is marked with internal ownership attributes.
- Storage logical metadata and an environment-specific wildcard route are registered through root `moox-cli` before metric ingestion; Monitor never creates or updates Storage metadata.
- Dynamic metric series use `series_id` as `subject_id` and require no per-series Subject or DatasetSubject registration.
- Monitor rejects unknown producers and bounded/cardinality-invalid snapshots without affecting business services.
- Valid snapshots are written idempotently to MooX Storage and projected into latest/catalog tables before ACK.
- Counter rates and window reducers use ordered historical samples and handle resets.
- Metric rules are structured, flat AND/OR groups with per-condition no-data and rule-level consecutive counts.
- Existing HTTP/TCP check rules remain behaviorally unchanged.
- The dashboard and rule editor handle loading, empty, stale, partial, error, and firing/resolved states.
- Duplicate, outage, restart, no-data, load, build, release, and deployment tests pass.

## References

- tRPC-Go Prometheus plugin: https://github.com/trpc-ecosystem/go-metrics-prometheus
- tRPC plugin ecosystem: https://trpc.group/zh/docs/what-is-trpc/plugin_ecosystem/
- Prometheus exposition parser/encoder already pinned in this repository: `github.com/prometheus/common` v0.67.5
