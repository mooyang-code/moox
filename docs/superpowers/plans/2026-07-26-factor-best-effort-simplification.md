# Factor Best-Effort Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `modules/factor` 收敛为适合个人量化的单机时序因子服务：实时链路采用明确的 best-effort 语义，失败后通过同步范围补算修复，并删除未形成完整能力的高可靠、截面、Arrow 和多实例设计。

**Architecture:** Factor 只持久化因子定义和绑定；Storage 新行事件进入内存固定窗口，形成按标的分片的有界实时任务队列。实时事件进入内存后立即 ACK，进程退出、队列满或重试耗尽都允许丢失；系统通过日志、计数器和同步 `run-once`/`RecalcFactor` 范围补算修复这些损失。实时单 bar 与手动时间范围使用同一个 range runtime；每个执行 chunk 读取目标数据及 `lookback_bars - 1` 条历史上下文、调用一次 Python worker、写回一次目标范围结果，不引入持久化调度器、Inbox、DLQ 或 exactly-once。

**Tech Stack:** Go 1.25, tRPC-Go, GORM + SQLite, NATS JetStream, `packages/events`, `packages/jetstream`, `packages/pyruntime`, Python 3 + pandas/numpy, Vue 3 + TypeScript + Arco Design.

---

## 1. 已确认的产品边界

本计划以个人量化、单实例、可接受少量实时漏算为前提。以下决策是硬约束：

1. Factor 是 best-effort realtime，不承诺进程退出后的任务恢复。
2. JetStream delivery 在事件成功解码并放入内存 batcher 后 ACK。
3. 内存窗口、待执行任务和正在执行任务都不写 SQLite。
4. 队列满时拒绝新 scope，增加 `queue_overflow_count` 并记录结构化日志，不阻塞 NATS 消费。
5. 同一 `(space, source, target, subject, freq)` 的待执行任务只保留最新 bar。
6. Storage 读取、Python 执行或写回失败只进行本进程内有限重试；重试耗尽后记录失败，不进入 DLQ。
7. 手动补算一次只处理一个 subject 的左闭右开时间范围 `[start_time, end_time)`；服务端按目标 bar 分块同步执行，不逐 bar 调用 Python。
8. Factor V1 只支持时序因子，不保留 `cross_section` 类型、目录或协议字段。
9. 每个 range chunk 只发起一次 Python 请求，chunk 内所有因子顺序执行；范围补算可由多个有序 chunk 组成。
10. JSON columnar 是唯一 Go/Python 数据传输编码；本轮删除 Arrow snapshot。
11. 因子结果允许 `null`，写回时跳过对应值；非数值、NaN 和 Infinity 不允许穿过 JSON 边界。
12. Metadata 只在因子或绑定变更时同步，绝不放在实时计算热路径。
13. 新项目不保留旧 Proto 字段、旧配置项、旧表、旧命令或兼容包装。
14. 服务端二进制继续使用 `moox-factor`，现有启动命令和部署入口保持不变。
15. CLI 继续作为独立二进制 `moox-factor-cli`，不与服务端合并。
16. `FactorDef.Depends` 保持现有名称；它只表达标准 K 线字段之外的额外输入列，不表示因子 DAG。
17. realtime 只写事件对应的单个 bar；手动补算写请求范围内的全部目标 bar。`lookback_bars` 保持现有名称，表示第一个目标 bar 所需的最小完整输入窗口，因此每个执行 chunk 最多额外读取 `lookback_bars - 1` 条前置历史，不表示写回行数。
18. `FactorResult` 只承载目标结果列；`PerFactorMS` 和 `ElapsedMS` 不进入结果契约。总任务耗时由 scheduler 或 CLI 在最外层统一测量并写日志。

明确不做：

- 不增加持久化 scheduler、outbox、inbox、任务租约或分布式锁。
- 不增加通用 DLQ、重放 Consumer、Schema Registry、Saga 或 exactly-once。
- 不增加 Factor 主从、多实例分片、心跳选主或接管。
- 不实现截面因子、依赖 DAG 或跨 subject 聚合。
- 不实现自动扫描缺口并补算。
- 不保留 FactorRun 历史表和异步补算进度表。
- 不为未经测量的性能问题保留 Arrow、共享 snapshot 或任务内因子并行。
- 不把 `moox-factor` 改成带 `serve` 子命令的统一二进制。
- 不把 `Depends` 重命名为 `extra_columns` 或引入因子依赖编排。
- 不为范围补算增加持久化进度、事务回滚或可恢复 chunk 状态；后续 chunk 失败时保留此前已写入结果，用户可幂等重跑同一范围。

## 2. 目标运行语义

### 2.1 实时路径

```text
Storage DatasetRowsUpserted
  -> Factor JetStream durable Consumer
  -> validate envelope and payload
  -> EventBatcher.Add
  -> ACK
  -> fixed 2s per-scope window
  -> reload executable bindings
  -> Scheduler.Enqueue
  -> bounded per-subject shard queue
  -> build one-bar range [bar_time, bar_time + 1ns)
  -> Storage.ReadRangeChunk
  -> PythonExecutor.Execute (JSON, all factors)
  -> validate target-range columns (null allowed)
  -> Storage.WriteFactorPatch
  -> log and metrics
```

### 2.2 失败矩阵

| 边界 | 行为 | 是否自动恢复 |
| --- | --- | --- |
| EventMessage 无法解码或身份不一致 | `TERM`，记录拒绝原因 | 否 |
| Factor batcher 未装配 | `RETRY`，避免服务启动异常时直接 ACK | Consumer 会重投 |
| 事件已进入内存 batcher | `ACK` | 进程退出后允许丢失 |
| 没有可执行 Binding | `ACK`，不生成任务 | 不需要 |
| scheduler 队列已满 | 新 scope 未能入队，`queue_overflow_count++` | 手动补算 |
| 待执行 scope 收到更新 bar | 原位置替换为最新任务，不增加异常计数 | 不需要 |
| Storage 读取失败 | 本进程内最多重试 `max_retry` 次 | 有限 |
| Python worker 失败 | worker pool 自身恢复；任务最多重试 `max_retry` 次 | 有限 |
| 结果校验失败 | 立即失败，不重试 | 修正因子后手动补算 |
| Storage 写回失败 | 本进程内最多重试 `max_retry` 次 | 有限 |
| 进程退出 | 内存任务全部丢弃 | 手动补算 |

### 2.3 手动补算路径

```text
RecalcFactor or moox-factor-cli run-once
  -> validate exact space/source/subject/freq/[start_time,end_time)
  -> load enabled executable bindings
  -> build the same range task shape
  -> synchronously call Scheduler.Run
  -> split target rows into at most 2000 bars per chunk
  -> for each chunk: read lookback context, execute once, validate, write once
  -> return terminal success/failure
```

补算不进入实时有界队列，不写进度表、不生成 run ID、不后台执行。调用超时或任一 chunk 失败即返回失败；此前已经完成的 chunk 不回滚，重复调用同一范围通过 Storage field upsert 幂等覆盖。
`RecalcFactor` 适合调用方可控制 timeout 的中小范围；较长的离线计算优先使用 `moox-factor-cli run-once`，避免 RPC gateway timeout，但二者必须复用相同 builder、chunk runner 和写回语义。

## 3. 目标数据契约

### 3.1 FactorDef

`factor.proto` 的最终定义应为：

```proto
message FactorDef {
  string factor_id = 1;
  string name = 2;
  string source_code = 3;
  string source_hash = 4;
  repeated int32 periods = 5;
  int32 lookback_bars = 6;
  repeated string depends = 7;
  string status = 8;
  string created_at = 9;
  string updated_at = 10;
}
```

约束：

- `factor_id` 和 Python `name` 必填。
- `periods` 必须非空、全部大于零、去重并升序保存。
- `lookback_bars >= max(periods)`。
- `status` 只能为 `enabled` 或 `disabled`。
- `depends` 由源码分析生成，API 输入值不作为权威来源。

`depends` 的业务含义固定为额外输入列集合。例如 `["funding_rate", "open_interest"]` 表示任务读取标准 K 线列之外，还要从 Storage 请求这两列。字段名保持 `Depends`/`depends`，不扩展为因子间依赖关系。

`lookback_bars` 保持现有名称，含义固定为“计算第一个目标 bar 所需的最小完整输入窗口”，其中包含该目标 bar 本身。执行 `[start_time, end_time)` 时，在第一条目标数据之前最多读取 `lookback_bars - 1` 条历史记录；目标范围和写回行数始终由运行时请求决定，不放入 `FactorDef`。

### 3.2 RecalcFactor

```proto
message RecalcFactorReq {
  string factor_id = 1;
  string space_id = 2;
  string source_dataset = 3;
  string subject_id = 4;
  string freq = 5;
  string start_time = 6;
  string end_time = 7;
}

message RecalcFactorRsp {
  common.RetInfo ret_info = 1;
}
```

`start_time` 是包含下界，`end_time` 是不包含上界，二者都必须是 RFC3339/RFC3339Nano 且 `start_time < end_time`，语义与 Storage `TimeRange` 完全一致。`factor_id` 为空时计算该 scope 下所有 enabled factors；非空时只计算指定且 enabled 的因子。

