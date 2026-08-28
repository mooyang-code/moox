# MooX 选币策略执行框架详细实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将当前手工 `run-once + quantity` Strategy V1 重构为由 View Ready 事件驱动、支持多标的横截面选币、多组件和多调仓轮次，并最终向 Trade 输出目标权重的 Strategy V2。

**Architecture:** Factor 先拆为不可变算法定义、参数实例和数据绑定；Strategy 通过 `UniverseSnapshot + View Ready` 构造不可变 `StrategyInputSnapshot`，使用标准选币 evaluator 生成各 RebalanceSlot 权重，再由 `WeightMerger` 合并为 FULL `StrategyWeightTarget`。Trade 首次幂等接受权重事件并保存 PENDING 请求，异步 resolver 冻结账户权益和参考价格，持久化 raw quantity 换算快照并继续复用现有 quantity TargetExecutor。

**Tech Stack:** Go 1.24、tRPC-Go、Protobuf、SQLite/GORM、Python 3.12、pandas、`packages/pyruntime`、NATS JetStream、Storage DataView、Trade LogicalAccount。

---

## 实施约束

- 项目未上线，不保留旧 schema、旧 RPC 或旧 wire contract 的兼容分支。
- 每个可独立编译的 MooX 工作包提交一次，下一工作包开始前定向测试必须通过。Task 2+3 和 Task 13+14 分别作为一个原子工作包；Task 1 位于独立、非 Git 的 OpenSpec 目录，不创建 MooX commit。
- Python 因子只独立重写公式，不复制下载目录源码、注释或配置结构。
- `bias`、`cci` 直接采用新公式，不创建 `xbx_` 别名。
- `circulating_supply` 不进入框架协议。`circulating_mcap` 只声明普通输入列；数据不存在时 Binding 保持 disabled。
- Strategy 到 Trade 的跨模块契约只出现 `target_weight`，`quantity` 只存在于 Trade 冻结换算后的内部目标。
- 运行时不自动创建 FactorInstance；Strategy 发布/装配阶段完成实例解析。
- 默认 readiness policy 为 `strict`，缺失输入时跳过当期运行。

## 目标文件图

```text
modules/factor/
  internal/domain/{factor_def.go,factor_instance.go,binding.go}
  internal/store/{factor_def.go,factor_instance.go,binding.go,database.go}
  internal/registry/{service.go,metadata_sync.go,binding_contract.go,source.go}
  internal/taskrunner/{builder.go,factor_gate.go,stale.go}
  proto/factor.proto
  schema/factor.sql

modules/strategy/
  internal/domain/{strategy.go,runner.go,universe.go,snapshot.go,run.go,weight.go}
  internal/config/{spec.go,compile.go,validate.go}
  internal/factorio/client.go
  internal/storageio/{client.go,market_frame.go}
  internal/universe/resolver.go
  internal/trigger/{consumer.go,readiness.go,runner.go}
  internal/selection/evaluator.go
  internal/weight/{slot.go,merger.go}
  internal/runtime/{runner.go,scheduler.go}
  internal/backtest/{iterator.go,runner.go,execution.go}
  internal/store/{snapshots.go,readiness.go,runs.go,slots.go,weights.go,inbox.go}
  pyworker/selection.py
  proto/strategy.proto
  schema/strategy.sql

modules/trade/
  internal/application/weight/{service.go,pricing.go}
  internal/infra/store/weight_target.go
  internal/eventconsumer/target.go
  proto/trade_service.proto
  schema/logical_account.sql

packages/
  quantdecimal/{decimal.go,decimal_test.go}
  storagepb/storage_events.proto
  tradeeventpb/trade_events.proto
  events/{registry.go,validation.go}

examples/factors/timeseries/
examples/setup/default/
docs/
```

## 依赖顺序

```text
Task 1 -> Task 2 -> Task 3 -> Task 4
Task 3 -> Task 5
Task 5 -> Task 6 -> Task 7 -> Task 8 -> Task 9
Task 6 + Task 9 -> Task 10 -> Task 11 -> Task 12
Task 12 -> Task 13 -> Task 14
Task 4 + Task 11 + Task 14 -> Task 15 -> Task 16 -> Task 17 -> Task 18
```

### Task 1: 冻结协议设计和 OpenSpec 边界

**Files:**
- Create: `../openspec/changes/factor-instances-and-weighted-strategy-execution/proposal.md`
- Create: `../openspec/changes/factor-instances-and-weighted-strategy-execution/design.md`
- Create: `../openspec/changes/factor-instances-and-weighted-strategy-execution/tasks.md`
- Create: `../openspec/changes/factor-instances-and-weighted-strategy-execution/specs/factor-instance-runtime/spec.md`
- Create: `../openspec/changes/factor-instances-and-weighted-strategy-execution/specs/strategy-readiness-and-snapshot/spec.md`
- Create: `../openspec/changes/factor-instances-and-weighted-strategy-execution/specs/weighted-selection-execution/spec.md`
- Create: `../openspec/changes/factor-instances-and-weighted-strategy-execution/specs/trade-weight-resolution/spec.md`
- Reference: `../openspec/changes/redesign-quant-data-protocols/design.md`
- Reference: `docs/选币策略执行框架设计.md`

- [ ] **Step 1: 创建独立 OpenSpec change**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code
openspec new change factor-instances-and-weighted-strategy-execution
```

Expected: change 目录包含 schema 要求的 artifact 路径。

- [ ] **Step 2: 固定跨模块协议决策**

在 proposal/design 中写明以下不可变边界：

```text
FactorDef -> FactorInstance -> FactorBinding
Factor completion event = FactorPeriodComputed
Strategy output = target_weight
Trade internal execution target = quantity
Universe scope != execution routing
```

同时注明旧 `redesign-quant-data-protocols` change 只与 Factor metadata、Factor value 和 QueryFrame 重叠，不承载完整 Strategy 实现。

- [ ] **Step 3: 校验 OpenSpec**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code
openspec validate factor-instances-and-weighted-strategy-execution --strict --no-interactive
```

Expected: PASS，无缺失 artifact 或未解析 requirement。

- [ ] **Step 4: 确认 change 已达到可实施状态**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code
openspec status --change factor-instances-and-weighted-strategy-execution
```

Expected: 所有 apply 所需 artifacts 均为 done。OpenSpec 根目录不属于 MooX Git 仓库，本任务只作为实施门禁，不执行 `git commit`；MooX 设计文档和本计划在实施开始前已经提交。

### Task 2: 拆分 FactorDef、FactorInstance 和 FactorBinding 协议

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Modify: `modules/factor/proto/factorgen/validation.go`
- Create: `modules/factor/proto/factorgen/contract_test.go`
- Modify: `modules/factor/internal/domain/factor.go`
- Modify: `modules/factor/internal/domain/binding.go`
- Create: `modules/factor/internal/domain/factor_instance.go`
- Modify: `modules/factor/schema/factor.sql`
- Modify: `modules/factor/schema/schema_test.go`
- Modify: `modules/factor/internal/store/database.go`

- [ ] **Step 1: 写失败的 proto descriptor 测试**

```go
func TestFactorContractSeparatesDefinitionAndInstance(t *testing.T) {
    def := (&factorpb.FactorDef{}).ProtoReflect().Descriptor()
    require.NotNil(t, def.Fields().ByName("factor_def_id"))
    require.NotNil(t, def.Fields().ByName("revision"))
    require.Nil(t, def.Fields().ByName("params_json"))

    instance := (&factorpb.FactorInstance{}).ProtoReflect().Descriptor()
    require.NotNil(t, instance.Fields().ByName("factor_instance_id"))
    require.NotNil(t, instance.Fields().ByName("params_json"))
    require.NotNil(t, instance.Fields().ByName("lookback_periods"))
}
```

- [ ] **Step 2: 运行测试并确认失败**

Run:

```bash
cd modules/factor/proto
go test ./... -run TestFactorContractSeparatesDefinitionAndInstance -count=1
```

Expected: FAIL，`FactorInstance` 或新字段尚不存在。

- [ ] **Step 3: 重写 Factor proto**

核心消息使用以下字段，不保留 deprecated/reserved 兼容项：

```protobuf
message FactorDef {
  string factor_def_id = 1;
  uint32 revision = 2;
  string display_name = 3;
  string source_code = 4;
  string source_hash = 5;
  repeated string input_columns = 6;
  repeated string outputs = 7;
  string param_schema_json = 8;
  LookbackSpec lookback_spec = 9;
  string state = 10;
}

message FactorInstance {
  string factor_instance_id = 1;
  string factor_def_id = 2;
  uint32 factor_def_revision = 3;
  string params_json = 4;
  string params_hash = 5;
  int32 lookback_periods = 6;
  string state = 7;
}

message FactorBinding {
  string binding_id = 1;
  string factor_instance_id = 2;
  string space_id = 3;
  string source_view_id = 4;
  string frequency = 5;
  string subject_mode = 6;
  string subjects_json = 7;
  string result_dataset_id = 8;
  string result_view_id = 9;
  string state = 10;
}
```

RPC 分为 Def CRUD、Instance CRUD/SetState、Binding Create/SetState/Delete 和 `RecalcFactorInstance`。

- [ ] **Step 4: 写 schema 精确结构测试**

```go
func TestFactorSchemaHasInstanceTable(t *testing.T) {
    db := openSchema(t)
    requireColumns(t, db, "t_factor_instances", []string{
        "c_factor_instance_id", "c_factor_def_id", "c_factor_def_revision",
        "c_params_json", "c_params_hash", "c_lookback_periods", "c_state",
        "c_ctime", "c_mtime",
    })
    requireForeignKey(t, db, "t_factor_bindings", "c_factor_instance_id", "t_factor_instances")
    requireUniqueIndex(t, db, "t_factor_bindings", []string{
        "c_factor_instance_id", "c_space_id", "c_source_view_id", "c_frequency",
    })
}
```

- [ ] **Step 5: 重写 domain 和 schema**

领域类型固定为：

```go
type FactorDef struct {
    ID string
    Revision uint32
    DisplayName string
    SourceCode string
    SourceHash string
    InputColumns []string
    Outputs []string
    ParamSchemaJSON json.RawMessage
    Lookback LookbackSpec
    State FactorDefState
}

