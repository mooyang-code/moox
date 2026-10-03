# 因子计算模块重构执行计划：数据集驱动的周期流水线

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把因子模块重构为单进程 `moox-factor`。它订阅源数据集 `CollectorPeriodCompleted`，按因子集一次读取最大回看窗口并共享，并发计算全部因子，把携带列与因子列整行写入结果数据集，上报 `FactorPeriodComputed`；Storage 自动维护结果 View，策略等待 `ViewDataReady`。

**Architecture:** 因子只与数据集交互，View 只是查询索引。一个数据集只属于一个 DataNode，同一 outbox 保证“行先于完成标记”。实时链路无本地账本，至少一次处理加确定性 commit_id 幂等写入。SQLite 只保存因子集、因子定义和补算任务。实时与补算共用同一 `pipeline.Runner`。`moox-merge` 下线，现货与永续以 `series_tag` 写入同一源数据集。

**Tech Stack:** Go 1.2x、tRPC-Go、NATS JetStream、SQLite/GORM、Pebble DataNode outbox、DuckDB View、Python worker（`packages/pyruntime` JSON 帧）、Vue 3 + Vitest。

**设计基线：** [因子计算模块重构设计](../specs/2026-10-04-factor-dataset-period-pipeline-design.md)。本计划中的“设计 §N”均指该文档章节。

---

## 当前基线与执行约束

- 计划基于 `feature/mooyang`（`f65c0274`，Collector/CloudNode 可靠性加固已提交）编写，此时工作区干净。执行前先 `git status` 确认；若出现与本计划无关的未提交改动，视为用户工作，不得覆盖、回滚或混入本计划的提交。
- 新项目原则：不兼容旧协议、旧数据、旧配置。proto 中的 `reserved` 声明直接删除；旧表、旧事件字段、旧 RPC 直接删除，不保留适配层。
- 每个 Task 结束都要满足：涉及模块的 `go build ./...`、`go vet ./...` 和相关 `go test` 通过；修改 schema 时逐个载入空 SQLite；`git diff --check` 无告警；`bash scripts/check/check-gofmt.sh` 通过。
- 每个 Task 单独提交，提交信息使用 Conventional Commit（例如 `feat(factor): ...`、`refactor(storage): ...`），只 `git add` 本 Task 涉及的文件。
- 阶段之间存在依赖：阶段 1（Storage）与阶段 2（Collector）完成前，不切换实时消费；阶段 3–5 可在阶段 1 的 proto 定稿后并行开发，但阶段 6 的切换必须等待阶段 1–5 全部完成。
- 一期只支持 crypto 空间；遇到 A 股相关路径时返回明确错误 `unsupported space calendar`，不做隐式兼容。

## 文件责任图

### Storage（阶段 1）

- 修改 `packages/storagepb/storage_events.proto`：`FactorPeriodState`、`FactorPeriodComputed` 新 payload，`ViewDataReady.factors`；删除 `FactorBindingPeriodState`、`MergePeriodCompleted`。
- 修改 `modules/storage/proto/dataset_markers.proto`、`primary_store.proto`、`data_node.proto`、`metadata.proto`：标记字段、`WriteFactorRows`，删除 `PatchFactor`、Merge 标记、Storage 因子元数据 RPC。
- 修改 `packages/events/{registry.go,decode.go,validation.go}`：事件注册与校验。
- 修改 `modules/storage/internal/service/primarystore/{marker.go,input_commit.go}`，新增 `factor_rows.go`：路由与鉴权。
- 修改 `modules/storage/internal/service/datanode/{service.go,marker.go}`、`datanode/pebble/{marker_message.go,marker_store.go,input_commit.go}`，新增 `datanode/pebble/factor_rows.go`：结果行写入与 outbox。
- 修改 `modules/storage/internal/service/view/{period_event_apply.go,data_ready.go,inventory_reconciler.go}`、`view/eventconsumer/{consumer.go,handler.go,delivery_policy.go}`：转发 `factors[]`，删除 Merge 分支，加列不重建。
- 修改 `modules/storage/internal/service/catalog/{metadata_catalog.go,activation.go,metadata_data_node.go,validate.go,metadata_space_view.go}`：`factor_result` 激活自动建默认 View、DataNode owner 不可变、删除因子实体与 `mdataset_`/`merged_factor`。
- 修改 `modules/storage/internal/service/metadata/sqlite/crud_dataset.go`、`modules/storage/schema/metadata.sql`：删除 `t_factors` 及其 CRUD。
- 修改 `modules/storage/cmd/server/main.go`、`modules/storage/cmd/cli/{reconcile_view_consumers.go,purge_dataset_events.go,reset_view_consumers.go}`、`modules/storage/config/{storage.yaml,storage_view/trpc_go.yaml}`、`modules/storage/internal/config/loader.go`：去掉 Merge 订阅与配置。

### Collector 与初始化元数据（阶段 2）

- 修改 `config/setup/metadata.yaml`、`config/setup/collection-tasks.yaml`（或实际定义 Binance K 线任务的文件）：合并为 `dataset_binance_kline_1m`，删除 `mdataset_binance_kline_1m` 与 `view_binance_kline_1m`。
- 修改 `modules/collector/internal/store/period_readiness.go`、`modules/collector/internal/marketfetch/period_reporter.go` 及测试：期望项按 `(subject, series_tag)` 统计。

### Factor 新实现（阶段 3–5）

- 重写 `modules/factor/schema/factor.sql`、`modules/factor/proto/factor.proto`（重新生成 `factorgen/`）。
- 新增或重写 `modules/factor/internal/{domain,store,catalog,periodclock,trigger,pipeline,pyexec,storageio,recalc,rpc,bootstrap,observability}/`。
- 修改 `modules/factor/pyworker/worker.py` 及其测试。
- 重写 `modules/factor/cmd/server/main.go`、`modules/factor/cmd/cli/main.go`、`modules/factor/config/app.yaml`。

### 下游与运维（阶段 6）

- 修改 `modules/strategy/internal/{trigger/processor.go,storageio/rpc.go,storageio/view.go,factorio/client.go,compiler/types.go,compiler/verify_dependencies.go,bootstrap/bootstrap.go}`。
- 修改 `web/src/api/factor/{index.ts,types.ts}`、`web/src/views/factor/**`、`web/src/router` 中的因子路由、`web/src/lang/modules/{zhCN,enUS}.ts`、`web/src/views/data/{shared/metadata-utils.ts,shared/module-attribution.ts,datasets/default-view.ts}`。
- 修改 `modules/cli/internal/command/setup_factors.go`、`modules/cli/internal/setup/config/config.go`、`modules/cli/internal/setup/deploy/deploy.go`、`moox.toml.example`、`moox.toml`、`modules/cli/README.md`。
- 修改 `modules/admin/internal/service/sysdeploy/{defaults.go,dao.go}`、`modules/admin/cmd/cli/eventbus_credentials.go`、`config/setup/service-deployments.yaml`。

### 删除与文档（阶段 7–8）

- 删除 `modules/merge/`、`go.work` 中的 `./modules/merge`、`scripts/deploy/factor-engine/`、`scripts/build/build-storage-linux.sh` 中的 merge/factor-engine 条目。
- 删除 `modules/factor/internal/{taskrunner,inputcache,catalogsync,registry,engine}/`、旧 `trigger/*`、旧 `bootstrap/*`、`modules/factor/cmd/engine/`、`modules/factor/config/engine-*.yaml`。
- 重写 `docs/因子计算模块设计.md`、`modules/factor/README.md`；删除 `docs/因子视图驱动计算设计.md`、`modules/factor/docs/realtime-verification.md`；更新 `docs/SUMMARY.md`、`skills/moox/SKILL.md`、`skills/moox/references/{view-catchup.md,cli-operations.md}`；在 `docs/superpowers/specs/2026-09-13-factor-dataset-view-refactor-design.md` 顶部标注“已被 2026-10-04 设计替代”。

## 接口约定

以下 Go 签名在多个 Task 中引用，实现时必须保持一致；字段可增，但名称与语义不得改变。

