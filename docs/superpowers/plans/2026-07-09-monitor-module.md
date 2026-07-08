# Monitor Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an independently deployable `moox-monitor` module that monitors MooX services and user-defined HTTP/TCP checks, sends webhook alerts, exposes its own health endpoint, and supports multiple monitor instances observing one another.

**Architecture:** V1 adds a new `modules/monitor` Go module with its own SQLite database, tRPC management API, local scheduler, HTTP/TCP probe engine, Gatus-inspired alert state machine, webhook notifier, system-service sync from Admin SysDeploy, and peer snapshot exchange. Admin remains only the gateway and deployment registry; existing Admin host resource monitoring stays unchanged. A small shared health package standardizes `/healthz` across independently deployable MooX processes so monitor can probe Admin, Web Host, Storage, CloudNode, Collector, Factor, Trade, and Monitor itself.

**Tech Stack:** Go 1.24, tRPC-Go, GORM + SQLite (`github.com/glebarez/sqlite`), standard `net/http` and `net.Dialer`, Admin `SysDeploy` proto client, Vue 3 + Arco Design, existing Admin gateway `/api/admin/{service}/{method}`, `scripts/build.sh`, `scripts/deploy-moox.sh`, and `scripts/release.sh`.

---

## Reading Summary

- Gatus contributes the useful concepts: endpoint definitions, per-check intervals, HTTP/TCP probes, condition evaluation, result persistence, alert thresholds, reminder intervals, resolution notifications, and provider abstraction.
- MooX should not copy Gatus UI or its large provider matrix. V1 internalizes the behavior into MooX style: independent module, tRPC API, SQLite schema, Admin gateway forwarding, and Arco-based operations pages.
- The existing Admin `monitor` service is host resource monitoring based on Node Exporter. It remains in Admin for now and is not migrated in this plan.
- Existing independently deployable modules follow the same broad shape: `cmd/server`, `cmd/cli`, `config/app.yaml`, `config/trpc_go.yaml`, embedded schema, generated proto package, `scripts/build.sh` target, SysDeploy defaults, release/deploy packaging.
- Current health coverage is inconsistent: Admin has `/api/admin/health`, Trade has an internal `Health()` model but no standard public endpoint, Web Host can add raw HTTP health easily, and the other tRPC modules need a lightweight raw health server or a standardized endpoint.

## Scope

### In V1

- Create `modules/monitor` with `moox-monitor` and `moox-monitor-cli`.
- Support check types `http` and `tcp` only.
- Support HTTP method, URL, headers, body, timeout, status expectation, response-time threshold, and optional body-contains condition.
- Support TCP host, port, timeout, and response-time threshold.
- Store check definitions, check results, alert channels, alert rules, alert state, alert events, monitor instances, and peer snapshots in the monitor SQLite database.
- Send alerts through a generic webhook notifier only.
- Expose `GET /healthz` for monitor and standardize `/healthz` for all independently deployable MooX processes.
- Sync built-in `moox-system` checks from Admin SysDeploy active deployments and persist them locally so monitoring continues if Admin is unavailable.
- Support active-active monitor instances with peer heartbeat/snapshot exchange and deterministic alert ownership failover.
- Add a MooX-style frontend service monitor page with status-page cards plus operational tables/drawers.
- Add build, proto, release, and deployment support.

### Not In V1

- Do not migrate Admin host resource monitoring.
- Do not implement ICMP, DNS, gRPC, WebSocket, TLS certificate, domain expiration, or SSH checks.
- Do not copy Gatus condition DSL.
- Do not add Slack, Feishu, email, PagerDuty, or provider-specific alert channels beyond generic webhook.
- Do not require a shared database across monitor instances.
- Do not make monitor depend on Admin at probe time; Admin is only used for periodic system check sync.

## Delivery Slices

- **M1 Health Contract:** Add shared health response/handler and expose `/healthz` from every independently deployable process.
- **M2 Monitor Module Foundation:** Add `modules/monitor`, config, schema, proto, repositories, CLI init, build/workspace wiring.
- **M3 Probe Runtime:** Implement HTTP/TCP probe engine, scheduler, result persistence, and manual run API.
- **M4 Alerts:** Implement webhook channel, alert rule CRUD, alert state machine, reminder/resolution logic, and alert event history.
- **M5 SysDeploy Sync:** Read active service deployments from Admin SysDeploy and maintain built-in `moox-system` checks.
- **M6 Peer Active-Active:** Add monitor instance registry, peer HTTP API, snapshot puller, peer health checks, and deterministic alert owner failover.
- **M7 Frontend:** Add service monitor API client and UI page with status cards, check table, detail drawer, rule editor, webhooks, alerts, and peer status.
- **M8 Packaging And Verification:** Wire build, proto, release, deploy, docs, and end-to-end verification.

## Decisions

- `modules/monitor` owns service availability monitoring. Existing Admin host resource monitoring remains at `/ops/resource-monitor` and keeps its current APIs.
- Admin defaults keep the existing internal `monitor` row for host resource monitoring, and add a new `moox_monitor` row for the independent service monitor module. The frontend route for the new page is `/ops/service-monitor`.
- `GET /healthz` is the common process-level health endpoint. tRPC services that cannot attach raw routes to their service port start a small raw HTTP health server on `health.addr`.
- `t_service_deployments.c_extra_config` carries health metadata instead of adding new Admin schema columns. Expected JSON keys are `health_url`, `health_kind`, and `monitor_enabled`.
- Built-in system checks use `space_id = ""`, `source = "sysdeploy"`, and group `moox-system`.
- User-defined checks are space-scoped and use `source = "manual"`.
- All monitor instances run all enabled checks locally. Alert sending is deduplicated by deterministic ownership: sort active instance IDs, hash `check_id + ":" + alert_rule_id`, and choose `hash % len(active_instances)`.
- Peer exchange is pull-based over monitor's raw HTTP endpoint with a shared token in config. Peer API does not go through Admin gateway.
- When no peer is reachable, the local monitor owns all alert rules and continues probing.