type FactorInstance struct {
    ID string
    FactorDefID string
    FactorDefRevision uint32
    ParamsJSON json.RawMessage
    ParamsHash string
    LookbackPeriods int
    State FactorInstanceState
}
```

`t_factor_defs` 使用 `(c_factor_def_id, c_revision)` 复合主键；Def `state` 仅表示制品发布生命周期，不控制执行。`t_factor_instances` 对 `(c_factor_def_id, c_factor_def_revision, c_params_hash)` 建唯一约束；Binding 外键改为 instance ID，并对 `(factor_instance_id, space_id, source_view_id, frequency)` 建自然唯一约束。本项目尚未投产，本次初始 schema rewrite 直接丢弃旧 FactorDef、Binding 和结果数据；新 `bias`、`cci` 以新公式创建 revision 1。此后发布 revision 不可修改，公式变化必须创建 revision 2。由于 store/runtime 仍依赖旧字段，Task 2 只完成同一工作包的协议与 schema 修改，不单独提交。

- [ ] **Step 6: 生成代码并进入同一原子工作包的 Task 3**

Run:

```bash
make proto
(cd modules/factor/proto/factorgen && go test ./... -run TestFactorContractSeparatesDefinitionAndInstance -count=1)
```

Expected: descriptor contract PASS。此时不要运行完整 Factor store 测试，也不要提交；Task 3 必须立即补齐所有 store、registry、RPC 和 runtime 调用点，再统一验证、提交。

### Task 3: 实现 FactorInstance 参数、生命周期和运行绑定

**Files:**
- Split: `modules/factor/internal/store/factor.go`
- Create: `modules/factor/internal/store/factor_def.go`
- Create: `modules/factor/internal/store/factor_instance.go`
- Modify: `modules/factor/internal/store/binding.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/registry/service.go`
- Modify: `modules/factor/internal/registry/binding_contract.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/registry/source.go`
- Modify: `modules/factor/internal/taskrunner/builder.go`
- Modify: `modules/factor/internal/taskrunner/factor_gate.go`
- Modify: `modules/factor/internal/engine/types.go`
- Modify: `modules/factor/pyworker/worker.py`

- [ ] **Step 1: 写参数规范化和 ID 测试**

```go
func TestBuildInstanceAppliesDefaultsAndBuildsReadableID(t *testing.T) {
    def := factorDefFixture("bias", 1, `{"window":{"type":"integer","minimum":1,"default":20}}`)
    got, err := BuildInstance(def, json.RawMessage(`{}`))
    require.NoError(t, err)
    require.Equal(t, "bias_r1_w20", got.ID)
    require.JSONEq(t, `{"window":20}`, string(got.ParamsJSON))
    require.Equal(t, 20, got.LookbackPeriods)
}
```

- [ ] **Step 2: 写 lookback 规则测试**

```go
func TestAffineLookback(t *testing.T) {
    got, err := ResolveLookback(LookbackSpec{
        Kind: "affine", Param: "window", Multiplier: 3, Offset: -2,
    }, map[string]any{"window": 10})
    require.NoError(t, err)
    require.Equal(t, 28, got)
}
```

- [ ] **Step 3: 实现 Instance builder 和 repositories**

实现以下接口：

```go
type FactorDefRepository interface {
    Create(context.Context, domain.FactorDef) error
    Get(context.Context, string, uint32) (domain.FactorDef, error)
    List(context.Context, FactorDefFilter) ([]domain.FactorDef, error)
}

type FactorInstanceRepository interface {
    Create(context.Context, domain.FactorInstance) error
    Get(context.Context, string) (domain.FactorInstance, error)
    FindByParams(context.Context, string, uint32, string) (domain.FactorInstance, error)
    SetState(context.Context, string, domain.FactorInstanceState) error
}
```

参数处理顺序固定为 schema default、严格类型校验、规范 JSON、SHA256、lookback、可读 ID。调用者不得提交 `params_hash` 或 `lookback_periods`。

- [ ] **Step 4: 切换 registry 和 runtime 身份**

`FactorTask` 同时携带：

```go
type FactorSpec struct {
    FactorDefID string
    FactorDefRevision uint32
    FactorInstanceID string
    SourceHash string
    InputColumns []string
    Outputs []string
    ParamsJSON json.RawMessage
    LookbackPeriods int
}
```

worker 模块缓存 key 使用 `factor_def_id + revision + source_hash`；不同 Instance 复用同一 Python 模块。结果字段、Storage Factor ID、运行 gate 和 stale-task 校验全部使用 FactorInstance ID。

- [ ] **Step 5: 实现生命周期约束**

状态规则：

```text
published FactorDef immutable
Instance disabled -> enabled -> cleanup_pending -> disabled
Binding pending_view -> enabled -> cleanup_pending -> disabled/deleted
enabled Instance + enabled Binding 才可执行
```

禁用 Instance 时清理 manifests 指向的结果值，但保留 Binding 配置；重新启用只恢复未来计算，历史计算必须调用 `RecalcFactorInstance`。

- [ ] **Step 6: 运行定向测试并提交**

Run:

```bash
(cd modules/factor && CGO_ENABLED=1 go test ./internal/store ./internal/registry ./internal/rpc ./internal/taskrunner ./internal/engine -count=1)
(cd modules/factor && python3 -m pytest pyworker -q)
```

Expected: PASS。

```bash
git add modules/factor
git commit -m "refactor(factor): split definitions and execute instances"
```

### Task 4: 独立重写并验证 12 个时序因子

**Files:**
- Modify: `examples/factors/timeseries/bias.py`
- Modify: `examples/factors/timeseries/cci.py`
- Create: `examples/factors/timeseries/bias_q.py`
- Create: `examples/factors/timeseries/circulating_mcap.py`
- Create: `examples/factors/timeseries/min_max.py`
- Create: `examples/factors/timeseries/quote_volume_mean.py`
- Create: `examples/factors/timeseries/quote_volume_mean_q.py`
- Create: `examples/factors/timeseries/volume_mean_q.py`
- Create: `examples/factors/timeseries/zf_abs_mean.py`
- Create: `examples/factors/timeseries/zf_mean_q.py`
- Create: `examples/factors/timeseries/zf_std.py`
- Create: `examples/factors/timeseries/zscore_abs_mean_q.py`
- Create: `examples/factors/tests/test_selection_factors.py`
- Create: `examples/factors/manifests/bias.yaml`
- Create: `examples/factors/manifests/bias_q.yaml`
- Create: `examples/factors/manifests/cci.yaml`
- Create: `examples/factors/manifests/circulating_mcap.yaml`
- Create: `examples/factors/manifests/min_max.yaml`
- Create: `examples/factors/manifests/quote_volume_mean.yaml`
- Create: `examples/factors/manifests/quote_volume_mean_q.yaml`
- Create: `examples/factors/manifests/volume_mean_q.yaml`
- Create: `examples/factors/manifests/zf_abs_mean.yaml`
- Create: `examples/factors/manifests/zf_mean_q.yaml`
- Create: `examples/factors/manifests/zf_std.yaml`
- Create: `examples/factors/manifests/zscore_abs_mean_q.yaml`

- [ ] **Step 1: 写公式 golden tests**

每个测试使用固定 8 到 16 行输入，直接用 pandas 基础运算构造 expected，再调用 MooX `compute`：

```python
def test_bias_uses_price_over_rolling_mean():
    frame = frame_with(close=[1.0, 2.0, 3.0, 4.0])
    got = load_factor("bias").compute(frame.copy(), {"window": 3})
    expected = frame["close"] / frame["close"].rolling(3, min_periods=1).mean()
    pandas.testing.assert_series_equal(got["value"], expected, check_names=False)
```

为 `cci` 增加零 mean-deviation、为 `min_max` 增加零 range、为所有 rank 因子增加 warmup 和 NaN 行用例。

再增加至少两个 `series_tag` 交错排列的 golden case，断言实现先按 series 分组、组内按时间排序，rolling 窗口绝不跨 instrument。

- [ ] **Step 2: 运行测试并确认旧 bias/cci 公式不匹配**

Run:

```bash
python3 -m pytest examples/factors/tests -q
```

Expected: FAIL，缺少新文件且 bias/cci golden 输出不匹配。

- [ ] **Step 3: 实现统一入口**

所有文件只导出：

```python
def compute(df, params):
    window = int(params["window"])
    result = df[["data_time", "series_tag"]].copy()
    result["value"] = calculated_series
    return result