该 RPC 是同步终态接口：`ret_info = SUCCESS` 表示范围内全部 chunk 均完成计算和写回，其他错误码表示参数校验、读取、计算、结果校验、写回或 context 失败。task ID、因子数、chunk 数和总耗时只进入结构化日志；CLI 可继续输出这些面向人的诊断信息。

### 3.3 EngineStatus

```proto
message GetEngineStatusRsp {
  common.RetInfo ret_info = 1;
  int32 queue_depth = 2;
  int64 queue_overflow_count = 3;
}
```

- `queue_depth` 是当前待执行 scope 数。
- `queue_overflow_count` 是本进程启动以来，因为待执行 scope 已达到 `queue_capacity` 而未能入队的任务数。
- 每次 `Enqueue` 因容量不足返回 `ErrQueueFull` 时，`queue_overflow_count` 恰好增加一次；supersede、参数错误、执行失败、写回失败和进程退出都不计入。
- `queue_capacity` 继续作为 scheduler 静态配置和内存边界，但不通过状态响应重复暴露。
- worker、数据库、scheduler 和 EventBus readiness 统一由 `/readyz` 返回，`GetEngineStatus` 不复制健康检查字段。
- 删除伪造的逐 worker `ready` 列表，以及无明确操作价值的 supersede、writeback failure 累计计数。

### 3.4 SQLite

Factor SQLite 只保留：

```text
t_factor_defs
t_factor_bindings
```

`t_factor_defs` 使用 `c_periods_json` 和 `c_depends_json` 存储数组，删除：

```text
c_kind
c_params_json
c_avg_runtime_ms
c_writeback_bars
```

删除整表：

```text
t_factor_event_inbox
t_factor_event_processed
t_factor_replay_tasks
t_factor_runs
```

这是新项目，直接更新建库 schema，不编写兼容迁移或双读逻辑。

## 4. 目标文件结构

### 保留并收敛

```text
modules/factor/internal/bootstrap/
  bootstrap.go              仅装配实时 Consumer、batcher、scheduler、RPC 和 health
  config.go                 只保留实际使用的配置

modules/factor/internal/trigger/
  event_batcher.go          纯内存固定窗口
  event_batcher_test.go
  eventconsumer/            JetStream Consumer 和 best-effort ACK

modules/factor/internal/scheduler/
  task.go                   可执行任务和完成结果
  builder.go                realtime、RPC、CLI 共用的任务构造器
  builder_test.go
  queue.go                  subject hash 和 queue key
  service.go                有界队列、supersede、执行和有限重试
  service_test.go

modules/factor/internal/engine/
  types.go                  精简任务和结果类型
  json_codec.go             唯一 JSON columnar 编码
  executor.go               唯一 Python 执行器
  executor_test.go
  errors.go

modules/factor/internal/storageio/
  client.go
  dataframe.go
  writeback.go

modules/factor/internal/rpc/
  service.go
  recalc.go                 同步范围补算
  convert.go
```

### 删除

```text
modules/factor/internal/trigger/pending.go
modules/factor/internal/trigger/event_batcher_inbox.go
modules/factor/internal/trigger/event_batcher_inbox_test.go
modules/factor/internal/trigger/replay.go
modules/factor/internal/trigger/replay_test.go
modules/factor/internal/store/event_inbox.go
modules/factor/internal/store/event_inbox_test.go
modules/factor/internal/store/replay.go
modules/factor/internal/store/replay_test.go
modules/factor/internal/scheduler/batch.go
modules/factor/internal/scheduler/batch_test.go
modules/factor/internal/scheduler/recalc.go
modules/factor/internal/storageio/cache.go
modules/factor/internal/storageio/snapshot.go
modules/factor/internal/storageio/snapshot_test.go
modules/factor/internal/engine/stdio_executor.go
modules/factor/internal/engine/worker_pool.go
modules/factor/cmd/cli/replay.go
modules/factor/sections/.gitkeep
```

如果实施基线出现新的无调用 Factor-local executor、snapshot 或 replay 文件，同一清理提交中删除，不能增加兼容包装。

---

### Task 0: 固化实施基线和失败证明

**Files:**
- Add: `docs/superpowers/plans/2026-07-26-factor-best-effort-simplification.md`

- [ ] **Step 1: 记录实施基线**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git fetch origin
git rev-parse HEAD
git ls-remote origin refs/heads/feature/mooyang
git status --short
```

Expected: 记录实际 local/remote SHA；已有用户改动保持不变，不混入 Factor 实施提交。

- [ ] **Step 2: 运行当前基线**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
PYTHONPATH=../../packages/pyruntime/python python3 -m pytest -q pyworker
```

Expected: Go 测试、race 和 vet PASS；Python 环境缺少 pytest 时先按 `pyworker/requirements.txt` 安装到隔离 venv，再重跑。基线失败必须先归因，不能把无关修复混入本计划。

- [ ] **Step 3: 固化当前四个缺陷的失败测试名**

实施前确认下列测试在对应 Task 加入后先失败：

```text
TestDisabledFactorIsExcludedFromRealtimeTask
TestBestEffortHandlerACKsAfterMemoryAdd
TestSchedulerRejectsNewScopeWhenQueueIsFull
TestValidateFactorResultAllowsNullElements
```

- [ ] **Step 4: 提交计划基线**

```bash
git add docs/superpowers/plans/2026-07-26-factor-best-effort-simplification.md
git commit -m "docs(factor): plan best-effort simplification"
```

Expected: 仅提交本计划文档。

---

### Task 1: 收敛 Factor Proto、领域模型和 SQLite Schema

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Regenerate: `modules/factor/proto/factorgen/factor.pb.go`
- Regenerate: `modules/factor/proto/factorgen/factor.trpc.go`
- Modify: `modules/factor/schema/factor.sql`
- Modify: `modules/factor/schema/schema_test.go`
- Modify: `modules/factor/internal/domain/factor.go`
- Create: `modules/factor/internal/domain/validation.go`
- Create: `modules/factor/internal/domain/validation_test.go`
- Modify: `modules/factor/internal/store/factor.go`
- Modify: `modules/factor/internal/store/binding.go`
- Modify: `modules/factor/internal/store/binding_test.go`
- Modify: `modules/factor/internal/registry/source.go`
- Create: `modules/factor/internal/registry/source_test.go`
- Modify: `modules/factor/internal/registry/service.go`
- Modify: `modules/factor/internal/registry/service_test.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/rpc/convert.go`
- Modify: `modules/factor/internal/rpc/convert_test.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/rpc/recalc.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/cmd/cli/import.go`
- Modify: `modules/factor/cmd/cli/main.go`
- Modify: `modules/factor/cmd/cli/main_test.go`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`
- Modify: `modules/factor/test/e2e_test.go`

- [ ] **Step 1: 先写新 schema 契约测试**

在 `schema_test.go` 增加：

```go
func TestFactorSchemaContainsOnlyDefinitionAndBindingState(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_factor_defs",
		"CREATE TABLE IF NOT EXISTS t_factor_bindings",
		"c_periods_json TEXT NOT NULL",
		"c_depends_json TEXT NOT NULL",
	} {
		require.Contains(t, sql, want)
	}
	for _, removed := range []string{
		"c_kind",
		"c_params_json",
		"c_avg_runtime_ms",
		"c_writeback_bars",
		"t_factor_event_inbox",
		"t_factor_event_processed",
		"t_factor_replay_tasks",
		"t_factor_runs",
	} {
		require.NotContains(t, sql, removed)
	}
}
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./schema -run TestFactorSchemaContainsOnlyDefinitionAndBindingState -count=1
```

Expected: FAIL because旧字段和事件表仍存在。

- [ ] **Step 2: 改写 FactorDef 协议并删除无效 RPC**

按“3. 目标数据契约”改写 `FactorDef`、`RecalcFactorReq/Rsp` 和 `GetEngineStatusRsp`。

同时删除：

```proto
message FactorRun
message WorkerStatus
message GetRecalcProgressReq
message GetRecalcProgressRsp
message ListFactorRunsReq
message ListFactorRunsRsp
rpc GetRecalcProgress(...)
rpc ListFactorRuns(...)
```

`ListFactorsReq` 只保留 `status` 和 `page`，不再接受 `kind`。

生成代码：

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor/proto
make clean all
```

Expected: `factor.pb.go` 和 `factor.trpc.go` 不再包含 CrossSection、FactorRun、WorkerStatus、WritebackBars、GetRecalcProgress 或 ListFactorRuns；`RecalcFactorRsp` 只生成 `RetInfo`。

- [ ] **Step 3: 改写领域模型**

`internal/domain/factor.go` 的核心结构应为：

```go
type FactorDef struct {
	FactorID      string    `gorm:"column:c_factor_id;primaryKey"`
	Name          string    `gorm:"column:c_name"`
	SourceCode    string    `gorm:"column:c_source_code"`
	SourceHash    string    `gorm:"column:c_source_hash"`
	SourcePath    string    `gorm:"column:c_source_path"`
	Periods       []int     `gorm:"column:c_periods_json;serializer:json"`
	LookbackBars  int       `gorm:"column:c_lookback_bars"`
	Depends       []string  `gorm:"column:c_depends_json;serializer:json"`
	Status        string    `gorm:"column:c_status"`
	CreateTime    time.Time `gorm:"column:c_ctime"`
	ModifyTime    time.Time `gorm:"column:c_mtime"`
}
```

