# Factor Global Combination Concurrency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保持 `ViewSourcePeriodReady` 事件驱动不变，将一个周期内全部“标的 × 因子绑定”任务交给单进程全局 Python worker 池滚动并行执行，仅用 `engine.python_workers` 控制端到端并发，并在全部任务写回终态后发布 `FactorPeriodComputed`。

**Architecture:** Factor 不增加定时器，也不再维护可配置的 Go scheduler 队列或显式标的批次。`ViewReadyRunner` 对一个 source-ready 周期生成确定性的笛卡尔积任务列表，`TaskRunner` 用固定数量的 Go worker 执行完整的“读 View -> Python -> 写结果”流程；这个数量与 Python 进程上限都取自同一个 `python_workers`。Python 进程池只预热一个进程，其余按需启动，并由“空闲 worker 领取下一个任务”，取消按 `subject_id` 固定分片，使同一标的的多个因子可以并行。

**Tech Stack:** Go 1.25、tRPC-Go、NATS JetStream、Python 3、pandas、MooX `packages/pyruntime`、Vue 3/Vitest。

---

## 0. 决策与边界

本计划锁定以下结论，实施时不再引入第二套并发参数：

- 实时入口仍是 `ViewSourcePeriodReady` JetStream 事件；Factor 不增加 cron、ticker 或周期轮询。
- 一个任务的唯一业务粒度是 `(space_id, source_view_id, binding_id, subject_id, frequency, period_time)`。
- 同一标的的多个因子允许并行；不同标的、不同因子共享同一个全局 worker 池。
- 唯一并发配置是 `engine.python_workers`，正式默认值设为 `100`。
- 并发令牌覆盖完整任务生命周期：读取 Source View、调用 Python、校验、写 Result Dataset。禁止先为全部任务读取 DataFrame，再在 Python 前排队。
- 不配置 `subject_batch_size`、`combination_concurrency`、`queue_capacity` 或 Go worker 数量。
- 不设置严格批次屏障。固定 worker 会形成滚动窗口：一个任务结束后立即领取下一个，不等待同一波的慢任务。
- source-ready consumer 保留 `MaxAckPending=1`；一个周期的所有组合任务终态且 `FactorPeriodComputed` 成功追加后才 ACK。
- 单任务失败不取消其他组合；失败任务清理旧输出，对应 binding 标记 `degraded`，其他任务继续完成。
- `RecalcFactor` 复用同一个进程内池和相同并发限制。短生命周期 `factor run-once` 保持单 worker，避免每次命令启动 100 个 pandas 进程。
- 保留现有 `OperationGate`、`FactorGate`、TaskValidator、manifest 和幂等写回语义；不增加持久化任务表、分布式锁、DLQ 或多实例协调。
- 本项目不兼容旧 `engine.workers`、`scheduler` YAML 和 `MOOX_FACTOR_ENGINE_WORKERS`；`yaml.KnownFields(true)` 应明确拒绝旧配置。

目标时序：

```text
Storage View
  -> ViewSourcePeriodReady
  -> Factor eventconsumer
  -> ViewReadyRunner 生成 subject-major 的 N x M 个 Task
  -> TaskRunner 的 100 个端到端 worker 滚动执行
       -> ReadRangeChunk
       -> PythonWorkerPool.Execute
       -> WriteFactorPatch
  -> 汇总每个 binding 的 complete/degraded
  -> ReportFactorPeriodComputed
  -> ACK source-ready
```

## 1. 文件职责

### 配置与装配

- `modules/factor/internal/bootstrap/config.go`：只暴露 `EngineConfig.PythonWorkers`，删除 `SchedulerConfig`。
- `modules/factor/config/app.yaml`：正式配置使用 `python_workers: 100`，删除 `scheduler` 节。
- `modules/factor/internal/bootstrap/bootstrap.go`：用同一个 `PythonWorkers` 构建 `PythonWorkerPool` 和 `TaskRunner`，不再 Start/Stop scheduler。
- `scripts/deploy/deploy-moox.sh`、`scripts/test/contract/test-deploy-moox-factor.sh`：透传并验证新环境变量。

### Python worker 分发

- `packages/pyruntime/pool/pool.go`：增加空闲 worker 队列；任意任务等待并领取下一台空闲 worker。
- `packages/pyruntime/pool/pool_test.go`：证明同一 shard key 的两个任务可占用两个 worker，并证明并发不超过池大小。
- `modules/factor/internal/engine/executor.go`：`PythonExecutor` 重命名为 `PythonWorkerPool`，执行时不再按 `SubjectID` 固定分片。
- `modules/factor/internal/engine/executor_test.go`：覆盖状态、动态分发和关闭。

### Go 任务执行

- Create: `modules/factor/internal/taskrunner/task.go`
- Create: `modules/factor/internal/taskrunner/builder.go`
- Create: `modules/factor/internal/taskrunner/builder_test.go`
- Create: `modules/factor/internal/taskrunner/factor_gate.go`
- Create: `modules/factor/internal/taskrunner/factor_gate_test.go`
- Create: `modules/factor/internal/taskrunner/service.go`
- Create: `modules/factor/internal/taskrunner/service_test.go`
- Delete after move: `modules/factor/internal/scheduler/task.go`
- Delete after move: `modules/factor/internal/scheduler/builder.go`
- Delete after move: `modules/factor/internal/scheduler/builder_test.go`
- Delete after move: `modules/factor/internal/scheduler/factor_gate.go`
- Delete after move: `modules/factor/internal/scheduler/factor_gate_test.go`
- Delete after rewrite: `modules/factor/internal/scheduler/service.go`
- Delete after rewrite: `modules/factor/internal/scheduler/service_test.go`
- Delete: `modules/factor/internal/scheduler/queue.go`
- Delete: `modules/factor/internal/scheduler/queue_test.go`

`TaskRunner` 保留现有单任务计算、校验、重试和指标逻辑，新增 `RunAll` 固定 worker 执行；删除生产代码未使用的 `Enqueue/Start/Stop/Drain/WaitIdle/DropQueuedFactor` 和容量溢出语义。