```

`circulating_mcap` 使用空参数对象，输入列为普通的 `circulating_supply` 和 `close`。不在 factor 文件中访问网络或执行补齐。

- [ ] **Step 4: 增加 12 个 FactorDef manifest fixtures**

在 `examples/factors/manifests/` 新增 12 份定义 fixture，供 CLI 装配测试使用。lookback 规则分别为：

```text
n: bias, min_max, quote_volume_mean
2n-1: bias_q, cci, quote_volume_mean_q, volume_mean_q, zf_mean_q
n+1: zf_std
2n: zf_abs_mean
3n-2: zscore_abs_mean_q
1: circulating_mcap
```

每份 fixture 固定 input columns、output `value`、参数 schema 和 lookback spec。
`circulating_mcap` fixture 不创建 enabled Binding，直至普通输入 Field 可用。

- [ ] **Step 5: 运行公式和 worker 测试并提交**

Run:

```bash
python3 -m pytest examples/factors/tests -q
(cd modules/factor && python3 -m pytest pyworker -q)
```

Expected: PASS。

```bash
git add examples/factors
git commit -m "feat(factor): add coin selection factor definitions"
```

### Task 5: 将完成事件身份切换到 FactorInstance

**Files:**
- Modify: `modules/storage/proto/dataset_markers.proto`
- Modify: `packages/storagepb/storage_events.proto`
- Modify: `packages/storagepb/storage_events_test.go`
- Modify: `packages/events/registry.go`
- Modify: `packages/events/validation.go`
- Modify: `packages/events/events_test.go`
- Modify: `packages/events/validation_test.go`
- Modify: `modules/factor/internal/trigger/view_ready_runner.go`
- Modify: `modules/factor/internal/trigger/view_ready_runner_test.go`
- Modify: `modules/factor/internal/storageio/marker.go`
- Modify: `modules/storage/internal/service/datanode/pebble/marker_message.go`
- Modify: `modules/storage/internal/service/view/period_event_apply.go`
- Modify: `modules/storage/internal/service/view/period_event_apply_test.go`

- [ ] **Step 1: 写事件 descriptor 和 validation 失败测试**

```go
func TestFactorBindingPeriodStateUsesInstanceIdentity(t *testing.T) {
    d := (&storagepb.FactorBindingPeriodState{}).ProtoReflect().Descriptor()
    require.NotNil(t, d.Fields().ByName("factor_def_id"))
    require.NotNil(t, d.Fields().ByName("factor_instance_id"))
    require.Nil(t, d.Fields().ByName("factor_id"))
}
```

验证器还必须拒绝空 `binding_id`、空 instance ID、未知 status 和重复 binding ID。

- [ ] **Step 2: 运行测试并确认失败**

Run:

```bash
(cd packages/storagepb && go test ./... -run TestFactorBindingPeriodStateUsesInstanceIdentity -count=1)
```

Expected: FAIL，当前事件仍只有 `factor_id`。

- [ ] **Step 3: 原子修改 Marker 和公共事件**

保持事件名和版本：

```text
storage.dataset.factor_period.computed v1
storage.view.factor_period.ready v1
```

本项目没有线上 wire compatibility，直接将状态项改为：

```protobuf
message FactorBindingPeriodState {
  string binding_id = 1;
  string factor_def_id = 2;
  string factor_instance_id = 3;
  string status = 4;
  repeated string skipped_subjects = 5;
  repeated string failed_subjects = 6;
}
```

Factor 仍是业务判定者，调用 Storage Marker RPC；Storage DataNode 仍是实际 EventBus publisher。不得增加 `FactorPeriodCollected`。

- [ ] **Step 4: 更新稳定 ID 和 View apply**

`FactorPeriodComputed` 的稳定逻辑键保持：

```text
space_id + result_dataset_id + trigger_event_id + period_time
```

payload hash 纳入 factor instance 状态。Storage View 复制新的 identity 字段并发布 `ViewFactorPeriodReady`。

- [ ] **Step 5: 生成、测试并提交**

Run:

```bash
make proto
(cd packages/events && go test ./... -count=1)
(cd packages/storagepb && go test ./... -count=1)
(cd modules/factor && CGO_ENABLED=1 go test ./internal/trigger ./internal/storageio -count=1)
(cd modules/storage && CGO_ENABLED=1 go test ./internal/service/datanode/... ./internal/service/view/... -count=1)
bash scripts/tests/e2e/test-factor-view-ready-e2e.sh
```

Expected: PASS，结果行仍在 `FactorPeriodComputed` 之前进入同一 DataNode outbox。

```bash
git add modules/storage/proto packages/storagepb packages/events modules/factor/internal modules/storage/internal
git commit -m "refactor(events): identify factor instances in period markers"
```

### Task 6: 以可编译的增量方式定义 Strategy V2 协议、领域对象和 schema

**Files:**
- Modify: `modules/strategy/proto/strategy.proto`
- Modify: `modules/strategy/proto/strategygen/validation.go`
- Modify: `modules/strategy/proto/strategy_contract_test.go`
- Split: `modules/strategy/internal/domain/types.go`
- Create: `modules/strategy/internal/domain/strategy.go`
- Create: `modules/strategy/internal/domain/runner.go`
- Create: `modules/strategy/internal/domain/universe.go`
- Create: `modules/strategy/internal/domain/snapshot.go`
- Create: `modules/strategy/internal/domain/run.go`
- Create: `modules/strategy/internal/domain/weight.go`
- Modify: `modules/strategy/schema/strategy.sql`
- Modify: `modules/strategy/schema/schema_test.go`
- Modify: `modules/strategy/internal/store/database.go`

- [ ] **Step 1: 写 Strategy V2 descriptor 测试**

```go
func TestStrategyV2ExposesWeightAndSnapshots(t *testing.T) {
    requireMessage(t, "UniverseSpec")
    requireMessage(t, "StrategyInputSnapshot")
    requireMessage(t, "StrategyRun")
    target := requireMessage(t, "InstrumentWeight")
    requireField(t, target, "target_weight")
    requireNoField(t, target, "quantity")
}
```

新增 V2 descriptor 测试，验证新对象字段和 FULL 权重语义。Task 6 暂时保留 V1 消息和表，仅用于让分阶段 commit 保持可编译；这不是受支持的兼容模式，最终切换和删除在 Task 13+14 原子工作包完成。

- [ ] **Step 2: 运行 descriptor 测试并确认失败**

Run:

```bash
cd modules/strategy/proto
go test ./... -run TestStrategyV2ExposesWeightAndSnapshots -count=1
```

Expected: FAIL，V2 消息尚不存在。

- [ ] **Step 3: 定义核心消息和 RPC**

核心类型：

```protobuf
message InstrumentWeight {
  string instrument_id = 1;
  string target_weight = 2;
}

message StrategyInputSnapshot {
  string snapshot_id = 1;
  string runner_id = 2;
  int64 period_time = 3;
  string frequency = 4;
  string universe_snapshot_id = 5;
  repeated string expected_instruments = 6;
  repeated string included_instruments = 7;
  repeated string ineligible_instruments = 8;
  repeated string missing_instruments = 9;
  repeated string source_event_ids = 10;
  string frame_hash = 11;
  string artifact_uri = 12;
  int64 artifact_size = 13;
  string artifact_encoding = 14;
  string availability_timeline_hash = 15;
}

message StrategyWeightTarget {
  string weight_target_id = 1;
  string runner_id = 2;
  string logical_account_id = 3;
  int64 command_sequence = 4;
  int64 signal_time = 5;
  string input_snapshot_id = 6;
  repeated InstrumentWeight targets = 7;
}
```

RPC 增加 `ValidateRunnerConfig`、`PreviewRunner`、`RunPeriod`、`GetInputSnapshot`、`GetStrategyRun`、`ListStrategyRuns` 和 `GetWeightTarget`。`RunOnceReq.data_json` 改为 preview 专用入口，不再是正式自动运行入口。

- [ ] **Step 4: 建立拆分后的领域对象**

```go
type InstrumentWeight struct {
    InstrumentID string `json:"instrument_id"`
    TargetWeight string `json:"target_weight"`
}

type SlotEvaluation struct {
    ComponentID string `json:"component_id"`
    SlotID string `json:"slot_id"`
    Action Action `json:"action"`
    Weights []InstrumentWeight `json:"weights,omitempty"`
}

type StrategyEvaluation struct {
    DueSlots []SlotEvaluation `json:"due_slots"`
    DebugInfo map[string]any `json:"debug_info,omitempty"`
}

type StrategyRunStatus string

const (
    RunPending StrategyRunStatus = "PENDING"
    RunRunning StrategyRunStatus = "RUNNING"
    RunSucceeded StrategyRunStatus = "SUCCEEDED"
    RunSkippedIncomplete StrategyRunStatus = "SKIPPED_INCOMPLETE_INPUT"
    RunFailed StrategyRunStatus = "FAILED"
    RunSuperseded StrategyRunStatus = "SUPERSEDED"
)
```

本任务不删除 `InstrumentTarget.quantity` 和旧 `Output.Targets`，只新增 V2 类型。Task 13+14 完成 V2 producer/consumer 后再一次性删除旧调用链。

- [ ] **Step 5: 重写 Strategy schema**

在旧表仍可运行的前提下新增以下 V2 表：

```text
t_strategies
t_strategy_runners
t_strategy_runner_dependencies
t_universe_snapshots
t_strategy_period_readiness
t_strategy_input_snapshots
t_strategy_runs
t_strategy_slot_targets
t_strategy_weight_targets
t_strategy_inbox
t_strategy_outbox
```

关键唯一约束：

```text
t_strategy_runs:
  (runner_id, strategy_revision, compiled_dependency_hash, namespace, period_time, recalc_revision)
t_strategy_slot_targets:
  (runner_id, component_id, slot_id)
t_strategy_weight_targets:
  (runner_id, command_sequence)
t_strategy_inbox:
  (consumer_name, message_id)
t_strategy_runners:
  partial unique(logical_account_id) where status = 'ENABLED'
```

数据库启动校验新增表、列和索引，同时暂时接受 V1 表存在。Task 13+14 删除 V1 后恢复最终 schema 的精确校验。必须保留“一 LogicalAccount 最多一个 enabled Runner”的唯一约束。

- [ ] **Step 6: 生成、测试并提交**

Run:

```bash
make proto
(cd modules/strategy/proto/strategygen && go test ./... -count=1)
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/domain ./schema ./internal/store -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/proto modules/strategy/internal/domain modules/strategy/internal/store modules/strategy/schema
git commit -m "refactor(strategy): define snapshot and weight domains"
```

### Task 7: 实现强类型选币配置编译器和 FactorInstance 解析

**Files:**
- Create: `modules/strategy/internal/config/spec.go`
- Create: `modules/strategy/internal/config/validate.go`
- Create: `modules/strategy/internal/config/compile.go`
- Create: `modules/strategy/internal/config/compile_test.go`
- Create: `modules/strategy/internal/factorio/client.go`
- Create: `modules/strategy/internal/factorio/client_test.go`
- Modify: `modules/strategy/internal/registry/service.go`
- Modify: `modules/strategy/internal/registry/service_test.go`

- [ ] **Step 1: 写严格 YAML 和 tuple 拒绝测试**

```go
func TestParseSelectionSpecRejectsUnknownFieldsAndTuples(t *testing.T) {
    _, err := ParseSelectionSpec([]byte(`components: [[bias, true, 20, 1]]`))
    require.Error(t, err)

    _, err = ParseSelectionSpec([]byte("universe:\n  markets: [spot]\n  mystery: true\n"))
    require.Error(t, err)
}
```

- [ ] **Step 2: 定义强类型配置**

```go
type SelectionSpec struct {
    Universe UniverseSpec `yaml:"universe"`
    Input InputSpec `yaml:"input"`
    Readiness ReadinessSpec `yaml:"readiness"`
    WeightOutput WeightOutputSpec `yaml:"weight_output"`
    Components []ComponentSpec `yaml:"components"`
}

