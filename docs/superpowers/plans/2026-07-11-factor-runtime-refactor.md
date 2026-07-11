# Factor Runtime Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将已上线的 `modules/factor` 从串行 JSON 临时 worker 实现迁移到共享 `packages/pyruntime`，实现真并发调度、不可变因子版本、100 因子共享快照和统一写回。

**Architecture:** Factor 仍以 per-symbol parent task 为业务和保序边界，Storage 窗口只读一次。scheduler 把因子按历史耗时拆成 `FactorBatch`，多个 Python worker mmap 同一 Arrow 快照，Go 聚合并严格校验全部结果后只写回一次。

**Tech Stack:** Go 1.24、`packages/pyruntime`、Python 3.12+、pandas Copy-on-Write、pyarrow、NATS JetStream、Storage Access tRPC、SQLite/GORM、Prometheus。

---

## 前置条件与迁移原则

- 先完成 [Python Runtime Implementation Plan](2026-07-11-python-runtime.md) 的 R1–R4，Factor 不再自己复制 protocol/supervisor/snapshot。
- 保留现有 `signal()` / `signal_multi_params()` 对 XBX 因子的接入契约，不改因子文件业务内容。
- 实时和 `run-once` 使用同一 `scheduler.Service` 与 Factor executor；`Drain` 只保留为测试/命令行的“等待当前队列空”接口，不再亲自执行任务。
- 默认全部 batch 成功才写回，不提供隐式部分成功。
- 本计划取代旧计划 `2026-07-06-factor-calculation-module.md` 中 Task 4、5、10、11、16 的 worker/调度/Arrow 部分，其他管理面和多实例内容不在本计划重复。

## 目标文件图

```text
modules/factor/
├── internal/engine/
│   ├── types.go              # parent task/batch/result
│   ├── codec.go              # Factor payload <-> pyruntime
│   ├── executor.go           # batch executor + result validation
│   └── executor_test.go
├── internal/scheduler/
│   ├── service.go            # Start/Stop/WaitIdle
│   ├── batch.go              # cost-aware partition
│   ├── aggregate.go          # all-or-nothing aggregate
│   └── *_test.go
├── internal/registry/
│   ├── source.go             # SHA-256 immutable publish
│   └── source_test.go
├── internal/storageio/
│   ├── snapshot.go           # DataFrame -> Arrow snapshot
│   └── writeback.go           # aggregate audit attributes
├── internal/observability/metrics.go
├── pyworker/
│   ├── worker.py
│   ├── factor_adapter.py
│   └── test_worker.py
└── internal/engine/{frame.go,json_codec.go,stdio_executor.go,worker_pool.go} # 迁移完成后删除
```

### Task 1: 加入 pyruntime 依赖并固定 Factor codec 边界

**Files:**
- Modify: `modules/factor/go.mod`
- Create: `modules/factor/internal/engine/codec.go`
- Create: `modules/factor/internal/engine/codec_test.go`
- Modify: `modules/factor/internal/engine/types.go`

- [ ] **Step 1: 写 Factor RUN meta 与 RESULT 严格校验测试**

```go
func TestDecodeBatchResultRejectsMissingColumn(t *testing.T) {
	spec := FactorBatch{ExpectedColumns: []string{"Bias_20", "Cci_14"}}
	_, err := DecodeBatchResult(spec, pyruntime.RunResult{Meta: []byte(`{"results":{"Bias_20":{"tail":1,"values":[1]}}}`)})
	if !errors.Is(err, ErrOutputContract) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: 在 `go.mod` 加入共享 module 及 replace**

```text
require github.com/mooyang-code/moox/packages/pyruntime v0.0.0-00010101000000-000000000000
replace github.com/mooyang-code/moox/packages/pyruntime => ../../packages/pyruntime
```

- [ ] **Step 3: 定义 parent/batch 类型**

```go
type FactorBatch struct { BatchID, ParentTaskID, SnapshotID, SnapshotHash string; Factors []FactorSpec; ExpectedColumns []string; Attempt int }
type BatchResult struct { BatchID string; Columns map[string]FactorColumnResult; PerFactorMS map[string]int64; ElapsedMS int64 }
```

codec 只组装 Factor meta，payload/ref 由 pyruntime transport 处理。输出严格拒绝缺失列、额外列、重复列、tail/数组长度不一致、Inf 和非数值。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/factor && go test ./internal/engine -count=1`