只保留：

```go
const (
	FactorStatusEnabled  = "enabled"
	FactorStatusDisabled = "disabled"
)
```

删除 kind、generic params JSON、writeback bars、平均运行时间和 cross-section 常量。

- [ ] **Step 4: 增加定义归一化测试**

在 `internal/domain/validation_test.go` 加入：

```go
func TestNormalizeFactorDefinitionSortsAndValidatesPeriods(t *testing.T) {
	got, err := NormalizeFactorDefinition(domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "def signal(): pass",
		Periods: []int{20, 5, 20}, LookbackBars: 30,
		Status: domain.FactorStatusEnabled,
	})
	require.NoError(t, err)
	require.Equal(t, []int{5, 20}, got.Periods)
}

func TestNormalizeFactorDefinitionRejectsInvalidWindow(t *testing.T) {
	_, err := NormalizeFactorDefinition(domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x",
		Periods: []int{20}, LookbackBars: 10,
	})
	require.Error(t, err)
}
```

在 `internal/domain/validation.go` 定义并实现：

```go
func NormalizeFactorDefinition(factor FactorDef) (FactorDef, error)
```

RPC Create/Update 和 CLI import 必须在写 repository 前调用该函数；repository 不再静默填入无效默认值。

将 registry 配置从 `DefaultParams []int` 改为 `DefaultPeriods []int`，默认值仍为 `[20]`。

Expected validation:

```text
period <= 0                    -> error
periods empty                  -> error
lookback < max(periods)        -> error
invalid factor status          -> error
```

- [ ] **Step 5: 更新源码依赖解析**

将：

```go
func DependsJSONFromSource(source string) string
```

替换为：

```go
func DependsFromSource(source string) []string
```

返回去重、升序的列名数组。删除 `DependsInfo`、`ExtraColumnsFromSource`、`ExtraColumnsFromFactors` 和 `extraColumnsFromDepends`；任务 builder 直接把 `FactorDef.Depends` 复制为 `FactorSpec.Depends`，不再引入 `extra_columns` 中间名称或重复 JSON 解析。

`metadata_sync.go` 遍历 `factor.Periods` 创建 Storage 列；只有调用 Storage Metadata 的 `CreateFactorReq.ParamsJson` 时才通过 `json.Marshal(factor.Periods)` 适配 Storage 公共协议。删除 Factor 模块自己的 `factorParams`、`paramsFromJSON`、`recalcParams` 和 `mustParseParams`。

- [ ] **Step 6: 更新 schema 和 repositories**

重写 `t_factor_defs`，只保留目标字段；`t_factor_bindings` 保持现有自然键和外键。

Schema 必须符合根 `AGENTS.md` 的 SQLite 格式。执行：

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
sqlite3 :memory: < modules/factor/schema/factor.sql
(cd modules/factor && go test ./... -count=1)
git diff --check
```

Expected: PASS；新空库只有 definition/binding 两张 Factor 业务表。

- [ ] **Step 7: 提交协议和持久化收敛**

```bash
git add modules/factor/proto modules/factor/schema \
  modules/factor/internal/domain modules/factor/internal/store \
  modules/factor/internal/registry modules/factor/internal/rpc \
  modules/factor/internal/bootstrap modules/factor/cmd/cli \
  modules/factor/test
git commit -m "refactor(factor): narrow definition and persistence contracts"
```

---

### Task 2: 删除 Durable Inbox 并定义 Best-Effort ACK

**Files:**
- Modify: `modules/factor/internal/trigger/event_batcher.go`
- Modify: `modules/factor/internal/trigger/event_batcher_test.go`
- Delete: `modules/factor/internal/trigger/pending.go`
- Delete: `modules/factor/internal/trigger/event_batcher_inbox.go`
- Delete: `modules/factor/internal/trigger/event_batcher_inbox_test.go`
- Delete: `modules/factor/internal/store/event_inbox.go`
- Delete: `modules/factor/internal/store/event_inbox_test.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/handler.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/consumer_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`

- [ ] **Step 1: 写 best-effort handler 失败测试**

将 pending store fake 从 `consumer_test.go` 删除，新增直接内存断言：

```go
func TestBestEffortHandlerACKsAfterMemoryAdd(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 30, 0, 0, time.UTC)
	batcher := trigger.NewEventBatcher(time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})
	handler := storageEventHandler{eventBatcher: batcher}
	delivery := encodedDelivery(t, event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now))

	got := handler.Handle(context.Background(), delivery)
	require.Equal(t, jetstream.ACK, got.Decision)
	require.Len(t, batcher.Flush(now.Add(2*time.Second)), 1)
}
```

同一测试文件增加完整 helper：

```go
func encodedDelivery(t *testing.T, payload *storagepb.DatasetRowsUpserted) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.DatasetRowsUpserted, payload, events.PublishOptions{
		EventID:    "factor-best-effort-1",
		OccurredAt: time.Now().UTC(),
		SpaceID:    payload.GetSpaceId(),
		SubjectID:  payload.GetDatasetId(),
	})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		Subject:       encoded.Subject,
		RawData:       raw,
		RawMessageID: "factor-best-effort-1",
		ContentType:  events.ContentType,
	}
}
```

另加：

```go
func TestBestEffortHandlerRetriesWhenBatcherUnavailable(t *testing.T)
func TestBestEffortHandlerACKsUnmatchedDataset(t *testing.T)
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/trigger/eventconsumer -run 'TestBestEffortHandler' -count=1
```

Expected: FAIL because handler 仍要求 `IngestMessage` 和 durable store，且 EventBatcher 尚未提供 `Add`。

- [ ] **Step 2: 将 EventBatcher 改为纯内存**

`EventBatcher` 最终只保留：

```go
type EventBatcher struct {
	mu       sync.Mutex
	window   time.Duration
	bindings []domain.FactorBinding
	buckets  map[bucketKey]*bucket
}

func NewEventBatcher(window time.Duration, bindings []domain.FactorBinding) *EventBatcher
func (d *EventBatcher) SetBindings(bindings []domain.FactorBinding)
func (d *EventBatcher) Add(event *storagepb.DatasetRowsUpserted, now time.Time)
func (d *EventBatcher) Flush(now time.Time) []Task
```

`Task` 删除：

```text
PendingEventIDs
FactorVersion
TargetRunID
```

保持完整 bucket key 和固定首事件 deadline：

```text
(space_id, source_dataset, target_dataset, subject_id, freq)
```

- [ ] **Step 3: 简化 handler**

删除只被调用一次的 `storageEventHandler.ingest`。`Handle` 完成解码和身份校验后直接执行：

```go
if h.eventBatcher == nil {
	return jetstream.HandlerResult{
		Decision: jetstream.RETRY,
		Delay:    time.Second,
		Err:      errors.New("factor event batcher is unavailable"),
	}
}
h.eventBatcher.Add(payload, time.Now().UTC())
return jetstream.HandlerResult{Decision: jetstream.ACK}
```

解码失败继续 `TERM`；不要依据 message ID 去重，也不要为一次调用保留 `ingest` 或 `acceptEvent` 包装方法。

- [ ] **Step 4: 简化 bootstrap**

启动时使用：

```go
eventBatcher := trigger.NewEventBatcher(
	time.Duration(cfg.Scheduler.EventBatchWindowMS)*time.Millisecond,
	bindings,
)
```

删除：

```text
NewDurableEventBatcher
eventBatcher.Replay
FlushPending
CommitPending
RestorePending
```

`drainEventBatch` 只执行：

```go
for _, pending := range deps.eventBatcher.Flush(time.Now()) {
	task, ok, err := buildSchedulerTask(ctx, deps.factors, deps.factorsDir, pending)
	if err != nil {
		log.WarnContextf(ctx, "build realtime factor task failed: %v", err)
		continue
	}
	if !ok {
		continue
	}
	if err := deps.scheduler.Enqueue(ctx, task); err != nil {
		log.WarnContextf(ctx, "drop realtime factor task: %v", err)
	}
}
```

一个任务失败不能恢复或阻塞本批其他任务。

- [ ] **Step 5: 删除 inbox 实现和测试**

删除 Target File Map 中的 pending/inbox 文件，并确认：

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
! rg -n 'PendingEvent|DurableEventBatcher|FlushPending|CommitPending|RestorePending|event_inbox|event_processed' modules/factor
```

Expected: 命令退出 0，Factor 不再含 durable inbox 概念。

- [ ] **Step 6: 运行 trigger 证明集并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/trigger ./internal/trigger/eventconsumer ./internal/bootstrap -count=1
go test -race ./internal/trigger ./internal/trigger/eventconsumer -count=1
```

Expected: PASS。

```bash
git add -A modules/factor/internal/trigger modules/factor/internal/store \
  modules/factor/internal/bootstrap