```go
// modules/factor/internal/domain
package domain

const (
	SetStatusPending  = "pending"
	SetStatusEnabled  = "enabled"
	SetStatusDisabled = "disabled"

	FactorStatusEnabled  = "enabled"
	FactorStatusDisabled = "disabled"

	FactorTypeTimeSeries   = "timeseries"
	FactorTypeCrossSection = "cross_section"

	SubjectModeAll     = "all"
	SubjectModeInclude = "include"
)

var ReservedColumns = []string{"subject_id", "freq", "data_time", "series_tag"}

type FactorSet struct {
	SetID           string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	SubjectMode     string
	Subjects        []string
	ResultDatasetID string
	Status          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type FactorDef struct {
	FactorID             string
	SetID                string
	Name                 string
	FactorType           string
	SourceCode           string
	SourceHash           string
	InputColumns         []string
	Outputs              []string
	ParamsJSON           string
	LookbackPeriods      int
	AllowPartialUniverse bool
	Status               string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func SetID(sourceDatasetID, freq string) string
func ResultDatasetID(sourceDatasetID, freq string) string
func SourceHash(sourceCode string) string // "sha256:<hex>"
func ValidateSet(set FactorSet) error
func ValidateFactor(def FactorDef, sourceColumns []string, siblings []FactorDef) error
func (s FactorSet) InScope(subjectID string) bool
```

```go
// modules/factor/internal/periodclock
package periodclock

type Clock interface {
	Duration(freq string) (time.Duration, error)
	Align(t time.Time, freq string) (time.Time, error)
	Window(t time.Time, freq string, n int) ([]time.Time, error) // 升序，末项为 t
}

func ForSpace(spaceID string) (Clock, error) // crypto → Continuous；其它返回 ErrUnsupportedCalendar
```

```go
// modules/factor/internal/storageio
package storageio

type Frame struct {
	SubjectID string
	Columns   []string // data_time, series_tag 之后为业务列
	Rows      [][]any  // 按 (data_time, series_tag) 升序
}

type ResultRow struct {
	SubjectID string
	DataTime  time.Time
	SeriesTag string
	Fields    map[string]any // nil 值表示显式写 null
}

type PeriodMarker struct {
	SpaceID          string
	ResultDatasetID  string
	SourceDatasetID  string
	Frequency        string
	PeriodTime       int64
	Status           string
	UniverseSubjects []string
	FailedSubjects   []string
	Factors          []FactorState
	TriggerEventID   string
	ComputedAt       time.Time
}

type FactorState struct {
	FactorID       string
	Status         string // complete | degraded | skipped
	FailedSubjects []string
	SourceHash     string
}

type Store interface {
	DatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error)
	ReadWindow(ctx context.Context, req ReadRequest) (map[string]*Frame, error)
	WriteRows(ctx context.Context, spaceID, datasetID, commitID string, rows []ResultRow) error
	ReportComputed(ctx context.Context, marker PeriodMarker) error
	ComputedExists(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (bool, error)
}

type ReadRequest struct {
	SpaceID   string
	DatasetID string
	Freq      string
	Subjects  []string
	Start     time.Time // 含
	End       time.Time // 不含
	Columns   []string
}

var ErrInfra = errors.New("storage infrastructure failure") // 用 errors.Is 判断是否 NAK
```

```go
// modules/factor/internal/pyexec
package pyexec

type FactorCall struct {
	FactorID        string
	Name            string
	SourceHash      string
	SourcePath      string
	FactorType      string
	InputColumns    []string
	Outputs         []string
	Params          json.RawMessage
	LookbackPeriods int
}

type Request struct {
	Frame   any            // 时序：单标的 Frame；截面：面板 {columns, rows}
	Factors []FactorCall
	Context map[string]any // period_time, frequency, period_times_by_factor, subject_id | expected/available_subjects
}

type ItemResult struct {
	FactorID string
	Columns  []string
	Rows     [][]any
	Err      error
}

type Executor interface {
	Exec(ctx context.Context, req Request) ([]ItemResult, error) // error 只表示整次调用失败
	Busy() int
	Close() error
}
```

```go
// modules/factor/internal/pipeline
package pipeline

type Mode int

const (
	ModeLive Mode = iota + 1
	ModeRecalc
)

type Plan struct {
	Mode            Mode
	Set             domain.FactorSet
	Factors         []domain.FactorDef // 已冻结
	TargetStart     time.Time          // 含
	TargetEnd       time.Time          // 不含；实时为 T+freq
	Expected        []string
	Available       []string
	UpstreamFailed  []string
	CarryColumns    []string
	WriteCarry      bool               // 实时与补算都为 true
	TriggerEventID  string             // 仅实时
	Budget          time.Duration
}

type Outcome struct {
	Status         string // complete | degraded
	FailedSubjects []string
	Factors        []storageio.FactorState
	RowsWritten    int
	StageDurations map[string]time.Duration
}

type Runner struct { /* store storageio.Store; exec pyexec.Executor; clock periodclock.Clock; cfg Config */ }

func (r *Runner) Run(ctx context.Context, plan Plan) (Outcome, error) // error 包装 storageio.ErrInfra 时调用方 NAK
```

```go
// modules/factor/internal/trigger
package trigger

const ConsumerName = "factor_collector_period_v1"

type SetLocator interface {
	EnabledSetByDataset(ctx context.Context, spaceID, datasetID, freq string) (domain.FactorSet, []domain.FactorDef, bool, error)
	FilterSubjects(ctx context.Context) ([]string, error) // 由 enabled 因子集生成 JetStream 过滤主题
}
```

---

## 阶段 1：Storage 协议与能力

### Task 1：事件协议改为以因子为单位

**Files:**
- Modify: `packages/storagepb/storage_events.proto`、`packages/storagepb/storage_events_test.go`
- Modify: `modules/storage/proto/dataset_markers.proto`
- Modify: `packages/events/registry.go`、`packages/events/decode.go`、`packages/events/validation.go` 及测试
- Regenerate: `packages/storagepb/storage_events.pb.go`、`modules/storage/proto/storagegen/*`

- [ ] **Step 1：写失败测试。** 在 `packages/events` 测试中新增：
  - `TestValidateFactorPeriodComputedRequiresFactorStates`：`factors[i].factor_id` 为空时报错；`status` 只能是 `complete|degraded`；`factors[i].status` 只能是 `complete|degraded|skipped`。
  - `TestDecodeViewDataReadyFactorStates`：`completion_kind=factor_period.computed` 时解码出 `factors`。
  - `TestRegistryHasNoMergePeriodCompleted`：注册表中不存在 Merge 事件名。
- [ ] **Step 2：运行测试确认失败。**

```bash
cd packages/events && go test ./... -run 'FactorPeriodComputed|ViewDataReadyFactor|NoMergePeriod' -count=1
```

  预期：编译失败或断言失败。
- [ ] **Step 3：修改 proto。** 按设计 §8.2 定义 `FactorPeriodState`、`FactorPeriodComputed`；`ViewDataReady` 中的 `bindings` 改为 `repeated FactorPeriodState factors`；删除 `FactorBindingPeriodState`、`MergePeriodCompleted` 及其枚举值、`completion_kind` 中的 merge 取值；删除所有 `reserved` 声明。`dataset_markers.proto` 中 `FactorPeriodComputedMarker` 同步字段。
- [ ] **Step 4：重新生成代码。**

```bash
make -C packages/storagepb
make -C modules/storage/proto
```

- [ ] **Step 5：修改注册与校验**，使 Step 1 测试通过；删除 Merge 相关分支。
- [ ] **Step 6：验证。**

```bash
cd packages/events && go test ./... -count=1
cd ../storagepb && go test ./... -count=1
bash scripts/check/verify-event-contracts.sh
```

  此时 `modules/storage`、`modules/factor`、`modules/strategy`、`modules/merge` 会编译失败，属于预期：Storage 由 Task 2–6 修复，Factor 由 Task 14–21 重写，Merge 在 Task 21 删除，Strategy 由 Task 22 修复。本 Task 只提交 packages 与 proto。
- [ ] **Step 7：提交** `refactor(events): report factor period state per factor`。

### Task 2：Storage 标记链路转发 `factors[]` 并删除 Merge 处理

**Files:**
- Modify: `modules/storage/internal/service/primarystore/marker.go`、`datanode/marker.go`、`datanode/pebble/{marker_message.go,marker_store.go}`、`datanode/outbox/publisher.go`
- Modify: `modules/storage/internal/service/view/{period_event_apply.go,data_ready.go,inventory_reconciler.go}`、`view/eventconsumer/{consumer.go,handler.go,delivery_policy.go}`
- Modify: `modules/storage/cmd/server/main.go`、`modules/storage/cmd/cli/{reconcile_view_consumers.go,purge_dataset_events.go,reset_view_consumers.go}`、`modules/storage/internal/config/loader.go`、`modules/storage/config/storage.yaml`、`modules/storage/config/storage_view/trpc_go.yaml`
- Test: 上述目录内现有测试