Expected: PASS。

```bash
git add modules/factor/go.mod modules/factor/go.sum modules/factor/internal/engine
git commit -m "refactor(factor): define pyruntime batch codec"
```

### Task 2: 把 Python worker 改为 HELLO/LOAD/RUN 适配器

**Files:**
- Modify: `modules/factor/pyworker/worker.py`
- Create: `modules/factor/pyworker/factor_adapter.py`
- Modify: `modules/factor/pyworker/test_worker.py`
- Delete: `modules/factor/pyworker/codec.py`
- Modify: `modules/factor/pyworker/runtime-requirements.txt`

- [ ] **Step 1: 写“只 LOAD 一次”和 stdout 不污染协议测试**

```python
def test_loaded_module_is_reused_and_logs_are_captured(worker):
    worker.load(version("CountImport", "IMPORTS = globals().get('IMPORTS', 0) + 1\ndef signal(df,n,name):\n print('run')\n df[name]=IMPORTS\n return df"))
    first = worker.run(request("CountImport"))
    second = worker.run(request("CountImport"))
    assert first.results == second.results
    assert second.logs[0]["message"] == "run\n"
```

- [ ] **Step 2: 实现 factor adapter**

`FactorAdapter.load()` 使用 `(factor_id, source_hash)` 唯一模块名导入，校验 `signal` 或 `signal_multi_params`；`run()` 对每个因子使用 `isolated_frame(base_df)`，只返回声明列。

- [ ] **Step 3: 改用 `moox_pyruntime.protocol`；日志捕获继续由 factor adapter 的 `redirect_stdout/redirect_stderr` 负责**

worker 启动只发 HELLO，不扫描 `factors/*.py`；LOAD 明确指定物化路径/hash；RUN 只引用已加载版本。

- [ ] **Step 4: 运行 Python 契约测试**

Run: `cd modules/factor && PYTHONPATH=../../packages/pyruntime/python python3 -m pytest pyworker/test_worker.py -q`

Expected: PASS，且因子 `print()` 出现在 RESULT logs 中而不是 worker stdout。

- [ ] **Step 5: 提交**

```bash
git add modules/factor/pyworker
git commit -m "refactor(factor): run factors through shared Python protocol"
```

### Task 3: 将因子源码发布改为 SHA-256 不可变版本

**Files:**
- Modify: `modules/factor/internal/registry/source.go`
- Create: `modules/factor/internal/registry/source_test.go`
- Modify: `modules/factor/internal/registry/service.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/cmd/cli/import.go`

- [ ] **Step 1: 写旧任务仍可读旧 hash 的发布测试**

```go
func TestPublishNewSourceDoesNotOverwriteActiveVersion(t *testing.T) {
	r := newRegistry(t)
	v1 := publish(t, r, "bias", "def signal(*a): return a[0]")
	v2 := publish(t, r, "bias", "def signal(*a): raise ValueError()")
	if v1.Path == v2.Path || readFile(t, v1.Path) == readFile(t, v2.Path) { t.Fatal("versions were overwritten") }
}
```

- [ ] **Step 2: 用 `moduleregistry.SourcePublisher` 取代 `<name>.py` 原地覆盖**

RPC/CLI 提交源码时，服务端重算 SHA-256，先物化并 LOAD 验证，再事务性切换 `c_source_hash`。LOAD 失败时 DB active hash 不变。

- [ ] **Step 3: 迁移内置 Bias/Cci**

