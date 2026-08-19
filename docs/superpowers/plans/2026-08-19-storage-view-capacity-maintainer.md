# Storage View Capacity Maintainer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Storage View 的周期性状态收敛统一命名为 View Maintainer，并通过可分层覆盖的容量策略，在任意一个时序序列超过上限时安全启动 A/B 重建，同时让 `custom.toml` 成为初始化和部署的唯一配置来源。

**Architecture:** View Maintainer 继续负责 View 恢复、必要修复、容量检查和 A/B 激活；垃圾文件删除仍由独立 Cleanup Timer 负责。容量策略按“单 View > 系统监控 > 全局默认”解析，DuckDB 在写事务内维护每个 `(subject_id, freq, series_tag)` 的准确行数，使定时检查无需扫描完整 View；任意一个序列超过上限或文件超过硬上限即可触发容量重建，新 B 只保留策略规定的最近根数。

**Tech Stack:** Go 1.23、tRPC-Go、DuckDB、SQLite Metadata、BurntSushi/toml、Bash、Vue 3、Vitest。

## Global Constraints

- 项目尚未上线，不保留 `Reconciler`、`reconcile_*`、`rebuild_check_interval` 等旧名称或配置别名。
- 对外中文统一使用“视图维护”或“View Maintainer”；A/B 构建、覆盖修复和手动重建仍使用“重建”。
- 全局默认 `max_periods_per_series=2000`、`rebuild_lookback_periods=1000`、`max_view_file_bytes=1073741824`。
- 任意一个 `(subject_id, frequency, series_tag)` 的物理行数大于 `max_periods_per_series` 即满足容量触发条件，不等待其他序列达到上限。
- 容量重建的新 B 对每个序列只保留最近 `rebuild_lookback_periods` 根完整数据。
- `max_periods_per_series` 必须严格大于解析后的 `rebuild_lookback_periods`；所有数量必须在 `1..1000000` 范围内。
- 配置优先级固定为：精确 `space_id + view_id` 覆盖 > `moox_system` 系统监控策略 > 全局默认。
- Record/Bleve View 不应用“每序列根数”规则，只应用文件大小硬上限。
- 容量维护继续服从对应 consumer partition 已绑定、积压水位、连续空闲检查、全局单构建许可和 Inactive Slot 可用性门禁。
- View Maintainer 不直接删除旧文件；新 B 激活后只解除旧索引引用，Cleanup Timer 连续确认 60 秒无引用后再删除。
- 默认 Metadata 只保留 3 个行情 View 和 5 个系统监控 View；不再初始化 4 个 `stock_cn` View，也不自动创建 Factor View。
- 所有新增容量触发、跳过、成功和失败结果必须进入 `t_view_rebuild_logs`，不得只写进程日志。

---

### Task 1: 将 Reconciler 全量重命名为 View Maintainer

**Files:**
- Rename: `modules/storage/internal/service/view/reconcile.go` -> `modules/storage/internal/service/view/maintenance.go`
- Rename: `modules/storage/internal/service/view/reconcile_test.go` -> `modules/storage/internal/service/view/maintenance_test.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/service/view/rebuild_log.go`
- Modify: `modules/storage/internal/service/view/event_apply.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/internal/service/catalog/metadata_space_view.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_rebuild.go`
- Modify: `modules/storage/proto/metadata.proto`
- Regenerate: `modules/storage/proto/storagegen/metadata.pb.go`
- Regenerate: `modules/storage/proto/storagegen/metadata.trpc.go`
- Modify: `modules/storage/README.md`

**Interfaces:**
- Produces: `type MaintenanceOptions struct`、`StartViewMaintainer`、`StartViewMaintainerAsync`、`normalizeMaintenanceOptions`、`maintainOnce`、`maintainView`、`startViewMaintenanceLoop`。
- Preserves: `RequestViewRebuild`、`ViewRebuildLog`、A/B build RPC 和 Metadata CAS 语义。

- [ ] **Step 1: 写重命名合同测试**

在 `modules/storage/internal/service/view/maintenance_test.go` 中将现有测试名统一为 `TestViewMaintainer...`，并增加编译期接口断言：

```go
func TestMaintenanceOptionsExposeCapacityInputs(t *testing.T) {
	var opts MaintenanceOptions
	require.Zero(t, opts.Interval)
	require.Nil(t, opts.Metadata)
}
```

- [ ] **Step 2: 运行测试并确认旧符号仍导致合同失败**

Run:

```bash
cd modules/storage
go test ./internal/service/view ./cmd/server -count=1
```

Expected: FAIL，提示 `undefined: MaintenanceOptions` 或 `undefined: StartViewMaintainerAsync`。

- [ ] **Step 3: 执行无兼容别名的完整重命名**