type FactorRef struct {
    FactorDefID string `yaml:"factor_def_id"`
    Params map[string]any `yaml:"params"`
}

type SelectionCount struct {
    Mode string `yaml:"mode"`
    Value string `yaml:"value,omitempty"`
    Start string `yaml:"start,omitempty"`
    End string `yaml:"end,omitempty"`
}
```

`SelectionCount.Mode` 仅允许 `count|fraction|rank_range|all|match_long`。Filter 使用显式 `phase/side/value_type/op/value`，不解析 `pct:<0.8` 字符串。

- [ ] **Step 3: 实现 Factor client port**

```go
type InstanceResolver interface {
    ResolveOrCreate(
        context.Context,
        string,
        uint32,
        map[string]any,
    ) (ResolvedFactorInstance, error)
}
```

该接口只在 Strategy 发布/Runner 保存阶段调用。运行阶段只读取 compiled config 中的 `factor_instance_id`、source hash、result View column 和 lookback。

- [ ] **Step 4: 实现 compiled config**

```go
type CompiledFactorRef struct {
    FactorInstanceID string `json:"factor_instance_id"`
    FactorDefID string `json:"factor_def_id"`
    FactorDefRevision uint32 `json:"factor_def_revision"`
    SourceHash string `json:"source_hash"`
    ColumnName string `json:"column_name"`
    LookbackPeriods int `json:"lookback_periods"`
}
```

每个 compiled dependency 还必须固定 `space_id`、primary `dataset_id`、`view_id`、frequency、column 和 lookback；这些字段规范化后共同计算 `compiled_dependency_hash`，供 readiness 和 run key 使用。

编译器必须：

1. 规范化 component weight 和 side weight。
2. 展开每个 offset 为稳定 `slot_id`。
3. 解析所有 FactorRef。
4. 计算最大 `history_periods`。
5. 生成 runner dependencies。
6. 规范 JSON 后计算 `compiled_config_hash`。

- [ ] **Step 5: 测试编译幂等性**

```go
func TestCompileIsIndependentOfMapAndComponentInputOrder(t *testing.T) {
    left := compileFixture(t, specA())
    right := compileFixture(t, semanticallyEquivalentSpecB())
    require.Equal(t, left.Hash, right.Hash)
    require.JSONEq(t, string(left.JSON), string(right.JSON))
}
```

- [ ] **Step 6: 运行测试并提交**

Run:

```bash
(cd modules/strategy && go test ./internal/config ./internal/factorio ./internal/registry -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/internal/config modules/strategy/internal/factorio modules/strategy/internal/registry
git commit -m "feat(strategy): compile typed coin selection configs"
```

### Task 8: 实现 UniverseResolver 和不可变 UniverseSnapshot

**Files:**
- Create: `modules/strategy/internal/universe/resolver.go`
- Create: `modules/strategy/internal/universe/resolver_test.go`
- Create: `modules/strategy/internal/storageio/metadata.go`
- Create: `modules/strategy/internal/storageio/metadata_test.go`
- Create: `modules/strategy/internal/store/snapshots.go`
- Create: `modules/strategy/internal/store/snapshots_test.go`

- [ ] **Step 1: 写 Universe 过滤和稳定排序测试**

```go
func TestResolveUniverseAppliesScopeAndStableOrdering(t *testing.T) {
    subjects := []Subject{
        {ID: "ETH-USDT-SPOT", Exchange: "binance", Market: "spot", QuoteAsset: "USDT", Status: "active"},
        {ID: "BTC-USDT-SPOT", Exchange: "binance", Market: "spot", QuoteAsset: "USDT", Status: "active"},
        {ID: "BTC-USDC-SPOT", Exchange: "binance", Market: "spot", QuoteAsset: "USDC", Status: "active"},
    }
    got := Resolve(specUSDTSpot(), subjects, time.Unix(100, 0))
    require.Equal(t, []string{"BTC-USDT-SPOT", "ETH-USDT-SPOT"}, got.InstrumentIDs)
}
```

- [ ] **Step 2: 实现 Storage Metadata client**

调用 `Metadata.ListSubjects` 分页读取候选 Subject，把 `market` 和 attributes 规范化为 Strategy `Subject`。必须串行分页，检测 `has_more=true` 但空页的异常响应，避免无限循环。

- [ ] **Step 3: 实现 UniverseResolver**

执行顺序固定为：

```text
space -> exchange -> market -> quote asset -> active status
      -> include whitelist -> exclude blacklist -> deterministic sort
```

include 非空时先取 include 交集；exclude 最后应用且优先级更高。
`min_history_periods` 不在元数据阶段猜测。MarketFrameLoader 将历史不足的标的归类为
`ineligible_instruments`，将本应完整但缺少当前周期或必需列的标的归类为
`missing_instruments`；只有后者进入 readiness policy。

- [ ] **Step 4: 持久化 UniverseSnapshot**

```go
type UniverseSnapshot struct {
    ID string
    RunnerID string
    PeriodTime time.Time
    InstrumentIDs []string
    IncludedJSON json.RawMessage
    ExcludedJSON json.RawMessage
    Hash string
    CreatedAt time.Time
}
```

稳定 ID 由 `runner_id + period_time + hash` 生成。同一内容重复解析返回已有快照，不创建重复行。

- [ ] **Step 5: 运行测试并提交**

Run:

```bash
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/universe ./internal/storageio ./internal/store -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/internal/universe modules/strategy/internal/storageio modules/strategy/internal/store
git commit -m "feat(strategy): freeze runner universe snapshots"
```

### Task 9: 实现 StrategyTrigger 和持久化数据齐备检查

**Files:**
- Create: `modules/strategy/internal/trigger/consumer.go`
- Create: `modules/strategy/internal/trigger/consumer_test.go`
- Create: `modules/strategy/internal/trigger/readiness.go`
- Create: `modules/strategy/internal/trigger/readiness_test.go`
- Create: `modules/strategy/internal/trigger/runner.go`
- Create: `modules/strategy/internal/store/readiness.go`
- Create: `modules/strategy/internal/store/readiness_test.go`
- Create: `modules/strategy/internal/store/inbox.go`
- Create: `modules/strategy/internal/store/inbox_test.go`
- Modify: `modules/strategy/internal/store/runner_queries.go`

- [ ] **Step 1: 写 required views 收敛测试**

```go
func TestDataReadyCheckWaitsForEveryRequiredView(t *testing.T) {
    state := State{RequiredViews: []string{"bars", "factors"}}
    state.Apply(readyEvent("bars", 100))
    require.False(t, state.Ready())
    state.Apply(readyEvent("factors", 100))
    require.True(t, state.Ready())
}
```

- [ ] **Step 2: 写重复和迟到事件测试**

覆盖：

```text
same message_id -> ACK without duplicate run
same view/period same logical payload but different event_id -> idempotent provenance append
same view/period different terminal payload -> conflict and metric
older period after newer period -> inbox recorded, no new run
deadline reached under strict -> SKIPPED_INCOMPLETE_INPUT
deadline reached under exclude -> continue with missing list
```

- [ ] **Step 3: 实现 durable inbox 和 readiness repository**

```go
func (r *Repository) ApplyReadyEvent(
    ctx context.Context,
    consumerName string,
    messageID string,
    event ReadyEvent,
) (ReadinessDecision, error)
```

同一 SQLite 事务只写 inbox 并 upsert readiness state。key 固定为 `(runner_id, strategy_revision, compiled_dependency_hash, frequency, period_time)`。重复事件不得再次产生 ready transition。事件 handler 不解析 UniverseSnapshot，也不创建 StrategyRun；Task 15 的异步 coordinator 在 ready transition 后完成 universe 冻结并创建 Run。

- [ ] **Step 4: 实现事件 consumer**

消费：

```text
storage.view.source_period.ready
storage.view.factor_period.ready
```

按 `space_id + view_id + frequency` 查询 enabled Runner dependencies。consumer 只做解码、验证和持久化，不在 JetStream handler 内同步运行 Python。

- [ ] **Step 5: 实现 deadline sweeper**

每分钟扫描未终态 readiness：

```go
func (r *Runner) FinalizeExpired(ctx context.Context, now time.Time) error
```

strict 写 terminal skipped decision；exclude 写 ready decision 和缺失集合。异步 coordinator 将 terminal decision 转成对应 StrategyRun 审计记录。所有决定保留 readiness 行。

- [ ] **Step 6: 运行测试并提交**

Run:

```bash
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/trigger ./internal/store -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/internal/trigger modules/strategy/internal/store
git commit -m "feat(strategy): trigger runs from durable view readiness"
```

### Task 10: 构造 MarketFrame 和 StrategyInputSnapshot

**Files:**
- Create: `modules/strategy/internal/storageio/client.go`
- Create: `modules/strategy/internal/storageio/client_test.go`
- Create: `modules/strategy/internal/storageio/market_frame.go`
- Create: `modules/strategy/internal/storageio/market_frame_test.go`
- Modify: `modules/strategy/internal/store/snapshots.go`
- Modify: `modules/strategy/internal/store/snapshots_test.go`
- Create: `modules/strategy/internal/snapshotstore/store.go`
- Create: `modules/strategy/internal/snapshotstore/store_test.go`
- Modify: `modules/strategy/internal/engine/engine.go`
- Modify: `modules/strategy/internal/engine/engine_test.go`

- [ ] **Step 1: 写多标的同时间输入测试**

```go
func TestValidateMarketFrameAllowsSameTimeForDifferentInstruments(t *testing.T) {
    frame := MarketFrame{Rows: []Row{
        {Time: ts(10), InstrumentID: "BTC-USDT-SPOT"},
        {Time: ts(10), InstrumentID: "ETH-USDT-SPOT"},
        {Time: ts(11), InstrumentID: "BTC-USDT-SPOT"},
        {Time: ts(11), InstrumentID: "ETH-USDT-SPOT"},
    }}
    require.NoError(t, frame.Validate(2))
}
```

同时增加重复 `(time,instrument_id)`、乱序、未来行、列缺失和未知 instrument 的拒绝测试。历史不足但小于 `min_history_periods` 的新标的必须进入 ineligible 集合，不能让 strict 策略跳过整个市场周期。

- [ ] **Step 2: 运行测试并确认旧校验失败**

Run:

```bash
(cd modules/strategy && go test ./internal/engine ./internal/storageio -run MarketFrame -count=1)
```

Expected: FAIL，当前 `validateHistoryWindow` 要求每行时间严格递增。

- [ ] **Step 3: 实现 Storage DataView client**

使用 `DataView.QueryTimeSeriesRows`，请求包含：

```text
space_id
view_id
selectors: every instrument + space_id + primary dataset_id + frequency
time_range: [period_time-(history_periods-1)*frequency, period_time+1ns)
column_names: compiled dependencies
sorts: data_time ASC, subject_id ASC
page: serial pagination
```

`dataset_id` 来自 compiled primary dependency，不允许运行时猜测。每页校验 `served_indexed_to >= period_time` 和 `complete=true`。结束点使用 `period_time+1ns`，避免要求下一周期数据才把本周期判为 complete。分页完成后再整体校验 distinct period 数量，不能把总行数当成 history periods。

- [ ] **Step 4: 实现规范 MarketFrame**

```go
type MarketFrame struct {
    Periods []time.Time
    Instruments []string
    Columns []string
    Rows []MarketRow
}

type MarketRow struct {
    Time time.Time
    InstrumentID string
    Values map[string]domain.TypedValue
}
```

规范化排序为 `(time ASC, instrument_id ASC)`。frame hash 使用规范列顺序、typed values 和 rows 计算；禁止对任意 map 的遍历顺序做 hash。

- [ ] **Step 5: 冻结 StrategyInputSnapshot**

Snapshot 保存 manifest、point-in-time availability timeline 和不可变制品引用：

```json
{
  "kind": "content_addressed_ndjson_gzip",
  "artifact_uri": "snapshots/sha256/ab/cd/<frame_hash>.ndjson.gz",
  "artifact_size": 12345,
  "artifact_encoding": "ndjson+gzip",
  "history_start": 0,
  "history_end": 0,
  "column_names": [],
  "served_indexed_to": "",
  "frame_hash": "",
  "availability_timeline_hash": ""
}
```

`snapshotstore` 使用临时文件加原子 rename 写入规范 NDJSON+gzip，以 `frame_hash` 内容寻址，并配置 root 与 retention。SQLite 不重复存 Frame，但必须保存 URI、hash、size、encoding 和 retention 信息。普通 DataView query 描述不能替代不可变制品。

历史横截面需要从 View Ready/period state 构造每个 period 的 subject membership 和列可用性 mask；MarketFrame 和 snapshot 都保存该 timeline。preview 可以内联输入，但必须通过同一个 `MarketFrame.Validate`、availability 和 hash 路径。

- [ ] **Step 6: 运行测试并提交**

Run:

```bash
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/storageio ./internal/engine ./internal/store -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/internal/storageio modules/strategy/internal/snapshotstore modules/strategy/internal/engine modules/strategy/internal/store
git commit -m "feat(strategy): build immutable market frame snapshots"
```

### Task 11: 实现标准横截面选币 evaluator

**Files:**
- Create: `modules/strategy/pyworker/selection.py`
- Create: `modules/strategy/pyworker/test_selection.py`
- Create: `modules/strategy/pyworker/requirements.txt`
- Modify: `modules/strategy/pyworker/worker.py`
- Modify: `modules/strategy/pyworker/test_worker.py`
- Create: `modules/strategy/internal/selection/evaluator.go`
- Create: `modules/strategy/internal/selection/evaluator_test.go`
- Modify: `modules/strategy/internal/engine/engine.go`
- Modify: `modules/strategy/pysdk/moox_strategy/types.py`
- Modify: `modules/strategy/pysdk/moox_strategy/validate.py`
- Modify: `modules/strategy/pysdk/tests/test_validate.py`

- [ ] **Step 1: 写横截面排名和稳定并列测试**

```python
def test_metric_rank_uses_min_ties_and_selection_breaks_ties_by_instrument_id():
    frame = market_frame([
        ("2026-08-29T10:00:00Z", "ETH-USDT-SPOT", 10),
        ("2026-08-29T10:00:00Z", "BTC-USDT-SPOT", 10),
    ])
    result = evaluate(frame, single_factor_spec(ascending=True, count=1))
    assert result["weights"] == [
        {"instrument_id": "BTC-USDT-SPOT", "target_weight": "1"},
    ]
```

- [ ] **Step 2: 写完整流水线顺序测试**

构造一个前置过滤会改变排名集合、后置过滤会删除入选结果的 fixture，验证执行顺序严格为：

```text
point-in-time availability -> derived/metric rank -> score -> pre-filter
-> deterministic select -> post-filter -> side weight
```

覆盖 long/short 独立规则、count、fraction、rank_range、all、match_long 和空候选集合。

- [ ] **Step 3: 实现内置 derived operators**

`selection.py` 注册固定 operator：

```python
DERIVED_OPERATORS = {
    "cross_section_rank": cross_section_rank,
    "cross_section_rank_diff": cross_section_rank_diff,
    "cross_section_rank_bias": cross_section_rank_bias,
    "cross_section_rank_pct_change": cross_section_rank_pct_change,
}
```

排名先按 `availability_timeline` 取每个 `time` 的适格 universe，并使用 pandas `rank(method="min")` 等价语义；diff/rolling mean/pct change 再按 instrument 分组并按 time 排序。最终 selection 才以 `(score, instrument_id)` 稳定决胜并截取精确数量。禁止动态 import 用户提供的 operator 名。

- [ ] **Step 4: 实现 filter、score 和 select**

Filter 只接受：

```text
value_type = value | rank | percentile
op = lt | lte | gt | gte | eq
```

Score 为未执行 pre-filter 的适格 universe 上各 metric 横截面排名的加权和。随后才执行 pre-filter；多头选择 score 小端，空头选择 score 大端。返回每个方向内的规范十进制等权结果，不访问账户权益和价格。

- [ ] **Step 5: 接入 worker kind 路由**

```python
if request["strategy_kind"] == "coin_selection":
    return selection.evaluate(dataframe, request["compiled_config"])
return run_custom_strategy(request, dataframe)
```

Go `selection.Evaluator` 复用 pyruntime worker pool，输入 compiled config 和 MarketFrame，输出 `StrategyEvaluation`。Go 继续做第二次严格校验。

- [ ] **Step 6: 将 Python SDK 输出改为权重**

SDK 只接受：

```python
{
    "due_slots": [{
        "component_id": "spot-long",
        "slot_id": "slot-10h",
        "action": "hold" | "rebalance" | "clear",
        "weights": [{
            "instrument_id": "BTC-USDT-SPOT",
            "target_weight": "1",
        }],
    }],
    "debug_info": {},
}
```

`weights` 是 Slot 内部归一权重，不包含 component/slot share。拒绝 `quantity`、重复 instrument、非规范 decimal 和 Spot 负权重。gross/net 只计算并记录，不在 Strategy 配置中设置账户风险上限。

- [ ] **Step 7: 运行 Python 和 Go 测试并提交**

Run:

```bash
(cd modules/strategy && python3 -m pytest pyworker -q)
(cd modules/strategy && python3 -m pytest pysdk/tests -q)
(cd modules/strategy && go test ./internal/selection ./internal/engine -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/pyworker modules/strategy/pysdk modules/strategy/internal/selection modules/strategy/internal/engine
git commit -m "feat(strategy): evaluate deterministic cross-sectional selections"
```

### Task 12: 实现 RebalanceSlot 和 WeightMerger

**Files:**
- Modify: `go.work`
- Modify: `modules/strategy/go.mod`
- Create: `packages/quantdecimal/go.mod`
- Create: `packages/quantdecimal/decimal.go`
- Create: `packages/quantdecimal/decimal_test.go`
- Create: `modules/strategy/internal/weight/slot.go`
- Create: `modules/strategy/internal/weight/slot_test.go`
- Create: `modules/strategy/internal/weight/merger.go`
- Create: `modules/strategy/internal/weight/merger_test.go`
- Create: `modules/strategy/internal/store/slots.go`
- Create: `modules/strategy/internal/store/slots_test.go`

- [ ] **Step 1: 写调度命中测试**

```go
func TestSlotDueUsesExplicitAnchorEveryAndOffset(t *testing.T) {
    slot := Slot{Anchor: ts(0), Every: 24 * time.Hour, Offset: 9 * time.Hour}
    require.True(t, slot.Due(ts(33*time.Hour)))
    require.False(t, slot.Due(ts(34*time.Hour)))
}
```

覆盖非整小时 offset、多个 offset、边界时点和 UTC 规范化。

- [ ] **Step 2: 写 WeightMerger 测试**

```go
func TestMergerAppliesComponentAndStaticSlotShareExactlyOnce(t *testing.T) {
    got, err := Merge([]SlotTarget{
        slotTarget("a", "BTC-USDT-SPOT", "1", componentWeight("0.8"), slotShare("0.5")),
        slotTarget("b", "BTC-USDT-SPOT", "1", componentWeight("0.8"), slotShare("0.5")),
    })
    require.NoError(t, err)
    require.Equal(t, "0.8", got.Targets[0].TargetWeight)
}
```

覆盖未初始化 Slot、`hold`、显式 `clear`、正负净额、尘埃删除、gross/net 统计、Spot 负权重和规范排序。Slot 不设置 `expires_at`；static slot share 固定为 `1/configured slot count`，不能按 active slot 数重新归一。

- [ ] **Step 3: 实现 Slot 领域规则**

稳定 ID：

```text
slot_id = sha256(runner_id + component_id + every + offset + anchor)
```

SlotTarget 保存 `source_run_id`、Slot 内部归一 weights、`effective_at` 和 hash，不保存过期时间。`hold` 不修改 Slot；`clear` 保存显式空目标并保留审计记录。

- [ ] **Step 4: 建立共享精确 decimal 并实现 WeightMerger**

将 `modules/trade/internal/domain/shared/decimal.go` 的精确 `big.Rat` 语义独立整理到
`packages/quantdecimal`，新 Strategy 和 Trade weight resolver 统一使用该包，不使用
float64，并将新 module 加入 `go.work`。合并顺序固定为 slot ID、instrument ID；输出
decimal string 去掉无意义尾零。

- [ ] **Step 5: 持久化和恢复 SlotTarget**

```go
func (r *Repository) ApplyDueSlotsAndMerge(
    ctx context.Context,
    evaluations []domain.SlotEvaluation,
) (domain.StrategyWeightTarget, error)
```

实现内部 transaction-aware 方法，供 Task 13 与 StrategyRun、sequence 和 outbox 放在同一事务提交。

- [ ] **Step 6: 运行测试并提交**

Run:

```bash
(cd packages/quantdecimal && go test ./... -count=1)
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/weight ./internal/store -count=1)
```

Expected: PASS。

```bash
git add go.work packages/quantdecimal modules/strategy/internal/weight modules/strategy/internal/store
git commit -m "feat(strategy): merge persistent rebalance slot weights"
```

### Task 13: 原子提交 StrategyRun、权重目标和 outbox

**Files:**
- Replace: `modules/strategy/internal/store/results.go`
- Create: `modules/strategy/internal/store/runs.go`
- Create: `modules/strategy/internal/store/weights.go`
- Create: `modules/strategy/internal/store/run_commit_test.go`
- Modify: `modules/strategy/internal/domain/outbox.go`
- Modify: `modules/strategy/internal/outbox/publisher.go`
- Modify: `modules/strategy/internal/outbox/publisher_test.go`
- Modify: `modules/strategy/internal/outbox/relay.go`
- Modify: `modules/strategy/internal/rpc/service.go`
- Modify: `packages/tradeeventpb/trade_events.proto`
- Modify: `packages/events/registry.go`
- Modify: `packages/events/validation.go`
- Modify: `packages/events/events_test.go`
- Modify: `packages/events/validation_test.go`

- [ ] **Step 1: 写原子提交和幂等测试**

```go
func TestCommitRunPersistsSlotWeightSequenceAndOutboxAtomically(t *testing.T) {
    repo := newRepository(t)
    result, err := repo.CommitRun(ctx, successfulRebalanceFixture())
    require.NoError(t, err)
    require.EqualValues(t, 1, result.CommandSequence)
    require.Len(t, listSlotTargets(t, repo), 1)
    require.Len(t, listWeightTargets(t, repo), 1)
    require.Len(t, listOutbox(t, repo), 1)
}
```

增加 transaction 故障注入，验证任一步失败时五类记录都不提交。

- [ ] **Step 2: 固定运行幂等键和 input hash**

```go
type CommitRunRequest struct {
    Run domain.StrategyRun
    InputHash string
    Evaluation domain.StrategyEvaluation
}
```

逻辑键固定为 `(runner_id, strategy_revision, compiled_dependency_hash, namespace, period_time, recalc_revision)`；自动运行使用 revision 0，人工 `RecalcStrategy` 分配稳定递增 revision。input hash 包含 config、universe、frame、availability 和 Factor revision/hash，不包含传输 event ID。相同逻辑键、相同 hash 返回已有 Run；相同逻辑键、不同 hash 返回 conflict。旧 period 或低 sequence 不得覆盖当前 FULL 权重。

- [ ] **Step 3: 实现 hold 和 rebalance 事务语义**

```text
for every due slot in one transaction:
  hold      -> retain previous slot target
  rebalance -> replace this slot target
  clear     -> save an explicit empty slot target

if every due slot is hold:
  save successful run without sequence/outbox
otherwise:
  merge every configured slot using static shares
  save full weight target
  increment sequence once
  write one outbox event
```

- [ ] **Step 4: 构造权重事件**

outbox payload 只包含：

```text
weight_target_id
runner_id
logical_account_id
command_sequence
signal_time
input_snapshot_id
instrument_id + target_weight
```

先增加 `LogicalAccountWeightTargetRequested`、`InstrumentWeight`、事件 registry 和 validator，使 Strategy publisher 只依赖已生成的真实 wire type。Event ID 等于 outbox message ID。发布成功但删除失败时允许重复投递，由 Trade 按 weight target ID 幂等吸收。旧 quantity event 暂时保留到 Task 14 consumer 完成。

- [ ] **Step 5: 运行 Strategy 定向测试并继续 Task 14**

Run:

```bash
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/store ./internal/outbox ./internal/rpc -count=1)
```

Expected: Strategy 侧 PASS。不要提交；Task 13+14 是一个原子跨模块工作包，只有 Trade 已能消费权重并且 V1 wire 被删除后才能提交。

### Task 14: 将 Strategy 到 Trade 的协议改为权重并冻结换算

**Files:**
- Modify: `modules/trade/go.mod`
- Modify: `packages/tradeeventpb/trade_events.proto`
- Modify: `packages/events/registry.go`
- Modify: `packages/events/validation.go`
- Modify: `packages/events/events_test.go`
- Modify: `modules/trade/proto/trade_service.proto`
- Modify: `modules/trade/proto/tradegen/validation.go`
- Modify: `modules/trade/schema/logical_account.sql`
- Modify: `modules/trade/internal/application/equity/service.go`
- Modify: `modules/trade/internal/application/equity/service_test.go`
- Modify: `modules/trade/internal/infra/store/equity_point.go`
- Modify: `modules/trade/internal/infra/store/equity_point_test.go`
- Create: `modules/trade/internal/infra/store/weight_target.go`
- Create: `modules/trade/internal/infra/store/weight_target_test.go`
- Create: `modules/trade/internal/application/weight/service.go`
- Create: `modules/trade/internal/application/weight/service_test.go`
- Create: `modules/trade/internal/application/weight/pricing.go`
- Modify: `modules/trade/internal/infra/store/logical_account.go`
- Modify: `modules/trade/internal/infra/store/logical_account_test.go`
- Modify: `modules/trade/internal/application/logicalaccount/service.go`
- Modify: `modules/trade/internal/application/logicalaccount/service_test.go`
- Modify: `modules/trade/internal/eventconsumer/target.go`
- Modify: `modules/trade/internal/eventconsumer/target_test.go`
- Modify: `modules/trade/internal/application/target/executor.go`
- Modify: `modules/trade/internal/application/target/executor_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/strategy/internal/action/service.go`
- Modify: `modules/strategy/internal/action/service_test.go`
- Modify: `modules/strategy/internal/rpc/frontend_service.go`
- Modify: `modules/strategy/internal/rpc/frontend_service_test.go`
- Modify: `modules/strategy/proto/strategy.proto`
- Modify: `modules/strategy/schema/strategy.sql`

- [ ] **Step 1: 写 wire contract 测试**

```go
func TestTradeEventCarriesWeightNotQuantity(t *testing.T) {
    d := (&tradeeventpb.InstrumentWeight{}).ProtoReflect().Descriptor()
    require.NotNil(t, d.Fields().ByName("target_weight"))
    require.Nil(t, d.Fields().ByName("quantity"))
}
```

事件名改为 `trade.weight_target.requested`，payload 为 `LogicalAccountWeightTargetRequested`。旧 `trade.target.requested` 和 quantity payload 直接删除。

- [ ] **Step 2: 写冻结估值幂等测试**

```go
func TestAcceptWeightTargetReusesFirstValuationOnReplay(t *testing.T) {
    service := fixture(t, equity("1000"), price("BTC", "100"))
    first := accept(t, service, weightTarget("target-1", "BTC", "0.2"))
    service.SetEquity("2000")
    service.SetPrice("BTC", "200")
    replay := accept(t, service, weightTarget("target-1", "BTC", "0.2"))
    require.Equal(t, first.QuantityTargets, replay.QuantityTargets)
    require.Equal(t, first.Valuation, replay.Valuation)
}