启动时可将旧 `factors/Bias.py` 读入后物化为第一个 hash 版本，但不删除导入源文件；这个迁移必须幂等。

- [ ] **Step 4: 运行 registry/RPC/CLI 测试**

Run: `cd modules/factor && go test ./internal/registry ./internal/rpc ./cmd/cli -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add modules/factor/internal/registry modules/factor/internal/rpc modules/factor/cmd/cli
git commit -m "feat(factor): publish immutable factor versions"
```

### Task 4: 用 pyruntime pool 取代 Factor 私有 worker pool

**Files:**
- Create: `modules/factor/internal/engine/executor.go`
- Create: `modules/factor/internal/engine/executor_test.go`
- Delete: `modules/factor/internal/engine/frame.go`
- Delete: `modules/factor/internal/engine/json_codec.go`
- Delete: `modules/factor/internal/engine/stdio_executor.go`
- Delete: `modules/factor/internal/engine/worker_pool.go`
- Modify: `modules/factor/internal/app/control/bootstrap.go`
- Modify: `modules/factor/internal/app/control/health_test.go`

- [ ] **Step 1: 写超时后下一任务能使用补位 worker 的集成测试**

```go
func TestExecutorRecoversAfterWorkerTimeout(t *testing.T) {
	exec := newExecutorWithModes(t, "sleep", "echo")
	_, _ = exec.ExecuteBatch(shortContext(t), batchFixture(), snapshotFixture(t))
	if _, err := exec.ExecuteBatch(context.Background(), batchFixture(), snapshotFixture(t)); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: 实现 Factor executor 适配**

```go
type Executor interface { Load(context.Context, FactorVersion) error; ExecuteBatch(context.Context, FactorBatch, InputRef) (BatchResult, error); Status() RuntimeStatus; Close() error }
```

`ExecuteBatch` 使用 parent subject 作 `ShardKey`，请求中固定 `source_hash` 和 snapshot hash，并通过 Task 1 codec 校验结果。

- [ ] **Step 3: bootstrap 创建共享 pool 并暴露真实 health**

health `ready=false` 条件包含 ready worker=0、crash-loop 熔断、runtime hash 不一致。

- [ ] **Step 4: 确认旧协议文件没有引用后删除**

Run: `cd modules/factor && rg 'FrameTypeReady|NewStdioExecutor|EncodeJSONRequestMeta' .`

Expected: 无匹配。

- [ ] **Step 5: 运行测试并提交**

Run: `cd modules/factor && go test -race ./internal/engine ./internal/app/control -count=1`

Expected: PASS。

```bash
git add modules/factor/internal/engine modules/factor/internal/app/control
git commit -m "refactor(factor): adopt supervised Python runtime"
```

### Task 5: 将 scheduler 改为固定分片消费循环

**Files:**
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`
- Modify: `modules/factor/internal/app/control/bootstrap.go`
- Modify: `modules/factor/internal/rpc/recalc.go`

- [ ] **Step 1: 写真并发、同 symbol 保序和 WaitIdle 测试**

```go
func TestServiceRunsDifferentShardsConcurrently(t *testing.T) {
	exec := newBarrierExecutor(2)
	s := NewService(Config{Workers:2}, fakeStorage{}, exec); s.Start(t.Context()); defer s.Stop()
	s.Enqueue(t.Context(), taskFor("BTC-USDT")); s.Enqueue(t.Context(), taskFor(otherShard("BTC-USDT", 2)))
	if err := s.WaitIdle(timeoutContext(t)); err != nil || exec.MaxConcurrent() != 2 { t.Fatalf("max=%d err=%v", exec.MaxConcurrent(), err) }
}
```

- [ ] **Step 2: 实现 `Start/Stop/WaitIdle`**

```go
func (s *Service) Start(ctx context.Context) error
func (s *Service) Stop() error
func (s *Service) WaitIdle(ctx context.Context) error
```