使用 `git mv` 重命名文件，并进行以下一一替换：

```text
ReconcilerOptions          -> MaintenanceOptions
StartReconciler            -> StartViewMaintainer
StartReconcilerAsync       -> StartViewMaintainerAsync
normalizeReconcilerOptions -> normalizeMaintenanceOptions
startReconcilerLoop        -> startViewMaintenanceLoop
reconcileOnce              -> maintainOnce
reconcileView              -> maintainView
reconcilerOptions          -> maintenanceOptions
stopReconciler             -> stopMaintainer
```

注释、错误信息、Proto 注释和 README 不得保留“reconciler/协调器”文案。不要为旧符号添加 type alias 或 wrapper。

- [ ] **Step 4: 验证仓库中不再出现旧名称**

Run:

```bash
rg -n 'Reconciler|reconciler|reconcileOnce|reconcileView|StartReconciler|rebuild_check_interval' modules/storage
```

Expected: 无输出。

- [ ] **Step 5: 重新生成 Storage Proto**

修改 Proto 注释或 RPC 定义后统一运行生成器，不直接手改生成文件：

```bash
cd modules/storage
make proto
git diff --exit-code -- proto/storagegen || true
```

Expected: 生成代码与 `metadata.proto` 一致，不残留旧的 Reconciler 文案。

- [ ] **Step 6: 运行 Storage View 定向测试**

Run:

```bash
cd modules/storage
go test ./internal/service/view/... ./internal/service/catalog ./internal/service/metadata/sqlite ./cmd/server -count=1
```

Expected: PASS。

- [ ] **Step 7: 提交命名变更**

```bash
git add modules/storage
git commit -m "refactor(storage): rename view reconciler to maintainer"
```

---

### Task 2: 定义 custom.toml 容量策略和覆盖优先级

**Files:**
- Modify: `custom.toml.example`
- Modify: `modules/cli/internal/setup/config/config.go`
- Modify: `modules/cli/internal/setup/config/config_test.go`
- Modify: `modules/cli/README.md`

**Interfaces:**
- Produces: `StorageView`、`StorageViewPolicyOverride`、`ResolvedStorageViewPolicy` 和 `ResolvePolicy(spaceID, viewID string)`。
- Consumes: Task 1 的 `maintenance_check_interval` 命名。

- [ ] **Step 1: 在 custom.toml.example 写出完整可复制配置**

```toml
[storage_view]
maintenance_check_interval = "1m"
rebuild_lookback_periods = 1000
max_periods_per_series = 2000
max_view_file_bytes = 1073741824

# 整个区块可省略；省略字段继承 storage_view 全局默认。
[storage_view.system_monitor]
rebuild_lookback_periods = 1000
max_periods_per_series = 2000
max_view_file_bytes = 1073741824

# 单 View 覆盖优先级最高；不填写的数值继承系统监控或全局策略。
[[storage_view.views]]
space_id = "crypto_market"
view_id = "binance_spot_kline_1m_view"
rebuild_lookback_periods = 1000
max_periods_per_series = 2000
max_view_file_bytes = 1073741824
```

- [ ] **Step 2: 先写 Manifest 默认值、解析和校验失败测试**

覆盖以下断言：

```go
assert.Equal(t, "1m", snapshot.Manifest.StorageView.MaintenanceCheckInterval)
assert.Equal(t, uint64(1000), snapshot.Manifest.StorageView.RebuildLookbackPeriods)
assert.Equal(t, uint64(2000), snapshot.Manifest.StorageView.MaxPeriodsPerSeries)
assert.Equal(t, int64(1073741824), snapshot.Manifest.StorageView.MaxViewFileBytes)

resolved := snapshot.Manifest.StorageView.ResolvePolicy("moox_system", "host_disk_view")
assert.Equal(t, uint64(2000), resolved.MaxPeriodsPerSeries)

resolved = snapshot.Manifest.StorageView.ResolvePolicy("crypto_market", "binance_spot_kline_1m_view")
assert.Equal(t, uint64(1000), resolved.RebuildLookbackPeriods)
```

增加重复 `space_id + view_id`、空 ID、非法 interval、`max_periods_per_series <= rebuild_lookback_periods`、超过 1000000 和非正文件上限测试。

- [ ] **Step 3: 运行配置测试确认失败**

Run:

```bash
cd modules/cli
go test ./internal/setup/config -count=1
```

Expected: FAIL，提示新增字段或 `ResolvePolicy` 不存在。

- [ ] **Step 4: 实现配置类型和严格解析**