## Target File Map

### Shared Health Contract

- Create: `packages/healthz/go.mod`
- Create: `packages/healthz/healthz.go`
- Create: `packages/healthz/healthz_test.go`
- Modify: `go.work`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `web-host/main.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/collector/internal/config/config.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/factor/internal/app/control/config.go`
- Modify: `modules/factor/internal/app/control/bootstrap.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/config/app.yaml`
- Modify: `modules/storage/internal/bootstrap/bootstrap.go`
- Modify: `modules/storage/config/storage.yaml`

### Monitor Module Foundation

- Create: `modules/monitor/go.mod`
- Create: `modules/monitor/config/app.yaml`
- Create: `modules/monitor/config/trpc_go.yaml`
- Create: `modules/monitor/cmd/server/main.go`
- Create: `modules/monitor/cmd/cli/main.go`
- Create: `modules/monitor/cmd/cli/init_schema.go`
- Create: `modules/monitor/cmd/cli/init_schema_test.go`
- Create: `modules/monitor/internal/config/config.go`
- Create: `modules/monitor/internal/config/config_test.go`
- Create: `modules/monitor/internal/storage/database.go`
- Create: `modules/monitor/internal/bootstrap/bootstrap.go`
- Create: `modules/monitor/schema/monitor.sql`
- Create: `modules/monitor/schema/schema.go`
- Create: `modules/monitor/schema/schema_test.go`
- Create: `modules/monitor/proto/Makefile`
- Create: `modules/monitor/proto/monitor.proto`
- Generate: `modules/monitor/proto/monitorgen/monitor.pb.go`
- Generate: `modules/monitor/proto/monitorgen/monitor.trpc.go`
- Create: `modules/monitor/proto/monitorgen/go.mod`
- Modify: `go.work`
- Modify: `Makefile`

### Monitor Domain And Runtime

- Create: `modules/monitor/internal/domain/check.go`
- Create: `modules/monitor/internal/domain/result.go`
- Create: `modules/monitor/internal/domain/alert.go`
- Create: `modules/monitor/internal/domain/peer.go`
- Create: `modules/monitor/internal/repository/check.go`
- Create: `modules/monitor/internal/repository/result.go`
- Create: `modules/monitor/internal/repository/alert.go`
- Create: `modules/monitor/internal/repository/peer.go`
- Create: `modules/monitor/internal/repository/page.go`
- Create: `modules/monitor/internal/probe/probe.go`
- Create: `modules/monitor/internal/probe/http.go`
- Create: `modules/monitor/internal/probe/tcp.go`
- Create: `modules/monitor/internal/scheduler/scheduler.go`
- Create: `modules/monitor/internal/scheduler/runner.go`
- Create: `modules/monitor/internal/alerting/evaluator.go`
- Create: `modules/monitor/internal/alerting/owner.go`
- Create: `modules/monitor/internal/alerting/webhook.go`
- Create: `modules/monitor/internal/sysdeploy/sync.go`
- Create: `modules/monitor/internal/peer/http.go`
- Create: `modules/monitor/internal/peer/puller.go`
- Create: `modules/monitor/internal/rpc/service.go`
- Create: `modules/monitor/internal/rpc/convert.go`
- Create: `modules/monitor/internal/rpc/service_test.go`

### Admin, Build, Deploy, Frontend

- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/README.md`
- Modify: `README.md`
- Create: `modules/monitor/README.md`
- Create: `web/src/api/monitor/index.ts`
- Create: `web/src/api/monitor/types.ts`
- Create: `web/src/views/ops/service-monitor/index.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

---

### Task 1: Add The Shared Health Contract

**Files:**
- Create: `packages/healthz/go.mod`
- Create: `packages/healthz/healthz.go`
- Create: `packages/healthz/healthz_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write health contract tests**

Create `packages/healthz/healthz_test.go` with tests for:

- `Response` JSON includes `module`, `instance_id`, `ready`, `status`, `version`, `git_commit`, `start_time`, and `time`.
- `Handler` returns HTTP 200 when `Ready=true`.
- `Handler` returns HTTP 503 when `Ready=false`.
- custom fields such as `db_ok` and `scheduler_ok` are preserved in `details`.

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./packages/healthz -run TestHealthz -count=1
```

Expected: FAIL because `packages/healthz` does not exist.

- [ ] **Step 2: Implement the shared package**

Create `packages/healthz/go.mod`:

```go
module github.com/mooyang-code/moox/packages/healthz

go 1.24.0
```

Create `packages/healthz/healthz.go` with:

- `type Response`
- `type SnapshotFunc func(context.Context) Response`
- `func Handler(snapshot SnapshotFunc) http.Handler`
- `func Start(ctx context.Context, addr string, snapshot SnapshotFunc) (*http.Server, error)`
- `func Base(module, instanceID, version, gitCommit string, start time.Time, ready bool) Response`

Rules:

- empty `addr` means health server is disabled and returns `(nil, nil)`.
- content type is `application/json`.
- `ready=false` maps to status `degraded` unless caller sets a status.
- `Handler` never panics; snapshot failure returns a 503 response with `status=error`.

- [ ] **Step 3: Add go.work entry**

Modify `go.work` and add:

```go
./packages/healthz
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./packages/healthz -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add go.work packages/healthz
git commit -m "feat: add shared healthz contract"
```

### Task 2: Expose `/healthz` From Existing Deployable Processes

**Files:**
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `web-host/main.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/collector/internal/config/config.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/factor/internal/app/control/config.go`
- Modify: `modules/factor/internal/app/control/bootstrap.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/config/app.yaml`
- Modify: `modules/storage/internal/bootstrap/bootstrap.go`
- Modify: `modules/storage/config/storage.yaml`
- Test: module-specific config/bootstrap tests added beside the modified code.

- [ ] **Step 1: Write admin/web-host health tests**

Add tests proving:

- Admin gateway serves both `/api/admin/health` and `/healthz`.
- Web Host serves `/healthz` before the static SPA fallback.

Run:

