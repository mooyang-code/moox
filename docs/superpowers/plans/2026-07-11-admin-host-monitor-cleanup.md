# Admin Legacy Host Monitor Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove Admin's retired Node Exporter host-monitoring path and turn `/ops/resource-monitor` into a compact host-card dashboard backed only by `moox-monitor`.

**Architecture:** Admin stops constructing or exposing the legacy Monitor service and no longer owns host metrics configuration or history schema. The web page continues through the Admin control gateway to the independent Monitor module, keeps at most ten hosts in a responsive card wall, and loads history only for the selected agent.

**Tech Stack:** Go 1.24, tRPC-Go, Protocol Buffers, SQLite, Vue 3, TypeScript, Arco Design, VChart.

---

## File Map

- Delete `modules/admin/internal/service/monitor/**`: retired scraper, parser, calculator, DAO, models, timers, service, and RPC adapter.
- Modify `modules/admin/internal/bootstrap/services.go`: remove Monitor construction and `Services.Monitor`.
- Modify `modules/admin/internal/bootstrap/trpc.go`: remove Admin Monitor RPC registration.
- Modify `modules/admin/internal/bootstrap/bootstrap.go`: remove stale compatibility comments.
- Modify `modules/admin/internal/config/app.go`, `modules/admin/config/app.yaml`: remove Node Exporter monitor configuration.
- Modify `modules/admin/schema/admin.sql`, `modules/admin/schema/schema_test.go`: remove legacy history table from new schemas.
- Modify/regenerate `modules/admin/proto/ops_service.proto`, `modules/admin/proto/admingen/ops_service.pb.go`, `modules/admin/proto/admingen/ops_service.trpc.go`: remove HostMetrics messages and Monitor service.
- Modify `modules/admin/config/trpc_go.yaml`: remove `trpc.moox.ops.Monitor`.
- Modify `modules/admin/internal/service/sysdeploy/defaults.go`, `defaults_test.go`: remove the old Admin deployment row and keep `moox_monitor`.
- Modify `modules/admin/README.md`, `docs/监控配置.md`: describe the independent Host Agent/Monitor/Storage path only.
- Modify `web/src/api/modules/host-monitor.ts`: expose pure mapping helpers and aggregate card data correctly.
- Modify `web/src/views/container/resource-monitor/resource-monitor.vue`: implement the compact card wall, selected-host details, device tables, and stable refresh behavior.
- Create `modules/admin/test/admin_host_monitor_cleanup_e2e_test.go`: verify the Admin runtime contract contains no legacy monitor surface.

### Task 1: Lock the Removal Contract With Failing Tests