每个 shard 独立 FIFO channel 和一个 goroutine；`Enqueue` 完成 supersede 后发送通知；`WaitIdle` 等待 queued+running=0。删除 `drainMu` 和扫描所有 queue 的执行循环。

- [ ] **Step 3: 实时 bootstrap 只 Start 一次**

`drainDebounced` 只 Enqueue，不再调 `Drain`；reconcile/CLI/recalc 如需等待，调 `WaitIdle(ctx)`。

- [ ] **Step 4: 运行 scheduler race 测试**

Run: `cd modules/factor && go test -race ./internal/scheduler ./internal/app/control ./internal/rpc -count=1`

Expected: PASS，并发测试观测到 `MaxConcurrent()==2`。

- [ ] **Step 5: 提交**

```bash
git add modules/factor/internal/scheduler modules/factor/internal/app/control modules/factor/internal/rpc
git commit -m "feat(factor): run sharded scheduler workers concurrently"
```

### Task 6: 实现按耗时均衡的 FactorBatch

**Files:**
- Create: `modules/factor/internal/scheduler/batch.go`
- Create: `modules/factor/internal/scheduler/batch_test.go`
- Modify: `modules/factor/internal/domain/factor.go`
- Modify: `modules/factor/schema/factor.sql`
- Modify: `modules/factor/schema/schema_test.go`

- [ ] **Step 1: 写慢因子均衡分组和小任务不分组测试**

```go
func TestPartitionBalancesHistoricalCost(t *testing.T) {
	got := Partition(specs("slow","a","b","c"), map[string]int64{"slow":90,"a":30,"b":30,"c":30}, Policy{MaxParallel:2, MinEstimatedMS:50})
	assertBatchNames(t, got, [][]string{{"slow"},{"a","b","c"}})
}
```

- [ ] **Step 2: 实现 deterministic LPT 分组**

因子按 `estimated_ms desc, factor_id asc` 排序，每次放入当前估时最小的 batch；历史不存在时使用参数数量和默认 1ms。估算总耗时低于阈值时只生成一个 batch。

- [ ] **Step 3: 持久化 EWMA 因子耗时**

