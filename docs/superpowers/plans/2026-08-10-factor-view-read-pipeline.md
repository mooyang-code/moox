# Factor View Read Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Factor 周期计算改造成“View 滑动窗口读取 -> 数据就绪任务队列 -> Python Worker 计算”的有界流水线，使不同标的的 K 线读取与 Python 计算重叠执行，并让单个读取超时不会阻塞其他标的。

**Architecture:** `ViewReadyRunner` 继续由 `ViewSourcePeriodReady` 驱动并生成轻量的“标的 × 因子”任务描述；`TaskRunner.RunAll` 按严格 period read key 合并任务，独立 View read worker 池以滑动窗口读取各标的并合并输入列，每个分组读取成功后立即把引用共享 Frame 的 prepared tasks 投递给有界计算队列。Python worker 只接收数据已就绪的任务；读取超时释放 read slot、任务进入队尾重试一次，最终失败只降级该标的，周期 Marker 仍等待全部读取、计算和写回进入终态。

**Tech Stack:** Go 1.25、tRPC-Go、Storage DataView、Python 3、pandas、MooX `packages/pyruntime`、Prometheus、JetStream。

---

## 0. 决策与边界

本计划覆盖并取代 `2026-08-10-factor-global-combination-concurrency.md` 中“TaskRunner worker 覆盖完整读/算/写生命周期”和“只有一个并发配置”的部分；旧计划作为历史实施记录保留，不回写。

### 0.1 入口和完成语义不变

- 实时入口仍是 `ViewSourcePeriodReady`，不增加 timer、cron 或 Factor 主动轮询。
- `ViewReadyRunner` 仍在一个周期内读取当前 enabled bindings 和 primary subjects。
- `FactorPeriodComputed` 只有在本周期所有组合的读取、计算、清理和结果写回全部终态后才能追加。
- source-ready consumer 只有在 Marker 追加成功后才 ACK。
- `OperationGate`、`FactorGate`、TaskValidator、稳定 TaskID、manifest 和幂等写回语义保持不变。

### 0.2 严格读取分组

只允许以下字段完全相同的任务共享一次 View 读取：

```text
space_id
source_view_id
source_dataset
subject_id
frequency
start_time
end_time
lookback_periods
period_time
trigger_type
trigger_event_id
```

同一分组读取所有成员 `Factor.InputColumns` 的去重并集，列名按字典序发送给 Storage。每个因子进入 Python 前再投影成自己声明的列和顺序。

### 0.3 流水线边界

```text
轻量任务描述
    -> View read pool（view_read_workers）
    -> bounded prepared-task queue（内部容量）
    -> Python worker pool（python_workers）
    -> Go 结果校验与 WriteFactorPatch
```

- View read worker 只负责 `ReadPeriodChunk`，不调用 Python，不写结果。
- Python subprocess 只接收已经准备好的 DataFrame 并计算；Go 继续校验和写回。
- prepared queue 容量派生为 `max(1, 2 * python_workers)`，不增加第三个配置。
- 队列满时 producer 阻塞投递并停止继续预取，形成背压。
- 队列保存共享 `RangeChunk` 引用，不预先为全部因子复制 DataFrame；投影延迟到计算任务获得 Python 执行机会时完成。

### 0.4 滑动窗口而不是严格批次

任意 read slot 完成后立即投递其 prepared tasks，并立即补入下一个标的。不得等待同一批全部读取完成。慢标的只占一个 read slot，不形成 batch barrier。

### 0.5 超时与重试

- 每次 View RPC 使用独立 `context.WithTimeout`。
- timeout 或 retryable RPC 错误不在当前 read worker 原地连续重试；分组追加到 pending 队尾。
- 每个分组固定最多两次尝试：首次加队尾重试一次，不增加 retry 配置。
- non-retryable 校验、字段和协议错误直接终态失败。
- 第二次仍失败时，该组全部因子不进入 Python，返回同一 read error。
- `ViewReadyRunner` 继续逐任务清理旧输出，并把 subject 加入对应 binding 的 `failed_subjects`，周期为 `degraded`。
- 父 context 取消后不发起新读取，取消在途 RPC，并将未终态任务返回 `context.Canceled`。

