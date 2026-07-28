# Factor Generic Time-Series Review Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `modules/factor` 收敛为简单、通用的时序数值因子模块，移除核心层对 OHLCV、整数 period 和邢不行 `signal` 协议的强制假设，同时修复本轮 review 识别出的 5 个真实正确性问题。

**Architecture:** 保留当前单实例、进程内 scheduler、best-effort realtime、Storage 事实源和 Go 调度 Python 的边界。Factor 定义显式声明输入列、输出列、静态 JSON 参数和回看行数；Python 统一执行 `compute(df, params)`，K 线只作为管理台初始模板和示例存在。实时事件按完整 scope 合并成半开时间范围，补算和实时继续复用同一个 range runner。

**Tech Stack:** Go 1.25, tRPC-Go, GORM + SQLite, NATS JetStream, `packages/pyruntime`, Storage Proto/DataNode, Python 3 + pandas/numpy/pytest, Vue 3 + TypeScript + Arco Design.

---

## 1. 实施基线

本计划基于：

```text
repository: /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
branch:     feature/mooyang
HEAD:       34104c285d47cd96c0d53436a9076dbdc75ea4aa
worktree:   clean
date:       2026-07-28
```

实施开始时必须重新记录 HEAD。如果代码已变化，先逐项复核本计划中的文件和符号，不允许机械套用旧行号。

## 2. 已确认的设计决策

### 2.1 产品边界

目标是“通用时序数值因子”，不是任意 ETL、指标市场或分布式计算平台：

1. 输入必须来自一个 Storage time-series Dataset。
2. 一次任务仍限定一个 `space/source_dataset/subject/freq/time_range`。
3. 一个 Factor 可以读取任意显式声明的数值列，不再自动读取 OHLCV。
4. 一个 Factor 可以输出一个或多个 double 列。
5. 不支持截面聚合、Factor DAG、跨 subject join、任意输出类型和状态化增量算法。
6. Storage 继续是真实数据源；Python Factor 不得自行连接数据库、交易所或 EventBus。
7. realtime 继续是 ACK 后进内存的 best-effort 语义，缺口由 `run-once` 或同步 `RecalcFactor` 修复。

### 2.2 FactorDef 最终契约

```proto
message FactorDef {
  string factor_id = 1;
  string name = 2;
  string source_code = 3;
  string source_hash = 4;
  repeated string input_columns = 5;
  repeated string outputs = 6;
  string params_json = 7;
  int32 lookback_rows = 8;
  string status = 9;
  string created_at = 10;
  string updated_at = 11;
}
```

字段语义：

- `input_columns`：Factor 实际可见的 Storage 字段；不包含系统时间列。
- `outputs`：最终写入 target Dataset 的稳定列名，不再由 `name + period` 隐式生成。
- `params_json`：静态 JSON object，默认 `{}`；同一 Factor 在所有 binding 使用同一组参数。
- `lookback_rows`：计算第一个目标行需要的最大输入行数，必须 `>= 1`，不再从参数值推导。
- `data_time`：worker 注入的保留系统列，使用 UTC 纳秒精度；Factor 不能把它声明为输入或输出。

如果同一算法需要不同参数，创建不同 `factor_id`，不增加 binding 级参数覆盖。

`outputs` 创建后不可修改。需要重命名或删除输出时创建新的 Factor 定义，避免个人项目为了列迁移引入复杂状态机。`source_code`、`input_columns`、`params_json` 和 `lookback_rows` 可以更新。

### 2.3 Python 最终契约

```python
def compute(df, params):
    return {
        "output_name": series,
    }
```

约束：

- `df` 按 `data_time` 升序。
- `df` 只包含系统列 `data_time` 和该 Factor 声明的 `input_columns`。
- `params` 一定是 dict。
- 返回值必须是 `dict[str, pandas.Series]`。
- 返回 key 必须与 `outputs` 完全一致。
- 每个 Series 必须与输入 DataFrame 等长且 index 一致。
- worker 再按目标半开区间切片，Go 校验目标行数和有限数值/null。
- 不保留 `signal`、`signal_multi_params` 或 `candle_begin_time` 兼容分支。

### 2.4 null 语义

Python 的 NaN/Infinity 继续编码为 JSON `null`。Factor 写回必须将 null 转成 Storage 已存在的显式 null marker：

```go
&storagepb.TypedValue{
    Value: &storagepb.TypedValue_NullValue{
        NullValue: storagepb.NullValue_NULL_VALUE_NULL,
    },
}
```

这表示“清除旧值”，与“请求中省略此字段，保留旧值”严格区分。不新增 `clear_field_ids`、删除 RPC 或整行覆盖模式。

### 2.5 不做事项

- 不增加 persistent scheduler、Inbox、DLQ、Exactly-once 或分布式锁。
- 不增加参数 Schema Registry、FactorKind、输入列映射 DSL 或模板注册中心。
- 不增加 Arrow、共享内存、Factor 内并行或多机 worker。
- 不兼容旧 SQLite schema、旧 Factor Proto 客户端或旧 Python `signal` 文件。
- 不把 `freq` 删除；它是 Storage 时序键和 binding 路由维度，不是 Factor 的计算 period。

## 3. 目标文件结构

现有包边界总体合理，不创建新的框架层。只在职责已经混杂的文件内做小范围拆分：

```text
modules/factor/internal/domain/
  factor.go                 FactorDef 和 binding subject 判断
  validation.go             FactorDef/Binding 归一化

modules/factor/internal/registry/
  service.go                显式单文件 import
  source.go                 文件名、默认 target dataset 等无源码解析 helper
  metadata_sync.go          Storage factor/dataset/subject/output reconciliation

modules/factor/internal/engine/
  types.go                  FactorTask/FactorSpec/FactorResult
  json_codec.go             JSON request/response
  executor.go               Python pool facade

modules/factor/internal/trigger/
  event_batcher.go          固定窗口范围聚合

modules/factor/internal/scheduler/
  builder.go                Domain -> executable task
  service.go                pending range merge、range runner、结果校验

modules/factor/internal/storageio/
  dataframe.go              Storage row -> generic DataFrame
  writeback.go              double/null patch

modules/factor/pyworker/
  codec.py                  generic DataFrame/result codec
  worker.py                 compute(df, params)
```

删除后不再存在的概念：

```text
Periods / periods
Depends / depends
DefaultPeriods
DefaultLookback
DependsFromSource
KLineColumns
signal
signal_multi_params
candle_begin_time
```

参数 JSON 中允许业务自行使用 `window`、`periods` 等 key；删除的是核心框架的固定字段和控制流。

---

### Task 0: 建立隔离实施基线

**Files:**
- No code changes

- [ ] **Step 1: 创建独立 worktree**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git status --short --branch
git worktree add ../moox-factor-generic-timeseries -b feature/factor-generic-timeseries
cd ../moox-factor-generic-timeseries
git rev-parse HEAD
```

Expected:

```text
原 worktree 无未提交变更
新 worktree HEAD 等于执行时记录的基线 SHA
```

- [ ] **Step 2: 运行修改前证明集**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/packages/pyruntime
go test ./... -count=1
go test -race ./... -count=1
```

Expected: all PASS. 若失败，先确认是基线问题还是环境依赖问题，并把原始失败记录到实施日志，不得把基线失败归因于后续改动。

- [ ] **Step 3: 运行 Python 基线**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
PYTHONPATH="$PWD/../../packages/pyruntime/python" \
  uv run --with-requirements pyworker/requirements.txt \
  python -m pytest pyworker -q