`t_factor_defs` 增加 `c_avg_runtime_ms REAL NOT NULL DEFAULT 0`，每次成功后按 `new=0.8*old+0.2*sample` 更新；不把耗时放 Prometheus 高基数 label。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/factor && go test ./internal/scheduler ./schema -count=1`

Expected: PASS。

```bash
git add modules/factor/internal/scheduler modules/factor/internal/domain modules/factor/schema
git commit -m "feat(factor): partition factors by measured cost"
```

### Task 7: 建立单次回读的 Arrow 共享快照

**Files:**
- Create: `modules/factor/internal/storageio/snapshot.go`
- Create: `modules/factor/internal/storageio/snapshot_test.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/load_test.go`

- [ ] **Step 1: 写 100 因子、8 batch 仍只读 Storage 一次的测试**

```go
func TestParentTaskReadsOnceAndSharesSnapshot(t *testing.T) {
	storage := &countingStorage{}
	runParentTask(t, storage, 100, 8)
	if storage.ReadCalls() != 1 || storage.SnapshotFiles() != 1 { t.Fatalf("reads=%d snapshots=%d", storage.ReadCalls(), storage.SnapshotFiles()) }
}
```

- [ ] **Step 2: 实现 DataFrame 到 pyruntime Table 的类型化转换**

K 线数值列统一 float64，`candle_begin_time` 统一 UTC timestamp(ms)；类型不符或行长度不一致时在写快照前失败。

- [ ] **Step 3: parent task 获取一个 snapshot handle 并传给全部 batch**

所有 batch 成功、失败或 context cancel 后在同一 defer 中 Release；某 batch 重试必须复用同一 snapshot hash，不再读 Storage。

- [ ] **Step 4: 运行负载和 race 测试**

Run: `cd modules/factor && go test -race ./internal/storageio ./internal/scheduler -run 'TestParentTask|TestSnapshot|TestLoad' -count=1`

Expected: PASS，100 因子用一个 snapshot file。

- [ ] **Step 5: 提交**

```bash
git add modules/factor/internal/storageio modules/factor/internal/scheduler
git commit -m "feat(factor): share one Arrow snapshot across batches"
```

### Task 8: 实现 batch 聚合、重试和单次写回

**Files:**
- Create: `modules/factor/internal/scheduler/aggregate.go`
- Create: `modules/factor/internal/scheduler/aggregate_test.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/storageio/writeback.go`
- Modify: `modules/factor/internal/storageio/storageio_test.go`

- [ ] **Step 1: 写部分失败不写回、单 batch 重试和成功只写一次测试**

```go
func TestAggregateDoesNotWritePartialResult(t *testing.T) {
	storage := &writeCountingStorage{}; exec := executorFailingBatch("b2")
	err := runBatchedTask(t, storage, exec)
	if err == nil || storage.WriteCalls() != 0 { t.Fatalf("writes=%d err=%v", storage.WriteCalls(), err) }
}
```

- [ ] **Step 2: 实现 deterministic aggregate**

按 parent 期望列顺序校验并合并；同列重复立即失败；可重试错误只重试失败 batch，已成功结果保留在 parent 内存；任一最终失败则整体不发布。

- [ ] **Step 3: 写回 attributes 记录计算来源**

```go
row.Attributes = map[string]string{"factor.parent_task_id": task.TaskID, "factor.snapshot_hash": task.SnapshotHash, "factor.computed_at": computedAt.UTC().Format(time.RFC3339Nano)}
```

每列 source hash 不能用行 attributes 表达多值，因此详细的 `factor_id -> source_hash` 保存到 `t_factor_runs.c_sources_json`；Storage 行只保留 parent/snapshot/computed_at 共通属性。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/factor && go test -race ./internal/scheduler ./internal/storageio -count=1`

Expected: PASS。

```bash
git add modules/factor/internal/scheduler modules/factor/internal/storageio
git commit -m "feat(factor): aggregate batches before unified writeback"
```

### Task 9: 更新配置、RPC 状态和可观测性

**Files:**
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/internal/app/control/config.go`
- Modify: `modules/factor/internal/app/control/config_test.go`
- Modify: `modules/factor/proto/factor.proto`
- Modify: `modules/factor/internal/rpc/service.go`
- Create: `modules/factor/internal/observability/metrics.go`
- Modify: `modules/factor/internal/metricspublish/handler.go`

- [ ] **Step 1: 写配置默认值和非法组合测试**

```go
func TestEngineConfigRejectsBatchParallelismAboveWorkers(t *testing.T) {
	cfg := validConfig(); cfg.Engine.Workers=4; cfg.Engine.MaxBatchParallelism=8
	if err := cfg.Validate(); err == nil { t.Fatal("expected validation error") }
}
```

- [ ] **Step 2: 增加运行时配置**

```yaml
engine:
  workers: 8
  max_batch_parallelism: 8
  batch_min_estimated_ms: 50
  encoding: auto
  arrow_stream_threshold_bytes: 1048576
  arrow_mmap_threshold_bytes: 4194304
  snapshot_dir: /dev/shm/moox-python
  snapshot_ttl_seconds: 300
  max_tasks_per_worker: 1000