### View-ready 周期编排

- Create: `modules/factor/internal/trigger/view_ready_runner.go`
- Create: `modules/factor/internal/trigger/view_ready_runner_test.go`
- Delete: `modules/factor/internal/trigger/period_executor.go`
- Delete: `modules/factor/internal/trigger/period_executor_test.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/consumer.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/handler.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/recalc.go`

`ViewReadyRunner` 以 subject 为外层、binding 为内层构造任务，统一调用 `TaskRunner.RunAll`，最后按 binding 聚合状态并追加 Marker。

### 状态接口与前端

- Modify: `modules/factor/proto/factor.proto`
- Generated: `modules/factor/proto/factorgen/factor.pb.go`
- Generated: `modules/factor/proto/factorgen/factor.trpc.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `web/src/api/factor/types.ts`
- Modify: `web/src/views/factor/results/index.vue`
- Modify: `web/src/views/factor/__tests__/factor-contract.spec.ts`

删除误导性的 `queue_overflow_count`，状态接口改为 Python worker 数、当前执行任务数和等待任务数。

### 文档和验收

- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/因子视图驱动计算设计.md`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `modules/factor/README.md`
- Modify: `modules/factor/test/view_driven_e2e_test.go`
- Modify: `modules/factor/test/storage_e2e_test.go`
- Modify: `scripts/test/e2e/test-factor-view-ready-e2e.sh`

## 2. 实施任务

### Task 1: 收敛为唯一的 `python_workers` 配置

**Files:**
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/config_test.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`

- [ ] **Step 1: 写配置契约失败测试**

在 `config_test.go` 增加完整 YAML 测试，固定新字段和旧字段拒绝行为：

```go
func writeConfig(t *testing.T, raw string) string {
    t.Helper()
    path := filepath.Join(t.TempDir(), "app.yaml")
    require.NoError(t, os.WriteFile(path, []byte(raw), 0o644))
    return path
}

func TestLoadUsesPythonWorkersAsOnlyConcurrencySetting(t *testing.T) {
    path := writeConfig(t, `
engine:
  python_workers: 100
`)
    cfg, err := Load(path)
    require.NoError(t, err)
    require.Equal(t, 100, cfg.Engine.PythonWorkers)
}

func TestLoadRejectsLegacyWorkersAndScheduler(t *testing.T) {
    for _, raw := range []string{
        "engine:\n  workers: 24\n",
        "scheduler:\n  queue_capacity: 2048\n",
    } {
        path := writeConfig(t, raw)
        _, err := Load(path)
        require.Error(t, err)
    }
}

func TestPythonWorkersEnvOverride(t *testing.T) {
    t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_WORKERS", "100")
    cfg := Default()
    cfg.applyEnv()
    require.Equal(t, 100, cfg.Engine.PythonWorkers)
}
```

- [ ] **Step 2: 运行测试确认旧结构仍存在**

Run: `cd modules/factor && go test ./internal/bootstrap -run 'TestLoadUsesPythonWorkers|TestLoadRejectsLegacy|TestPythonWorkersEnv' -count=1`

Expected: FAIL，至少包含 `PythonWorkers undefined` 或旧字段未被拒绝。

- [ ] **Step 3: 修改配置结构并删除 SchedulerConfig**

`Config` 和 `EngineConfig` 收敛为：

```go
type Config struct {
    Database DatabaseConfig `yaml:"database"`
    Storage  StorageConfig  `yaml:"storage"`
    EventBus EventBusConfig `yaml:"eventbus"`
    Engine   EngineConfig   `yaml:"engine"`
}

type EngineConfig struct {
    PythonBin     string `yaml:"python_bin"`
    WorkerPath    string `yaml:"worker_path"`
    FactorsDir    string `yaml:"factors_dir"`
    PythonWorkers int    `yaml:"python_workers"`
    TaskTimeoutMS int    `yaml:"task_timeout_ms"`
}

const defaultPythonWorkers = 100
```

`Default/applyDefaults/applyEnv` 统一使用 `PythonWorkers` 和 `MOOX_FACTOR_ENGINE_PYTHON_WORKERS`。删除 `runtime.NumCPU()` 默认值、`SchedulerConfig` 和 `scheduler.max_retry` 校验。

- [ ] **Step 4: 更新正式 YAML 和 run-once 边界**

`modules/factor/config/app.yaml` 使用：

```yaml
engine:
  python_bin: python3
  worker_path: ./pyworker/worker.py
  factors_dir: ./factors
  python_workers: 100
  task_timeout_ms: 30000
```

`runOnceRuntime` 删除 `MaxRetry`，字段改为 `PythonWorkers int`，但 `resolveRunOnceRuntime` 明确赋值 `PythonWorkers: 1`。这条命令是独立短生命周期进程，不启动 100 个 pandas worker。

- [ ] **Step 5: 运行配置和 CLI 测试**

Run: `cd modules/factor && go test ./internal/bootstrap ./cmd/cli -count=1`

Expected: PASS；旧 YAML 字段测试必须失败闭合，新环境变量覆盖为 100。

- [ ] **Step 6: 提交配置改动**

```bash
git add modules/factor/internal/bootstrap/config.go \
  modules/factor/internal/bootstrap/config_test.go \
  modules/factor/config/app.yaml \
  modules/factor/cmd/cli/run_once.go \
  modules/factor/cmd/cli/run_once_test.go
git commit -m "refactor(factor): use one python worker concurrency setting"
```

### Task 2: 将 Python 池改为空闲 worker 动态领取

**Files:**
- Modify: `packages/pyruntime/pool/pool.go`
- Modify: `packages/pyruntime/pool/pool_test.go`
- Modify: `modules/factor/internal/engine/executor.go`
- Modify: `modules/factor/internal/engine/executor_test.go`

- [ ] **Step 1: 写同标的多因子并行失败测试**

在 `pool_test.go` 定义阻塞 worker，两个请求不携带分片亲和性，断言二者在释放前都已进入不同 worker：

```go
type blockingWorker struct {
    entered chan<- string
    release <-chan struct{}
}