### 0.6 配置

```yaml
engine:
  python_workers: 100
  view_read_workers: 16
  view_read_timeout_ms: 10000
  task_timeout_ms: 30000
```

- `python_workers`：Python 计算进程上限。
- `view_read_workers`：不同 read groups 的最大并发读取数；允许大于 `python_workers`，默认 16，不默认放大到 100。
- `view_read_timeout_ms`：单次 Storage View RPC 超时，默认 10 秒。
- `task_timeout_ms`：现有 Python 单任务超时，含义不变。
- `run-once` 固定 `python_workers=1`、`view_read_workers=1`，但复用同一读取超时。

## 1. 文件职责

### 配置与装配

- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/test/contract/test-deploy-moox-factor.sh`

### 读取流水线

- Create: `modules/factor/internal/taskrunner/read_pipeline.go`
- Create: `modules/factor/internal/taskrunner/read_pipeline_test.go`
- Modify: `modules/factor/internal/taskrunner/service.go`
- Modify: `modules/factor/internal/taskrunner/run_all_test.go`
- Modify: `modules/factor/internal/taskrunner/service_test.go`

`read_pipeline.go` 只拥有分组、列并集、滑动窗口、timeout、队尾重试和 prepared-task 投递；计算、校验和写回继续留在 `service.go`。

### 周期语义、文档和验收

- Modify: `modules/factor/internal/trigger/view_ready_runner_test.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/因子视图驱动计算设计.md`
- Modify: `modules/factor/README.md`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `modules/factor/test/view_driven_e2e_test.go`
- Modify: `modules/factor/test/storage_e2e_test.go`

不修改 Storage marker protobuf，不新增 Factor task 持久化表，不修改前端状态 RPC 字段。

## 2. 实施任务

### Task 1: 增加独立 View 读取配置

**Files:**
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`

- [x] **Step 1: 写配置失败测试**

```go
func TestDefaultViewReadPipelineConfig(t *testing.T) {
    cfg := Default()
    require.Equal(t, 100, cfg.Engine.PythonWorkers)
    require.Equal(t, 16, cfg.Engine.ViewReadWorkers)
    require.Equal(t, 10000, cfg.Engine.ViewReadTimeoutMS)
}

func TestViewReadPipelineEnvOverrides(t *testing.T) {
    t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS", "24")
    t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS", "7500")
    cfg := Default()
    cfg.applyEnv()
    require.Equal(t, 24, cfg.Engine.ViewReadWorkers)
    require.Equal(t, 7500, cfg.Engine.ViewReadTimeoutMS)
}

func TestInvalidViewReadPipelineEnvKeepsDefaults(t *testing.T) {
    t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS", "0")
    t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS", "-1")
    cfg := Default()
    cfg.applyEnv()
    require.Equal(t, 16, cfg.Engine.ViewReadWorkers)
    require.Equal(t, 10000, cfg.Engine.ViewReadTimeoutMS)
}
```

- [x] **Step 2: 运行测试确认新字段尚不存在**

Run: `cd modules/factor && go test ./internal/bootstrap -run 'Test(DefaultViewReadPipeline|ViewReadPipelineEnv|InvalidViewReadPipeline)' -count=1`

Expected: FAIL，编译错误包含 `ViewReadWorkers undefined` 或 `ViewReadTimeoutMS undefined`。

- [x] **Step 3: 实现字段、默认值和环境变量**

```go
type EngineConfig struct {
    PythonBin         string `yaml:"python_bin"`
    WorkerPath        string `yaml:"worker_path"`
    FactorsDir        string `yaml:"factors_dir"`
    PythonWorkers     int    `yaml:"python_workers"`
    ViewReadWorkers   int    `yaml:"view_read_workers"`
    ViewReadTimeoutMS int    `yaml:"view_read_timeout_ms"`
    TaskTimeoutMS     int    `yaml:"task_timeout_ms"`
}

const (
    defaultPythonWorkers     = 100
    defaultViewReadWorkers   = 16
    defaultViewReadTimeoutMS = 10000
)
```

`Default/applyDefaults` 使用这些默认值；`applyEnv` 仅接受大于 0 的整数：