```bash
go test ./modules/admin/internal/gateway ./web-host -run 'Test.*Health' -count=1
```

Expected: FAIL because `/healthz` is not registered consistently.

- [ ] **Step 2: Implement admin and web-host health endpoints**

Admin:

- Register `GET /healthz` beside `/api/admin/health`.
- Return shared healthz JSON with `module=admin`, `ready=true`, and `status=ok`.

Web Host:

- Handle `GET /healthz` before `optimizedStaticHandler`.
- Return `module=web-host`, `ready=true`, and `status=ok`.

- [ ] **Step 3: Write module health config tests**

For CloudNode, Collector, Factor, Trade, and Storage, add config tests that prove:

- a `health.addr` field loads from config;
- empty `health.addr` disables the raw health listener;
- default dev config uses non-conflicting local addresses:

```text
cloudnode: :11411
collector: :11412
factor: :11414
trade: :11210
storage: :20210
```

Run the targeted config tests:

```bash
go test ./modules/cloudnode/internal/config ./modules/collector/internal/config ./modules/factor/internal/app/control -run TestHealthConfig -count=1
```

Expected: FAIL until config structs include `health`.

- [ ] **Step 4: Start health servers in module bootstrap**

In each module bootstrap:

- add a `HealthConfig` with `Addr string`;
- call `healthz.Start(ctx, cfg.Health.Addr, snapshotFunc)`;
- include module-specific details:
  - CloudNode: `db_ok`, `queue_backend`, `jetstream_enabled`.
  - Collector: `db_ok`, `scheduler_configured`.
  - Factor: `db_ok`, `nats_enabled`, `worker_count`.
  - Trade: `db_ok`.
  - Storage: `metadata_ok`, `eventbus_type`, `root`.

Do not make `/healthz` perform remote dependency calls. It should report local readiness only.

- [ ] **Step 5: Run health tests**

```bash
go test ./packages/healthz ./modules/admin/internal/gateway ./web-host ./modules/cloudnode/internal/config ./modules/collector/internal/config ./modules/factor/internal/app/control -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.work packages/healthz modules/admin web-host modules/cloudnode modules/collector modules/factor modules/trade modules/storage
git commit -m "feat: expose healthz for deployable modules"
```

### Task 3: Scaffold The Independent Monitor Module

**Files:**
- Create: `modules/monitor/go.mod`
- Create: `modules/monitor/config/app.yaml`
- Create: `modules/monitor/config/trpc_go.yaml`
- Create: `modules/monitor/internal/config/config.go`
- Create: `modules/monitor/internal/config/config_test.go`
- Create: `modules/monitor/internal/storage/database.go`
- Create: `modules/monitor/internal/bootstrap/bootstrap.go`
- Create: `modules/monitor/cmd/server/main.go`
- Create: `modules/monitor/cmd/cli/main.go`
- Create: `modules/monitor/cmd/cli/init_schema.go`
- Create: `modules/monitor/cmd/cli/init_schema_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write config tests**

Create tests for:

- default database path is `./data/monitor/monitor.db`;
- default RPC port in `trpc_go.yaml` is `11410`;
- default raw HTTP `health_addr` is `:11409`;
- `MOOX_MONITOR_DB_PATH` overrides `database.path`;
- `instance.instance_id` must not be empty;
- peer entries require `instance_id`, `base_url`, and `token`.

Run:

```bash
go test ./modules/monitor/internal/config -run TestMonitorConfig -count=1
```

Expected: FAIL because the module does not exist.

- [ ] **Step 2: Implement config loader**

Implement config types:

```go
type Config struct {
    Database DatabaseConfig `yaml:"database"`
    Health   HealthConfig   `yaml:"health"`
    Instance InstanceConfig `yaml:"instance"`
    Scheduler SchedulerConfig `yaml:"scheduler"`
    SysDeploy SysDeployConfig `yaml:"sysdeploy"`
    Peer PeerConfig `yaml:"peer"`
    Alert AlertConfig `yaml:"alert"`
}
```

Required defaults:

| Field | Value |
| --- | --- |
| `database.path` | `./data/monitor/monitor.db` |
| `health.addr` | `:11409` |
| `instance.instance_id` | hostname plus pid fallback, overridable |
| `instance.base_url` | `http://127.0.0.1:11409` |
| `scheduler.reload_interval_seconds` | `30` |
| `scheduler.result_retention_days` | `14` |
| `scheduler.max_concurrency` | `16` |
| `sysdeploy.enabled` | `true` |
| `sysdeploy.target` | `ip://127.0.0.1:11109` |
| `sysdeploy.sync_interval_seconds` | `60` |
| `peer.enabled` | `true` |
| `peer.pull_interval_seconds` | `10` |
| `peer.timeout_seconds` | `5` |
| `alert.send_timeout_seconds` | `10` |

- [ ] **Step 3: Add CLI init test**

Create `cmd/cli/init_schema_test.go` that runs `init --db-path <tmp>/monitor.db` and asserts all monitor tables exist.

Run:

```bash
go test ./modules/monitor/cmd/cli -run TestInitSchema -count=1
```

Expected: FAIL because schema and CLI are not implemented yet.

- [ ] **Step 4: Add server and CLI skeleton**

Implement:

- `cmd/server/main.go` matching other modules' `trpc.NewServer()` pattern.
- `cmd/cli/main.go` with `init` subcommand.
- `internal/bootstrap.Initialize(ctx, s)` that loads config, opens SQLite, applies schema, registers healthz, and later registers RPC services.
- `internal/storage.Manager` using `github.com/glebarez/sqlite`, WAL, busy timeout, and single-writer pool settings.

- [ ] **Step 5: Add go.work entry**

Add:

```go
./modules/monitor
```

Run:

```bash
go test ./modules/monitor/internal/config ./modules/monitor/cmd/cli -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.work modules/monitor
git commit -m "feat: scaffold monitor module"
```

### Task 4: Add Monitor Schema And Repositories