```go
type StorageViewPolicyOverride struct {
	SpaceID                   string `toml:"space_id" json:"space_id"`
	ViewID                    string `toml:"view_id" json:"view_id"`
	RebuildLookbackPeriods    uint64 `toml:"rebuild_lookback_periods" json:"rebuild_lookback_periods"`
	MaxPeriodsPerSeries       uint64 `toml:"max_periods_per_series" json:"max_periods_per_series"`
	MaxViewFileBytes          int64  `toml:"max_view_file_bytes" json:"max_view_file_bytes"`
}

type StorageView struct {
	MaintenanceCheckInterval string                    `toml:"maintenance_check_interval" json:"maintenance_check_interval"`
	RebuildLookbackPeriods   uint64                    `toml:"rebuild_lookback_periods" json:"rebuild_lookback_periods"`
	MaxPeriodsPerSeries      uint64                    `toml:"max_periods_per_series" json:"max_periods_per_series"`
	MaxViewFileBytes         int64                     `toml:"max_view_file_bytes" json:"max_view_file_bytes"`
	SystemMonitor            StorageViewPolicyOverride `toml:"system_monitor" json:"system_monitor"`
	Views                    []StorageViewPolicyOverride `toml:"views" json:"views"`
}
```

零值只允许出现在 `system_monitor` 和 `views` 覆盖中并表示继承；全局字段缺失时写入固定默认值。解析后的最终策略必须再次执行完整校验。

- [ ] **Step 5: 运行配置和 CLI 测试**

Run:

```bash
cd modules/cli
go test ./internal/setup/config ./internal/command -count=1
```

Expected: PASS。

- [ ] **Step 6: 提交 Manifest 变更**

```bash
git add custom.toml.example modules/cli
git commit -m "feat(cli): define view maintenance capacity policies"
```

---

### Task 3: 初始化和部署时将策略写入 Storage View 包

**Files:**
- Create: `modules/storage/config/storage_view/maintenance.json`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/cli/internal/setup/deploy/deploy.go`
- Modify: `modules/cli/internal/setup/deploy/deploy_test.go`
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `modules/cli/internal/command/setup_test.go`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/tests/contract/test-deploy-moox-storage-view.sh`
- Modify: `scripts/tests/contract/test-deploy-moox-storage-profile.sh`

**Interfaces:**
- Produces deployed file: `<deploy-root>/storage-view/config/maintenance.json`。
- Produces server config key: `storage.view.maintenance_policy_file: config/maintenance.json`。
- Produces one-time deployment flag: `moox-cli setup deploy-storage --reset-view-data`，只清空 View 消费状态、A/B 索引和 active/build 引用，保留 Primary/DataNode 事实数据。
- Removes: `MOOX_STORAGE_VIEW_REBUILD_LOOKBACK_PERIODS` and `MOOX_INSTALLED_STORAGE_VIEW_REBUILD_LOOKBACK_PERIODS`。

- [ ] **Step 1: 写打包合同测试**

测试使用非默认值打包：

```go
StorageView: setupconfig.StorageView{
	MaintenanceCheckInterval: "2m",
	RebuildLookbackPeriods: 800,
	MaxPeriodsPerSeries: 1600,
	MaxViewFileBytes: 805306368,
}
```

解包后读取 `storage-view/config/maintenance.json`，断言值完全一致，并断言 `components.env` 和 `start.sh` 不再含旧的 lookback 环境变量。

- [ ] **Step 2: 运行打包测试确认失败**

Run:

```bash
cd modules/cli
go test ./internal/setup/deploy ./internal/command -count=1
```

Expected: FAIL，提示维护策略文件不存在或内容仍为默认值。

- [ ] **Step 3: 序列化策略并通过部署边界传递**

`StoragePackager.Package` 将完整 `StorageView` JSON 编码为 base64，设置：

```go
payload, err := json.Marshal(opts.StorageView)
encoded := base64.RawStdEncoding.EncodeToString(payload)
command.Env = setCommandEnv(command.Env, "MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64", encoded)
```

`scripts/deploy-moox.sh` 必须：

1. 要求 Storage profile 必须提供该变量；
2. 使用 `base64 --decode` 和 `python3 -m json.tool` 验证；
3. 原子写入 `${STAGE_DIR}/storage-view/config/maintenance.json`；
4. 不把策略留在运行时环境变量或 `components.env`；
5. 远程打包 heredoc 明确传递该变量。

- [ ] **Step 4: 让 View 服务只读取包内策略文件**

`trpc_go.yaml` 增加：

```yaml
storage:
  view:
    maintenance_policy_file: config/maintenance.json
```

仓库内 `maintenance.json` 使用与 `custom.toml.example` 相同的默认值，使本地开发不依赖 setup CLI。

- [ ] **Step 5: 为无兼容物理格式切换增加显式 View-only reset**

本计划会给 DuckDB 增加必需的 `view_series_counts` 表，不兼容旧 A/B 文件。不要做运行时迁移，也不要清空 Primary。新增 `deploy-storage --reset-view-data`，并与现有 `--reset-storage-data` 互斥：