**Files:**
- Modify: `modules/admin/schema/schema_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Create: `modules/admin/test/admin_host_monitor_cleanup_e2e_test.go`

- [ ] **Step 1: Add a schema assertion that the legacy table is absent**

Add a test that opens a fresh Admin database, applies `schema.SQL()`, queries `sqlite_master`, and fails when `t_host_monitor_history` exists:

```go
func TestAdminSchemaExcludesLegacyHostMonitorHistory(t *testing.T) {
    db := openTestDB(t)
    if err := applySchema(db); err != nil { t.Fatal(err) }
    var count int
    if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='t_host_monitor_history'`).Scan(&count).Error; err != nil { t.Fatal(err) }
    if count != 0 { t.Fatal("legacy host monitor history table still exists") }
}
```

- [ ] **Step 2: Change the SysDeploy expectation**

Replace the assertion that `monitor` exists with:

```go
if _, ok := byName["monitor"]; ok {
    t.Fatal("legacy admin monitor deployment row still exists")
}
if _, ok := byName["moox_monitor"]; !ok {
    t.Fatal("independent moox_monitor deployment row missing")
}
```

- [ ] **Step 3: Add the module-root contract test**

Create `modules/admin/test/admin_host_monitor_cleanup_e2e_test.go` in package `test`. Read the authoritative source files and assert:

```go
func TestAdminHasNoLegacyHostMonitorSurface(t *testing.T) {
    root := repoRoot(t)
    assertNotContains(t, filepath.Join(root, "modules/admin/schema/admin.sql"), "t_host_monitor_history")
    assertNotContains(t, filepath.Join(root, "modules/admin/config/trpc_go.yaml"), "trpc.moox.ops.Monitor")
    assertNotContains(t, filepath.Join(root, "modules/admin/proto/ops_service.proto"), "service Monitor")
    assertNotContains(t, filepath.Join(root, "modules/admin/config/app.yaml"), "node_exporter_port")
}
```

- [ ] **Step 4: Run the tests and confirm failure**

Run:

```bash
go test -count=1 ./modules/admin/schema ./modules/admin/internal/service/sysdeploy ./modules/admin/test
```

Expected: FAIL because the legacy table, deployment row, RPC service, and configuration still exist.

### Task 2: Delete the Admin Runtime Implementation

**Files:**
- Delete: `modules/admin/internal/service/monitor/**`
- Modify: `modules/admin/internal/bootstrap/services.go`
- Modify: `modules/admin/internal/bootstrap/trpc.go`
- Modify: `modules/admin/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: Remove Monitor from the service container**

Delete the monitor import, `Services.Monitor`, construction, and global initialization. The resulting service construction must end with:

```go
services := &Services{
    DBManager: dbManager,
    SpaceMgr: spaceService,
    SSHService: sshService,
    SecretService: secretService,
    SysDeploy: sysDeployService,
}
```

- [ ] **Step 2: Remove tRPC registration**

Delete the `monitorrpc` import and these lines:

```go
monitorSvc := monitorrpc.NewService(services.Monitor)
adminpb.RegisterMonitorService(s.Service("trpc.moox.ops.Monitor"), monitorSvc)
```

- [ ] **Step 3: Delete the package**

Delete every file under `modules/admin/internal/service/monitor`. Confirm no runtime reference remains:

```bash
rg -n 'internal/service/monitor|InitMonitorInstance|HandleMonitorSchedule|HandleMonitorCleanupSchedule' modules/admin
```

Expected: no matches.

- [ ] **Step 4: Run Admin Go tests**

Run:

```bash
go test -count=1 ./modules/admin/...
```

Expected: compile failures only from the still-present proto/config/schema contracts, not from deleted Go imports.

### Task 3: Remove the Admin Protocol, Configuration, Schema, and Deployment Row

**Files:**
- Modify: `modules/admin/proto/ops_service.proto`
- Modify/regenerate: `modules/admin/proto/admingen/ops_service.pb.go`, `ops_service.trpc.go`
- Modify: `modules/admin/config/trpc_go.yaml`
- Modify: `modules/admin/internal/config/app.go`
- Modify: `modules/admin/config/app.yaml`
- Modify: `modules/admin/schema/admin.sql`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`

- [ ] **Step 1: Remove the Monitor proto surface**

Delete `CPUMetrics`, `MemoryMetrics`, `DiskMetrics`, `NetworkSpeed`, `LoadMetrics`, `HostMetrics`, `HistoryPoint`, all Monitor request/response messages, and `service Monitor` from `ops_service.proto`.

- [ ] **Step 2: Regenerate Admin protobuf code**

Run:

```bash
make -C modules/admin/proto all
```

Expected: generated files contain no `MonitorService`, `GetCurrentMetrics`, or `GetHistoryMetrics`.

- [ ] **Step 3: Remove the dedicated tRPC service entry**

Delete the `trpc.moox.ops.Monitor` service block from `modules/admin/config/trpc_go.yaml`. Keep the independent `moox_monitor` deployment and gateway route untouched.

- [ ] **Step 4: Remove MonitorConfig**

Change `AppConfig` to:

```go
type AppConfig struct {
    Database DatabaseConfig `yaml:"database"`
}
```

Delete `MonitorConfig`, monitor defaults, environment overrides, and `GetMonitorConfig`. Remove the `monitor:` block from `modules/admin/config/app.yaml`.

- [ ] **Step 5: Remove the legacy schema and deployment row**

Delete the `t_host_monitor_history` table and all three indexes from `admin.sql`. Delete:

```go
deployment("monitor", "admin_rpc", "http", "127.0.0.1", 11103, "trpc.moox.ops.Monitor", "internal", "资源监控 RPC 服务"),
```

- [ ] **Step 6: Run contract tests**

Run:

```bash
go test -count=1 ./modules/admin/schema ./modules/admin/internal/service/sysdeploy ./modules/admin/test ./modules/admin/...
```

Expected: PASS.

### Task 4: Clean Documentation and Release References

**Files:**
- Modify: `modules/admin/README.md`
- Modify: `docs/监控配置.md`
- Search: repository runtime and release files

- [ ] **Step 1: Remove the retired Admin module from README**

Delete the `monitor/ | trpc.moox.ops.Monitor` row. Add one sentence stating that host monitoring belongs to `moox-host-agent`, EventBus, `moox-monitor`, and Storage.

- [ ] **Step 2: Replace Node Exporter documentation**

Keep only the current configuration and deployment flow:

```text
moox-host-agent -> EventBus -> moox-monitor -> Storage
```

Document `ListHostAgents`, `QueryHostMetricHistory`, 72-hour retention, unavailable values, and the Host Agent deployment skill. Remove Node Exporter download, systemd, Admin environment variables, and tuning examples.

- [ ] **Step 3: Verify repository cleanliness**

Run:

```bash
rg -n 'trpc\.moox\.ops\.Monitor|node_exporter_port|MONITOR_COLLECT_TIMEOUT|t_host_monitor_history|internal/service/monitor' modules/admin scripts web/src docs --glob '!docs/superpowers/**'
```

Expected: no matches.

### Task 5: Improve Host Monitor Data Mapping

**Files:**
- Modify: `web/src/api/modules/host-monitor.ts`
- Create: `web/scripts/check-host-monitor-contract.mjs`
- Modify: `web/package.json`

- [ ] **Step 1: Extract deterministic card helpers**

Export helpers with stable input/output types:

```ts
export const maxAvailableFilesystemUsage = (items: DiskMetrics[]) => {
  const values = items.filter(item => item.percent_available).map(item => item.percent);
  return values.length ? Math.max(...values) : null;
};

export const aggregateNetworkRate = (items: NetworkSpeed[]) => {
  const available = items.filter(item => item.rate_available);
  if (!available.length) return null;
  return available.reduce((sum, item) => ({ rx: sum.rx + item.rx_speed, tx: sum.tx + item.tx_speed }), { rx: 0, tx: 0 });
};
```

- [ ] **Step 2: Preserve detailed device data**

Map filesystems, disks, and networks without discarding rows. Add explicit `last_seen_at`, `storage_available`, and availability flags to the UI model. Keep `host_id` as the history selection key.

- [ ] **Step 3: Add a static contract check**

Create `web/scripts/check-host-monitor-contract.mjs` that reads the API and Vue files and fails unless it finds `host_id`, `maxAvailableFilesystemUsage`, `aggregateNetworkRate`, `storage_available`, `data_gap`, and the `3d` selector. Add:

```json
"check:host-monitor": "node scripts/check-host-monitor-contract.mjs"
```

- [ ] **Step 4: Run the mapping/build checks**

Run:

```bash
pnpm -C web check:host-monitor
pnpm -C web build:prod
```

Expected: PASS.

### Task 6: Rebuild the Resource Monitor Card Wall

**Files:**
- Modify: `web/src/views/container/resource-monitor/resource-monitor.vue`

- [ ] **Step 1: Replace oversized summary cards**

Use one compact header row with online count, attention count, Storage status, last refresh, refresh button, and auto-refresh switch. Do not nest cards inside a page card.

- [ ] **Step 2: Implement the responsive card grid**

Use stable CSS grid tracks:

```scss
.host-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}
@media (max-width: 1200px) { .host-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 720px) { .host-grid { grid-template-columns: minmax(0, 1fr); } }
```

Each card shows host identity, freshness, CPU, memory, maximum filesystem usage, and aggregate network rates. Clicking a card sets `selectedHostID`.

- [ ] **Step 3: Add selected-host detail tables**

Render unframed filesystem, disk, and network tables next to the trend chart. Use `--` for unavailable rate values and preserve long device or mount names with wrapping or ellipsis tooltips.

- [ ] **Step 4: Stabilize refresh behavior**

`refreshData(true)` updates cards only. `loadHistory()` runs only after selecting a host, changing `historyDuration`, or manual refresh. Preserve the last successful card data when a refresh fails.

- [ ] **Step 5: Verify production rendering**

Run the dev server, capture desktop and mobile screenshots, and verify no overlap, blank chart, clipped labels, or unstable card dimensions.

### Task 7: Final End-to-End Verification and Review

**Files:**
- Test: `modules/admin/test/admin_host_monitor_cleanup_e2e_test.go`
- Test: `web/scripts/check-host-monitor-contract.mjs`

- [ ] **Step 1: Run the module-root end-to-end test**

Run:

```bash
go test -count=1 ./modules/admin/test
```

Expected: PASS and proof that Admin exposes no legacy monitor schema, service, config, or deployment row.

- [ ] **Step 2: Run all affected tests**

Run:

```bash
go test -race -count=1 ./modules/admin/... ./modules/monitor/...
pnpm -C web check:host-monitor
pnpm -C web build:prod
./scripts/check-module-boundaries.sh
git diff --check
```

Expected: all commands pass.

- [ ] **Step 3: Start an independent review agent**

Ask the agent to review deletion completeness, Admin gateway regressions, frontend identity handling, unavailable states, responsive layout, and missing tests. Fix every P0/P1 finding and rerun Step 2.

- [ ] **Step 4: Commit the implementation**

```bash
git add modules/admin web/src web/scripts web/package.json docs/监控配置.md docs/superpowers/plans/2026-07-11-admin-host-monitor-cleanup.md
git commit -m "refactor(admin): remove legacy host monitoring"
```