```go
if v := os.Getenv("MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS"); v != "" {
    if workers, err := strconv.Atoi(v); err == nil && workers > 0 {
        c.Engine.ViewReadWorkers = workers
    }
}
if v := os.Getenv("MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS"); v != "" {
    if timeoutMS, err := strconv.Atoi(v); err == nil && timeoutMS > 0 {
        c.Engine.ViewReadTimeoutMS = timeoutMS
    }
}
```

- [x] **Step 4: 更新 YAML 和 run-once 边界**

`app.yaml` 使用 100/16/10000；`run_once.go` 的 runtime config 强制：

```go
PythonWorkers:   1,
ViewReadWorkers: 1,
ViewReadTimeout: time.Duration(cfg.Engine.ViewReadTimeoutMS) * time.Millisecond,
```

测试断言服务 YAML 即使为 100/16，run-once 仍解析为 1/1。

- [x] **Step 5: 验证并提交**

Run: `cd modules/factor && go test ./internal/bootstrap ./cmd/cli -count=1`

Expected: PASS。

```bash
git add modules/factor/internal/bootstrap/config.go modules/factor/internal/bootstrap/config_test.go modules/factor/config/app.yaml modules/factor/cmd/cli/run_once.go modules/factor/cmd/cli/run_once_test.go
git commit -m "feat(factor): configure View read pipeline"
```

### Task 2: 建立严格读取分组和延迟投影

**Files:**
- Create: `modules/factor/internal/taskrunner/read_pipeline.go`
- Create: `modules/factor/internal/taskrunner/read_pipeline_test.go`
- Modify: `modules/factor/internal/taskrunner/service.go`

- [x] **Step 1: 写分组和列并集失败测试**

```go
func TestBuildPeriodReadGroupsUsesExactReadIdentity(t *testing.T) {
    base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
    bias := oneBarTask("BTC-USDT", base)
    bias.PeriodTime = base.Unix()
    bias.TriggerType = "view_ready"
    bias.TriggerEventID = "ready-1"
    bias.LookbackPeriods = 20
    bias.Factor.InputColumns = []string{"close"}
    cci := bias
    cci.Factor.FactorID = "cci"
    cci.Factor.InputColumns = []string{"high", "low", "close"}
    longer := bias
    longer.Factor.FactorID = "bias-60"
    longer.LookbackPeriods = 60

    groups, singles := buildPeriodReadGroups([]Task{bias, cci, longer})
    require.Len(t, groups, 2)
    require.Empty(t, singles)
    require.Equal(t, []string{"close", "high", "low"}, groups[0].columns)
    require.Len(t, groups[0].members, 2)
}
```

再增加不同 `trigger_event_id` 不共享、`PeriodTime <= 0` 返回 singles 的断言。

- [x] **Step 2: 写延迟投影失败测试**

```go
func TestPreparedTaskProjectsOnlyAtComputeTime(t *testing.T) {
    chunk := frameChunkWithColumns([]string{"close", "high", "low"}, [][]any{{100.0, 105.0, 95.0}})
    prepared := preparedTask{
        task: Task{FactorTask: engine.FactorTask{Factor: engine.FactorSpec{InputColumns: []string{"close"}}}},
        shared: chunk,
    }
    projected, err := prepared.project()
    require.NoError(t, err)
    require.Equal(t, []string{"close"}, projected.Frame.Columns)
    require.Same(t, chunk, prepared.shared)
}
```

- [x] **Step 3: 运行测试确认类型不存在**

Run: `cd modules/factor && go test ./internal/taskrunner -run 'Test(BuildPeriodReadGroups|PreparedTaskProjects)' -count=1`

Expected: FAIL，包含 `buildPeriodReadGroups undefined` 或 `preparedTask undefined`。

- [x] **Step 4: 创建领域类型并迁移现有共享读取 identity**

```go
type periodReadGroup struct {
    key     periodReadKey
    columns []string
    members []indexedTask
    attempt int
}

type indexedTask struct {
    index int
    task  Task
}

type preparedTask struct {
    index  int
    task   Task
    shared *storageio.RangeChunk
}

func (p preparedTask) project() (*storageio.RangeChunk, error) {
    return projectRangeChunk(p.shared, p.task.Factor.InputColumns)
}
```