1. `Options` 增加 `ResetViewData bool`，安装脚本使用独立位置参数传递，禁止复用含义不同的 `ResetStorageData`；
2. 新包解压并继承 `data` 后、任何 Storage 服务启动前，调用新包内 `moox-storage-cli reset-view-consumers --restart=false --yes`；
3. reset 必须清除所有 View consumer durable/待处理消息、A/B 物理索引、active/build 引用和旧 checkpoint；
4. 不得删除 Metadata 中保留的 8 个 View 定义，也不得删除 Storage Primary/DataNode Pebble；
5. reset 成功后才启动新包，失败则按 activation token 回滚旧包和原数据；
6. 部署 JSON 必须分别输出 `reset_view_data` 与 `reset_storage_data`，避免运维误判。

增加行为测试，至少证明 Primary marker 保留、旧 DuckDB 文件删除、新服务创建的索引包含 `view_series_counts`，以及两个 reset flag 同时传入时命令拒绝执行。

- [ ] **Step 6: 运行部署合同**

Run:

```bash
bash scripts/tests/contract/test-deploy-moox-storage-view.sh
bash scripts/tests/contract/test-deploy-moox-storage-profile.sh
bash -n scripts/deploy-moox.sh
```

Expected: 全部 PASS。

- [ ] **Step 7: 提交部署链路**

```bash
git add modules/cli modules/storage/config scripts custom.toml.example
git commit -m "feat(deploy): render view maintenance policy from custom toml"
```

---

### Task 4: 在 DuckDB 中维护准确的每序列行数

**Files:**
- Modify: `modules/storage/internal/service/viewindex/model.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/managed_indexes_test.go`

**Interfaces:**
- Produces: `type SeriesCapacityReader interface`。
- Produces: `SeriesCapacity(ctx, indexID string, maxPeriods uint64) (SeriesCapacityResult, error)`。

- [ ] **Step 1: 写容量统计行为测试**

测试必须覆盖：

- 同一 RowKey 的多次字段 patch 只计一行；
- 新 RowKey 使对应序列计数加一；
- `series_tag` 不同必须独立计数；
- 只有一个序列达到 `max+1` 时立即返回该 offender；
- 其他序列不需要达到上限；
- 新建 B 的统计从零开始；
- Record/Bleve 不实现该接口。

预期结果结构：

```go
type SeriesCapacityResult struct {
	Exceeded  bool
	SubjectID string
	Freq      string
	SeriesTag string
	Rows      uint64
}
```

- [ ] **Step 2: 运行 DuckDB 测试确认失败**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test ./internal/service/viewindex/duckdb -run 'Test.*SeriesCapacity' -count=1
```

Expected: FAIL，提示接口、表或方法不存在。

- [ ] **Step 3: 创建并事务性维护 view_series_counts**

每个新 DuckDB 索引创建：

```sql
CREATE TABLE view_series_counts (
  subject_id VARCHAR NOT NULL,
  freq VARCHAR NOT NULL,
  series_tag VARCHAR NOT NULL,
  row_count UBIGINT NOT NULL,
  PRIMARY KEY (subject_id, freq, series_tag)
);
```

在现有 `Write` 事务中，对折叠后的唯一 RowKey 先执行 key-only 插入：

```sql
INSERT INTO view_rows (subject_id, freq, data_time, series_tag)
VALUES (?, ?, ?, ?), ...
ON CONFLICT DO NOTHING
RETURNING subject_id, freq, series_tag;
```

只聚合 `RETURNING` 返回的新行，并在同一事务更新计数：

```sql
INSERT INTO view_series_counts (subject_id, freq, series_tag, row_count)
VALUES (?, ?, ?, ?)
ON CONFLICT (subject_id, freq, series_tag)
DO UPDATE SET row_count = view_series_counts.row_count + excluded.row_count;
```

随后执行现有字段 UPSERT。任何一步失败都回滚数据行和统计，禁止 best-effort 计数。

- [ ] **Step 4: 实现 O(1) offender 查询**

```sql
SELECT subject_id, freq, series_tag, row_count
FROM view_series_counts
WHERE row_count > ?
ORDER BY row_count DESC, subject_id, freq, series_tag
LIMIT 1;
```

没有结果返回 `Exceeded=false`。查询必须使用调用者 context，不做全表 `GROUP BY view_rows`。

- [ ] **Step 5: 运行 DuckDB 正确性和 race 测试**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -race ./internal/service/viewindex/duckdb -count=1
```

Expected: PASS；只允许现有 macOS linker warning。

- [ ] **Step 6: 提交容量统计实现**

```bash
git add modules/storage/internal/service/viewindex
git commit -m "feat(storage): track exact per-series view capacity"
```

