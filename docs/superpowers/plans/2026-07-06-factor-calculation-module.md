# Factor Calculation Module Implementation Plan

> **状态：历史实施计划，不是当前 Factor 架构事实源。**
> 该计划中的 durable inbox、FactorRun、Arrow、截面因子、多实例分片和 replay
> 持久化已经被 2026-07-26 的个人量化简化决策取代。当前实施依据为
> [Factor Best-Effort Simplification](2026-07-26-factor-best-effort-simplification.md)。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `modules/factor` 从健康检查占位服务落地为事件驱动的因子计算服务，支持邢不行风格 Python 时序因子、Storage 事件触发、结果 Dataset 写回、补算与管理面。

**Architecture:** V1 采用单进程 factor 服务：NATS JetStream trigger 消费 Storage 行变更事件，scheduler 合并并保序调度 per-symbol 任务，storageio 回读 K 线窗口并写回独立因子结果 Dataset，engine 通过本机 Python stdio worker 池执行 pandas 因子。V2/V3 在同一任务结构和 `Executor` 接口上扩展 Arrow、截面因子与多实例分片，不改变 V1 主链路。

**Tech Stack:** Go 1.24, tRPC-Go, GORM + SQLite, NATS JetStream (`github.com/nats-io/nats.go`), Storage `proto/gen`, Python 3 + pandas/numpy/pytest, Vue 3 + Arco Design, existing admin gateway and SysDeploy.

---

## Reading Summary

我已阅读 `docs/因子计算模块设计.md`，关键理解如下：

- `factor` 是独立业务服务，不进入 `admin` 或 `storage` 内部；跨模块只依赖稳定 proto 包。
- 触发源必须是 Storage 的 NATS JetStream 事件 `moox.event.storage.time_series.rows_changed.v1`，Storage 当前配置文件仍写着 `eventbus.type: memory`，上线前必须显式切到 `nats`。
- 因子结果必须写入独立结果 Dataset，避免 Storage 写回事件再次触发 factor 自身。
- 任务粒度固定为 per-symbol 批量：同一 symbol 的 K 线窗口只回读一次，再计算该 symbol 绑定的所有启用因子。
- Python 因子协议保持 `signal(df, n, factor_name)` / `signal_multi_params(df, param_list)`，worker 对存量因子文件零侵入。
- V1 不做截面因子和跨机执行，但需要在任务结构、Executor、编码协议和分片过滤处预留演进边界。

## Scope And Delivery Slices

这个设计横跨引擎、触发、管理面、前端、截面因子和多实例分片，不能作为一个巨型提交执行。按可独立验收的软件切片拆成六个里程碑：

- **M1 Engine Run-Once:** 本地 SQLite、registry 最小闭环、pyworker、Go stdio executor、storageio 读写、`factor-cli run-once` 端到端。
- **M2 Realtime Pipeline:** NATS trigger、debounce、scheduler、subject 保序、supersede、K 线窗口缓存、重试。
- **M3 Management Plane:** `FactorMgr` proto/RPC、CRUD、绑定、补算、运行记录查询、SysDeploy/admin gateway 接入、前端因子管理页。
- **M4 Observability And Arrow:** engine 状态、慢因子统计、对账 timer、热重载、Arrow IPC 编码协商。
- **M5 Cross-Section Factors:** 截面因子依赖 DAG、因子结果 Dataset watermark、long-format 宽表组装、截面队列与写回。
- **M6 Multi-Instance Sharding:** primary/replica 心跳、分片计划、分配同步、前端分片页、一键接管。

V1 生产可用验收边界是 **M1 + M2 + M3 的核心链路**。M4 提升可观测和大窗口性能；M5/M6 保持为后续版本，但文件和接口命名从第一天预留。

## Plan Review Notes (2026-07-06)

对照当前代码核实后，本计划做如下校正（已并入下方 Decisions / Tasks，实施时以这些为准）：

- **Storage proto 包名是 `storagepb`，不是 `gen`**。import path 为 `github.com/mooyang-code/moox/modules/storage/proto/gen`，但 package 声明为 `package storagepb`。所有引用一律 `storagepb.XxxReq`。已核实 `CreateFactorReq`(含 `Factor` 子消息)、`CreateDatasetReq`(含 `Dataset`)、`UpsertDatasetColumnReq`(含 `DatasetColumn`)、`WriteTimeSeriesRowsReq/Rsp`、`ReadTimeSeriesRowsReq/Rsp`、`NewAccessClientProxy`、`NewMetadataClientProxy`、`FieldValueType_FIELD_VALUE_TYPE_DOUBLE=3`、`DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR=2` 均存在。
- **collector 没有 `ReadTimeSeriesRows` 调用示例，`storageio` 包也不存在**——Task 6 是全新实现，不能"照抄"。写入侧可参考 `modules/collector/internal/sources/binance/storage_rpc.go`（`normalizeStorageTarget` 加 `ip://` 前缀、构造 `AuthInfo`、`WriteTimeSeriesRows`）与 `kline.go:276-292`（列名/`doubleField`/`intField`/时间放 `TimeSeriesKey.data_time`）；读取侧的 Req/Rsp 形状参考 `web/src/api/storage/access.ts`。
- **调用 Storage 必须构造 `AuthInfo`**（复用 collector 的 service-auth 构造方式），虽然 Storage 服务端当前未强校验，但保持一致以免后续开启鉴权时返工。
- **SQLite `synchronous` 用 `NORMAL`**，不沿用 collector 的 `OFF`（后者已被代码审查列为 C13 缺陷）。
- **`web/src/views/data/factors/index.vue` 已存在**（Storage 元数据的因子字典页，调用 `metadata.CreateFactor/ListFactors`）。M3 新增的 `web/src/views/factor/*` 是生产因子管理面，二者并存；为避免用户混淆，M3 需在菜单文案上区分（如"因子字典（元数据）" vs "因子计算"）。
- **`modules/factor` 尚无 proto 目录、go.work 无 factorgen 条目、SysDeploy 无 factormgr 条目、build.sh 仍指向 `./cmd/moox-factor`**——均需按计划新增/修改。生成的 `factorgen` 需要一个手写 `go.mod`（module path `github.com/mooyang-code/moox/modules/factor/proto/factorgen`），与 `modules/storage/proto/gen/go.mod` 同构（storage 用 `trpc-open create --nogomod`，go.mod 是手工维护的）。
- **M1 run-once 的绑定缺口（重要）**：管理面（含 UpsertBinding RPC）在 M3 才有，但 Task 7/8 的 run-once 依赖"已启用绑定"。M1 阶段没有任何 binding 创建入口。修正见下方 Decisions。
- **SQLite 表结构已按仓库惯例与设计文档复审修正**（2026-07-06 第二轮）：时间列改为全仓统一的 `c_ctime`/`c_mtime`（原 `c_create_time`/`c_update_time` 全仓 7 个 schema 文件中零出现）；mtime 触发器加 `WHEN` 守卫；`t_factor_bindings` 补自然键唯一索引（UpsertBinding 语义依赖它防重复绑定）；`c_lookback_bars` 去掉 `DEFAULT 0`；`t_factor_runs` 去掉 `running` 状态（终态一次写入）；新增 `runs_retention_days` 配置与清理 timer（1m 高频约 72 万行/天，设计文档要求近 N 天保留但原计划遗漏）。设计文档中的 schema、config 样例（db path、storage target 裸地址、proto 生成目录 `factorgen`）已同步更新，两文档现已一致。
- **与设计文档的已知良性差异**：计划把 `cli init/import` 提前到 M1（设计文档排在 M3）——init 是一切本地持久化的前置，import 是 run-once 的前置，提前是必要修正而非偏离。

