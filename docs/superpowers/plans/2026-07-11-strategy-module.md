# Strategy Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新建 `modules/strategy` 交易策略模块，让同一份 Python 策略和显式状态在回测、观察、模拟和实盘中共用，只在最后的 ExecutionPort 上区分执行方式。

**Architecture:** Go 负责策略版本、point-in-time 数据快照、Binding 保序调度、状态 CAS、目标组合、风控、Inbox/Outbox 和执行编排；Python 只实现 `run(context, data, params, state)` 并返回 `action + targets + next_state`。回测和实时共用 `StrategyEngine` 与 action commit，历史回放器和实时 trigger 只生成同一 `StrategyTask`。

**Tech Stack:** Go 1.24、tRPC-Go、SQLite/GORM、`packages/pyruntime`、Python 3.12+、pandas/pyarrow、NATS JetStream、Storage DataView、Trade RebalanceSvc、Parquet、Prometheus。

---

## 交付分期与上线门禁

1. M1 契约 + `run-once`：不接交易，先固定策略包、Python I/O、状态事务。
2. M2 回测：用同一 engine/action 闭环历史回放和 Parquet 产物。
3. M3 实时 + observe/paper：完成 readiness、Outbox 和虚拟账户，仍不开 live。
4. M4 多策略组合：固定资金比例聚合与组合风控。
5. M5 live：完成 Trade 协议前置改造、查询对账和目标偏差监控后才开启。
6. M6 管理面、运维和真实环境验证。

前置依赖：[Python Runtime Implementation Plan](2026-07-11-python-runtime.md) R1–R4 已完成；Factor 已为结果提供 source/snapshot/computed_at 可追溯信息。

## 目标文件图

```text
modules/strategy/
├── cmd/{server,cli}/
├── config/{app.yaml,trpc_go.yaml}
├── proto/{strategy.proto,strategygen/}
├── schema/{strategy.sql,schema.go}
├── strategies/
├── pyworker/{worker.py,strategy_adapter.py,runtime-requirements.txt,test_worker.py}
├── pysdk/moox_strategy/{__init__.py,types.py,validate.py,logging.py}
└── internal/
    ├── app/control/
    ├── domain/
    ├── repository/
    ├── registry/
    ├── snapshot/
    ├── engine/
    ├── scheduler/
    ├── action/
    ├── combine/
    ├── risk/
    ├── execution/
    ├── backtest/
    ├── trigger/
    ├── bus/
    ├── rpc/
    └── observability/
```

### Task 1: 创建 module、server/CLI 骨架和配置

**Files:**
- Create: `modules/strategy/go.mod`
- Create: `modules/strategy/cmd/server/main.go`
- Create: `modules/strategy/cmd/cli/main.go`
- Create: `modules/strategy/config/app.yaml`
- Create: `modules/strategy/config/trpc_go.yaml`
- Create: `modules/strategy/internal/app/control/config.go`
- Create: `modules/strategy/internal/app/control/config_test.go`
- Modify: `go.work`

- [ ] **Step 1: 写配置默认值、环境变量覆盖和非法 live 配置测试**

```go
func TestLiveRequiresTradeTargetAndRuntimeHash(t *testing.T) {
	cfg := ValidConfig(); cfg.Trade.Target=""; cfg.Engine.RuntimeEnvHash=""
	if err := cfg.Validate(); err == nil { t.Fatal("live prerequisites must be explicit") }
}
```

- [ ] **Step 2: 创建 module 依赖**

`go.mod` 引用 storage/trade 生成 proto、commonpb、healthz、jetstream、pyruntime、GORM/SQLite、Prometheus、yaml，本地 module 全部用 replace。`go.work` 加入 `./modules/strategy`。

- [ ] **Step 3: 实现配置结构**

```go
type Config struct { Database DatabaseConfig; Storage StorageConfig; Trade TradeConfig; NATS NATSConfig; Engine EngineConfig; Scheduler SchedulerConfig; Readiness ReadinessConfig; Backtest BacktestConfig; Health HealthConfig }
```

`live_enabled=false` 为默认；不允许因 Trade 未配置而自动回落 paper。

- [ ] **Step 4: 编译骨架并提交**

Run: `cd modules/strategy && go test ./internal/app/control ./cmd/... -count=1`

Expected: PASS。

```bash
git add go.work modules/strategy
git commit -m "feat(strategy): scaffold strategy service module"
```