func TestOlderResolutionCannotOverwriteNewerSequence(t *testing.T) {
    old := acceptPending(t, service, weightTarget("target-1", sequence(1)))
    acceptPending(t, service, weightTarget("target-2", sequence(2)))
    resolve(t, service, old)
    require.Equal(t, "SUPERSEDED", status(t, "target-1"))
    require.Equal(t, "target-2", currentWeightTargetID(t))
}
```

- [ ] **Step 3: 增加权重请求和估值快照表**

```text
t_logical_account_weight_targets
  weight_target_id, logical_account_id, runner_id, sequence,
  weights_json, status, accepted_at

t_logical_account_weight_resolutions
  weight_target_id, frozen_equity, equity_sources_json, equity_as_of,
  reference_prices_json, raw_quantity_targets_json, priced_at,
  status, resolution_hash
```

consumer 先把权重请求以 `PENDING` 保存并 ACK。异步 resolver 冻结估值后，以 LogicalAccount 当前最新 `weight_target_id/sequence` 做 CAS；只有仍为最新的请求才能写 `t_logical_account_targets`，旧请求标记 `SUPERSEDED`。权重请求是审计事实，raw quantity 目标继续供现有 TargetExecutor 使用。

- [ ] **Step 4: 实现冻结换算**

```go
type Service struct {
    Store Store
    Equity EquitySource
    Prices PriceSource
    Instruments InstrumentSource
}