---

### Task 5: 在 View Maintainer 中实现分层策略和单序列触发

**Files:**
- Create: `modules/storage/internal/service/view/maintenance_policy.go`
- Create: `modules/storage/internal/service/view/maintenance_policy_test.go`
- Modify: `modules/storage/internal/service/view/maintenance.go`
- Modify: `modules/storage/internal/service/view/maintenance_test.go`
- Modify: `modules/storage/internal/service/view/rebuild_log.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/proto/metadata.proto`
- Regenerate: `modules/storage/proto/storagegen/metadata.pb.go`
- Regenerate: `modules/storage/proto/storagegen/metadata.trpc.go`

**Interfaces:**
- Consumes: Task 3 的 `maintenance.json`。
- Consumes: Task 4 的 `SeriesCapacityReader`。
- Produces: `MaintenancePolicy.Resolve(spaceID, viewID string) ResolvedViewPolicy`。
- Produces trigger: `VIEW_REBUILD_TRIGGER_REASON_SERIES_CAPACITY`。

- [ ] **Step 1: 写策略解析和触发顺序测试**

覆盖以下行为：

```go
resolved := policy.Resolve("moox_system", "host_disk_view")
require.Equal(t, systemPolicy, resolved)

resolved = policy.Resolve("crypto_market", "binance_spot_kline_1m_view")
require.Equal(t, exactOverride, resolved)
```

容量测试必须证明：

- 单个序列 `2001`、其余序列低于 `2000` 时触发；
- `2000` 不触发；
- 文件超过硬上限仍触发；
- 两种条件同时满足时优先记录 `SERIES_CAPACITY` 并在详情附带文件大小；
- Bleve 只检查文件大小；
- `keep_duration=0` 不再屏蔽每序列上限，只有“文件大小整理”继续要求有限保留窗口；
- 任何容量触发仍受 backlog 和 idle checks 门禁；
- revision、active missing、coverage 等必要修复仍优先执行且不受容量门禁。

- [ ] **Step 2: 运行 View 测试确认失败**

Run:

```bash
cd modules/storage
go test ./internal/service/view -run 'Test.*(MaintenancePolicy|SeriesCapacity)' -count=1
```

Expected: FAIL，提示策略解析器或新 trigger 不存在。

- [ ] **Step 3: 加载并解析 maintenance.json**

`cmd/server/main.go` 从 `maintenance_policy_file` 读取 JSON，拒绝：

- 文件不存在或不是 regular file；
- JSON 未知字段；
- 重复 View override；
- 解析后的 `max_periods_per_series <= rebuild_lookback_periods`；
- interval、行数或字节上限非法。

验证成功后构造 `MaintenanceOptions`，不再从环境变量读取 rebuild lookback。

- [ ] **Step 4: 实现容量判定和触发详情**

`maintainView` 先处理必要修复，再为当前 View 解析容量策略。DuckDB time-series View 调用：

```go
capacity, err := reader.SeriesCapacity(ctx, activeIndexID, resolved.MaxPeriodsPerSeries)
```

当 `capacity.Exceeded` 为 true 时，创建原因 `SERIES_CAPACITY` 的构建日志，`details_json` 至少包含：

```json
{
  "subject_id": "BTC-USDT",
  "frequency": "1m",
  "series_tag": "venue:binance",
  "observed_periods": 2001,
  "max_periods_per_series": 2000,
  "rebuild_lookback_periods": 1000,
  "physical_bytes": 81276928
}
```

Primary backfill 使用解析后的 `rebuild_lookback_periods`，不能继续使用全局 map。

- [ ] **Step 5: 保留并重命名现有容量门禁**

将 `sizeLimitBuild...`、`needsSizeLimit...` 命名改为 `capacityMaintenance...`，保持以下语义：

- partition bound；
- pending + ack_pending 水位；
- 连续 idle checks；
- 同进程一次只运行一个容量构建；
- 失败后 30 分钟冷却；
- Inactive Slot 正在退休时跳过。

- [ ] **Step 6: 运行 View 和 server race 测试**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -race ./internal/service/view/... ./cmd/server -count=1
```

Expected: PASS。

- [ ] **Step 7: 提交维护策略实现**

```bash
git add modules/storage
git commit -m "feat(storage): rebuild views when any series exceeds capacity"
```

---

### Task 6: 将容量维护结果展示到现有构建日志

**Files:**
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/views/data/view-browse/index.vue`
- Modify: `web/src/api/storage/metadata.test.ts`
- Create: `web/src/views/data/view-browse/view-maintenance-log.test.ts`
- Modify: `modules/storage/README.md`

**Interfaces:**
- Consumes: `VIEW_REBUILD_TRIGGER_REASON_SERIES_CAPACITY` 和 `details_json`。
- Produces user text: “序列容量维护”。

