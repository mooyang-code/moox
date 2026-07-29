# StrategyRunner 与 LogicalAccount 通用交易执行实施计划

> **供 Agent 执行：** REQUIRED SUB-SKILL: 使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，严格按任务中的 `- [ ]` 顺序实施、验证和提交。

**目标：** 将 Strategy 与 Trade 收敛为适合个人量化的通用执行链：不可变 Strategy、可运行的 StrategyRunner、同质 LogicalAccount、FULL 组合目标、动态择优执行，以及可暂停、人工下单和一键清仓的操作入口。

**架构：** Strategy 是只基于完整历史窗口和参数的无状态函数，只产生按 `instrument_id` 表达的完整理论组合；Trade 以 LogicalAccount 独占执行 ownership，串行 TargetExecutor 每轮最多做一个外部动作。自动执行和人工干预共享订单服务，但 ownership 由服务端赋值。账户私有流和定期同步仍以物理 ExchangeAccount 为边界，LogicalAccount 只聚合控制状态、就绪度和执行选择。

**技术栈：** Go、tRPC/Protobuf、SQLite、JetStream、Python Strategy SDK/Worker、Vue/TypeScript、Binance/OKX API。

**设计依据：** [StrategyRunner and LogicalAccount Trade Design](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/superpowers/specs/2026-07-29-strategy-runner-logical-account-trade-design.md)

---

## 实施边界

- 新项目不迁移旧 Strategy/Trade schema，不做双写、双读或兼容别名；检测到旧表或旧列时拒绝启动，并给出重建数据库的明确错误。
- 不引入 Saga、分布式锁、通用任务引擎、算法插件体系、账户内虚拟仓位或多 Runner 组合聚合层。
- 一个启用的 LogicalAccount 最多由一个启用的 StrategyRunner 控制；一个物理 ExchangeAccount 最多属于一个启用的 LogicalAccount。
- StrategyRunner 可以没有 LogicalAccount，此时为观察模式；一个 StrategyRunner 只控制一个 LogicalAccount，一个 Strategy 可创建多个 Runner。
- LogicalAccount 成员必须同为 paper 或 live、同为 SPOT 或 SWAP、同一 settlement asset；允许来自不同 Exchange，自动执行时动态择优。
- LogicalAccount 处于 `PAUSED` 时继续维护物理账户 session、私有流和同步，只禁止新的自动 TARGET 子订单。
- 人工 RPC 必须先暂停整个 LogicalAccount。人工下单和一键清仓分别以持久化 `OperatorAction` 执行，调用者不能传入订单 source、owner 或 Runner 标识。
- `TargetIntent` 是 LogicalAccount 的 FULL 快照：遗漏 instrument 表示目标为零，空列表表示全部归零。
- Strategy artifact 由唯一 `strategy_id` 标识并不可变；源码、manifest、输入契约或参数 schema 发生变化必须使用新的 `strategy_id`。
- V1 Strategy 只支持完整历史窗口计算，不支持 `state_json`、`state_revision`、`next_state`、`state_format_version` 或增量状态迁移。

## 最终目录和数据结构

### Strategy 四表

```text
t_strategies
t_strategy_runners
t_strategy_results
t_strategy_outbox
```

### Trade 核心表

```text
t_exchange_accounts
t_logical_accounts
t_logical_account_members
t_trade_orders
t_trade_fills
t_exchange_positions
t_exchange_account_snapshots
t_logical_account_targets
t_operator_actions
```

删除 Trade 的三张账本表；订单的 `reserved_quantity`、`reserved_notional` 继续作为未完成委托的资金占用事实。

### 关键进程边界

```text
Strategy Engine
  -> StrategyResult + Runner current target + Outbox（同一事务）
  -> TradeTargetRequested（subject = logical_account_id）
  -> Trade LogicalAccountTarget（每个 LogicalAccount 一行）
  -> TargetWorker（串行、每轮最多一个动作）
  -> OrderService
  -> Exchange Adapter

Operator RPC
  -> Pause LogicalAccount
  -> OperatorAction
  -> OperatorWorker
  -> OrderService / AccountSync
```

## 提交和验证规则

- 每个任务先写失败测试，再写最小实现，再运行任务内命令。
- 生成的 `*.pb.go`、`*.trpc.go` 只通过 Makefile 生成，禁止手改。
- 每个任务完成并验证后单独提交；不要把无关工作树改动带入提交。
- 下文命令均从仓库根目录 `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox` 执行，除非命令显式 `cd`。

---

## 任务 1：先修复独立的订单正确性问题

这一批不依赖 LogicalAccount，可先降低后续改造的变量数量。

**文件：**

- 修改：`modules/trade/internal/domain/order/state.go`
- 修改：`modules/trade/internal/domain/order/spec.go`
- 修改：`modules/trade/internal/application/order/service.go`
- 修改：`modules/trade/internal/application/order/service_test.go`
- 修改：`modules/trade/internal/application/order/validator.go`
- 修改：`modules/trade/internal/application/order/validator_test.go`
- 修改：`modules/trade/internal/application/accountsync/service.go`
- 修改：`modules/trade/internal/application/accountsync/service_test.go`
- 修改：`modules/trade/internal/exchange/okx/okx.go`
- 修改：`modules/trade/internal/exchange/okx/okx_test.go`
- 修改：`modules/trade/internal/exchange/paper/paper.go`
- 修改：`modules/trade/internal/exchange/paper/paper_test.go`
- 修改：`modules/trade/go.mod`
- 修改：`modules/trade/go.sum`

- [ ] **1.1 写 OrderService 状态感知幂等测试**

覆盖：

```text
TestSubmitPendingSubmitsOnce
TestSubmitSubmittingQueriesExchangeBeforeRetry
TestSubmitUnknownQueriesExchangeBeforeRetry
TestSubmitUnknownResetsToPendingOnlyAfterConfirmedAbsentAndWindowExpired
TestSubmitOpenPartialOrTerminalReturnsStoredOrder
TestSubmitRejectedReturnsStableRejection
TestSubmitSameClientFieldsIgnoresServerReferencePriceAndTime
TestSubmitSameClientIDWithDifferentClientFieldsConflicts
```

幂等比较只包含调用者可控制字段：

```go
type ClientOrderSpec struct {
    ExchangeAccountID string
    ClientOrderID      string
    InstrumentID      string
    Side              Side
    PositionSide      PositionSide
    Type              Type
    FillPolicy        FillPolicy
    Quantity          decimal.Decimal
    LimitPrice        *decimal.Decimal
}
```

`FillPolicy` 只用于 LIMIT，取值为 `GTC|IOC|FOK`；MARKET 必须为 unspecified。公开 RPC 和 `ClientOrderSpec` 不暴露 `ReducePositionOnly`。OrderService 根据 SPOT/SWAP、当前已确认仓位和 Target/Operator 执行阶段生成有效订单：

```go
type OrderSpec struct {
    ClientOrderSpec
    ReducePositionOnly bool
    Owner              OrderOwner
}
```

规则固定为：SPOT、SWAP 开仓或加仓为 false；SWAP 减仓、关闭反向仓位和 Flatten 为 true。人工订单可能穿过零点时直接拒绝，要求先减仓或 Flatten，确认归零后再开反向仓位。

服务入口状态分流必须是：

```text
PENDING                         -> Submit
SUBMITTING / SUBMIT_UNKNOWN     -> QueryByClientOrderID
OPEN / PARTIALLY_FILLED / 终态   -> 返回已有订单
REJECTED                        -> 返回已有订单和稳定拒绝原因
```

只有 Exchange 明确返回“不存在”且超过不确定窗口时，才允许将同一订单恢复为 `PENDING`，并使用相同 client order ID 再提交。

- [ ] **1.2 运行 RED 测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/order -run 'TestSubmit(Pending|Submitting|Unknown|Open|Rejected|SameClientID|SameClientFields)'
```

预期：新增状态分流和客户端字段幂等测试失败。

- [ ] **1.3 实现状态感知入口和单调状态合并**

REST 成功回包为 `OPEN` 以外状态时，持久化响应后立即调用 `SyncAccount`；不要根据没有 Fill ID 的聚合响应直接记资金结算。Reducer 必须拒绝终态倒退、累计成交量减少和旧 exchange update time 覆盖新事实。

- [ ] **1.4 写并实现 OKX client order ID 约束**

使用 `github.com/rs/xid`，在订单首次创建时生成并持久化：

```go
clientOrderID := xid.New().String()
```

`xid.New().String()` 固定为 20 位小写字母数字。unknown 查询和受控重试复用数据库中的原值，禁止重新生成。OKX Adapter 在发送前仍做长度和字母数字 fail-closed 校验。覆盖：

```text
TestOKXRejectsClientOrderIDWithDash
TestOKXRejectsClientOrderIDLongerThan32
TestOKXAcceptsGeneratedClientOrderID
```

- [ ] **1.5 写并实现成交策略、只减仓保护和 Paper 约束**

Validator 必须在创建订单和 reservation 前拿到 Account 类型并拒绝 Paper LIMIT：

```text
TestValidatorRejectsPaperLimitBeforeReservation
TestValidatorAcceptsPaperMarket
TestPaperAdapterRejectsLimitAsDefenseInDepth
TestValidatorRequiresFillPolicyForLimit
TestValidatorRejectsFillPolicyForMarket
TestExchangeAdaptersMapFillPolicy
TestOrderServiceDerivesReducePositionOnlyForSwapReduction
TestOrderServiceLeavesReducePositionOnlyFalseForSwapIncrease
TestOrderServiceRejectsManualOrderThatWouldCrossZero
TestOrderServiceNeverSetsReducePositionOnlyForSpot
```

- [ ] **1.6 运行 GREEN 和竞态测试**

```bash
cd modules/trade
go test -count=1 ./internal/domain/order ./internal/application/order ./internal/application/accountsync ./internal/exchange/okx ./internal/exchange/paper
go test -race -count=1 ./internal/application/order ./internal/application/accountsync
```

预期：全部通过，`Submit` 不会对已知订单盲目重发，Paper LIMIT 在落库前被拒绝。

- [ ] **1.7 提交**

```bash
git add modules/trade/internal/domain/order \
  modules/trade/internal/application/order \
  modules/trade/internal/application/accountsync \
  modules/trade/internal/exchange/okx \
  modules/trade/internal/exchange/paper \
  modules/trade/go.mod modules/trade/go.sum