func (s *Service) Accept(
    context.Context,
    domain.LogicalAccountWeightTarget,
) (domain.WeightResolution, bool, error)
```

扩展 `equity.Service` 和 `equity_point` repository，提供按 LogicalAccount 获取新鲜权益的内部 API，返回 `capital_source_id`、equity、settlement asset、`source_time` 和 freshness 状态。一个 capital source 只允许一个 authoritative point；多个 execution members 只能引用它，不能各自重复贡献权益。

换算规则：

```text
notional = frozen equity * target weight
raw base quantity = notional / reference price
```

权重分母只能使用按 `capital_source_id` 去重后的新鲜 authoritative equity；available funds 仅用于容量检查。resolver 不应用 step、minimum quantity 或 contract value，也不提前创建 blocked target。TargetExecutor 选出实际 execution member 后，才复用 `baseQuantityRules` 做 venue-specific 量化、minimum 和容量判断。Spot 负权重直接拒绝；gross、杠杆和保证金限制由 LogicalAccount 的 Trade risk policy 校验。

- [ ] **Step 5: 将 LogicalAccount 从单市场改为能力集合**

保持一个 LogicalAccount 一个 owner Runner。删除 LogicalAccount 自身的 `market_type` 字段和同市场成员约束，成员必须共享 execution mode 和 settlement asset。目标中的每个 instrument 必须能被某个 enabled、ready member 执行。

`TradingAccount.market_type` 保留，用于声明成员能力；同时新增必填 `capital_source_id`。LogicalAccount readiness 收集所有 enabled member 支持的 instrument 集合；Spot 和 Perpetual 可以共存，但不同 settlement asset 或不同 execution mode 仍被拒绝。

多个 execution member 可以共享同一个 capital source，但 equity service 对每个 source 只能返回一份带 `source_time` 的权威快照，聚合时只计一次。相同 source 出现冲突 equity 或过期快照时拒绝执行。LogicalAccount proto/schema 同时增加 Trade 拥有的 gross/leverage policy 字段。

- [ ] **Step 6: 改造 event consumer**

consumer 解码权重事件后调用 `weight.Service.AcceptPending`；PENDING 权重请求幂等保存成功即可 ACK。异步 resolver 对暂时缺少权益/价格执行 RETRY，对业务非法请求记 FAILED，对被新 sequence 超过的请求记 SUPERSEDED。Task 14 同时删除 Strategy V1 action/frontend/results 调用链、旧 quantity event 和过渡表，恢复最终 schema 精确校验。

- [ ] **Step 7: 生成、测试并提交**

Run:

```bash
make proto
(cd packages/events && go test ./... -count=1)
(cd modules/trade/proto/tradegen && go test ./... -count=1)
(cd modules/trade && CGO_ENABLED=1 go test -gcflags=all=-l -ldflags=-s=false ./internal/application/weight ./internal/application/equity ./internal/application/target ./internal/eventconsumer ./internal/infra/store ./internal/bootstrap -count=1)
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/action ./internal/rpc ./internal/store ./internal/outbox -count=1)
```

Expected: PASS。

```bash
git add packages/tradeeventpb packages/events modules/strategy modules/trade
git commit -m "feat(strategy): deliver weighted targets to trade"
```

### Task 15: 接入 Strategy 自动运行、恢复和健康检查

**Files:**
- Modify: `modules/strategy/internal/bootstrap/config.go`
- Modify: `modules/strategy/internal/bootstrap/config_test.go`
- Modify: `modules/strategy/internal/bootstrap/bootstrap.go`
- Modify: `modules/strategy/internal/bootstrap/health_test.go`
- Create: `modules/strategy/internal/runtime/runner.go`
- Create: `modules/strategy/internal/runtime/runner_test.go`
- Create: `modules/strategy/internal/runtime/scheduler.go`
- Create: `modules/strategy/internal/runtime/scheduler_test.go`
- Modify: `modules/strategy/config/app.yaml`
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`
- Modify: `examples/setup/default/service-deployments.yaml`