### Task 2: 定义 Strategy proto 与稳定事件契约

**Files:**
- Create: `modules/strategy/proto/strategy.proto`
- Create: `modules/strategy/proto/Makefile`
- Create: `modules/strategy/proto/strategygen/go.mod`
- Create: `modules/strategy/proto/contract_test.go`
- Modify: `go.work`

- [ ] **Step 1: 先写 descriptor contract test**

```go
func TestTargetWeightUsesInstrumentIDAndDecimalString(t *testing.T) {
	d := (&strategypb.TargetWeight{}).ProtoReflect().Descriptor()
	assertField(t, d, "instrument_id", protoreflect.StringKind)
	assertField(t, d, "target_weight", protoreflect.StringKind)
}
```

- [ ] **Step 2: 定义核心 message 和 RPC**

proto 包含 `StrategyDef/Binding/Group/ExecutionBinding/State/Run/TargetWeight/BacktestJob`，以及架构文档列出的 StrategyMgr RPC。金额、权重和数量用 decimal string，时间用 RFC3339Nano string，不用 float32/float64 表达资金。

- [ ] **Step 3: 定义 EventBus protobuf**

`StrategyActionAccepted`、`StrategyGroupTargetReady`、`StrategyExecutionRequested`、`StrategyExecutionStateChanged` 必须包含 message_id、space_id、业务幂等键和 trace context。

- [ ] **Step 4: 生成代码、运行 contract test 并提交**

Run: `cd modules/strategy/proto && make && go test ./... -count=1`

Expected: PASS，`go.work` 加入 `./modules/strategy/proto/strategygen`。

```bash
git add go.work modules/strategy/proto
git commit -m "feat(strategy): define manager and event contracts"
```

### Task 3: 建立领域模型、SQLite schema 和 repository

**Files:**
- Create: `modules/strategy/internal/domain/definition.go`
- Create: `modules/strategy/internal/domain/binding.go`
- Create: `modules/strategy/internal/domain/group.go`
- Create: `modules/strategy/internal/domain/execution.go`
- Create: `modules/strategy/internal/domain/state.go`
- Create: `modules/strategy/internal/domain/run.go`
- Create: `modules/strategy/internal/domain/target.go`
- Create: `modules/strategy/internal/domain/backtest.go`
- Create: `modules/strategy/schema/strategy.sql`
- Create: `modules/strategy/schema/schema.go`
- Create: `modules/strategy/schema/schema_test.go`
- Create: `modules/strategy/internal/repository/repository.go`
- Create: `modules/strategy/internal/repository/strategy.go`
- Create: `modules/strategy/internal/repository/state.go`
- Create: `modules/strategy/internal/repository/execution.go`
- Create: `modules/strategy/internal/repository/outbox.go`
- Create: `modules/strategy/internal/repository/repository_test.go`

- [ ] **Step 1: 写唯一约束、state CAS 和 Outbox 同事务测试**

