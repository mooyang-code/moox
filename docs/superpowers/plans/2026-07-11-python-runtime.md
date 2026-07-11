# Python Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建设 `packages/pyruntime` 共享运行时，为 Factor 和 Strategy 提供可版本化、可恢复、可观测的常驻 Python worker 池与 Arrow 共享快照。

**Architecture:** Go 宿主通过 MX 长度前缀帧向独立 Python 进程发送 `HELLO/LOAD/RUN/RESULT`，supervisor 管理进程状态、超时、轮换和重建。小数据内嵌 JSON，单 worker 大数据使用 Arrow stream，多 worker 共用 Arrow IPC file + mmap；业务 task/result 由各模块 codec 定义。

**Tech Stack:** Go 1.24、Python 3.12+、pandas、pyarrow、Apache Arrow Go v18、stdio binary framing、Prometheus、SHA-256。

---

## 实施边界

- 本计划只建设通用运行时和 fake worker 验收工具，不改 Factor 调度和 Strategy 业务状态。
- `packages/pyruntime` 不得 import `modules/factor/internal` 或 `modules/strategy/internal`。
- 现有 `modules/factor/internal/engine/frame.go` 在 Factor 改造计划中删除；本计划先以新包的 contract tests 固定协议。
- V1 信任内部 Python 代码，进程隔离不等于安全沙箱。

## 目标文件图

```text
packages/pyruntime/
├── go.mod
├── protocol/
│   ├── frame.go
│   ├── message.go
│   ├── message_test.go
│   └── frame_test.go
├── process/
│   ├── config.go
│   ├── worker.go
│   ├── supervisor.go
│   └── supervisor_test.go
├── pool/
│   ├── pool.go
│   └── pool_test.go
├── moduleregistry/
│   ├── publisher.go
│   └── publisher_test.go
├── transport/
│   ├── encoding.go
│   ├── arrow.go
│   └── arrow_test.go
├── snapshot/
│   ├── store.go
│   └── store_test.go
├── metrics/metrics.go
├── python/moox_pyruntime/
│   ├── __init__.py
│   ├── protocol.py
│   └── arrow.py
└── testkit/
    ├── fake_worker.py
    └── fixture.go
```

### Task 1: 创建独立 Go module 与基础协议类型

**Files:**
- Create: `packages/pyruntime/go.mod`
- Create: `packages/pyruntime/protocol/message.go`
- Modify: `go.work`
- Test: `packages/pyruntime/protocol/message_test.go`

- [ ] **Step 1: 先写协议常量和版本校验失败测试**