- [ ] **Step 1: 写 bootstrap dependency 测试**

```go
func TestInitializeRequiresStorageAndReadyConsumerForAutomaticRuns(t *testing.T) {
    cfg := validConfig()
    cfg.Storage.MetadataTarget = ""
    _, err := Initialize(context.Background(), cfg)
    require.ErrorContains(t, err, "storage metadata target")
}
```

- [ ] **Step 2: 增加运行配置**

```go
type StorageConfig struct {
    MetadataTarget string `yaml:"metadata_target"`
    DataViewTarget string `yaml:"data_view_target"`
    AppID string `yaml:"app_id"`
    AppKeySecretRef string `yaml:"app_key_secret_ref"`
}

type SnapshotConfig struct {
    Root string `yaml:"root"`
    Retention time.Duration `yaml:"retention"`
}

type TriggerConfig struct {
    ConsumerName string `yaml:"consumer_name"`
    MaxConcurrentRunners int `yaml:"max_concurrent_runners"`
    DeadlineSweepInterval time.Duration `yaml:"deadline_sweep_interval"`
}
```

默认 `MaxConcurrentRunners=4`。同一 Runner 使用 keyed mutex 串行提交，不同 Runner 可并行；不得引入分布式锁。

- [ ] **Step 3: 实现异步 Run worker**

```text
claim ready transition
  -> load compiled runner and verify dependency hash
  -> resolve and persist UniverseSnapshot
  -> create PENDING run with recalc_revision=0
claim PENDING run
  -> build MarketFrame
  -> freeze StrategyInputSnapshot
  -> evaluate due slots
  -> CommitRun
  -> mark SUCCEEDED/FAILED
```

coordinator 补齐 Task 9 留出的链路：readiness 事务不直接创建 Run。claim 使用 SQLite 状态更新和 owner token，进程重启后将超过 lease 的 RUNNING 恢复为 PENDING。一个周期失败不阻塞下一周期。

- [ ] **Step 4: 接入 consumer、sweeper、worker 和 outbox**

启动顺序：

```text
database -> storage clients -> Python engine -> outbox -> run worker
         -> ready consumer -> deadline sweeper -> RPC/health ready
```

关闭时先停止 consumer 和 sweeper，再等待 run worker，最后关闭 Python engine、outbox 和数据库。

- [ ] **Step 5: 更新 EventBus ACL 和健康条件**

Strategy 凭据增加订阅：

```text
storage.view.source_period.ready
storage.view.factor_period.ready
```

Storage client 必须从 secret source 读取 AppKey，并按 DataView HMAC 协议签名；`service-deployments.yaml` 为 DataView callers 增加 strategy AppID。EventBus 凭据除订阅 subject 外，还要允许固定 durable 的 `$JS.API.CONSUMER.CREATE/INFO/DELETE`、`$JS.API.CONSUMER.MSG.NEXT` 和 `$JS.ACK`。保留发布 `trade.weight_target.requested`。健康检查要求 Storage clients、consumer、worker pool 和 outbox relay 全部初始化成功；积压只降级 readiness，不把进程判定为 dead。

- [ ] **Step 6: 运行测试并提交**

Run:

```bash
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/bootstrap ./internal/runtime ./internal/trigger -count=1)
(cd modules/admin && go test ./cmd/cli -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/internal/bootstrap modules/strategy/internal/runtime modules/strategy/config modules/admin/cmd/cli examples/setup/default/service-deployments.yaml
git commit -m "feat(strategy): run automatically from view ready events"
```

### Task 16: 实现 CLI 声明式装配和示例策略

**Files:**
- Modify: `modules/cli/internal/setup/config/config.go`
- Modify: `modules/cli/internal/setup/config/config_test.go`
- Modify: `modules/cli/internal/command/setup_factors.go`
- Modify: `modules/cli/internal/command/setup_factors_test.go`
- Create: `modules/cli/internal/command/setup_strategies.go`
- Create: `modules/cli/internal/command/setup_strategies_test.go`
- Modify: `modules/cli/internal/command/setup_init.go`
- Modify: `modules/cli/internal/command/setup_init_test.go`
- Modify: `custom.toml.example`
- Create: `examples/strategies/coin_selection_v1/strategy.yaml`
- Create: `examples/strategies/coin_selection_v1/selection.yaml`
- Create: `examples/strategies/coin_selection_v1/reference_compatibility.yaml`
- Create: `examples/strategies/coin_selection_v1/reference_compatibility_test.go`
- Modify: `examples/setup/default/README.md`
- Modify: `modules/cli/README.md`

- [ ] **Step 1: 写三层 Factor 配置解析测试**

```go
func TestManifestSeparatesFactorDefinitionsInstancesAndBindings(t *testing.T) {
    manifest := loadManifest(t, factorThreeLayerFixture)
    require.Len(t, manifest.Factors.Definitions, 1)
    require.Len(t, manifest.Factors.Instances, 2)
    require.Len(t, manifest.Factors.Bindings, 2)
}
```

配置结构：

```toml
[[factors.definitions]]
factor_def_id = "bias"
revision = 1
manifest_file = "manifests/bias.yaml"
source_file = "timeseries/bias.py"

[[factors.instances]]
factor_instance_id = "bias_r1_w20"
factor_def_id = "bias"
factor_def_revision = 1
params_json = '{"window":20}'

[[factors.bindings]]
binding_id = "bias-r1-w20-spot-1h"
factor_instance_id = "bias_r1_w20"
space_id = "default"
source_view_id = "crypto_spot_1h"
frequency = "1h"
```

- [ ] **Step 2: 实现 Factor 装配顺序**

```text
publish FactorDef
  -> create/resolve FactorInstance
  -> create disabled Binding
  -> reconcile result Dataset/View
  -> enable Instance
  -> enable Binding
```

同一配置重跑必须幂等；相同 ID 不同源码、参数或 binding scope 返回 conflict，不执行静默覆盖。

- [ ] **Step 3: 写 Strategy 装配测试**