从 `service.go` 迁移现有 `periodReadKey` 和 column union；`PeriodTime <= 0` 不进入 period pipeline。

- [x] **Step 5: 验证并提交**

Run: `cd modules/factor && go test ./internal/taskrunner -run 'Test(BuildPeriodReadGroups|PreparedTaskProjects|RunAllSharesPeriodRead)' -count=1`

Expected: PASS。

```bash
git add modules/factor/internal/taskrunner/read_pipeline.go modules/factor/internal/taskrunner/read_pipeline_test.go modules/factor/internal/taskrunner/service.go
git commit -m "refactor(factor): model shared period View reads"
```

### Task 3: 实现滑动窗口和有界背压

**Files:**
- Modify: `modules/factor/internal/taskrunner/read_pipeline.go`
- Modify: `modules/factor/internal/taskrunner/read_pipeline_test.go`

- [x] **Step 1: 写无 batch barrier 的失败测试**

设置 A 阻塞、B/C 立即成功，读取并发为 2。B 完成后必须在 A 释放前启动 C：

```go
func TestReadPipelineRefillsWindowWithoutWaitingForSlowGroup(t *testing.T) {
    storage := newControlledPeriodStorage()
    storage.block("A")
    runner := newPipelineHarness(2, time.Second, storage)
    done := runner.start(groupsForSubjects("A", "B", "C"))
    require.Eventually(t, func() bool { return storage.started("A") && storage.started("B") }, time.Second, time.Millisecond)
    require.Eventually(t, func() bool { return storage.started("C") }, time.Second, time.Millisecond)
    require.False(t, storage.released("A"))
    storage.release("A")
    require.NoError(t, <-done)
    require.LessOrEqual(t, storage.maxConcurrent(), int64(2))
}
```

- [x] **Step 2: 写 prepared queue 背压测试**

prepared queue 容量为 1 并暂停 consumer，断言 producer 不能把所有 groups 的 Frame 全部准备进内存；恢复 consumer 后正常结束。

- [x] **Step 3: 实现 coordinator 和 read workers**

```go
type readPipelineConfig struct {
    workers     int
    timeout     time.Duration
    maxAttempts int
}

type readOutcome struct {
    group *periodReadGroup
    chunk *storageio.RangeChunk
    err   error
}
```

Coordinator 始终保持 `inflight <= workers`。每收到一个 outcome 立即释放 slot：success 立即投递 group members；terminal error 填充所有成员；然后立刻从 pending 头部补充下一 group。不得等待其他 inflight reads。

每个请求必须使用：

```go
attemptCtx, cancel := context.WithTimeout(ctx, p.config.timeout)
defer cancel()
chunk, err := p.service.readPeriodGroup(attemptCtx, group)
```

- [x] **Step 4: 验证并提交**

Run: `cd modules/factor && go test ./internal/taskrunner -run 'TestReadPipeline(Refills|Backpressure)' -count=1`

Expected: PASS。

```bash
git add modules/factor/internal/taskrunner/read_pipeline.go modules/factor/internal/taskrunner/read_pipeline_test.go
git commit -m "feat(factor): stream View reads through a sliding window"
```

### Task 4: 实现超时释放、队尾重试和失败隔离

**Files:**
- Modify: `modules/factor/internal/taskrunner/read_pipeline.go`
- Modify: `modules/factor/internal/taskrunner/read_pipeline_test.go`
- Modify: `modules/factor/internal/taskrunner/run_all_test.go`

- [x] **Step 1: 写超时后继续其他标的测试**

```go
func TestReadTimeoutMovesGroupToTailAndContinuesOtherSubjects(t *testing.T) {
    storage := &timeoutAwarePeriodStorage{slowSubject: "SLOW"}
    runner := newPipelineHarness(1, 20*time.Millisecond, storage)
    results := runner.run(groupsForSubjects("SLOW", "FAST-1", "FAST-2"))
    require.Equal(t, []string{"SLOW#1", "FAST-1#1", "FAST-2#1", "SLOW#2"}, storage.startOrder())
    require.ErrorIs(t, results.forSubject("SLOW"), context.DeadlineExceeded)
    require.NoError(t, results.forSubject("FAST-1"))
    require.NoError(t, results.forSubject("FAST-2"))
}
```