```go
func TestValidateHelloRejectsRuntimeHashMismatch(t *testing.T) {
	hello := Hello{ProtocolVersion: VersionV1, RuntimeEnvHash: "actual", Encodings: []Encoding{EncodingJSON}}
	err := ValidateHello(HelloExpectation{ProtocolVersion: VersionV1, RuntimeEnvHash: "expected"}, hello)
	if !errors.Is(err, ErrRuntimeMismatch) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: 运行测试确认因缺少类型而失败**

Run: `cd packages/pyruntime && go test ./protocol -run TestValidateHelloRejectsRuntimeHashMismatch -count=1`

Expected: FAIL，提示 `undefined: Hello` 或 `undefined: ValidateHello`。

- [ ] **Step 3: 定义稳定消息和编码枚举**

```go
const VersionV1 = "moox.py/v1"
type MessageType byte
const (
	TypeHello MessageType = 0x01; TypeLoad MessageType = 0x02; TypeRun MessageType = 0x03
	TypeResult MessageType = 0x04; TypeError MessageType = 0x05; TypePing MessageType = 0x06
)
type Encoding string
const ( EncodingJSON Encoding = "json"; EncodingArrowStream Encoding = "arrow_stream"; EncodingArrowMMap Encoding = "arrow_mmap" )
type Hello struct { ProtocolVersion, WorkerVersion, PythonVersion, RuntimeEnvHash string; Encodings []Encoding; Packages map[string]string }
```

`ValidateHello` 按 expectation 逐项校验 protocol version、runtime hash 和必需 encoding，并分别返回 `ErrProtocolMismatch`、`ErrRuntimeMismatch` 或 `ErrEncodingUnsupported`，便于 health 和告警精确归因。

- [ ] **Step 4: 建立 module 并加入 workspace**

`go.mod` 的 module 必须是 `github.com/mooyang-code/moox/packages/pyruntime`，`go.work` 加入 `./packages/pyruntime`。

- [ ] **Step 5: 运行测试并提交**

Run: `cd packages/pyruntime && go test ./protocol -count=1`

Expected: PASS。

```bash
git add go.work packages/pyruntime
git commit -m "feat(pyruntime): define runtime protocol contracts"
```

### Task 2: 实现有边界检查的 MX 帧 codec

**Files:**
- Create: `packages/pyruntime/protocol/frame.go`
- Modify: `packages/pyruntime/protocol/frame_test.go`

- [ ] **Step 1: 写往返、截断、错误 magic 和超大帧测试**

```go
func TestReadFrameRejectsTotalSizeBeforePayloadAllocation(t *testing.T) {
	buf := encodeHeaderForTest(TypeRun, 8, 1<<30)
	_, err := ReadFrame(bytes.NewReader(buf), Limits{MaxMetaBytes: 1024, MaxPayloadBytes: 1024, MaxFrameBytes: 2048})
	if !errors.Is(err, ErrFrameTooLarge) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: 确认新测试失败**

Run: `cd packages/pyruntime && go test ./protocol -run 'Test(Read|Write)Frame' -count=1`

Expected: FAIL，`ReadFrame` 尚未实现。

- [ ] **Step 3: 实现 codec 与大小上限**

```go
type Limits struct { MaxMetaBytes, MaxPayloadBytes, MaxFrameBytes int64 }
type Frame struct { Type MessageType; Meta json.RawMessage; Payload []byte }
var ErrFrameTooLarge = errors.New("pyruntime: frame too large")
var ErrInvalidFrame = errors.New("pyruntime: invalid frame")
```

`ReadFrame(r, limits)` 必须依次读固定 header、检查 meta 长度、读 meta、检查 payload 长度与总长度，最后才分配 payload；`WriteFrame(w, limits, frame)` 先执行同样的大小和 JSON 校验，再用内部 `writeAll` 循环处理 short write。

必须拒绝负值溢出、未知 type、meta 非 JSON 和累计大小超限；不允许在验证前 `make([]byte, payloadLen)`。

- [ ] **Step 4: 跑 race 测试并提交**

Run: `cd packages/pyruntime && go test -race ./protocol -count=1`

Expected: PASS。

```bash
git add packages/pyruntime/protocol
git commit -m "feat(pyruntime): add bounded MX frame codec"
```

### Task 3: 建立 fake worker 和进程会话测试底座

**Files:**
- Create: `packages/pyruntime/testkit/fake_worker.py`
- Create: `packages/pyruntime/testkit/fixture.go`
- Create: `packages/pyruntime/testkit/fixture_test.go`

- [ ] **Step 1: 写能验证 HELLO 并回显 RUN 的集成测试**

```go
func TestFakeWorkerEmitsValidHello(t *testing.T) {
	cmd, stdout := StartFakeWorker(t, "echo")
	frame, err := protocol.ReadFrame(stdout, protocol.DefaultLimits())
	if err != nil || frame.Type != protocol.TypeHello { t.Fatalf("type=%v err=%v", frame.Type, err) }
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
}
```

- [ ] **Step 2: 实现 Python 假 worker 命令**

`fake_worker.py` 支持 `echo`、`sleep`、`crash`、`print_stdout`、`stderr`、`bad_hello` 六种 mode，并使用与 Go 完全相同的 MX 帧布局。

- [ ] **Step 3: 运行 Python 和 Go 交叉测试**

Run: `cd packages/pyruntime && go test ./testkit -run TestFakeWorkerEmitsValidHello -count=1`

Expected: PASS，证明 fake worker 与 Go frame codec 兼容，且本 task 不留下失败测试。

```bash
git add packages/pyruntime/testkit
git commit -m "test(pyruntime): add subprocess worker fixture"
```

### Task 4: 实现单 worker 会话、日志隔离和超时终止

**Files:**
- Create: `packages/pyruntime/process/config.go`
- Create: `packages/pyruntime/process/worker.go`
- Create: `packages/pyruntime/process/worker_test.go`

- [ ] **Step 1: 增加 stdout 污染、stderr 捕获、超时和进程组清理测试**

```go
func TestRunTimeoutKillsWorker(t *testing.T) {
	w := startFakeWorker(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond); defer cancel()
	_, err := w.Run(ctx, RunRequest{RequestID: "slow"})
	if !errors.Is(err, context.DeadlineExceeded) || w.State() != StateDead { t.Fatalf("state=%s err=%v", w.State(), err) }
}
```

- [ ] **Step 2: 实现单请求串行会话**

```go
type Worker interface { Load(context.Context, LoadRequest) (LoadResult, error); Run(context.Context, RunRequest) (RunResult, error); State() State; Close() error }
type LogRecord struct { RequestID, LogicalID, SourceHash, Stream, Message string; Truncated bool }
```

`exec.CommandContext` 必须配合独立进程组；worker stdout 只读帧，stderr 进入有上限 ring buffer。Python 业务 stdout 由 Python capture 层转入 RESULT，不能混入协议流。

- [ ] **Step 3: 运行会话测试**

Run: `cd packages/pyruntime && go test -race ./process -run 'TestWorker' -count=1`

Expected: PASS，超时 case 不留存 fake worker 进程。

- [ ] **Step 4: 提交**

```bash
git add packages/pyruntime/process
git commit -m "feat(pyruntime): manage worker sessions and timeouts"
```

### Task 5: 实现 supervisor 自动重建和 crash-loop 熔断

**Files:**
- Create: `packages/pyruntime/process/supervisor.go`
- Create: `packages/pyruntime/process/supervisor_test.go`

- [ ] **Step 1: 写崩溃补位、退避、drain 和轮换测试**

```go
func TestSupervisorReplacesCrashedWorker(t *testing.T) {
	s := newSupervisorWithSequence(t, "crash", "echo")
	_, _ = s.Run(context.Background(), RunRequest{RequestID: "first"})
	got, err := s.Run(context.Background(), RunRequest{RequestID: "second", Meta: []byte(`{"ok":true}`)})
	if err != nil || s.Restarts() != 1 || len(got.Meta) == 0 { t.Fatalf("restarts=%d err=%v", s.Restarts(), err) }
}
```

- [ ] **Step 2: 实现状态机和 factory**

```go
type Factory func(context.Context) (Worker, error)
type SupervisorConfig struct { StartTimeout, BackoffMin, BackoffMax time.Duration; MaxConsecutiveFailures, MaxTasksPerWorker int; MaxLifetime time.Duration }
```

重建成功前 worker 不返回 ready；超过连续失败阈值进入 failed，健康状态必须反映 degraded/unready。

- [ ] **Step 3: 使用 fake clock 验证退避，不在测试中真等待**

Run: `cd packages/pyruntime && go test -race ./process -run TestSupervisor -count=1`

Expected: PASS，且无 sleep 超过 100ms 的测试。

- [ ] **Step 4: 提交**

```bash
git add packages/pyruntime/process
git commit -m "feat(pyruntime): supervise and rebuild workers"
```

### Task 6: 实现 source hash 不可变版本发布与 LOAD

**Files:**
- Create: `packages/pyruntime/moduleregistry/publisher.go`
- Create: `packages/pyruntime/moduleregistry/publisher_test.go`
- Modify: `packages/pyruntime/process/worker.go`

- [ ] **Step 1: 写 hash 重算、原子发布、同名版本隔离和路径穿越测试**

```go
func TestMaterializeRejectsLogicalIDTraversal(t *testing.T) {
	_, err := NewSourcePublisher(t.TempDir()).Publish(context.Background(), ModuleSource{Type: "factor", LogicalID: "../x", Source: []byte("x=1")})
	if !errors.Is(err, ErrInvalidLogicalID) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: 实现物化接口**

```go
type ModuleSource struct { Type, LogicalID string; Source []byte }
type ModuleVersion struct { Type, LogicalID, SourceHash, Path string }
func (p *SourcePublisher) Publish(ctx context.Context, src ModuleSource) (ModuleVersion, error)
```

路径固定为 `<root>/<type>/<logical_id>/<sha256>/module.py`，先写 temp dir、fsync、rename；已存在 hash 直接复用，不覆盖。

- [ ] **Step 3: 让 Worker.Load 核对 Python 返回的 source hash 和入口清单**

Run: `cd packages/pyruntime && go test ./moduleregistry ./process -count=1`

Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add packages/pyruntime/moduleregistry packages/pyruntime/process
git commit -m "feat(pyruntime): materialize immutable Python modules"
```

### Task 7: 实现可并发调度的 worker 池

**Files:**
- Create: `packages/pyruntime/pool/pool.go`
- Create: `packages/pyruntime/pool/pool_test.go`

- [ ] **Step 1: 用 barrier fake worker 写“确实同时运行”测试**

```go
func TestPoolRunsOnTwoWorkersConcurrently(t *testing.T) {
	p := newBarrierPool(t, 2)
	start := time.Now()
	runTwo(t, p, 100*time.Millisecond)
	if time.Since(start) >= 180*time.Millisecond { t.Fatal("tasks ran serially") }
}
```

- [ ] **Step 2: 实现每 worker 一个 mailbox 的池**

```go
type Request struct { ShardKey string; Run process.RunRequest }
type Pool interface { Run(context.Context, Request) (process.RunResult, error); BroadcastLoad(context.Context, process.LoadRequest) error; Status() Status; Close() error }
```

`hash(ShardKey)%N` 提供稳定分片，空 `ShardKey` 使用 least-busy；每 worker 只有一个消费协程，队列满返明确 backpressure 错误。

- [ ] **Step 3: 验证并发、保序、关闭和 worker 替换**

Run: `cd packages/pyruntime && go test -race ./pool -count=1`

Expected: PASS，同 shard 请求顺序不变，不同 shard 能并行。

- [ ] **Step 4: 提交**

```bash
git add packages/pyruntime/pool
git commit -m "feat(pyruntime): add concurrent supervised worker pool"
```

### Task 8: 实现 Arrow stream 编解码（已完成）

**Files:**
- Create: `packages/pyruntime/transport/encoding.go`
- Create: `packages/pyruntime/transport/arrow.go`
- Create: `packages/pyruntime/transport/arrow_test.go`
- Modify: `packages/pyruntime/go.mod`

- [x] **Step 1: 写 null、UTC 时间、float64 和 string 往返测试**

```go
func TestArrowRoundTripPreservesNullAndUTC(t *testing.T) {
	table := Table{Columns: []Column{{Name:"close", Type:Float64, Values:[]any{1.5,nil}}, {Name:"time", Type:TimestampMS, Values:[]any{time.UnixMilli(1).UTC(),time.UnixMilli(2).UTC()}}}}
	got := roundTripArrow(t, table)
	assertTableEqual(t, table, got)
}
```

- [x] **Step 2: 加入 `github.com/apache/arrow-go/v18` 并实现 stream/file codec**

```go
type Table struct { Columns []Column; Rows int }
type Codec interface { Encode(Table) ([]byte, error); Decode([]byte) (Table, error) }
```

只允许 schema 白名单类型；timestamp 固定 UTC 毫秒；解码后验证 row count 和 schema hash。

- [x] **Step 3: 运行编解码和内存泄漏测试**

Run: `cd packages/pyruntime && go test -race ./transport -count=1`

Expected: PASS，Arrow record/allocator 均 release。

- [x] **Step 4: 提交**

```bash
git add packages/pyruntime/go.mod packages/pyruntime/go.sum packages/pyruntime/transport
git commit -m "feat(pyruntime): add Arrow stream transport"
```

### Task 9: 实现 Arrow mmap 快照生命周期（已完成）

**Files:**
- Create: `packages/pyruntime/snapshot/store.go`
- Create: `packages/pyruntime/snapshot/store_test.go`

- [x] **Step 1: 写单份去重、引用计数和 TTL reaper 测试**

```go
func TestAcquireSameKeyCreatesOneFile(t *testing.T) {
	s := NewStore(Config{Root:t.TempDir(), TTL:time.Minute})
	a := acquireTable(t, s, "same"); b := acquireTable(t, s, "same")
	if a.Path != b.Path || s.Status().Files != 1 || s.Status().References != 2 { t.Fatal("snapshot not shared") }
}
```

- [x] **Step 2: 实现 store、handle 和只读 mmap reader**

```go
type Key struct { Namespace, DataRevision, SchemaHash string; InputHash [32]byte }
type Handle struct { ID, Hash, SchemaHash, Path string; Rows int64; Bytes int64; release func() }
func (h *Handle) Release() error
```

创建时使用 temp + fsync + rename；reaper 只删除 ref=0 且超过 TTL 的文件；启动时清理无法被索引恢复的过期残留。

- [x] **Step 3: 运行并发 acquire/release 测试**

Run: `cd packages/pyruntime && go test -race ./snapshot -count=1`

Expected: PASS，100 个并发 acquire 仍只有一个快照文件。

- [x] **Step 4: 提交**

```bash
git add packages/pyruntime/snapshot
git commit -m "feat(pyruntime): manage shared mmap snapshots"
```

### Task 10: 提供 Python 端协议、日志捕获和 mmap SDK（已完成基础契约）

**Files:**
- Create: `packages/pyruntime/python/moox_pyruntime/__init__.py`
- Create: `packages/pyruntime/python/moox_pyruntime/protocol.py`
- Create: `packages/pyruntime/python/moox_pyruntime/arrow.py`
- Create: `packages/pyruntime/python/moox_pyruntime/capture.py`
- Create: `packages/pyruntime/python/tests/test_runtime.py`

- [x] **Step 1: 写 Python frame/Arrow codec 测试**

```python
def test_capture_prevents_business_stdout_from_touching_protocol():
    with capture_output(limit_bytes=128) as logs:
        print("factor log")
    assert logs.stdout == "factor log\n"
```

- [x] **Step 2: 实现与 Go 同版本的帧 codec**

`protocol.py` 定义同样的 type byte、大小上限和 JSON meta；业务 worker 使用 `redirect_stdout/redirect_stderr` 捕获日志；`arrow.py` 用 `pyarrow.memory_map(path, "r")` 打开 Arrow IPC file 并转 pandas。

- [ ] **Step 3: 固定 Copy-on-Write 语义**（由 Factor/Strategy worker 各自启用）

```python
pd.options.mode.copy_on_write = True
def isolated_frame(base: pd.DataFrame) -> pd.DataFrame:
    return base.copy(deep=False)
```

- [x] **Step 4: 运行 Python 测试**

Run: `cd packages/pyruntime && python3 -m pytest python/tests -q`

Expected: PASS；本机如未安装 pytest，先按 lock file 创建 venv，不得用“未安装”跳过 CI 验收。

- [ ] **Step 5: 提交**

```bash
git add packages/pyruntime/python
git commit -m "feat(pyruntime): add Python runtime SDK"
```

### Task 11: 接入指标、健康快照和运行环境 hash

**Files:**
- Create: `packages/pyruntime/metrics/metrics.go`
- Create: `packages/pyruntime/metrics/metrics_test.go`
- Modify: `packages/pyruntime/process/config.go`
- Modify: `packages/pyruntime/pool/pool.go`

- [ ] **Step 1: 写 label 基数和 health 聚合测试**

```go
func TestMetricsDoNotUseRequestIDAsLabel(t *testing.T) {
	reg := prometheus.NewRegistry(); m := New(reg)
	m.ObserveTask("factor", "json", "ok", time.Millisecond)
	assertMetricLabels(t, reg, "moox_pyruntime_worker_task_total", []string{"module_type","encoding","status"})
}
```

- [ ] **Step 2: 实现 `moox_pyruntime_` 指标和 Status**

`request_id`、`logical_id`、`source_hash` 只入日志/trace，不进 Prometheus label。`Status` 包含 configured/ready/busy/restarting/failed worker 数、队列深度、快照字节和最近错误时间。

- [ ] **Step 3: 计算并校验 runtime env hash**

hash 输入固定为 Python 可执行文件 hash、lock file hash、SDK hash、OS/arch；不包含临时路径和时间。

- [ ] **Step 4: 运行全包测试并提交**

Run: `cd packages/pyruntime && go test -race ./... -count=1`

Expected: PASS。

```bash
git add packages/pyruntime
git commit -m "feat(pyruntime): expose metrics and runtime health"
```

### Task 12: 完成交叉契约、基准测试和文档验收

**Files:**
- Create: `packages/pyruntime/protocol/contract_test.go`
- Create: `packages/pyruntime/transport/benchmark_test.go`
- Create: `packages/pyruntime/README.md`
- Modify: `docs/Python计算运行时架构设计.md`

- [ ] **Step 1: 用 Go 写帧、Python 读帧，再反向验证**

Run: `cd packages/pyruntime && go test ./protocol -run TestGoPythonFrameContract -count=1`

Expected: PASS，所有 message type 和 0/1MB payload 均往返一致。

- [ ] **Step 2: 比较 JSON、Arrow stream 和 mmap**

Run: `cd packages/pyruntime && go test ./transport -bench 'Benchmark(JSON|Arrow|MMap)' -benchmem -run '^$'`

Expected: 输出 ns/op、B/op、allocs/op；文档记录阈值，不以没有证据的“零拷贝”作验收。

- [ ] **Step 3: 验证故障情况**

Run: `cd packages/pyruntime && go test -race ./... -count=1 && python3 -m pytest python/tests -q`

Expected: PASS，并覆盖超时、crash loop、stdout 污染、stderr 截断、snapshot 残留清理。

- [ ] **Step 4: 更新 README 与架构文档的“已实现”清单**

README 必须给出 Go 业务 codec 接入范例、Python worker 入口范例、配置表和故障语义。

- [ ] **Step 5: 提交**

```bash
git add packages/pyruntime/README.md packages/pyruntime/protocol packages/pyruntime/transport docs/Python计算运行时架构设计.md
git commit -m "test(pyruntime): verify runtime contracts and performance"
```

## 最终验收

Run:

```bash
go work sync
cd packages/pyruntime && go test -race ./... -count=1
cd packages/pyruntime && python3 -m pytest python/tests -q
npm run docs:build
git diff --check
```

Expected:

- 全部命令 PASS，且 `go.work`/`go.sum` 无未提交变化。
- 两个 fake worker 的 100ms 任务墙钟明显小于 200ms，证明池是真并发。
- worker 超时/崩溃后自动补位，health 不会把 dead worker 计为 ready。
- 同 snapshot key 被多 worker 使用时只有一个 Arrow file，引用归零后可回收。
- Go/Python 帧、Arrow schema、runtime hash 契约有交叉测试防漂移。
# 实施状态（2026-07-11）

已完成首版实现：`packages/pyruntime` 提供统一 HELLO/LOAD/RUN 帧协议、常驻 Python worker、Supervisor 重启与退避、worker pool、源码版本发布（`moduleregistry.SourcePublisher`）、快照存储、限制校验和端到端测试。因子与策略模块均已接入该运行时，并分别在模块根目录 `test/` 增加 Python 真实进程端到端用例。Prometheus 指标、runtime env hash 聚合和生产健康页仍属于后续任务，不能按已实现能力使用。

当前传输可按 HELLO 能力和任务大小选择受限 JSON、Arrow IPC stream 或 Arrow IPC file/mmap。`packages/pyruntime/python/moox_pyruntime` 提供 Python 侧的标准 Arrow 和帧契约；业务模块仍需在自己的 codec 中接入选择策略。pyarrow 未安装的环境必须显式回退 JSON。