## Decisions

- Canonical server path 改为 `modules/factor/cmd/server`，并修改 `scripts/build/build.sh` 的 factor target；旧 `cmd/moox-factor` 在迁移提交中删除，不继续承载主入口。
- **M1 run-once 不依赖 binding**：run-once 直接对"选定 source dataset/freq/subject + 全部 enabled 因子定义"计算，target dataset 由 `ResultDataset(source)` 推导，命令行可选 `--factors id1,id2` 覆盖。binding 表在 M2 起才成为实时链路的权威来源；M3 提供 binding 的 RPC/CLI 创建。这样 M1 端到端不被管理面阻塞。
- 新建 `modules/factor/cmd/cli`，提供 `init`、`import`、`run-once` 三个子命令；CLI 只操作 factor 自己的 SQLite 和公开 Storage RPC。
- 新建 `modules/factor/proto/factor.proto`，生成包放在 `modules/factor/proto/factorgen`，并加入 `go.work`。
- Trigger 直接使用 `github.com/nats-io/nats.go`；不能 import `modules/storage/internal/service/transport`，因为模块边界脚本禁止跨模块 internal 依赖。
- M5 截面结果默认写入独立 Dataset，命名 `{source_dataset去掉_kline}_section`，避免事件只含 key 时无法区分时序列写回与截面列写回。
- 当前 `web/src/views/data/factors` 是 Storage Metadata 的低层因子字典页；M3 新增 `web/src/views/factor/*` 作为生产 factor 管理面，不覆盖原页面。

## Target File Map

### Module Foundation

- Modify: `modules/factor/go.mod`
  Add direct dependencies: `github.com/glebarez/sqlite`, `github.com/nats-io/nats.go`, `gopkg.in/yaml.v3`, `gorm.io/gorm`, `trpc.group/trpc-go/trpc-go`, `trpc.group/trpc-go/trpc-filter/validation`, `trpc.group/trpc-go/trpc-log-cls`, `github.com/mooyang-code/moox/modules/storage/proto/gen`, `github.com/mooyang-code/moox/packages/commonpb`.
- Modify: `go.work`
  Add `./modules/factor/proto/factorgen` after proto generation.
- Modify: `scripts/build/build.sh`
  Build factor from `modules/factor ./cmd/server moox-factor`.
- Modify: `Makefile`
  Add `$(MAKE) -C modules/factor/proto all` in `proto`.
- Create: `modules/factor/config/app.yaml`
- Create: `modules/factor/config/trpc_go.yaml`
- Create: `modules/factor/internal/app/control/config.go`
- Create: `modules/factor/internal/store/database.go`
- Create: `modules/factor/internal/app/control/bootstrap.go`
- Create: `modules/factor/internal/app/control/discovery.go`
- Create: `modules/factor/cmd/server/main.go`
- Create: `modules/factor/cmd/cli/main.go`

### Local Persistence

- Create: `modules/factor/schema/factor.sql`
- Create: `modules/factor/schema/schema.go`
- Create: `modules/factor/schema/schema_test.go`
- Create: `modules/factor/internal/domain/factor.go`
- Create: `modules/factor/internal/domain/binding.go`
- Create: `modules/factor/internal/domain/run.go`
- Create: `modules/factor/internal/store/factor.go`
- Create: `modules/factor/internal/store/binding.go`
- Create: `modules/factor/internal/store/run.go`
- Create: `modules/factor/internal/store/page.go`

### Registry And Storage RPC

- Create: `modules/factor/internal/registry/service.go`
- Create: `modules/factor/internal/registry/source.go`
- Create: `modules/factor/internal/registry/metadata_sync.go`
- Create: `modules/factor/internal/storageio/client.go`
- Create: `modules/factor/internal/storageio/dataframe.go`
- Create: `modules/factor/internal/storageio/cache.go`
- Create: `modules/factor/internal/storageio/writeback.go`

### Engine And Worker

- Create: `modules/factor/internal/engine/types.go`
- Create: `modules/factor/internal/engine/frame.go`
- Create: `modules/factor/internal/engine/json_codec.go`
- Create: `modules/factor/internal/engine/stdio_executor.go`
- Create: `modules/factor/internal/engine/worker_pool.go`
- Create: `modules/factor/internal/engine/errors.go`
- Create: `modules/factor/pyworker/codec.py`
- Create: `modules/factor/pyworker/worker.py`
- Create: `modules/factor/pyworker/test_worker.py`
- Create: `modules/factor/pyworker/requirements.txt`
- Create: `modules/factor/factors/Bias.py`
- Create: `modules/factor/factors/Cci.py`
- Create: `modules/factor/sections/.gitkeep`

### Realtime Pipeline

- Create: `modules/factor/internal/trigger/nats.go`
- Create: `modules/factor/internal/trigger/debounce.go`
- Create: `modules/factor/internal/scheduler/task.go`
- Create: `modules/factor/internal/scheduler/queue.go`
- Create: `modules/factor/internal/scheduler/service.go`
- Create: `modules/factor/internal/scheduler/recalc.go`
- Create: `modules/factor/internal/scheduler/reconcile.go`

### RPC And Frontend

- Create: `modules/factor/proto/Makefile`
- Create: `modules/factor/proto/factor.proto`
- Create: `modules/factor/proto/factorgen/*` through `make proto`
- Create: `modules/factor/internal/rpc/service.go`
- Create: `modules/factor/internal/rpc/convert.go`
- Create: `modules/factor/internal/rpc/recalc.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Create: `web/src/api/factor/http.ts`
- Create: `web/src/api/factor/types.ts`
- Create: `web/src/api/factor/index.ts`
- Create: `web/src/views/factor/definitions/index.vue`
- Create: `web/src/views/factor/bindings/index.vue`
- Create: `web/src/views/factor/runs/index.vue`
- Create: `web/src/views/factor/instances/index.vue` in M6
- Modify: `web/src/router/route.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

---

### Task 1: Wire The Factor Module As A Real Service