- [ ] **Step 1: 写日志格式化失败测试**

输入 `SERIES_CAPACITY` 日志，断言弹窗显示：

```text
序列容量维护
BTC-USDT · 1m · venue:binance
2001 / 2000 根
重建后保留 1000 根
```

同时断言文件超限继续显示“文件容量维护”，必要修复和手动修复文案不变。

- [ ] **Step 2: 运行 Web 测试确认失败**

Run:

```bash
cd web
pnpm exec vitest run src/api/storage/metadata.test.ts src/views/data/view-browse/view-maintenance-log.test.ts
```

Expected: FAIL，新枚举或格式化函数尚不存在。

- [ ] **Step 3: 实现展示并容错 details_json**

只展示白名单字段；JSON 缺失或解析失败时保留通用终态，不把原始错误、凭据或完整请求体直接输出到页面。

- [ ] **Step 4: 运行 Web 验证**

Run:

```bash
cd web
pnpm exec vue-tsc --noEmit
pnpm exec vitest run src/api/storage/metadata.test.ts src/views/data/view-browse/view-maintenance-log.test.ts
pnpm run build:prod
```

Expected: 全部 PASS。

- [ ] **Step 5: 提交 Web 和文档**

```bash
git add web modules/storage/README.md
git commit -m "feat(web): show per-series view maintenance logs"
```

---

### Task 7: 将默认逻辑 View 收敛为 3 个行情加 5 个系统监控

**Files:**
- Modify: `examples/setup/default/metadata.yaml`
- Modify: `modules/storage/internal/bootstrap/metadata/seed_test.go`
- Modify: `custom.toml.example`
- Modify: `modules/cli/internal/setup/config/config_test.go`
- Create: `modules/storage/cmd/cli/retain_views.go`
- Create: `modules/storage/cmd/cli/retain_views_test.go`
- Modify: `modules/storage/cmd/cli/main.go`
- Modify: `skills/moox/references/cli-operations.md`

**Interfaces:**
- Produces exact default View allowlist:
  - `crypto_market/binance_spot_kline_1m_view`
  - `crypto_market/perpetual_kline_1h_view`
  - `crypto_market/spot_kline_1h_view`
  - `moox_system/host_resource_view`
  - `moox_system/host_fs_view`
  - `moox_system/host_disk_view`
  - `moox_system/host_net_view`
  - `moox_system/moox_service_metrics_view`
- Produces one-time command: `moox-storage-cli retain-views --metadata-db <path> --keep-view <space/view>... --yes`。

- [ ] **Step 1: 写默认 View 集合合同测试**

测试解析 `metadata.yaml`，排序后与上述 8 个完整 ID 精确相等；任何第 9 个 View 都导致失败。

- [ ] **Step 2: 运行 seed 测试确认失败**

Run:

```bash
cd modules/storage
go test ./internal/bootstrap/metadata -run TestDefaultViewInventory -count=1
```

Expected: FAIL，实际仍包含 4 个 `stock_cn` View。

- [ ] **Step 3: 删除默认 stock_cn View 和对应 View columns**

从 seed 删除：

```text
stock_cn/stock_kline_1d_view
stock_cn/index_kline_1d_view
stock_cn/financial_metric_view
stock_cn/financial_summary_view
```

保留其 Dataset 元数据，不创建查询 View。删除所有引用这些 View ID 的 `view_columns` 条目。

- [ ] **Step 4: 关闭默认 Factor 自动导入**

`custom.toml.example` 将：

```toml
[factors]
enabled = false
```

保留示例 Factor 条目作为注释示例，但 setup 默认不得创建 Factor Dataset、View 或 binding。

- [ ] **Step 5: 运行 seed 和 setup 测试**

Run:

```bash
cd modules/storage
go test ./internal/bootstrap/metadata -count=1
cd ../../modules/cli
go test ./internal/setup/config ./internal/command -count=1
```

Expected: PASS。

- [ ] **Step 6: 先写 retain-views 破坏性命令测试**

测试创建含 8 个保留 View、2 个待删除 View、View columns、build 和 rebuild logs 的 SQLite fixture，断言：

- 缺少 `--yes` 拒绝执行；
- keep 集合不是精确 8 个 ID 时拒绝执行；
- 任一 keep View 不存在或不是 active 时拒绝执行；
- Storage View 进程仍存活时拒绝执行；
- 成功后只删除不在 keep 集合中的 `t_views` 行；
- 外键级联删除其 columns、build 和 rebuild logs；
- 8 个保留 View 的 active index、revision 和日志不变；
- 命令只输出待清理的 `engine + active_index_id/build_index_id`，不删除物理文件。

- [ ] **Step 7: 实现 retain-views 原子元数据收敛**