func (*blockingWorker) Load(context.Context, process.LoadRequest) error { return nil }
func (w *blockingWorker) Run(_ context.Context, req process.RunRequest) (process.RunResult, error) {
    w.entered <- req.RequestID
    <-w.release
    return process.RunResult{Meta: []byte(`{"ok":true}`)}, nil
}
func (*blockingWorker) State() process.State { return process.StateReady }
func (*blockingWorker) Close() error         { return nil }

func TestRunAnyLoadedManyUsesTwoFreeWorkersForSameShard(t *testing.T) {
    entered := make(chan string, 2)
    release := make(chan struct{})
    workers := []process.Worker{
        &blockingWorker{entered: entered, release: release},
        &blockingWorker{entered: entered, release: release},
    }
    var factoryMu sync.Mutex
    next := 0
    p := New(2, func(context.Context) (process.Worker, error) {
        factoryMu.Lock()
        defer factoryMu.Unlock()
        worker := workers[next]
        next++
        return worker, nil
    })

    var wg sync.WaitGroup
    for _, id := range []string{"bias-5", "bias-20"} {
        wg.Add(1)
        go func(id string) {
            defer wg.Done()
            _, err := p.RunAnyLoadedMany(context.Background(), nil, process.RunRequest{RequestID: id})
            require.NoError(t, err)
        }(id)
    }

    require.ElementsMatch(t, []string{"bias-5", "bias-20"}, []string{<-entered, <-entered})
    close(release)
    wg.Wait()
}
```

再加 `TestRunAnyLoadedManyNeverExceedsPoolSize`：沿用上述 `blockingWorker`，启动 3 个请求、2 个 worker，前两次读取 `entered` 后用 50ms `select` 断言第三个未进入；关闭 `release` 后三个 goroutine 全部返回。

- [ ] **Step 2: 运行测试确认缺少动态领取接口**

Run: `cd packages/pyruntime && go test ./pool -run 'TestRunAnyLoadedMany' -count=1`

Expected: FAIL，错误包含 `RunAnyLoadedMany undefined`。

- [ ] **Step 3: 实现空闲 worker 队列**

`Pool` 初始化每个 worker 索引，调用方必须在返回、报错或 context 取消时归还：

```go
type Pool struct {
    workers   []*process.Supervisor
    available chan int
    next      uint64
    mu        sync.Mutex
}

func New(n int, f Factory) *Pool {
    if n < 1 {
        n = 1
    }
    p := &Pool{available: make(chan int, n)}
    for i := 0; i < n; i++ {
        p.workers = append(p.workers, process.NewSupervisor(process.Factory(f), process.SupervisorConfig{}))
        p.available <- i
    }
    return p
}

func (p *Pool) RunAnyLoadedMany(ctx context.Context, loads []process.LoadRequest, run process.RunRequest) (process.RunResult, error) {
    if p == nil || len(p.workers) == 0 {
        return process.RunResult{}, errors.New("pyruntime: empty pool")
    }
    select {
    case index := <-p.available:
        defer func() { p.available <- index }()
        return p.workers[index].RunLoadedMany(ctx, loads, run)
    case <-ctx.Done():
        return process.RunResult{}, ctx.Err()
    }
}
```

保留现有按 shard key 的方法供其他模块使用；Factor 改走 `RunAnyLoadedMany`。

- [ ] **Step 4: 将 `PythonExecutor` 重命名为 `PythonWorkerPool`**

`modules/factor/internal/engine/executor.go` 的公开类型和构造器改为：

```go
type PythonWorkerPool struct {
    workers int
    pool    *pool.Pool
    hello   protocol.Hello
}

func NewPythonWorkerPool(ctx context.Context, workers int, cfg process.Config) (*PythonWorkerPool, error)
```

`Execute` 中使用：

```go
response, err = e.pool.RunAnyLoadedMany(ctx, loads, run)
```

不再将 `task.SubjectID` 作为 worker 路由键。保留 `engine.Executor` 接口，使 TaskRunner 测试继续使用 fake executor。

- [ ] **Step 5: 运行 pool 和 engine 测试及 race**

Run: `cd packages/pyruntime && go test -race ./pool ./process -count=1`

Expected: PASS，同 shard 两请求并行，池大小严格限制为 worker 数。

Run: `cd modules/factor && go test -race ./internal/engine -count=1`

Expected: PASS，`Status().Workers` 返回构造时数量，Close 后不泄漏子进程。

- [ ] **Step 6: 提交动态分发改动**

```bash
git add packages/pyruntime/pool/pool.go packages/pyruntime/pool/pool_test.go \
  modules/factor/internal/engine/executor.go modules/factor/internal/engine/executor_test.go
git commit -m "feat(factor): dispatch combinations to free python workers"
```

### Task 3: 用 `TaskRunner` 替换误导性的 scheduler 和死队列

**Files:**
- Move: `modules/factor/internal/scheduler/task.go` -> `modules/factor/internal/taskrunner/task.go`
- Move: `modules/factor/internal/scheduler/builder.go` -> `modules/factor/internal/taskrunner/builder.go`
- Move: `modules/factor/internal/scheduler/factor_gate.go` -> `modules/factor/internal/taskrunner/factor_gate.go`
- Rewrite: `modules/factor/internal/scheduler/service.go` -> `modules/factor/internal/taskrunner/service.go`
- Move: `modules/factor/internal/scheduler/builder_test.go` -> `modules/factor/internal/taskrunner/builder_test.go`
- Move: `modules/factor/internal/scheduler/factor_gate_test.go` -> `modules/factor/internal/taskrunner/factor_gate_test.go`
- Rewrite: `modules/factor/internal/scheduler/service_test.go` -> `modules/factor/internal/taskrunner/service_test.go`
- Delete: `modules/factor/internal/scheduler/queue.go`
- Delete: `modules/factor/internal/scheduler/queue_test.go`

- [ ] **Step 1: 写 `RunAll` 全局端到端并发测试**

测试沿用迁移后的 `oneBarTask`，并在同一文件增加线程安全的阻塞 Storage。它在读取 View 的入口计数，因此可以证明全局限制覆盖完整任务，而不只是 Python 调用：

```go
type blockingReadStorage struct {
    active  atomic.Int64
    maximum atomic.Int64
    entered chan struct{}
    release chan struct{}
}