- [x] **Step 2: 写 non-retryable 和 cancel 测试**

- `engine.NonRetryableError` 只尝试一次；
- 父 context cancel 后不启动下一个 subject；
- group 最终失败时所有 member indexes 都收到 error；
- 重试成功时每个 member 只投递一次，不接受旧 attempt 的迟到结果。

- [x] **Step 3: 实现统一判定和队尾策略**

```go
func retryRead(parent context.Context, err error) bool {
    if err == nil || errors.Is(parent.Err(), context.Canceled) {
        return false
    }
    return errors.Is(err, context.DeadlineExceeded) || isRetryable(err)
}
```

retryable 且 `attempt < 2` 时只执行 `pending = append(pending, group)`；禁止 read worker 内部 `for attempt`。每个 outcome 带 attempt generation，coordinator 只接受 group 当前 generation，丢弃过期 outcome。

- [x] **Step 4: race 验证并提交**

Run: `cd modules/factor && go test -race ./internal/taskrunner -run 'Test(ReadTimeout|NonRetryableRead|ReadPipelineCancellation)' -count=1`

Expected: PASS，无数据竞争。

```bash
git add modules/factor/internal/taskrunner/read_pipeline.go modules/factor/internal/taskrunner/read_pipeline_test.go modules/factor/internal/taskrunner/run_all_test.go
git commit -m "feat(factor): isolate timed out View reads"
```

### Task 5: 将 RunAll 改成读算流水线

**Files:**
- Modify: `modules/factor/internal/taskrunner/service.go`
- Modify: `modules/factor/internal/taskrunner/run_all_test.go`
- Modify: `modules/factor/internal/taskrunner/service_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/cmd/cli/run_once.go`

- [x] **Step 1: 写核心 overlap 失败测试**

```go
func TestRunAllOverlapsNextSubjectReadWithPythonExecution(t *testing.T) {
    storage := newPipelineStorage()
    exec := newBlockingExecutor("BTC")
    runner := NewService(2, storage, exec, WithViewReadConfig(1, time.Second))
    tasks := twoFactorsPerSubject("BTC", "ETH")
    done := make(chan []Result, 1)
    go func() { done <- runner.RunAll(context.Background(), tasks) }()
    <-exec.entered("BTC")
    require.Eventually(t, func() bool { return storage.read("ETH") }, time.Second, time.Millisecond)
    exec.release("BTC")
    requireAllSucceeded(t, <-done)
}
```

这是核心验收：BTC Python 未完成时，ETH View read 已开始或完成。

- [x] **Step 2: 增加 Service 配置 option**

```go
func WithViewReadConfig(workers int, timeout time.Duration) Option {
    return func(service *Service) {
        if workers > 0 { service.viewReadWorkers = workers }
        if timeout > 0 { service.viewReadTimeout = timeout }
    }
}
```

未提供 option 时默认 1 个 read worker、10 秒，保证现有测试构造器确定。

- [x] **Step 3: 拆分 prepared execution**

`executePrepared` 在获得 prepared task 后才获取对应 FactorGate、执行 TaskValidator、投影列、调用 Python、校验和写回。它不得再次读取 View。通用 `Run` 和 `PeriodTime <= 0` range task 保留现有 chunk loop。

- [x] **Step 4: 改写 RunAll**

固定流程：

1. 创建与输入同长度的 ordered results；
2. 建立 period read groups；
3. pending 增加全部 tasks；
4. 启动 `python_workers` 个 prepared consumers；
5. 创建容量 `max(1, 2*python_workers)` 的 prepared queue；
6. 启动 read pipeline；
7. read terminal error 直接填写 group member results；
8. consumer 执行 prepared task 并填写原 index；
9. read pipeline 结束后关闭 queue，等待 consumers，返回 results。

`ActiveTasks` 只在 prepared consumer 开始执行时增加，View read 不计为执行中；`PendingTasks` 包括尚未读取或尚未获得计算机会的组合，终态时减少。

- [x] **Step 5: 装配生产和 run-once**