- [ ] **Step 1：改测试。** 在 `period_event_apply` 测试中断言：处理 `FactorPeriodComputed` 后发布的 `ViewDataReady.factors` 与标记中的 `factors` 完全一致（顺序、failed_subjects、source_hash）。删除所有 Merge 用例，新增 `TestViewConsumerIgnoresUnknownMergeSubject`，断言 Merge 主题不再被订阅。
- [ ] **Step 2：运行确认失败。**

```bash
cd modules/storage && go test ./internal/service/view/... ./internal/service/datanode/... ./internal/service/primarystore/... -count=1
```

- [ ] **Step 3：实现。** 标记存储与 outbox 发布透传新字段；View 删除 `MergePeriodCompleted` 订阅、投递策略和库存对账分支；CLI 与配置中的 merge 订阅项一并删除。
- [ ] **Step 4：验证。**

```bash
cd modules/storage && go build ./... && go vet ./... && go test ./... -count=1
```

- [ ] **Step 5：提交** `refactor(storage): forward factor states and drop merge period events`。

### Task 3：新增 `WriteFactorRows` 并删除 `PatchFactor`

**Files:**
- Modify: `modules/storage/proto/primary_store.proto`、`modules/storage/proto/data_node.proto`（重新生成）
- Create: `modules/storage/internal/service/primarystore/factor_rows.go`、`factor_rows_test.go`
- Create: `modules/storage/internal/service/datanode/pebble/factor_rows.go`、`factor_rows_test.go`
- Modify: `modules/storage/internal/service/datanode/service.go`
- Delete: `primarystore/input_commit.go`、`datanode/pebble/input_commit.go` 中仅服务 `PatchFactor` 的逻辑及对应测试（input_commit 若仍被 Collector 写入路径使用则保留该部分，删除前用 `rg -n "InputCommit|PatchFactor" modules` 确认调用方）

- [ ] **Step 1：写失败测试**（`primarystore/factor_rows_test.go`）：
  - `TestWriteFactorRowsRejectsNonFactorResultDataset`：目标数据集角色不是 `factor_result` 时返回权限错误；
  - `TestWriteFactorRowsRejectsNonFactorCaller`：调用身份不是 `factor` 时拒绝；
  - `TestWriteFactorRowsRoutesToOwnerDataNode`：请求只发往数据集 owner 节点；
  - `TestWriteFactorRowsRejectsUnknownColumns`：行中出现数据集未声明的列时拒绝。
- [ ] **Step 2：写失败测试**（`pebble/factor_rows_test.go`）：
  - `TestFactorRowsWholeRowUpsertOverwritesNullFields`：同键第二次写入把字段写成 null 后读回为 null；
  - `TestFactorRowsSameCommitIDIsIdempotent`：同 commit_id 重复写入只产生一次 outbox `DatasetRowsUpserted`，`write_kind=factor_result`；
  - `TestFactorRowsThenMarkerOrdering`：写行后上报 `FactorPeriodComputed`，outbox 中行事件序号小于标记序号。
- [ ] **Step 3：运行确认失败。**

```bash
cd modules/storage && go test ./internal/service/primarystore/ ./internal/service/datanode/pebble/ -run 'FactorRows' -count=1
```

- [ ] **Step 4：实现。** proto 按设计 §8.3 增加 `PrimaryWriteFactorRowsReq/Rsp` 与 DataNode 对应 RPC；删除 `PrimaryPatchFactorReq/Rsp`、`binding_version`、`owned_fields`。Pebble 写入路径复用现有整行 upsert，`write_kind` 固定为 `factor_result`，commit_id 去重沿用现有 commit 去重机制。
- [ ] **Step 5：验证。**

```bash
make -C modules/storage/proto
cd modules/storage && go build ./... && go vet ./... && go test ./... -count=1
```

- [ ] **Step 6：提交** `feat(storage): add batched WriteFactorRows for factor result datasets`。

### Task 4：`factor_result` 激活自动建默认 View，加列不重建

**Files:**
- Modify: `modules/storage/internal/service/catalog/{activation.go,metadata_catalog.go,metadata_space_view.go}` 及测试
- Modify: `modules/storage/internal/service/view/` 中处理 View 列变更的代码（先定位，见 Step 1）
- Reference: `web/src/views/data/datasets/default-view.ts`（现有默认 View 的列与主键规则，迁移到服务端）

- [ ] **Step 1：定位现状。** 运行 `rg -n "UpsertViewColumn|ViewColumn|rebuild" modules/storage/internal/service/view modules/storage/internal/service/catalog`，记录数据集加列时 View 当前是增量加列还是触发 A/B 重建，并把结论写进本 Task 的提交说明。
- [ ] **Step 2：写失败测试**（catalog）：
  - `TestActivateFactorResultDatasetCreatesDefaultView`：激活 `factor_result` 数据集后存在 View `view_<去掉 dataset_ 的后缀>`，主键为 `subject_id, freq, data_time, series_tag`，列与数据集一致，保留期与数据集一致；
  - `TestActivateFactorResultDatasetIsIdempotent`：重复激活不创建第二个 View；
  - `TestActivateRawCollectionDatasetDoesNotCreateView`：其它角色不自动建 View。
- [ ] **Step 3：写失败测试**（view）：`TestFactorResultColumnAddDoesNotRebuild`：数据集新增列后 View 增量加列，active index 不变，不产生 rebuild 记录；已有行的新列读出为 null。
- [ ] **Step 4：运行确认失败。**

```bash
cd modules/storage && go test ./internal/service/catalog/ ./internal/service/view/ -run 'FactorResult|DefaultView' -count=1
```

- [ ] **Step 5：实现。** 在 `ActivateDataset` 提交成功后创建默认 View（同一事务或可重试的幂等步骤；创建失败时 `ActivateDataset` 返回错误，重试幂等）。View 列变更：新增列走 DuckDB `ALTER TABLE ADD COLUMN`，删除或改类型仍按现有规则处理（因子不会触发这两种变更）。
- [ ] **Step 6：验证。**

```bash
cd modules/storage && go test ./internal/service/catalog/... ./internal/service/view/... -count=1
```

- [ ] **Step 7：提交** `feat(storage): auto-create default view for factor result datasets`。

### Task 5：强制数据集单 DataNode owner

**Files:**
- Modify: `modules/storage/internal/service/catalog/{metadata_catalog.go,metadata_data_node.go,validate.go}` 及测试
- Modify: `modules/storage/proto/metadata.proto`（`RebindDatasetDataNode` 注释注明仅限离线运维）

- [ ] **Step 1：写失败测试：**
  - `TestCreateDatasetRequiresDataNodeID`：缺少 `data_node_id` 时报错；
  - `TestUpdateActiveDatasetCannotChangeDataNode`：`UpdateDataset` 修改已激活数据集的 owner 被拒绝；
  - `TestRebindDatasetRequiresDisabledState`：只有 disabled 数据集允许 Rebind。
- [ ] **Step 2：运行确认失败。**

```bash
cd modules/storage && go test ./internal/service/catalog/ -run 'DataNode' -count=1
```

- [ ] **Step 3：实现校验**，错误码复用现有 `INVALID_ARGUMENT` / `FAILED_PRECONDITION`。
- [ ] **Step 4：验证**：`cd modules/storage && go test ./internal/service/catalog/... ./internal/service/e2e/... -count=1`。
- [ ] **Step 5：提交** `fix(storage): pin each dataset to a single immutable data node`。

### Task 6：删除 Storage 因子元数据实体与 Merge 数据集概念