func (s *blockingReadStorage) ReadRangeChunk(
    _ context.Context,
    _ storageio.WindowKey,
    start time.Time,
    _ time.Time,
    _ int,
    _ int,
    _ []string,
) (*storageio.RangeChunk, error) {
    current := s.active.Add(1)
    for current > s.maximum.Load() && !s.maximum.CompareAndSwap(s.maximum.Load(), current) {
    }
    s.entered <- struct{}{}
    <-s.release
    s.active.Add(-1)
    return frameChunk([]time.Time{start}), nil
}

func (*blockingReadStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
    return 1, nil
}

func TestRunAllBoundsWholeTaskConcurrency(t *testing.T) {
    storage := &blockingReadStorage{
        entered: make(chan struct{}, 7),
        release: make(chan struct{}),
    }
    runner := NewService(3, storage, &fakeExecutor{})
    base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
    tasks := make([]Task, 7)
    for index := range tasks {
        tasks[index] = oneBarTask(fmt.Sprintf("S%d", index), base)
    }
    done := make(chan []Result, 1)
    go func() { done <- runner.RunAll(context.Background(), tasks) }()

    <-storage.entered
    <-storage.entered
    <-storage.entered
    select {
    case <-storage.entered:
        t.Fatal("fourth task read View before a worker slot was released")
    case <-time.After(50 * time.Millisecond):
    }
    require.EqualValues(t, 3, storage.maximum.Load())
    require.Equal(t, Status{ActiveTasks: 3, PendingTasks: 4}, runner.Status())

    close(storage.release)
    results := <-done
    require.Len(t, results, 7)
    require.Equal(t, Status{}, runner.Status())
}
```

`maximum` 更新实现可拆成私有 `observeMaximum` helper，但测试必须保留“第 4 个任务未提前调用 `ReadRangeChunk`”的断言。

- [ ] **Step 2: 运行测试确认当前 Service 只有队列 worker 语义**

Run: `cd modules/factor && go test ./internal/taskrunner -run TestRunAllBoundsWholeTaskConcurrency -count=1`

Expected: FAIL，package 或 `RunAll` 尚不存在。

- [ ] **Step 3: 移动纯任务文件并统一 package 名**

使用非交互命令移动文件：

```bash
mkdir -p modules/factor/internal/taskrunner
git mv modules/factor/internal/scheduler/task.go modules/factor/internal/taskrunner/task.go
git mv modules/factor/internal/scheduler/builder.go modules/factor/internal/taskrunner/builder.go
git mv modules/factor/internal/scheduler/factor_gate.go modules/factor/internal/taskrunner/factor_gate.go
```

将 package 声明改为 `package taskrunner`；保留 `Task`、`TaskScope`、`BuildTask`、`DeterministicTaskID`、`FactorGate` 和 `OperationGate` 的行为。

- [ ] **Step 4: 实现无可配置队列的 TaskRunner**

核心接口固定为：

```go
const maxRetry = 1

type Result struct {
    Task Task
    Err  error
}

type Status struct {
    ActiveTasks  int
    PendingTasks int
}

type Service struct {
    workers       int
    storage       StorageIO
    exec          engine.Executor
    metrics       DatasetRunObserver
    factorGate    *FactorGate
    taskValidator TaskValidator
    active        atomic.Int64
    pending       atomic.Int64
}

func NewService(workers int, storage StorageIO, exec engine.Executor, opts ...Option) *Service {
    if workers < 1 {
        workers = 1
    }
    return &Service{workers: workers, storage: storage, exec: exec, factorGate: NewFactorGate()}
}

func (s *Service) RunAll(ctx context.Context, tasks []Task) []Result {
    results := make([]Result, len(tasks))
    if len(tasks) == 0 {
        return results
    }
    workerCount := min(s.workers, len(tasks))
    jobs := make(chan int)
    s.pending.Add(int64(len(tasks)))
    var wg sync.WaitGroup
    for worker := 0; worker < workerCount; worker++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for index := range jobs {
                s.pending.Add(-1)
                s.active.Add(1)
                results[index] = Result{Task: tasks[index], Err: s.runTracked(ctx, tasks[index])}
                s.active.Add(-1)
            }
        }()
    }
    for index := range tasks {
        jobs <- index
    }
    close(jobs)
    wg.Wait()
    return results
}
```

实现时发送 jobs 必须监听 `ctx.Done()`；未派发任务写入 `ctx.Err()`，并正确扣减 pending。`Run` 和 `RunAll` 都复用私有 `runValidated`，避免状态重复计数。

- [ ] **Step 5: 保留现有计算正确性并删除死队列**

把旧 `service.go` 中以下逻辑原样迁移到 `taskrunner.Service`：

- FactorGate 和 TaskValidator；
- `ReadRangeChunk`、空结果清理、target range 过滤；
- 结果校验、`WriteFactorPatch`；
- `RetryableError` 最多额外重试一次；
- Dataset 运行指标和结构化完成日志。

删除 `Enqueue/Start/Stop/Drain/WaitIdle/DropQueuedFactor`、`ErrQueueFull`、taskKey、HashSubject、queue capacity 和 overflow 计数。它们没有生产 enqueue 调用，并且与新的 source-ready 同步执行模型冲突。

- [ ] **Step 6: 迁移并精简测试**

移动 builder、gate 和计算正确性测试；删除只验证旧 queue 合并、shard 和 overflow 的测试。新增：

- 7 个任务、3 worker 的端到端并发上限；
- 同一 subject、两个 factor 可同时进入 Executor；
- 一个任务失败不取消其他任务；
- context 取消后未启动任务返回 `context.Canceled`；
- `Status` 最终归零；
- retryable 错误执行两次，non-retryable 错误执行一次。

- [ ] **Step 7: 运行 TaskRunner 测试及 race**

Run: `cd modules/factor && go test -race ./internal/taskrunner -count=1`

Expected: PASS；`MaxActive()` 不超过构造参数，测试结束 active/pending 均为 0。

- [ ] **Step 8: 提交 TaskRunner 重构**

```bash
git add modules/factor/internal/taskrunner modules/factor/internal/scheduler
git commit -m "refactor(factor): replace scheduler with bounded task runner"
```

### Task 4: 将周期执行改为 subject-major 全组合滚动并行

**Files:**
- Move/Rewrite: `modules/factor/internal/trigger/period_executor.go` -> `modules/factor/internal/trigger/view_ready_runner.go`
- Move/Rewrite: `modules/factor/internal/trigger/period_executor_test.go` -> `modules/factor/internal/trigger/view_ready_runner_test.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/consumer.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/handler.go`
- Modify: `modules/factor/test/view_driven_e2e_test.go`

- [ ] **Step 1: 写 N × M 组合和 marker 屏障失败测试**

将现有 `periodFactors` 改成按 FactorID 查询的 map，并增加一个捕获整批任务的 fake runner：

```go
type periodFactors map[string]domain.FactorDef