```go
runner = taskrunner.NewService(cfg.Engine.PythonWorkers, storage, pythonPool,
    taskrunner.WithViewReadConfig(
        cfg.Engine.ViewReadWorkers,
        time.Duration(cfg.Engine.ViewReadTimeoutMS)*time.Millisecond,
    ),
    taskrunner.WithDatasetMetrics(runMetrics),
    taskrunner.WithFactorGate(factorGate),
    taskrunner.WithTaskValidator(newTaskValidator(factorRepo, bindingRepo)),
)
```

- [x] **Step 6: race 验证并提交**

Run: `cd modules/factor && go test -race ./internal/taskrunner ./internal/bootstrap ./cmd/cli -count=1`

Expected: PASS，overlap 测试证明边读边算。

```bash
git add modules/factor/internal/taskrunner modules/factor/internal/bootstrap/bootstrap.go modules/factor/cmd/cli/run_once.go
git commit -m "feat(factor): pipeline View reads into Python workers"
```

### Task 6: 锁定周期失败、清理和 Marker 语义

**Files:**
- Modify: `modules/factor/internal/trigger/view_ready_runner_test.go`
- Modify: `modules/factor/internal/rpc/service_test.go`

- [x] **Step 1: 写单标的读取失败测试**

BTC/ETH 两个 subjects、bias/cci 两个 bindings。BTC 两个任务返回共享 timeout，ETH 成功。断言只清理 BTC 两个输出，两个 binding 都记录 BTC failed，Marker degraded，ETH 结果保留。

- [x] **Step 2: 写 Marker 等待全部写回测试**

阻塞 ETH `WriteFactorPatch`，阻塞期间 `ReportFactorPeriodComputed` 调用数必须为 0；释放后恰好为 1。

- [x] **Step 3: 保护既有入口**

定向断言：

- realtime 无 executable binding：ACK，不读 View，不追加结果 Marker；
- pending binding：返回 `ErrBindingNotReady`；
- Recalc 指定 factor：只生成目标 binding members；
- Recalc 无匹配 binding：返回 `ErrNoExecutableBinding`；
- lifecycle mutation 仍与整次周期 OperationGate 互斥。

- [x] **Step 4: race 验证并提交**

Run: `cd modules/factor && go test -race ./internal/trigger/... ./internal/rpc -count=1`

Expected: PASS。

```bash
git add modules/factor/internal/trigger modules/factor/internal/rpc
git commit -m "test(factor): preserve period semantics across read pipeline"
```

### Task 7: 部署配置和结构化观测

**Files:**
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/test/contract/test-deploy-moox-factor.sh`
- Modify: `modules/factor/internal/taskrunner/read_pipeline.go`
- Modify: `modules/factor/internal/taskrunner/read_pipeline_test.go`

- [x] **Step 1: 写部署合同**

`start.sh` 必须包含默认 16 和 10000 的两个 FACTOR_ENV；打包 YAML 必须包含：

```bash
grep -Fq 'view_read_workers: 16' "${UNPACKED}/factor/config/app.yaml"
grep -Fq 'view_read_timeout_ms: 10000' "${UNPACKED}/factor/config/app.yaml"
```

- [x] **Step 2: 更新 FACTOR_ENV**

在 `MOOX_FACTOR_ENGINE_PYTHON_WORKERS` 后加入：

```bash
"MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS=${MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS:-16}"
"MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS=${MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS:-10000}"
```

run-once wrapper 不透传这两个服务并发值。

- [x] **Step 3: 增加安全日志**

每个 attempt 记录：space、view、subject、freq、period、lookback、attempt、result、elapsed_ms、column_count。timeout 重试额外记录 `retry_position=tail`。不得输出 AuthInfo、HMAC、K 线内容或 DataFrame。

- [x] **Step 4: 验证并提交**

Run: `bash scripts/test/contract/test-deploy-moox-factor.sh`

Expected: PASS。

```bash
git add scripts/deploy/deploy-moox.sh scripts/test/contract/test-deploy-moox-factor.sh modules/factor/internal/taskrunner/read_pipeline.go modules/factor/internal/taskrunner/read_pipeline_test.go
git commit -m "chore(factor): deploy and observe View read workers"
```

### Task 8: 更新设计文档并完成 E2E

**Files:**
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/因子视图驱动计算设计.md`
- Modify: `modules/factor/README.md`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `modules/factor/test/view_driven_e2e_test.go`
- Modify: `modules/factor/test/storage_e2e_test.go`