```

- [ ] **Step 3: 扩展 GetEngineStatus**

返回 configured/ready/busy/restarting/failed workers、queue/running parent tasks、active batches、snapshot bytes、各 encoding 计数和最近 worker error；重新生成 `factorgen`。

- [ ] **Step 4: 增加低基数指标**

`factor_batch_total{status}`、`factor_batch_parallelism`、`factor_parent_duration_seconds{status}`、`factor_snapshot_reuse_total`，不使用 factor_id/symbol 做 label。

- [ ] **Step 5: 运行生成与测试并提交**

Run: `cd modules/factor/proto && make && cd .. && go test ./internal/app/control ./internal/rpc ./internal/observability ./internal/metricspublish -count=1`

Expected: PASS，生成代码与 proto 一起提交。

```bash
git add modules/factor/config modules/factor/internal modules/factor/proto
git commit -m "feat(factor): expose batched runtime status and metrics"
```

### Task 10: 完成端到端、容量和故障验收

**Files:**
- Modify: `modules/factor/internal/scheduler/load_test.go`
- Create: `modules/factor/internal/engine/runtime_integration_test.go`
- Modify: `modules/factor/docs/realtime-verification.md`
- Modify: `docs/因子计算模块设计.md`

- [ ] **Step 1: 增加 100 因子共享快照端到端测试**

使用 8 worker、100 个可控 sleep 因子、1 个 600行快照；断言 Storage read=1、snapshot file=1、worker max concurrency>1、writeback=1、结果列=100。

- [ ] **Step 2: 增加 worker 崩溃和 batch 超时验收**

首次 batch 中杀死 worker，断言 supervisor 补位、同 snapshot 重试、无部分写回；超过 MaxRetry 后 parent run 状态 failed。

- [ ] **Step 3: 运行全量测试**

Run: `cd modules/factor && go test -race ./... -count=1`

Expected: PASS。

- [ ] **Step 4: 运行 Python 测试与 run-once**

Run: `cd modules/factor && PYTHONPATH=../../packages/pyruntime/python python3 -m pytest pyworker -q`

Run: `cd modules/factor && go run ./cmd/cli run-once --symbol BTC-USDT --freq 1m --no-write`

Expected: pytest PASS；`run-once` 输出 source/snapshot/output hash 和完整因子列，不写 Storage。

- [ ] **Step 5: 更新验证手册和实现状态**

文档记录压测命令、基准数据、实际并发度、RSS、快照复用率和回滚方法；只把已完成项标为现状。

- [ ] **Step 6: 提交**

```bash
git add modules/factor docs/因子计算模块设计.md
git commit -m "test(factor): verify shared snapshot parallel execution"
```

## 最终验收

```bash
cd packages/pyruntime && go test -race ./... -count=1
cd modules/factor && go test -race ./... -count=1
cd modules/factor && PYTHONPATH=../../packages/pyruntime/python python3 -m pytest pyworker -q
npm run docs:build
git diff --check
```

Expected:

- 当前 `Drain()` 执行任务的串行路径已消失，实时 scheduler 有固定 shard worker loop。
- 旧 `frame.go/json_codec.go/stdio_executor.go/worker_pool.go/pyworker/codec.py` 在服务启动路径已不再使用；为 CLI 和迁移窗口暂保留，完成调用方迁移后再删除。
- 100 因子分批并行时 Storage 只读一次、Arrow 快照只生成一份、成功时只写回一次。
- 因子源码更新不覆盖在途任务使用的版本，LOAD 失败不切换 active hash。
- worker 超时/崩溃可补位，异常栈可查，stdout 不会破坏帧协议。
# 实施状态（2026-07-11）

已完成：调度器支持按因子成本拆分 batch、一次读取并共享同一输入窗口、按父任务 subject 维持 worker 分片、结果列/尾长严格校验、失败整体不写回；运行路径接入 `packages/pyruntime` pool，源码使用 hash 版本路径，输入窗口物化为 Arrow IPC 快照并可通过 mmap 复用；数据库增加因子名唯一约束；`modules/factor/test` 覆盖真实 Python worker，调度单测覆盖 100 因子共享一份快照。

兼容说明：JSON 是 pyarrow 未安装时的显式回退；新服务启动路径使用 pyruntime，旧协议代码暂保留供历史单测/CLI 迁移窗口使用。Arrow/MMap 的 Go/Python 互操作已由 `packages/pyruntime` 端到端测试覆盖。