func (p periodFactors) Get(_ context.Context, factorID string) (*domain.FactorDef, error) {
    factor, ok := p[factorID]
    if !ok {
        return nil, fmt.Errorf("factor %s not found", factorID)
    }
    value := factor
    return &value, nil
}

type blockingCombinationRunner struct {
    tasks   chan []taskrunner.Task
    release chan struct{}
}

func (r *blockingCombinationRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
    copied := append([]taskrunner.Task(nil), tasks...)
    r.tasks <- copied
    <-r.release
    results := make([]taskrunner.Result, len(copied))
    for index := range copied {
        results[index] = taskrunner.Result{Task: copied[index]}
    }
    return results
}

func TestViewReadyRunnerRunsSubjectFactorCartesianProduct(t *testing.T) {
    runner := &blockingCombinationRunner{
        tasks: make(chan []taskrunner.Task, 1), release: make(chan struct{}),
    }
    storage := &periodStorageFake{}
    bindings := periodBindings{rows: []domain.FactorBinding{
        {BindingID: "b-bias5", FactorID: "bias5", SpaceID: "crypto", SourceViewID: "bars_view", ResultDatasetID: "bars_factor", Freq: "1m", Status: domain.BindingStatusEnabled},
        {BindingID: "b-bias20", FactorID: "bias20", SpaceID: "crypto", SourceViewID: "bars_view", ResultDatasetID: "bars_factor", Freq: "1m", Status: domain.BindingStatusEnabled},
    }}
    factors := periodFactors{
        "bias5":  {FactorID: "bias5", Name: "bias_5", SourceHash: "hash-5", Outputs: []string{"bias_5"}, Status: domain.FactorStatusEnabled},
        "bias20": {FactorID: "bias20", Name: "bias_20", SourceHash: "hash-20", Outputs: []string{"bias_20"}, Status: domain.FactorStatusEnabled},
    }
    sut := NewViewReadyRunner(bindings, factors, runner, storage, t.TempDir())
    period := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
    ready := &publicstoragepb.ViewSourcePeriodReady{
        SourceViewId: "bars_view", Frequency: "1m", PeriodTime: period.Unix(),
        Status: "complete", PrimarySubjects: []string{"SOL", "BTC", "ETH"},
        ReadyAt: timestamppb.New(period),
    }

    done := make(chan error, 1)
    go func() {
        done <- sut.Execute(context.Background(), "crypto", "view-ready-1", ready)
    }()

    tasks := <-runner.tasks
    got := make([]string, 0, len(tasks))
    for _, task := range tasks {
        got = append(got, task.SubjectID+"/"+task.Factor.FactorID)
    }
    require.Equal(t, []string{"BTC/bias5", "BTC/bias20", "ETH/bias5", "ETH/bias20", "SOL/bias5", "SOL/bias20"}, got)
    require.Nil(t, storage.marker)

    close(runner.release)
    require.NoError(t, <-done)
    require.Equal(t, "complete", storage.marker.GetStatus())
}
```

测试必须覆盖 subject-major 顺序、6 个组合以及 marker 不早发。

- [ ] **Step 2: 写失败隔离测试**

让 `ETH/bias20` 返回错误，断言其他 5 个任务仍执行；只清理失败任务输出；`bias20.failed_subjects=[ETH]`；group marker 为 `degraded` 且只追加一次。

- [ ] **Step 3: 运行测试确认当前 per-binding goroutine 模型不满足契约**

Run: `cd modules/factor && go test ./internal/trigger -run 'TestViewReadyRunnerRunsSubjectFactorCartesianProduct|TestViewReadyRunnerIsolatesCombinationFailure' -count=1`

Expected: FAIL，`NewViewReadyRunner`/`RunAll` 不存在或任务顺序仍以 binding 为外层。

- [ ] **Step 4: 定义新的 TaskRunner 接口并删除每 binding semaphore**

```go
type CombinationTaskRunner interface {
    RunAll(context.Context, []taskrunner.Task) []taskrunner.Result
}