命令要求调用方先停止 `storage-view`，以 `BEGIN IMMEDIATE` 执行完整校验和删除。固定输出 JSON：

```json
{
  "status": "ready_for_physical_cleanup",
  "kept_views": 8,
  "deleted_views": 6,
  "retired_indexes": [
    {"engine":"duckdb","index_id":"view_..._a"}
  ]
}
```

不得调用 `os.Remove`；Storage View 重启后由 Cleanup Timer 发现并删除这些无引用索引。

- [ ] **Step 8: 运行 CLI 和 Metadata 测试**

Run:

```bash
cd modules/storage
go test ./cmd/cli ./internal/service/metadata/sqlite -count=1
```

Expected: PASS。

- [ ] **Step 9: 提交默认库存收敛**

```bash
git add examples custom.toml.example modules/storage/internal/bootstrap/metadata modules/storage/cmd/cli modules/cli skills/moox
git commit -m "chore(setup): keep only active market and system views"
```

---

### Task 8: 全量验证、生产清理和真实容量重建验收

**Files:**
- Create: `scripts/tests/e2e/test-storage-view-series-capacity.sh`
- Modify: `Makefile`
- Modify: `docs/superpowers/plans/2026-08-19-storage-view-capacity-maintainer.md`

**Interfaces:**
- Consumes: 前七个 Task 的二进制、配置、Metadata seed 和日志。
- Produces: 本地、发布包和生产验收证据。

- [ ] **Step 1: 运行多模块本地门禁**

```bash
cd modules/storage
CGO_ENABLED=1 go test -race ./internal/service/view/... ./internal/service/viewindex/duckdb ./internal/bootstrap/metadata ./cmd/server -count=1
go vet ./internal/service/view/... ./internal/service/viewindex/duckdb ./cmd/server

cd ../cli
go test ./internal/setup/config ./internal/setup/deploy ./internal/command -count=1

cd ../../web
pnpm exec vue-tsc --noEmit
pnpm run build:prod

cd ..
bash scripts/tests/contract/test-deploy-moox-storage-view.sh
bash scripts/tests/contract/test-deploy-moox-storage-profile.sh
git diff --check
```

Expected: 全部 PASS。

- [ ] **Step 2: E2E 证明“一个序列超限即可触发”**

E2E 创建两个序列：A 写入 `max+1` 根、B 写入 1 根；运行一次 View Maintainer，断言：

- 原 A 继续可读；
- Metadata 创建原因 `SERIES_CAPACITY` 的 B build；
- B 激活后 A、B 分别最多保留 `rebuild_lookback_periods` 根；
- 构建日志 offender 是序列 A；
- 旧 Slot 解除引用后由 Cleanup Timer 删除；
- 序列 B 未达上限不阻止触发。

Run:

```bash
bash scripts/tests/e2e/test-storage-view-series-capacity.sh
```

Expected: `storage view series capacity e2e passed`。

- [ ] **Step 3: 构建 Linux Storage 二进制**

```bash
MOOX_CLI=/tmp/moox-cli-darwin ./scripts/build-storage-linux.sh
shasum -a 256 bin/moox-storage-primary bin/moox-storage-node bin/moox-storage-view bin/moox-storage-cli
```

Expected: 四个 Linux/amd64 产物存在，三个 Storage server 的 hash 一致。

- [ ] **Step 4: 发布前备份并清理当前多余 View 元数据**

先停止 Factor 自动创建路径，再停止 `storage-view`。使用 SQLite online backup API 或 `sqlite3 .backup` 生成一致备份，禁止分别复制主文件、WAL 和 SHM。然后执行：

```bash
/home/ubuntu/moox/storage/bin/moox-storage-cli retain-views \
  --metadata-db /home/ubuntu/moox/storage/data/storage/metadata/storage_metadata.db \
  --keep-view crypto_market/binance_spot_kline_1m_view \
  --keep-view crypto_market/perpetual_kline_1h_view \
  --keep-view crypto_market/spot_kline_1h_view \
  --keep-view moox_system/host_resource_view \
  --keep-view moox_system/host_fs_view \
  --keep-view moox_system/host_disk_view \
  --keep-view moox_system/host_net_view \
  --keep-view moox_system/moox_service_metrics_view \
  --yes
```

命令应删除以下 6 个逻辑 View 的 Metadata 行：

```text
crypto_market/bin_988b08e19ae99e18
crypto_market/binance_spot_kline_1m_factor_v
stock_cn/stock_kline_1d_view
stock_cn/index_kline_1d_view
stock_cn/financial_metric_view
stock_cn/financial_summary_view
```

禁止直接 `rm` 物理文件。重新启动 `storage-view` 后等待 Cleanup Timer 删除其 DuckDB/Bleve 物理索引。