**Files:**
- Modify: `modules/factor/go.mod`
- Create: `modules/factor/config/app.yaml`
- Create: `modules/factor/config/trpc_go.yaml`
- Create: `modules/factor/internal/app/control/config.go`
- Create: `modules/factor/internal/app/control/config_test.go`
- Create: `modules/factor/internal/store/database.go`
- Create: `modules/factor/cmd/server/main.go`
- Modify: `scripts/build/build.sh`

- [ ] **Step 1: Add config tests**

Create `modules/factor/internal/app/control/config_test.go` with tests that assert:

- `Default().Storage.MetadataTarget == "127.0.0.1:20100"`.
- `Default().Storage.AccessTarget == "127.0.0.1:20102"`.
- `Default().NATS.Stream == "MOOX_STORAGE"`.
- `Default().Engine.Workers > 0`.
- `MOOX_FACTOR_DB_PATH` overrides `database.path`.
- HTTP URLs are rejected for `storage.metadata_target` and `storage.access_target`.

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/factor/internal/app/control -run TestDefaultFactorConfig -count=1
```

Expected: FAIL because config loader does not exist.

- [ ] **Step 2: Implement config loader**

Implement `Config`, `DatabaseConfig`, `StorageConfig`, `NATSConfig`, `EngineConfig`, `SchedulerConfig`, `InstanceConfig`, and `SysDeployConfig` in `internal/app/control/config.go`. Use collector's `Load` pattern, but environment variables must use `MOOX_FACTOR_*`.

Required defaults:

| Field | Value |
| --- | --- |
| `database.path` | `./data/factor/factor.db` |
| `storage.metadata_target` | `127.0.0.1:20100` |
| `storage.access_target` | `127.0.0.1:20102` |
| `nats.url` | `nats://127.0.0.1:4222` |
| `nats.stream` | `MOOX_STORAGE` |
| `nats.consumer` | `factor_calc` |
| `nats.subject` | `moox.event.storage.time_series.rows_changed.v1` |
| `engine.python_bin` | `python3` |
| `engine.factors_dir` | `./factors` |
| `engine.sections_dir` | `./sections` |
| `engine.workers` | `8`（yaml 缺省 0 时取 `min(runtime.NumCPU(), 8)`） |
| `engine.task_timeout_ms` | `30000` |
| `engine.encoding` | `auto` |
| `engine.arrow_row_threshold` | `50000` |
| `engine.shm_dir` | `""`（空用系统临时目录，M4 Arrow 旁路用） |
| `scheduler.debounce_window_ms` | `2000` |
| `scheduler.max_retry` | `3` |
| `scheduler.reconcile_interval_min` | `10` |
| `scheduler.runs_retention_days` | `7`（`t_factor_runs` 清理 timer，1m 高频下约 72 万行/天必须清理） |
| `instance.instance_id` | `factor-01` |
| `instance.role` | `primary` |
| `instance.primary_target` | `""`（replica 必填，M6 前不用） |
| `instance.heartbeat_interval_ms` | `5000` |
| `sysdeploy.admin_gateway_url` | `http://127.0.0.1:11000` |

- [ ] **Step 3: Add app and tRPC config**

Create `modules/factor/config/app.yaml` matching the defaults above. Create `modules/factor/config/trpc_go.yaml` with HTTP service `trpc.moox.factor.FactorMgr` on port `11404`, plus an optional timer service `trpc.moox.factor.reconcile.timer`.

- [ ] **Step 4: Add SQLite manager**

Implement `internal/store/database.go` following collector 的 DSN 结构，但 **`synchronous` 用 `NORMAL` 而非 collector 现用的 `OFF`**（factor 是新模块，不继承已在代码审查中被标记为 C13 的坏做法；因子运行记录/定义不应在断电时丢失）：

```text
journal_mode(WAL), synchronous(NORMAL), busy_timeout(5000), temp_store(MEMORY), cache_size(-64000), wal_autocheckpoint(1000)
```

- [ ] **Step 5: Replace placeholder server entrypoint**

Create `cmd/server/main.go` with `trpc.NewServer()`, `control.Initialize(ctx, s)`, and `server.Serve()`, following `modules/collector/cmd/server/main.go`.

- [ ] **Step 6: Update build target**