**Files:**
- Create: `modules/monitor/schema/monitor.sql`
- Create: `modules/monitor/schema/schema.go`
- Create: `modules/monitor/schema/schema_test.go`
- Create: `modules/monitor/internal/domain/check.go`
- Create: `modules/monitor/internal/domain/result.go`
- Create: `modules/monitor/internal/domain/alert.go`
- Create: `modules/monitor/internal/domain/peer.go`
- Create: `modules/monitor/internal/repository/check.go`
- Create: `modules/monitor/internal/repository/result.go`
- Create: `modules/monitor/internal/repository/alert.go`
- Create: `modules/monitor/internal/repository/peer.go`
- Create: `modules/monitor/internal/repository/page.go`
- Create: `modules/monitor/internal/repository/repository_test.go`

- [ ] **Step 1: Write schema tests**

Test that schema creates these tables and indexes:

- `t_monitor_checks`
- `t_monitor_check_results`
- `t_monitor_webhooks`
- `t_monitor_alert_rules`
- `t_monitor_alert_states`
- `t_monitor_alert_events`
- `t_monitor_instances`
- `t_monitor_peer_snapshots`

Required natural keys:

- `t_monitor_checks(c_space_id, c_check_id, c_is_deleted)`
- `t_monitor_webhooks(c_space_id, c_webhook_id, c_is_deleted)`
- `t_monitor_alert_rules(c_space_id, c_rule_id, c_is_deleted)`
- `t_monitor_alert_states(c_space_id, c_rule_id, c_check_id)`
- `t_monitor_instances(c_instance_id)`

Run:

```bash
go test ./modules/monitor/schema -count=1
```

Expected: FAIL until schema exists.

- [ ] **Step 2: Implement schema**

Use repository conventions:

- columns start with `c_`;
- soft delete uses `c_is_deleted INTEGER NOT NULL DEFAULT 0`;
- create/modify columns use `c_ctime` and `c_mtime`;
- add update triggers for mutable tables;
- JSON fields are `TEXT NOT NULL DEFAULT '{}'` or `TEXT NOT NULL DEFAULT '[]'`.

Core fields:

```text
t_monitor_checks:
  c_space_id, c_check_id, c_name, c_group_name, c_kind,
  c_url, c_method, c_headers, c_body,
  c_tcp_host, c_tcp_port,
  c_interval_seconds, c_timeout_ms,
  c_expected_status, c_max_response_ms, c_body_contains,
  c_enabled, c_source, c_labels, c_description

t_monitor_check_results:
  c_result_id, c_space_id, c_check_id, c_instance_id,
  c_success, c_status, c_http_status, c_connected,
  c_latency_ms, c_error_message, c_body_excerpt, c_checked_at

t_monitor_webhooks:
  c_space_id, c_webhook_id, c_name, c_url, c_method,
  c_headers, c_body_template, c_enabled

t_monitor_alert_rules:
  c_space_id, c_rule_id, c_check_id, c_webhook_id,
  c_failure_threshold, c_success_threshold,
  c_minimum_reminder_interval_seconds,
  c_send_on_resolved, c_enabled, c_description

t_monitor_alert_states:
  c_space_id, c_rule_id, c_check_id,
  c_status, c_failure_count, c_success_count,
  c_owner_instance_id, c_triggered_at, c_resolved_at,
  c_last_reminder_at, c_dedupe_key
```

- [ ] **Step 3: Write repository tests**

Test:

- create/update/list/delete check;
- insert result and query recent results;
- upsert alert state;
- list enabled checks by due scheduler criteria;
- upsert peer instance and snapshot;
- soft-deleted rows do not appear in list APIs.

Run:

```bash
go test ./modules/monitor/internal/repository -count=1
```

Expected: FAIL until repositories exist.

- [ ] **Step 4: Implement domain and repositories**

Use GORM models in `internal/domain` and repository methods in `internal/repository`. Keep conversion to proto out of repositories.

- [ ] **Step 5: Run tests**

```bash
go test ./modules/monitor/schema ./modules/monitor/internal/repository -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/monitor/schema modules/monitor/internal/domain modules/monitor/internal/repository
git commit -m "feat: add monitor persistence"
```

### Task 5: Add Monitor Proto And RPC CRUD

**Files:**
- Create: `modules/monitor/proto/Makefile`
- Create: `modules/monitor/proto/monitor.proto`
- Create: `modules/monitor/proto/monitorgen/go.mod`
- Generate: `modules/monitor/proto/monitorgen/monitor.pb.go`
- Generate: `modules/monitor/proto/monitorgen/monitor.trpc.go`
- Create: `modules/monitor/internal/rpc/service.go`
- Create: `modules/monitor/internal/rpc/convert.go`
- Create: `modules/monitor/internal/rpc/service_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `go.work`
- Modify: `Makefile`

- [ ] **Step 1: Define the proto contract**

Create `monitor.proto` with:

- enums: `CheckKind`, `CheckStatus`, `AlertStatus`, `AlertEventType`.
- messages: `MonitorCheck`, `CheckResult`, `WebhookChannel`, `AlertRule`, `AlertState`, `AlertEvent`, `MonitorInstance`, `Overview`.
- requests/responses:
  - `ListChecks`
  - `GetCheck`
  - `CreateCheck`
  - `UpdateCheck`
  - `DeleteCheck`
  - `RunCheckOnce`
  - `ListResults`
  - `GetOverview`
  - `ListWebhookChannels`
  - `CreateWebhookChannel`
  - `UpdateWebhookChannel`
  - `DeleteWebhookChannel`
  - `ListAlertRules`
  - `CreateAlertRule`
  - `UpdateAlertRule`
  - `DeleteAlertRule`
  - `ListAlertEvents`
  - `ListMonitorInstances`
  - `SyncSystemChecks`

All responses must have `common.RetInfo ret_info = 1`.

- [ ] **Step 2: Generate code**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
make -C modules/monitor/proto all
```

Expected: generated files under `modules/monitor/proto/monitorgen`.

- [ ] **Step 3: Write RPC tests**

Create tests for:

- invalid check kind returns `INVALID_PARAM`;
- HTTP check requires URL;
- TCP check requires host and port;
- create/list/get/update/delete check round trip;
- create/list webhook round trip;
- create/list alert rule round trip.

Run:

```bash
go test ./modules/monitor/internal/rpc -run TestMonitorRPC -count=1
```

Expected: FAIL until RPC service exists.

- [ ] **Step 4: Implement RPC service**

Implement service methods by composing repositories. Validation rules:

- `interval_seconds >= 5`;
- `timeout_ms >= 100 && timeout_ms <= 60000`;
- HTTP method defaults to `GET`;
- HTTP expected status defaults to `200-299`;
- TCP port must be between 1 and 65535;
- alert thresholds default to failure `3`, success `2`;
- minimum reminder interval must be `0` or at least `300` seconds.

- [ ] **Step 5: Register service in bootstrap**

Register:

```go
monitorgen.RegisterMonitorMgrService(s.Service("trpc.moox.monitor.MonitorMgr"), svc)
```

Add `go.work` entry:

```go
./modules/monitor/proto/monitorgen
```

Add root `Makefile` proto target:

```make
$(MAKE) -C modules/monitor/proto all
```

- [ ] **Step 6: Run tests**

```bash
go test ./modules/monitor/internal/rpc ./modules/monitor/proto/monitorgen -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go.work Makefile modules/monitor/proto modules/monitor/internal/rpc modules/monitor/internal/bootstrap/bootstrap.go
git commit -m "feat: add monitor management api"
```

### Task 6: Implement HTTP And TCP Probes

**Files:**
- Create: `modules/monitor/internal/probe/probe.go`
- Create: `modules/monitor/internal/probe/http.go`
- Create: `modules/monitor/internal/probe/tcp.go`
- Create: `modules/monitor/internal/probe/probe_test.go`

- [ ] **Step 1: Write probe tests**

Test HTTP:

- success on status `200` with expected range `200-299`;
- failure when status does not match;
- failure when response time exceeds `max_response_ms`;
- failure when `body_contains` is configured and missing;
- request uses configured method, headers, and body;
- timeout returns failure with an error message.

Test TCP:

- success when a local TCP listener accepts a connection;
- failure when connection is refused;
- failure when latency exceeds `max_response_ms`;
- timeout returns failure.

Run:

```bash
go test ./modules/monitor/internal/probe -count=1
```

Expected: FAIL until probe package exists.

- [ ] **Step 2: Implement probe types**

Use a small interface:

```go
type Runner interface {
    Run(ctx context.Context, check domain.Check) domain.CheckResult
}
```

Implement:

- HTTP with `http.Client{Timeout: check.Timeout}`.
- TCP with `net.Dialer{Timeout: check.Timeout}`.
- `ParseStatusExpectation("200")`, `ParseStatusExpectation("200-299")`, and `ParseStatusExpectation("200,204")`.
- body excerpt cap at 2048 bytes.

- [ ] **Step 3: Run tests**

```bash
go test ./modules/monitor/internal/probe -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add modules/monitor/internal/probe
git commit -m "feat: add monitor probe runners"
```

### Task 7: Implement Scheduler And Result Persistence

**Files:**
- Create: `modules/monitor/internal/scheduler/scheduler.go`
- Create: `modules/monitor/internal/scheduler/runner.go`
- Create: `modules/monitor/internal/scheduler/scheduler_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/rpc/service.go`

- [ ] **Step 1: Write scheduler tests**

Test:

- scheduler loads enabled checks and skips disabled checks;
- one due check writes exactly one result;
- concurrency is capped by `scheduler.max_concurrency`;
- changing a check interval is picked up after reload;
- `RunCheckOnce` executes immediately and persists one result without changing the periodic schedule.

Run:

```bash
go test ./modules/monitor/internal/scheduler -count=1
```

Expected: FAIL until scheduler exists.

- [ ] **Step 2: Implement scheduler**

Implement:

- a reload loop using `scheduler.reload_interval_seconds`;
- per-check next-run calculation using last local result time;
- semaphore-based concurrency cap;
- graceful shutdown on context cancel;
- result insert through `ResultRepository`;
- callback hook for alert evaluator after each result.

The scheduler must not depend on Admin or peer APIs.

- [ ] **Step 3: Wire manual run**

Implement `RunCheckOnce` RPC by calling the probe runner directly and persisting the result. Return the persisted result in the response.

- [ ] **Step 4: Wire scheduler in bootstrap**

Start scheduler after repositories and RPC service are initialized. Expose `scheduler_ok` and `active_checks` in monitor `/healthz`.

- [ ] **Step 5: Run tests**

```bash
go test ./modules/monitor/internal/scheduler ./modules/monitor/internal/rpc -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/monitor/internal/scheduler modules/monitor/internal/bootstrap/bootstrap.go modules/monitor/internal/rpc
git commit -m "feat: schedule monitor checks"
```

### Task 8: Implement Webhook Alerts And State Machine

**Files:**
- Create: `modules/monitor/internal/alerting/evaluator.go`
- Create: `modules/monitor/internal/alerting/webhook.go`
- Create: `modules/monitor/internal/alerting/alerting_test.go`
- Modify: `modules/monitor/internal/scheduler/runner.go`
- Modify: `modules/monitor/internal/rpc/service.go`

- [ ] **Step 1: Write alert evaluator tests**

Test:

- first and second failure do not trigger when `failure_threshold=3`;
- third consecutive failure triggers alert;
- repeated failures do not resend until `minimum_reminder_interval_seconds` elapses;
- consecutive successes resolve after `success_threshold`;
- `send_on_resolved=false` records resolved event but does not call webhook;
- webhook send failure records `send_failed` event and keeps alert state triggered.

Run:

```bash
go test ./modules/monitor/internal/alerting -run TestAlertEvaluator -count=1
```

Expected: FAIL until alerting package exists.

- [ ] **Step 2: Implement webhook notifier**

Implement placeholders in webhook body template:

```text
{{check_id}}
{{check_name}}
{{group_name}}
{{status}}
{{event_type}}
{{target}}
{{latency_ms}}
{{error_message}}
{{dedupe_key}}
{{instance_id}}
{{checked_at}}
```

Default method is `POST`. HTTP status `>=400` is send failure. Headers are JSON map from `t_monitor_webhooks.c_headers`.

- [ ] **Step 3: Implement alert state transitions**

Implement:

- `normal -> triggered` after failure threshold;
- `triggered -> triggered` reminder after interval;
- `triggered -> resolved` after success threshold;
- event insert for `triggered`, `reminder`, `resolved`, and `send_failed`;
- state counters persisted after every evaluated result.

- [ ] **Step 4: Wire evaluator into scheduler and manual run**

After each result is inserted, call evaluator with:

- check;
- result;
- enabled alert rules;
- active instance list from peer repository;
- local instance ID.

- [ ] **Step 5: Run tests**

```bash
go test ./modules/monitor/internal/alerting ./modules/monitor/internal/scheduler ./modules/monitor/internal/rpc -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/monitor/internal/alerting modules/monitor/internal/scheduler modules/monitor/internal/rpc
git commit -m "feat: add monitor webhook alerts"
```

### Task 9: Sync Built-In System Checks From SysDeploy

**Files:**
- Create: `modules/monitor/internal/sysdeploy/sync.go`
- Create: `modules/monitor/internal/sysdeploy/sync_test.go`
- Modify: `modules/monitor/go.mod`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/rpc/service.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`

- [ ] **Step 1: Write sysdeploy sync tests**

Use fake `SysDeploy` data to assert:

- active deployments with `extra_config.health_url` create HTTP checks under `moox-system`;
- active deployments without health URL create TCP checks for their host/port when `protocol=http`;
- inactive or deleted deployments are ignored;
- sync updates existing `source=sysdeploy` checks without touching `source=manual` checks;
- when Admin call fails, previously synced checks remain enabled.

Run:

```bash
go test ./modules/monitor/internal/sysdeploy -count=1
```

Expected: FAIL until sync package exists.

- [ ] **Step 2: Add Admin proto dependency**

Add monitor dependency on:

```text
github.com/mooyang-code/moox/modules/admin/proto/admingen
```

Use `admingen.NewSysDeployClientProxy(client.WithTarget(cfg.SysDeploy.Target))`.

- [ ] **Step 3: Implement sync**

Implement sync rules:

- health URL checks:
  - `kind=http`
  - `url=<health_url>`
  - `expected_status=200-299`
  - `body_contains="\"ready\":true"`
  - `group_name=moox-system`
  - `source=sysdeploy`
- TCP fallback checks:
  - `kind=tcp`
  - `tcp_host=deployment.host`
  - `tcp_port=deployment.port`
  - `group_name=moox-system`
  - `source=sysdeploy`

- [ ] **Step 4: Enrich SysDeploy default extra_config**

For default deployment rows, add `health_url` where the process-level endpoint is known:

```json
{"health_url":"http://127.0.0.1:11411/healthz","monitor_enabled":true}
```

Examples:

- `moox_cloudnode` -> `http://127.0.0.1:11411/healthz`
- `moox_collector` -> `http://127.0.0.1:11412/healthz`
- `moox_factor` -> `http://127.0.0.1:11414/healthz`
- `moox_monitor` -> `http://127.0.0.1:11409/healthz`
- `web_host` -> configured Web Host listen address plus `/healthz`

Keep the existing Admin `monitor` deployment row for resource monitor and add a new `moox_monitor` row for the independent module.

- [ ] **Step 5: Add timer/manual sync**

Register periodic sync in monitor bootstrap using `sysdeploy.sync_interval_seconds`. Implement RPC `SyncSystemChecks` for manual sync from frontend.

- [ ] **Step 6: Run tests**

```bash
go test ./modules/monitor/internal/sysdeploy ./modules/admin/internal/service/sysdeploy -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add modules/monitor/internal/sysdeploy modules/monitor/go.mod modules/monitor/internal/bootstrap modules/monitor/internal/rpc modules/admin/internal/service/sysdeploy
git commit -m "feat: sync monitor checks from sysdeploy"
```

### Task 10: Add Peer Active-Active Coordination

**Files:**
- Create: `modules/monitor/internal/alerting/owner.go`
- Create: `modules/monitor/internal/alerting/owner_test.go`
- Create: `modules/monitor/internal/peer/http.go`
- Create: `modules/monitor/internal/peer/puller.go`
- Create: `modules/monitor/internal/peer/peer_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/repository/peer.go`
- Modify: `modules/monitor/internal/rpc/service.go`

- [ ] **Step 1: Write alert owner tests**

Test:

- owner selection is deterministic for the same active instance set;
- ownership spreads checks across multiple instances;
- when the previous owner is removed from active instances, another instance becomes owner;
- single instance owns all alerts.

Run:

```bash
go test ./modules/monitor/internal/alerting -run TestAlertOwner -count=1
```

Expected: FAIL until owner code exists.

- [ ] **Step 2: Implement owner selection**

Implement:

```go
func Owner(checkID, ruleID string, activeInstanceIDs []string) string
```

Use stable sorting and FNV-1a or SHA-256 hash. Empty active list returns empty string; caller should treat empty as local owner.

- [ ] **Step 3: Write peer API tests**

Test raw HTTP endpoints:

- `GET /healthz` includes `peer_count` and `active_peer_count`;
- `GET /internal/monitor/v1/snapshot` rejects missing token;
- snapshot includes local instance, current check statuses, and recent alert events;
- puller stores peer snapshots and marks stale peers inactive after timeout.

Run:

```bash
go test ./modules/monitor/internal/peer -count=1
```

Expected: FAIL until peer package exists.

- [ ] **Step 4: Implement peer API**

Implement raw HTTP endpoints on monitor's `health.addr` server:

```text
GET /healthz
GET /internal/monitor/v1/snapshot
```

Token rules:

- request header `X-MooX-Monitor-Peer-Token`;
- token compared with peer config;
- no token logging.

Snapshot payload includes:

- `instance_id`
- `base_url`
- `observed_at`
- `checks[]` with latest status per check
- `alert_events[]` after optional cursor

- [ ] **Step 5: Implement peer puller**

The puller:

- polls configured peers every `peer.pull_interval_seconds`;
- upserts `t_monitor_instances`;
- upserts `t_monitor_peer_snapshots`;
- marks peers stale when `last_seen_at` is older than `peer.timeout_seconds * 3`;
- never blocks local probing or alerting.

- [ ] **Step 6: Wire alert ownership**

Evaluator sends webhook only when:

```text
Owner(check_id, rule_id, active_instances) == local_instance_id
```

If active instance list is empty or only local, local sends.

- [ ] **Step 7: Run tests**

```bash
go test ./modules/monitor/internal/alerting ./modules/monitor/internal/peer ./modules/monitor/internal/rpc -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add modules/monitor/internal/alerting modules/monitor/internal/peer modules/monitor/internal/repository/peer.go modules/monitor/internal/bootstrap modules/monitor/internal/rpc
git commit -m "feat: coordinate monitor peers"
```

### Task 11: Add Overview And Query APIs

**Files:**
- Modify: `modules/monitor/internal/rpc/service.go`
- Modify: `modules/monitor/internal/rpc/convert.go`
- Modify: `modules/monitor/internal/repository/check.go`
- Modify: `modules/monitor/internal/repository/result.go`
- Modify: `modules/monitor/internal/repository/alert.go`
- Test: `modules/monitor/internal/rpc/service_test.go`

- [ ] **Step 1: Write overview tests**

Create tests for:

- `GetOverview` returns total checks, healthy count, degraded count, down count;
- 24h success rate is calculated from results;
- p95 latency is calculated from successful results;
- group summaries include `moox-system`;
- peer disagreement marks a check as `degraded`.

Run:

```bash
go test ./modules/monitor/internal/rpc -run TestGetOverview -count=1
```

Expected: FAIL until aggregation exists.

- [ ] **Step 2: Implement aggregation repository methods**

Add methods:

- latest local result by check;
- latest peer snapshot by check;
- result stats by time window;
- alert events by page;
- active alert states by check.

- [ ] **Step 3: Implement RPC query methods**

Implement:

- `GetOverview`
- `ListResults`
- `ListAlertEvents`
- `ListMonitorInstances`

Use `common.Page` and `common.PageResult` where applicable.

- [ ] **Step 4: Run tests**

```bash
go test ./modules/monitor/internal/rpc ./modules/monitor/internal/repository -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/monitor/internal/rpc modules/monitor/internal/repository
git commit -m "feat: add monitor overview queries"
```

### Task 12: Wire Build, Proto, Release, And Deploy

**Files:**
- Modify: `scripts/build.sh`
- Modify: `Makefile`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/README.md`
- Create: `modules/monitor/README.md`
- Modify: `README.md`

- [ ] **Step 1: Write packaging checks**

Add or update shell-script tests if a script test harness exists. If no harness exists, use direct dry-run shell checks:

```bash
bash -n scripts/build.sh scripts/release.sh scripts/deploy-moox.sh
```

Expected before edits: scripts pass syntax but monitor target is missing.

- [ ] **Step 2: Add build target**

In `scripts/build.sh`:

- all target builds `moox-monitor` and `moox-monitor-cli`;
- add `monitor)` target;
- add `monitor-cli)` target.

Run:

```bash
./scripts/build.sh monitor
```

Expected: PASS and binaries appear in `bin/`.

- [ ] **Step 3: Add release packaging**

In `scripts/release.sh`:

- create `monitor/bin` and `monitor/config`;
- copy `moox-monitor`, `moox-monitor-cli`;
- copy `modules/monitor/config`.

- [ ] **Step 4: Add deploy support**

In `scripts/deploy-moox.sh`:

- add flag `--no-monitor`;
- build monitor when enabled;
- stage monitor config and binaries;
- rewrite monitor db path to `../data/monitor/monitor.db`;
- create `data/monitor` and `logs/monitor`;
- add `start_monitor`, `stop monitor`, `status monitor`, and healthcheck coverage;
- include monitor in default service order after Admin and before Web Host, while allowing independent start/stop.

- [ ] **Step 5: Update docs**

Document:

- V1 scope: HTTP/TCP service monitor only;
- relationship with existing resource monitor;
- monitor config and peer config examples;
- health endpoint standard;
- deployment examples for one monitor and two monitors.

- [ ] **Step 6: Run verification**

```bash
go test ./modules/monitor/... ./packages/healthz/... -count=1
./scripts/build.sh monitor
bash -n scripts/build.sh scripts/release.sh scripts/deploy-moox.sh
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add scripts/build.sh Makefile scripts/release.sh scripts/deploy-moox.sh modules/README.md modules/monitor/README.md README.md
git commit -m "feat: package monitor module"
```

### Task 13: Add Frontend API Client

**Files:**
- Create: `web/src/api/monitor/types.ts`
- Create: `web/src/api/monitor/index.ts`

- [ ] **Step 1: Write TypeScript typecheck expectation**

Add types matching `monitor.proto` JSON names:

- `MonitorCheck`
- `CheckResult`
- `WebhookChannel`
- `AlertRule`
- `AlertEvent`
- `MonitorInstance`
- `MonitorOverview`

Run:

```bash
cd web
pnpm typecheck
```

Expected: FAIL until API module exists or typecheck command reports missing imports after page task begins.

- [ ] **Step 2: Implement API client**

Use `callControl` with service `monitor`:

```ts
callControl('monitor', 'ListChecks', req)
callControl('monitor', 'GetOverview', req)
callControl('monitor', 'RunCheckOnce', req)
```

Include all RPC methods needed by the page:

- checks CRUD;
- webhook CRUD;
- alert rule CRUD;
- results list;
- alert events list;
- overview;
- peer instances;
- system sync.

- [ ] **Step 3: Run typecheck**

```bash
cd web
pnpm typecheck
```

Expected: PASS if no page imports are added yet.

- [ ] **Step 4: Commit**

```bash
git add web/src/api/monitor
git commit -m "feat: add monitor web api client"
```

### Task 14: Add Service Monitor Frontend Page

**Files:**
- Create: `web/src/views/ops/service-monitor/index.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: Create page skeleton with failing import check**