type ViewReadyRunner struct {
    bindings      PeriodBindingSource
    factors       PeriodFactorSource
    taskRunner    CombinationTaskRunner
    storage       PeriodStorage
    factorsDir    string
    operationGate *taskrunner.OperationGate
    periodMetrics *observability.PeriodMetrics
}
```

删除 `taskConcurrency`、`WithTaskConcurrency`、每 binding 的 goroutine、subject semaphore 和 `PeriodScheduler` 命名。

- [ ] **Step 5: 构造确定性的 subject-major 执行计划**

实现逻辑顺序固定为：

1. 在 `OperationGate` 内读取并按 `binding_id` 排序 executable bindings；
2. 按 `subject_id` 排序 `primary_subjects`；
3. 每个 binding 只读取一次 FactorDef，并建立 `FactorBindingPeriodState`；
4. subject 外层、binding 内层构造任务；
5. upstream failed 或 BindingAllowsSubject=false 的组合不进入 Python，按现有语义 clear 并写入 failed/skipped；
6. 所有可执行任务一次传入 `RunAll`；
7. 按返回 Task.BindingID/SubjectID 聚合错误；
8. 排序每个 state 的 skipped/failed；
9. 所有任务和必要 clear 终态后追加一个 `FactorPeriodComputed`。

组合任务 ID 继续使用 `DeterministicTaskID`，不得加入队列位置或 worker 编号。

- [ ] **Step 6: 更新 eventconsumer 命名但保持事件契约**

`eventconsumer` 内接口从 `PeriodExecutor` 改为：

```go
type ViewReadyExecutor interface {
    Execute(context.Context, string, string, *storagepb.ViewSourcePeriodReady) error
}
```

Handler 仍只解码 `ViewSourcePeriodReady`；成功 ACK，临时/永久输入错误保持当前 RETRY 策略，无 executable binding 时 ACK。不得增加定时触发。

- [ ] **Step 7: 运行 trigger、eventconsumer 和进程内 E2E**

Run: `cd modules/factor && go test -race ./internal/trigger/... ./test -run 'ViewReady|Period|Factor' -count=1`

Expected: PASS；同标的多个因子并行、marker 屏障、degraded 聚合和 no-binding ACK 全部通过。

- [ ] **Step 8: 提交 View-ready 编排改动**

```bash
git add modules/factor/internal/trigger modules/factor/test/view_driven_e2e_test.go
git commit -m "feat(factor): run view-ready combinations through global worker pool"
```

### Task 5: 更新 Bootstrap、RPC、Recalc 和生命周期锁

**Files:**
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/rpc/recalc.go`
- Modify: `modules/factor/internal/rpc/recalc_view_test.go`
- Modify: `modules/factor/cmd/cli/run_once.go`
- Modify: `modules/factor/cmd/cli/run_once_test.go`
- Modify imports: `modules/factor/test/storage_e2e_test.go`

- [ ] **Step 1: 写 Bootstrap 单配置装配测试**

测试 fake 构造参数，断言 `PythonWorkers=7` 同时传给 `NewPythonWorkerPool` 和 `taskrunner.NewService`，且不调用 scheduler Start：

```go
require.Equal(t, 7, capturedPythonWorkers)
require.Equal(t, 7, capturedTaskWorkers)
```

- [ ] **Step 2: 更新 Bootstrap 装配**

核心装配改为：

```go
pythonPool, err = engine.NewPythonWorkerPool(ctx, cfg.Engine.PythonWorkers, process.Config{...})
runner := taskrunner.NewService(
    cfg.Engine.PythonWorkers,
    storage,
    pythonPool,
    taskrunner.WithDatasetMetrics(runMetrics),
    taskrunner.WithFactorGate(factorGate),
    taskrunner.WithTaskValidator(newTaskValidator(factorRepo, bindingRepo)),
)
viewReadyRunner := trigger.NewViewReadyRunner(
    bindingRepo,
    factorRepo,
    runner,
    storage,
    cfg.Engine.FactorsDir,
    trigger.WithOperationGate(operationGate),
    trigger.WithPeriodMetrics(periodMetrics),
)
```

删除 scheduler `Start/Stop` 和 `WithTaskConcurrency`。关闭顺序仍先停止 consumer/reconciler，再关闭 Python pool 和数据库。

- [ ] **Step 3: 删除 RPC 的 queued task 旧语义**

`rpc.Service` 的运行时依赖改成 `TaskRunnerStatus`；删除 `dropQueuedFactor` 和 `DropQueuedFactor` 类型断言。生命周期 mutation 继续通过 `OperationGate` 等待整个 source-ready/Recalc 操作结束，因此不存在 mutation 后继续执行的旧队列任务。

- [ ] **Step 4: 保持 Recalc 多周期原子性**

`RecalcFactor` 继续在整个多周期循环外持有同一 `OperationGate`，每个周期调用 `ViewReadyRunner.ExecuteSelectedWithGate`。每个周期内部使用同一个 100 worker 池；周期之间保持串行，避免新旧周期同时覆盖相同 Result View。

- [ ] **Step 5: 更新 run-once 使用 TaskRunner**

run-once 构造：

```go
pythonPool, err := engine.NewPythonWorkerPool(ctx, 1, process.Config{...})
runner := taskrunner.NewService(1, storageClient, pythonPool)
```

保留手工 factor 循环和同步错误返回，不借用线上服务的 100 个常驻 worker。

- [ ] **Step 6: 运行 Bootstrap、RPC、CLI 和 Recalc 测试**

Run: `cd modules/factor && go test -race ./internal/bootstrap ./internal/rpc ./cmd/cli -count=1`

Expected: PASS；disable/unbind 等待正在执行周期，Recalc 多周期不与 lifecycle mutation 交错。

- [ ] **Step 7: 提交装配改动**

```bash
git add modules/factor/internal/bootstrap modules/factor/internal/rpc \
  modules/factor/cmd/cli modules/factor/test/storage_e2e_test.go
git commit -m "refactor(factor): wire view-ready runner to one global pool"
```