```go
func TestCommitActionCASRejectsStaleState(t *testing.T) {
	r := newRepo(t); seedState(t,r,"b1",3)
	err := r.CommitAction(t.Context(), Commit{BindingID:"b1", PreviousRevision:2, Run:runFixture(), StateJSON:`{}`})
	if !errors.Is(err, ErrStateConflict) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: 实现架构文档中的 14 张表**

每表统一 `c_ctime/c_mtime`，业务唯一约束必须在 SQL 级落地；`t_strategy_runs` 唯一键是 `(binding_id,strategy_version,trigger_bar_time,namespace)`，`t_strategy_outbox.message_id` 和 inbox `(consumer,message_id)` 唯一。

- [ ] **Step 3: 实现聚合 repository 和 `WithTx`**

```go
func (r *Repository) WithTx(ctx context.Context, fn func(*Repository) error) error
func (r *Repository) CommitAction(ctx context.Context, c Commit) error
```

CommitAction 在同事务写 run、targets、CAS state、outbox；任一步失败全部回滚。

- [ ] **Step 4: 运行 SQLite 真实测试并提交**

Run: `cd modules/strategy && go test -race ./schema ./internal/repository -count=1`

Expected: PASS。

```bash
git add modules/strategy/schema modules/strategy/internal/domain modules/strategy/internal/repository
git commit -m "feat(strategy): persist strategy state and execution facts"
```

### Task 4: 实现策略包校验与不可变版本发布

**Files:**
- Create: `modules/strategy/internal/registry/manifest.go`
- Create: `modules/strategy/internal/registry/service.go`
- Create: `modules/strategy/internal/registry/service_test.go`
- Create: `modules/strategy/strategies/example/strategy.yaml`
- Create: `modules/strategy/strategies/example/strategy.py`

- [ ] **Step 1: 写 manifest schema、source hash 和禁止覆盖测试**

```go
func TestPublishRejectsChangingExistingVersion(t *testing.T) {
	r := newRegistry(t); publish(t,r,"demo","1.0.0","return hold")
	_, err := r.Publish(t.Context(), packageWith("demo","1.0.0","return rebalance"))
	if !errors.Is(err, ErrImmutableVersion) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: 解析并校验 `strategy.yaml`**

必填 api_version、id、version、entrypoint、data columns/lookback、params schema、state schema version；拒绝未知根字段和重复列。

- [ ] **Step 3: 使用 pyruntime Materializer 发布源码**

发布顺序是 parse manifest、重算 hash、物化、LOAD 验证 `run`、写 DB；不把 draft 文件目录当运行版本。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/strategy && go test ./internal/registry -count=1`

Expected: PASS。

```bash
git add modules/strategy/internal/registry modules/strategy/strategies
git commit -m "feat(strategy): publish immutable strategy packages"
```

### Task 5: 实现 Python SDK、worker adapter 和输出校验

**Files:**
- Create: `modules/strategy/pysdk/moox_strategy/__init__.py`
- Create: `modules/strategy/pysdk/moox_strategy/types.py`
- Create: `modules/strategy/pysdk/moox_strategy/validate.py`
- Create: `modules/strategy/pysdk/moox_strategy/logging.py`
- Create: `modules/strategy/pysdk/tests/test_validate.py`
- Create: `modules/strategy/pyworker/worker.py`
- Create: `modules/strategy/pyworker/strategy_adapter.py`
- Create: `modules/strategy/pyworker/test_worker.py`
- Create: `modules/strategy/pyworker/runtime-requirements.txt`
- Create: `modules/strategy/internal/engine/types.go`
- Create: `modules/strategy/internal/engine/codec.go`
- Create: `modules/strategy/internal/engine/executor.go`
- Create: `modules/strategy/internal/engine/engine_test.go`

- [ ] **Step 1: 写 hold/rebalance、next_state、非法权重和 warm worker 确定性测试**

```python
def test_rebalance_requires_complete_unique_targets():
    with pytest.raises(ContractError):
        validate_output({"action":"rebalance","targets":[{"instrument_id":"BTC","target_weight":"0.5"},{"instrument_id":"BTC","target_weight":"0.5"}],"next_state":{}})
```

- [ ] **Step 2: 实现最小 SDK**

SDK 只包含 `StrategyOutput`、`empty_targets()`、decimal/time/instrument 校验和 `strategy_log()`，不提供 Storage/Trade/网络客户端。

- [ ] **Step 3: 实现 `StrategyAdapter`**

LOAD 校验 `run(context,data,params,state)`；RUN 传入只读 context、mmap/stream DataFrame、params、state，捕获 logs，返回结构化 RESULT。不在 worker 中保存 state 或 previous targets。

- [ ] **Step 4: 实现 Go `StrategyEngine`**

```go
type StrategyEngine interface { Load(context.Context, StrategyVersion) error; Run(context.Context, StrategyTask, InputRef) (StrategyOutput, error); Status() RuntimeStatus; Close() error }
```

Go 再次校验 action、权重 decimal、标的唯一性、state JSON 大小和 output hash，不信任 Python SDK 已校验。

- [ ] **Step 5: 运行 Python/Go 测试并提交**

Run: `cd modules/strategy && PYTHONPATH=../../packages/pyruntime/python:./pysdk python3 -m pytest pysdk/tests pyworker -q && go test ./internal/engine -count=1`

Expected: PASS。

```bash
git add modules/strategy/pysdk modules/strategy/pyworker modules/strategy/internal/engine
git commit -m "feat(strategy): execute versioned Python strategies"
```

### Task 6: 补齐 Storage point-in-time 查询契约

**Files:**
- Modify: `modules/storage/proto/view.proto`
- Modify: `modules/storage/proto/view_index.proto`
- Modify: `modules/storage/internal/services/view/service.go`
- Modify: `modules/storage/internal/services/view/query_options.go`
- Modify: `modules/storage/internal/services/view/query_options_test.go`
- Modify: `modules/storage/internal/services/viewindex/client.go`
- Modify: `modules/storage/internal/services/viewindex/service.go`
- Modify: `modules/storage/internal/services/viewindex/service_test.go`
- Create: `modules/storage/internal/services/view/point_in_time_test.go`
- Create: `modules/strategy/internal/snapshot/client.go`
- Create: `modules/strategy/internal/snapshot/normalize.go`
- Create: `modules/strategy/internal/snapshot/client_test.go`

- [ ] **Step 1: 写 `data_cutoff/data_revision` 防未来数据测试**

```go
func TestQueryAtCutoffExcludesLaterVisibleRows(t *testing.T) {
	seedRows(t, rowAt("10:00", "rev1"), rowAt("10:01", "rev2"))
	got := queryAt(t, "10:00", "rev1")
	if containsTime(got, "10:01") { t.Fatal("future row leaked") }
}
```

- [ ] **Step 2: 扩展 QueryTimeSeriesRowsReq/Rsp**

请求增加 `data_cutoff`、`data_revision`、`available_at_cutoff`；响应回传实际 revision、schema revision 和 snapshot hash。无法满足指定 revision 时显式失败，不回落 current active View。

- [ ] **Step 3: 实现 Strategy snapshot normalizer**

按 manifest 要求回读有序列、lookback 和 universe，固定 UTC 时间、instrument_id、null 规则，并用 pyruntime 生成 InputRef。

- [ ] **Step 4: 重新生成 storage proto 并运行测试**

Run: `cd modules/storage/proto && make && cd .. && go test ./internal/services/view/... ./internal/services/viewindex/... -count=1`

Run: `cd modules/strategy && go test ./internal/snapshot -count=1`

Expected: PASS，回测查询不可见 cutoff 后数据。

- [ ] **Step 5: 提交**

```bash
git add modules/storage/proto modules/storage/internal/services/view modules/storage/internal/services/viewindex modules/strategy/internal/snapshot
git commit -m "feat(storage): support point-in-time strategy snapshots"
```

### Task 7: 实现 run-once、action commit 和状态 CAS

**Files:**
- Create: `modules/strategy/internal/action/validate.go`
- Create: `modules/strategy/internal/action/service.go`
- Create: `modules/strategy/internal/action/service_test.go`
- Create: `modules/strategy/internal/scheduler/task.go`
- Create: `modules/strategy/internal/scheduler/service.go`
- Create: `modules/strategy/internal/scheduler/service_test.go`
- Create: `modules/strategy/cmd/cli/run_once.go`
- Create: `modules/strategy/cmd/cli/run_once_test.go`

- [ ] **Step 1: 写相同幂等键仅接受一次和 stale state 拒绝测试**

```go
func TestRunSameLogicalPointReturnsAcceptedRun(t *testing.T) {
	s := fixture(t); first := s.Run(task("2026-07-11T10:00:00Z")); second := s.Run(task("2026-07-11T10:00:00Z"))
	if first.RunID != second.RunID || s.EngineCalls()!=1 { t.Fatal("logical point executed twice") }
}
```

- [ ] **Step 2: 实现 Binding 串行 scheduler**

`shard=hash(binding_id)%workers`，同 Binding 按 trigger time 串行；已有 accepted run 直接返回；旧 bar 晚到默认记录 skipped，不改写当前 state。

- [ ] **Step 3: 实现 action commit**

`hold` 保留 previous targets，但仍可推进 next_state；`rebalance` 完整替换 targets。输出校验后在一个 SQLite 事务中 CAS state、写 run/targets/outbox。

- [ ] **Step 4: 实现 `run-once --no-commit` 和正式 commit 模式**

`--no-commit` 输出 input/source/state/output hash 与 targets，不写 DB；默认 run-once 在明确 `--commit` 时才推进 state。

- [ ] **Step 5: 运行测试并提交**

Run: `cd modules/strategy && go test -race ./internal/action ./internal/scheduler ./cmd/cli -count=1`

Expected: PASS。

```bash
git add modules/strategy/internal/action modules/strategy/internal/scheduler modules/strategy/cmd/cli
git commit -m "feat(strategy): commit deterministic strategy actions"
```

### Task 8: 实现策略组合与组合风控

**Files:**
- Create: `modules/strategy/internal/combine/combine.go`
- Create: `modules/strategy/internal/combine/combine_test.go`
- Create: `modules/strategy/internal/risk/policy.go`
- Create: `modules/strategy/internal/risk/validate.go`
- Create: `modules/strategy/internal/risk/validate_test.go`
- Modify: `modules/strategy/internal/action/service.go`

- [ ] **Step 1: 写 hold/rebalance 组合、decimal 精度和风控拒绝测试**

```go
func TestCombineAppliesCapitalWeightWithoutNormalization(t *testing.T) {
	got := Combine([]MemberTarget{{CapitalWeight:dec("0.6"), Targets:weights("BTC","0.5")},{CapitalWeight:dec("0.4"), Targets:weights("BTC","-0.25")}})
	assertWeight(t, got, "BTC", "0.20")
}
```

- [ ] **Step 2: 使用 decimal 库实现固定资金聚合**

不用 float64，不自动归一化。成员新目标 accepted 后读所有 active 成员最新完整目标，生成单调 group revision 和 target hash。

- [ ] **Step 3: 实现 gross/net/单标的/标的数量/空头开关门禁**

风控失败时保留策略级 accepted action，但 group target 状态 rejected，不生成 execution request。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/strategy && go test ./internal/combine ./internal/risk ./internal/action -count=1`

Expected: PASS。

```bash
git add modules/strategy/internal/combine modules/strategy/internal/risk modules/strategy/internal/action
git commit -m "feat(strategy): combine and validate portfolio targets"
```

### Task 9: 实现 Inbox/Outbox 与 Strategy EventBus

**Files:**
- Modify: `modules/eventbus/config/app.yaml`
- Modify: `modules/eventbus/internal/config/config_test.go`
- Modify: `modules/eventbus/internal/registry/registry_test.go`
- Create: `modules/strategy/internal/bus/outbox.go`
- Create: `modules/strategy/internal/bus/inbox.go`
- Create: `modules/strategy/internal/bus/relay.go`
- Create: `modules/strategy/internal/bus/relay_test.go`
- Modify: `modules/strategy/internal/app/control/bootstrap.go`

- [ ] **Step 1: 写发布后崩溃不重复业务处理的测试**

```go
func TestInboxDeduplicatesRedelivery(t *testing.T) {
	h := newHandler(t); msg := actionMessage("m1")
	h.Handle(t.Context(), msg); h.Handle(t.Context(), msg)
	if h.BusinessCalls()!=1 { t.Fatalf("calls=%d", h.BusinessCalls()) }
}
```

- [ ] **Step 2: 声明 `MOOX_STRATEGY` limits-retention stream**

subjects 为 `moox.strategy.>`，max_age 168h；增加 `strategy_group_target_v1` 和 `strategy_execution_v1` durable，凭证 ACL 只允许 strategy 发布/消费所需 subject。

- [ ] **Step 3: 实现 Outbox relay 与 Inbox 事务**

relay 按 ctime/message_id 批量抢占、发布 protobuf、标记 published；失败指数退避。consumer 在同事务写 inbox 和业务结果，成功后 Ack。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/eventbus && go test ./... -count=1`

Run: `cd modules/strategy && go test -race ./internal/bus -count=1`

Expected: PASS。

```bash
git add modules/eventbus modules/strategy/internal/bus modules/strategy/internal/app/control
git commit -m "feat(strategy): relay strategy events with inbox outbox"
```

### Task 10: 实现 ObserveExecution 和 PaperExecution

**Files:**
- Create: `modules/strategy/internal/execution/port.go`
- Create: `modules/strategy/internal/execution/observe.go`
- Create: `modules/strategy/internal/execution/paper.go`
- Create: `modules/strategy/internal/execution/execution_test.go`
- Create: `modules/strategy/internal/domain/paper.go`

- [ ] **Step 1: 写 observe 不产生 Trade 请求和 paper 幂等收敛测试**

```go
func TestObserveNeverCallsTrade(t *testing.T) {
	trade := &panicTrade{}; p := NewObserve(trade)
	if _, err := p.Submit(t.Context(), requestFixture()); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: 定义唯一执行端口**

```go
type Port interface { Submit(context.Context, ExecutionRequest) (ExecutionResult, error); Inspect(context.Context, string) (ExecutionResult, error) }
```

Observe 保存理论目标/绩效并返回 accepted；Paper 按固定 fee/slippage/latency 规则更新虚拟现金、持仓和成交，所有规则版本进结果 hash。

- [ ] **Step 3: 实现每 ExecutionBinding 独立失败语义**

同 group 多账户的执行请求分别持久化，某一失败不改写其他 request，也不回滚 group target。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/strategy && go test -race ./internal/execution -count=1`

Expected: PASS。

```bash
git add modules/strategy/internal/execution modules/strategy/internal/domain
git commit -m "feat(strategy): add observe and paper execution ports"
```

### Task 11: 实现共用 StrategyEngine 的历史回测

**Files:**
- Create: `modules/strategy/internal/backtest/job.go`
- Create: `modules/strategy/internal/backtest/replayer.go`
- Create: `modules/strategy/internal/backtest/account.go`
- Create: `modules/strategy/internal/backtest/writer.go`
- Create: `modules/strategy/internal/backtest/backtest_test.go`
- Create: `modules/strategy/cmd/cli/backtest.go`
- Create: `modules/strategy/cmd/cli/backtest_test.go`

- [ ] **Step 1: 写相同配置重复回测 hash/NAV 完全一致测试**

```go
func TestBacktestIsDeterministic(t *testing.T) {
	a := runBacktest(t, fixtureConfig()); b := runBacktest(t, fixtureConfig())
	if a.DecisionHash!=b.DecisionHash || a.FillHash!=b.FillHash || a.FinalNAV!=b.FinalNAV { t.Fatal("nondeterministic backtest") }
}
```

- [ ] **Step 2: 实现 HistoricalReplayer**

按 freq/schedule 生成逻辑时间点，每个点查 point-in-time snapshot，调同一 scheduler/engine/action，但使用 `backtest:<id>` namespace 和隔离 state repository。默认任一 bar 失败终止，不跳过。

- [ ] **Step 3: 写 Parquet 产物和 SQLite 摘要**

Parquet 包含 decision/group target/order/fill/position/nav/deviation 表，路径按 backtest_id 隔离；SQLite 只写 job 进度、配置 hash、摘要和产物 hash。

- [ ] **Step 4: 运行测试和 CLI 小样本**

Run: `cd modules/strategy && go test ./internal/backtest ./cmd/cli -count=1`

Expected: PASS，100 bar 小样本重复执行 hash 一致。

- [ ] **Step 5: 提交**

```bash
git add modules/strategy/internal/backtest modules/strategy/cmd/cli
git commit -m "feat(strategy): backtest through the production strategy engine"
```

### Task 12: 实现实时 trigger 与 readiness gate

**Files:**
- Create: `modules/strategy/internal/trigger/consumer.go`
- Create: `modules/strategy/internal/trigger/readiness.go`
- Create: `modules/strategy/internal/trigger/readiness_test.go`
- Modify: `modules/strategy/internal/app/control/bootstrap.go`
- Modify: `modules/strategy/config/app.yaml`

- [ ] **Step 1: 写到齐、超时、重复事件和晚到 revision 测试**

```go
func TestReadinessEmitsOnceForLogicalBar(t *testing.T) {
	r := newGate(expected("kline","factor")); r.Observe(event("kline","10:00")); r.Observe(event("factor","10:00")); r.Observe(event("factor","10:00"))
	if len(r.Ready())!=1 { t.Fatalf("ready=%d", len(r.Ready())) }
}
```

- [ ] **Step 2: 订阅 `moox.storage.time_series.rows_updated.v1`**

事件只作触发信号；gate 按 Binding 的 trigger dataset + required datasets 统计 `(bar_time,data_revision)`，达到到齐率或超时后生成 StrategyTask。

- [ ] **Step 3: 实现 late-data 规则**

已 accepted 的 live 逻辑 bar 不自动重算；记录 late revision 指标和运行事实，需重算时由显式 replay namespace 启动。

- [ ] **Step 4: 运行事件风暴测试并提交**

Run: `cd modules/strategy && go test -race ./internal/trigger ./internal/app/control -count=1`

Expected: PASS，10,000 重复事件每 Binding/bar 只生成一个 task。

```bash
git add modules/strategy/internal/trigger modules/strategy/internal/app/control modules/strategy/config
git commit -m "feat(strategy): schedule ready real-time strategy bars"
```

### Task 13: 改造 Trade 协议并实现 LiveTradeExecution

**Files:**
- Modify: `modules/trade/proto/trade_service.proto`
- Modify: `modules/trade/internal/domain/rebalance/rebalance.go`
- Modify: `modules/trade/internal/domain/rebalance/rebalance_test.go`
- Modify: `modules/trade/internal/application/rebalance/service.go`
- Modify: `modules/trade/internal/infra/store/store.go`
- Modify: `modules/trade/internal/infra/store/store_test.go`
- Modify: `modules/trade/internal/rpc/server.go`
- Create: `modules/trade/internal/application/rebalance/strategy_attribution_test.go`
- Create: `modules/strategy/internal/execution/live.go`
- Create: `modules/strategy/internal/execution/live_test.go`

- [ ] **Step 1: 写 Strategy 归因、instrument 标识和幂等查询 contract test**

```go
func TestCreateRebalancePreservesStrategyAttribution(t *testing.T) {
	run := createRebalance(t, reqWithStrategy("g1","e1",7))
	got := getRebalance(t, run.RunID)
	if got.StrategyExecutionId!="e1" || got.GroupTargetRevision!=7 { t.Fatal("attribution lost") }
}
```

- [ ] **Step 2: 扩展 Trade proto 与持久化**

`TargetPosition` 增 instrument_id/market_type；CreateRebalance 增 source/strategy_group_id/strategy_execution_id/group_target_revision；增 GetRebalance/ListRebalances；完成事件使用稳定 protobuf，不用 BytesValue JSON。

- [ ] **Step 3: 实现 LiveTradeExecution 的“提交或查询”语义**

先按 strategy execution idempotency key 查询；未存在才 CreateRebalance；网络超时结果未知时不盲目重建，继续 Inspect 直到确定状态。

- [ ] **Step 4: 重新生成 Trade proto 并运行测试**

Run: `cd modules/trade/proto && make && cd .. && go test ./internal/services/rebalance/... ./internal/repository/... -count=1`

Run: `cd modules/strategy && go test ./internal/execution -run TestLive -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add modules/trade modules/strategy/internal/execution
git commit -m "feat(strategy): submit attributed live rebalances"
```

### Task 14: 实现 StrategyMgr RPC 和 CLI

**Files:**
- Create: `modules/strategy/internal/rpc/service.go`
- Create: `modules/strategy/internal/rpc/convert.go`
- Create: `modules/strategy/internal/rpc/service_test.go`
- Create: `modules/strategy/cmd/cli/validate.go`
- Create: `modules/strategy/cmd/cli/import.go`
- Create: `modules/strategy/cmd/cli/inspect.go`
- Modify: `modules/strategy/cmd/cli/main.go`
- Modify: `modules/strategy/internal/app/control/bootstrap.go`

- [ ] **Step 1: 写 CRUD、分页、ResetState 门禁和 RunOnce RPC 测试**

```go
func TestResetStateRequiresPausedBindingAndReason(t *testing.T) {
	s := fixture(t); rsp,_ := s.ResetState(t.Context(), &pb.ResetStateReq{BindingId:"active",Reason:""})
	if rsp.RetInfo.Code==0 { t.Fatal("unsafe reset accepted") }
}
```

- [ ] **Step 2: 实现架构文档列出的 RPC**

所有写操作记录 operator/request_id；分页使用 commonpb Page/PageResult；InspectExecution 可区分“未提交”、“结果未知”和“下游失败”。

- [ ] **Step 3: 实现 init/validate/import/run-once/backtest/inspect-run/inspect-execution CLI**

CLI 的 stdout 只输出结果，stderr 输出进度/错误，失败返回非 0；`validate` 不连 Storage/Trade。

- [ ] **Step 4: 运行测试并提交**

Run: `cd modules/strategy && go test -race ./internal/rpc ./cmd/cli -count=1`

Expected: PASS。

```bash
git add modules/strategy/internal/rpc modules/strategy/cmd/cli modules/strategy/internal/app/control
git commit -m "feat(strategy): expose strategy management RPC and CLI"
```

### Task 15: 接入 Admin、构建、发布和运维

**Files:**
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/admin/config/gateway.yaml`
- Modify: `Makefile`
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Create: `docs/运维/MooX-Strategy运维.md`
- Modify: `docs/SUMMARY.md`

- [ ] **Step 1: 写 SysDeploy 服务 ID/端口/健康路径测试**

`moox_strategy` / `strategymgr`，HTTP `11408`，health/metrics `11418`；Admin 只转发 StrategyMgr，不 import strategy internal。

- [ ] **Step 2: 增加构建与发布产物**

server、CLI、config、schema、strategies、pysdk、pyworker、runtime lock 必须同版本打包；deploy 支持 `--no-strategy`，但不允许只更新 worker 不更新 Go 协议端。

- [ ] **Step 3: 编写运维手册**

包含启停、health、runtime hash、队列积压、worker crash loop、Binding pause、observe 紧急断开、Outbox 积压、Trade 结果未知、state reset 和回滚流程。

- [ ] **Step 4: 运行构建测试并提交**

Run: `make build-strategy && npm run docs:build`

Expected: PASS，产物中包含 Go 二进制和固定 Python runtime 文件。

```bash
git add modules/admin Makefile scripts docs/运维 docs/SUMMARY.md
git commit -m "build(strategy): integrate deployment and operations"
```

### Task 16: 可观测性、全链路验收和 live 开关

**Files:**
- Create: `modules/strategy/internal/observability/health.go`
- Create: `modules/strategy/internal/observability/metrics.go`
- Create: `modules/strategy/internal/observability/metrics_test.go`
- Create: `modules/strategy/internal/testkit/storage_fake.go`
- Create: `modules/strategy/internal/testkit/trade_fake.go`
- Create: `modules/strategy/internal/testkit/runtime_fake.go`
- Create: `modules/strategy/docs/realtime-verification.md`
- Modify: `docs/策略模块架构设计.md`
- Modify: `docs/策略模块Python策略接入手册.md`

- [ ] **Step 1: 实现低基数指标和 readiness**

`strategy_run_total{mode,status}`、`strategy_run_duration_seconds{mode,status}`、`strategy_binding_queue_depth`、`strategy_state_conflict_total`、`strategy_outbox_lag_seconds`、`strategy_execution_total{mode,status}`；Binding/account/strategy ID 不进 label。health 分 DB、NATS、Storage、runtime、Outbox、Trade live dependency。

- [ ] **Step 2: 端到端对比回测和实时 observe**

给定同一 strategy/source/params/state/data snapshot，历史 replayer 与实时 task 的 output hash、next_state hash、group target hash 必须相同。

- [ ] **Step 3: 端到端验证 paper 和 live 幂等**

对同 execution request 重复投递 3 次，paper 只产生一组成交，live Trade 只产生一个 rebalance run；模拟 Trade 超时后 Inspect 能收敛。

- [ ] **Step 4: 运行全仓相关测试**

```bash
cd packages/pyruntime && go test -race ./... -count=1
cd modules/storage && go test ./internal/services/view/... ./internal/services/viewindex/... -count=1
cd modules/trade && go test ./internal/services/rebalance/... -count=1
cd modules/strategy && go test -race ./... -count=1
cd modules/strategy && PYTHONPATH=../../packages/pyruntime/python:./pysdk python3 -m pytest pysdk/tests pyworker -q
npm run docs:build
```

Expected: 全部 PASS。

- [ ] **Step 5: 先在 observe/paper 运行窗口验证，再开 live**

live 开关前必须有：连续运行报告、目标与实际偏差告警、Trade Get/ListRebalance 对账、紧急切 observe 演练、回滚版本和负责人确认。

- [ ] **Step 6: 更新文档状态并提交**

```bash
git add modules/strategy docs/策略模块架构设计.md docs/策略模块Python策略接入手册.md
git commit -m "test(strategy): verify backtest and live execution parity"
```

## 最终验收清单

- 策略开发者只需 `strategy.yaml` 和 `strategy.py`，不接触 Storage/Trade worker 细节。
- 回测、observe、paper、live 使用同一 Python 入口、StrategyEngine、action validator、state CAS 和 group/risk 逻辑。
- 同 Binding 串行且稳定幂等；worker 重试不会重复推进 state 或重复提交 Trade。
- 回测只读 point-in-time 数据，相同配置重复运行的 decision/fill/NAV hash 一致。
- Strategy 不 import Storage/Trade internal，所有跨模块交互经生成 proto 或稳定 EventBus protobuf。
- live 默认关闭，且只在 Trade 归因、查询、幂等、偏差对账和紧急 observe 演练验收后开启。