- [x] **Step 1: 更新执行模型**

删除“python_workers 是唯一并发配置”“一个 worker 覆盖读算写完整生命周期”“禁止提前读取 DataFrame”的过时描述，替换成滑动读取、prepared queue、队尾 timeout retry 和 Marker 等待终态。配置表加入两个新字段。

- [x] **Step 2: 增加 process-local 流水线 E2E**

3 subjects × 2 factors：A 的 Python 阻塞时 B/C View read 已发生；B 首次 timeout、队尾第二次成功；成功场景 6 个结果 complete。另建 B 两次 timeout 场景，断言只清 B 的两个输出且 Marker degraded。

- [x] **Step 3: 扩展真实 Storage happy path**

至少 2 subjects × 2 factors，在任何 run-once 修复前依次验证 Source View、Result Dataset、Result View、`FactorPeriodComputed` 和 `ViewFactorPeriodReady`。不按完成顺序断言，只校验完整集合与 final-ready 晚于结果可读。

- [x] **Step 4: 完整验证**

Run:

```bash
(cd modules/factor && go test -race ./internal/taskrunner ./internal/trigger/... ./internal/rpc ./internal/bootstrap ./cmd/cli ./test -count=1)
(cd modules/factor && go test ./... -count=1)
(cd modules/factor && go vet ./...)
bash scripts/test/contract/test-deploy-moox-factor.sh
bash scripts/test/e2e/test-factor-view-ready-e2e.sh
./scripts/test/contract/test-go-workspace.sh
git diff --check
```

Expected: 全部 PASS；既有 workspace 失败必须记录原始命令和错误，不能用 focused PASS 代替。

- [x] **Step 5: 部署环境真实 E2E**

```bash
MOOX_RUN_REAL_FACTOR_E2E=1 MOOX_DEPLOY_ROOT=/home/ubuntu/moox/prod bash scripts/test/e2e/test-factor-view-ready-e2e.sh
```

Expected: PASS，并保存 report -> source-ready -> read pipeline -> computed marker -> final-ready 时间线。

- [x] **Step 6: 记录性能对比**

相同数据、周期和 `python_workers` 下记录 subjects、bindings、read workers、周期总耗时、View read p50/p95、Python 活跃度、timeout 数和最大 RSS。验收不要求固定倍数，但必须证明读算重叠、并发有界、timeout 不阻塞、内存不随 `N*M` 无界增长。

- [x] **Step 7: 提交文档和 E2E**

```bash
git add docs/因子计算模块设计.md docs/因子视图驱动计算设计.md modules/factor/README.md modules/factor/docs/realtime-verification.md modules/factor/test/view_driven_e2e_test.go modules/factor/test/storage_e2e_test.go
git commit -m "docs(factor): verify pipelined View reads"
```

## 3. 明确不做

- 不增加 Factor timer、cron、持久化任务表或分布式任务队列。
- 不让 Collector 参与 Factor 内部 View read 调度。
- 不建立严格读取批次屏障。
- 不对每个因子重复读取同一标的 K 线。
- 不允许无限 prepared queue 或预先复制全部 `N*M` 个 DataFrame。
- 不增加 `ready_queue_capacity`、`read_retry_count`、`subject_batch_size` 或 write worker 配置。
- 不因单个 subject timeout 取消其他 subjects。
- 不无限重试；两次失败后 degraded 收口。
- 不修改三个 ready/computed 事件 protobuf。
- 不改变 Result Dataset、Result View、manifest 或稳定 source ID 规则。

## 4. 最终验收清单