### Task 6: 将 Engine Status 改成真实 worker 状态

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Generated: `modules/factor/proto/factorgen/factor.pb.go`
- Generated: `modules/factor/proto/factorgen/factor.trpc.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `web/src/api/factor/types.ts`
- Modify: `web/src/views/factor/results/index.vue`
- Modify: `web/src/views/factor/__tests__/factor-contract.spec.ts`

- [ ] **Step 1: 写新的 RPC 和前端契约测试**

Go 侧期望：

```go
require.EqualValues(t, 100, rsp.GetPythonWorkers())
require.EqualValues(t, 7, rsp.GetActiveTasks())
require.EqualValues(t, 93, rsp.GetPendingTasks())
```

Vitest 期望：

```ts
const status: EngineStatus = {
  ret_info: { code: 0, msg: "success" },
  python_workers: 100,
  active_tasks: 7,
  pending_tasks: 93
};
expect(status).not.toHaveProperty("queue_overflow_count");
```

- [ ] **Step 2: 修改 proto 并串行生成代码**

```protobuf
message GetEngineStatusRsp {
  common.RetInfo ret_info = 1;
  int32 python_workers = 2;
  int32 active_tasks = 3;
  int32 pending_tasks = 4;
}
```

Run: `cd modules/factor/proto && make clean && make`

Expected: `factorgen/factor.pb.go` 包含三个新 getter，不再包含 queue overflow getter。生成期间不要并发运行其他 Go 测试。

- [ ] **Step 3: 更新 RPC 和 health details**

`GetEngineStatus` 从 Python pool status 和 TaskRunner status 构造响应。Health detail 使用：

```go
"python_workers":    cfg.Engine.PythonWorkers,
"active_tasks":      runnerStatus.ActiveTasks,
"pending_tasks":     runnerStatus.PendingTasks,
"task_runner_ready": runner != nil,
```

删除 `scheduler_ready`、`worker_count`、`queue_depth` 和 `queue_overflow_count`。

- [ ] **Step 4: 更新结果页状态展示**

结果页顶部展示：

```vue
<div class="engine-status">
  <span>Python Worker {{ engineStatus.python_workers }}</span>
  <span>执行中 {{ engineStatus.active_tasks }}</span>
  <span>等待中 {{ engineStatus.pending_tasks }}</span>
</div>
```

这只是运行状态，不增加配置表单。

- [ ] **Step 5: 运行 Go、Vitest 和类型检查**

Run: `cd modules/factor && go test ./internal/rpc ./internal/bootstrap ./proto/factorgen -count=1`

Expected: PASS。

Run: `cd web && pnpm vitest run src/views/factor/__tests__/factor-contract.spec.ts && pnpm vue-tsc --noEmit`

Expected: PASS，前端不再引用旧 queue 字段。

- [ ] **Step 6: 提交状态契约改动**

```bash
git add modules/factor/proto modules/factor/internal/rpc modules/factor/internal/bootstrap \
  web/src/api/factor/types.ts web/src/views/factor/results/index.vue \
  web/src/views/factor/__tests__/factor-contract.spec.ts
git commit -m "refactor(factor): expose global worker pool status"
```

### Task 7: 更新部署配置和合同测试

**Files:**
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/test/contract/test-deploy-moox-factor.sh`
- Modify: `scripts/runtime/moox-factor-run-once.sh`

- [ ] **Step 1: 写部署环境变量失败断言**

合同测试增加：

```bash
grep -Fxq "MOOX_FACTOR_ENGINE_PYTHON_WORKERS=100" "${UNPACKED}/captured.env"
! grep -Fq "MOOX_FACTOR_ENGINE_WORKERS=" "${UNPACKED}/captured.env"
! grep -Fq "scheduler:" "${UNPACKED}/factor/config/app.yaml"
grep -Fq "python_workers: 100" "${UNPACKED}/factor/config/app.yaml"
```

- [ ] **Step 2: 运行合同测试确认新变量未透传**

Run: `bash scripts/test/contract/test-deploy-moox-factor.sh`

Expected: FAIL，缺少 `MOOX_FACTOR_ENGINE_PYTHON_WORKERS=100`。

- [ ] **Step 3: 更新 deploy 环境**

`FACTOR_ENV` 增加：

```bash
"MOOX_FACTOR_ENGINE_PYTHON_WORKERS=${MOOX_FACTOR_ENGINE_PYTHON_WORKERS:-100}"
```

不再生成旧 `MOOX_FACTOR_ENGINE_WORKERS`。`moox-factor-run-once.sh` 不设置该变量，因为 run-once 固定单 worker。

- [ ] **Step 4: 运行部署合同测试**

Run: `bash scripts/test/contract/test-deploy-moox-factor.sh`

Expected: PASS，解包后的配置只有新字段，captured env 为 100。

- [ ] **Step 5: 提交部署改动**

```bash
git add scripts/deploy/deploy-moox.sh scripts/test/contract/test-deploy-moox-factor.sh \
  scripts/runtime/moox-factor-run-once.sh
git commit -m "chore(factor): deploy global python worker limit"
```

### Task 8: 更新设计和运行手册

**Files:**
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/因子视图驱动计算设计.md`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `modules/factor/README.md`

- [ ] **Step 1: 更新名词和调用链**

全文将 Factor 模块中的“Go Scheduler/调度器”改为“TaskRunner/任务执行器”，同时明确 Collector 的 scheduler 不在本次重命名范围。文档调用链固定为：

```text
ViewSourcePeriodReady -> ViewReadyRunner -> TaskRunner -> PythonWorkerPool -> WriteFactorPatch
```

- [ ] **Step 2: 写清唯一并发参数语义**

文档必须包含：

```yaml
engine:
  python_workers: 100
```

并说明它同时限制单 Factor 进程内完整组合任务和按需启动的 Python 进程数量；真实全局并发为 `min(N*M, python_workers)`。同一 subject 的不同 binding 可以并行。

- [ ] **Step 3: 删除旧批次和队列说明**

删除 `queue_capacity`、`queue_overflow_count`、`subject_batch_size`、严格批次屏障、按 subject 固定 Python shard 和 `scheduler.max_retry` 的用户配置说明。重试仍是内部固定一次，不作为调优旋钮。

- [ ] **Step 4: 更新实时验证手册**

手册改为观察：

1. source-ready 到达后 `pending_tasks` 增加；
2. `active_tasks <= python_workers`；
3. 同一 subject 的两个 factor 日志时间段重叠；
4. 结果写入完成前没有 `FactorPeriodComputed`；
5. 周期结束后 active/pending 均回到 0；
6. 任一组合失败时 marker degraded，但其他组合仍有结果。

- [ ] **Step 5: 运行文档合同检查**

Run: `rg -n 'engine\.workers|scheduler\.max_retry|queue_capacity|queue_overflow_count|WithTaskConcurrency' modules/factor docs/因子计算模块设计.md docs/因子视图驱动计算设计.md`

Expected: 无活动设计或运行配置命中；历史计划文档不在本检查范围。

- [ ] **Step 6: 提交文档**

```bash
git add docs/因子计算模块设计.md docs/因子视图驱动计算设计.md \
  modules/factor/docs/realtime-verification.md modules/factor/README.md