Add route `/ops/service-monitor` and menu item under Ops before resource monitor.

Run:

```bash
cd web
pnpm typecheck
```

Expected: FAIL until page exists.

- [ ] **Step 2: Implement status-page style top section**

The top area uses MooX styling plus status-page intuition:

- overall status card: `Healthy`, `Degraded`, `Down`;
- counters: total checks, healthy, degraded, down;
- 24h success rate;
- p95 latency;
- group status cards for `moox-system` and user groups;
- prominent cards for currently failing checks.

Keep layout dense and operational. Do not copy Gatus visual assets or card-heavy landing-page composition.

- [ ] **Step 3: Implement operational table**

Table columns:

- name;
- group;
- type;
- target;
- status;
- latest latency;
- latest checked time;
- failure/success counters;
- alert status;
- enabled;
- actions.

Actions:

- run once;
- view detail;
- edit;
- enable/disable;
- delete.

- [ ] **Step 4: Implement drawers**

Create drawers:

- check editor with HTTP/TCP segmented control;
- alert rule editor;
- webhook editor;
- detail drawer with recent results, condition details, alert timeline, and peer views;
- peer drawer showing active monitor instances.

Use Arco controls already used in the project: `a-table`, `a-drawer`, `a-form`, `a-select`, `a-switch`, `a-input-number`, `a-tabs`, and `a-tag`.

- [ ] **Step 5: Add polling**

Poll overview every 15 seconds and table every 30 seconds. Pause polling while an edit drawer is open.

- [ ] **Step 6: Run frontend checks**

```bash
cd web
pnpm typecheck
pnpm lint
pnpm build
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/views/ops/service-monitor web/src/router/route.ts web/src/api/modules/system/static-menu.ts web/src/lang/modules/zhCN.ts web/src/lang/modules/enUS.ts
git commit -m "feat: add service monitor page"
```

### Task 15: End-To-End Verification

**Files:**
- Modify: `modules/monitor/README.md`
- Create: `docs/superpowers/verification/2026-07-09-monitor-module.md`

- [ ] **Step 1: Run backend tests**

```bash
go test ./packages/healthz/... ./modules/monitor/... -count=1
```

Expected: PASS.

- [ ] **Step 2: Run affected module tests**

```bash
go test ./modules/admin/internal/gateway ./modules/admin/internal/service/sysdeploy ./modules/cloudnode/internal/config ./modules/collector/internal/config ./modules/factor/internal/app/control -count=1
```

Expected: PASS.

- [ ] **Step 3: Build monitor**

```bash
./scripts/build.sh monitor
```

Expected:

```text
==> build moox-monitor
==> build moox-monitor-cli
==> binaries written to /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/bin
```

- [ ] **Step 4: Start local monitor with temp database**

```bash
tmpdir="$(mktemp -d)"
MOOX_MONITOR_DB_PATH="$tmpdir/monitor.db" ./bin/moox-monitor-cli init --db-path "$tmpdir/monitor.db"
```

Expected: schema initializes without error.

- [ ] **Step 5: Verify HTTP probe against a local server**

Start a local test server:

```bash
python3 -m http.server 18080
```

Create an HTTP check through `/api/admin/monitor/CreateCheck` or direct RPC target, run `RunCheckOnce`, and confirm:

- result is successful;
- latency is greater than zero;
- latest status appears in `GetOverview`.

- [ ] **Step 6: Verify TCP probe**

Use the same local server port `18080` as a TCP target. Create a TCP check and run once. Confirm connected result.

- [ ] **Step 7: Verify webhook alert**

Start a local webhook receiver:

```bash
python3 -m http.server 18081
```

Create a failing HTTP check with `failure_threshold=1`, point webhook to the receiver, run once, and confirm:

- alert state becomes triggered;
- alert event is recorded;
- webhook request is received;
- `dedupe_key` is present in request body.

- [ ] **Step 8: Verify peer ownership**

Start two monitor instances with different `instance_id` and `health.addr` values, configure them as peers, create the same check/rule, and confirm:

- both instances probe;
- only owner sends webhook;
- after stopping owner and waiting for stale timeout, the other instance sends the next reminder/resolution.

- [ ] **Step 9: Verify frontend**

```bash
cd web
pnpm typecheck
pnpm lint
pnpm build
```

Start the web dev server if visual verification is needed:

```bash
cd web
pnpm dev
```

Use browser verification for:

- status cards render without overlapping text;
- table actions work;
- editor drawers validate HTTP/TCP fields;
- peer/detail drawers show data.

- [ ] **Step 10: Record verification**

Create `docs/superpowers/verification/2026-07-09-monitor-module.md` with:

- commands run;
- pass/fail outputs;
- local ports used;
- screenshots or notes for frontend checks;
- any residual risks.

- [ ] **Step 11: Final commit**

```bash
git add docs/superpowers/verification/2026-07-09-monitor-module.md modules/monitor/README.md
git commit -m "docs: record monitor module verification"
```

## Self-Review Checklist

- V1 explicitly supports only HTTP and TCP checks.
- Existing Admin host resource monitoring remains unchanged.
- `moox-monitor` is independently deployable and does not run inside Admin.
- All independently deployable MooX processes get a monitorable health endpoint.
- Monitor monitors itself and can monitor peer monitor instances.
- Multi-instance operation does not require a shared database.
- Alert ownership failover is deterministic and testable.
- Frontend follows MooX UI style while borrowing status-page summary cards.
- Admin is only gateway and SysDeploy source; monitor keeps local synced checks.
- Build, release, deploy, docs, and verification are included.