git commit -m "refactor(factor): use best-effort realtime batching"
```

---

### Task 3: 统一 Executable Binding 和 Metadata 边界

**Files:**
- Modify: `modules/factor/internal/store/binding.go`
- Modify: `modules/factor/internal/store/binding_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/registry/metadata_sync_test.go`

- [ ] **Step 1: 写 disabled factor 回归测试**

在 binding repository 测试加入：

```go
func TestListExecutableExcludesDisabledFactorsAndBindings(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedFactor(t, db, "enabled-factor")
	seedFactor(t, db, "disabled-factor")
	require.NoError(t, NewFactorRepository(db).SetStatus(
		ctx, "disabled-factor", domain.FactorStatusDisabled,
	))
	repo := NewBindingRepository(db)
	require.NoError(t, repo.Upsert(ctx, domain.FactorBinding{
		BindingID: "enabled-binding", FactorID: "enabled-factor",
		SpaceID: "crypto", SourceDataset: "bars", Freq: "1m",
		SubjectMode: domain.SubjectModeAll, SubjectsJSON: "[]",
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
	require.NoError(t, repo.Upsert(ctx, domain.FactorBinding{
		BindingID: "disabled-factor-binding", FactorID: "disabled-factor",
		SpaceID: "crypto", SourceDataset: "bars", Freq: "1m",
		SubjectMode: domain.SubjectModeAll, SubjectsJSON: "[]",
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))

	got, err := repo.ListExecutable(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "enabled-factor", got[0].FactorID)
}
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/store -run TestListExecutableExcludesDisabledFactorsAndBindings -count=1
```

Expected: FAIL because repository 只过滤 Binding 状态。

- [ ] **Step 2: 增加唯一 executable query**

实现：

```go
func (r *BindingRepository) ListExecutable(ctx context.Context) ([]domain.FactorBinding, error) {
	var rows []domain.FactorBinding
	err := r.db.WithContext(ctx).
		Table("t_factor_bindings AS b").
		Select("b.*").
		Joins("JOIN t_factor_defs AS f ON f.c_factor_id = b.c_factor_id").
		Where("b.c_status = ? AND f.c_status = ?",
			domain.BindingStatusEnabled,
			domain.FactorStatusEnabled,
		).
		Order("b.c_space_id, b.c_source_dataset, b.c_freq, b.c_factor_id").
		Scan(&rows).Error
	return rows, err
}
```

实时快照和补算默认选择都使用该方法；删除语义重叠的 `ListEnabled`。

- [ ] **Step 3: 每个 flush tick 刷新 executable snapshot**

`startRealtimeLoop` 的单一 ticker 每次执行：

```go
bindings, err := deps.bindings.ListExecutable(ctx)
if err != nil {
	log.WarnContextf(ctx, "refresh executable factor bindings failed: %v", err)
	continue
}
deps.eventBatcher.SetBindings(bindings)
drainEventBatch(ctx, deps)
```

删除独立 30 秒 `bindingReloadTicker`。启停最长在一个 batch window 内生效。

- [ ] **Step 4: task builder 再次过滤 Factor status**

增加测试：

```go
func TestDisabledFactorIsExcludedFromRealtimeTask(t *testing.T)
func TestRealtimeTaskBecomesNoopWhenAllFactorsDisabled(t *testing.T)
```

`buildSchedulerTask` 返回 `(scheduler.Task, bool, error)`；加载到 disabled factor 时跳过，全部跳过时返回 `ok=false`，不能让一个已禁用因子恢复整个 batch。

- [ ] **Step 5: 从实时热路径删除 metadata sync**

删除：

```text
syncTaskMetadata
drainEventBatch 中的 MetadataSync 调用
realtimeLoopDeps.meta
bootstrap 中无使用结果的 registry.NewService
```

保留并测试：

```text
CreateFactor/UpdateFactor -> sync enabled bindings
SetFactorStatus(enabled)  -> sync enabled bindings
UpsertBinding(enabled)    -> sync this binding
```

禁用操作不调用 Metadata，但必须通过 `ListExecutable` 立即停止后续任务。

- [ ] **Step 6: 验证并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/store ./internal/registry ./internal/rpc ./internal/bootstrap -count=1
go test -race ./internal/bootstrap ./internal/rpc -count=1
```

Expected: PASS；禁用 Factor 后不再生成 realtime task。

```bash
git add modules/factor/internal/store modules/factor/internal/registry \
  modules/factor/internal/rpc modules/factor/internal/bootstrap
git commit -m "fix(factor): derive realtime work from executable bindings"
```

---

### Task 4: 将 Scheduler 收敛为有界的单层并发

**Files:**
- Modify: `modules/factor/internal/scheduler/task.go`
- Modify: `modules/factor/internal/scheduler/queue.go`
- Modify: `modules/factor/internal/scheduler/queue_test.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`
- Delete: `modules/factor/internal/scheduler/batch.go`
- Delete: `modules/factor/internal/scheduler/batch_test.go`
- Delete: `modules/factor/internal/scheduler/recalc.go`
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/config/app.yaml`

- [ ] **Step 1: 写有界队列和原位 supersede 失败测试**

加入：

```go
func oneBarTaskAt(at time.Time) Task {
	return Task{FactorTask: engine.FactorTask{
		TaskID: "task-" + at.UTC().Format(time.RFC3339Nano),
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC-USDT", Freq: "1m",
		StartTime: at, EndTime: at.Add(time.Nanosecond),
	}}
}

func TestSchedulerRejectsNewScopeWhenQueueIsFull(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, &fakeStorage{}, &fakeExecutor{})
	first := oneBarTaskAt(time.Unix(1, 0))
	first.SubjectID = "BTC-USDT"
	require.NoError(t, svc.Enqueue(context.Background(), first))
	second := oneBarTaskAt(time.Unix(1, 0))
	second.SubjectID = "ETH-USDT"
	err := svc.Enqueue(context.Background(), second)
	require.ErrorIs(t, err, ErrQueueFull)
	require.EqualValues(t, 1, svc.Status().QueueOverflowCount)
}

func TestSchedulerSupersedeDoesNotGrowQueue(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, &fakeStorage{}, &fakeExecutor{})
	first := oneBarTaskAt(time.Unix(1, 0))
	first.SubjectID = "BTC-USDT"
	second := oneBarTaskAt(time.Unix(2, 0))
	second.SubjectID = "BTC-USDT"
	require.NoError(t, svc.Enqueue(context.Background(), first))
	require.NoError(t, svc.Enqueue(context.Background(), second))
	require.Equal(t, 1, svc.Status().QueueDepth)
}
```

Expected: FAIL because当前 queue 无容量限制，supersede 会追加 stale item。

- [ ] **Step 2: 定义目标 scheduler API**

```go
var ErrQueueFull = errors.New("factor scheduler queue is full")

type Config struct {
	Workers       int
	QueueCapacity int
	MaxRetry      int
}

type Status struct {
	QueueDepth         int
	QueueOverflowCount int64
}

func (s *Service) Enqueue(ctx context.Context, task Task) error
```

删除 `EnqueueChecked` 名称和 durable 注释。

- [ ] **Step 3: 用 key queue 避免 stale items**

内部状态改为：

```go
queues  [][]taskKey
pending map[taskKey]Task
```

入队规则：

```text
existing key + newer/equal bar -> 只更新 pending[key]，不追加 queues
existing key + older bar       -> 忽略，不增加异常计数
new key + capacity available   -> pending[key]=task，queues[shard]=append(key)
new key + capacity exhausted   -> queue_overflow_count++，返回 ErrQueueFull
```

`QueueDepth` 必须等于 `len(pending)`，不统计 stale item。
realtime task 使用 `[bar_time, bar_time + 1ns)`，因此新旧判断比较 `task.EndTime`；手动范围补算不进入该队列，也不参与 supersede。
删除 `supersedeCount` 和 `writebackFailures` 原子计数；supersede 是正常合并行为，最终执行或写回失败通过结构化任务日志记录。

- [ ] **Step 4: 删除任务内因子分批**

每个 `executeChunk` 只调用一次：

```go
result, err := s.exec.Execute(ctx, &task.FactorTask, frame)
if err != nil {
	return err
}
if err := validateFactorResult(task.Factors, len(targetTimes), result); err != nil {
	return engine.NonRetryableError{Err: err}
}
```

删除：

```text
Partition
FactorBatch
executeFactorBatches
BatchMinEstimatedMS
EstimatedMS
```

- [ ] **Step 5: 清理 scheduler 配置**

目标配置：

```yaml
scheduler:
  event_batch_window_ms: 2000
  queue_capacity: 2048
  max_retry: 1
```

`queue_capacity <= 0` 默认 2048；`max_retry < 0` 报错，允许显式设置 0。

- [ ] **Step 6: 运行负载和 race 测试**

保留 120 subject synthetic storm，增加 2000 scope 容量测试：

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/scheduler ./internal/bootstrap -count=1
go test -race ./internal/scheduler -count=1
```

Expected:

```text
same subject -> same shard
same scope supersede -> depth remains 1
different target -> separate task
queue full -> deterministic ErrQueueFull
queue full -> queue_overflow_count increments exactly once
one realtime task -> one range-chunk read, one executor call, one write
```

- [ ] **Step 7: 提交 scheduler 收敛**

```bash
git add -A modules/factor/internal/scheduler \
  modules/factor/internal/bootstrap modules/factor/config/app.yaml
git commit -m "refactor(factor): bound scheduler and remove factor batching"
```

---

### Task 5: 统一 Python Executor、范围结果和空值契约

**Files:**
- Modify: `modules/factor/internal/engine/types.go`
- Modify: `modules/factor/internal/engine/json_codec.go`
- Modify: `modules/factor/internal/engine/json_codec_test.go`
- Move: `modules/factor/internal/engine/runtime_pool_executor.go` -> `modules/factor/internal/engine/executor.go`
- Create: `modules/factor/internal/engine/executor_test.go`
- Modify: `modules/factor/internal/engine/errors_test.go`
- Delete: `modules/factor/internal/engine/stdio_executor.go`
- Delete: `modules/factor/internal/engine/worker_pool.go`
- Delete: `modules/factor/internal/storageio/cache.go`
- Delete: `modules/factor/internal/storageio/snapshot.go`
- Delete: `modules/factor/internal/storageio/snapshot_test.go`
- Modify: `modules/factor/internal/storageio/client.go`
- Modify: `modules/factor/internal/storageio/dataframe.go`
- Modify: `modules/factor/internal/storageio/writeback.go`
- Modify: `modules/factor/internal/storageio/client_test.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/pyworker/codec.py`
- Modify: `modules/factor/pyworker/worker.py`
- Modify: `modules/factor/pyworker/test_worker.py`
- Modify: `modules/factor/pyworker/requirements.txt`
- Modify: `modules/factor/test/e2e_test.go`
- Delete: `modules/factor/sections/.gitkeep`

- [ ] **Step 1: 写范围结果和 null 契约失败测试**

Go：

```go
func TestValidateFactorResultAllowsNullElements(t *testing.T) {
	specs := []engine.FactorSpec{{Name: "Cci", Periods: []int{20}}}
	result := &engine.FactorResult{Columns: map[string][]any{
		"Cci_20": []any{nil, 1.25},
	}}
	require.NoError(t, validateFactorResult(specs, 2, result))
}

func TestValidateFactorResultRejectsNonNumericAndNonFinite(t *testing.T) {
	for _, value := range []any{"bad", math.NaN(), math.Inf(1)} {
		err := validateFactorResult(
			[]engine.FactorSpec{{Name: "Cci", Periods: []int{20}}},
			1,
			&engine.FactorResult{Columns: map[string][]any{
				"Cci_20": []any{value},
			}},
		)
		require.Error(t, err)
	}
}

func TestValidateFactorResultRejectsWrongTargetLength(t *testing.T) {
	err := validateFactorResult(
		[]engine.FactorSpec{{Name: "Cci", Periods: []int{20}}},
		2,
		&engine.FactorResult{Columns: map[string][]any{
			"Cci_20": []any{1.0},
		}},
	)
	require.Error(t, err)
}
```

Python：

```python
def test_json_value_normalizes_nan_and_infinity():
    assert _json_value(float("nan")) is None
    assert _json_value(float("inf")) is None
    assert _json_value(float("-inf")) is None

def test_execute_returns_values_only_for_target_range():
    response = worker.execute_request(
        request_meta(
            periods=[2],
            target_start_time="2026-07-26T00:02:00Z",
            target_end_time="2026-07-26T00:04:00Z",
        )
    )
    assert len(response["results"]["Bias_2"]) == 2
    assert "result_tails" not in response
    assert "per_factor_ms" not in response
    assert "elapsed_ms" not in response
```

同时增加：

```text
storageio/client_test.go: TestReadRangeChunkPrependsLookbackAndReturnsTargetTimes
storageio/client_test.go: TestWriteFactorPatchWritesTargetRangeAndSkipsNullCells
scheduler/service_test.go: TestRunRejectsChunkWithoutTargetRows
```

读取测试固定目标范围内 2 条数据和范围前 3 条历史，设置 `lookback_bars=4`，断言返回顺序为“3 条历史 + 2 条目标”，并且 `TargetTimes` 只包含后 2 条。写回测试提供两列两行结果，其中一个单元格为 `nil`，断言只跳过该单元格，不丢弃同一行的其他有效列。scheduler 测试返回空 `TargetTimes`，断言 executor 和 writeback 都不会被调用。

Expected: Go 范围长度、逐单元格 null 写回和 Python 范围数组测试 FAIL。

- [ ] **Step 2: 精简 Engine 类型**

`FactorTask` 删除：

```text
FactorVersion
TargetRunID
SnapshotID
SnapshotHash
SnapshotPath
Kind
```

`FactorSpec` 改为：

```go
type FactorSpec struct {
	FactorID   string
	Name       string
	SourceHash string
	SourcePath string
	Periods    []int
	Depends    []string
}

type FactorResult struct {
	Columns map[string][]any
}
```

`FactorTask` 使用：

```go
StartTime    time.Time // inclusive
EndTime      time.Time // exclusive
LookbackBars int
```

删除 `BarTime`。`StartTime` 和 `EndTime` 必须非零且 `StartTime.Before(EndTime)`；realtime 将事件时间转换为 `[bar_time, bar_time + 1ns)`。

JSON meta 使用 `"periods"`、`"target_start_time"` 和 `"target_end_time"`，不再发送 `"writeback_bars"` 或 `"extra_columns"`；Python worker 同步读取这些字段。scheduler 读取 Storage 时使用“标准 K 线列与所有 `FactorSpec.Depends` 的去重并集”。

`FactorResult` 只承载业务结果。删除 Go/Python 协议中的 `PerFactorMS`、`ElapsedMS`、`per_factor_ms` 和 `elapsed_ms`；scheduler/CLI 在最外层用 `time.Since(started)` 测量包含读取、计算、校验和写回的完整任务耗时。

- [ ] **Step 3: 重命名唯一执行器并删除 Arrow/snapshot**

将 concrete executor 从 `RuntimePoolExecutor` 重命名为 `PythonExecutor`，文件改为 `executor.go`：

```go
type Executor interface {
	Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error)
	Close() error
}

type PythonExecutor struct {
	workers int
	pool    *pool.Pool
	hello   protocol.Hello
}

type ExecutorStatus struct {
	Workers        int
	Ready          bool
	WorkerVersion  string
	PythonVersion  string
	RuntimeEnvHash string
}

func NewPythonExecutor(ctx context.Context, workers int, cfg process.Config) (*PythonExecutor, error)
func (e *PythonExecutor) Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error)
func (e *PythonExecutor) Status() ExecutorStatus
func (e *PythonExecutor) Close() error
```

将 `Executor` 接口从 `types.go` 移到 `executor.go`，继续供 scheduler 和测试 fake 使用。`PythonExecutor` 内部保留 `pyruntime/pool`，但公共命名不暴露 pool 实现细节。所有 `runtimeExec` 局部变量同步改为 `pythonExec`，原有 pool executor 测试移入 `executor_test.go`。

`PythonExecutor`：

- 删除 `arrow bool`。
- 删除独立 `python -c import pyarrow` 探测。
- 永远使用 `protocol.EncodingJSON`。
- `EncodeJSONRequestMeta` 永远包含 columns 和 `index_ms`。

Python worker：

- 删除 `sections` module map。
- 删除 `cross_section` 分支。
- 删除 `arrow_mmap` decode 分支。
- `load_one` 永远加载到 factors map。
- 对完整输入 frame 计算因子，但每个结果列只返回 `[target_start_time, target_end_time)` 对应的数组。
- 删除 tail、`result_tails`、`per_factor_ms` 和 `elapsed_ms`；结果数组顺序必须与目标 `DataTimes` 升序一致。

核心筛选和响应形状固定为：

```python
target_start = pd.Timestamp(meta["target_start_time"])
target_end = pd.Timestamp(meta["target_end_time"])
target_mask = (
    (df["candle_begin_time"] >= target_start)
    & (df["candle_begin_time"] < target_end)
)

values = out_df.loc[target_mask, column].tolist()
results[column] = [_json_value(value) for value in values]

return {
    "id": meta.get("id", ""),
    "ok": True,
    "encoding": "json",
    "results": results,
}
```

`pyworker/requirements.txt` 只保留：

```text
pandas>=2.2,<3
numpy>=2,<3
pytest>=8,<9
```

- [ ] **Step 4: 增加范围读取、归一化缺失值并固定范围写回**

`codec.py`：

```python
def _json_value(value):
    if pd.isna(value):
        return None
    if hasattr(value, "item"):
        value = value.item()
    if isinstance(value, float) and not np.isfinite(value):
        return None
    return value
```

Go validator：

```go
func validFactorValue(value any) bool {
	if value == nil {
		return true
	}
	switch n := value.(type) {
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0)
	case float32:
		return !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}
```

Go codec 将 Python 结果解码为 `FactorResult.Columns map[string][]any`；删除 `FactorColumnResult`、`Tail` 和 tail-to-frame-row 映射。

`storageio` 增加一个明确的逻辑读取契约：

```go
type RangeChunk struct {
	Frame       *engine.DataFrame
	TargetTimes []time.Time
}

func (c *Client) ReadRangeChunk(
	ctx context.Context,
	key WindowKey,
	startTime time.Time,
	endTime time.Time,
	lookbackBars int,
	targetLimit int,
	columns []string,
) (*RangeChunk, error)
```

同步更新 scheduler 的依赖接口，删除 `ReadWindow`：

```go
type StorageIO interface {
	ReadRangeChunk(
		ctx context.Context,
		key storageio.WindowKey,
		startTime time.Time,
		endTime time.Time,
		lookbackBars int,
		targetLimit int,
		columns []string,
	) (*storageio.RangeChunk, error)
	WriteFactorPatch(
		ctx context.Context,
		task *engine.FactorTask,
		targetTimes []time.Time,
		result *engine.FactorResult,
	) error
}
```

实现顺序：

1. 使用 Storage `TimeRange[startTime,endTime)`、升序和 `targetLimit` 读取目标行。
2. 没有目标行时返回空 `TargetTimes`，不调用 Python。
3. 使用第一个目标时间作为右开上界，倒序读取最多 `lookback_bars - 1` 条历史行。
4. 将历史行反转为升序，与目标行拼成 `Frame`；`TargetTimes` 只保存目标行时间。

每个 chunk 调用 executor 后，`validateFactorResult` 必须确认结果列集合准确、每列长度等于 `len(TargetTimes)`，并逐单元格验证 `nil` 或有限数值。

`WriteFactorPatch` 改为：

```go
func (c *Client) WriteFactorPatch(
	ctx context.Context,
	task *engine.FactorTask,
	targetTimes []time.Time,
	result *engine.FactorResult,
) error
```

它按 `TargetTimes` 与各结果列的相同下标创建 `RowFieldUpsert`；`nil` 只跳过对应单元格，整行没有有效值时不提交空 upsert。移除 `factor.snapshot_hash` attribute，只保留：

```text
factor.parent_task_id
factor.computed_at
```

- [ ] **Step 5: 删除旧执行器和无调用缓存**

删除 Target File Map 中的 stdio executor、worker pool、WindowCache 和 SnapshotStore，同时删除 `WritebackBars`、`ExtraColumns`、`FactorColumnResult`、`result_tails`、`PerFactorMS`、`ElapsedMS` 和旧 runtime executor 名称。

确认：

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
! rg -n \
  'StdioExecutor|WorkerPool|SnapshotStore|SnapshotPath|arrow_mmap|cross_section|sections-dir|WritebackBars|writeback_bars|ExtraColumns|extra_columns|FactorColumnResult|PerFactorMS|ElapsedMS|RuntimePoolExecutor|NewRuntimePoolExecutor|runtime_pool_executor' \
  modules/factor/internal modules/factor/proto modules/factor/cmd modules/factor/test
! rg -n \
  'arrow_mmap|cross_section|result_tails|per_factor_ms|elapsed_ms' \
  modules/factor/pyworker --glob '!test_*.py'
```

Expected: 命令退出 0。

- [ ] **Step 6: 运行 Go/Python 契约测试**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/engine ./internal/scheduler ./internal/storageio ./internal/bootstrap ./test -count=1
go test -race ./internal/engine ./internal/scheduler ./internal/bootstrap -count=1
PYTHONPATH=../../packages/pyruntime/python python3 -m pytest -q pyworker
```

Expected: PASS；平盘 `Cci` 返回 null 结果，不再使整个实时任务失败。

- [ ] **Step 7: 提交 runtime 收敛**

```bash
git add -A modules/factor/internal/engine modules/factor/internal/storageio \
  modules/factor/internal/scheduler modules/factor/internal/bootstrap \
  modules/factor/pyworker modules/factor/sections modules/factor/test
git commit -m "refactor(factor): simplify Python executor and range output"
```

---

### Task 6: 在 Scheduler 建立唯一 Task Builder 并简化手动补算

**Files:**
- Create: `modules/factor/internal/scheduler/builder.go`
- Create: `modules/factor/internal/scheduler/builder_test.go`
- Modify: `modules/factor/internal/scheduler/task.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/internal/rpc/recalc.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/cmd/cli/main.go`
- Modify: `modules/factor/cmd/cli/main_test.go`
- Modify: `modules/factor/cmd/cli/import.go`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`
- Modify: `modules/factor/examples/run-once/README.md`
- Delete: `modules/factor/cmd/cli/replay.go`
- Delete: `modules/factor/internal/trigger/replay.go`
- Delete: `modules/factor/internal/trigger/replay_test.go`
- Delete: `modules/factor/internal/store/replay.go`
- Delete: `modules/factor/internal/store/replay_test.go`

- [ ] **Step 1: 写共享 builder 测试**

`scheduler/builder_test.go`：

```go
func TestBuildTaskUsesAllFactorsAndMaximumLookback(t *testing.T) {
	task, err := BuildTask(TaskScope{
		TaskID: "task-1", TriggerType: "recalc",
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC-USDT", Freq: "1m",
		StartTime: time.Unix(1, 0), EndTime: time.Unix(3, 0),
	}, []domain.FactorDef{
		{FactorID: "bias", Name: "Bias", SourceHash: "h1", Periods: []int{20}, LookbackBars: 100, Depends: []string{"funding_rate"}},
		{FactorID: "cci", Name: "Cci", SourceHash: "h2", Periods: []int{14}, LookbackBars: 200},
	}, "/factor")
	require.NoError(t, err)
	require.Equal(t, 200, task.LookbackBars)
	require.Len(t, task.Factors, 2)
	require.Equal(t, []int{20}, task.Factors[0].Periods)
	require.Equal(t, []string{"funding_rate"}, task.Factors[0].Depends)
}
```

另加 invalid empty factors、missing source hash、zero range endpoint 和 `start_time >= end_time` 测试。

- [ ] **Step 2: 实现唯一 builder**

核心 API：

```go
type TaskScope struct {
	TaskID        string
	TriggerType   string
	SpaceID       string
	SourceDataset string
	TargetDataset string
	SubjectID     string
	Freq          string
	StartTime     time.Time
	EndTime       time.Time
}

func BuildTask(scope TaskScope, factors []domain.FactorDef, factorsDir string) (Task, error)
```

该函数负责：

```text
source version path
periods
depends
maximum lookback
FactorSpec creation
```

Builder 与目标 `scheduler.Task` 位于同一包，不引入 `calculation`、`taskbuilder` 或 `engine -> scheduler` 反向依赖。Bootstrap、RPC 和 CLI 不再各自解析 periods 或拼接 version path。realtime builder 将事件 `bar_time` 转换为 `[bar_time, bar_time + 1ns)`；RPC 和 CLI 直接使用请求的半开范围。

- [ ] **Step 3: 增加同步 range runner**

删除 `scheduler.TaskResult`、`Task.Completion` 以及 completion channel。scheduler 增加：

```go
const maxTargetBarsPerChunk = 2000

func (s *Service) Run(ctx context.Context, task Task) error
```

`Run` 是 realtime worker、RPC 和 CLI 共用的唯一同步执行入口：

```text
cursor = task.StartTime
processed = 0
while cursor < task.EndTime:
    ReadRangeChunk(cursor, task.EndTime, task.LookbackBars, 2000)
    no target rows and processed == 0 -> error
    no target rows and processed > 0  -> done
    derive chunk task range from first/last TargetTimes
    execute Python once
    validate every column length against TargetTimes
    write this chunk once
    cursor = last target time + 1ns
```

每个 chunk 沿用 `max_retry` 的本进程有限重试。总耗时在 `Run` 外层从读取开始计到最后一次写回结束，写入结构化日志；不放入 `FactorResult`。任一后续 chunk 最终失败时立即返回错误，已完成 chunk 不回滚。

终态日志固定包含：

```text
task_id trigger_type space_id source_dataset target_dataset subject_id freq
start_time end_time factor_count chunk_count status task_elapsed_ms error
```

`task_elapsed_ms` 是完整范围调用耗时；不再用含义不清的 Python `elapsed_ms` 冒充任务总耗时。

- [ ] **Step 4: 将 RecalcFactor 改为同步范围补算**

删除 `recalcState`、`recalc map`、`GetRecalcProgress` 和后台 goroutine。

实现结构：

```go
func (s *Service) RecalcFactor(ctx context.Context, req *factorpb.RecalcFactorReq) (*factorpb.RecalcFactorRsp, error) {
	task, err := s.buildRecalcTask(ctx, req)
	if err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.scheduler.Run(ctx, task); err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.RecalcFactorRsp{RetInfo: success()}, nil
}
```

只接受合法 `start_time/end_time` 半开范围。补算直接调用 `Run`，不占用 realtime queue capacity；request context 取消会停止当前 chunk 并返回失败。

- [ ] **Step 5: 统一 CLI run-once runtime**

`run-once`：

- 使用 `scheduler.BuildTask`。
- 接受必填 `--start-time` 和 `--end-time`，删除 `--bar-time`。
- 通过 `scheduler.Service.Run` 同步处理范围，不自行逐 bar 循环。
- 使用 `engine.NewPythonExecutor(ctx, 1, process.Config{...})`。
- 不再创建 Factor-local `StdioExecutor`。
- 不再 claim replay task。
- CLI import 将 `--default-params` 改名为 `--default-periods`，`cliConfig.DefaultParams` 改名为 `DefaultPeriods`。
- 构建产物继续命名为 `moox-factor-cli`；服务端产物继续命名为 `moox-factor`，不得合并入口。
- 输出 terminal JSON：

```json
{"ok":true,"task_id":"manual-...","status":"succeeded","factor_count":2,"start_time":"2026-07-26T00:00:00Z","end_time":"2026-07-27T00:00:00Z","elapsed_ms":12}
```

这里的 `elapsed_ms` 由 CLI 包围整个 `Run` 调用测量，是读取、全部 chunk 计算和写回的总耗时，不来自 `FactorResult`。

- [ ] **Step 6: 删除 replay 命令和持久状态**

从 CLI 参数解析和 README 删除：

```text
replay
--input
--factor-version
--target-run-id
```

删除 replay files 和 `t_factor_replay_tasks` 相关代码。

确认：

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
! rg -n 'ReplayTask|ReplayRange|target_run_id|factor_version|GetRecalcProgress' modules/factor
```

Expected: 命令退出 0。

- [ ] **Step 7: 补算行为测试**

必须覆盖：

```text
missing subject -> invalid
missing start_time/end_time -> invalid
bad RFC3339/RFC3339Nano -> invalid
start_time >= end_time -> invalid
factor_id empty -> all executable factors
explicit disabled factor -> invalid
manual range bypasses realtime queue capacity
2001 target bars -> two ordered chunks
second chunk failure -> synchronous error and first chunk remains written
all chunks succeed -> SUCCESS ret_info
request context cancelled -> context error
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/scheduler ./internal/rpc ./cmd/cli -count=1
go test -race ./internal/rpc -count=1
```

Expected: PASS。

- [ ] **Step 8: 提交补算收敛**

```bash
git add -A modules/factor/internal/scheduler modules/factor/internal/bootstrap \
  modules/factor/internal/rpc modules/factor/internal/trigger \
  modules/factor/internal/store modules/factor/cmd/cli \
  modules/factor/examples/run-once/README.md
git commit -m "refactor(factor): unify task building and manual recalc"
```

---

### Task 7: 删除未使用配置、Timer 和伪状态

**Files:**
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/config/trpc_go.yaml`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/health/server.go`

- [ ] **Step 1: 写最小配置契约测试**

```go
func TestFactorConfigContainsOnlyRuntimeInputs(t *testing.T) {
	cfg := Default()
	require.Equal(t, 2048, cfg.Scheduler.QueueCapacity)
	require.Equal(t, 1, cfg.Scheduler.MaxRetry)
	require.NotEmpty(t, cfg.Engine.PythonBin)
	require.NotEmpty(t, cfg.Engine.FactorsDir)
}
```

并通过编译保证以下字段不存在：

```text
Engine.SectionsDir
Engine.Encoding
Engine.ArrowRowThreshold
Engine.ShmDir
Engine.MaxBatchParallelism
Engine.BatchMinEstimatedMS
Engine.SnapshotTTLSeconds
Scheduler.ReconcileIntervalMin
Config.Instance
Config.SysDeploy
Config.Health
```

- [ ] **Step 2: 改写 app.yaml**

最终结构：

```yaml
database:
  type: sqlite
  path: ./data/factor/factor.db
  max_idle_conns: 10
  max_open_conns: 30
  conn_max_lifetime: 1h
  conn_max_idle_time: 10m

storage:
  gateway_target: "ip://127.0.0.1:11003"
  gateway_node_id: ""
  key_id: "factor"
  hmac_key_file: ""

eventbus:
  urls:
    - nats://127.0.0.1:4222
  credential_file: ~/.config/moox/eventbus/factor-eventbus.yaml
  fetch_max_wait: 1s

engine:
  python_bin: python3
  factors_dir: ./factors
  workers: 8
  task_timeout_ms: 30000

scheduler:
  event_batch_window_ms: 2000
  queue_capacity: 2048
  max_retry: 1
```

- [ ] **Step 3: 删除 reconcile timer**

删除：

```text
registerReconcileSchedule
factorReconcileSchedule
trpc.moox.factor.reconcile.timer
```

保留 started scheduler 自己的 worker goroutines；`Drain` 仅供测试使用。

- [ ] **Step 4: 返回真实整体状态**

`GetEngineStatus` 只从 scheduler 返回：

```text
queue_depth
queue_overflow_count
```

同时删除 RPC 层的 `engineStatusProvider`、`Service.engine` 和 `NewWithRuntime` 的 engine 参数，并更新 bootstrap 与测试调用点。`PythonExecutor.Status()` 继续供 `/readyz` 使用；不得在 RPC 中构造虚假的 `worker-1 ready` 列表或复制健康状态。

- [ ] **Step 5: 验证严格 YAML 和服务启动**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test ./internal/bootstrap ./internal/rpc ./internal/health -count=1
go test ./internal/bootstrap -run 'TestLoadRejectsUnknownField' -count=1
```

Expected: PASS；旧配置字段在 YAML 中出现时由 `KnownFields(true)` 明确拒绝。

- [ ] **Step 6: 提交配置清理**

```bash
git add modules/factor/internal/bootstrap modules/factor/internal/rpc \
  modules/factor/internal/health modules/factor/config
git commit -m "refactor(factor): remove unused runtime configuration"
```

---

### Task 8: 收敛 Factor Web 管理面

**Files:**
- Modify: `web/src/api/factor/types.ts`
- Modify: `web/src/api/factor/index.ts`
- Modify: `web/src/views/factor/definitions/index.vue`
- Modify: `web/src/views/factor/bindings/index.vue`
- Modify: `web/src/views/factor/results/index.vue`
- Add or Modify: `web/src/views/factor/__tests__/factor-contract.spec.ts`

- [ ] **Step 1: 写 TypeScript 契约测试**

测试必须断言：

```text
FactorDef 使用 periods: number[]
FactorDef 不含 kind/params_json/writeback_bars
RecalcFactorRsp 只包含 ret_info
RecalcFactorReq 使用 start_time/end_time，不含 bar_time
API 不导出 getRecalcProgress
EngineStatus 只包含 queue_depth/queue_overflow_count
```

示例：

```ts
it("uses explicit periods and synchronous recalc", () => {
  const factor: FactorDef = {
    factor_id: "bias",
    name: "Bias",
    source_code: "def signal(): pass",
    periods: [5, 20],
    lookback_bars: 100,
    depends: ["funding_rate"],
    status: "enabled"
  };
  expect(factor.periods).toEqual([5, 20]);
});
```

- [ ] **Step 2: 删除多余定义字段 UI**

Definitions 页面删除：

```text
kind filter
kind table column
kind form field
cross_section option
writeback_bars table column
writeback_bars form field
```

将参数 JSON textarea 替换为数字 tags/input，提交时发送 `periods: number[]`。

- [ ] **Step 3: 收敛补算 API**

`api/factor/types.ts`：

```ts
export interface RecalcFactorReq {
  factor_id?: string;
  space_id: string;
  source_dataset: string;
  subject_id: string;
  freq: string;
  start_time: string;
  end_time: string;
}
```

`api/factor/index.ts`：

```ts
export function recalcFactor(params: RecalcFactorReq) {
  return callFactor<RecalcFactorReq, FactorRetRsp>("RecalcFactor", params);
}
```

删除 `getRecalcProgress`、`RecalcProgress` 和 `RecalcFactorResult`。

如果页面提供补算操作，弹窗只包含：

```text
space_id
source_dataset
subject_id
freq
start_time
end_time
optional factor_id
```

前端校验 `start_time < end_time`，并明确按 `[start_time,end_time)` 提交。点击确认后等待单次响应，成功或失败使用已有 Message feedback；不轮询。

- [ ] **Step 4: Results 页面只展示 Storage 结果**

`results/index.vue` 不展示 FactorRun 历史或异步进度。保留 Storage 结果数据集查询，并在状态区域显示：

```text
queue depth
queue overflow count
```

- [ ] **Step 5: 运行前端验证**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm test -- factor-contract
pnpm lint:eslint:check
pnpm build:prod
```

Expected: PASS；页面和 API 无 `cross_section`、`params_json`、`GetRecalcProgress`。

- [ ] **Step 6: 提交 Web 收敛**

```bash
git add web/src/api/factor web/src/views/factor
git commit -m "refactor(web): align factor UI with simple timeseries runtime"
```

---

### Task 9: 更新事实文档并加入真实实时链路 E2E

**Files:**
- Modify: `modules/factor/test/e2e_test.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/consumer_test.go`
- Modify: `scripts/check/verify-event-contracts.sh`
- Modify: `modules/factor/README.md`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/superpowers/plans/2026-07-06-factor-calculation-module.md`

- [ ] **Step 1: 将旧大计划标记为历史**

在 `2026-07-06-factor-calculation-module.md` 标题后加入：

```markdown
> **状态：历史实施计划，不是当前 Factor 架构事实源。**
> 该计划中的 durable inbox、FactorRun、Arrow、截面因子、多实例分片和
> replay 持久化已经被 2026-07-26 的个人量化简化决策取代。当前实施依据为
> [Factor Best-Effort Simplification](2026-07-26-factor-best-effort-simplification.md)。
```

- [ ] **Step 2: 重写 Factor 设计文档的核心契约**

`docs/因子计算模块设计.md` 必须明确：

```text
single instance
timeseries only
best-effort ACK after `EventBatcher.Add`
bounded queue
one logical read / one Python call / one write per chunk
realtime one-bar range
manual half-open range recalc
lookback_bars is input context, not output length
range result columns have one value per target bar
null result contract
no durable scheduler/inbox/DLQ/exactly-once
no Arrow/cross-section/multi-instance
```

删除 M4-M6 作为现有路线的描述；性能扩展只保留“以 profiling 结果重新立项”的一句边界。

- [ ] **Step 3: 将模块 README 改为可操作事实**

README 包含：

```bash
./scripts/build/build.sh factor

# 服务端启动方式保持不变
./bin/moox-factor

./bin/moox-factor-cli init --db ./data/factor/factor.db
./bin/moox-factor-cli import --db ./data/factor/factor.db --factors-dir ./factors --default-periods 20,96
./bin/moox-factor-cli run-once \
  --space crypto \
  --dataset binance_spot_kline \
  --subject BTC-USDT \
  --freq 1m \
  --start-time 2026-07-26T00:00:00Z \
  --end-time 2026-07-27T00:00:00Z
```

README 必须直说：ACK 后进程退出可能漏算，使用 `run-once` 或 `RecalcFactor` 修复。范围采用 `[start_time,end_time)`；大于 2000 个目标 bar 时在进程内自动分 chunk，同步调用期间没有持久化进度，失败后可重跑整个范围。

- [ ] **Step 4: 扩展实时 E2E**

`test/e2e_test.go` 测试应启动：

```text
embedded JetStream server
real packages/events publish
real Factor eventconsumer
real EventBatcher
real Scheduler
real PythonExecutor
fake StorageIO with deterministic candle frame
```

断言：

```text
one DatasetRowsUpserted is ACKed
batch window creates one task
task contains enabled Bias and excludes disabled Cci
fake Storage receives one range-chunk read and one write
write contains Bias_20
queue and worker return idle
```

该测试名固定为：

```go
func TestRealtimeEventToPythonWritebackE2E(t *testing.T)
```

它不是完整部署 E2E；名称和文档必须准确说明 Storage RPC 使用 fake。真实部署验证仍通过 `realtime-verification.md` 手工执行。

- [ ] **Step 5: 更新事件契约脚本**

`scripts/check/verify-event-contracts.sh`：

- 删除 durable inbox 字符串和 store 测试假设。
- 保留 Factor Consumer 重连和真实 JetStream delivery。
- 增加 `TestRealtimeEventToPythonWritebackE2E`。
- 增加禁止残留词：

```bash
if rg -n \
  'NewDurableEventBatcher|PendingEventStore|t_factor_event_inbox|cross_section|arrow_mmap|GetRecalcProgress|ListFactorRuns' \
  modules/factor; then
  echo "removed factor capability remains"
  exit 1
fi
```

- [ ] **Step 6: 运行文档和 E2E 验证**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
(cd modules/factor && go test ./test -run TestRealtimeEventToPythonWritebackE2E -count=1 -v)
./scripts/check/verify-event-contracts.sh
bash scripts/test/contract/test-docs-architecture.sh
```

Expected: PASS。

- [ ] **Step 7: 提交文档和 E2E**

```bash
git add modules/factor/test modules/factor/internal/trigger/eventconsumer \
  modules/factor/README.md modules/factor/docs \
  docs/因子计算模块设计.md \
  docs/superpowers/plans/2026-07-06-factor-calculation-module.md \
  scripts/check/verify-event-contracts.sh
git commit -m "test(factor): prove best-effort realtime pipeline"
```

---

### Task 10: 全量验收、独立审查和远端闭环

**Files:**
- Modify only files required by findings from the checks below.

- [ ] **Step 1: 扫描被删除概念**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
! rg -n \
  'cross_section|params_json|ParamsJSON|default-params|sections_dir|arrow_row_threshold|max_batch_parallelism|snapshot_ttl_seconds|reconcile_interval_min|primary_target|heartbeat_interval_ms|t_factor_event_inbox|t_factor_event_processed|t_factor_replay_tasks|GetRecalcProgress|ListFactorRuns|StdioExecutor|WorkerPool|SnapshotStore|WritebackBars|writeback_bars|ExtraColumns|extra_columns|FactorColumnResult|PerFactorMS|RuntimePoolExecutor|NewRuntimePoolExecutor|runtime_pool_executor|IngestMessage' \
  modules/factor/internal modules/factor/proto modules/factor/schema modules/factor/cmd \
  modules/factor/config modules/factor/test \
  web/src/api/factor web/src/views/factor
! rg -n \
  'dropped_tasks|DroppedTasks|supersede_count|SupersedeCount|writeback_failures|WritebackFailures|WorkerStatus' \
  modules/factor/proto modules/factor/internal/rpc modules/factor/internal/scheduler web/src/api/factor
! rg -n \
  'queue_capacity|QueueCapacity|worker_count|WorkerCount|worker_ready|WorkerReady' \
  modules/factor/proto web/src/api/factor
! rg -n \
  'ElapsedMS|PerFactorMS' \
  modules/factor/internal/engine
! rg -n \
  'result_tails|per_factor_ms|elapsed_ms' \
  modules/factor/pyworker --glob '!test_*.py'
```

Expected: 命令退出 0。若命中 generated code，说明 Proto 未正确重新生成。

- [ ] **Step 2: 格式和 schema 验证**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
gofmt -w $(find modules/factor -name '*.go' -not -path '*/factorgen/*')
sqlite3 :memory: < modules/factor/schema/factor.sql
git diff --check
```

Expected: PASS。

- [ ] **Step 3: Factor 全量测试**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
PYTHONPATH=../../packages/pyruntime/python python3 -m pytest -q pyworker
```

Expected: 全部 PASS。

- [ ] **Step 4: 仓库集成验证**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
./scripts/build/build.sh factor
./scripts/check/verify-event-contracts.sh
./scripts/test/contract/test-go-workspace.sh
test -x /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/bin/moox-factor
test -x /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/bin/moox-factor-cli
```

Expected: 全部 PASS，且服务端与 CLI 构建产物名称保持不变。

- [ ] **Step 5: Web 验证**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm test
pnpm lint:eslint:check
pnpm build:prod
```

Expected: 全部 PASS。

- [ ] **Step 6: 独立代码审查**

审查者必须逐项确认：

```text
Factor disable 最迟一个 batch window 后停止执行
best-effort ACK 边界与 README 一致
队列容量有上限且 supersede 不制造 stale item
每个 range chunk 只有一次 executor call，任务内因子不再二次分批
null 被允许且跳过写回
realtime 只写事件对应的单 bar range
手动补算严格写 `[start_time,end_time)` 内的目标 bar
每个 chunk 最多 2000 个目标 bar，并补充 `lookback_bars - 1` 条前置历史
FactorResult 只包含等长结果列，不包含性能计时字段
FactorDef 和 FactorSpec 都使用 Depends，不存在 ExtraColumns 别名
Metadata 不在实时热路径
run-once 和 realtime 使用同一 builder/runtime
Proto/DB/UI 不再暴露删除能力
E2E 实际经过 JetStream 和 Python worker
```

发现问题时只修改本计划范围内文件，重跑受影响测试和 Step 3-5。

- [ ] **Step 7: 检查提交边界**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git log --oneline --decorate --max-count=15
git status --short
```

Expected: Factor 改造由小提交组成；用户原有无关改动未被提交或改写。

- [ ] **Step 8: 推送并验证远端 SHA**

```bash
git push origin feature/mooyang
LOCAL_SHA=$(git rev-parse HEAD)
REMOTE_SHA=$(git ls-remote origin refs/heads/feature/mooyang | awk '{print $1}')
test "${LOCAL_SHA}" = "${REMOTE_SHA}"
git status --short --branch
```

Expected: local HEAD 与远端 `feature/mooyang` 完全一致；工作树只允许保留实施前已存在且明确属于用户的无关改动。

## 5. 完成定义

只有同时满足以下条件才可宣布完成：

1. 实时链路不再访问 Factor event inbox 或 replay 表。
2. 文档明确承认 ACK 后进程退出会漏算。
3. 因子和 Binding 任一禁用都不会进入新的实时任务。
4. scheduler 的待执行 scope 数受 `queue_capacity` 限制；每次因容量不足而失败的入队操作只增加一次 `queue_overflow_count`。
5. 一个 realtime task 只产生一个单 bar chunk；每个 chunk 只进行一次逻辑 Storage range read、一次 Python execute 和一次 Storage write，并且只写该 chunk 的 `TargetTimes`。
6. 平盘 Cci 的 null 输出不会导致整任务失败。
7. `run-once` 和 `RecalcFactor` 可同步完成 `[start_time,end_time)` 范围补算；超过 2000 个目标 bar 时自动分 chunk，任一失败返回错误且不回滚已成功 chunk；`RecalcFactorRsp` 只通过 `ret_info` 表达成功或失败。
8. `FactorResult` 只包含 `Columns map[string][]any`，每列长度与当前 chunk 的目标 bar 数一致；性能耗时由外层日志测量。
9. Proto、Schema、Go、Python 和 Web 中不再存在 cross-section、Arrow、FactorRun、异步补算进度、durable inbox、`writeback_bars`、`ExtraColumns`、结果内性能计时字段或旧 runtime executor 名称。
10. Factor Go tests、race、vet、Python tests、build、event contracts、workspace tests 和 Web build 全部通过。
11. 独立审查无未处理发现，local/remote SHA 完全一致。