git commit -m "docs(factor): document global combination concurrency"
```

### Task 9: 完成真实链路、性能和资源验收

**Files:**
- Modify: `modules/factor/test/view_driven_e2e_test.go`
- Modify: `modules/factor/test/storage_e2e_test.go`
- Modify: `scripts/test/e2e/test-factor-view-ready-e2e.sh`

- [ ] **Step 1: 增加同 subject 双因子结果 E2E**

沿用 `examples/factors` 中的 `bias` 和 `cci` 定义，E2E 使用同一个 subject、两个 bindings，触发一个 source-ready 后断言两个字段都写入 Result Dataset/View，并且 marker 中两个 binding 都是 complete。实际并行时序由 Task 2、Task 3 和 Task 4 的阻塞式单元测试确定性证明；真实 Python E2E 不使用基于 sleep 的脆弱耗时断言。

```go
require.NotNil(t, resultRow.Fields["bias_5"])
require.NotNil(t, resultRow.Fields["cci"])
require.Len(t, computed.GetBindings(), 2)
require.Equal(t, "complete", computed.GetStatus())
```

- [ ] **Step 2: 增加全局并发和 marker 屏障 E2E**

构造 4 subjects × 3 factors、`python_workers=3`：

- 12 个组合全部执行；
- 探针最大并发恰好为 3；
- 第 12 个结果写入前 `GetFactorPeriodComputed` 为 false；
- 全部写入后只有一个 marker；
- Result View 中包含 12 个 subject-factor 输出组合。

- [ ] **Step 3: 增加失败隔离 E2E**

让一个 factor 只对 ETH 抛错，断言 BTC/SOL 和其他 factor 仍成功，ETH 旧输出被清，marker 为 degraded，最终 source-ready 被 ACK 而不是永久阻塞。

- [ ] **Step 4: 运行 Python worker 测试**

Run:

```bash
cd modules/factor
PYTHONPATH="$PWD/../../packages/pyruntime/python:$PWD/pyworker" \
  uv run --with-requirements pyworker/requirements.txt \
  python -m pytest pyworker -q
```

Expected: 全部 PASS。

- [ ] **Step 5: 运行 Factor 模块和 race**

Run: `cd modules/factor && go test ./... -count=1`

Expected: PASS。

Run: `cd modules/factor && go test -race ./internal/taskrunner ./internal/trigger/... ./internal/engine ./internal/rpc -count=1`

Expected: PASS，无 shared result/state 数据竞争。

- [ ] **Step 6: 运行真实 EventBus/Storage E2E**

Run: `MOOX_RUN_REAL_FACTOR_E2E=1 bash scripts/test/e2e/test-factor-view-ready-e2e.sh`

Expected: PASS，真实链路覆盖 `ViewSourcePeriodReady -> N*M 组合 -> Result Dataset/View -> FactorPeriodComputed -> ViewFactorPeriodReady`。

- [ ] **Step 7: 在目标主机压测 100 worker**

使用实际 Factor 定义和一个完整 1m 周期记录以下验收项：

- Factor 启动后 100 个 Python worker 全部 ready；
- `active_tasks` 峰值不超过 100；
- 周期总耗时小于 60 秒；
- Factor 进程 RSS 不触发系统 OOM；
- CPU 不造成 Storage/View 健康检查失败；
- source-ready durable 不持续积压。

若资源不满足，仅将部署环境 `MOOX_FACTOR_ENGINE_PYTHON_WORKERS` 调低到 64 或 48；不增加第二个并发参数。

- [ ] **Step 8: 运行工作区和发布合同**

Run: `./scripts/test/contract/test-go-workspace.sh`

Expected: PASS。

Run: `make test-script-contracts`

Expected: PASS。

Run: `git status --short`

Expected: 只包含本计划明确列出的改动；没有生成中间态或 Python 缓存文件。

- [ ] **Step 9: 提交验收测试**

```bash
git add modules/factor/test scripts/test/e2e/test-factor-view-ready-e2e.sh
git commit -m "test(factor): verify global combination worker pool"
```

## 3. 完成标准

以下条件必须同时满足才算完成：

- Factor 实时计算只有 `ViewSourcePeriodReady` 事件入口，没有新增定时器。
- 配置中只有 `engine.python_workers` 一个并发旋钮，正式值为 100。
- 不存在 Factor `scheduler` package、scheduler YAML、旧 workers 字段或旧环境变量。
- N subjects × M bindings 生成 N×M 个确定性任务，同 subject 的不同 factor 可以重叠执行。
- 最多只有 `python_workers` 个完整任务同时读取/计算/写回，不会提前堆积 N×M 个 DataFrame。
- 空闲 worker 动态领取任务，没有 subject hash 热点和严格批次尾部等待。
- 单组合失败不取消其他组合，失败输出被清，marker 正确 degraded。
- `FactorPeriodComputed` 只在全部组合及写回终态后追加。
- Recalc 共享同一池；run-once 保持单 worker。
- Engine Status 和前端展示 Python worker、执行中、等待中，不再展示 queue overflow。
- Go、race、Python、前端类型、部署合同和真实 View-ready E2E 全部通过。

## 4. 明确不做

- 不并行处理多个 source-ready 周期；consumer 继续 `MaxAckPending=1`。
- 不增加用户可配置的 batch size、Go worker、prefetch、queue capacity 或 retry 次数。
- 不增加跨 Factor 实例的全局限流；个人系统保持单实例。
- 不引入持久化任务队列、任务恢复表、分布式调度或 exactly-once 状态机。
- 不改变 Python 因子的 `compute(df, params)` 输入输出契约。
- 不让短生命周期 run-once 启动 100 个常驻 Python 进程。