```

Expected: current Python tests PASS.

---

### Task 1: 修复 Python worker 生命周期泄漏

**Files:**
- Modify: `packages/pyruntime/process/worker.go`
- Modify: `packages/pyruntime/process/worker_test.go`
- Modify: `packages/pyruntime/process/supervisor.go`
- Modify: `packages/pyruntime/process/supervisor_test.go`
- Modify: `packages/pyruntime/pool/pool.go`
- Modify: `packages/pyruntime/pool/pool_test.go`

- [ ] **Step 1: 为 dead worker 替换和多 worker Close 写失败测试**

在 `supervisor_test.go` 增加：

```go
func TestSupervisorEnsureClosesDeadWorkerBeforeReplacement(t *testing.T) {
    first := &failingWorker{memoryWorker: memoryWorker{ready: true}, runErr: errors.New("boom")}
    second := &memoryWorker{}
    created := 0
    supervisor := NewSupervisor(func(context.Context) (Worker, error) {
        created++
        if created == 1 {
            return first, nil
        }
        return second, nil
    }, SupervisorConfig{})

    _, err := supervisor.RunLoadedMany(
        context.Background(),
        []LoadRequest{{LogicalID: "factor"}},
        RunRequest{RequestID: "run-1"},
    )
    require.Error(t, err)
    require.True(t, first.closed)

    _, err = supervisor.Ensure(context.Background())
    require.NoError(t, err)
    require.Equal(t, 2, created)
}
```

在 `pool_test.go` 增加两个可记录 Close 的 worker，断言第一个 Close 返回错误时第二个仍然被关闭，最终错误包含第一个错误。

在 `worker_test.go` 增加真实子进程回收测试：

```go
func TestStdioWorkerCloseReapsProcessAndIsIdempotent(t *testing.T) {
    cmd := exec.Command("sh", "-c", "sleep 30")
    require.NoError(t, cmd.Start())
    worker := &StdioWorker{cmd: cmd, state: StateReady}

    require.NoError(t, worker.Close())
    require.NotNil(t, cmd.ProcessState)
    require.Equal(t, StateDead, worker.State())
    require.NoError(t, worker.Close())
}
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/packages/pyruntime
go test ./process ./pool -run 'TestSupervisorEnsureClosesDeadWorkerBeforeReplacement|TestPoolCloseContinuesAfterWorkerError' -count=1
```

Expected: FAIL，证明当前 `RunLoadedMany` 和 `Pool.Close` 会遗留 worker。

- [ ] **Step 2: 增加幂等 terminate helper**

在 `worker.go` 增加仅在持有 `w.mu` 时调用的 helper：

```go
func (w *StdioWorker) terminateLocked() error {
    if w.cmd == nil {
        w.state = StateDead
        return nil
    }
    cmd := w.cmd
    w.cmd = nil
    w.state = StateDead

    var errs []error
    if cmd.Process != nil {
        if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
            errs = append(errs, err)
        }
    }
    if err := cmd.Wait(); err != nil {
        var exitErr *exec.ExitError
        if !errors.As(err, &exitErr) && !errors.Is(err, os.ErrProcessDone) {
            errs = append(errs, err)
        }
    }
    return errors.Join(errs...)
}
```

将 `Run` 和 `control` 中写失败、超时、读失败、`TYPE_ERROR`、意外 frame 的 fatal 分支统一调用 `terminateLocked()`。`Close()` 只加锁后调用该 helper，二次 Close 必须返回 nil。

- [ ] **Step 3: Supervisor 不得覆盖未关闭 worker**

修改 `Supervisor.Ensure`：

```go
if s.worker != nil {
    if s.worker.State() == StateReady {
        return s.worker, nil
    }
    _ = s.worker.Close()
    s.worker = nil
}
```

修改 `RunLoadedMany`，Load 或 Run 任一步失败都执行 `restart`：

```go
result, err := w.Run(ctx, req)
if err != nil {
    return RunResult{}, s.restart(w, err)
}
return result, nil
```

不在 Supervisor 内额外重试 Factor Run；现有 scheduler 的 `max_retry` 仍是任务级唯一重试策略。

- [ ] **Step 4: Pool 关闭全部 supervisor**

修改 `pool.Close`：

```go
func (p *Pool) Close() error {
    var errs []error
    for _, worker := range p.workers {
        if err := worker.Close(); err != nil {
            errs = append(errs, err)
        }
    }
    return errors.Join(errs...)
}
```

- [ ] **Step 5: 运行共享 runtime 和 Strategy 回归**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/packages/pyruntime
go test ./... -count=1
go test -race ./... -count=1
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/strategy
go test ./internal/engine/... -count=1
```

Expected: PASS；错误 Factor 不再遗留可继续循环的 Python 子进程，Strategy 共享调用不回归。

- [ ] **Step 6: 提交**

```bash
git add packages/pyruntime/process packages/pyruntime/pool
git commit -m "fix(pyruntime): reap failed python workers"
```

---

### Task 2: 贯通显式 null 清值语义

**Files:**
- Modify: `modules/storage/proto/common.proto`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `modules/storage/internal/service/primarystore/metadata_validator.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_validator_test.go`
- Modify: `modules/storage/internal/eventmapper/rows_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/store_test.go`
- Modify: `modules/archive/internal/domain/row.go`
- Modify: `modules/factor/internal/storageio/writeback.go`
- Modify: `modules/factor/internal/storageio/client_test.go`

- [ ] **Step 1: 写 null 覆盖旧值的失败测试**

将 `TestWriteFactorPatchSkipsNullCells` 改名为 `TestWriteFactorPatchWritesExplicitNullCells`，核心断言：

```go
first := access.writeReqs[0].GetRows()[0].GetFields()
require.Len(t, first, 2)
require.Equal(
    t,
    storagepb.NullValue_NULL_VALUE_NULL,
    fieldByID(first, "bias_20").GetValue().GetNullValue(),
)
```

测试文件内增加：

```go
func fieldByID(fields []*storagepb.FieldValue, id string) *storagepb.FieldValue {
    for _, field := range fields {
        if field.GetFieldId() == id {
            return field
        }
    }
    return nil
}
```

在 `metadata_validator_test.go` 增加：

```go
func TestMetadataValidatorAcceptsExplicitNullForRegisteredColumn(t *testing.T) {
    // 注册 double 列 close。
    // 对 close 写 NULL_VALUE_NULL 应通过。
    // 对未知列写 null 应失败。
    // 对 NULL_VALUE_UNSPECIFIED 应失败。
}
```

在 Pebble store 测试中先写 double，再对同一 key/field 写 null，读取后断言返回 null marker 而不是旧 double。

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./internal/storageio -run TestWriteFactorPatchWritesExplicitNullCells -count=1
```

Expected: FAIL，当前代码会跳过 null。

- [ ] **Step 2: 统一本地和公共事件 enum 名称**

将 `modules/storage/proto/common.proto` 改为：

```proto
enum NullValue {
  NULL_VALUE_UNSPECIFIED = 0;
  NULL_VALUE_NULL = 1;
}
```

wire number 保持 `1`，只是让 Storage 本地 Proto 与 `packages/storagepb/storage_events.proto` 的 protojson 名称一致。

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
make -C modules/storage/proto all
```

更新 `modules/archive/internal/domain/row.go` 中生成符号引用。不得手改生成的 `.pb.go`。

- [ ] **Step 3: Metadata validator 允许已注册列写有效 null**

在字段循环中先确认列存在，再处理 null：

```go
if typed, ok := field.GetValue().GetValue().(*pb.TypedValue_NullValue); ok {
    if typed.NullValue != pb.NullValue_NULL_VALUE_NULL {
        return fmt.Errorf("field %q has invalid null marker", field.GetFieldId())
    }
    continue
}
```