**Files:**
- Modify: `modules/storage/proto/metadata.proto`：删除 `CreateFactor/UpdateFactor/GetFactor/ListFactors` 及其消息、`FACTOR_NOT_FOUND`（若无其他用途）、`merged_factor` 角色。
- Modify: `modules/storage/schema/metadata.sql`：删除 `t_factors` 及其索引、触发器；更新头部注释中的 “Factor”。
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_dataset.go`、`crud_space_test.go`、`modules/storage/internal/service/catalog/{metadata_catalog.go,validate.go}` 及测试
- Modify: 数据集 ID 校验：只允许 `dataset_` 前缀

- [ ] **Step 1：写失败测试：** `TestDatasetIDRejectsMdatasetPrefix`、`TestDatasetRoleRejectsMergedFactor`；在列属性测试中断言 `origin=<factor_id>` 的列可以正常写入（替代因子实体）。
- [ ] **Step 2：运行确认失败**：`cd modules/storage && go test ./internal/service/catalog/ -run 'Mdataset|MergedFactor' -count=1`。
- [ ] **Step 3：删除代码与表**，并修复 `isFactorDatasetColumn` 等依赖：只依据数据集角色 `factor_result` 判断。
- [ ] **Step 4：schema 校验。**

```bash
for f in modules/storage/schema/*.sql; do sqlite3 :memory: < "$f" || echo "FAIL $f"; done
git diff --check
```

- [ ] **Step 5：验证**：`make -C modules/storage/proto && cd modules/storage && go build ./... && go test ./... -count=1`。
- [ ] **Step 6：提交** `refactor(storage): remove factor metadata entity and merged dataset role`。

---

## 阶段 2：Collector 多 tag 源数据集

### Task 7：周期完成按 `(subject, series_tag)` 统计

**Files:**
- Modify: `modules/collector/internal/store/period_readiness.go`、`period_readiness_test.go`
- Modify: `modules/collector/internal/marketfetch/period_reporter.go`、`period_reporter_test.go`

- [ ] **Step 1：确认现状。** 阅读 `period_readiness.go` 中期望项的键。如果已经包含 series_tag，本 Task 只补测试。
- [ ] **Step 2：写测试：**
  - `TestPeriodReadinessWaitsForAllSeriesTags`：同一数据集中 subject `BTCUSDT` 有 `venue:binance` 与永续 tag 两个期望项，只完成一个时周期仍为 waiting；
  - `TestPeriodReportedUniverseIsDistinctSubjects`：上报的 `universe_subject_ids` 去重，按 subject 计；任一 tag 失败时 subject 进入 `failed_subjects`。
- [ ] **Step 3：运行**：`cd modules/collector && go test ./internal/store/ ./internal/marketfetch/ -run 'SeriesTag|DistinctSubjects' -count=1`。
- [ ] **Step 4：按需实现**，使测试通过。
- [ ] **Step 5：验证**：`cd modules/collector && go test ./... -count=1`。
- [ ] **Step 6：提交** `fix(collector): complete periods only after every series tag lands`。

### Task 8：初始化元数据合并现货与永续

**Files:**
- Modify: `config/setup/metadata.yaml`
- Modify: 定义 Binance spot/swap 1m 采集任务的配置（用 `rg -n "binance_(spot|swap)_kline_1m" config modules/cli modules/admin` 定位）
- Modify: `modules/admin/cmd/cli/service_deployments_test.go`、`modules/cli/internal/setup/deploy/deploy_test.go` 等引用旧 ID 的测试

- [ ] **Step 1：** `metadata.yaml` 新增 `dataset_binance_kline_1m`（`raw_collection`、`data_node_id: storage-node-0`、`keep_duration: 720h`、列为现有 spot 数据集的列集合），删除 `dataset_binance_spot_kline_1m`、`dataset_binance_swap_kline_1m`、`mdataset_binance_kline_1m` 以及 `view_binance_spot_kline_1m`、`view_binance_swap_kline_1m`、`view_binance_kline_1m`；新增 `view_binance_kline_1m` 指向新源数据集，作为人工查询索引。
- [ ] **Step 2：** 采集任务的 spot 与 swap 都写入 `dataset_binance_kline_1m`，series_tag 留空，由 `defaultMarketSeriesTag` 生成。
- [ ] **Step 3：** 全仓搜索旧 ID 并更新测试与文档引用：

```bash
rg -n "dataset_binance_(spot|swap)_kline_1m|mdataset_binance_kline_1m|view_binance_(spot|swap)_kline_1m" --glob '!docs/superpowers/**'
```

  预期：除计划与历史设计文档外无结果（Monitor 新鲜度配置、Strategy 示例、skills 文档都要改）。
- [ ] **Step 4：验证**：`cd modules/cli && go test ./... -count=1`、`cd modules/admin && go test ./... -count=1`、`cd modules/monitor && go test ./... -count=1`。
- [ ] **Step 5：提交** `refactor(setup): collect binance spot and swap into one tagged dataset`。

---

## 阶段 3：Factor 骨架

### Task 9：重写 SQLite schema 与仓储

**Files:**
- Rewrite: `modules/factor/schema/factor.sql`
- Rewrite: `modules/factor/internal/store/`（删除旧文件，新建 `database.go`、`sets.go`、`defs.go`、`recalc_jobs.go` 及测试）

- [ ] **Step 1：** 按设计 §7 写入 schema，并补齐三张表的 mtime 触发器，例如：

```sql
CREATE TRIGGER IF NOT EXISTS trg_t_factor_sets_mtime
AFTER UPDATE ON t_factor_sets
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_sets SET c_mtime = CURRENT_TIMESTAMP WHERE c_set_id = OLD.c_set_id;
END;
```

- [ ] **Step 2：** 校验 schema：

```bash
sqlite3 :memory: < modules/factor/schema/factor.sql && echo OK
git diff --check
```

- [ ] **Step 3：写失败测试**（`store/*_test.go`，使用临时文件 SQLite）：
  - `TestCreateSetRejectsDuplicateSourceFreq`：同 `(space, dataset, freq)` 第二次创建返回 `ErrConflict`；
  - `TestEnabledSetByDatasetReturnsOnlyEnabledFactors`：返回 enabled 因子集及其 enabled 因子，按 `factor_id` 排序；
  - `TestDeleteSetFailsWhenFactorsExist`：外键阻止删除；
  - `TestRecalcJobRequestIDIsIdempotent`：同 request_id 返回同一 job；
  - `TestUpdateRecalcProgressAndCancel`：进度单调递增，cancelled 后不再变为 running。
- [ ] **Step 4：运行确认失败**：`cd modules/factor && go test ./internal/store/ -count=1`。
- [ ] **Step 5：实现。** `database.go` 用 `//go:embed` 内嵌 schema，启动时执行；不做旧表迁移。仓储方法：

```go
func (s *Store) CreateSet(ctx context.Context, set domain.FactorSet) error
func (s *Store) UpdateSet(ctx context.Context, set domain.FactorSet) error
func (s *Store) GetSet(ctx context.Context, setID string) (domain.FactorSet, error)
func (s *Store) ListSets(ctx context.Context) ([]domain.FactorSet, error)
func (s *Store) DeleteSet(ctx context.Context, setID string) error
func (s *Store) EnabledSetByDataset(ctx context.Context, spaceID, datasetID, freq string) (domain.FactorSet, []domain.FactorDef, bool, error)
func (s *Store) CreateFactor(ctx context.Context, def domain.FactorDef) error
func (s *Store) UpdateFactor(ctx context.Context, def domain.FactorDef) error
func (s *Store) GetFactor(ctx context.Context, factorID string) (domain.FactorDef, error)
func (s *Store) ListFactors(ctx context.Context, setID, status string) ([]domain.FactorDef, error)
func (s *Store) DeleteFactor(ctx context.Context, factorID string) error
func (s *Store) CreateRecalcJob(ctx context.Context, job RecalcJob) (RecalcJob, error)
func (s *Store) UpdateRecalcJob(ctx context.Context, jobID string, mutate func(*RecalcJob) error) (RecalcJob, error)
func (s *Store) GetRecalcJob(ctx context.Context, jobID string) (RecalcJob, error)
func (s *Store) ListRecalcJobs(ctx context.Context, statuses ...string) ([]RecalcJob, error)
```

- [ ] **Step 6：验证**：`cd modules/factor && go test ./internal/store/ -count=1`（旧包此时可能编译失败，用 `go test ./internal/store/` 单独验证，旧包在 Task 21 删除）。
- [ ] **Step 7：提交** `refactor(factor): replace factor ledger tables with sets, defs and recalc jobs`。

### Task 10：domain 实体、命名与校验

**Files:**
- Rewrite: `modules/factor/internal/domain/`（删除 `merged_dataset.go` 等旧文件，新建 `types.go`、`naming.go`、`validate.go` 及测试）

- [ ] **Step 1：写失败测试**（表驱动）：
  - `TestSetIDAndResultDatasetID`：`dataset_binance_kline_1m`+`1m` → `fset_binance_kline_1m`、`dataset_factor_binance_kline_1m`；`dataset_spot_kline`+`1h` → `fset_spot_kline_1h`、`dataset_factor_spot_kline_1h`；
  - `TestValidateFactorRejectsReservedOutput`：输出 `series_tag` 报错；
  - `TestValidateFactorRejectsOutputCollidingWithSourceColumn`：输出 `close` 报错；
  - `TestValidateFactorRejectsDuplicateOutputInSet`：与同集合其他因子输出重复时报错（siblings 中排除自身）；
  - `TestValidateFactorRejectsUnknownInput`：`input_columns` 不在源列中报错；
  - `TestValidateFactorLookbackAtLeastOne`、`TestValidateFactorParamsMustBeJSONObject`；
  - `TestSetInScope`：`all` 模式全部为真；`include` 只对名单为真。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/domain/ -count=1`。
- [ ] **Step 3：实现**接口约定中的 domain 签名。
- [ ] **Step 4：验证并提交** `refactor(factor): model factor sets and per-set factor definitions`。

### Task 11：periodclock（crypto 连续周期）

**Files:**
- Create: `modules/factor/internal/periodclock/{clock.go,continuous.go,clock_test.go}`

- [ ] **Step 1：写失败测试：**
  - `TestContinuousWindow1m`：`Window(2026-10-04T00:10:00Z, "1m", 3)` = `[00:08, 00:09, 00:10]`；
  - `TestContinuousWindowCrossesDay`：`1h`、N=3 跨日正确；
  - `TestAlignRejectsUnalignedTime`：`00:10:30` 在 `1m` 下返回错误；
  - `TestForSpaceStockCNUnsupported`：`ForSpace("stock_cn")` 返回 `ErrUnsupportedCalendar`；
  - `TestDurationSupportedFrequencies`：`1m, 5m, 15m, 30m, 1h, 4h, 1d` 正确，未知频率报错。
- [ ] **Step 2：运行确认失败**，然后实现，再运行通过：`cd modules/factor && go test ./internal/periodclock/ -count=1`。频率解析复用仓库已有的频率工具（`rg -n "func .*ParseFrequency|func .*FrequencyDuration" packages` 定位），没有时在本包实现。
- [ ] **Step 3：提交** `feat(factor): add continuous period clock for crypto windows`。

### Task 12：factor.proto 与 RPC 骨架

**Files:**
- Rewrite: `modules/factor/proto/factor.proto`，重新生成 `modules/factor/proto/factorgen/`
- Rewrite: `modules/factor/internal/rpc/`（`service.go`、`convert.go` 及测试）

- [ ] **Step 1：** 按设计 §8.1 重写 proto：消息 `FactorSet`、`FactorDef`、`RecalcJob`、`SetRunSummary`（`last_period_time`、`last_status`、`lag_seconds`），以及全部请求与响应；服务 `FactorMgr` 只包含设计 §8.1 列出的 RPC；删除 `FactorBinding`、`UpsertBinding/ListBindings/DeleteBinding`、`RecalcFactor`、`GetEngineStatus` 和全部 `reserved`。
- [ ] **Step 2：** 生成：`make -C modules/factor/proto`。
- [ ] **Step 3：写失败测试**（`rpc/service_test.go`，catalog 用接口 fake）：
  - `TestCreateFactorRequiresSetID`；
  - `TestRecalcFactorsValidatesTimeRange`：`end <= start` 或时间未按频率对齐时返回参数错误；
  - `TestConvertFactorDefRoundTrip`：domain ⇄ pb 转换不丢字段。
- [ ] **Step 4：实现。** RPC 层只做参数校验和转换，业务交给 `catalog.Service` 与 `recalc.Service` 接口：

```go
type CatalogAPI interface {
	CreateSet(ctx context.Context, in domain.FactorSet) (domain.FactorSet, error)
	UpdateSetSubjects(ctx context.Context, setID, mode string, subjects []string) (domain.FactorSet, error)
	SetSetStatus(ctx context.Context, setID, status string) (domain.FactorSet, error)
	DeleteSet(ctx context.Context, setID string, purge bool) error
	GetSet(ctx context.Context, setID string) (domain.FactorSet, []domain.FactorDef, error)
	ListSets(ctx context.Context) ([]domain.FactorSet, error)
	CreateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error)
	UpdateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error)
	SetFactorStatus(ctx context.Context, factorID, status string) (domain.FactorDef, error)
	DeleteFactor(ctx context.Context, factorID string) error
	GetFactor(ctx context.Context, factorID string) (domain.FactorDef, error)
	ListFactors(ctx context.Context, setID, status string) ([]domain.FactorDef, error)
}
```

- [ ] **Step 5：验证并提交** `refactor(factor): reshape FactorMgr around factor sets`。

### Task 13：catalog 生命周期与结果数据集对齐

**Files:**
- Create: `modules/factor/internal/catalog/{service.go,reconcile.go,locks.go,artifacts.go}` 及测试
- Depends: `storageio.Metadata`（本 Task 定义，Task 14 实现真实 RPC）

```go
// modules/factor/internal/storageio/metadata.go
type Metadata interface {
	GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error)
	ListColumns(ctx context.Context, spaceID, datasetID string) ([]ColumnInfo, error)
	CreateResultDataset(ctx context.Context, spec ResultDatasetSpec) error // 幂等
	UpsertColumns(ctx context.Context, spaceID, datasetID string, cols []ColumnInfo) error
	ActivateDataset(ctx context.Context, spaceID, datasetID string) error   // 幂等
	DeleteDataset(ctx context.Context, spaceID, datasetID string) error
}
```

- [ ] **Step 1：写失败测试**（fake Metadata + 真实 SQLite store）：
  - `TestCreateSetCreatesAndActivatesResultDataset`：结果数据集角色为 `factor_result`，DataNode 与源数据集一致，保留期一致，列为源业务列；最终状态 enabled；
  - `TestCreateSetRetryResumesFromPending`：第一次在 Activate 处失败后为 pending，第二次调用完成且不重复创建；
  - `TestCreateSetRejectsFactorResultSource`、`TestCreateSetRejectsMissingFreq`；
  - `TestCreateFactorValidatesAgainstSourceColumnsAndLoadsSource`：源码试加载失败（注入 `SourceChecker` fake）时拒绝；
  - `TestEnableFactorAddsColumnsThenSubmitsRecalc`：调用顺序为 UpsertColumns → 状态 enabled → `RecalcSubmitter.Submit(set, [factor], now−keep, 当前周期)`；
  - `TestDisableFactorKeepsColumns`：停用不调用任何 Metadata 写方法；
  - `TestDeleteFactorRequiresDisabled`；
  - `TestUpdateFactorRequiresDisabled`；
  - `TestReconcileSetAddsNewSourceColumns`：源数据集新增列后结果数据集补列；
  - `TestLifecycleOpsSerializeWithPeriodLock`：持有 `Locks.Lock(setID)` 时生命周期操作阻塞。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/catalog/ -count=1`。
- [ ] **Step 3：实现。**
  - `locks.go`：每个 set 一把 `sync.Mutex`，供 pipeline、recalc、catalog 共享：`func (l *Locks) Lock(setID string) (unlock func())`。
  - `artifacts.go`：把源码物化到 `<factors_dir>/<name>/<source_hash>.py`（存在则跳过，写临时文件后 rename）。
  - `reconcile.go`：启动时与每 5 分钟对 enabled 因子集执行 `ReconcileSet`。
  - 状态变更后通知 trigger 刷新过滤主题：`type Notifier interface{ SetsChanged() }`。
- [ ] **Step 4：验证并提交** `feat(factor): manage factor set lifecycle and result dataset columns`。

---

## 阶段 4：实时周期流水线

### Task 14：storageio 封装 Storage RPC

**Files:**
- Rewrite: `modules/factor/internal/storageio/`（删除 View 读取、dataset_cache、manifest、receipt 相关文件；新建 `client.go`、`read.go`、`write.go`、`marker.go`、`metadata.go`、`commit.go` 及测试）

- [ ] **Step 1：写失败测试**（fake `PrimaryStoreClientProxy`、`MetadataClientProxy`）：
  - `TestReadWindowPagesUntilExhausted`：多页 `after_key` 正确拼接，按 subject 分组，组内按 `(data_time, series_tag)` 升序；
  - `TestReadWindowRequestsAllSelectorsWithoutSeriesTag`；
  - `TestWriteRowsEncodesNullFields`：`Fields["x"]=nil` 编码为显式 null 值；
  - `TestCommitIDIsDeterministic`：字段 map 遍历顺序不同，commit_id 相同；
  - `TestTransientErrorsWrapErrInfra`：`Unavailable`、`DeadlineExceeded` 包装为 `ErrInfra`；参数错误不包装；
  - `TestReportComputedBuildsDeterministicEventID`；
  - `TestComputedExistsNotFoundIsFalse`。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/storageio/ -count=1`。
- [ ] **Step 3：实现**接口约定中的 `storageio.Store` 与 `Metadata`。`commit.go`：

```go
func CommitID(setID string, periodTime int64, rows []ResultRow) string
// sha256(setID | periodTime | 按 (subject, data_time, series_tag) 排序后，每行字段名升序的 name=value 规范化串)
```

- [ ] **Step 4：验证并提交** `refactor(factor): slim storageio to dataset reads, factor writes and markers`。

### Task 15：Python worker 批量协议与 pyexec

**Files:**
- Modify: `modules/factor/pyworker/worker.py`、`modules/factor/pyworker/test_worker.py`（若测试文件名不同，以 `ls modules/factor/pyworker` 为准）
- Rewrite: `modules/factor/internal/pyexec/`（替代 `internal/engine`，新建 `executor.go`、`protocol.go` 及测试）

- [ ] **Step 1：写 Python 失败测试：**
  - `test_batch_runs_multiple_factors_on_shared_frame`：一个 frame 加两个因子，各自返回结果，按 `factor_id` 区分；
  - `test_slices_frame_per_factor_lookback`：lookback=2 的因子只看到最后 2 个 data_time；
  - `test_projects_only_input_columns`：因子 df 只含身份列加 `input_columns`；
  - `test_factor_error_isolated`：一个因子抛异常，另一个仍成功；
  - `test_timeseries_context_has_subject_and_period_times`；
  - `test_cross_section_rejects_unknown_subject_output`；
  - `test_rejects_legacy_fields`：请求中出现 `task_id`、`binding_id`、`config_snapshot_id` 时报协议错误。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && python3 -m pytest pyworker -q`。
- [ ] **Step 3：修改 worker.py。** 保留 `slice_batch_frame`、输出列严格校验、模块缓存（键为 `source_hash`）；删除单因子模式和旧字段；context 按设计 §11 组装，`period_times` 取请求中 `period_times_by_factor[factor_id]`。
- [ ] **Step 4：写 Go 失败测试**（启动真实 worker 子进程，测试因子写在 `testdata/`）：
  - `TestExecutorRunsBatch`；
  - `TestExecutorTimeoutReplacesWorker`：超时后 `Busy()` 恢复为 0，下一次调用成功；
  - `TestExecutorCrashReturnsCallError`；
  - `TestExecutorLimitsConcurrencyToWorkers`。
- [ ] **Step 5：实现** `pyexec.Executor`，基于 `packages/pyruntime` 进程池。
- [ ] **Step 6：验证**：`cd modules/factor && python3 -m pytest pyworker -q && go test ./internal/pyexec/ -count=1`。
- [ ] **Step 7：提交** `refactor(factor): batch multiple factors over one shared frame`。

### Task 16：pipeline 规划、读取与计算

**Files:**
- Create: `modules/factor/internal/pipeline/{runner.go,plan.go,load.go,compute.go}` 及测试

- [ ] **Step 1：写失败测试**（fake Store、fake Executor、真实 periodclock）：
  - `TestBuildLivePlanIntersectsUniverseAndScope`：`expected = universe ∩ scope`，`available = expected − upstream failed`；
  - `TestLoadUsesMaxLookbackRange`：lookback 为 5 与 20 时读取范围是 `[T−19m, T+1m)`；
  - `TestLoadBatchesSubjectsAndLimitsConcurrency`：250 个标的、批大小 100 → 3 次读取，并发不超过 `read_workers`；
  - `TestLoadBatchFailureMarksSubjectsFailed`：一批重试 2 次仍失败，该批标的进入 `FailedSubjects`，其余继续；
  - `TestLoadAllBatchesFailReturnsErrInfra`；
  - `TestComputeTimeSeriesOneCallPerSubject`：3 个标的、4 个时序因子 → 3 次 `Exec`，每次 4 个 `FactorCall`；
  - `TestComputeCrossSectionSkipsWhenPartialUniverseNotAllowed`：状态为 skipped，不调用 `Exec`；
  - `TestComputeCrossSectionRunsOnAvailableWhenAllowed`；
  - `TestComputeBudgetExceededCancelsRemaining`：预算耗尽后未完成调用记失败，返回已完成部分。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/pipeline/ -count=1`。
- [ ] **Step 3：实现。** `runner.go` 编排阶段并记录 `StageDurations`（`plan/load/compute/assemble/write/report`）；`compute.go` 用 errgroup 投递调用，并发上限等于 Executor worker 数。
- [ ] **Step 4：验证并提交** `feat(factor): plan, load and compute factor periods on a shared window`。

### Task 17：pipeline 组装、写入与上报

**Files:**
- Create: `modules/factor/internal/pipeline/{assemble.go,write.go,report.go}` 及测试

- [ ] **Step 1：写失败测试：**
  - `TestAssembleKeepsOnlyTargetRange`：实时只保留 `data_time == T`；
  - `TestAssembleCarriesSourceColumnsOnSourceTagRows`：源行的全部业务列被携带；
  - `TestAssembleDerivedTagWritesFactorColumnsOnly`：因子输出新 tag `binance-okx` 时，该行只有因子列；
  - `TestAssembleFailedFactorWritesNull`：失败因子在源 tag 行上写 null；
  - `TestAssembleNaNAndInfBecomeNull`；
  - `TestAssembleRecalcWritesOnlySelectedFactors`：补算模式下未选因子的列不出现在行中；
  - `TestWriteBatchesRows`：2500 行、批大小 1000 → 3 次写入，commit_id 各不相同且确定；
  - `TestWriteRetriesThenReturnsErrInfra`；
  - `TestOutcomeStatusDegradedWhenAnyFactorFailed`；
  - `TestReportOnlyInLiveMode`：补算不调用 `ReportComputed`；
  - `TestNoEnabledFactorsStillReportsComplete`：`factors` 为空，状态 complete。
- [ ] **Step 2：运行确认失败**，实现后通过：`cd modules/factor && go test ./internal/pipeline/ -count=1`。
- [ ] **Step 3：提交** `feat(factor): assemble, write and report factor result rows`。

### Task 18：trigger 消费与因子集 lane

**Files:**
- Rewrite: `modules/factor/internal/trigger/`（删除 `dataset_rows.go`、`period_barrier.go`、`subject_runs.go`、`view_ready_runner.go`、`eventconsumer/`；新建 `consumer.go`、`lanes.go`、`handler.go` 及测试）

- [ ] **Step 1：写失败测试**（fake JetStream 消息、fake Runner）：
  - `TestHandlerAcksWhenNoEnabledSet`；
  - `TestHandlerAcksWhenComputedAlreadyExists`：预检命中时不调用 Runner；
  - `TestHandlerNaksOnErrInfra`：Runner 返回包装 `ErrInfra` 的错误时 NAK，延迟取配置 `nak_delay`（默认 10s）；
  - `TestHandlerAcksDegradedOutcome`；
  - `TestLanesSerialPerSetParallelAcrossSets`：同一 set 的两个周期严格串行，不同 set 并行；
  - `TestLaneHoldsSetLockDuringRun`：执行期间 `Locks.Lock(setID)` 被持有；
  - `TestFilterSubjectsFollowEnabledSets`：`SetsChanged()` 后消费过滤主题更新为 `moox.event.storage.collector.period.completed.v1.<space>.<dataset>` 列表；
  - `TestHandlerTerminatesMalformedEvent`：无法解码的消息 `Term`，不无限重投。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/trigger/ -count=1`。
- [ ] **Step 3：实现。** 使用 `packages/eventbus`（或现有 JetStream 封装，`rg -n "func NewDurableConsumer|FilterSubjects" packages` 定位）。durable 名称为 `trigger.ConsumerName`，`DeliverNew`，`AckWait` 不低于 `period_budget_max + 1m`；处理中定期 `InProgress()` 续期。
- [ ] **Step 4：验证并提交** `feat(factor): trigger factor sets from collector period completion`。

### Task 19：单进程装配、配置、健康检查与指标

**Files:**
- Rewrite: `modules/factor/internal/bootstrap/`（删除 `bootstrap.go` 旧 `Initialize`、`control.go`、`engine_runtime.go`、`role_config.go`、`catalog.go`、`subject.go`、`view_ready.go`、`recalc.go`；新建 `bootstrap.go`、`config.go`、`health.go` 及测试）
- Create: `modules/factor/internal/observability/{metrics.go,metrics_test.go}`
- Rewrite: `modules/factor/cmd/server/main.go`、`modules/factor/config/app.yaml`（含 `trpc_go.yaml` 的服务名，以现有文件为准）
- Delete: `modules/factor/cmd/engine/`、`modules/factor/config/engine-*.yaml`

- [ ] **Step 1：写失败测试：**
  - `TestLoadConfigDefaults`：未配置时取设计 §17 默认值；
  - `TestLoadConfigRejectsInvalidBudget`：`period_budget_min > period_budget_max` 报错；
  - `TestHealthReportsStorageWriteLatch`：连续 3 次写失败后健康为 unhealthy，一次成功后恢复；
  - `TestHealthReportsStuckLane`：单周期运行超过 `2 × period_budget_max` 判定卡住；
  - `TestMetricsRegistered`：设计 §18 中的全部指标名已注册。
- [ ] **Step 2：运行确认失败**，然后实现：装配顺序为 SQLite → Storage 客户端 → Python 池 → catalog（含启动对齐） → pipeline → recalc（恢复 running 任务为 accepted 并继续） → trigger → tRPC 服务。关闭时按逆序。
- [ ] **Step 3：验证**：

```bash
cd modules/factor && go build ./... && go vet ./... && go test ./internal/bootstrap/ ./internal/observability/ -count=1
```

- [ ] **Step 4：提交** `refactor(factor): run control and computation in a single moox-factor process`。

---

## 阶段 5：补算与 CLI

### Task 20：recalc 服务与 CLI

**Files:**
- Create: `modules/factor/internal/recalc/{service.go,worker.go}` 及测试
- Rewrite: `modules/factor/cmd/cli/main.go`

- [ ] **Step 1：写失败测试：**
  - `TestSubmitIsIdempotentByRequestID`；
  - `TestRunSplitsIntoChunks`：5000 个周期、`chunk_periods=2000` → 3 块，`c_progress_time` 依次推进；
  - `TestChunkReadRangeIncludesLookback`：块读取起点为 `块起点 − (N−1)·freq`，N 取所选因子的最大 lookback；
  - `TestRecalcUsesSelectedFactorsOnly`；
  - `TestCancelStopsBeforeNextChunk`；
  - `TestChunkHoldsSetLock`：块执行期间持有锁，块与块之间释放；
  - `TestRecalcDoesNotReportMarker`；
  - `TestRunningJobsResumeAfterRestart`：`running` 任务重启后从 `c_progress_time` 继续。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/recalc/ -count=1`。
- [ ] **Step 3：实现。** 每块构造 `pipeline.Plan{Mode: ModeRecalc, TargetStart: 块起点, TargetEnd: 块终点}` 调用同一 Runner；时序因子在块内每标的一次调用，截面因子按周期逐个调用（Runner 内部按 Mode 分派）。
- [ ] **Step 4：CLI 子命令**（直连 SQLite 与 Storage，或经 RPC，与现有 CLI 风格一致）：
  - `init`：初始化 SQLite；
  - `import --set <set_id> --file <factor.py> --factor-id ... --outputs ... --inputs ... --lookback ... [--params JSON]`；
  - `import-catalog --dir modules/factor/factors --set <set_id>`：读取 `catalog.json` 批量导入（disabled）；
  - `recalc --set ... --start ... --end ... [--factor ...] [--subject ...]`：提交异步任务；
  - `run-once --set ... --period <RFC3339>`：同步补算一个周期并打印各因子状态；
  - `status`：调用 `GetStatus`。
  输出遵循仓库 CLI 规范：结果写 stdout，诊断写 stderr，失败返回非零退出码。
- [ ] **Step 5：验证**：`cd modules/factor && go test ./internal/recalc/ ./cmd/... -count=1`。
- [ ] **Step 6：提交** `feat(factor): add chunked recalc jobs and rewrite factor cli`。

---

## 阶段 6：下游与运维切换

### Task 21：删除旧因子代码与 Merge 模块

**Files:**
- Delete: `modules/factor/internal/{taskrunner,inputcache,catalogsync,registry,engine}/`，以及旧 `trigger`、`bootstrap`、`domain`、`store`、`storageio` 中未被新实现覆盖的残留文件
- Delete: `modules/merge/`；修改 `go.work` 删除 `./modules/merge`
- Delete: `scripts/deploy/factor-engine/`；修改 `scripts/build/build-storage-linux.sh` 删除 `moox-factor-engine`、`moox-merge`
- Modify: `scripts/check/check-module-boundaries.sh`、`scripts/check/check-package-boundaries.sh` 中对已删除包的规则

- [ ] **Step 1：** 删除目录后全仓搜索残留：

```bash
rg -n "moox-merge|modules/merge|factor-engine|factor_engine|cmd/engine|inputcache|catalogsync|taskrunner|MergePeriodCompleted|mdataset_|merged_factor|FactorBinding|ListBindings" --glob '!docs/**' --glob '!*.pb.go'
```

  预期：无结果（`docs/` 在 Task 25 处理）。
- [ ] **Step 2：验证**：

```bash
go work sync
cd modules/factor && go build ./... && go vet ./... && go test ./... -count=1
bash scripts/check/check-module-boundaries.sh
bash scripts/check/check-package-boundaries.sh
```

- [ ] **Step 3：** 统计规模，目标为非测试代码 ≤ 6000 行：

```bash
find modules/factor -name '*.go' ! -name '*_test.go' ! -path '*/factorgen/*' | xargs wc -l | tail -1
```

- [ ] **Step 4：提交** `refactor(factor): remove legacy factor engine and merge module`。

### Task 22：Strategy 适配

**Files:**
- Modify: `modules/strategy/internal/trigger/processor.go`、`modules/strategy/internal/storageio/{rpc.go,view.go}`、`modules/strategy/internal/factorio/client.go`、`modules/strategy/internal/compiler/{types.go,verify_dependencies.go}`、`modules/strategy/internal/bootstrap/bootstrap.go` 及测试
- Modify: `modules/strategy/docs/coin-selection-runtime.md`

- [ ] **Step 1：写失败测试：**
  - `TestProcessorReadsFactorStatesFromViewDataReady`：`factors[]` 中目标因子 degraded 时，按现有降级策略处理；
  - `TestProcessorIgnoresNonFactorCompletionKinds`：只处理 `factor_period.computed`；
  - `TestVerifyDependenciesUsesFactorSets`：策略声明的因子必须存在于目标结果 View 对应因子集，且为 enabled；
  - 删除所有 merge completion kind 与 binding 相关用例。
- [ ] **Step 2：运行确认失败**：`cd modules/strategy && go test ./... -count=1`。
- [ ] **Step 3：实现。** `factorio.Client` 改为调用 `ListFactorSets` 与 `ListFactors`，返回 `FactorDescriptor{FactorID, SetID, Outputs, Status, ResultDatasetID}`，删除 `BindingDescriptor`。
- [ ] **Step 4：验证并提交** `refactor(strategy): consume factor set results and per-factor period states`。

### Task 23：Web 因子页面

**Files:**
- Modify: `web/src/api/factor/{index.ts,types.ts}`
- Modify: `web/src/views/factor/definitions/{index.vue,factor-form.ts}`、`web/src/views/factor/tasks/index.vue`、`web/src/views/factor/results/index.vue`
- Create: `web/src/views/factor/sets/index.vue`
- Delete: `web/src/views/factor/{bindings,construct,datasets}/`
- Modify: 因子路由文件（`rg -n "factor/bindings|factor/construct|factor/datasets" web/src/router` 定位）、`web/src/lang/modules/{zhCN,enUS}.ts`
- Modify: `web/src/views/data/shared/{metadata-utils.ts,module-attribution.ts}`、`web/src/views/data/datasets/default-view.ts` 及测试：删除 `mdataset_`、`merged_factor`；`factor_result` 数据集不再由前端创建默认 View
- Modify: `web/src/views/factor/__tests__/factor-contract.spec.ts`

- [ ] **Step 1：改契约测试**：API 方法名与请求字段对齐新 `FactorMgr`（`createFactorSet`、`listFactorSets`、`createFactor`、`setFactorStatus`、`recalcFactors` 等），删除 binding 断言。
- [ ] **Step 2：运行确认失败**：`cd web && npm run test -- src/views/factor src/views/data`。
- [ ] **Step 3：实现页面：**
  - 因子集：列表（源数据集、频率、结果数据集、状态、最近周期、延迟），新建与停用；
  - 因子定义：按因子集筛选，新建与编辑（仅 disabled 可编辑），启用与停用，删除；
  - 补算任务：提交、进度、取消；
  - 结果：选择因子集后查询结果 View（View ID 由结果数据集 ID 推导，或通过 Metadata 查询该数据集的默认 View）。
- [ ] **Step 4：验证**：`cd web && npm run test && npm run build`（或仓库实际使用的类型检查命令）。
- [ ] **Step 5：提交** `refactor(web): manage factor sets, definitions and recalc jobs`。

### Task 24：CLI setup、Admin 部署与 EventBus 凭证

**Files:**
- Modify: `modules/cli/internal/setup/config/config.go`、`config_test.go`、`moox.toml.example`、`moox.toml`
- Modify: `modules/cli/internal/command/setup_factors.go` 及测试、`modules/cli/internal/command/setup_init.go`
- Modify: `modules/cli/internal/setup/deploy/deploy.go`、`deploy_test.go`、`modules/cli/internal/command/collector.go`（`rg -n "mdataset|merge" modules/cli` 定位）
- Modify: `modules/admin/internal/service/sysdeploy/{defaults.go,dao.go}` 及测试、`config/setup/service-deployments.yaml`、`modules/admin/cmd/cli/eventbus_credentials.go` 及测试
- Modify: `modules/cli/README.md`

- [ ] **Step 1：** `moox.toml` 因子配置改为：

```toml
[factors]
enabled = true
source_dir = "./modules/factor/factors"

[[factors.sets]]
space_id = "crypto"
source_dataset_id = "dataset_binance_kline_1m"
freq = "1m"
subject_mode = "all"

[[factors.items]]
factor_id = "Bias"
source_dataset_id = "dataset_binance_kline_1m"
freq = "1m"
factor_type = "timeseries"
file = "Bias.py"
input_columns = ["close"]
outputs = ["bias_20"]
params_json = '{"window":20}'
lookback_periods = 20
status = "enabled"
```

  `items` 通过 `(source_dataset_id, freq)` 关联因子集；删除 `source_view_id`、`subject_mode`（移到 set）。
- [ ] **Step 2：写失败测试：**
  - `TestLoadSetupFactorsRequiresMatchingSet`：item 引用不存在的 set 时报错；
  - `TestSetupFactorsCreatesSetBeforeFactors`：调用顺序为 `CreateFactorSet` → `CreateFactor` → `SetFactorStatus(enabled)`；
  - `TestSetupFactorsIdempotent`：已存在时跳过或更新，不报错；
  - `TestServiceDeploymentsHaveNoMergeOrFactorEngine`；
  - `TestEventbusCredentialsFactorConsumer`：factor 凭证允许订阅 `moox.event.storage.collector.period.completed.v1.>`，consumer 为 `factor_collector_period_v1`；删除 merge 凭证。
- [ ] **Step 3：运行确认失败**，然后实现。`storagePrimaryGatewayCallers` 删除 `"merge"`。
- [ ] **Step 4：验证**：

```bash
cd modules/cli && go test ./... -count=1
cd ../admin && go test ./... -count=1
```

- [ ] **Step 5：提交** `refactor(setup): configure factor sets and drop merge deployment`。

---

## 阶段 7：端到端验收

### Task 25：端到端测试脚本

**Files:**
- Create: `scripts/test/e2e/test-factor-period-pipeline.sh`
- Create: `modules/factor/test/period_pipeline_e2e_test.go`（进程内：真实 SQLite + Storage 测试服务 + 内嵌 NATS + 真实 Python worker；以 `modules/storage/internal/service/e2e` 现有夹具为参考）

- [ ] **Step 1：进程内 E2E 用例：**
  - `TestE2ELivePeriodWritesResultAndViewReady`：写 3 个标的 × 2 个 tag × 25 根 1m K 线 → 上报 `CollectorPeriodCompleted(T)` → 等待结果 View `ViewDataReady(kind=factor_period.computed, period_time=T)` → 读结果 View：每个 `(subject, tag)` 在 T 有一行，携带列与源一致，`bias_20` 等因子列非空；
  - `TestE2EDuplicateDeliveryIsIdempotent`：同一事件投递两次，结果行与完成标记各一份；
  - `TestE2EDegradedUpstreamSubject`：上游 failed 一个标的，结果中该标的无因子行，事件 `failed_subjects` 包含它；
  - `TestE2EEnableFactorBackfills`：启用新因子后，补算任务 succeeded，保留期窗口内历史行出现新列值；
  - `TestE2EDisableFactorKeepsColumnStopsWriting`：停用后下一周期该列为 null，View 列仍存在，View 未重建（active index 不变）。
- [ ] **Step 2：运行**：`cd modules/factor && go test ./test/ -run E2E -count=1 -timeout 10m`。
- [ ] **Step 3：shell 脚本**按 `scripts/test/e2e/test-series-tag-e2e.sh` 的风格编排本机多进程：启动 NATS、Storage、moox-factor，注入 K 线和完成事件，断言结果 View 行数与事件。
- [ ] **Step 4：提交** `test(factor): cover live period pipeline end to end`。

### Task 26：真实链路验收（本机或测试环境）

- [ ] **Step 1：** 部署顺序：Storage → Collector → moox-factor → Strategy；确认 `moox-merge`、`moox-factor-engine` 进程不存在。
- [ ] **Step 2：** `moox-factor-cli import-catalog --set fset_binance_kline_1m` 导入 12 个 XBX 因子，按需启用。
- [ ] **Step 3：** 连续观察 30 个 1m 周期，记录：`factor_period_total{status="complete"}` 占比、`factor_period_duration_seconds` 的 P95（目标 < 15s）、`factor_period_lag_seconds` 的 P95（目标 < 30s）、Strategy 收到 `ViewDataReady` 的周期数与因子周期数一致。
- [ ] **Step 4：** 重启 moox-factor，确认积压周期被依次处理且无重复完成标记。
- [ ] **Step 5：** 把验收记录写入 `docs/superpowers/verification/2026-10-xx-factor-period-pipeline.md`，`xx` 为实际验收日期。

---

## 阶段 8：文档

### Task 27：文档与技能同步

**Files:**
- Rewrite: `docs/因子计算模块设计.md`（以设计文档为蓝本，描述已实现状态）、`modules/factor/README.md`
- Delete: `docs/因子视图驱动计算设计.md`、`modules/factor/docs/realtime-verification.md`
- Modify: `docs/SUMMARY.md`（删除旧执行计划链接 `2026-07-11-factor-runtime-refactor.md`，增加本计划链接）
- Modify: `docs/superpowers/specs/2026-09-13-factor-dataset-view-refactor-design.md`：顶部加“状态：已被 2026-10-04 因子数据集周期流水线设计替代”
- Modify: `skills/moox/SKILL.md`、`skills/moox/references/{view-catchup.md,cli-operations.md}`、`modules/cli/README.md`、`modules/storage/README.md` 中的 merge、binding、mdataset 描述
- Modify: `docs/运维/MooX-EventBus运维.md`、`docs/运维/MooX指标监控.md` 中的 consumer 与指标名

- [ ] **Step 1：** 更新上述文档。
- [ ] **Step 2：** 残留扫描：

```bash
rg -n "moox-merge|factor-engine|mdataset_|ViewSourcePeriodReady|ViewFactorPeriodReady|DatasetPeriodCollected|UpsertBinding" docs skills modules --glob '!docs/superpowers/**'
```

  预期：无结果。
- [ ] **Step 3：** `git diff --check`。
- [ ] **Step 4：提交** `docs(factor): document dataset-driven factor period pipeline`。

---

## 全局验收清单

- [ ] `modules/factor` 非测试 Go 代码 ≤ 6000 行，SQLite 只有 3 张表。
- [ ] 全仓 `go build ./...`、`go vet ./...`、`go test ./...` 通过（各 module 分别执行，或执行 `make test`）。
- [ ] `python3 -m pytest modules/factor/pyworker -q` 通过。
- [ ] `cd web && npm run test` 通过。
- [ ] 所有 `modules/*/schema/*.sql` 逐个载入空 SQLite 成功；`git diff --check` 无告警。
- [ ] `bash scripts/check/verify-event-contracts.sh`、`check-module-boundaries.sh`、`check-package-boundaries.sh`、`check-gofmt.sh` 通过。
- [ ] Task 25 进程内 E2E 全部通过；Task 26 真实链路验收记录已归档。
- [ ] 设计 §2 的 D1–D15 均可在代码中找到对应实现或约束测试：D12 对应 `TestDisableFactorKeepsColumns` 与 `TestE2EDisableFactorKeepsColumnStopsWriting`；D13 对应 `TestForSpaceStockCNUnsupported`；D14 对应补算请求中没有扩展参数；D15 对应 `TestLanesSerialPerSetParallelAcrossSets`。

## 自检记录

- 设计覆盖：设计 §5（单 DataNode、标记顺序）对应 Task 3、5；§7 对应 Task 9；§8 对应 Task 1、3、12；§10 对应 Task 16–18；§11 对应 Task 15；§12 对应 Task 4、13；§13 对应 Task 13；§14 对应 Task 20；§15 的失败语义分布在 Task 14、16–18 的测试中；§18 对应 Task 19；§19 对应 Task 7、8、22–24。
- 类型一致性：`storageio.Store`、`pipeline.Plan/Outcome`、`pyexec.Executor`、`catalog.Locks`、`trigger.ConsumerName` 均在“接口约定”或首次出现的 Task 中定义，后续 Task 只引用这些名称。
- TODO 范围：A 股日历、环形缓冲增量读取、`committed_positions` 简化不在本计划内，见设计 §20。