```go
func TestSetupStrategyCompilesFactorRefsBeforeRunnerCreation(t *testing.T) {
    calls := runSetupFixture(t)
    require.Equal(t, []string{
        "publish-strategy", "resolve-factors", "create-runner", "validate-runner",
    }, calls)
}
```

- [ ] **Step 4: 实现 Strategy 装配**

`custom.toml` 增加：

```toml
[strategies]
enabled = true
source_dir = "./examples/strategies"

[[strategies.runners]]
runner_id = "coin-selection-paper"
strategy_file = "coin_selection_v1/strategy.yaml"
selection_file = "coin_selection_v1/selection.yaml"
space_id = "default"
logical_account_id = "paper-usdt"
status = "disabled"
```

装配顺序为 StrategyDef、compiled config、Runner disabled、依赖校验、用户显式 enable。不得写 `capital_base` 或固定资金字段。

- [ ] **Step 5: 编写非资金绑定的示例策略**

示例至少展示：

```text
spot + perpetual UniverseSpec
include/exclude
two StrategyComponents
independent long/short filters
weighted cross-sectional score
fraction and rank_range selection
two RebalanceSlots
strict readiness
gross/net observability without Strategy risk limits
```

示例使用通用权重，不出现 2000U，也不复制下载目录的策略名称和注释。

另增加只用于兼容性验收的 reference fixture，不作为默认生产配置：它必须能声明式编译参考配置的 9 个 components，解析其实际需要的 24 个 FactorInstance，包含 `cross_section_rank_diff`、`cross_section_rank_bias`，并证明最大 history periods 为 1609。该 fixture 只验证框架表达能力，不保留参考资金、名称或注释。

- [ ] **Step 6: 运行 CLI 测试并提交**

Run:

```bash
(cd modules/cli && go test ./internal/setup/config ./internal/command -count=1)
```

Expected: PASS。

```bash
git add modules/cli custom.toml.example examples/strategies examples/setup/default/README.md modules/cli/README.md
git commit -m "feat(cli): provision factor instances and selection runners"
```

### Task 17: 让 Backtest 复用相同选币和权重核心

**Files:**
- Create: `modules/strategy/internal/backtest/iterator.go`
- Create: `modules/strategy/internal/backtest/iterator_test.go`
- Create: `modules/strategy/internal/backtest/runner.go`
- Create: `modules/strategy/internal/backtest/runner_test.go`
- Create: `modules/strategy/internal/backtest/execution.go`
- Create: `modules/strategy/internal/backtest/execution_test.go`
- Modify: `modules/strategy/proto/strategy.proto`
- Modify: `modules/strategy/internal/rpc/service.go`
- Modify: `modules/strategy/cmd/cli/main.go`
- Modify: `modules/strategy/cmd/cli/main_test.go`

- [ ] **Step 1: 写 Live/Backtest evaluator 一致性测试**

```go
func TestBacktestAndLiveProduceSameWeightsForSameSnapshot(t *testing.T) {
    snapshot := fixedSnapshot(t)
    live := evaluateLive(t, snapshot)
    backtest := evaluateBacktest(t, snapshot)
    require.Equal(t, live.Weights, backtest.Weights)
}
```

- [ ] **Step 2: 实现历史 period iterator**

iterator 只生成与 Live 相同的：

```text
period_time
UniverseSnapshot
StrategyInputSnapshot
due RebalanceSlots
```

不在 iterator 内计算因子、排名或交易数量。

- [ ] **Step 3: 实现模拟权重执行器**

Backtest 显式接收：

```go
type ExecutionConfig struct {
    InitialEquity string
    MakerFeeRate string
    TakerFeeRate string
    SlippageBPS string
}
```

该 `InitialEquity` 只属于 Backtest job，不进入 StrategyRunner。模拟器使用与 Trade weight resolver 一致的 notional/quantity 公式，但不访问真实账户。

- [ ] **Step 4: 增加 Backtest RPC 和 CLI**

```text
CreateBacktest
GetBacktest
ListBacktests
CancelBacktest
```

CLI：

```bash
go run ./modules/strategy/cmd/cli backtest run \
  --runner coin-selection-paper \
  --start 2026-01-01T00:00:00Z \
  --end 2026-06-01T00:00:00Z \
  --initial-equity 10000
```

- [ ] **Step 5: 运行测试并提交**

Run:

```bash
(cd modules/strategy && CGO_ENABLED=1 go test ./internal/backtest ./internal/selection ./internal/weight ./internal/rpc ./cmd/cli -count=1)
```

Expected: PASS。

```bash
git add modules/strategy/internal/backtest modules/strategy/proto modules/strategy/internal/rpc modules/strategy/cmd/cli
git commit -m "feat(strategy): reuse weighted selection in backtests"
```

### Task 18: 完成跨模块 E2E、文档切换和总体验收

**Files:**
- Modify: `modules/strategy/test/e2e_test.go`
- Modify: `modules/strategy/test/outbox_jetstream_e2e_test.go`
- Replace: `modules/strategy/test/strategy_trade_external_e2e_test.go`
- Replace: `modules/trade/test/strategy_target_external_e2e_test.go`
- Create: `modules/strategy/test/view_ready_weight_trade_external_e2e_test.go`
- Modify: `scripts/verify-event-contracts.sh`
- Modify: `docs/策略模块架构设计.md`
- Modify: `docs/策略模块Python策略接入手册.md`
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/交易模块架构设计.md`
- Modify: `docs/SUMMARY.md`

- [ ] **Step 1: 写完整自动链路 E2E**

测试链路必须真实经过：

```text
DatasetPeriodCollected
  -> ViewSourcePeriodReady
  -> Factor compute/write
  -> FactorPeriodComputed
  -> ViewFactorPeriodReady
  -> StrategyTrigger
  -> MarketFrame + StrategyInputSnapshot
  -> selection + WeightMerger
  -> LogicalAccountWeightTargetRequested
  -> Trade frozen resolution
  -> LogicalAccount quantity target
```

断言同一 ready event 和同一 weight event 重投不产生第二个 Run、sequence 或估值快照。

- [ ] **Step 2: 增加故障恢复 E2E**

覆盖：

```text
Strategy 在保存 readiness 后重启
Strategy 在保存 weight target 后、发布前重启
Trade 在保存 weight request 后、生成 quantity 前事务失败
EventBus publish success 但 outbox delete 失败
迟到的旧 View Ready 事件
```

每个场景最终只存在一份有效 FULL 权重和一份冻结 quantity 结果。

- [ ] **Step 3: 增加术语和协议守卫**

`scripts/verify-event-contracts.sh` 断言：

```text
Strategy domain/proto/event 不出现 InstrumentTarget.quantity
Trade event 不出现 quantity
Trade internal target 仍必须出现 quantity
不存在 FactorPeriodCollected
FactorPeriodComputed 状态包含 factor_instance_id
Strategy 不定义账户 gross/net 风控上限
Trade weight resolution 按 capital_source_id 去重 equity
```

- [ ] **Step 4: 将当前文档切换为 V2**

实现完成后，`docs/策略模块架构设计.md` 改为当前 V2 架构，不再把 `run-once + quantity` 描述成主路径；Python 手册改为 MarketFrame 和 weights；Factor/Trade 文档同步新的三层身份和冻结估值边界。

- [ ] **Step 5: 运行模块级验证**

Run:

```bash
(cd modules/factor && CGO_ENABLED=1 go test -race ./... -count=1)
(cd modules/strategy && CGO_ENABLED=1 go test -race ./... -count=1)
(cd modules/trade && CGO_ENABLED=1 go test -race -gcflags=all=-l -ldflags=-s=false ./... -count=1)
(cd modules/strategy && python3 -m pytest pyworker -q)
(cd modules/strategy && python3 -m pytest pysdk/tests -q)
python3 -m pytest examples/factors/tests -q
```

Expected: PASS。

- [ ] **Step 6: 提交最终集成，使 clean-tree 门禁可运行**

```bash
git add modules packages scripts examples docs custom.toml.example
git commit -m "test(strategy): verify view ready to weighted trade flow"
```

- [ ] **Step 7: 运行仓库级验证**

Run:

```bash
make proto
git diff --check
./scripts/test-go-workspace.sh
make verify-pr
make verify
```

Expected: 全部 PASS，`git status --short` 为空。`make verify-pr` 内含 clean-tree `proto-check`，因此必须在 commit 后运行。若验证产生必要修复，新增或 amend 一个 fix commit，确认工作树重新为空后，从本步骤完整重跑。

## 完成标准

- 12 个 FactorDef 可导入；同一 Def 可创建多个可读 ID 的 FactorInstance。
- `bias`、`cci` golden tests 证明使用新公式。
- FactorPeriodComputed 和 ViewFactorPeriodReady 全链路使用 FactorInstance identity。
- Runner 可配置用户选币范围、多因子、过滤、多空、选币数量、组件权重和调仓轮次。
- 同一时点多个 instrument 的 MarketFrame 可以校验、hash 和执行。
- 默认 strict readiness 不会在缺失标的时静默改变横截面。
- RebalanceSlot 在重启后恢复，WeightMerger 输出稳定 FULL 权重。
- Strategy/Trade 跨模块协议只有权重；Trade raw quantity 冻结结果可审计、可幂等重放，venue-specific 量化只在 TargetExecutor 选定 member 后发生。
- 同周期全部 due slots 原子更新；Slot 不自动过期，`hold` 保留旧目标，`clear` 才清空。
- StrategyInputSnapshot 含内容寻址不可变 Frame 制品和 point-in-time availability timeline，可真实重放。
- Trade 按 `capital_source_id` 去重 authoritative equity，并用 sequence CAS 阻止旧解析覆盖新目标。
- Live、Paper 和 Backtest 对同一输入快照产生相同策略权重。
- 不存在固定资金 Runner 配置、`is_use_spot`、`FactorPeriodCollected` 或框架级 `circulating_supply` 对象。
- 模块测试、跨模块 E2E、`make verify-pr` 和 `make verify` 全部通过。