只有非 null 才继续执行 declared/actual 类型相等校验。未知列仍必须在 null 判断前失败。

- [ ] **Step 4: Factor 写回 null marker**

在 `writeback.go` 增加：

```go
func nullField(name string) *storagepb.FieldValue {
    return &storagepb.FieldValue{
        FieldId: name,
        Value: &storagepb.TypedValue{
            Value: &storagepb.TypedValue_NullValue{
                NullValue: storagepb.NullValue_NULL_VALUE_NULL,
            },
        },
    }
}
```

写回循环改为：

```go
if i >= len(values) {
    return fmt.Errorf("factor column %s is shorter than target rows", name)
}
if values[i] == nil {
    row.Fields = append(row.Fields, nullField(name))
    continue
}
```

每个结果行都包含所有声明输出的 double 或 null，不再通过省略字段表达计算结果。

- [ ] **Step 5: 验证 null 跨边界保留**

在 `eventmapper/rows_test.go` 的输入中加入本地 `NULL_VALUE_NULL`，断言 `ToEventRows` 和 `ToStorageRows` 往返后仍为 null。

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/storage
go test ./internal/eventmapper ./internal/service/primarystore ./internal/service/datanode/pebble -count=1
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./internal/storageio -count=1
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/archive
go test ./... -count=1
```

Expected: PASS；旧 double 被显式 null 覆盖，事件转换和 Archive 编译均正常。

- [ ] **Step 6: 提交**

```bash
git add modules/storage/proto modules/storage/internal modules/archive/internal/domain/row.go modules/factor/internal/storageio
git commit -m "fix(storage): honor explicit null field updates"
```

---

### Task 3: 将 Factor 核心切换为通用时序契约

这是唯一需要原子完成的跨层 breaking change。不要通过同时保留 `periods/depends` 制造临时兼容层；在一个提交中完成 Proto、SQLite、Go、Python、CLI 和 Web 契约切换。

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Regenerate: `modules/factor/proto/factorgen/*`
- Modify: `modules/factor/schema/factor.sql`
- Modify: `modules/factor/schema/schema_test.go`
- Modify: `modules/factor/internal/store/database.go`
- Modify: `modules/factor/internal/store/database_test.go`
- Modify: `modules/factor/internal/domain/factor.go`
- Modify: `modules/factor/internal/domain/validation.go`
- Modify: `modules/factor/internal/domain/validation_test.go`
- Modify: `modules/factor/internal/store/factor.go`
- Modify: `modules/factor/internal/store/factor_test.go`
- Modify: `modules/factor/internal/store/binding_test.go`
- Modify: `modules/factor/internal/rpc/convert.go`
- Modify: `modules/factor/internal/rpc/convert_test.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/registry/service.go`
- Modify: `modules/factor/internal/registry/service_test.go`
- Modify: `modules/factor/internal/registry/source.go`
- Modify: `modules/factor/internal/registry/source_test.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/engine/types.go`
- Modify: `modules/factor/internal/engine/json_codec.go`
- Modify: `modules/factor/internal/engine/json_codec_test.go`
- Modify: `modules/factor/internal/storageio/dataframe.go`
- Modify: `modules/factor/internal/scheduler/builder.go`
- Modify: `modules/factor/internal/scheduler/builder_test.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`
- Modify: `modules/factor/pyworker/codec.py`
- Modify: `modules/factor/pyworker/worker.py`
- Modify: `modules/factor/pyworker/test_worker.py`
- Modify: `modules/factor/factors/Bias.py`
- Modify: `modules/factor/factors/Cci.py`
- Modify: `examples/factors/README.md`
- Modify: `examples/factors/timeseries/Bias.py`
- Modify: `examples/factors/timeseries/Cci.py`
- Add: `examples/factors/timeseries/ExcessReturn.py`
- Delete: `examples/factors/timeseries/*.py` except the three files above
- Delete: `examples/factors/sections/`
- Modify: `modules/factor/cmd/cli/main.go`
- Modify: `modules/factor/cmd/cli/main_test.go`
- Modify: `modules/factor/cmd/cli/import.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`
- Modify: `web/src/api/factor/types.ts`
- Modify: `web/src/views/factor/definitions/index.vue`
- Modify: `web/src/views/factor/__tests__/factor-contract.spec.ts`

- [ ] **Step 1: 先写新的 Domain 和 scheduler 失败测试**

`validation_test.go` 使用：

```go
factor := FactorDef{
    FactorID:     "excess-return",
    Name:         "ExcessReturn",
    SourceCode:   "def compute(df, params): return {}",
    InputColumns: []string{" benchmark_return ", "nav", "nav"},
    Outputs:      []string{"rolling_rank", "excess_return"},
    ParamsJSON:   ` { "window": 20 } `,
    LookbackRows: 20,
}
```

断言：

```text
InputColumns == ["benchmark_return", "nav"]
Outputs == ["excess_return", "rolling_rank"]
ParamsJSON == {"window":20}
status 默认 disabled
```

表驱动失败用例必须包含：

```text
空 input_columns
空 outputs
input/output 含 data_time
input/output 含空字符串
lookback_rows < 1
params_json 非法 JSON
params_json 是数组、字符串或数字而不是 object
```

`scheduler/service_test.go` 增加：

```go
func TestInputColumnsUsesOnlyDeclaredColumns(t *testing.T) {
    specs := []engine.FactorSpec{
        {InputColumns: []string{"nav", "benchmark_return"}},
        {InputColumns: []string{"nav", "risk_free_rate"}},
    }
    require.Equal(
        t,
        []string{"benchmark_return", "nav", "risk_free_rate"},
        inputColumns(specs),
    )
}
```

并增加两个 Factor 声明同一 output 时 `BuildTask` 返回错误的测试。

- [ ] **Step 2: 替换 Proto 并生成 factorgen**

将 `FactorDef` 替换为本计划 2.2 的最终定义，不 `reserved` 旧字段，不保留旧 tag 别名。

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
make -C modules/factor/proto all
```

Expected: `factor.pb.go` 和 `factor.trpc.go` 只出现新字段。

- [ ] **Step 3: 替换 SQLite 和 Domain**

`t_factor_defs` 最终列：

```sql
CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_source_path TEXT NOT NULL DEFAULT '',
    c_input_columns_json TEXT NOT NULL,
    c_outputs_json TEXT NOT NULL,
    c_params_json TEXT NOT NULL DEFAULT '{}',
    c_lookback_rows INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'disabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_lookback_rows >= 1),
    CHECK (c_status IN ('enabled', 'disabled')),
    UNIQUE (c_name)
);
```

Domain 定义：

```go
type FactorDef struct {
    FactorID     string    `gorm:"column:c_factor_id;primaryKey"`
    Name         string    `gorm:"column:c_name"`
    SourceCode   string    `gorm:"column:c_source_code"`
    SourceHash   string    `gorm:"column:c_source_hash"`
    SourcePath   string    `gorm:"column:c_source_path"`
    InputColumns []string  `gorm:"column:c_input_columns_json;serializer:json"`
    Outputs      []string  `gorm:"column:c_outputs_json;serializer:json"`
    ParamsJSON   string    `gorm:"column:c_params_json"`
    LookbackRows int       `gorm:"column:c_lookback_rows"`
    Status       string    `gorm:"column:c_status"`
    CreateTime   time.Time `gorm:"column:c_ctime"`
    ModifyTime   time.Time `gorm:"column:c_mtime"`
}
```

`database.go` 的精确列校验同步替换。`ApplySchema` 继续对任何旧列组合返回：

```text
factor database uses an obsolete schema; create a fresh database
```

不写 `ALTER TABLE`、复制脚本或双读逻辑。

- [ ] **Step 4: 实现通用定义归一化**

`NormalizeFactorDefinition` 必须：

1. trim id/name/source/status。
2. 对 input/output 逐项 trim；发现任何空项立即报错，再稳定去重、升序保存。不能静默丢弃空项。
3. 拒绝保留名 `data_time`。
4. `params_json` 空值归一为 `{}`。
5. 用 `json.Decoder` 解码并要求顶层为 object。
6. 重新 `json.Marshal` 保存紧凑、确定性的 JSON。
7. 要求 `lookback_rows >= 1`。

核心 JSON helper：

```go
func normalizeParamsJSON(raw string) (string, error) {
    if strings.TrimSpace(raw) == "" {
        return "{}", nil
    }
    var params map[string]any
    dec := json.NewDecoder(strings.NewReader(raw))
    dec.UseNumber()
    if err := dec.Decode(&params); err != nil {
        return "", fmt.Errorf("params_json must be a JSON object: %w", err)
    }
    if params == nil {
        return "", errors.New("params_json must be a JSON object")
    }
    if dec.More() {
        return "", errors.New("params_json contains trailing JSON values")
    }
    normalized, err := json.Marshal(params)
    if err != nil {
        return "", err
    }
    return string(normalized), nil
}
```

实现时使用第二次 decode 或检查 EOF 来拒绝尾随 JSON；不要只依赖 `dec.More()`。

- [ ] **Step 5: 更新 RPC 转换，并让所有写入口维护 outputs 不变量**

`factorToPB`/`factorFromPB` 映射新字段。

`normalizeFactor` 删除：

```text
registry.DependsFromSource
registry.DefaultLookback
period/max(period) 校验
```

`UpdateFactor` 在写入前读取旧定义：

```go
if !slices.Equal(existing.Outputs, normalized.Outputs) {
    return &factorpb.UpdateFactorRsp{
        RetInfo: invalid(errors.New("factor outputs are immutable; create a new factor_id")),
    }, nil
}
```

`source_hash` 仍由 source code 计算；API 传入值不作为权威。

`CreateFactor` 不得继续调用 repository `Upsert`。将持久层写入口拆成：

```go
Create(ctx context.Context, factor domain.FactorDef) error
Update(ctx context.Context, factor domain.FactorDef) error
```

`Create` 使用原子 insert-only；重复 `factor_id` 或 `name` 返回 already exists，绝不覆盖旧行。`Update` 只更新已经存在的定义，其 SQL assignment 列表根本不包含 `c_outputs_json`；service 另外比较请求与旧 outputs，给调用方返回清晰错误。不要保留一个可被 RPC/CLI 直接调用的无条件 Upsert。

必须增加：

```text
CreateFactor 同 factor_id、不同 outputs -> 拒绝，数据库旧 outputs 不变
CreateFactor 同 name、不同 factor_id -> 拒绝
UpdateFactor 修改 outputs -> 拒绝
```

registry import 在不存在时调用 Create，已存在时先应用相同的 outputs 不可变校验再调用 Update。

- [ ] **Step 6: 删除源码猜测，改为显式单文件 import**

删除 `DefaultLookback`、`DependsFromSource` 和三个源码正则。

`registry.ImportOptions`：

```go
type ImportOptions struct {
    FactorID     string
    InputColumns []string
    Outputs      []string
    ParamsJSON   string
    LookbackRows int
    Status       string
}
```

`ImportFactorFile(ctx, path, options)` 从文件名推导 Python module `name`，其他定义全部来自 options。
如果同一 `factor_id` 已存在，registry 必须读取旧定义并应用与 RPC 相同的 outputs 不可变校验，不能通过 CLI 绕过。

CLI `import` 改为一次导入一个文件：

```bash
./bin/moox-factor-cli import \
  --db ./data/factor/factor.db \
  --factors-dir ./factors \
  --file ./factors/Bias.py \
  --factor-id bias-20-96 \
  --input-columns close \
  --outputs bias_20,bias_96 \
  --params-json '{"windows":[20,96]}' \
  --lookback-rows 200
```

删除 `--default-periods`、目录扫描和 `parseIntCSV`。输入/输出继续复用 `parseStringCSV`。

- [ ] **Step 7: 删除默认 target 命名中的 K 线特判**

`ResultDataset` 不得再删除 source dataset 的 `_kline` 后缀。最终规则：

```text
normalized = strings.ToLower(strings.TrimSpace(sourceDataset))
candidate  = normalized + "_factor"
candidate <= 20 字符：直接返回
candidate > 20 字符：用 normalized 的完整值计算短 hash，并保留可读前缀
```

短名和长名都以完整 normalized source 为身份，不能先裁掉任何业务后缀。测试必须证明：

```text
ResultDataset("foo")       != ResultDataset("foo_kline")
ResultDataset("foo_kline") == "foo_kline_factor"
两个超过 20 字符但前缀相同的 source 不碰撞
```

- [ ] **Step 8: 替换 Engine 和 scheduler 类型**

最终类型：

```go
type FactorTask struct {
    TaskID        string
    SpaceID       string
    SourceDataset string
    TargetDataset string
    SubjectID     string
    Freq          string
    StartTime     time.Time
    EndTime       time.Time
    LookbackRows  int
    Factors       []FactorSpec
}

type FactorSpec struct {
    FactorID     string
    Name         string
    SourceHash   string
    SourcePath   string
    InputColumns []string
    Outputs      []string
    ParamsJSON   string
}
```

`BuildTask`：

- 取所有 Factor 的最大 `LookbackRows`。
- 验证所有 output 在同一 task 内全局唯一。
- 将显式定义复制到 `FactorSpec`。

`inputColumns` 只返回所有 `InputColumns` 的排序去重并集：

```go
func inputColumns(specs []engine.FactorSpec) []string {
    seen := map[string]struct{}{}
    for _, spec := range specs {
        for _, column := range spec.InputColumns {
            seen[column] = struct{}{}
        }
    }
    out := make([]string, 0, len(seen))
    for column := range seen {
        out = append(out, column)
    }
    sort.Strings(out)
    return out
}
```

删除 `storageio.KLineColumns`。将 `maxTargetBarsPerChunk` 重命名为 `maxTargetRowsPerChunk`，只改术语，不改变 2000 行上限。

`validateFactorResult` 的 expected set 来自 `spec.Outputs`，不再拼接 `Name_period`。

- [ ] **Step 9: 改为纳秒时间和 params object 请求**

`EncodeJSONRequestMeta` 使用 RFC3339Nano 字符串，避免 UnixMilli 截断：

```go
dataTimes := make([]string, 0, len(frame.DataTimes))
for _, at := range frame.DataTimes {
    dataTimes = append(dataTimes, at.UTC().Format(time.RFC3339Nano))
}
```

每个 factor meta：

```go
var params map[string]any
if err := json.Unmarshal([]byte(factor.ParamsJSON), &params); err != nil {
    return nil, fmt.Errorf("decode params_json for factor %s: %w", factor.FactorID, err)
}
factors = append(factors, map[string]any{
    "factor_id":     factor.FactorID,
    "name":          factor.Name,
    "source_hash":   factor.SourceHash,
    "source_path":   factor.SourcePath,
    "input_columns": factor.InputColumns,
    "outputs":       factor.Outputs,
    "params":        params,
})
```

DataFrame meta：

```go
"df": map[string]any{
    "columns":    columns,
    "data_times": dataTimes,
},
```

- [ ] **Step 10: 替换 Python codec 和 worker**

`decode_json_df`：

```python
def decode_json_df(meta):
    spec = meta.get("df", {})
    df = pd.DataFrame(spec.get("columns", {}))
    data_times = spec.get("data_times", [])
    if len(data_times) != len(df.index):
        raise ValueError("data_times length must match dataframe rows")
    df.insert(
        0,
        "data_time",
        pd.to_datetime(data_times, format="ISO8601", utc=True),
    )
    return df
```

Python 测试必须在同一个 `data_times` 数组内混合：

```text
2026-07-28T00:00:00Z
2026-07-28T00:00:00.000000001Z
```

并证明 pandas 2.x 可解析、排序正确，回传到 Go 后纳秒不丢失。不能使用默认格式推断；它会按首行整秒格式拒绝后续纳秒行。

`execute_request` 的 Factor 循环：

```python
for factor in meta.get("factors", []):
    name = factor["name"]
    inputs = list(factor.get("input_columns", []))
    expected_outputs = list(factor.get("outputs", []))
    params = factor.get("params", {})
    if not isinstance(params, dict):
        raise TypeError(f"{name} params must be an object")

    module = self.factors[name]
    compute = getattr(module, "compute", None)
    if not callable(compute):
        raise AttributeError(f"{name} must define compute(df, params)")

    factor_df = df[["data_time", *inputs]].copy(deep=False)
    produced = compute(factor_df, params)
    if not isinstance(produced, dict):
        raise TypeError(f"{name} compute result must be a dict")
    if set(produced) != set(expected_outputs):
        raise ValueError(
            f"{name} outputs mismatch: got={sorted(produced)} "
            f"want={sorted(expected_outputs)}"
        )
    for output in expected_outputs:
        series = produced[output]
        if not isinstance(series, pd.Series):
            raise TypeError(f"{name}.{output} must be a pandas Series")
        if len(series) != len(df.index) or not series.index.equals(df.index):
            raise ValueError(f"{name}.{output} must align with input rows")
        results[output] = series.loc[target_mask].tolist()
```

`target_mask` 使用 `df["data_time"]`。删除全部 `signal*` 和 `candle_begin_time` 分支。

- [ ] **Step 11: 迁移并收敛所有公开示例**

`Bias.py`：

```python
def compute(df, params):
    close = df["close"]
    outputs = {}
    for window in params["windows"]:
        average = close.rolling(window, min_periods=1).mean()
        outputs[f"bias_{window}"] = close / average - 1
    return outputs
```

`Cci.py`：

```python
def compute(df, params):
    window = int(params["window"])
    typical = (df["high"] + df["low"] + df["close"]) / 3
    mean = typical.rolling(window, min_periods=1).mean()
    deviation = (typical - mean).abs().rolling(window, min_periods=1).mean()
    return {"cci": (typical - mean) / (0.015 * deviation)}
```

K 线只是这两个文件的业务输入，不进入框架判断。

同步把 `examples/factors/timeseries/Bias.py`、`Cci.py` 改为相同 `compute(df, params)` 协议，并新增只依赖 `nav/benchmark_return` 的 `ExcessReturn.py`，作为真正的非 OHLCV 示例。

其余旧 `timeseries` 文件仍依赖 `signal`、隐式 period 或特定交易数据列；绿地项目不保留失效样例，直接删除。`examples/factors/sections/` 属于本模块明确不支持的截面协议，也直接删除。更新 `examples/factors/README.md`，只列出仍可被当前 CLI 导入并执行的三个文件。

- [ ] **Step 12: 更新管理台契约**

TypeScript：

```ts
export interface FactorDef {
  factor_id: string;
  name: string;
  source_code: string;
  source_hash?: string;
  input_columns: string[];
  outputs: string[];
  params_json: string;
  lookback_rows: number;
  status: FactorStatus;
  created_at?: string;
  updated_at?: string;
}
```

表格列改为“输入列 / 输出列 / 回看行数”。表单使用两个 `a-input-tag` 和一个 JSON textarea。编辑态禁用 outputs：

```vue
<a-input-tag
  v-model="outputTags"
  :disabled="editing"
  allow-clear
  placeholder="输入输出列名后回车"
/>
```

提交前执行：

```ts
let params: unknown;
try {
  params = JSON.parse(form.params_json || "{}");
} catch {
  Message.warning("参数必须是合法 JSON");
  return;
}
if (!params || Array.isArray(params) || typeof params !== "object") {
  Message.warning("参数必须是 JSON object");
  return;
}
```

新建表单可以预填 K 线 Bias 草稿，但不得增加 `kind: "kline"` 或后端模板字段。

- [ ] **Step 13: 运行原子契约验证**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./... -count=1
go test -race ./internal/domain ./internal/registry ./internal/engine ./internal/scheduler ./internal/rpc ./internal/store -count=1
go vet ./...
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
PYTHONPATH="$PWD/../../packages/pyruntime/python" \
  uv run --with-requirements pyworker/requirements.txt \
  python -m pytest pyworker -q
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
pnpm --dir web exec vitest run \
  --config vitest.config.ts \
  src/views/factor/__tests__/factor-contract.spec.ts
pnpm --dir web run build:prod
```

Expected: PASS。Python 测试必须证明双输出、缺失/多余输出、错位 Series、旧 `signal` 拒绝和纳秒 `data_time`。

- [ ] **Step 14: 扫描核心假设残留**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
rg -n \
  'DefaultPeriods|DefaultLookback|DependsFromSource|KLineColumns|signal_multi_params|c_periods_json|c_depends_json|candle_begin_time|TrimSuffix\([^[:cntrl:]]*_kline' \
  examples/factors \
  modules/factor/cmd \
  modules/factor/internal \
  modules/factor/proto \
  modules/factor/schema \
  modules/factor/pyworker \
  web/src/api/factor \
  web/src/views/factor
```

Expected: no matches。历史计划文档不在这个静态扫描范围内。

- [ ] **Step 15: 提交**

```bash
git add modules/factor web/src/api/factor web/src/views/factor
git commit -m "refactor(factor): generalize time-series factor contract"
```

---

### Task 4: 修复 Metadata reconciliation 和 binding 输入校验

**Files:**
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/registry/metadata_sync_test.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/domain/factor.go`
- Modify: `modules/factor/internal/domain/binding_test.go`

- [ ] **Step 1: 写 active target 扩列失败测试**

在 fake MetadataClient 中配置：

```text
source dataset: active, time-series, node/retention complete
target dataset: active + binding_locked
new factor outputs: ["excess_return", "rolling_rank"]
```

断言：

```go
require.Equal(
    t,
    []string{"excess_return", "rolling_rank"},
    fake.upsertedColumnNames(),
)
require.Zero(t, fake.checkActivationCalls)
require.Zero(t, fake.activateCalls)
```

fake client 保存 `upsertedColumns []*storagepb.DatasetColumn`，helper：

```go
func (f *fakeMetadataClient) upsertedColumnNames() []string {
    names := make([]string, 0, len(f.upsertedColumns))
    for _, column := range f.upsertedColumns {
        names = append(names, column.GetColumnName())
    }
    sort.Strings(names)
    return names
}
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./internal/registry -run TestSyncTargetDatasetReconcilesOutputsForActiveLockedTarget -count=1
```

Expected: FAIL，当前 `SyncTargetDataset` 在列同步前返回。

- [ ] **Step 2: 重排 SyncTargetDataset**

最终顺序：

```text
reconcile Storage Factor metadata
load and validate source Dataset is time-series
find/create target Dataset
merge target attributes and freq
copy source subjects
upsert every declared output column
if target already active+locked: return success
check activation
activate
```

删除 `metadata_sync.go:83-86` 的首次 active 短路，保留列同步后的 active 终态短路。

output column：

```go
func factorColumnOriginID(factorID, output string) string {
    return strings.TrimSpace(factorID) + "." + strings.TrimSpace(output)
}
```

属性：

```go
map[string]string{
    "display_name":     columnDisplayName(output),
    "origin_factor_id": factor.FactorID,
    "factor_output":    output,
}
```

不再写 `factor_param`。

- [ ] **Step 3: Storage Factor metadata 使用 Create-or-Update**

扩展 `MetadataClient` 加入 `UpdateFactor`。逻辑：

```text
GetFactor 成功 -> UpdateFactor
GetFactor not found -> CreateFactor
其他错误 -> 返回错误
```

Storage Factor：

```go
inputColumnsJSON, err := json.Marshal(factor.InputColumns)
if err != nil {
    return fmt.Errorf("marshal input columns for factor %s: %w", factor.FactorID, err)
}
outputsJSON, err := json.Marshal(factor.Outputs)
if err != nil {
    return fmt.Errorf("marshal outputs for factor %s: %w", factor.FactorID, err)
}

&storagepb.Factor{
    SpaceId:    spaceID,
    FactorId:   factor.FactorID,
    Name:       factor.Name,
    Algorithm:  factor.Name,
    ParamsJson: factor.ParamsJSON,
    ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
    Status:     storageFactorStatus(factor.Status),
    Attributes: map[string]string{
        "input_columns_json": string(inputColumnsJSON),
        "outputs_json":       string(outputsJSON),
        "lookback_rows":      strconv.Itoa(factor.LookbackRows),
    },
}
```

Factor 定义已在 Domain 归一化，slice marshal 通常不会失败；实现仍必须返回 marshal error，不使用 panic。

- [ ] **Step 4: 校验并规范化 subjects_json**

`subject_mode=all` 时固定保存 `[]`。

`subject_mode=include` 时：

- JSON 必须是 string array。
- 每项 trim 后非空。
- 去重并升序保存。
- 至少包含一个 subject。

增加 helper：

```go
func NormalizeBindingSubjects(mode, raw string) (string, error)
```

`normalizeBinding` 在持久化和 Metadata 同步前调用。`BindingAllowsSubject` 可以继续做防御性解析，但非法 RPC 输入必须返回 `INVALID_PARAM`，不能成功落库后永远不匹配。

- [ ] **Step 5: 验证**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./internal/registry ./internal/rpc ./internal/domain -count=1
go test -race ./internal/registry ./internal/rpc -count=1
```

Expected: PASS；active target 可以增加第二个 Factor 的输出列，且不重复 activation。

- [ ] **Step 6: 提交**

```bash
git add modules/factor/internal/registry modules/factor/internal/rpc modules/factor/internal/domain
git commit -m "fix(factor): reconcile active result datasets"
```

---

### Task 5: 将多行事件和 pending supersede 改为范围合并

**Files:**
- Modify: `modules/factor/internal/trigger/event_batcher.go`
- Modify: `modules/factor/internal/trigger/event_batcher_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`

- [ ] **Step 1: 写多行范围失败测试**

测试一个 event 同一 scope 包含：

```text
2026-07-28T00:00:00Z
2026-07-28T00:01:00Z
2026-07-28T00:02:00Z
```

Flush 后必须只有一个 task：

```go
require.Len(t, tasks, 1)
require.Equal(t, first, tasks[0].StartTime)
require.Equal(t, last.Add(time.Nanosecond), tasks[0].EndTime)
```

再写同一个 bar 重复出现的测试，范围不扩大、不生成第二个 task。

为 scheduler 增加：

```go
func TestSchedulerSupersedeMergesPendingRanges(t *testing.T) {
    first := rangeTask("BTC", time.Unix(10, 0), time.Unix(20, 0))
    second := rangeTask("BTC", time.Unix(5, 0), time.Unix(30, 0))
    require.NoError(t, svc.Enqueue(context.Background(), first))
    require.NoError(t, svc.Enqueue(context.Background(), second))
    queued, ok := svc.popShard(0, false)
    require.True(t, ok)
    require.Equal(t, time.Unix(5, 0), queued.StartTime)
    require.Equal(t, time.Unix(30, 0), queued.EndTime)
}
```

测试 helper：

```go
func rangeTask(subject string, start, end time.Time) Task {
    return Task{
        FactorTask: engine.FactorTask{
            TaskID:        "task-" + subject,
            SpaceID:       "crypto",
            SourceDataset: "series",
            TargetDataset: "series_factor",
            SubjectID:     subject,
            Freq:          "1m",
            StartTime:     start,
            EndTime:       end,
            LookbackRows:  2,
            Factors: []engine.FactorSpec{{
                FactorID: "f",
                Name:     "Factor",
                Outputs:  []string{"value"},
            }},
        },
        TriggerType: "event",
    }
}
```

Expected: FAIL。

- [ ] **Step 2: EventBatcher 持有半开范围**

`trigger.Task`：

```go
type Task struct {
    SpaceID         string
    SourceDataset   string
    TargetDataset   string
    SubjectID       string
    Freq            string
    StartTime       time.Time
    EndTime         time.Time
    FirstReceivedAt time.Time
    LastReceivedAt  time.Time
    TriggerType     string
    FactorIDs       []string
}
```

初始化：

```go
StartTime: dataTime.UTC(),
EndTime:   dataTime.UTC().Add(time.Nanosecond),
```

合并：

```go
if dataTime.Before(bucket.task.StartTime) {
    bucket.task.StartTime = dataTime.UTC()
}
end := dataTime.UTC().Add(time.Nanosecond)
if end.After(bucket.task.EndTime) {
    bucket.task.EndTime = end
}
```

不保存每个 bar 的集合，不生成每 bar 一个 scheduler task。范围中的实际目标行由 `ReadRangeChunk` 从 Storage 返回，天然跳过不存在的时间点。

- [ ] **Step 3: bootstrap 直接提交范围**

`buildSchedulerTask`：

```go
StartTime: task.StartTime,
EndTime:   task.EndTime,
```

`deterministicTaskID` 同时写入 RFC3339Nano 的 start/end。删除所有 `BarTime` 引用。

- [ ] **Step 4: pending task 合并范围而不是替换**

`Enqueue` 的同 key 分支：

```go
if current, ok := s.pending[key]; ok {
    if task.StartTime.Before(current.StartTime) {
        current.StartTime = task.StartTime
    }
    if task.EndTime.After(current.EndTime) {
        current.EndTime = task.EndTime
    }
    current.TaskID = task.TaskID
    current.Factors = task.Factors
    current.LookbackRows = task.LookbackRows
    s.pending[key] = current
    return nil
}
```

使用最新 Factor snapshot，但保留两个 pending task 的范围并集。running task 不做在线合并；其间到达的新事件进入下一批，仍符合 best-effort 边界。

- [ ] **Step 5: 验证多行 Collector 语义**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./internal/trigger ./internal/bootstrap ./internal/scheduler -count=1
go test -race ./internal/trigger ./internal/scheduler -count=1
```

Expected: PASS；一个 Collector catch-up batch 不再只计算最大 data_time。

- [ ] **Step 6: 提交**

```bash
git add modules/factor/internal/trigger modules/factor/internal/bootstrap modules/factor/internal/scheduler
git commit -m "fix(factor): preserve realtime event ranges"
```

---

### Task 6: 让 run-once 使用真实服务配置

**Files:**
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/cmd/cli/main.go`
- Modify: `modules/factor/cmd/cli/main_test.go`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`
- Add: `scripts/moox-factor-run-once.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-release-contract.sh`

- [ ] **Step 1: 写 config precedence 失败测试**

临时 YAML 包含非默认值：

```yaml
database:
  path: ../data/factor/test.db
storage:
  gateway_target: ip://10.0.0.8:11003
  gateway_node_id: node-a
engine:
  python_bin: /tmp/factor-venv/bin/python
  worker_path: ./pyworker/worker.py
  factors_dir: ./factors
  workers: 4
  task_timeout_ms: 45000
scheduler:
  max_retry: 2
```

测试 `resolveRunOnceRuntime`：

```text
加载 YAML、MOOX_FACTOR_* env 和 Gateway 凭证 env
相对 DB/factors/worker 路径相对于 Factor runtime 目录
显式 --db/--factors-dir 覆盖 YAML
worker 数仍固定为 1
timeout/max_retry 来自配置
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./cmd/cli -run 'TestRunOnceLoadsAppConfig|TestRunOnceCLIOverridesAppConfig' -count=1
```

Expected: FAIL，当前使用 `bootstrap.Default()`。

- [ ] **Step 2: 配置 worker_path**

`EngineConfig` 增加：

```go
WorkerPath string `yaml:"worker_path"`
```

默认：

```yaml
engine:
  python_bin: python3
  worker_path: ./pyworker/worker.py
  factors_dir: ./factors
```

环境覆盖：

```text
MOOX_FACTOR_ENGINE_WORKER_PATH
```

服务 bootstrap 和 run-once 都使用 `cfg.Engine.WorkerPath`，删除两处硬编码。

- [ ] **Step 3: CLI 增加 --config 并实现优先级**

`cliConfig` 增加：

```go
ConfigPath string
```

`run-once`：

```text
--config 默认 ./config/app.yaml
--db 默认空，空表示继承 config
--factors-dir 默认空，空表示继承 config
```

解析流程：

```go
appCfg, err := bootstrap.Load(cli.ConfigPath)
if err != nil {
    return nil, err
}
if cli.DBPath != "" {
    appCfg.Database.Path = cli.DBPath
}
if cli.FactorsDir != "" {
    appCfg.Engine.FactorsDir = cli.FactorsDir
}
```

根据 `<factor-runtime>/config/app.yaml` 布局，把相对的 DB、factor dir 和 worker path 解析到 config 目录的父目录。绝对路径保持不变。

- [ ] **Step 4: 提供可从干净 shell 执行的部署 wrapper**

服务进程仍通过 `FACTOR_ENV` 启动；同时新增 `scripts/moox-factor-run-once.sh` 并在发布包安装为 `${ROOT}/bin/moox-factor-run-once`。wrapper 自己解析 `${ROOT}`，不能依赖 `start_factor` 函数执行后留在父 shell 的数组或变量。

wrapper 必须显式设置并导出：

```text
MOOX_FACTOR_DB_PATH=${ROOT}/data/factor/factor.db
MOOX_FACTOR_ENGINE_PYTHON_BIN=${ROOT}/data/factor/venv/bin/python
MOOX_FACTOR_ENGINE_WORKER_PATH=${ROOT}/factor/pyworker/worker.py
MOOX_FACTOR_ENGINE_FACTORS_DIR=${ROOT}/factor/factors
MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=ip://127.0.0.1:11003
MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID=<gateway-service.env 中的 node id>
MOOX_GATEWAY_SERVICE_KEY_ID=factor
MOOX_GATEWAY_CALLER=factor
MOOX_GATEWAY_SERVICE_SECRET_KEY=<secrets/gateway-factor.key 的单行内容>
MOOX_GATEWAY_CA_FILE=${ROOT}/certs/gateway/peers.pem
```

wrapper 在 exec 前检查 config、CLI、venv Python、worker、factor 目录、Gateway factor key 和 CA 文件均存在；secret 为空或含换行立即失败。最后：

```bash
exec "${ROOT}/bin/moox-factor-cli" run-once \
  --config "${ROOT}/factor/config/app.yaml" \
  "$@"
```

`FACTOR_ENV` 也增加绝对 worker/factor 路径，保证常驻服务与 wrapper 契约一致。不能只修改 `FACTOR_ENV`，因为它只是 `start_factor` 子进程的 `env` 参数，不会导出给后续手工命令。

部署机从干净 shell 执行：

```bash
"${DEPLOY_ROOT}/bin/moox-factor-run-once" \
  --space crypto \
  --dataset binance_spot_kline \
  --subject BTC-USDT \
  --freq 1m \
  --start-time 2026-07-28T00:00:00Z \
  --end-time 2026-07-28T01:00:00Z
```

- [ ] **Step 5: 验证配置和发布 wrapper**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./internal/bootstrap ./cmd/cli -count=1
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
./scripts/build.sh factor
bash scripts/test-release-contract.sh
```

`test-release-contract.sh` 不只检查文件存在。它使用临时发布根目录、假的 `moox-factor-cli` 和假的 venv Python，从 `env -i HOME="$HOME" PATH="$PATH"` 启动 wrapper，捕获 exec 时的 argv/env，并断言：

```text
config、DB、Python、worker、factors 全部是发布根目录下的绝对路径
Gateway key id/caller 为 factor
secret 来自 gateway-factor.key
node id、target 和 CA 均已设置
调用方参数原样透传
```

再补一个真实部署 smoke：服务启动后从干净 shell 调用 wrapper 对两行临时时序数据执行 `run-once`，退出码必须为 0。Expected: PASS；两个 Factor 二进制生成，发布包包含 wrapper、worker、factors 和 Python runtime。

- [ ] **Step 6: 提交**

```bash
git add modules/factor/internal/bootstrap modules/factor/config modules/factor/cmd/cli scripts
git commit -m "fix(factor): load runtime config for run-once"
```

---

### Task 7: 更新文档并建立非 K 线端到端证明

**Files:**
- Modify: `modules/factor/test/e2e_test.go`
- Add: `modules/factor/test/storage_e2e_test.go`
- Modify: `modules/factor/README.md`
- Modify: `modules/factor/examples/run-once/README.md`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `docs/因子计算模块设计.md`
- Add: `scripts/test-factor-storage-e2e.sh`
- Modify: `scripts/verify-event-contracts.sh`

- [ ] **Step 1: 将现有组件集成测试改为非 K 线输入和多行事件**

E2E Factor source：

```python
def compute(df, params):
    excess = df["nav"] - df["benchmark_return"]
    return {
        "excess_return": excess,
        "rolling_rank": excess.rolling(
            int(params["window"]),
            min_periods=1,
        ).rank(),
    }
```

FactorDef：

```go
domain.FactorDef{
    FactorID:     "excess-return",
    Name:         "ExcessReturn",
    SourceHash:   sourceHash,
    SourcePath:   factorPath,
    InputColumns: []string{"nav", "benchmark_return"},
    Outputs:      []string{"excess_return", "rolling_rank"},
    ParamsJSON:   `{"window":2}`,
    LookbackRows: 2,
    Status:       domain.FactorStatusEnabled,
}
```

发布一个含两个 `data_time` 的 `DatasetRowsUpserted`。断言：

```text
EventBatcher 生成一个覆盖两行的半开范围
storageFake 收到的 column_names 恰好是 benchmark_return/nav
请求中没有 open/high/low/close/volume/quote_volume/trade_num
Python 一次调用返回两个 output
两个目标行都进入 WriteFactorPatch
```

这个测试继续保留 NATS、scheduler 和真实 Python，但 Storage 是 fake，因此准确定位为“Factor pipeline component integration”，不能作为真实 Storage E2E 的唯一证据。

- [ ] **Step 2: 增加穿过真实 Storage 的验收**

`storage_e2e_test.go` 使用 `//go:build integration`，只使用公开 Storage Metadata/Primary/View RPC client 和真实 `storageio.Client`，禁止定义 `storageFake`、`fakeAccessClient` 或直接构造 DataFrame。

测试流程：

```text
1. 通过真实 Metadata RPC 创建临时 space、time-series source dataset 和 DataNode 绑定。
2. source 只声明 data_time/nav/benchmark_return，不声明任何 OHLCV。
3. 通过真实 Primary RPC 写入两行输入，时间分别为整秒和 +1ns。
4. 通过 Factor RPC 创建 ExcessReturn、创建 binding，并同步真实 target metadata。
5. 调用部署 wrapper 的 run-once 计算覆盖两行的半开范围。
6. 通过真实 View/QueryTimeSeriesRows RPC 读取 target。
7. 断言 target 只有声明的两个输出列，数值正确，两个纳秒时间均存在。
8. 将第二行计算结果改为 null 后重跑，断言真实 Storage 中旧 double 已被清除。
9. 删除临时 space/dataset/factor；清理失败不能掩盖主断言。
```

`scripts/test-factor-storage-e2e.sh` 负责：

```text
要求一个已启动且包含 Gateway + Metadata + Primary + View + DataNode + Factor 的本地部署根目录
从 deploy root 读取 factor 凭证、node id 和 CA，并只在子进程中导出
设置 MOOX_FACTOR_STORAGE_E2E=1
运行 go test -tags=integration ./test -run TestFactorRealStorageE2E -count=1 -v
```

脚本缺少任一服务或凭证时必须 fail，不得 skip 成功。该脚本加入最终本地部署验收；普通 `go test ./...` 不依赖运行中的服务。

- [ ] **Step 3: 文档写清最终契约**

README 必须包含：

```text
FactorDef 新字段
data_time 系统列
compute(df, params)
outputs 不可变
null 清旧值
run-once --config
realtime best-effort 边界
```

`docs/因子计算模块设计.md` 删除把 OHLCV、period 和 `signal` 作为核心协议的描述；K 线 Bias/CCI 放入“模板示例”，不放入“运行时契约”。

不恢复旧计划中的 durable inbox、FactorRun、Arrow、截面因子或多实例分片。

- [ ] **Step 4: 更新静态边界检查**

`verify-event-contracts.sh` 增加对 active Factor 代码的拒绝项：

```text
KLineColumns
DependsFromSource
DefaultLookback
signal_multi_params
c_periods_json
c_depends_json
```

同时保留现有对 Inbox、replay、cross_section、Arrow 和异步补算状态的拒绝。

- [ ] **Step 5: 运行 Factor 完整验收**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries/modules/factor
PYTHONPATH="$PWD/../../packages/pyruntime/python" \
  uv run --with-requirements pyworker/requirements.txt \
  python -m pytest pyworker -q
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
./scripts/verify-event-contracts.sh
./scripts/build.sh factor
```

Expected: all PASS。

- [ ] **Step 6: 运行真实 Storage E2E**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
MOOX_DEPLOY_ROOT=/absolute/path/to/running/moox \
  ./scripts/test-factor-storage-e2e.sh
```

Expected: PASS；日志明确显示真实 Metadata/Primary/View/DataNode RPC 均被调用，且没有 fake Storage 实现。

- [ ] **Step 7: 运行跨模块和 Web 验收**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
./scripts/test-go-workspace.sh
make proto-check
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
pnpm --dir web test
pnpm --dir web run build:prod
pnpm --dir web run lint:eslint:check
pnpm --dir web run lint:prettier:check
```

Expected: PASS；`make proto-check` 后 worktree 没有生成代码漂移。

- [ ] **Step 8: 运行格式和残留扫描**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox-factor-generic-timeseries
git diff --name-only -- '*.go' | xargs gofmt -w
git diff --check
```

Run:

```bash
rg -n \
  'DefaultPeriods|DefaultLookback|DependsFromSource|KLineColumns|signal_multi_params|c_periods_json|c_depends_json|candle_begin_time|TrimSuffix\([^[:cntrl:]]*_kline' \
  examples/factors \
  modules/factor/cmd \
  modules/factor/internal \
  modules/factor/proto \
  modules/factor/schema \
  modules/factor/pyworker \
  web/src/api/factor \
  web/src/views/factor
```

Expected: no matches and no whitespace errors。

- [ ] **Step 9: 请求独立 codeCR 审查**

审查范围必须包含：

```text
通用契约是否仍隐式请求 OHLCV
params/output 校验是否可被绕过
worker 错误后是否完整回收进程
explicit null 是否真正覆盖旧值并穿过事件边界
active target 是否先扩列再返回
多行 event/pending range 是否仍会确定性丢行
run-once 是否使用部署配置和凭证
best-effort 边界是否被无意升级或削弱
```

codeCR 返回必须带文件和行号。主 Agent 逐条独立复核；确认的 finding 修复后重新运行受影响测试。

- [ ] **Step 10: 提交最终文档和测试**

```bash
git add modules/factor docs scripts/verify-event-contracts.sh web
git commit -m "test(factor): prove generic time-series execution"
```

- [ ] **Step 11: 最终 Git 验收**

Run:

```bash
git status --short --branch
git log --oneline --decorate -8
git diff HEAD^ --check
```

Expected:

```text
worktree clean
提交顺序与 Task 1-7 对应
不存在未生成 Proto、临时测试文件或未追踪产物
```

## 4. 最终验收矩阵

| 能力 | 必须证明 |
| --- | --- |
| 非 K 线输入 | `nav/benchmark_return` 计算时 Storage 请求中无任何隐式 OHLCV |
| 参数 | `params_json` 非 object 被拒绝，合法 object 作为 dict 传入一次 |
| 多输出 | output key 精确匹配；缺失、多余、重复、错位全部失败；重复 Create/Update/CLI 均不能修改 outputs |
| 时间 | `data_time` 为 UTC，同一 frame 混合整秒/纳秒 RFC3339Nano 时解析和往返不丢纳秒 |
| 回看 | `lookback_rows` 与 params 数值独立，只控制输入上下文 |
| Metadata | active+locked target 可新增第二个 Factor 的输出列，不重复 activation |
| null | 重算 null 后旧 double 不再可读，事件边界保留 null marker |
| realtime | 一个多行事件形成完整范围；pending 同 scope 合并范围而不是只保留最新行 |
| worker | Factor error、timeout、read/write failure 后进程被 Kill+Wait，pool 全量关闭 |
| run-once | 干净 shell 通过部署 wrapper 获得 Storage target、factor 凭证、DB、Python、worker、factors、timeout、retry；CLI 显式值优先 |
| 真实链路 | 非 K 线输入穿过真实 Metadata/Primary/View/DataNode，双输出可读且 null 可清旧值 |
| 简洁边界 | 无 Inbox、DLQ、Exactly-once、分布式、DAG、参数 Schema、FactorKind 或旧协议兼容 |

## 5. 实施完成定义

只有同时满足以下条件才可以声明完成：

1. Task 1-7 的 focused tests 全部通过。
2. Factor Go test/race/vet、Python pytest、Web test/build/lint 全部通过。
3. `./scripts/test-go-workspace.sh`、`make proto-check`、build 和 event contract 检查通过。
4. 非 K 线真实 Storage E2E、null 覆盖、active target 扩列、多行事件和 worker 回收均有行为测试。
5. codeCR 独立审查 finding 已逐条处置。
6. worktree clean，最终 HEAD 和远端状态在交付时分别核对。