git commit -m "fix(trade): make order submission state aware"
```

---

## 任务 2：将 Strategy 持久化收敛为四表

**文件：**

- 修改：`modules/strategy/schema/strategy.sql`
- 修改：`modules/strategy/schema/schema_test.go`
- 修改：`modules/strategy/internal/store/database.go`
- 修改：`modules/strategy/internal/store/database_test.go`
- 修改：`modules/strategy/internal/store/store.go`
- 修改：`modules/strategy/internal/domain/types.go`
- 修改：`modules/strategy/internal/domain/types_test.go`
- 修改：`modules/strategy/internal/domain/frontend.go`
- 删除：`modules/strategy/internal/store/definitions.go`
- 删除：`modules/strategy/internal/store/bindings.go`
- 删除：`modules/strategy/internal/store/frontend_store.go`
- 新增：`modules/strategy/internal/store/strategies.go`
- 新增：`modules/strategy/internal/store/strategies_test.go`
- 新增：`modules/strategy/internal/store/runners.go`
- 新增：`modules/strategy/internal/store/runners_test.go`
- 新增：`modules/strategy/internal/store/results.go`
- 新增：`modules/strategy/internal/store/results_test.go`
- 新增：`modules/strategy/internal/store/runner_queries.go`
- 新增：`modules/strategy/internal/store/runner_queries_test.go`

- [ ] **2.1 先写严格 schema 测试**

```text
TestAllSQLCreatesExactlyStrategyTables
TestStrategySchemaUsesRunnerAndResultColumns
TestStrategySchemaHasNoStateOrDataRevisionColumns
TestOpenRejectsObsoleteStrategySchema
TestOpenAcceptsCurrentSchemaOnReopen
```

精确断言只存在：

```go
[]string{
    "t_strategies",
    "t_strategy_runners",
    "t_strategy_results",
    "t_strategy_outbox",
}
```

- [ ] **2.2 运行 RED 测试**

```bash
cd modules/strategy
go test -count=1 ./schema ./internal/store -run 'Test(AllSQLCreatesExactlyStrategyTables|StrategySchemaUsesRunnerAndResultColumns|StrategySchemaHasNoStateOrDataRevisionColumns|OpenRejectsObsoleteStrategySchema|OpenAcceptsCurrentSchemaOnReopen)$'
```

预期：当前 12 表 schema 和旧列断言失败。

- [ ] **2.3 用四表替换 Strategy schema**

核心字段固定如下：

```sql
CREATE TABLE t_strategies (
    strategy_id   TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    manifest_yaml TEXT NOT NULL,
    source_code   TEXT NOT NULL,
    source_hash   TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE t_strategy_runners (
    runner_id             TEXT PRIMARY KEY,
    strategy_id           TEXT NOT NULL,
    space_id              TEXT NOT NULL,
    view_id               TEXT NOT NULL,
    frequency             TEXT NOT NULL,
    params_json           TEXT NOT NULL,
    logical_account_id    TEXT,
    status                TEXT NOT NULL,
    current_targets_json  TEXT NOT NULL,
    command_sequence      INTEGER NOT NULL,
    last_result_id        TEXT,
    last_success_at       INTEGER,
    last_error            TEXT,
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_strategy_runners_enabled_logical_account
ON t_strategy_runners(logical_account_id)
WHERE logical_account_id IS NOT NULL AND status = 'ENABLED';

CREATE TABLE t_strategy_results (
    result_id          TEXT PRIMARY KEY,
    runner_id          TEXT NOT NULL,
    strategy_id        TEXT NOT NULL,
    trigger_bar_time   INTEGER NOT NULL,
    namespace          TEXT NOT NULL,
    input_hash         TEXT NOT NULL,
    action             TEXT NOT NULL,
    output_json        TEXT NOT NULL,
    command_sequence   INTEGER,
    created_at         INTEGER NOT NULL,
    UNIQUE(runner_id, strategy_id, namespace, trigger_bar_time)
);

CREATE TABLE t_strategy_outbox (
    message_id  TEXT PRIMARY KEY,
    event_data  BLOB NOT NULL,
    created_at  INTEGER NOT NULL
);
```

`t_strategy_outbox` 保持当前轻量 relay 语义，但外键和 payload identity 改为 Result/Runner。

- [ ] **2.4 定义最终领域类型**

```go
type Strategy struct {
    ID           string
    Name         string
    ManifestYAML string
    SourceCode   string
    SourceHash   string
    CreatedAt    time.Time
}

type StrategyRunner struct {
    ID                 string
    StrategyID         string
    LogicalAccountID   *string
    Status             RunnerStatus
    CurrentTargetsJSON json.RawMessage
    CommandSequence    int64
    LastResultID       *string
    LastSuccessAt      *time.Time
    LastError          *string
}

type StrategyResult struct {
    ID              string
    RunnerID        string
    StrategyID      string
    TriggerBarTime  time.Time
    Namespace       string
    InputHash       string
    Action          Action
    OutputJSON      json.RawMessage
    CommandSequence *int64
}
```

删除 Definition、Binding、ExecutionBinding、StrategyRun、PerformancePoint、BindingHealth 和 OperationAudit 类型。

- [ ] **2.5 实现严格启动检查**

SQLite 已存在任一旧表时返回包含表名和“删除旧数据库后重建”的错误；不要自动 `DROP`，不要静默忽略。

- [ ] **2.6 运行 GREEN 测试**

```bash
cd modules/strategy
go test -count=1 ./schema ./internal/domain ./internal/store
```

预期：仅四表，旧 schema 被明确拒绝，空数据库和当前 schema 可重复打开。

- [ ] **2.7 提交**

```bash
git add modules/strategy/schema modules/strategy/internal/domain modules/strategy/internal/store
git commit -m "refactor(strategy): reduce persistence to four tables"
```

---

## 任务 3：实现不可变 Strategy、manifest 和 Runner 生命周期

**文件：**

- 修改：`modules/strategy/internal/registry/service.go`
- 修改：`modules/strategy/internal/registry/service_test.go`
- 修改：`modules/strategy/internal/engine/engine.go`
- 修改：`modules/strategy/internal/engine/engine_test.go`
- 修改：`modules/strategy/internal/store/strategies.go`
- 修改：`modules/strategy/internal/store/runners.go`
- 修改：`modules/strategy/internal/store/runners_test.go`
- 修改：`modules/strategy/strategies/example/strategy.yaml`
- 修改：`modules/strategy/cmd/cli/main.go`
- 修改：`modules/strategy/cmd/cli/main_test.go`

- [ ] **3.1 写 manifest 和不可变 artifact 测试**

```text
TestParseManifestRequiresAPIVersionEntrypointAndInputWindow
TestParseManifestRejectsStateFields
TestParseManifestRejectsUnsupportedAPIVersion
TestSaveStrategyRejectsChangedArtifactWithSameID
TestSaveStrategyAcceptsChangedSourceWithNewID
TestEngineLoadsStrategyByArtifactID
```

manifest 最小结构：

```yaml
api_version: moox.strategy/v1
entrypoint: strategy.py:run
input:
  history_bars: 200
```

数据库不复制 `api_version`。V1 Registry 只接受 `moox.strategy/v1`，manifest 拒绝 `state_format_version`、state schema 和增量状态配置。

- [ ] **3.2 写 Runner 生命周期测试**

```text
TestCreateRunnerInitializesTargetSequenceAndHealth
TestUpdateRunnerRequiresDisabledStatus
TestSwitchRunnerStrategyPreservesTargetAndSequence
TestSwitchRunnerStrategyClearsArtifactSpecificHealth
TestEnableRunnerRejectsLogicalAccountOwnedByAnotherRunner
TestObserveOnlyRunnerHasNoLogicalAccount
```

新 Runner 初值必须是：

```text
current_targets_json  = []
command_sequence      = 0
last_result_id        = NULL
last_success_at       = NULL
last_error            = NULL
```

Runner disabled 时可以切换 Strategy；保留 current targets 和 command sequence，避免 Strategy 与 Trade 当前目标错位，同时清空旧 artifact 的 last result/health。

- [ ] **3.3 运行 RED 测试**

```bash
cd modules/strategy
go test -count=1 ./internal/registry ./internal/engine ./internal/store ./cmd/cli -run 'Test(ParseManifest|SaveStrategy|EngineLoadsStrategy|CreateRunner|UpdateRunner|SwitchRunner|EnableRunner|ObserveOnlyRunner)'
```

- [ ] **3.4 实现 Registry 和 Runner**

使用解析后的 manifest 做执行校验，不在表里存投影列。Engine cache key 只使用不可变 `strategy_id`。Python entrypoint 固定接收 `context, data, params`，不存在第四个 state 参数。

- [ ] **3.5 运行 GREEN 测试**

```bash
cd modules/strategy
go test -count=1 ./internal/registry ./internal/engine ./internal/store ./cmd/cli
```

- [ ] **3.6 提交**

```bash
git add modules/strategy/internal/registry modules/strategy/internal/engine \
  modules/strategy/internal/store modules/strategy/strategies/example \
  modules/strategy/cmd/cli
git commit -m "feat(strategy): add immutable strategies and runners"
```

---

## 任务 4：统一无状态 Strategy、完整历史窗口和 FULL 输出契约

**文件：**

- 修改：`modules/strategy/internal/engine/engine.go`
- 修改：`modules/strategy/internal/engine/engine_test.go`
- 修改：`modules/strategy/pysdk/moox_strategy/types.py`
- 修改：`modules/strategy/pysdk/moox_strategy/validate.py`
- 修改：`modules/strategy/pysdk/tests/test_validate.py`
- 修改：`modules/strategy/pyworker/worker.py`
- 修改：`modules/strategy/pyworker/test_worker.py`
- 修改：`modules/strategy/strategies/example/strategy.py`

- [ ] **4.1 先将三层测试改为允许空 rebalance**

```text
Go:     TestValidateOutputAcceptsEmptyRebalance
Go:     TestRunDoesNotExposePreviousTargetsOrState
Go:     TestRunHashesCompleteHistoryParamsAndTriggerContext
Go:     TestValidateOutputRejectsNextState
Python: test_accepts_empty_rebalance
Python: test_context_has_no_previous_targets_or_state
Python: test_rejects_symbol_and_native_account_fields
Python: test_rejects_next_state
Worker: test_accepts_empty_rebalance
Worker: test_passes_complete_history_without_previous_targets
Worker: test_rejects_four_argument_stateful_entrypoint
```

Strategy 输出目标只允许：

```json
{
  "action": "rebalance",
  "targets": [
    {
      "instrument_id": "BTC-USDT-SPOT",
      "target_quantity": "0.25"
    }
  ],
  "debug_info": {
    "fast": 12,
    "slow": 48
  }
}
```

禁止输出 `symbol`、Exchange 原生 symbol、账户 ID、source、owner、`state` 或 `next_state`。`hold` 保留 Runner 当前 target；`rebalance` 用本次完整列表替换，空列表表示全平。

- [ ] **4.2 运行 RED 测试**

```bash
cd modules/strategy
go test -count=1 ./internal/engine -run 'Test(ValidateOutputAcceptsEmptyRebalance|RunDoesNotExposePreviousTargetsOrState|RunHashesCompleteHistoryParamsAndTriggerContext|ValidateOutputRejectsNextState)$'
python3 -m unittest discover -s pysdk/tests -p 'test_*.py'
python3 -m unittest pyworker/test_worker.py
```

- [ ] **4.3 实现全快照输入输出**

每次调用 Python 传入完整历史窗口：

```json
{
  "context": {
    "strategy_id": "strategy-1",
    "runner_id": "runner-1",
    "trigger_bar_time": "2026-07-29T10:00:00Z"
  },
  "params": {
    "fast": 12,
    "slow": 48
  },
  "data": [
    {"time": "2026-07-29T09:59:00Z", "close": "68352.42"}
  ]
}
```

“完整历史窗口”指 manifest 声明、以 `trigger_bar_time` 结束、时间顺序稳定的完整计算窗口，不要求加载全部市场历史。Engine 不维护增量 Strategy 状态，也不把 `current_targets_json` 作为 Python 输入。均线、冷却期、连续信号和滚动指标必须从该窗口计算。`input_hash` 覆盖 immutable strategy identity、完整 data、params、namespace 和 trigger bar time，排除 result ID、run time 等重试会变化的字段；不接收调用者提供的 `data_revision`。accepted Result 的 `output_json` 只保存 `targets` 和可选、受大小限制的 `debug_info`。

- [ ] **4.4 运行 GREEN 测试**

```bash
cd modules/strategy
go test -count=1 ./internal/engine
python3 -m unittest discover -s pysdk/tests -p 'test_*.py'
python3 -m unittest pyworker/test_worker.py
```

- [ ] **4.5 提交**

```bash
git add modules/strategy/internal/engine modules/strategy/pysdk \
  modules/strategy/pyworker modules/strategy/strategies/example/strategy.py
git commit -m "feat(strategy): define stateless full target contract"
```

---

## 任务 5：新增 Trade LogicalAccount、LogicalAccountTarget 和 OperatorAction 持久化

**文件：**

- 新增：`modules/trade/schema/logical_account.sql`
- 修改：`modules/trade/schema/account.sql`
- 修改：`modules/trade/schema/execution.sql`
- 删除：`modules/trade/schema/ledger.sql`
- 修改：`modules/trade/schema/schema.go`
- 修改：`modules/trade/schema/schema_test.go`
- 新增：`modules/trade/internal/domain/logicalaccount/account.go`
- 新增：`modules/trade/internal/domain/logicalaccount/account_test.go`
- 新增：`modules/trade/internal/domain/operator/action.go`
- 新增：`modules/trade/internal/domain/operator/action_test.go`
- 修改：`modules/trade/internal/domain/exchangeaccount/account.go`
- 修改：`modules/trade/internal/domain/exchangeaccount/account_test.go`
- 新增：`modules/trade/internal/infra/store/logical_account.go`
- 新增：`modules/trade/internal/infra/store/logical_account_test.go`
- 新增：`modules/trade/internal/infra/store/operator_action.go`
- 新增：`modules/trade/internal/infra/store/operator_action_test.go`
- 新增：`modules/trade/internal/infra/store/reservation.go`
- 新增：`modules/trade/internal/infra/store/reservation_test.go`
- 修改：`modules/trade/internal/infra/store/target.go`
- 修改：`modules/trade/internal/infra/store/target_test.go`
- 修改：`modules/trade/internal/infra/store/store.go`
- 修改：`modules/trade/internal/infra/store/store_test.go`
- 删除：`modules/trade/internal/infra/store/execution.go`
- 删除：`modules/trade/internal/infra/store/execution_test.go`

- [ ] **5.1 写新 schema 和旧 schema 拒绝测试**

```text
TestAllSQLCreatesLogicalAccountTargetAndOperatorTablesWithoutLedger
TestOpenRejectsSingleAccountTargetAndLedgerSchema
TestLogicalAccountMembershipEnforcesOneEnabledMembershipPerPhysicalAccount
TestAcceptLogicalAccountTargetUsesOwnerAndKeepsMaximumSequence
TestAcceptLogicalAccountTargetAllowsEmptyFullWhilePaused
TestAcceptLogicalAccountTargetRejectsMismatchedRunner
TestLogicalAccountTargetCASCannotOverwriteNewSequence
```

- [ ] **5.2 运行 RED 测试**

```bash
cd modules/trade
go test -count=1 ./schema ./internal/domain/logicalaccount ./internal/domain/operator ./internal/infra/store
```

预期：新 package 尚不存在或断言失败。

- [ ] **5.3 建立 LogicalAccount 和成员表**

核心字段：

```sql
CREATE TABLE t_logical_accounts (
    space_id           TEXT NOT NULL,
    logical_account_id TEXT NOT NULL,
    name               TEXT NOT NULL,
    owner_runner_id    TEXT,
    execution_mode     TEXT NOT NULL,
    market_type        TEXT NOT NULL,
    settlement_asset   TEXT NOT NULL,
    control_state      TEXT NOT NULL,
    control_revision   INTEGER NOT NULL,
    pause_reason       TEXT,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    PRIMARY KEY(space_id, logical_account_id)
);

CREATE TABLE t_logical_account_members (
    space_id            TEXT NOT NULL,
    logical_account_id  TEXT NOT NULL,
    exchange_account_id TEXT NOT NULL,
    enabled             INTEGER NOT NULL,
    priority            INTEGER NOT NULL,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    PRIMARY KEY(space_id, logical_account_id, exchange_account_id)
);

CREATE UNIQUE INDEX ux_enabled_physical_account_membership
ON t_logical_account_members(space_id, exchange_account_id)
WHERE enabled = 1;
```

`execution_mode` 只允许 `PAPER|LIVE`；`control_state` 只允许 `ACTIVE|PAUSED|FLATTENING|DISABLED`。ExchangeAccount 另存静态 `PAPER|TESTNET|PRODUCTION` 环境 profile。成员的 execution mode、环境 profile、market type 和 settlement asset 必须同质；`priority` 只是稳定选择顺序，实时容量从账户快照和 Exchange 约束计算，不存分配权重。

- [ ] **5.4 将 TargetExecution 改为单行 LogicalAccountTarget**

```sql
CREATE TABLE t_logical_account_targets (
    space_id            TEXT NOT NULL,
    logical_account_id  TEXT NOT NULL,
    target_id           TEXT NOT NULL,
    runner_id           TEXT NOT NULL,
    command_sequence    INTEGER NOT NULL,
    targets_json        TEXT NOT NULL,
    status              TEXT NOT NULL,
    blocked_targets_json TEXT NOT NULL,
    last_error          TEXT,
    accepted_at         INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    PRIMARY KEY(space_id, logical_account_id),
    UNIQUE(space_id, target_id)
);
```

`status` 只允许 `PENDING|CONVERGING|CONVERGED|BLOCKED`。`blocked_targets_json` 初始为 `[]`，只记录因最小下单量、最小名义金额或全部成员容量不足而无法继续执行的目标差额和原因。执行进度从 targets、positions、orders 和 snapshots 重新计算，不持久化 `progress_json`。

新 sequence 原子替换旧 FULL target；Strategy 发布时令 `target_id = result_id`，Trade 不复制 StrategyResult 内容。相同 target ID/sequence 和相同 payload 返回已有目标；旧 sequence 拒绝；PAUSED 时仍接收和保存最新目标，但不执行。

- [ ] **5.5 建立 OperatorAction**

```sql
CREATE TABLE t_operator_actions (
    space_id            TEXT NOT NULL,
    action_id           TEXT NOT NULL,
    logical_account_id  TEXT NOT NULL,
    action_type         TEXT NOT NULL,
    reason              TEXT NOT NULL,
    request_json        TEXT NOT NULL,
    status              TEXT NOT NULL,
    result_json         TEXT,
    last_error          TEXT,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    PRIMARY KEY(space_id, action_id)
);
```

`action_type` 只允许 `MANUAL_ORDER|FLATTEN`；`status` 只允许 `RUNNING|COMPLETED|PARTIAL|FAILED`。同 `action_id` 相同请求稳定返回，不同请求冲突。

- [ ] **5.6 删除物理 PauseAccount 和账本**

全链删除 ExchangeAccount 的 `paused`、`pause_reason` 和 SetPause 语义。PAUSED LogicalAccount 不能让 Manager 停掉物理 session。

删除 ledger schema、projection 和 `PostLedger`；将 `GetUnreflectedReservation` 的订单 reservation 查询迁入 `reservation.go`，保持订单创建、成交、撤单后的资金占用测试。

- [ ] **5.7 运行 GREEN 测试**

```bash
cd modules/trade
go test -count=1 ./schema ./internal/domain/exchangeaccount \
  ./internal/domain/logicalaccount ./internal/domain/operator ./internal/infra/store
```

- [ ] **5.8 提交**

```bash
git add modules/trade/schema modules/trade/internal/domain \
  modules/trade/internal/infra/store
git commit -m "feat(trade): add logical account persistence"
```

---

## 任务 6：一次性切换 StrategyResult 到 Trade TargetIntent 的公共契约

该任务必须在一个提交中同时修改事件生产者、公共校验和消费者，避免某一层仍拒绝空 FULL。

**文件：**

- 修改：`packages/tradeeventpb/trade_events.proto`
- 生成：`packages/tradeeventpb/trade_events.pb.go`
- 修改：`packages/events/validation.go`
- 修改：`packages/events/validation_test.go`
- 修改：`packages/events/events_test.go`
- 修改：`modules/strategy/internal/store/results.go`
- 修改：`modules/strategy/internal/store/results_test.go`
- 修改：`modules/strategy/internal/action/service.go`
- 修改：`modules/strategy/internal/action/service_test.go`
- 修改：`modules/strategy/internal/outbox/publisher.go`
- 修改：`modules/strategy/internal/outbox/publisher_test.go`
- 修改：`modules/strategy/internal/rpc/service.go`
- 修改：`modules/strategy/internal/rpc/service_test.go`
- 修改：`modules/trade/internal/domain/execution/target.go`
- 修改：`modules/trade/internal/domain/execution/target_test.go`
- 修改：`modules/trade/internal/eventconsumer/target.go`
- 修改：`modules/trade/internal/eventconsumer/target_test.go`
- 修改：`modules/trade/internal/infra/store/target.go`
- 修改：`modules/trade/internal/infra/store/target_test.go`
- 修改：`scripts/verify-event-contracts.sh`
- 修改：`scripts/test-strategy-trade-event-e2e.sh`
- 修改：`modules/strategy/test/strategy_trade_external_e2e_test.go`
- 修改：`modules/trade/test/strategy_target_external_e2e_test.go`

- [ ] **6.1 写公共事件契约测试**

```text
TestTradeTargetRequestedRequiresTargetRunnerAndLogicalAccount
TestTradeTargetRequestedAcceptsEmptyFullTarget
TestTradeTargetRequestedRejectsDuplicateInstrumentID
TestTradeTargetRequestedSubjectUsesLogicalAccountID
TestTargetIntentValidationAcceptsEmptyFullTarget
TestHandleTargetMapsTargetIdentity
TestAcceptLogicalAccountTargetPersistsTargetID
```

- [ ] **6.2 将 Proto 改为唯一公共形状**

```proto
message TradeTarget {
  string instrument_id = 1;
  string target_quantity = 2;
}

message TradeTargetRequested {
  string target_id = 1;
  string runner_id = 2;
  string logical_account_id = 3;
  int64 command_sequence = 4;
  repeated TradeTarget targets = 5;
}
```

Strategy 发布时令 `target_id = result_id`。删除 `strategy_run_id`、`strategy_result_id`、`execution_id`、`execution_binding_id`、`exchange_account_id`、`data_revision`、`not_after` 和 `symbol`。事件 `SubjectID` 使用 `logical_account_id`；目标按 `instrument_id` 唯一，空 targets 合法。

- [ ] **6.3 运行事件 RED 测试并生成 Proto**

```bash
cd packages/events
go test -count=1 ./...
cd ../..
make -C packages/tradeeventpb all
```

预期：生成后，旧 Strategy/Trade 字段引用导致编译失败，证明 blast radius 已暴露。

- [ ] **6.4 实现 StrategyResult 原子提交**

同一 SQLite 事务完成：

```text
1. 以 runner_id + strategy_id + namespace + trigger_bar_time 做逻辑重试判定
2. 使用完整历史窗口、params、trigger context 和 Strategy identity 的 input_hash 判断相同或冲突重试
3. 写入 accepted StrategyResult；output_json 只有 targets/debug_info
4. hold 保留 current target 和 sequence
5. rebalance 替换 current target 并原子递增 command sequence
6. 有 LogicalAccount 的 rebalance 以 target_id=result_id 写 outbox；观察模式不写
7. 更新 Runner 的 last_result_id、last_success_at 和 last_error
```

测试：

```text
TestCommitResultHoldPreservesCurrentTargetsAndSequence
TestCommitResultObserveOnlyRebalanceUpdatesTargetWithoutOutbox
TestCommitResultExecutingRebalanceAtomicallyAdvancesRunnerAndWritesOutbox
TestCommitResultEmptyRebalanceWritesEmptyFullTarget
TestCommitResultCommandSequenceIsMonotonicPerRunner
TestCommitResultLogicalRetrySameHashReturnsExistingResult
TestCommitResultLogicalRetryDifferentHashConflicts
TestCommitResultConcurrentSequenceIsMonotonic
TestCommitResultTransactionFailureRollsBackResultTargetSequenceAndOutbox
TestFailedAttemptUpdatesRunnerErrorWithoutCreatingResult
```

- [ ] **6.5 实现 Trade 消费者**

消费者用 `logical_account_id` 串行加锁并调用 `AcceptLogicalAccountTarget`。验证事件 runner 等于 LogicalAccount 当前 `owner_runner_id`，并验证每个 `instrument_id` 至少有一个成员可解析；不匹配或永久不支持时拒绝并记录可见错误。PAUSED 时仍以更高 sequence 更新 LogicalAccountTarget，但不创建订单。

- [ ] **6.6 加入旧字段静态护栏**

`scripts/verify-event-contracts.sh` 在生产 Target 链禁止：

```text
strategy_run_id
strategy_result_id
execution_id
execution_binding_id
exchange_account_id
data_revision
not_after
symbol
```

护栏只扫描 Strategy Target publisher、公共 event 和 Trade Target consumer/domain，避免误伤 Order/Exchange 正常字段。

- [ ] **6.7 运行 GREEN、竞态和跨模块测试**

```bash
(cd packages/events && go test -count=1 ./...)
(cd modules/strategy && go test -count=1 ./internal/store ./internal/action ./internal/outbox ./internal/rpc)
(cd modules/strategy && go test -race -count=1 ./internal/store ./internal/action ./internal/outbox)
(cd modules/trade && go test -count=1 ./internal/domain/execution ./internal/eventconsumer ./internal/infra/store)
bash scripts/verify-event-contracts.sh
bash scripts/test-strategy-trade-event-e2e.sh
```

- [ ] **6.8 提交**

```bash
git add packages/tradeeventpb packages/events \
  modules/strategy/internal/store modules/strategy/internal/action \
  modules/strategy/internal/outbox modules/strategy/internal/rpc \
  modules/strategy/test/strategy_trade_external_e2e_test.go \
  modules/trade/internal/domain/execution modules/trade/internal/eventconsumer \
  modules/trade/internal/infra/store/target.go \
  modules/trade/internal/infra/store/target_test.go \
  modules/trade/test/strategy_target_external_e2e_test.go \
  scripts/verify-event-contracts.sh scripts/test-strategy-trade-event-e2e.sh
git commit -m "feat(trade): route full targets through logical accounts"
```

---

## 任务 7：实现 LogicalAccount 服务、所有权和就绪度

**文件：**

- 新增：`modules/trade/internal/application/logicalaccount/service.go`
- 新增：`modules/trade/internal/application/logicalaccount/service_test.go`
- 修改：`modules/trade/internal/application/account/service.go`
- 修改：`modules/trade/internal/application/account/service_test.go`
- 修改：`modules/trade/internal/application/account/repository.go`
- 修改：`modules/trade/internal/application/account/repository_test.go`
- 修改：`modules/trade/internal/runtime/manager.go`
- 修改：`modules/trade/internal/runtime/manager_test.go`
- 修改：`modules/trade/internal/health/state.go`
- 修改：`modules/trade/internal/health/server_test.go`

- [ ] **7.1 写领域服务测试**

```text
TestLogicalAccountRejectsHeterogeneousMembers
TestLogicalAccountMembershipChangeRequiresPausedOrDisabled
TestRemoveMemberRejectsActiveOrdersOrPositions
TestAddMemberRequiresAdoptionForExistingExposure
TestLogicalAccountOwnerRunnerIsExclusive
TestLogicalReadinessRequiresEveryEnabledMember
TestPausedLogicalAccountKeepsPhysicalSessionsRunning
TestResumeRequiresReadyNoConflictAndWarnsAboutReopen
```

- [ ] **7.2 运行 RED 测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/logicalaccount ./internal/runtime ./internal/health
```

- [ ] **7.3 实现同质性、成员变更和 owner 约束**

LogicalAccount 只有在 PAUSED 或 DISABLED 时允许增删成员。加入已有订单/持仓/非 settlement SPOT 余额的账户必须显式 adoption；移除仍有活动订单或 exposure 的成员必须拒绝。

控制状态：

```go
const (
    ControlActive   ControlState = "ACTIVE"
    ControlPaused   ControlState = "PAUSED"
    ControlFlattening ControlState = "FLATTENING"
    ControlDisabled ControlState = "DISABLED"
)
```

- [ ] **7.4 实现聚合 readiness**

所有 enabled member 都必须满足：

```text
session running
private stream ready
initial sync completed
account snapshot fresh
instrument metadata available
no unresolved submit
```

此外，当前 LogicalAccountTarget 的每个 `instrument_id` 都必须至少有一个 Ready member 可执行。任一成员 Not Ready 或目标无临时候选时，LogicalAccount readiness 为 false，自动执行门禁关闭并写明 member/instrument/reason；不要自动修改 operator control state，不要删除 session，不要引入 supervisor。

`ResumeLogicalAccount` 只有在没有 RUNNING OperatorAction、没有未解决 EXTERNAL 冲突、全部 enabled member Ready 且当前 targets 可解析时成功。恢复后从最新 LogicalAccountTarget 重新收敛，响应必须明确提示人工清仓后的旧 target 可能重新开仓。

- [ ] **7.5 修复 Manager 首次失败**

首次 reconcile 失败固定间隔重试；至少一次成功前 readiness 为 false。manager worker 意外退出时 health 显式不就绪，禁止 `0 enabled == 0 ready` 被判断为成功。

- [ ] **7.6 运行 GREEN 测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/logicalaccount \
  ./internal/application/account ./internal/runtime ./internal/health
go test -race -count=1 ./internal/application/logicalaccount ./internal/runtime
```

- [ ] **7.7 提交**

```bash
git add modules/trade/internal/application/logicalaccount \
  modules/trade/internal/application/account \
  modules/trade/internal/runtime/manager.go \
  modules/trade/internal/runtime/manager_test.go \
  modules/trade/internal/health
git commit -m "feat(trade): manage logical account ownership and readiness"
```

---

## 任务 8：实现串行 TargetExecutor、TargetWorker 和动态择优

**文件：**

- 重写：`modules/trade/internal/application/target/executor.go`
- 重写：`modules/trade/internal/application/target/executor_test.go`
- 新增：`modules/trade/internal/application/target/lanes.go`
- 新增：`modules/trade/internal/application/target/lanes_test.go`
- 重写：`modules/trade/internal/runtime/target_worker.go`
- 重写：`modules/trade/internal/runtime/target_worker_test.go`
- 修改：`modules/trade/internal/application/target/price.go`
- 删除：`modules/trade/internal/application/target/submission.go`
- 修改：`modules/trade/internal/infra/store/fact.go`
- 修改：`modules/trade/internal/infra/store/fact_test.go`

- [ ] **8.1 写 FULL union 和排序测试**

```text
TestTargetExecutorAggregatesAcrossExchanges
TestTargetExecutorClosesOpposingBeforeOpening
TestTargetExecutorZeroTargetClosesEveryMemberWithoutCrossAccountNetting
TestTargetExecutorReductionSelectsLargestPosition
TestTargetExecutorIncreaseFallsThroughPriorityOnCapacity
TestTargetExecutorSubmitsOnlyOneChildPerPassAndRestoresAfterRestart
TestTargetExecutorFullOmissionClosesPositionAndOwnedOrder
TestTargetExecutorPausesWhenAnyMemberNotReady
TestTargetExecutorPausesOnExternalOrderWithoutCancel
TestTargetExecutorRecordsBelowMinimumBlockedTarget
TestTargetExecutorInsufficientCapacityRecordsBlockedTarget
TestListTargetLanesIncludesSpotBalancesAndUnmappedPositions
```

- [ ] **8.2 运行 RED 测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/target ./internal/runtime ./internal/infra/store -run 'Test(TargetExecutor|ListTargetLanes)'
```

- [ ] **8.3 实现 canonical instrument lane union**

每轮从以下集合取并集：

```text
LogicalAccountTarget.targets
SWAP t_exchange_positions
所有活动 TARGET orders
SPOT snapshot 中非 settlement 的正余额
```

SPOT 余额用 base asset、LogicalAccount settlement asset 和 Instrument Registry 映射 canonical `instrument_id`。映射失败的资产作为可见 remaining-position conflict，不能因 FULL omission 消失。

- [ ] **8.4 实现每轮最多一个动作**

固定决策顺序：

```text
1. LogicalAccount 非 ACTIVE 或成员 Not Ready -> 不下单
2. 非当前 runner 的 TARGET、OPERATOR、EXTERNAL 活动订单 -> PAUSED
3. 待 cancel 的旧 TARGET -> cancel 一个并立即 return
4. SUBMIT_UNKNOWN -> resolve 一个并立即 return
5. 关闭反向物理仓位 -> place 一个 reduce child 并立即 return
6. 减仓 -> 选择绝对仓位最大成员，place 一个 child 并立即 return
7. 加仓 -> 按 priority、可用容量、费用/价格稳定排序，place 一个 child 并立即 return
8. 剩余量低于交易所 minimum -> 写入 blocked_targets_json
9. 所有成员容量仍不足 -> 写入 blocked_targets_json 并置为 BLOCKED，不超配
```

动态择优只在同质成员间做；若第一候选容量不足，按稳定顺序落到下一候选。不能跨账户净掉相反仓位。

- [ ] **8.5 实现 restart 恢复**

每个 TARGET child 保存：

```text
owner_type = TARGET
owner_id = target_id
logical_account_id
runner_id
```

重启后按 LogicalAccountTarget、owner 和活动订单重算，不依赖内存任务队列。client order ID 读取订单首次创建时持久化的 `xid.New().String()`，不得重生成。

- [ ] **8.6 运行 GREEN 和竞态测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/target ./internal/runtime ./internal/infra/store
go test -race -count=1 ./internal/application/target ./internal/runtime ./internal/infra/store
```

- [ ] **8.7 替换旧单账户 TargetExecutor 后复跑 Trade**

```bash
cd modules/trade
go test -count=1 ./...
```

- [ ] **8.8 提交**

```bash
git add modules/trade/internal/application/target \
  modules/trade/internal/runtime \
  modules/trade/internal/infra/store/fact.go \
  modules/trade/internal/infra/store/fact_test.go
git commit -m "feat(trade): converge logical targets dynamically"
```

---

## 任务 9：实现订单 ownership 和 AccountSync 冲突暂停

**文件：**

- 修改：`modules/trade/schema/execution.sql`
- 修改：`modules/trade/internal/domain/order/order.go`
- 修改：`modules/trade/internal/domain/order/order_test.go`
- 修改：`modules/trade/internal/application/order/service.go`
- 修改：`modules/trade/internal/application/order/service_test.go`
- 修改：`modules/trade/internal/application/accountsync/service.go`
- 修改：`modules/trade/internal/application/accountsync/service_test.go`
- 修改：`modules/trade/internal/infra/store/fact.go`
- 修改：`modules/trade/internal/infra/store/fact_test.go`

- [ ] **9.1 写 ownership 和同步冲突测试**

```text
TestOrderOwnershipRequiresServerAssignedOwner
TestExternalOrderPausesOwningLogicalAccount
TestExternalFillImportsExternalOwnerAndPauses
TestAccountFactsWakeTargetWorkerAfterReleasingAccountLock
TestTargetOrderFromOtherRunnerPausesLogicalAccount
```

- [ ] **9.2 固定订单 ownership**

```go
type OrderOwner struct {
    Type             OwnerType // TARGET | OPERATOR | EXTERNAL
    OwnerID          string    // target_id | action_id | exchange import identity
    LogicalAccountID string
    RunnerID         *string
}
```

RPC 不可传 ownership。Target 调用 OrderService 时由可信内部参数赋值；Operator 使用 `action_id`；同步发现的未知 exchange order 固定为 EXTERNAL。

- [ ] **9.3 实现冲突暂停并避免锁反转**

统一锁顺序：

```text
LogicalAccount lock -> ExchangeAccount lock
```

AccountSync 当前在 account lock 内导入事实；新的“暂停 LogicalAccount”和“唤醒 TargetWorker”回调必须在释放 account lock 后执行。不要从 account lock 内反向获取 logical lock。

- [ ] **9.4 运行测试**

```bash
cd modules/trade
go test -count=1 ./internal/domain/order ./internal/application/order \
  ./internal/application/accountsync ./internal/infra/store
go test -race -count=1 ./internal/application/order \
  ./internal/application/accountsync ./internal/application/target
```

- [ ] **9.5 提交**

```bash
git add modules/trade/schema/execution.sql modules/trade/internal/domain/order \
  modules/trade/internal/application/order \
  modules/trade/internal/application/accountsync \
  modules/trade/internal/infra/store
git commit -m "feat(trade): enforce trusted order ownership"
```

---

## 任务 10：实现人工下单和一键清仓

**文件：**

- 新增：`modules/trade/internal/application/operator/service.go`
- 新增：`modules/trade/internal/application/operator/service_test.go`
- 新增：`modules/trade/internal/application/operator/flatten.go`
- 新增：`modules/trade/internal/application/operator/flatten_test.go`
- 新增：`modules/trade/internal/runtime/operator_worker.go`
- 新增：`modules/trade/internal/runtime/operator_worker_test.go`
- 修改：`modules/trade/internal/application/accountsync/service.go`
- 修改：`modules/trade/internal/infra/store/operator_action.go`

- [ ] **10.1 写人工操作测试**

```text
TestManualOrderPausesAndCancelsTargetsBeforeSubmit
TestManualOrderActionIDIsIdempotent
TestFlattenFreshSyncsBeforeCancelAndClose
TestFlattenWaitsForCancellationConfirmation
TestFlattenSkipsStaleFailedAccountAndContinuesOthers
TestFlattenReportsPartialRemainingPositionsAndEndsPaused
TestFlattenRetriesSameActionWithoutDuplicateChildren
TestFlattenIncludesDisabledMembersAndKeepsSpotSettlementCash
TestResumeRequiresReadyNoConflictAndWarnsAboutReopen
```

- [ ] **10.2 实现 ManualOrder action**

固定步骤：

```text
1. 以 action_id 幂等创建 OperatorAction
2. 将整个 LogicalAccount 置为 PAUSED
3. 取消该 LogicalAccount 的活动 TARGET orders
4. 同步并确认这些订单终态
5. 使用 owner_type=OPERATOR、owner_id=action_id 提交人工订单
6. 持久化结果；LogicalAccount 保持 PAUSED
```

失败时返回逐账户错误，不自动恢复 ACTIVE。

- [ ] **10.3 实现 FlattenLogicalAccount**

Flatten 的含义是：将 LogicalAccount 每个物理成员的可交易风险仓位精确归零，不做跨账户净额。

固定步骤：

```text
1. 原子写入 FLATTENING、递增 control_revision，并读取全部成员，包括 disabled member
2. 对每个成员做 fresh SyncAccount；同步失败记录并继续其他成员
3. 取消该成员所有 TARGET、OPERATOR、EXTERNAL 活动订单
4. 再次同步，确认 cancel 已终态
5. SWAP 按物理 position 精确 reduce-only 平仓
6. SPOT 保留 settlement cash，对其他正余额逐个卖出
7. unmapped asset、dust、minimum violation 写入逐账户 remaining positions
8. final sync
9. COMPLETED：全部归零；PARTIAL：有 remaining position/账户失败；FAILED：没有任何成员可执行
10. 无论 COMPLETED、PARTIAL 或 FAILED，最终都写回 PAUSED；执行期间保持 FLATTENING
```

相同 `action_id` 重启或重试时，通过 `owner_id=action_id` 找回已有 child，禁止重复下单。

- [ ] **10.4 运行 GREEN 和重启测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/operator ./internal/runtime
go test -race -count=1 ./internal/application/operator \
  ./internal/application/accountsync ./internal/runtime
```

- [ ] **10.5 提交**

```bash
git add modules/trade/internal/application/operator \
  modules/trade/internal/application/accountsync \
  modules/trade/internal/runtime/operator_worker.go \
  modules/trade/internal/runtime/operator_worker_test.go \
  modules/trade/internal/infra/store/operator_action.go
git commit -m "feat(trade): add paused manual execution and flatten"
```

---

## 任务 11：重做 Strategy 和 Trade 公共 RPC

**文件：**

- 修改：`modules/strategy/proto/strategy.proto`
- 生成：`modules/strategy/proto/strategygen/strategy.pb.go`
- 生成：`modules/strategy/proto/strategygen/strategy.trpc.go`
- 修改：`modules/strategy/proto/strategygen/validation.go`
- 修改：`modules/strategy/internal/rpc/service.go`
- 修改：`modules/strategy/internal/rpc/service_test.go`
- 修改：`modules/strategy/internal/rpc/frontend_service.go`
- 修改：`modules/strategy/internal/rpc/frontend_service_test.go`
- 删除：`modules/strategy/internal/bootstrap/exchange_account.go`
- 删除：`modules/strategy/internal/bootstrap/exchange_account_test.go`
- 新增：`modules/strategy/internal/bootstrap/logical_account.go`
- 新增：`modules/strategy/internal/bootstrap/logical_account_test.go`
- 修改：`modules/strategy/internal/bootstrap/bootstrap.go`
- 修改：`modules/strategy/internal/bootstrap/config.go`
- 修改：`modules/strategy/config/app.yaml`
- 修改：`modules/strategy/config/trpc_go.yaml`
- 修改：`modules/trade/proto/trade_service.proto`
- 生成：`modules/trade/proto/tradegen/trade_service.pb.go`
- 生成：`modules/trade/proto/tradegen/trade_service.trpc.go`
- 修改：`modules/trade/proto/tradegen/validation.go`
- 修改：`modules/trade/internal/rpc/account.go`
- 修改：`modules/trade/internal/rpc/account_test.go`
- 修改：`modules/trade/internal/rpc/execution.go`
- 修改：`modules/trade/internal/rpc/execution_test.go`
- 新增：`modules/trade/internal/rpc/logical_account.go`
- 新增：`modules/trade/internal/rpc/logical_account_test.go`
- 修改：`modules/trade/internal/rpc/convert.go`
- 修改：`modules/trade/internal/rpc/register.go`
- 修改：`modules/trade/internal/rpc/register_test.go`
- 修改：`modules/trade/config/trpc_go.yaml`

- [ ] **11.1 写 Strategy Proto/RPC 契约测试**

最终服务面：

```text
CreateStrategy / GetStrategy / ListStrategies
CreateRunner / GetRunner / ListRunners / UpdateRunner / SetRunnerStatus
RunOnce
ListStrategyResults / GetStrategyResult
ListStrategyTargets / GetEngineStatus
```

覆盖：

```text
TestStrategyProtoUsesRunnerAndResultVocabulary
TestCreateStrategyStoresImmutableArtifact
TestCreateRunnerValidatesLogicalAccountOwnership
TestRunOncePreviewDoesNotCreateResult
TestRunOnceFailedAttemptReturnsNoResult
TestRunOnceAcceptsCompleteHistoryWithoutStateOrDataRevision
TestRunnerQueriesEnforceSpaceScope
```

`RunOnce` 请求只携带 runner ID、trigger time、namespace 和完整历史 `data_json`；不携带 `data_revision` 或 Strategy state。删除 Binding、Run、Performance、SetExecutionMode API。Strategy 不再直连 ExchangeAccountService；是否可执行只由 Runner 是否挂 LogicalAccount 表达。

- [ ] **11.2 写 Trade Proto/RPC 契约测试**

新增：

```text
LogicalAccountService:
  CreateLogicalAccount
  GetLogicalAccount
  ListLogicalAccounts
  UpdateLogicalAccount
  AddLogicalAccountMember
  RemoveLogicalAccountMember
  ClaimLogicalAccountOwner
  ReleaseLogicalAccountOwner
  PauseLogicalAccount
  ResumeLogicalAccount
  FlattenLogicalAccount

TradeExecutionService:
  PlaceManualOrder
  CancelOrder
  GetOperatorAction
  GetLogicalAccountTarget
  ListOrders
  GetOrder
  ListFills
  ListPositions
```

删除公开的 `PauseAccount`、`PlaceOrder`、`SubmitTarget`。`PlaceManualOrder` 请求只含 `action_id`、目标 `exchange_account_id`、订单客户端字段、`fill_policy` 和 reason；服务端由物理账户反查所属 LogicalAccount，再暂停整个 LogicalAccount。请求不含 source、owner、strategy result、runner 或 `reduce_position_only`。

覆盖：

```text
TestManualOrderRPCCannotForgeOwnership
TestManualOrderRPCCannotSetReducePositionOnly
TestLogicalRPCRequiresActionIDAndReason
TestServiceNamesIncludesLogicalAccountService
TestPhysicalAccountRPCDoesNotExposePause
```

- [ ] **11.3 先改 Proto 并生成**

```bash
make -C modules/strategy/proto clean all
make -C modules/trade/proto clean all
```

预期：生成成功，但 RPC 实现因接口变化编译失败。

- [ ] **11.4 实现 RPC 和手写 validation**

Strategy 新建 Runner 默认为 disabled。启用带 LogicalAccount 的 Runner 时，先调用 Trade `ClaimLogicalAccountOwner`，再在 Strategy 事务中启用；本地事务失败则立即调用 `ReleaseLogicalAccountOwner`。禁用时先停 Runner，再释放 Trade owner。进程重启后若两侧 owner 不一致，readiness 失败并要求重复启用/禁用操作完成收敛，不增加 Saga。Trade 的服务端构造可信订单 ownership；所有 space scope 沿用当前认证边界。

- [ ] **11.5 运行模块测试**

```bash
(cd modules/strategy && go test -count=1 ./proto/strategygen ./internal/rpc ./internal/bootstrap)
(cd modules/trade && go test -count=1 ./proto/tradegen ./internal/rpc)
```

- [ ] **11.6 提交**

```bash
git add modules/strategy/proto modules/strategy/internal/rpc \
  modules/strategy/internal/bootstrap modules/strategy/config \
  modules/trade/proto modules/trade/internal/rpc modules/trade/config/trpc_go.yaml
git commit -m "feat(api): expose runners and logical account operations"
```

---

## 任务 12：收口 Secret、live 开关、Bootstrap 和健康状态

**文件：**

- 修改：`modules/trade/internal/secretclient/client.go`
- 修改：`modules/trade/internal/secretclient/client_test.go`
- 修改：`modules/trade/internal/config/app.go`
- 修改：`modules/trade/internal/config/app_test.go`
- 修改：`modules/trade/config/app.yaml`
- 修改：`modules/trade/internal/bootstrap/bootstrap.go`
- 修改：`modules/trade/internal/bootstrap/bootstrap_test.go`
- 修改：`modules/trade/internal/health/state.go`
- 修改：`modules/trade/internal/health/server_test.go`
- 修改：`modules/trade/internal/exchange/binance/binance.go`
- 修改：`modules/trade/internal/exchange/binance/binance_test.go`
- 修改：`modules/trade/internal/exchange/okx/okx.go`
- 修改：`modules/trade/internal/exchange/okx/okx_test.go`

- [ ] **12.1 写 Secret 和环境 profile 测试**

```text
TestResolveSecretRevealsOnlyConfiguredID
TestResolveSecretValidatesExchangeCategoryAndActive
TestConfigRejectsProductionAccountWhenLiveTradingDisabled
TestAdapterUsesFixedTestnetEndpoint
TestAdapterUsesFixedProductionEndpoint
TestConfigRejectsCustomExchangeEndpoint
```

- [ ] **12.2 删除无效 encryption_key，加入显式 live 开关**

```yaml
trade:
  live_trading_enabled: false
```

默认 false。存在 PRODUCTION ExchangeAccount 且开关 false 时拒绝自动和人工下单；同步仍允许。TESTNET 不受 live 开关影响。

- [ ] **12.3 单条 Reveal Secret**

直接按 `secret_id` 调用 `RevealSecret`，使用返回 metadata 校验：

```text
active = true
category = exchange credential
exchange = ExchangeAccount.exchange
space scope 匹配
```

删除最多列 200 条并逐条 Reveal 的路径。

- [ ] **12.4 固定 Testnet/Production endpoint**

ExchangeAccount 只保存环境枚举，Adapter 内按 Exchange 和环境选择受控 endpoint；不接受数据库或 RPC 自定义 URL，防止 profile 混用。

- [ ] **12.5 将新 worker 接入 Bootstrap 和健康检查**

启动顺序：

```text
store -> exchange manager -> initial successful reconcile
-> logical account service -> account sync
-> portfolio worker -> operator worker -> event consumer -> RPC
```

readiness 必须包含 Manager 首次成功、LogicalAccount worker、TargetWorker、Operator worker 和 Event consumer。worker 退出立即不就绪。

- [ ] **12.6 运行测试**

```bash
cd modules/trade
go test -count=1 ./internal/secretclient ./internal/config \
  ./internal/exchange/binance ./internal/exchange/okx \
  ./internal/bootstrap ./internal/health
```

- [ ] **12.7 提交**

```bash
git add modules/trade/internal/secretclient modules/trade/internal/config \
  modules/trade/config/app.yaml modules/trade/internal/bootstrap \
  modules/trade/internal/health modules/trade/internal/exchange/binance \
  modules/trade/internal/exchange/okx
git commit -m "fix(trade): gate live trading and runtime readiness"
```

---

## 任务 13：更新 Web、Admin 服务发现和当前文档

**文件：**

- 修改：`web/src/api/strategy.ts`
- 修改：`web/src/api/strategy-types.ts`
- 修改：`web/src/api/strategy.test.ts`
- 修改：`web/src/api/trade/http.ts`
- 修改：`web/src/api/trade/index.ts`
- 修改：`web/src/api/trade/types.ts`
- 修改：`web/src/api/trade/trade.test.ts`
- 修改：`web/src/views/strategy/overview/index.vue`
- 修改：`web/src/views/strategy/running/index.vue`
- 修改：`web/src/views/strategy/detail/index.vue`
- 修改：`web/src/views/strategy/components/strategy-operation-panel.vue`
- 修改：`web/src/views/strategy/components/strategy-operation-panel.test.ts`
- 删除：`web/src/views/strategy/components/strategy-state-summary.vue`
- 修改：`web/src/views/strategy/components/strategy-run-timeline.vue`
- 修改：`web/src/views/strategy/components/strategy-target-table.vue`
- 删除：`web/src/views/strategy/performance/index.vue`
- 删除：`web/src/views/strategy/components/strategy-performance-chart.vue`
- 删除：`web/src/views/strategy/performance/performance-format.ts`
- 删除：`web/src/views/strategy/performance/performance-format.test.ts`
- 修改：`web/tests/strategy-console.spec.ts`
- 删除：`web/tests/strategy-console-performance.spec.ts`
- 修改：`modules/admin/internal/service/sysdeploy/defaults.go`
- 修改：`modules/admin/internal/service/sysdeploy/defaults_test.go`
- 修改：`modules/trade/README.md`
- 修改：`modules/trade/DESIGN.md`
- 修改：`modules/trade/docs/exchange-apis.md`
- 修改：`modules/strategy/docs/frontend-verification.md`
- 修改：`docs/策略模块Python策略接入手册.md`
- 修改：`docs/系统架构.md`
- 删除或整体替换：`docs/交易模块架构设计.md`

- [ ] **13.1 先改 Web 测试**

Web 只展示 Strategy、Runner、StrategyResult、当前完整 targets、LogicalAccount control/readiness 和 OperatorAction。删除 state summary 和无生产写入来源的 performance UI。

```text
strategy API 返回 runner/result 字段
operation panel 只操作 Runner 启停
manual order 必须提供 action_id/reason
flatten 显示逐账户 remaining position/error
LogicalAccount PAUSED 时明确显示未执行的新 LogicalAccountTarget
```

- [ ] **13.2 更新 Admin 服务发现**

保留当前 Trade account/execution 服务，并新增：

```text
service name: trade_logical_account
default port: 11202
```

同步 defaults 和 acceptance 测试中的敏感服务列表。

- [ ] **13.3 更新权威文档**

`modules/trade/README.md + DESIGN.md` 作为 Trade 唯一事实源；Strategy 接入手册使用 `Strategy`、`StrategyRunner`、`StrategyResult`、LogicalAccount 和空 FULL 全平语义，并只描述三参数、完整历史窗口 Python entrypoint。删除 Saga、RebalanceSvc、Inbox、ExecutionBinding、StrategyDefinition/Run、state/next_state 和 state-format migration 的现役描述。

历史 `docs/superpowers/plans/*` 不回写。

- [ ] **13.4 运行 Web/Admin/文档测试**

```bash
(cd web && pnpm exec vitest run \
  src/api/strategy.test.ts \
  src/api/trade/trade.test.ts \
  src/views/strategy/components/strategy-operation-panel.test.ts)
(cd web && pnpm exec vue-tsc --noEmit)
(cd web && pnpm exec playwright test tests/strategy-console.spec.ts)
(cd web && pnpm build:prod)
(cd modules/admin && go test -count=1 ./internal/service/sysdeploy)
pnpm docs:build
```

- [ ] **13.5 提交**

```bash
git add web modules/admin/internal/service/sysdeploy \
  modules/trade/README.md modules/trade/DESIGN.md \
  modules/trade/docs/exchange-apis.md \
  modules/strategy/docs/frontend-verification.md \
  docs/策略模块Python策略接入手册.md docs/系统架构.md \
  docs/交易模块架构设计.md
git commit -m "docs(ui): align consoles with runners and logical accounts"
```

---

## 任务 14：补齐跨模块 E2E、重启和真实 Testnet 闭环

**文件：**

- 修改：`modules/trade/test/strategy_target_e2e_test.go`
- 修改：`modules/trade/test/strategy_target_external_e2e_test.go`
- 修改：`modules/trade/test/uncertain_order_e2e_test.go`
- 修改：`modules/trade/test/account_sync_e2e_test.go`
- 新增：`modules/trade/test/logical_account_operator_e2e_test.go`
- 修改：`modules/strategy/test/e2e_test.go`
- 修改：`modules/strategy/test/outbox_jetstream_e2e_test.go`
- 修改：`modules/strategy/test/strategy_trade_external_e2e_test.go`
- 修改：`modules/trade/scripts/testnet-smoke.sh`
- 新增：`modules/trade/cmd/testnet-smoke/main.go`
- 新增：`modules/trade/cmd/testnet-smoke/main_test.go`

- [ ] **14.1 补跨模块内存/SQLite E2E**

必须覆盖：

```text
TestLogicalAccountFullTargetConvergesAcrossBinanceAndOKX
TestLogicalAccountOmittedInstrumentConvergesToZero
TestLogicalAccountEmptyFullFlattensAllPhysicalPositions
TestLogicalAccountDoesNotNetOpposingPhysicalPositions
TestManualFlattenThenResumeCanReopenLatestTarget
TestRestartRestoresTargetOperatorAndUnknownSubmit
TestStrategyRunnerRunOnceCommitsResultAndOutbox
TestStrategyOutboxJetStreamReconnectAndCatchUp
```

这些测试使用 fake Exchange 或 Paper MARKET，验证真实 store、worker、事件和 restart 链，不只调用纯函数。

- [ ] **14.2 把 testnet-smoke 从预检改成真实闭环**

脚本必须分别接受 Binance 和 OKX Testnet 凭据，并对每个 Exchange 执行：

```text
1. 验证账户环境固定为 TESTNET
2. 启动服务并等待 Manager/LogicalAccount ready
3. fresh account sync
4. 提交带合法 client order ID 的最小 MARKET 或可撤 LIMIT
5. 按 client ID 查询并确认 exchange 接收
6. 等待 private stream 或主动 sync 收敛 order/fill
7. 若仍活动则 cancel，并同步确认终态
8. 重启 Trade
9. 再按 client ID 查询，确认没有重复 submit，状态和 fill 可恢复
10. 清理所有活动订单和测试仓位，输出逐账户 remaining position
```

真实执行门禁固定为：

```bash
: "${MOOX_BINANCE_TESTNET_SECRET_ID:?先导出 Binance Testnet secret ID}"
: "${MOOX_OKX_TESTNET_SECRET_ID:?先导出 OKX Testnet secret ID}"
MOOX_TRADE_TESTNET_CONFIRM=YES \
MOOX_BINANCE_TESTNET_SECRET_ID="$MOOX_BINANCE_TESTNET_SECRET_ID" \
MOOX_OKX_TESTNET_SECRET_ID="$MOOX_OKX_TESTNET_SECRET_ID" \
bash modules/trade/scripts/testnet-smoke.sh
```

脚本缺少确认值或任一凭据时必须明确 `SKIP` 并返回非零，不得把预检当 PASS。正式验收必须在两个 Exchange 都输出 `PASS submit/query/stream/sync/restart/cleanup`。

- [ ] **14.3 运行本地 E2E 和竞态测试**

```bash
(cd modules/strategy && CGO_ENABLED=1 go test -count=1 ./test)
(cd modules/trade && CGO_ENABLED=1 go test -count=1 ./test)
(cd modules/trade && go test -race -count=1 \
  ./internal/application/target \
  ./internal/application/operator \
  ./internal/application/order \
  ./internal/application/accountsync \
  ./internal/runtime \
  ./internal/infra/store)
bash scripts/test-strategy-trade-event-e2e.sh
```

- [ ] **14.4 在可用凭据环境运行两个 Testnet**

```bash
MOOX_TRADE_TESTNET_CONFIRM=YES \
MOOX_BINANCE_TESTNET_SECRET_ID="$MOOX_BINANCE_TESTNET_SECRET_ID" \
MOOX_OKX_TESTNET_SECRET_ID="$MOOX_OKX_TESTNET_SECRET_ID" \
bash modules/trade/scripts/testnet-smoke.sh
```

预期：Binance 和 OKX 各自完成 submit、query、private stream/sync、cancel 或 fill、restart recovery 和 cleanup；最终无活动测试订单，无非预期仓位。

- [ ] **14.5 提交**

```bash
git add modules/trade/test modules/trade/scripts/testnet-smoke.sh \
  modules/trade/cmd/testnet-smoke modules/strategy/test
git commit -m "test(trade): prove logical execution and testnet recovery"
```

---

## 任务 15：全量生成、独立 Code Review 和最终验收

**文件：**

- 检查：本计划涉及的全部文件
- 检查：`docs/superpowers/specs/2026-07-29-strategy-runner-logical-account-trade-design.md`

- [ ] **15.1 生成两次并确认幂等**

```bash
make proto
git status --short
make proto
git status --short
```

预期：第二次生成不产生新 diff；仅存在明确属于本改造的变更，或用户原有的无关未跟踪文件。

- [ ] **15.2 运行静态契约和 greenfield 检查**

```bash
bash scripts/verify-event-contracts.sh
bash scripts/test-greenfield-contract.sh
rg -n 'StrategyDefinition|StrategyBinding|ExecutionBinding|StrategyRun|strategy_version|state_schema_version|state_format_version|state_json|state_revision|next_state|data_revision|not_after|TargetState|PortfolioExecutor|PortfolioWorker' \
  modules/strategy modules/trade packages web/src docs \
  --glob '!docs/superpowers/plans/**' \
  --glob '!docs/superpowers/specs/**'
rg -n 'TimeInForce|ReduceOnly|time_in_force|reduce_only' \
  modules/trade/internal/domain modules/trade/proto
```

预期：前两条 PASS；第一条 `rg` 只允许出现在明确的旧 schema 拒绝测试/错误文本中；第二条不得命中生产领域或公开 Proto。Exchange Adapter 内仍允许使用原生 `timeInForce`、`reduceOnly` 字段。

- [ ] **15.3 运行全仓验证**

```bash
(cd packages/events && go test -count=1 ./...)
(cd modules/strategy && go test -count=1 ./...)
(cd modules/trade && go test -count=1 ./...)
./scripts/test-go-workspace.sh
make verify-pr
pnpm docs:build
```

预期：全部通过。

- [ ] **15.4 请求独立 `codeCR` subAgent 审查**

审查范围必须包含：

```text
Strategy 四表和不可变 artifact
无状态完整历史窗口、input hash、Result/outbox 原子性
空 FULL 跨 Go/Python/Event/Trade 的一致性
LogicalAccount 同质性、ownership 和 readiness
TargetExecutor 一轮一个动作及 SPOT/SWAP FULL union
OperatorAction 幂等、Flatten 重启和逐账户 remaining position
OrderService unknown submit 恢复与 reducer 单调性
FillPolicy 校验和 Exchange 原生字段映射
ReducePositionOnly 仅由服务端生成，人工 RPC 不可设置且禁止穿零
AccountSync 锁顺序
RPC ownership 防伪造
Testnet profile 和 live gate
旧 schema/旧术语删除
```

要求审查结论附文件、符号或行号。主 Agent 对每条结论独立核验；确认的问题先补失败测试再修复，并重跑受影响测试。

- [ ] **15.5 提交审查修复并保持工作树边界清晰**

```bash
git diff --check
git status --short
```

若有审查修复，为每条 finding 先增加失败测试，修复后重复该 finding 所属任务的精确 `git add` 文件列表，再执行：

```bash
git commit -m "fix(trade): address execution design review"
```

禁止使用 `git add -A` 或 `git add .`，禁止提交用户原有的无关文件。

- [ ] **15.6 在所有代码提交后运行 clean-tree Proto 检查**

```bash
make proto-check
git diff --check
git status --short
```

预期：`make proto-check` PASS；工作树只允许保留用户原有、与本计划无关的文件。

---

## 规格覆盖检查表

| 规格主题 | 实施任务 | 关键证明 |
|---|---:|---|
| Strategy 四表与不可变 artifact | 2、3 | schema 严格测试、manifest/ID 测试 |
| 无状态完整历史窗口 | 3、4、6 | 三参数 entrypoint、拒绝 state 字段、完整 input hash |
| StrategyRunner / StrategyResult 命名 | 2、6、11、13 | Proto、RPC、Web、文档 |
| Strategy 到 LogicalAccount | 5、6、7 | owner 唯一、subject、LogicalAccountTarget |
| FULL / 空全平 / instrument_id | 4、6、8、14 | 五层契约和 E2E |
| 同质账户组和动态择优 | 5、7、8 | 同质校验、优先级/容量回退 |
| 一轮一个外部动作 | 8 | cancel/resolve/place 后立即返回 |
| TARGET/OPERATOR/EXTERNAL ownership | 9、10、11 | 服务端赋值、RPC 防伪造 |
| 暂停整个 LogicalAccount | 7、9、10 | session 继续、自动执行停止 |
| ManualOrder / FlattenLogicalAccount | 10、11、14 | action 幂等、逐账户归零/剩余仓位 |
| 订单状态、OKX ID、Paper LIMIT | 1 | focused/race tests |
| Manager、Secret、live gate | 7、12 | readiness、单 Reveal、profile |
| 删除账本、旧 schema、旧文档 | 5、13、15 | schema 检查、静态护栏 |
| Binance/OKX 真实闭环 | 14、15 | submit/query/stream/sync/restart/cleanup |

## 最终完成定义

以下条件必须同时满足，才能宣告实施完成：

- Strategy 生产 schema 恰好四表，旧 schema 明确拒绝。
- Strategy schema、Proto、Go、Python、manifest 和现役文档不存在 `state_json`、`state_revision`、`next_state`、`state_format_version` 或 `data_revision`。
- Strategy/Trade/Web 只使用 Strategy、StrategyRunner、StrategyResult、LogicalAccount、LogicalAccountTarget、OperatorAction 术语。
- 空 `rebalance` 从 Python 到 Trade LogicalAccountTarget 全链合法，并在 E2E 中让全部物理仓位归零。
- 一个 LogicalAccount 的自动 TARGET 执行严格串行，每轮最多一个外部动作。
- 人工 RPC 无法伪造 ownership，人工下单和一键清仓都会先暂停整个 LogicalAccount。
- PAUSED 不停止物理账户同步和私有流；所有 enabled 成员 Ready 才允许恢复自动执行。
- OrderService 在 unknown submit 恢复时不会盲目重发，OKX ID 合法，Paper 只下 MARKET。
- 本地模块测试、race、跨模块 E2E、workspace、`make verify-pr`、文档构建和 clean-tree `make proto-check` 全部通过。
- Binance 与 OKX Testnet 都完成真实 submit/query/private stream/sync/restart/cleanup，且无遗留测试订单或非预期仓位。
- 独立 `codeCR` 审查问题已逐条核验和处理。