- [ ] **Step 5: 使用新包执行一次 View-only 格式重置并部署**

```bash
MOOX_SKIP_STORAGE_BUILD=1 /tmp/moox-cli-darwin setup deploy-storage \
  --file ./custom.toml \
  --host control \
  --reset-view-data
```

Expected JSON:

```json
{"host":"control","reset_storage_data":false,"reset_view_data":true,"status":"ready"}
```

该步骤是本次无兼容 schema 切换的强制门禁，只执行一次。它必须保留 Primary K 线事实数据，并让 8 个逻辑 View 全部从 Primary 按 `rebuild_lookback_periods` 重建；不得使用 `--reset-storage-data`。

- [ ] **Step 6: 验证配置确实来自 custom.toml**

在目标机比较 `custom.toml` 解析结果和：

```text
/home/ubuntu/moox/storage/storage-view/config/maintenance.json
```

断言全局、系统监控和单 View override 完全一致，并确认进程环境中不存在旧 lookback 变量。

- [ ] **Step 7: 触发受控容量维护并观察 A/B 全链路**

为测试 View 使用临时精确 override，将 `max_periods_per_series` 设为当前最大序列行数减一；等待 Maintainer Timer 后验证：

1. 日志显示一个明确 offender；
2. build 原因是 `SERIES_CAPACITY`；
3. Active A 在构建期间持续可读；
4. 新 B 每个序列不超过目标根数；
5. B 激活后旧 A 继续存在至少 60 秒；
6. Cleanup Timer 最终删除旧 A；
7. 其他 7 个保留 View 没有被重建或删除。

- [ ] **Step 8: 恢复正式阈值并完成库存验收**

恢复生产策略后，Metadata 必须满足：

```text
active logical views = 8
duplicate (space_id, view_id) = 0
active/build physical index missing = 0
unreferenced managed indexes after cleanup grace = 0
```

同时检查 `storage-primary`、`storage-view`、`storage-node` ready，Watchdog `enabled/active/waiting`，并记录部署 commit、dirty 状态和二进制 hash。

- [ ] **Step 9: 更新计划执行记录并提交**

将真实命令、时间、hash、8 个 View 清单、容量重建日志 ID 和 Cleanup Timer 删除日志写入本文末尾的“执行记录”，然后提交：

```bash
git add scripts/tests/e2e/test-storage-view-series-capacity.sh Makefile docs/superpowers/plans/2026-08-19-storage-view-capacity-maintainer.md
git commit -m "test(storage): verify view capacity maintenance end to end"
```

## Execution Record

2026-08-19 实际执行记录：

- 本地验证：`modules/storage` CLI、Pebble、View、config 测试通过；`modules/cli` setup/deploy、setup/config、command 测试通过；CGO race（View/Pebble/DuckDB）通过；前端 `vue-tsc` 与生产构建通过；Storage View/Watchdog 部署合同与 `git diff --check` 通过。
- 容量 E2E：新增 `scripts/tests/e2e/test-storage-view-series-capacity.sh` 和 `make test-storage-view-series-capacity`；测试使用真实 DuckDB，验证单个序列超过上限即可触发、另一个序列不超限、审计详情包含 offender、A/B replacement 激活后每序列保留 lookback 根数，命令已通过，并已接入 `verify-pr`。
- Linux 发布：使用 `make build-storage-linux` 构建并通过 `moox-cli setup deploy-storage --file ./custom.toml --host control` 发布至 `106.53.107.122`；部署返回 `status=ready`。目标机 `maintenance.json` 已写入 1m/1000/2000/1GiB 策略，Storage Primary/View/Node 均运行，`moox-storage-view-watchdog.timer` 为 `active/waiting` 且已重新 arm。
- View-only 重置与库存收敛：使用具备 JetStream 管理权限的 `internal-admin.yaml` 执行 `reset-view-consumers --restart=false --yes`，保留 Primary 事实数据；`retain-views` 后 Metadata 保留 8 个逻辑 View（3 个行情、5 个系统监控），重复 View 已清理。
- 真实 A/B 验证：`binance_spot_kline_1m_view` 构建日志 `1145` 以 `490000` 条回填成功并激活 A；Cleanup Timer 日志确认已删除 4 个废弃物理索引。后续日志 `1153` 的触发原因实际为 `COVERAGE_REPAIR`（不是 `SERIES_CAPACITY`），成功激活 B；构建期间 A 仍是 active。容量触发由真实 DuckDB E2E 覆盖，生产环境尚未观察到独立的 `SERIES_CAPACITY` 日志。
- 仍需后续补充：通配 selector 长保留期的性能压测，以及跨 tag 分页、TTL 后读取、重启 marker、cleanup/materialize 竞态的集成测试。