Change `scripts/build/build.sh` **两处**（`all` 分支约 84 行、`factor` 分支约 118 行）from `./cmd/moox-factor` to `./cmd/server`；输出二进制名保持 `moox-factor`（`scripts/release/release.sh:46` 按二进制名拷贝，无需改动）。同步更新 `modules/factor/README.md` 中的 `go run ./cmd/moox-factor` 与目录树描述。

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/factor/internal/app/control -count=1
./scripts/build/build.sh factor
```

Expected: PASS; `bin/moox-factor` is produced.

---

### Task 2: Add SQLite Schema And Repositories

**Files:**
- Create: `modules/factor/schema/factor.sql`
- Create: `modules/factor/schema/schema.go`
- Create: `modules/factor/schema/schema_test.go`
- Create: `modules/factor/internal/domain/factor.go`
- Create: `modules/factor/internal/domain/binding.go`
- Create: `modules/factor/internal/domain/run.go`
- Create: `modules/factor/internal/store/factor.go`
- Create: `modules/factor/internal/store/binding.go`
- Create: `modules/factor/internal/store/run.go`
- Create: `modules/factor/internal/store/page.go`
- Create: `modules/factor/internal/store/*_test.go`

- [ ] **Step 1: Write schema test**

Assert `schema.AllSQL()` contains these exact objects:

- `CREATE TABLE IF NOT EXISTS t_factor_defs`
- `CREATE TABLE IF NOT EXISTS t_factor_bindings`
- `CREATE TABLE IF NOT EXISTS t_factor_runs`
- `idx_factor_bindings_unique`
- `idx_factor_bindings_source`
- `idx_factor_runs_scope_time`
- `update_factor_defs_mtime`

Also assert the schema does **not** contain `c_create_time` / `c_update_time`（时间列必须是全仓惯例的 `c_ctime`/`c_mtime`）。

Run:

```bash
go test ./modules/factor/schema -count=1
```

Expected: FAIL before schema files exist.

- [ ] **Step 2: Create schema**

Use this table set for V1:

命名遵循全仓惯例：时间列用 `c_ctime` / `c_mtime`（`DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP`，与 collector/storage/trade/cloudnode 全部现有 schema 一致，**不要用** `c_create_time`/`c_update_time`）；mtime 触发器带 `WHEN NEW.c_mtime = OLD.c_mtime` 守卫（同 `modules/storage/schema/metadata.sql`，允许业务代码显式写 mtime 且避免触发器覆盖）。

```sql
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_kind TEXT NOT NULL DEFAULT 'timeseries' CHECK (c_kind IN ('timeseries', 'cross_section')),
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_params_json TEXT NOT NULL DEFAULT '[]',
    c_lookback_bars INTEGER NOT NULL,          -- registry 导入时计算（max(params)*3，下限 200），无 DB 默认值：0 值窗口是 bug 不该被默认值掩盖
    c_writeback_bars INTEGER NOT NULL DEFAULT 5,
    c_depends_json TEXT NOT NULL DEFAULT '[]',
    c_status TEXT NOT NULL DEFAULT 'disabled' CHECK (c_status IN ('enabled', 'disabled')),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_factor_defs_kind_status
ON t_factor_defs(c_kind, c_status);

CREATE TABLE IF NOT EXISTS t_factor_bindings (
    c_binding_id TEXT PRIMARY KEY,
    c_factor_id TEXT NOT NULL REFERENCES t_factor_defs(c_factor_id),
    c_space_id TEXT NOT NULL,
    c_source_dataset TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_subject_mode TEXT NOT NULL DEFAULT 'all' CHECK (c_subject_mode IN ('all', 'include')),
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_target_dataset TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'enabled' CHECK (c_status IN ('enabled', 'disabled')),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- UpsertBinding 的自然键：同一因子对同一 (space, source_dataset, freq) 只允许一条绑定，
-- upsert 按此键 ON CONFLICT 更新，防止重复绑定导致任务因子清单重复计算
CREATE UNIQUE INDEX IF NOT EXISTS idx_factor_bindings_unique
ON t_factor_bindings(c_factor_id, c_space_id, c_source_dataset, c_freq);

CREATE INDEX IF NOT EXISTS idx_factor_bindings_source
ON t_factor_bindings(c_space_id, c_source_dataset, c_freq, c_status);

CREATE TABLE IF NOT EXISTS t_factor_runs (
    c_run_id TEXT PRIMARY KEY,
    c_trigger_type TEXT NOT NULL,              -- event / recalc / reconcile / manual(run-once)
    c_space_id TEXT NOT NULL,
    c_source_dataset TEXT NOT NULL,
    c_target_dataset TEXT NOT NULL DEFAULT '',
    c_subject_id TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_bar_time TEXT NOT NULL,
    c_factor_count INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL CHECK (c_status IN ('succeeded', 'failed', 'superseded')),
    c_error TEXT NOT NULL DEFAULT '',
    c_elapsed_ms INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_factor_runs_scope_time
ON t_factor_runs(c_space_id, c_source_dataset, c_subject_id, c_freq, c_bar_time DESC);

CREATE INDEX IF NOT EXISTS idx_factor_runs_status_time
ON t_factor_runs(c_status, c_ctime DESC);

CREATE TRIGGER IF NOT EXISTS update_factor_defs_mtime AFTER UPDATE ON t_factor_defs
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_defs SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

CREATE TRIGGER IF NOT EXISTS update_factor_bindings_mtime AFTER UPDATE ON t_factor_bindings
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_bindings SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;
```

设计说明（对齐 `docs/因子计算模块设计.md`）：

- `t_factor_runs` **只在任务终态插入一次**（succeeded/failed/superseded），没有 `running` 状态——运行中的任务在 scheduler 内存中，`GetEngineStatus` 暴露 current task IDs，不落库。避免 1m 高频下每任务两次写库。
- `t_factor_runs` 是纯 append 日志表，无 mtime 触发器；容量治理见 Task 10 的清理 timer（`runs_retention_days`）。
- 多实例的 `t_factor_instances` / `t_shard_plans` 在 M6（Task 18）加入，DDL 以设计文档「多实例分片管理」章节为准，时间列同样用 `c_ctime`/`c_mtime`。

- [ ] **Step 3: Add domain models**

Use GORM `TableName()` methods mapping to the exact `t_factor_*` table names. Store JSON fields as strings in domain models and decode them in registry/service layers, not in GORM hooks.

- [ ] **Step 4: Add repository tests**

Repository tests must cover:

- Upsert factor by `factor_id`, including source hash update.
- List enabled timeseries factors.
- Upsert binding and filter by `(space_id, source_dataset, freq)`.
- 对同一 `(factor_id, space_id, source_dataset, freq)` 重复 upsert 只产生一行（命中 `idx_factor_bindings_unique`，更新而非新增）。
- `subject_mode=include` JSON round trip.
- Insert run record and list by scope ordered by `c_bar_time DESC`.
- `DeleteRunsBefore(cutoff)` 删除早于截止时间的运行记录并返回删除行数（供清理 timer 使用）。

Run:

```bash
go test ./modules/factor/schema ./modules/factor/internal/store -count=1
```

Expected: PASS.

---

### Task 3: Implement Registry And Metadata Sync

**Files:**
- Create: `modules/factor/internal/registry/service.go`
- Create: `modules/factor/internal/registry/source.go`
- Create: `modules/factor/internal/registry/metadata_sync.go`
- Create: `modules/factor/internal/registry/service_test.go`

- [ ] **Step 1: Write registry tests**

Tests must verify:

- `ImportFactorFile("Bias.py")` produces `factor_id=bias`, `name=Bias`, SHA-256 source hash, `params_json=[20]` when no sidecar config exists.
- `DefaultLookback(params)` returns `max(params)*3`, with minimum `200`.
- `ResultDataset("binance_spot_kline")` returns `binance_spot_factor`.
- Metadata sync（输入为 `space + source dataset + 因子集合`，不绑定 binding 概念，供 M1 run-once 与 M2+ binding 链路共用）calls Storage Metadata in this order: create factor, create dataset if absent, upsert each factor result column.

- [ ] **Step 2: Implement source import**

Read `.py` files from `modules/factor/factors`, reject names outside `^[A-Za-z][A-Za-z0-9_]*\.py$`, compute `source_hash`, and persist source code to SQLite. During update, also write the DB source back to `factors/{Name}.py` before worker reload.

- [ ] **Step 3: Implement metadata sync interface**

Define a small interface around Storage Metadata RPC:

```go
type MetadataClient interface {
    CreateFactor(ctx context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error)
    CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
    UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
}
```

The sync implementation must use `storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE` and `storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR`.

- [ ] **Step 4: Make sync idempotent**

Treat Storage duplicate-create responses as success only when a follow-up get/list confirms the resource exists. Do not ignore arbitrary Storage errors.

Run:

```bash
go test ./modules/factor/internal/registry -count=1
```

Expected: PASS.

---

### Task 4: Implement Python Worker Frame Protocol

**Files:**
- Create: `modules/factor/pyworker/codec.py`
- Create: `modules/factor/pyworker/worker.py`
- Create: `modules/factor/pyworker/test_worker.py`
- Create: `modules/factor/pyworker/requirements.txt`
- Create: `modules/factor/factors/Bias.py`
- Create: `modules/factor/factors/Cci.py`

- [ ] **Step 1: Add Python tests**

`test_worker.py` must cover:

- Frame round trip with magic `MX`, type byte, big-endian `meta_len`, JSON meta, big-endian `payload_len`.
- Worker loads `Bias.py` and returns `ready` with loaded factor names.
- `signal_multi_params` is preferred over `signal`.
- `signal` path calls `df.copy()` for each param and only returns `{factor_name}` columns.
- Null JSON values become pandas `NaN`, while `candle_begin_time` becomes UTC datetime.

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
python3 -m pip install -r pyworker/requirements.txt
python3 -m pytest pyworker -q
```

Expected: FAIL before implementation.

- [ ] **Step 2: Add requirements**

`pyworker/requirements.txt`:

```text
pandas>=2.2,<3
numpy>=2,<3
pytest>=8,<9
pyarrow>=16
```

- [ ] **Step 3: Implement codec**

`codec.py` must expose `read_frame(stream)`, `write_frame(stream, frame_type, meta, payload=b"")`, `decode_json_df(meta)`, and `encode_json_results(task_id, results, tail, per_factor_ms, elapsed_ms)`.

- [ ] **Step 4: Implement worker**

`worker.py` accepts `--factors-dir`, `--sections-dir`, and `--encoding`. It writes a ready frame on startup, loops on request frames, imports modules with `importlib.util.spec_from_file_location`, and writes error frames containing `id`, `error_type`, and `message` when factor code raises.

- [ ] **Step 5: Add sample factors**

Add `Bias.py` with `signal_multi_params`; add `Cci.py` with `signal` only. These are test fixtures and useful for `run-once`.

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
python3 -m pytest pyworker -q
```

Expected: PASS.

---

### Task 5: Implement Go Engine Codec And Stdio Executor

**Files:**
- Create: `modules/factor/internal/engine/types.go`
- Create: `modules/factor/internal/engine/frame.go`
- Create: `modules/factor/internal/engine/json_codec.go`
- Create: `modules/factor/internal/engine/stdio_executor.go`
- Create: `modules/factor/internal/engine/worker_pool.go`
- Create: `modules/factor/internal/engine/errors.go`
- Create: `modules/factor/internal/engine/*_test.go`

- [ ] **Step 1: Write frame codec tests**

Test exact binary layout:

```text
4d 58 | type | meta_len_be | meta_json | payload_len_be | payload
```

Also test corrupt magic, truncated meta, truncated payload, and oversized frame rejection.

- [ ] **Step 2: Define shared task/result types**

`types.go` must contain:

- `DataFrame` with ordered `Columns []string`, `Rows [][]any`, and `DataTimes []time.Time`.
- `FactorTask` with `TaskID`, `Kind`, `SpaceID`, `SourceDataset`, `TargetDataset`, `SubjectID`, `Freq`, `BarTime`, `LookbackBars`, and `Factors []FactorSpec`.
- `FactorSpec` with `FactorID`, `Name`, `Params []int`, `WritebackBars`.
- `FactorResult` with `Columns map[string]FactorColumnResult`, `PerFactorMS map[string]int64`, `ElapsedMS int64`.

- [ ] **Step 3: Implement JSON codec**

`json_codec.go` converts `DataFrame` into the request meta expected by `worker.py`. Wire 格式必须是**列式**（`{"columns": {"open": [...], ...}, "index_ms": [...]}`），与设计文档的 JSON v1 编码一致，方便 Python 侧直接 `pd.DataFrame(columns_dict)` 构造且天然对齐 Arrow v2。`candle_begin_time` must be epoch milliseconds. Numeric Go values must remain numeric JSON; missing values must be `nil`.

- [ ] **Step 4: Implement stdio executor**

`stdio_executor.go` starts Python with `exec.CommandContext`, waits for ready frame, serializes one request at a time per worker process, enforces task timeout with process kill, and returns typed retryable/non-retryable errors.

- [ ] **Step 5: Implement worker pool**

`worker_pool.go` creates `workers` processes, dispatches tasks through an internal channel, restarts crashed workers with exponential backoff capped at 30s, and exposes `Status()` for M3/M4.

Run:

```bash
go test ./modules/factor/internal/engine -count=1
```

Expected: PASS.

---

### Task 6: Implement StorageIO Read, Cache, And Writeback

**Files:**
- Create: `modules/factor/internal/storageio/client.go`
- Create: `modules/factor/internal/storageio/dataframe.go`
- Create: `modules/factor/internal/storageio/cache.go`
- Create: `modules/factor/internal/storageio/writeback.go`
- Create: `modules/factor/internal/storageio/*_test.go`

- [ ] **Step 1: Write conversion tests**

Tests must assert Storage rows with columns `open`, `high`, `low`, `close`, `volume`, `quote_volume`, `trade_num` convert into a `DataFrame` ordered by `candle_begin_time ASC`.

- [ ] **Step 2: Implement Access client interface**

Wrap Storage Access RPC with:

- `ReadWindow(ctx, key, lookbackBars, endTime, columns) (*engine.DataFrame, error)`
- `WriteFactorPatch(ctx, task, result) error`

Use `storagepb.NewAccessClientProxy` with `client.WithTarget(...)` and no import from storage internal packages. Target 规范化复刻 collector 的 `normalizeStorageTarget`（`modules/collector/internal/sources/binance/storage_rpc.go:101-122`，裸 `host:port` 自动加 `ip://` 前缀）。每个请求都要构造 `AuthInfo`（同 collector 的 service-auth 方式）。注意仓库中目前 **没有** `ReadTimeSeriesRows` 的 Go 调用示例（collector 只写不读），Req 字段为 `keys + time_range + order + column_names + page`，时间戳在 `TimeSeriesKey.data_time`（RFC3339），不是普通列。

- [ ] **Step 3: Implement window cache**

Key cache by `(space_id, source_dataset, subject_id, freq)`. Capacity is `max_lookback_bars + writeback_bars + 10`. Cold start reads the full window; steady state reads only missing tail rows; late events older than cache tail invalidate and refill the affected range.

- [ ] **Step 4: Implement writeback rows**

For each result column, map returned tail values onto the last `tail` `DataTimes` of the input frame. Write one `WriteTimeSeriesRows` request per symbol containing multiple rows and all factor columns for that row. `nil` values are written as absent columns, not zero.

Run:

```bash
go test ./modules/factor/internal/storageio -count=1
```

Expected: PASS.

---

### Task 7: Add CLI Init, Import, And Run-Once

**Files:**
- Create: `modules/factor/cmd/cli/main.go`
- Create: `modules/factor/cmd/cli/init_schema.go`
- Create: `modules/factor/cmd/cli/import.go`
- Create: `modules/factor/cmd/cli/run_once.go`
- Create: `modules/factor/cmd/cli/*_test.go`
- Modify: `scripts/build/build.sh`

- [ ] **Step 1: Add CLI command tests**

Tests must cover argument parsing for:

- `init --db ./tmp/factor.db`
- `import --db ./tmp/factor.db --factors-dir ./factors --default-params 20,96,288`
- `run-once --space crypto --dataset binance_spot_kline --subject BTC-USDT --freq 1m --bar-time 2026-07-06T09:15:00Z [--factors bias,cci]`

- [ ] **Step 2: Implement `init`**

Open SQLite with factor manager, execute `schema.AllSQL()`, and print JSON:

```json
{"ok":true,"database":"./tmp/factor.db","tables":["t_factor_defs","t_factor_bindings","t_factor_runs"]}
```

- [ ] **Step 3: Implement `import`**

Scan `factors-dir`, import valid `.py` files through registry, and print imported factor IDs and hashes.

- [ ] **Step 4: Implement `run-once`**

M1 阶段 **不依赖 binding**（binding 的创建入口要到 M3）。解析目标 = 选定 `space/dataset/freq/subject` + 全部 enabled 因子定义（可用 `--factors id1,id2` 覆盖为子集）；target dataset 由 `registry.ResultDataset(source)` 推导。流程：同步 metadata（CreateFactor + CreateDataset + UpsertDatasetColumn）→ 读 K 线窗口 → 执行 engine → 写因子 patch → 插入一条 `t_factor_runs`。

`--factors` 缺省时用全部 enabled 定义；`run-once` 内部构造的是一个 per-symbol `FactorTask`，与 M2 scheduler 产出的任务结构完全一致（复用同一执行管线）。

Run:

```bash
go test ./modules/factor/cmd/cli -count=1
./scripts/build/build.sh factor
```

Expected: PASS.

---

### Task 8: Build M1 End-To-End Verification

**Files:**
- Create: `modules/factor/examples/run-once/README.md`
- Modify: `modules/factor/README.md`

- [ ] **Step 1: Document local prerequisites**

State that Storage must be running with metadata/access tRPC ports `20100/20102`, and that factor writes require result Dataset and columns to be registered by registry sync.

- [ ] **Step 2: Add a scripted manual verification**

Document exact commands:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go run ./cmd/cli init --db ./data/factor/factor.db
go run ./cmd/cli import --db ./data/factor/factor.db --factors-dir ./factors --default-params 20,96
go run ./cmd/cli run-once --space crypto --dataset binance_spot_kline --subject BTC-USDT --freq 1m --bar-time 2026-07-06T09:15:00Z
```

Acceptance:

- `t_factor_runs` contains one `succeeded` row.
- Storage `binance_spot_factor` has `Bias_20` and `Bias_96` values for the requested tail bars.
- Storage View can join K line rows with the factor result Dataset after View columns are added.

---

### Task 9: Implement NATS Trigger And Debounce

**Files:**
- Create: `modules/factor/internal/trigger/nats.go`
- Create: `modules/factor/internal/trigger/debounce.go`
- Create: `modules/factor/internal/trigger/*_test.go`

- [ ] **Step 1: Write debounce tests**

Tests must assert:

- Events for non-bound datasets are dropped.
- Events for factor result datasets are dropped by source-dataset whitelist.
- Multiple keys with same `(space, dataset, subject, freq)` inside 2s produce one task at max `data_time`.
- Include-mode bindings only allow configured subjects.

- [ ] **Step 2: Implement durable consumer**

Use `nats.Connect`, `JetStream`, `AddConsumer` or `UpdateConsumer` with durable name from config, filter subject from config, `AckWait=60s`, `MaxDeliver=5`, `DeliverPolicy=DeliverNewPolicy`, and manual ack. Ack immediately after protojson parse and enqueue into debounce, matching the design's "event is only a trigger signal" rule.

- [ ] **Step 3: Implement binding snapshot**

Trigger reads enabled binding snapshot from registry/repository and refreshes on ticker every 30s plus explicit reload channel from RPC changes.

Run:

```bash
go test ./modules/factor/internal/trigger -count=1
```

Expected: PASS.

---

### Task 10: Implement Scheduler, Supersede, Retry, And Run Records

**Files:**
- Create: `modules/factor/internal/scheduler/task.go`
- Create: `modules/factor/internal/scheduler/queue.go`
- Create: `modules/factor/internal/scheduler/service.go`
- Create: `modules/factor/internal/scheduler/recalc.go`
- Create: `modules/factor/internal/scheduler/*_test.go`

- [ ] **Step 1: Write scheduler tests**

Tests must assert:

- Same subject hashes to same shard.
- Same `(space, dataset, subject, freq)` pending task is replaced by newer `bar_time`.
- Superseded task is recorded as `superseded`.
- Retryable storage/worker crash errors retry at most 3 times.
- Non-retryable factor code errors insert failed run and do not block the next task.

- [ ] **Step 2: Implement queue**

Use one FIFO per worker shard and a pending map keyed by `(space, source_dataset, subject_id, freq)`. Replacement keeps the newer `bar_time` and merged factor list. Worker shard goroutine owns execution order for that subject.

- [ ] **Step 3: Implement execution pipeline**

For each task:

1. Resolve factor list and max lookback/writeback.
2. Call `storageio.ReadWindow`.
3. Call `engine.Execute`.
4. Call `storageio.WriteFactorPatch`.
5. Insert `t_factor_runs` with elapsed time and status.

- [ ] **Step 4: Implement recalc task stream**

`RecalcFactor` produces low-priority tasks sliced by `(subject, freq, time window)`. Recalc tasks share the same queue but only run when realtime queue for that shard is empty.

- [ ] **Step 5: Implement runs retention cleanup**

后台 timer（每小时）调用 `repository.DeleteRunsBefore(now - runs_retention_days)`，删除行数记入日志。1m × 500 symbol 每天产生约 72 万条 run 记录，缺少清理会让 SQLite 无限膨胀（设计文档「本地 SQLite schema」注释明确要求近 N 天保留）。

Run:

```bash
go test ./modules/factor/internal/scheduler -count=1
```

Expected: PASS.

---

### Task 11: Bootstrap Realtime Service

**Files:**
- Create: `modules/factor/internal/app/control/bootstrap.go`
- Modify: `modules/factor/cmd/server/main.go`
- Create: `modules/factor/internal/app/control/bootstrap_test.go`

- [ ] **Step 1: Write bootstrap tests**

With fake dependencies, assert `Initialize`:

- Loads config.
- Initializes DB.
- Creates registry, storageio, engine pool, scheduler, trigger.
- Registers `trpc.moox.factor.FactorMgr` only when the service exists in `trpc_go.yaml`.
- Skips trigger startup when `nats.url` is empty in test config.

- [ ] **Step 2: Implement lifecycle**

`Initialize` must return the server and install shutdown hooks for trigger, scheduler, and engine pool. Startup order is DB → registry → metadata sync snapshot → engine → scheduler → trigger → RPC registration.

Run:

```bash
go test ./modules/factor/internal/app/control -count=1
go test ./modules/factor/... -count=1
```

Expected: PASS.

---

### Task 12: Add FactorMgr Proto And RPC

**Files:**
- Create: `modules/factor/proto/Makefile`
- Create: `modules/factor/proto/factor.proto`
- Create: `modules/factor/proto/factorgen/*` via generation
- Modify: `go.work`
- Modify: `Makefile`
- Create: `modules/factor/internal/rpc/service.go`
- Create: `modules/factor/internal/rpc/convert.go`
- Create: `modules/factor/internal/rpc/recalc.go`
- Create: `modules/factor/internal/rpc/*_test.go`

- [ ] **Step 1: Define V1 proto**

V1 must include these RPCs:

- `CreateFactor`
- `UpdateFactor`
- `GetFactor`
- `ListFactors`
- `SetFactorStatus`
- `UpsertBinding`
- `ListBindings`
- `DeleteBinding`
- `RecalcFactor`
- `GetRecalcProgress`
- `ListFactorRuns`
- `GetEngineStatus`

Every response first field is `common.RetInfo ret_info = 1`, matching collector/trade conventions.

- [ ] **Step 2: Generate proto**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/proto
make all
```

`proto/Makefile` 复刻 `modules/collector/proto/Makefile`（`trpc-open create --rpconly --nogomod --mock=false`，`--protodir ../../../packages/commonpb`），输出目录改为 `./factorgen`。因 `--nogomod` 不生成 go.mod，需**手写** `factorgen/go.mod`（module `github.com/mooyang-code/moox/modules/factor/proto/factorgen`，依赖对齐 `modules/storage/proto/gen/go.mod`）。然后 add `./modules/factor/proto/factorgen` to `go.work` and add the proto target to the root `Makefile`.

- [ ] **Step 3: Implement RPC service**

RPC write operations call registry/repository, then request scheduler/trigger reload. `RecalcFactor` returns a stable `recalc_id` immediately and leaves progress observable through `GetRecalcProgress`.

- [ ] **Step 4: Add RPC tests**

Use fake registry/scheduler/engine status providers. Tests must verify validation failures return `INVALID_PARAM`, not Go errors, and successful writes return `SUCCESS`.

Run:

```bash
make -C modules/factor/proto all
go test ./modules/factor/internal/rpc -count=1
go test ./modules/factor/... -count=1
```

Expected: PASS.

---

### Task 13: Register Admin Gateway Deployment

**Files:**
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Modify: `modules/factor/config/trpc_go.yaml`

- [ ] **Step 1: Add default deployment**

Add a deployment row:

| Field | Value |
| --- | --- |
| `service_name` | `moox_factor` |
| `service_kind` | `factor` |
| `protocol` | `http` |
| `host` | `127.0.0.1` |
| `port` | `11404` |
| `gateway_path` | `trpc.moox.factor.FactorMgr` |
| `scope` | `internal` |
| `description` | `因子计算服务，承载因子定义、绑定、补算与运行记录` |

`deployment(...)` 签名为 `(name, kind, protocol, host string, port int32, gatewayPath, scope, description string)`（`defaults.go:41`）。网关按 URL 段 `/api/admin/{service}/{method}` 以 `{service}` 作为 deployment 的 `service_name` 查表，别名在 `gatewayDeploymentName`（`sysdeploy/service.go:168-179`）。按设计文档约定 serviceID `factormgr` → 部署名 `moox_factor`（与 `moox_collector`/`moox_cloudnode` 命名一致），需要在 `gatewayDeploymentName` 中新增一行 `case "factor", "factormgr": return "moox_factor"`。11403 未被占用，11404 与设计文档一致，无端口冲突。

- [ ] **Step 2: Verify gateway path**

Call path after server startup:

```bash
curl -sS http://127.0.0.1:11000/api/admin/factormgr/GetEngineStatus -d '{}'
```

Expected: response contains `ret_info.code` success when admin gateway and factor service are running.

Run:

```bash
go test ./modules/admin/internal/service/sysdeploy ./modules/factor/... -count=1
```

Expected: PASS.

---

### Task 14: Add Web Management Pages

**Files:**
- Create: `web/src/api/factor/http.ts`
- Create: `web/src/api/factor/types.ts`
- Create: `web/src/api/factor/index.ts`
- Create: `web/src/views/factor/definitions/index.vue`
- Create: `web/src/views/factor/bindings/index.vue`
- Create: `web/src/views/factor/runs/index.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: Add API wrapper**

Use `/api/admin/factormgr/{Method}` and the same response handling style as storage/trade APIs. Types must mirror `factor.proto`.

- [ ] **Step 2: Add definition page**

Page capabilities:

- List factor defs with status, kind, params, lookback, writeback, source hash.
- Create/update source code through textarea upload.
- Enable/disable factor.
- Import hint that `.py` files can also be imported by CLI.

- [ ] **Step 3: Add binding page**

Page capabilities:

- Select source dataset, freq, target dataset.
- Bind factor to all subjects or include list.
- Enable/disable/delete binding.
- Trigger metadata sync after binding save.

- [ ] **Step 4: Add runs page**

Page capabilities:

- Filter by space, dataset, subject, freq, status, time range.
- Show run status, trigger type, bar time, factor count, elapsed, error.
- Trigger recalc with factor selector, subject selector and time range.

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
npm run type-check
npm run build
```

Expected: PASS.

---

### Task 15: M2 Load Test And Acceptance

**Files:**
- Create: `modules/factor/internal/testkit/events.go`
- Create: `modules/factor/internal/testkit/storage_fake.go`
- Create: `modules/factor/internal/testkit/worker_fake.go`
- Create: `modules/factor/docs/realtime-verification.md`

- [ ] **Step 1: Add event storm test**

Generate 500 symbols × one bar event and assert debounce emits 500 tasks, not more.

- [ ] **Step 2: Add scheduler load test**

Use fake storage and fake engine with deterministic 5ms task latency. Assert queue drains within a configured bound and supersede counter increments under artificial backlog.

- [ ] **Step 3: Document live verification**

Acceptance for M2:

- Storage `eventbus.type` is `nats`.
- Collector writes 1m K lines.
- Factor trigger receives events from `MOOX_STORAGE`.
- Factor result dataset receives tail writes.
- No repeated self-trigger loop occurs after factor writes.

Run:

```bash
go test ./modules/factor/internal/trigger ./modules/factor/internal/scheduler ./modules/factor/internal/storageio -count=1
go test ./modules/factor/... -run TestEventStorm -count=1
make check-boundaries
```

Expected: PASS.

---

### Task 16: Add Observability, Reconcile, Hot Reload, And Arrow

**Files:**
- Modify: `modules/factor/internal/engine/*`
- Modify: `modules/factor/internal/scheduler/reconcile.go`
- Create: `modules/factor/internal/metrics/status.go`
- Create: `modules/factor/internal/engine/arrow_codec.go`
- Modify: `modules/factor/pyworker/codec.py`
- Modify: `modules/factor/pyworker/worker.py`
- Modify: `modules/factor/internal/rpc/service.go`

- [ ] **Step 1: Add engine status model**

Expose queue depth, worker state, worker restart count, current task IDs, supersede count, writeback failures, NATS consumer lag, and per-factor elapsed top list.

- [ ] **Step 2: Implement reconcile timer**

Every configured interval, sample recent bars for enabled bindings. Compare source K line rows and factor result rows; enqueue recalc tasks for missing factor rows.

- [ ] **Step 3: Implement hot reload**

On factor source hash change, worker pool drains each worker, restarts it, waits for ready frame, verifies loaded factor hashes, and returns the worker to service.

- [ ] **Step 4: Implement Arrow IPC**

Go uses `github.com/apache/arrow-go/v18` only in `engine/arrow_codec.go`. Python worker uses pyarrow. Encoding selection:

- `json` when row count is at or below threshold.
- `arrow` when row count exceeds threshold.
- Fallback to `json` if worker ready frame does not include `arrow`.

Run:

```bash
go test ./modules/factor/internal/engine ./modules/factor/internal/scheduler ./modules/factor/internal/rpc -count=1
cd modules/factor && python3 -m pytest pyworker -q
```

Expected: PASS.

---

### Task 17: Implement M5 Cross-Section Factors

**Files:**
- Modify: `modules/factor/schema/factor.sql`
- Modify: `modules/factor/internal/registry/*`
- Create: `modules/factor/internal/crosssection/deps.go`
- Create: `modules/factor/internal/crosssection/watermark.go`
- Create: `modules/factor/internal/crosssection/table_builder.go`
- Create: `modules/factor/internal/crosssection/scheduler.go`
- Modify: `modules/factor/pyworker/worker.py`
- Create: `modules/factor/sections/RankPct.py`

- [ ] **Step 1: Extend dependency model**

Parse `get_factor_list(n)` during import for `sections/*.py`. Store dependencies in `c_depends_json`, validate all dependent timeseries factors are enabled for the same dataset/freq before enabling a cross-section factor.

- [ ] **Step 2: Implement watermark trigger**

Subscribe to factor result Dataset rows-changed events, track ready subjects by `(target_dataset, freq, bar_time)`, trigger when ready ratio reaches config or force delay expires.

- [ ] **Step 3: Build long-format DataFrame**

Read dependency columns from factor result Dataset and subject attributes from the configured subject attribute Dataset. Build columns `candle_begin_time`, `symbol`, dependency factor columns, and declared subject attribute columns.

- [ ] **Step 4: Execute and write back**

Use `meta.kind="cross_section"`, route to sections modules, and write result rows to independent `{source_dataset去掉_kline}_section` Dataset.

Run:

```bash
go test ./modules/factor/internal/crosssection ./modules/factor/internal/registry ./modules/factor/internal/engine -count=1
cd modules/factor && python3 -m pytest pyworker -q
```

Expected: PASS.

---

### Task 18: Implement M6 Multi-Instance Sharding

**Files:**
- Modify: `modules/factor/schema/factor.sql`
- Create: `modules/factor/internal/cluster/instance.go`
- Create: `modules/factor/internal/cluster/plan.go`
- Create: `modules/factor/internal/cluster/assignment.go`
- Create: `modules/factor/internal/cluster/heartbeat.go`
- Modify: `modules/factor/proto/factor.proto`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/trigger/debounce.go`
- Create: `web/src/views/factor/instances/index.vue`
- Create: `web/src/views/factor/shards/index.vue`

- [ ] **Step 1: Add cluster tables**

Add `t_factor_instances` and `t_shard_plans` exactly as described in the design, with indexes on status, role, and plan version.

- [ ] **Step 2: Add RPC methods**

Extend `FactorMgr` with:

- `ListInstances`
- `GetShardPlan`
- `SaveShardPlan`
- `ApplyShardPlan`
- `TakeoverShard`
- `ListShardPlanHistory`
- `InstanceHeartbeat`
- `PullAssignment`

- [ ] **Step 3: Implement primary/replica protocol**

Replica registers then heartbeats to primary. Heartbeat response includes latest `plan_version` and `defs_hash`. Replica calls `PullAssignment` when either value differs.

- [ ] **Step 4: Apply shard filter**

Trigger filters by assignment after binding filtering. Hash mode uses `hash(subject_id) % total == shard_index`; explicit mode uses symbol membership.

- [ ] **Step 5: Add web pages**

Instance page shows instance state, metrics, heartbeat age, plan version and defs hash. Shard page supports hash and explicit draft editing, apply, convergence progress and one-click takeover.

Run:

```bash
go test ./modules/factor/internal/cluster ./modules/factor/internal/trigger ./modules/factor/internal/rpc -count=1
cd web && npm run type-check && npm run build
```

Expected: PASS.

---

## Deployment Checklist

- [ ] Storage `modules/storage/config/storage.yaml` or deployment config sets `storage.eventbus.type: nats`.
- [ ] NATS is reachable from Storage and Factor at `nats://127.0.0.1:4222` or configured URL.
- [ ] `MOOX_STORAGE` stream exists with subject `moox.event.storage.>`.
- [ ] SysDeploy has active `moox_factor` deployment pointing to `127.0.0.1:11404`, path `trpc.moox.factor.FactorMgr`（serviceID 别名 `factormgr`）。若开发库已有旧 `t_service_deployments` 数据，需重建库或手动补插该行（默认部署仅在初始化时种子写入）。
- [ ] `moox-factor` process runs with working directory `modules/factor` or absolute paths for config, factors, sections and db.
- [ ] Python environment has `pandas`, `numpy`; `pyarrow` is installed before enabling Arrow or cross-section tasks.
- [ ] Result Dataset columns are synced before first writeback.
- [ ] View columns are added after result columns when users need wide-table query.

## Verification Matrix

| Milestone | Command | Acceptance |
| --- | --- | --- |
| M1 | `go test ./modules/factor/... -count=1` plus `python3 -m pytest pyworker -q` | run-once can compute `Bias` and write `binance_spot_factor` |
| M2 | event storm and scheduler tests | 500 symbol event storm creates 500 tasks and drains within one bar period |
| M3 | `curl /api/admin/factormgr/GetEngineStatus` | admin gateway reaches factor service and RPC returns success |
| M4 | Arrow codec tests | large window task negotiates Arrow, small window remains JSON |
| M5 | `RankPct` test | cross-section factor writes section Dataset without triggering time-series self loop |
| M6 | two local factor instances | shard plan change converges and affected symbols are reconciled |

## Open Questions

1. ~~`cmd/server` rename 影响面~~ **已核实**：仓库内引用 `cmd/moox-factor` 的只有 `scripts/build/build.sh`（两处）和 `modules/factor/README.md`；`scripts/release/release.sh` 按二进制名 `moox-factor` 拷贝，不受影响。Task 1 Step 6 已覆盖。
2. Storage `CreateDataset` duplicate semantics should be checked during M1; if it is not idempotent, registry must perform read-before-create.
3. M5 chooses independent section Dataset to avoid event ambiguity. If product strongly wants one result Dataset for both time-series and cross-section columns, watermark must get column-level change metadata from Storage first.