- [x] 相同 subject/period/lookback/trigger 的多个因子只读取一次 View。
- [x] 共享读取列是成员 input columns 的规范化并集。
- [x] 每个因子进入 Python 前按声明顺序投影。
- [x] View read 与 Python execution 时间重叠。
- [x] 任意 read 完成立即投递，不等待慢请求。
- [x] timeout 释放 slot，并在其他 pending subjects 后重试。
- [x] 每个 read group 最多尝试两次。
- [x] 单 subject 最终失败只降级该 subject。
- [x] prepared queue 有界，Frame 不按全部组合预复制。
- [x] `active_tasks` 不再把 View read 等待计为执行中。
- [x] Marker 在全部读取、计算、清理和写回终态前不会发布。
- [x] realtime、Recalc、run-once 和生命周期测试通过。
- [x] 部署配置可独立设置 read workers 和 timeout。
- [x] process-local E2E、真实 Storage E2E、race、deploy contract 和 workspace 验证均有执行记录。

## 5. 执行记录（2026-08-10）

实现已完成。由于当前分支包含多模块共享改动，本轮没有按计划中的示例命令拆分或创建 Git commit；代码、测试和文档均保留在当前工作树中。

### 5.1 最终实现补充

- Factor DataView 请求下推分组列并集，并使用 `TotalMode_NONE`；读取 RPC 自身不重试，由 TaskRunner 统一控制两次队尾尝试。
- Storage DuckDB 将唯一逻辑列后缀解析到物理 qualified column；缺失或歧义 fail closed。显式投影的 SQL NULL 保留 FieldId，确保 Factor 的 exact runtime column、qualified alias 和 exact NULL 语义一致。
- Recalc 指定 `factor_id` 时只生成目标 binding 的任务和 Marker state；run-once 读取同样应用 `view_read_timeout`。
- 增加同形滑动窗口测试：A 读取阻塞时，B 完成后 C 在 A 释放前补入空闲 read slot。
- 真实 E2E 在收到 `ViewFactorPeriodReady` 后立即执行第一次 Result View 查询，并要求两标的、两因子的精确结果已经可读，避免轮询掩盖提前发布。

### 5.2 本地验证

以下命令通过：

```text
go test -race ./internal/taskrunner ./internal/storageio ./internal/trigger/... ./internal/rpc ./internal/bootstrap ./cmd/cli ./test -count=1
go test ./... -count=1
go vet ./...
CGO_ENABLED=1 go test -race ./internal/service/viewindex/duckdb ./internal/service/view -count=1
bash scripts/test/contract/test-deploy-moox-factor.sh
bash scripts/test/e2e/test-factor-view-ready-e2e.sh
./scripts/test/contract/test-go-workspace.sh
git diff --check
```

独立 `codeCR` 在最终树上复核了 TaskRunner、StorageIO、DuckDB projection、DataFrame alias、Recalc 和 Marker 链路，结论为无剩余 P0-P2。

### 5.3 部署环境 E2E 与性能记录

目标：`106.53.107.122`，2 subjects × 2 factors，`view_read_workers=16`，`view_read_timeout_ms=10000`。目标机只有约 3.6 GiB 内存，`python_workers=100` 会触发 OOM，因此部署验收使用运行时覆盖 `python_workers=8`；代码和默认配置仍保持 100。

真实 `TestFactorRealStorageE2E` 在最终 Factor 与 Storage View 二进制上通过，耗时 109.44 秒（包含资源创建、View 构建、事件链、run-once smoke 和清理）。事件链验证：

```text
DatasetPeriodCollected -> ViewSourcePeriodReady -> 2 shared View reads
-> 4 Python factor tasks -> FactorPeriodComputed(2 complete bindings)
-> ViewFactorPeriodReady -> first Result View query readable
```

本次计算段观测：

| 指标 | 结果 |
|---|---:|
| subjects / bindings / combinations | 2 / 2 / 4 |
| shared View reads | 2 |
| View read elapsed | 4 ms, 6 ms |
| View read p50 / p95 | 5 ms / 6 ms |
| read timeout / retry | 0 / 0 |
| Python task elapsed | 43 ms, 483 ms, 493 ms, 493 ms |
| Factor + Python 当前 RSS | 约 305.2 MiB |
| 各进程 VmHWM 合计上界 | 约 318.3 MiB |

两次 View read 只对应两个 subject，而不是四个 subject-factor 组合；四个 Python task 的完成时间重叠，证明共享读取、读算流水线和全局 Python worker 并发均已生效。
