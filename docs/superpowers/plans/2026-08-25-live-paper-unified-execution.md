# Trade 实盘与模拟盘统一执行 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在单个 Trade 进程和单个 SQLite 中实现统一的 Live/Paper 交易内核；Paper 使用真实公共行情、SQLite 事实和进程内原子撮合，Live 继续连接 Binance/OKX，并向 Strategy、RPC 和前端提供同一套订单、成交、持仓和资金曲线能力。

**Architecture:** OrderService、订单状态机、FillReducer、Holding/Position 投影、LogicalAccount 和查询层保持共用。`ExecutionFactory` 按 TradingAccount 组装 `ExecutionBundle`；Live 提供 ExecutionAdapter、MarketDataSource 和 AccountEventSource，Paper 提供只接受订单的 Adapter、公共行情、纯 ReservationPolicy 和单一 PaperMatcher。SQLite 是 Paper 唯一事实源，所有 Match/CAS/Fill/Position/reservation 更新在同一事务中完成。

**Tech Stack:** Go 1.25、tRPC-Go、Protocol Buffers、SQLite/GORM、NATS JetStream、Vue 3、TypeScript、Arco Design、VChart、Vitest、Playwright。

**Source of truth:** `docs/superpowers/specs/2026-08-25-live-paper-unified-execution-design.md`，基线提交 `f82d5bd2`。

---

## 一、实施约束

1. 这是绿场破坏式升级，不迁移旧 Trade/Strategy 数据，不保留旧 Proto、表、字段、服务或兼容别名。
2. 所有实现必须从干净 worktree 开始；当前工作区的 Factor、Monitor 改动不属于本计划。
3. 每个任务先写失败测试，确认失败原因，再写最小实现；每个任务结束时创建独立 commit。
4. 生成文件只通过 `make -C modules/trade/proto all` 或根目录 `make proto` 更新，禁止手工编辑 `*.pb.go` 和 `*.trpc.go`。
5. Paper 不实现 Reset、Archive、盘口、部分成交、独立账本、独立服务或分布式锁。
6. Production 开关关闭时禁止所有新 Production Order，包括 Flatten；允许账户创建、同步、查单和撤单。
7. Adapter 不承担幂等权威。Place/Cancel 幂等留在 OrderService；Adapter 只保证稳定 client ID 传递、错误归类和查询恢复。
8. 不在同一 SQLite 事务中通过普通 Store 查询。事务内只使用当前 `*store.Tx`，避免单连接等待自身。
9. 每个 PR 最多包含三个任务，后一 PR 基于前一 PR 的远程分支；禁止将未审查的五个阶段堆入同一 PR。

## 二、目标文件结构

### 2.1 新增文件

```text
modules/trade/schema/
  paper_account_config.sql
  equity.sql

modules/trade/internal/domain/reservation/
  reservation.go
  reservation_test.go

modules/trade/internal/infra/store/
  paper_account_config.go
  paper_account_config_test.go
  equity_point.go
  equity_point_test.go
  reservation_facts.go
  reservation_facts_test.go
  paper_match.go
  paper_match_test.go
  paper_simulation.go
  paper_simulation_test.go

modules/trade/internal/execution/
  adapter.go
  adapter_contract_test.go
  account_events.go
  marketdata.go
  marketdata_test.go
  instrument_resolver.go
  instrument_resolver_test.go
  factory.go
  factory_test.go
  reservation_policy.go
  reservation_policy_test.go
  paper/
    account_state.go
    account_state_test.go
    pricing.go
    pricing_test.go
    adapter.go
    adapter_test.go
    matcher.go
    matcher_test.go

modules/trade/internal/application/
  papersimulation/
    service.go
    service_test.go
  equity/
    service.go
    service_test.go
  holding/
    service.go
    service_test.go

modules/trade/internal/runtime/
  equity_sampler.go
  equity_sampler_test.go
  paper_matcher_worker.go
  paper_matcher_worker_test.go

modules/trade/internal/rpc/
  console.go
  console_test.go
  capabilities.go
  capabilities_test.go

web/src/views/trading/account-overview/
  account-form.ts
  account-form.test.ts

web/src/views/trading/logical-accounts/
  equity-curve.ts
  equity-curve.test.ts
  equity-curve.vue

modules/trade/test/
  live_paper_parity_e2e_test.go
  paper_matcher_restart_e2e_test.go
  equity_sampler_e2e_test.go
  close_paper_simulation_e2e_test.go
```

### 2.2 移动与删除

```text
Move:
  modules/trade/internal/domain/exchangeaccount/
    -> modules/trade/internal/domain/tradingaccount/

Delete after all references switch:
  modules/trade/internal/exchange/adapter.go
  modules/trade/internal/exchange/registry.go
  modules/trade/internal/exchange/registry_test.go
  modules/trade/internal/exchange/paper/paper.go
  modules/trade/internal/exchange/paper/paper_test.go

Never create:
  modules/trade/internal/infra/store/paper_reset.go
  modules/trade/internal/application/operator/reset_paper.go
  modules/trade/internal/execution/paper/portfolio.go
```

### 2.3 任务依赖与 PR

```text
PR 1: Task 1-3
  TradingAccount 绿场身份
    -> 最终 Schema/Store
    -> Execution 端口与 Live Adapter

PR 2: Task 4-5
  TradingSession / ExecutionFactory / Production gate
    -> ReservationFacts / OrderService / FillReducer 事务内核

PR 3: Task 6-8
  Paper AccountState / Adapter
    -> PaperMatcher 原子撮合
    -> Paper 模拟生命周期

PR 4: Task 9-10
  EquitySampler / Holding
    -> TradeConsoleService / Admin / Strategy

PR 5: Task 11-12
  共用前端
    -> E2E / Testnet / 文档 / 破坏式 cutover
```

---

### Task 1: 绿场重命名 TradingAccount 与双标的身份

**Files:**
- Move: `modules/trade/internal/domain/exchangeaccount/account.go` → `modules/trade/internal/domain/tradingaccount/account.go`
- Move: `modules/trade/internal/domain/exchangeaccount/account_test.go` → `modules/trade/internal/domain/tradingaccount/account_test.go`
- Modify: `modules/trade/schema/account.sql`
- Modify: `modules/trade/schema/instrument.sql`
- Modify: `modules/trade/schema/logical_account.sql`
- Modify: `modules/trade/schema/execution.sql`
- Modify: `modules/trade/schema/schema_test.go`
- Modify: `modules/trade/internal/infra/store/account.go`
- Modify: `modules/trade/internal/infra/store/fact.go`
- Modify: `modules/trade/internal/infra/store/logical_account.go`
- Modify: `modules/trade/internal/infra/store/reservation.go`
- Modify: `modules/trade/internal/infra/store/store.go`
- Modify: `modules/trade/internal/domain/shared/ids.go`
- Modify: `modules/trade/internal/domain/order/spec.go`
- Modify: `modules/trade/internal/application/account/repository.go`
- Modify: `modules/trade/internal/application/account/service.go`
- Modify: `modules/trade/internal/application/order/service.go`
- Modify: `modules/trade/internal/application/order/validator.go`
- Modify: `modules/trade/internal/application/accountsync/service.go`
- Modify: `modules/trade/internal/application/accountsync/facts_observer.go`
- Modify: `modules/trade/internal/application/logicalaccount/service.go`
- Modify: `modules/trade/internal/application/target/executor.go`
- Modify: `modules/trade/internal/application/operator/service.go`
- Modify: `modules/trade/internal/application/operator/flatten.go`
- Modify: `modules/trade/internal/runtime/manager.go`
- Modify: `modules/trade/internal/runtime/session.go`
- Modify: `modules/trade/internal/rpc/account.go`
- Modify: `modules/trade/internal/rpc/execution.go`
- Modify: `modules/trade/internal/rpc/logical_account.go`
- Modify: `modules/trade/internal/rpc/convert.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/exchange/types.go`
- Modify: `modules/trade/test/e2e_helpers_test.go`
- Modify: `modules/trade/cmd/testnet-smoke/harness.go`

- [ ] **Step 1: 写最终 Schema 身份失败测试**

在 `modules/trade/schema/schema_test.go` 增加：

```go
func TestAllSQLUsesTradingAccountIdentity(t *testing.T) {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.Exec(AllSQL()).Error)

    require.True(t, tableExists(t, db, "t_trading_accounts"))
    require.True(t, tableExists(t, db, "t_trading_positions"))
    require.False(t, tableExists(t, db, "t_exchange_accounts"))
    require.False(t, tableExists(t, db, "t_exchange_positions"))
    require.True(t, hasColumns(t, db, "t_trade_orders",
        "c_trading_account_id", "c_instrument_id", "c_exchange_symbol"))
    require.True(t, hasColumns(t, db, "t_order_fills",
        "c_trading_account_id", "c_instrument_id", "c_exchange_symbol"))
}
```

- [ ] **Step 2: 运行测试并确认 RED**

Run:

```bash
cd modules/trade
go test -count=1 ./schema -run TestAllSQLUsesTradingAccountIdentity
```

Expected: FAIL，错误包含 `t_trading_accounts` 不存在。

- [ ] **Step 3: 完成物理身份机械重命名**

执行以下一一映射，不保留别名：

```text
t_exchange_accounts             -> t_trading_accounts
c_exchange_account_id           -> c_trading_account_id
t_exchange_positions            -> t_trading_positions
ExchangeAccountRecord           -> TradingAccountRecord
ExchangeAccountSnapshot         -> TradingAccountSnapshot
ExchangeAccountID               -> TradingAccountID
LockExchangeAccount             -> LockTradingAccount
GetExchangeAccount              -> GetTradingAccount
GetExchangeAccountByID          -> GetTradingAccountByID
ListExchangeAccounts            -> ListTradingAccounts
ListEnabledExchangeAccounts     -> ListEnabledTradingAccounts
ExchangeSession                 -> TradingSession
```

使用 `git mv` 移动 domain 包。同步更新 account、logical member、order、fill、position、operator、target、runtime、RPC 和 testnet smoke 的字段名。

- [ ] **Step 4: 引入强类型标的 ID**

在 `modules/trade/internal/domain/shared/ids.go` 中定义：

```go
type InstrumentID string
type ExchangeSymbol string

func (v InstrumentID) String() string { return string(v) }
func (v ExchangeSymbol) String() string { return string(v) }
```

`OrderSpec` 只接收 `InstrumentID`；ExecutionAdapter 的边界只接收 `ExchangeSymbol`。Store 使用字符串持久化，但记录结构字段命名为 `InstrumentID` 和 `ExchangeSymbol`。

同步把 `exchange.Instrument.InstrumentID`、`exchange.Instrument.Symbol`、`exchange.OrderRequest.Symbol`、`exchange.Order.Symbol`、`exchange.Fill.Symbol` 和 `exchange.Position.Symbol` 改为对应强类型；HTTP 参数序列化时显式调用 `.String()`。

- [ ] **Step 5: 重写 instrument 与执行事实身份**

`instrument.sql` 的主键改为：

```sql
PRIMARY KEY (
    c_exchange,
    c_environment,
    c_market_type,
    c_exchange_symbol
)
```

`t_trade_orders`、`t_order_fills` 和 `t_trading_positions` 同时保存：

```text
c_instrument_id
c_exchange_symbol
```

所有账户外键改用 `t_trading_accounts (c_space_id, c_trading_account_id)`。执行事实不额外复制 environment，也不保留旧的三列 instrument 外键；`Tx.CreateOrder` 在事务内根据 TradingAccount 的 `MarketDataEnvironment()` 校验 `(exchange, environment, market_type, exchange_symbol, instrument_id)`，Fill/Position 身份从已校验 Order 派生。`validateExistingTradeSchema` 只批准最终表名；旧库应直接被拒绝。

- [ ] **Step 6: 更新 Go 引用并运行模块回归**

Run:

```bash
cd modules/trade
go test -count=1 ./schema ./internal/domain/... ./internal/infra/store \
  ./internal/application/... ./internal/runtime ./internal/rpc ./internal/bootstrap
```

Expected: PASS。

- [ ] **Step 7: 扫描旧后端身份**

Run:

```bash
rg -n 'ExchangeAccount|exchange_account_id|t_exchange_accounts|t_exchange_positions' \
  --glob '*.go' --glob '*.sql' \
  modules/trade/schema modules/trade/internal/domain modules/trade/internal/infra/store \
  modules/trade/internal/application modules/trade/internal/runtime \
  modules/trade/internal/exchange modules/trade/test modules/trade/cmd
```

Expected: 无输出。Proto、Web、Admin 和 Strategy 在 Task 10 统一切换，不纳入本次扫描。

- [ ] **Step 8: 提交绿场身份**

```bash
git add modules/trade/schema modules/trade/internal/domain \
  modules/trade/internal/infra/store modules/trade/internal/application \
  modules/trade/internal/runtime modules/trade/internal/rpc \
  modules/trade/internal/bootstrap modules/trade/internal/exchange/types.go \
  modules/trade/test modules/trade/cmd/testnet-smoke
git commit -m "refactor(trade): rename physical trading accounts"
```

---

### Task 2: 固化 PaperConfig、Paper Match 与双资金曲线 Schema

**Files:**
- Create: `modules/trade/schema/paper_account_config.sql`
- Create: `modules/trade/schema/equity.sql`
- Create: `modules/trade/internal/infra/store/paper_account_config.go`
- Create: `modules/trade/internal/infra/store/paper_account_config_test.go`
- Create: `modules/trade/internal/infra/store/equity_point.go`
- Create: `modules/trade/internal/infra/store/equity_point_test.go`
- Modify: `modules/trade/schema/account.sql`
- Modify: `modules/trade/schema/execution.sql`
- Modify: `modules/trade/schema/schema.go`
- Modify: `modules/trade/schema/schema_test.go`
- Modify: `modules/trade/internal/infra/store/account.go`
- Modify: `modules/trade/internal/infra/store/fact.go`
- Modify: `modules/trade/internal/infra/store/store.go`
- Modify: `modules/trade/internal/domain/tradingaccount/account.go`
- Modify: `modules/trade/internal/application/account/repository.go`
- Modify: `modules/trade/internal/config/app.go`
- Modify: `modules/trade/internal/config/app_test.go`
- Modify: `modules/trade/config/app.yaml`

- [ ] **Step 1: 写新表和 Paper 字段失败测试**

```go
func TestAllSQLCreatesPaperAndEquityFacts(t *testing.T) {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.Exec(AllSQL()).Error)

    for _, table := range []string{
        "t_paper_account_configs",
        "t_account_equity_points",
        "t_logical_account_equity_points",
    } {
        require.True(t, tableExists(t, db, table), table)
    }
    require.True(t, hasColumns(t, db, "t_trade_orders",
        "c_paper_execution_price", "c_first_match_pending"))
}
```

Run:

```bash
cd modules/trade
go test -count=1 ./schema -run TestAllSQLCreatesPaperAndEquityFacts
```

Expected: FAIL，缺少三个表和两个订单列。

- [ ] **Step 2: 创建不可变 PaperConfig 表**

`t_trading_accounts` 将旧 `c_environment` 改为 `c_live_environment TEXT NOT NULL DEFAULT ''`。约束固定为：

```sql
CHECK (
    (
        c_execution_mode = 'PAPER'
        AND c_live_environment = ''
        AND c_credential_secret_id = ''
    )
    OR
    (
        c_execution_mode = 'LIVE'
        AND c_live_environment IN ('TESTNET', 'PRODUCTION')
        AND c_credential_secret_id <> ''
    )
)
```

`paper_account_config.sql`：

```sql
CREATE TABLE IF NOT EXISTS t_paper_account_configs (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_initial_balance TEXT NOT NULL,
    c_maker_fee_rate TEXT NOT NULL,
    c_taker_fee_rate TEXT NOT NULL,
    c_slippage_bps TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
);
```

不提供 Update/Delete Store 方法。Store 创建时使用 `shared.ParseDecimal` 校验：

```text
initial_balance > 0
0 <= maker_fee_rate < 1
0 <= taker_fee_rate < 1
0 <= slippage_bps < 10000
```

- [ ] **Step 3: 创建账户与 LogicalAccount 曲线表**

`equity.sql`：

```sql
CREATE TABLE IF NOT EXISTS t_account_equity_points (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_bucket_time INTEGER NOT NULL,
    c_equity TEXT NOT NULL,
    c_available_funds TEXT NOT NULL,
    c_used_margin TEXT NOT NULL,
    c_unrealized_pnl TEXT,
    c_source_time INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_trading_account_id, c_bucket_time),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
);

CREATE TABLE IF NOT EXISTS t_logical_account_equity_points (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_bucket_time INTEGER NOT NULL,
    c_equity TEXT NOT NULL,
    c_available_funds TEXT NOT NULL,
    c_used_margin TEXT NOT NULL,
    c_unrealized_pnl TEXT,
    c_source_time INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_logical_account_id, c_bucket_time),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
);
```

Upsert 必须带：

```sql
ON CONFLICT (...) DO UPDATE SET
    c_equity = excluded.c_equity,
    c_available_funds = excluded.c_available_funds,
    c_used_margin = excluded.c_used_margin,
    c_unrealized_pnl = excluded.c_unrealized_pnl,
    c_source_time = excluded.c_source_time,
    c_mtime = CURRENT_TIMESTAMP
WHERE excluded.c_source_time >= c_source_time
```

- [ ] **Step 4: 增加 Paper Match 持久字段**

`t_trade_orders` 新增：

```sql
c_paper_execution_price TEXT,
c_first_match_pending INTEGER NOT NULL DEFAULT 0,
CHECK (c_first_match_pending IN (0, 1))
```

Paper MARKET 和首次 LIMIT 决策使用持久化 reference quote；MARKET 额外保存应用滑点后的 execution price。Live Order 两列保持 NULL/0。

- [ ] **Step 5: 实现 Store 记录与测试**

```go
type PaperAccountConfigRecord struct {
    SpaceID         string
    TradingAccountID string
    InitialBalance  string
    MakerFeeRate    string
    TakerFeeRate    string
    SlippageBPS     string
}

type EquityPointRecord struct {
    SpaceID          string
    TradingAccountID string
    LogicalAccountID string
    BucketTime       int64
    Equity           string
    AvailableFunds   string
    UsedMargin       string
    UnrealizedPnL    *string
    SourceTime       int64
}
```

最终领域账户：

```go
type LiveConfig struct {
    Environment        exchange.AccountEnvironment
    CredentialSecretID string
}

type PaperConfig struct {
    InitialBalance shared.Decimal
    MakerFeeRate   shared.Decimal
    TakerFeeRate   shared.Decimal
    SlippageBPS    shared.Decimal
}

type Account struct {
    ID                 string
    SpaceID            string
    Name               string
    Exchange           exchange.Exchange
    MarketType         exchange.MarketType
    ExecutionMode      exchange.ExecutionMode
    SettlementAsset    string
    MarginMode         exchange.MarginMode
    Status             exchange.AccountStatus
    Live               *LiveConfig
    Paper              *PaperConfig
    Ready              bool
    SyncSymbols         []shared.ExchangeSymbol
    LeverageSettings   map[shared.ExchangeSymbol]shared.Decimal
    FillCursors         map[shared.ExchangeSymbol]string
    Snapshot           exchange.AccountSnapshot
    SnapshotSourceTime time.Time
    LastSyncAt          time.Time
    LastReadyAt         time.Time
    LastError           string
}

func (a Account) MarketDataEnvironment() exchange.AccountEnvironment {
    if a.Paper != nil {
        return exchange.AccountEnvironmentProduction
    }
    return a.Live.Environment
}
```

`Validate()` 要求 Live/Paper 恰好一个非 nil；Paper 禁止 Secret，Live 必须有 Secret。Repository 创建 Paper 时在一个事务中写 TradingAccount 和 PaperConfig。

测试至少包含：

```go
func TestUpsertAccountEquityPointRejectsOlderSource(t *testing.T)
func TestUpsertLogicalEquityPointRejectsOlderSource(t *testing.T)
func TestPaperAccountConfigRejectsSecondInsert(t *testing.T)
func TestPaperAccountConfigRejectsInvalidDecimals(t *testing.T)
```

- [ ] **Step 6: 删除进程级 Paper 初始资金**

删除以下符号：

```text
config.RuntimeConfig.PaperInitialBalance
runtime.paper_initial_balance
MOOX 配置中的 paper_initial_balance
bootstrap 对 decimal(cfg.Runtime.PaperInitialBalance) 的读取
```

PaperConfig 由 TradingAccount repository 在创建账户的同一事务写入。

- [ ] **Step 7: 运行持久化测试**

```bash
cd modules/trade
go test -count=1 ./schema ./internal/infra/store \
  ./internal/domain/tradingaccount ./internal/application/account ./internal/config
```

Expected: PASS。

- [ ] **Step 8: 提交最终持久化基础**

```bash
git add modules/trade/schema modules/trade/internal/infra/store \
  modules/trade/internal/domain/tradingaccount \
  modules/trade/internal/application/account modules/trade/internal/config \
  modules/trade/config modules/trade/internal/bootstrap
git commit -m "feat(trade): persist paper and equity facts"
```

---

### Task 3: 建立 Execution 端口并迁移 Live Adapter

**Files:**
- Create: `modules/trade/internal/execution/adapter.go`
- Create: `modules/trade/internal/execution/adapter_contract_test.go`
- Create: `modules/trade/internal/execution/account_events.go`
- Create: `modules/trade/internal/execution/marketdata.go`
- Create: `modules/trade/internal/execution/marketdata_test.go`
- Create: `modules/trade/internal/execution/instrument_resolver.go`
- Create: `modules/trade/internal/execution/instrument_resolver_test.go`
- Create: `modules/trade/internal/execution/factory.go`
- Create: `modules/trade/internal/execution/factory_test.go`
- Modify: `modules/trade/internal/domain/reservation/reservation.go`
- Modify: `modules/trade/internal/execution/reservation_policy.go`
- Modify: `modules/trade/internal/exchange/binance/binance.go`
- Modify: `modules/trade/internal/exchange/binance/binance_test.go`
- Move: `modules/trade/internal/exchange/binance/private_stream.go` → `modules/trade/internal/exchange/binance/account_events.go`
- Move: `modules/trade/internal/exchange/binance/private_stream_test.go` → `modules/trade/internal/exchange/binance/account_events_test.go`
- Modify: `modules/trade/internal/exchange/okx/okx.go`
- Modify: `modules/trade/internal/exchange/okx/okx_test.go`
- Move: `modules/trade/internal/exchange/okx/private_stream.go` → `modules/trade/internal/exchange/okx/account_events.go`
- Move: `modules/trade/internal/exchange/okx/private_stream_test.go` → `modules/trade/internal/exchange/okx/account_events_test.go`
- Modify: `modules/trade/internal/exchange/adapter.go`
- Modify: `modules/trade/internal/exchange/paper/paper.go`
- Modify: `modules/trade/internal/exchange/paper/paper_test.go`

- [ ] **Step 1: 写 QuoteKey 与报价失败测试**

```go
func TestQuoteKeySeparatesBinanceSpotAndSwap(t *testing.T) {
    spot := execution.QuoteKey{
        Exchange: exchange.ExchangeBinance,
        MarketType: exchange.MarketTypeSpot,
        ExchangeSymbol: shared.ExchangeSymbol("BTCUSDT"),
    }
    swap := execution.QuoteKey{
        Exchange: exchange.ExchangeBinance,
        MarketType: exchange.MarketTypeSwap,
        ExchangeSymbol: shared.ExchangeSymbol("BTCUSDT"),
    }
    require.NotEqual(t, spot, swap)
}
```

在 Binance/OKX 测试中增加 `TestGetQuoteReturnsBidAskLastAndSourceTime`。Binance SPOT 断言请求 `/api/v3/ticker/24hr`，SWAP 断言 `/fapi/v1/ticker/24hr`，并解析 `bidPrice`、`askPrice`、`lastPrice`；OKX 断言 instrument type 与 MarketType 一致。

Run:

```bash
cd modules/trade
go test -count=1 ./internal/exchange/binance ./internal/exchange/okx \
  -run TestGetQuoteReturnsBidAskLastAndSourceTime
```

Expected: FAIL，`GetQuote` 尚不存在。

- [ ] **Step 2: 创建最终端口**

`adapter.go`：

```go
type ExecutionAdapter interface {
    GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error)
    ListPositionSnapshots(context.Context) ([]exchange.Position, error)
    ListOpenOrders(context.Context) ([]exchange.Order, error)
    ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error)
    GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error)
    PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error)
    CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error)
    SetLeverage(context.Context, shared.ExchangeSymbol, shared.Decimal) error
    SetMarginMode(context.Context, shared.ExchangeSymbol, exchange.MarginMode) error
}
```

`marketdata.go`：

```go
type MarketQuote struct {
    Bid        shared.Decimal
    Ask        shared.Decimal
    Last       shared.Decimal
    SourceTime time.Time
}

type QuoteKey struct {
    Exchange       exchange.Exchange
    MarketType     exchange.MarketType
    ExchangeSymbol shared.ExchangeSymbol
}

type MarketDataSource interface {
    LoadInstruments(context.Context) ([]exchange.Instrument, error)
    GetQuote(context.Context, shared.ExchangeSymbol) (MarketQuote, error)
}

type InstrumentResolver interface {
    Resolve(
        context.Context,
        tradingaccount.Account,
        shared.InstrumentID,
    ) (exchange.Instrument, shared.ExchangeSymbol, error)
}
```

Store-backed resolver 使用 `account.Exchange`、`account.MarketDataEnvironment()`、`account.MarketType` 和 canonical InstrumentID 查询唯一原生 symbol。测试必须证明 Binance TESTNET/PRODUCTION 与 SPOT/SWAP 不串行。

`account_events.go`：

```go
type AccountEventSource interface {
    Subscribe(context.Context, AccountEventHandler) error
}

type AccountEventHandler interface {
    OnSubscribed()
    OnOrder(context.Context, exchange.Order) error
    OnFill(context.Context, exchange.Fill) error
    OnPosition(context.Context, exchange.Position) error
    OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error
}
```

`domain/reservation/reservation.go` 在本任务先定义 `Facts`、`Reservation` 和 `ErrInsufficientReducibleQuantity`；`execution/reservation_policy.go` 先定义纯接口，Task 5 再增加 Live/Paper 实现：

```go
type ReservationPolicy interface {
    Evaluate(
        tradingaccount.Account,
        exchange.Instrument,
        order.OrderSpec,
        MarketQuote,
        reservation.Facts,
    ) (reservation.Reservation, error)
}
```

- [ ] **Step 3: 实现 Binance/OKX MarketDataSource**

两个 Adapter 实例都绑定 Exchange 和 MarketType，因此 `GetQuote` 只接收 ExchangeSymbol。返回 bid、ask、last 和来源时间；缺失 bid/ask 时保留零值，由调用方按设计回退 last。

旧 `internal/exchange/paper.Adapter` 只做签名机械迁移，使其临时满足 ExecutionAdapter；不得在该文件继续增加能力。Task 6 用 SQLite 实现替换并删除它。

保留 `GetReferencePrice` 作为迁移期间的薄包装：

```go
func (a *Adapter) GetReferencePrice(ctx context.Context, symbol string) (exchange.ReferencePrice, error) {
    quote, err := a.GetQuote(ctx, shared.ExchangeSymbol(symbol))
    if err != nil {
        return exchange.ReferencePrice{}, err
    }
    return exchange.ReferencePrice{Price: quote.Last, UpdatedAt: quote.SourceTime}, nil
}
```

- [ ] **Step 4: 迁移账户事件握手**

新增 `Subscribe`。Binance 在用户数据连接建立或订阅 ACK 后调用 `OnSubscribed`；OKX 必须等待全部 channel ACK。Task 3 为保持当前 Session 可编译，临时保留一行 `SubscribePrivate` 委托包装；Task 4 切换 Session 后立即删除包装、`PrivateReadyHandler` 和 `PrivateStreamMetadataGate`。保留心跳、重连错误分类和标准化逻辑。

测试：

```go
func TestAccountEventsNotifySubscribedAfterAcknowledgement(t *testing.T)
func TestAccountEventsDoNotNotifyBeforeAcknowledgement(t *testing.T)
func TestAccountEventsReturnTransportErrorOnDisconnect(t *testing.T)
```

- [ ] **Step 5: 定义注入式 ExecutionFactory**

父包不 import `execution/paper`，避免包循环：

```go
type ExecutionBundle struct {
    Adapter            ExecutionAdapter
    AccountEvents      AccountEventSource
    MarketData         MarketDataSource
    ReservationPolicy ReservationPolicy
    InstrumentResolver InstrumentResolver
}

type LiveBinder func(
    tradingaccount.Account,
    exchange.Credential,
) (ExecutionBundle, error)

type PaperBinder func(
    tradingaccount.Account,
) (ExecutionBundle, error)

type Factory struct {
    BindLive  LiveBinder
    BindPaper PaperBinder
}

func (f Factory) Bind(account tradingaccount.Account, credential exchange.Credential) (ExecutionBundle, error) {
    if account.ExecutionMode == exchange.ExecutionModePaper {
        return f.BindPaper(account)
    }
    return f.BindLive(account, credential)
}
```

- [ ] **Step 6: 添加 Adapter 分层契约测试**

契约只验证：

```text
client_order_id 原样传递
OrderRequest 字段映射
错误归类为 REJECTED / TRANSPORT_UNKNOWN / RATE_LIMITED / NOT_FOUND
UNKNOWN 后可以通过 GetOrder/ListRecentFills 恢复
Cancel 使用原 client_order_id
```

不要重复调用真实 Adapter 并要求幂等；OrderService 在 Task 5 验证 Place/Cancel 幂等。

- [ ] **Step 7: 运行执行端口测试并提交**

```bash
cd modules/trade
go test -count=1 ./internal/execution ./internal/exchange/binance ./internal/exchange/okx
go run ../../scripts/check/check-trade-exchange-terminology.go
```

Expected: PASS，术语门禁输出 `trade Exchange terminology passed`。

```bash
git add modules/trade/internal/execution modules/trade/internal/exchange/binance \
  modules/trade/internal/exchange/okx modules/trade/internal/exchange/paper \
  modules/trade/internal/exchange/adapter.go modules/trade/internal/domain/shared/ids.go \
  modules/trade/internal/domain/reservation
git commit -m "refactor(trade): define unified execution ports"
```

---

### Task 4: 接入 ExecutionFactory、TradingSession 与 Production 安全闸门

**Files:**
- Modify: `modules/trade/internal/runtime/session.go`
- Modify: `modules/trade/internal/runtime/session_test.go`
- Modify: `modules/trade/internal/runtime/manager.go`
- Modify: `modules/trade/internal/runtime/manager_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/trade/internal/application/account/service.go`
- Modify: `modules/trade/internal/application/account/service_test.go`
- Modify: `modules/trade/internal/application/order/service.go`
- Modify: `modules/trade/internal/application/order/service_test.go`
- Modify: `modules/trade/internal/health/state.go`
- Modify: `modules/trade/internal/health/server_test.go`

- [ ] **Step 1: 写 Live 启动顺序失败测试**

在 `runtime/session_test.go` 固定顺序：

```go
func TestTradingSessionLoadsMetadataBeforeSubscribeAndReplaysBuffer(t *testing.T) {
    fixture := newSessionFixture(t)
    fixture.events.EmitFillBeforeSnapshot()
    require.NoError(t, fixture.session.Run(fixture.ctx))
    require.Equal(t, []string{
        "load-instruments",
        "subscribe",
        "subscribed",
        "account-snapshot",
        "positions",
        "open-orders",
        "recent-fills",
        "apply-snapshot",
        "replay-fill",
        "ready",
    }, fixture.calls)
}
```

另写：

```go
func TestTradingSessionDisconnectClearsReady(t *testing.T)
func TestTradingSessionNeverReadyBeforeOnSubscribed(t *testing.T)
```

- [ ] **Step 2: 重构 TradingSession**

Live 路径固定为：

```text
SetReady(false)
LoadInstruments + persist
start AccountEventSource.Subscribe with buffering handler
wait OnSubscribed
load account/position/order/fill snapshots
apply snapshot
replay buffered account events
SetReady(true)
periodic REST reconciliation
Subscribe return -> SetReady(false)
```

Paper 路径在 Task 7 接入真实 matcher；本任务使用注入的 `PaperExecutionReady func() bool`，无 AccountEvents 时执行 instrument 加载和当前 Adapter 快照，再根据该函数设置 Ready，禁止创建伪订阅。

Session 切换完成后删除 Binance/OKX 的临时 `SubscribePrivate` 包装、`exchange.EventHandler`、`PrivateReadyHandler` 和 `PrivateStreamMetadataGate`；测试名称同步使用 AccountEvent。

- [ ] **Step 3: 让 Manager 暴露 ExecutionBundle**

```go
func (m *Manager) Bundle(tradingAccountID string) (execution.ExecutionBundle, error)
func (m *Manager) Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
func (m *Manager) MarketData(tradingAccountID string) (execution.MarketDataSource, error)
```

Manager 的 session config 比较必须包含 Exchange、MarketType、ExecutionMode、LiveConfig/PaperConfig、settlement asset、margin mode、sync symbols 和 leverage。

- [ ] **Step 4: Bootstrap 使用 ExecutionFactory**

替换 `exchange.Registry.Bind` 和直接 `paper.New` 分支。Live binder 创建 Binance/OKX Adapter 并把同一实例放入 Adapter、MarketData、AccountEvents；Paper binder 暂时绑定旧 Paper Adapter 以保持编译，Task 6 替换为新实现。

- [ ] **Step 5: 收紧 Production 闸门语义**

修改 `account.Service`：

```text
validateCredential:
  始终验证 Live Secret
  不读取 live_trading_enabled

ExecutionEligibility:
  校验 ENABLED + Ready
  Production 且 live_trading_enabled=false 时返回 ErrLiveTradingDisabled
```

OrderService 的 Place 和 Submit 都调用 ExecutionEligibility。Cancel、Sync、GetOrder 不调用该闸门。

测试：

```go
func TestCreateProductionAccountAllowedWhenTradingDisabled(t *testing.T)
func TestPlaceProductionOrderRejectedWhenTradingDisabled(t *testing.T)
func TestSubmitPendingProductionOrderRejectedWhenTradingDisabled(t *testing.T)
func TestCancelProductionOrderAllowedWhenTradingDisabled(t *testing.T)
func TestTestnetOrderIgnoresProductionGate(t *testing.T)
```

- [ ] **Step 6: 运行 Runtime 与安全闸门测试**

```bash
cd modules/trade
go test -count=1 ./internal/runtime ./internal/application/account \
  ./internal/application/order ./internal/bootstrap ./internal/health
```

Expected: PASS。

- [ ] **Step 7: 提交 Session 与安全闸门**

```bash
git add modules/trade/internal/runtime modules/trade/internal/bootstrap \
  modules/trade/internal/application/account modules/trade/internal/application/order \
  modules/trade/internal/health modules/trade/internal/exchange
git commit -m "refactor(trade): unify account sessions and safety gate"
```

---

### Task 5: 实现事务内 ReservationFacts 与 FillReducer 核心

**Files:**
- Create: `modules/trade/internal/domain/reservation/reservation.go`
- Create: `modules/trade/internal/domain/reservation/reservation_test.go`
- Create: `modules/trade/internal/infra/store/reservation_facts.go`
- Create: `modules/trade/internal/infra/store/reservation_facts_test.go`
- Create: `modules/trade/internal/execution/reservation_policy.go`
- Create: `modules/trade/internal/execution/reservation_policy_test.go`
- Modify: `modules/trade/internal/application/order/validator.go`
- Modify: `modules/trade/internal/application/order/validator_test.go`
- Modify: `modules/trade/internal/application/order/service.go`
- Modify: `modules/trade/internal/application/order/service_test.go`
- Modify: `modules/trade/internal/application/consumer/fill.go`
- Modify: `modules/trade/internal/application/consumer/fill_test.go`
- Modify: `modules/trade/internal/infra/store/reservation.go`

- [ ] **Step 1: 完善无 Store 依赖的 Reservation 领域**

```go
package reservation

var ErrInsufficientReducibleQuantity = errors.New("trade reservation: insufficient reducible quantity")

type Facts struct {
    AvailableByAsset           map[string]shared.Decimal
    AvailableFunds             shared.Decimal
    SignedPositionQuantity     shared.Decimal
    AvailableReducibleQuantity shared.Decimal
    Leverage                   shared.Decimal
}

type Reservation struct {
    Asset               string
    Quantity            shared.Decimal
    PaperExecutionPrice *shared.Decimal
    FirstMatchPending   bool
}
```

`execution/reservation_policy.go` 定义纯计算端口；Store 只 import `domain/reservation`，因此不会形成循环：

```go
type ReservationPolicy interface {
    Evaluate(
        tradingaccount.Account,
        exchange.Instrument,
        order.OrderSpec,
        MarketQuote,
        reservation.Facts,
    ) (reservation.Reservation, error)
}
```

- [ ] **Step 2: 写纯 Policy 表驱动测试**

测试矩阵：

```go
func TestPaperReservationPolicy(t *testing.T) {
    tests := []struct {
        name      string
        spec      order.OrderSpec
        facts     reservation.Facts
        wantAsset string
        wantQty   string
        wantErr   error
    }{
        {"spot market buy uses persisted price and taker fee", marketBuyAt("2", "100"), spotFunds("1000"), "USDT", "200.4", nil},
        {"spot gtc buy uses limit and max fee", limitBuy("2", "100"), spotFunds("1000"), "USDT", "200.4", nil},
        {"spot sell reserves base quantity", marketSell("2"), spotBTC("3"), "BTC", "2", nil},
        {"swap open reserves margin and fee", swapBuy("2"), swapFunds("1000", "10"), "USDT", "20.4", nil},
        {"swap reduce only reserves fee", reduceSell("0.8"), longPosition("1", "1"), "USDT", "0.16", nil},
        {"aggregate reduce only exceeds capacity", reduceSell("0.8"), longPosition("1", "0.2"), "", "0", reservation.ErrInsufficientReducibleQuantity},
    }
    runReservationCases(t, tests)
}
```

Run:

```bash
cd modules/trade
go test -count=1 ./internal/execution -run TestPaperReservationPolicy
```

Expected: FAIL，Policy 尚未实现。

- [ ] **Step 3: 实现 `Tx.LoadReservationFacts`**

签名固定为：

```go
func (tx *Tx) LoadReservationFacts(
    account TradingAccountRecord,
    instrument InstrumentRecord,
    spec order.OrderSpec,
) (reservation.Facts, error)
```

规则：

```text
Live:
  available = confirmed snapshot - GetUnreflectedReservation

Paper Spot:
  balances = initial balance + all persisted fills
  available = balances - all non-terminal order reservations

Paper Swap:
  available_funds = initial + realized + unrealized - fees - used margin - active reservations
  available_reducible =
    max(0, abs(position) - sum(non-terminal same-side reduce-only remaining quantity))
  remaining quantity = quantity - filled_quantity
  include CANCELING and CANCEL_UNKNOWN
  never use remaining_reserved_quantity as reduce capacity
```

所有 SQL 通过 `tx.db` 执行；测试把 Store 最大连接数设为 1，并以 context timeout 证明不会调用普通 Store。

- [ ] **Step 4: 收窄 Validator**

`Validator.Validate` 只处理：

```text
account ownership / status / Ready
OrderSpec freshness and shape
Instrument status
quantity step and minimum
min/max notional
leverage ceiling
```

删除 `ErrPaperLimit`、`FeeBufferRate`、`withFeeBuffer`、余额读取和 reduce-only 单笔仓位校验。

- [ ] **Step 5: 重写 OrderService.Place 原子流程**

```go
func (s *Service) Place(ctx context.Context, spaceID string, spec order.OrderSpec) (order.Order, error) {
    unlock := s.Store.LockTradingAccount(spec.TradingAccountID)
    defer unlock()

    if spec.ClientOrderID == "" {
        spec.ClientOrderID = xid.New().String()
    }
    existing, err := s.Store.GetOrderByClientID(
        ctx,
        spaceID,
        spec.TradingAccountID,
        spec.ClientOrderID,
    )
    if err == nil {
        if !sameSpec(existing, spec) {
            return order.Order{}, ErrIdempotencyConflict
        }
        return domainOrder(existing)
    }
    if !errors.Is(err, gorm.ErrRecordNotFound) {
        return order.Order{}, err
    }

    account, instrument, quote, err := s.validateAndQuote(ctx, spaceID, spec)
    if err != nil {
        return order.Order{}, err
    }
    bundle, err := s.Executions.Bundle(spec.TradingAccountID)
    if err != nil {
        return order.Order{}, err
    }

    var created store.OrderRecord
    err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
        facts, err := tx.LoadReservationFacts(accountRecord(account), instrumentRecord(instrument), spec)
        if err != nil {
            return err
        }
        spec, err = deriveReduceOnly(spec, facts.SignedPositionQuantity)
        if err != nil {
            return err
        }
        held, err := bundle.ReservationPolicy.Evaluate(account, instrument, spec, quote, facts)
        if err != nil {
            return err
        }
        created = orderRecord(s.orderID(), spec, account, instrument, held)
        return tx.CreateOrder(created)
    })
    if errors.Is(err, store.ErrConflict) {
        replay, getErr := s.Store.GetOrderByClientID(
            ctx,
            spaceID,
            spec.TradingAccountID,
            spec.ClientOrderID,
        )
        if getErr != nil {
            return order.Order{}, getErr
        }
        if !sameSpec(replay, spec) {
            return order.Order{}, ErrIdempotencyConflict
        }
        return domainOrder(replay)
    }
    if err != nil {
        return order.Order{}, err
    }
    return domainOrder(created)
}
```

instrument 解析、网络报价和 freshness 检查在事务外完成；Facts、Policy 和 CreateOrder 在同一事务内完成。

- [ ] **Step 6: 抽出 FillReducer 事务内核心**

在 `consumer.Reducer` 增加：

```go
func (r *Reducer) ApplyFillToOrderTx(
    tx *store.Tx,
    record store.OrderRecord,
    expectedVersion uint64,
    fill exchange.Fill,
    source Source,
) (bool, error)
```

`ApplyFill` 只负责开启事务、FindOrderForFill，再调用该核心。新增来源：

```go
const (
    OriginAccountEvent FillOrigin = "account_event"
    OriginRESTSync     FillOrigin = "rest_sync"
    OriginPaperMatcher FillOrigin = "paper_matcher"
)
```

核心按顺序执行 InsertFill、Order state、reservation、Swap Position、Order CAS。reduce-only 也要从 settlement reservation 消耗实际 fee；事务任一步失败全部回滚。

- [ ] **Step 7: 增加 Fill 成功 observer**

```go
type FillAppliedObserver interface {
    FillApplied(context.Context, string)
}
```

仅在外层事务提交成功且 `applied == true` 后传入 TradingAccountID；重复 Fill 不 enqueue。wake 是可丢失提示，不把 observer 失败返回给已提交的 Fill 调用。

- [ ] **Step 8: 运行预占与 reducer 测试**

```bash
cd modules/trade
go test -count=1 ./internal/domain/reservation ./internal/infra/store \
  ./internal/execution ./internal/application/order ./internal/application/consumer \
  -run 'Reservation|ReduceOnly|ApplyFill|FillApplied'
```

Expected: PASS。

- [ ] **Step 9: 提交事务执行核心**

```bash
git add modules/trade/internal/domain/reservation \
  modules/trade/internal/infra/store modules/trade/internal/execution \
  modules/trade/internal/application/order modules/trade/internal/application/consumer
git commit -m "feat(trade): reserve orders from transactional facts"
```

---

### Task 6: 实现 Paper AccountState、定价与仅接受订单的 Adapter

**Files:**
- Create: `modules/trade/internal/execution/paper/account_state.go`
- Create: `modules/trade/internal/execution/paper/account_state_test.go`
- Create: `modules/trade/internal/execution/paper/pricing.go`
- Create: `modules/trade/internal/execution/paper/pricing_test.go`
- Create: `modules/trade/internal/execution/paper/adapter.go`
- Create: `modules/trade/internal/execution/paper/adapter_test.go`
- Modify: `modules/trade/internal/execution/factory.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Delete: `modules/trade/internal/exchange/paper/paper.go`
- Delete: `modules/trade/internal/exchange/paper/paper_test.go`

- [ ] **Step 1: 写 MARKET 定价失败测试**

```go
func TestMarketExecutionPriceUsesSideAndSlippage(t *testing.T) {
    quote := execution.MarketQuote{
        Bid: shared.MustDecimal("100"),
        Ask: shared.MustDecimal("101"),
        Last: shared.MustDecimal("100.5"),
        SourceTime: time.UnixMilli(1_700_000_000_000),
    }
    buy, err := marketExecutionPrice(exchange.SideBuy, quote, shared.MustDecimal("10"))
    require.NoError(t, err)
    sell, err := marketExecutionPrice(exchange.SideSell, quote, shared.MustDecimal("10"))
    require.NoError(t, err)
    require.Equal(t, "101.101", buy.String())
    require.Equal(t, "99.9", sell.String())
}
```

另测 bid/ask 缺失回退 last、过期 quote 拒绝、LIMIT 成交价不劣于 limit。

- [ ] **Step 2: 写 AccountState 数学测试**

```text
Spot:
  initial 1000 USDT
  BUY 2 BTC @100, fee 0.4 USDT
  result: 799.6 USDT + 2 BTC
  active BUY reservation 100 USDT
  available USDT: 699.6, locked: 100

Swap:
  open long 2 @100
  add long 1 @110
  reduce 1 @120
  verify quantity, entry, realized, unrealized, used margin, fee
```

测试函数：

```go
func TestSpotAccountStateIncludesActiveReservations(t *testing.T)
func TestSwapAccountStateTracksPositionAndFees(t *testing.T)
func TestAccountStateAndReservationFactsAgreeOnAvailable(t *testing.T)
```

交叉测试使用同一组 PaperConfig、Fill 和活动订单，断言 AccountState Snapshot 的 available/locked 与 `Tx.LoadReservationFacts` 完全一致，防止两条重建路径出现资金口径漂移。

- [ ] **Step 3: 实现纯 AccountState**

```go
type AccountState struct {
    SettlementAsset string
    Balances        map[string]shared.Decimal
    Positions       map[shared.InstrumentID]PositionState
    CumulativeFee   shared.Decimal
    RealizedPnL     shared.Decimal
    Reservations    []reservation.Reservation
}

func Rebuild(
    account tradingaccount.Account,
    instruments []exchange.Instrument,
    fills []exchange.Fill,
    activeOrders []store.OrderRecord,
) (AccountState, error)
```

该文件不调用网络、不持有 Store。Snapshot/Holding/Position 方法显式接收 `map[execution.QuoteKey]execution.MarketQuote`。

- [ ] **Step 4: 实现 PaperAdapter 接受语义**

PaperAdapter 只读取共享事实并返回确定性接受结果：

```go
type Adapter struct {
    Account store.TradingAccountRecord
    Store   FactStore
    Wake    func()
    Now     func() time.Time
}

func (a *Adapter) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (exchange.Order, error) {
    orderID, _ := paperIDs(a.Account.TradingAccountID, req.ClientOrderID)
    a.Wake()
    return exchange.Order{
        ExchangeOrderID: orderID,
        ClientOrderID: req.ClientOrderID,
        Symbol: req.Symbol,
        Status: exchange.OrderStatusOpen,
        CreatedAt: a.now(),
        UpdatedAt: a.now(),
    }, nil
}
```

Adapter 不生成 Fill、不更新 Position、不维护 map、不实现 AccountEventSource。Get/List 方法从 SQLite 重建。

OrderService 调用 PlaceOrder 时仍持有 TradingAccount 锁；wake 可以先到达，但 Matcher 必须获取同一锁后才执行，因此只能看到已持久化的 OPEN/version。周期扫描负责 wake 丢失恢复。

`CancelOrder` 返回确定性的 CANCELED 回报，不生成事件。OrderService 收到终态回报后调用共用 ConfirmCancel 事务；Task 7 用 OPEN/version 校验证明 Cancel 与 Match 只有一方成功。

- [ ] **Step 5: 用新 PaperAdapter 替换旧包装器**

Bootstrap 的 PaperBinder 读取 PaperConfig，绑定所选 Exchange/MarketType 的生产公共 MarketDataSource，再构造 PaperAdapter。删除 `paper.New(baseAdapter, ..., initialBalance)` 和整个旧 `internal/exchange/paper`。

- [ ] **Step 6: 运行 Paper State 与 Adapter 测试**

```bash
cd modules/trade
go test -count=1 ./internal/execution/paper \
  -run 'TestMarketExecution|TestSpotAccountState|TestSwapAccountState|TestPaperAdapter'
go test -count=1 ./internal/bootstrap
```

Expected: PASS；`TestPaperAdapterReturnsAcceptedWithoutFill` 断言共享 Fill 表仍为空。

- [ ] **Step 7: 提交 Paper 账户状态**

```bash
git add modules/trade/internal/execution/paper \
  modules/trade/internal/execution/factory.go modules/trade/internal/bootstrap \
  modules/trade/internal/exchange/paper
git commit -m "feat(trade): rebuild paper account state"
```

---

### Task 7: 实现 PaperMatcher 原子 MatchOrder 与 worker 健康

**Files:**
- Create: `modules/trade/internal/infra/store/paper_match.go`
- Create: `modules/trade/internal/infra/store/paper_match_test.go`
- Create: `modules/trade/internal/execution/paper/matcher.go`
- Create: `modules/trade/internal/execution/paper/matcher_test.go`
- Create: `modules/trade/internal/runtime/paper_matcher_worker.go`
- Create: `modules/trade/internal/runtime/paper_matcher_worker_test.go`
- Modify: `modules/trade/internal/application/consumer/fill.go`
- Modify: `modules/trade/internal/application/consumer/fill_test.go`
- Modify: `modules/trade/internal/runtime/session.go`
- Modify: `modules/trade/internal/runtime/session_test.go`
- Modify: `modules/trade/internal/health/state.go`
- Modify: `modules/trade/internal/health/server_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Delete: `modules/trade/internal/exchange/adapter.go`
- Delete: `modules/trade/internal/exchange/registry.go`
- Delete: `modules/trade/internal/exchange/registry_test.go`

- [ ] **Step 1: 写候选订单恢复失败测试**

```go
func TestListPaperMatchCandidatesIncludesEveryFirstDecision(t *testing.T) {
    s := openTestStore(t)
    seedPaperOrders(t, s,
        paperOrder("market", "MARKET", "", true),
        paperOrder("ioc", "LIMIT", "IOC", true),
        paperOrder("fok", "LIMIT", "FOK", true),
        paperOrder("gtc-first", "LIMIT", "GTC", true),
        paperOrder("gtc-resting", "LIMIT", "GTC", false),
    )
    got, err := s.ListPaperMatchCandidates(context.Background(), 100)
    require.NoError(t, err)
    require.ElementsMatch(t,
        []string{"market", "ioc", "fok", "gtc-first", "gtc-resting"},
        orderIDs(got),
    )
}
```

候选 SQL 必须 join `t_trading_accounts`，只选 ENABLED PAPER + OPEN：

```sql
AND (
    o.c_first_match_pending = 1
    OR (o.c_order_type = 'LIMIT' AND o.c_time_in_force = 'GTC')
)
```

- [ ] **Step 2: 写 LIMIT 与首次决策表驱动测试**

```go
func TestMatchOrderPolicies(t *testing.T) {
    tests := []struct {
        name       string
        orderType  exchange.OrderType
        policy     exchange.FillPolicy
        marketable bool
        wantState  order.State
        wantRole   string
    }{
        {"market", exchange.OrderTypeMarket, exchange.FillPolicyUnspecified, true, order.Filled, "TAKER"},
        {"gtc immediate", exchange.OrderTypeLimit, exchange.FillPolicyGTC, true, order.Filled, "TAKER"},
        {"gtc rests", exchange.OrderTypeLimit, exchange.FillPolicyGTC, false, order.Open, ""},
        {"ioc immediate", exchange.OrderTypeLimit, exchange.FillPolicyIOC, true, order.Filled, "TAKER"},
        {"ioc cancels", exchange.OrderTypeLimit, exchange.FillPolicyIOC, false, order.Canceled, ""},
        {"fok immediate", exchange.OrderTypeLimit, exchange.FillPolicyFOK, true, order.Filled, "TAKER"},
        {"fok cancels", exchange.OrderTypeLimit, exchange.FillPolicyFOK, false, order.Canceled, ""},
    }
    runMatchCases(t, tests)
}
```

首次 MARKET 使用持久化 `paper_execution_price`；首次 LIMIT 使用提交报价。首次不成交 GTC 清除 `first_match_pending`，延迟穿价后使用新 quote 和 maker fee。

每个 Fill 在事务前确定 `price`、`quantity`、`liquidity_role`、`fee`、`fee_asset` 和 `realized_pnl`。Spot realized PnL 保持 0；Swap 按最新 Position 的 entry price 计算平仓部分，反向订单只对关闭旧方向的数量确认 realized PnL。

MARKET 在 Place 时缺少新鲜报价直接拒绝；延迟 GTC 缺少或拿到过期报价时保持 OPEN；IOC/FOK 只使用提交时已验证的新鲜报价完成首次成交或取消。

- [ ] **Step 3: 实现 QuoteKey 批量报价**

```go
type Matcher struct {
    Store      *store.Store
    Reducer    MatchReducer
    Executions ExecutionSource
    State      *MatcherState
    Now        func() time.Time
}

type MatcherState struct {
    ready atomic.Bool
    errMu sync.RWMutex
    lastError string
}
```

`Run` 持续运行 worker；`Scan` 扫描一轮候选订单，并按 `Exchange + MarketType + ExchangeSymbol` 建 QuoteKey。MARKET 和首次 LIMIT 不请求新报价；延迟 GTC 每个 QuoteKey 只请求一次，禁止仅以 symbol 缓存。

- [ ] **Step 4: 实现单事务 MatchOrder**

Paper ExchangeOrderID、ExchangeTradeID 和本地 FillID 由 TradingAccountID + client order ID 确定性生成：

```go
func paperIDs(tradingAccountID, clientOrderID string) (string, string, string) {
    sum := sha256.Sum256([]byte(tradingAccountID + "\x00" + clientOrderID))
    suffix := hex.EncodeToString(sum[:12])
    return "paper-order-" + suffix, "paper-trade-" + suffix, "paper-fill-" + suffix
}
```

报价决策在事务外完成；提交在一个 Store.Transaction 内：

```go
func (m *Matcher) MatchOrder(
    ctx context.Context,
    candidate store.OrderRecord,
    decision Decision,
) error {
    unlock := m.Store.LockTradingAccount(candidate.TradingAccountID)
    defer unlock()
    return m.Store.Transaction(ctx, func(tx *store.Tx) error {
        current, err := tx.GetOpenOrderForMatch(
            candidate.SpaceID,
            candidate.OrderID,
            candidate.Version,
        )
        if err != nil {
            return err
        }
        if decision.Rest {
            return tx.ClearFirstMatchPending(current, candidate.Version)
        }
        if decision.Cancel {
            return tx.CancelPaperOrder(current, candidate.Version, decision.Reason)
        }
        if current.ReduceOnly {
            ok, err := tx.CanFillReduceOnly(current)
            if err != nil {
                return err
            }
            if !ok {
                return tx.CancelPaperOrder(
                    current,
                    candidate.Version,
                    "paper reduce-only capacity changed",
                )
            }
        }
        _, err = m.Reducer.ApplyFillToOrderTx(
            tx,
            current,
            candidate.Version,
            decision.Fill,
            consumer.Source{
                SpaceID: current.SpaceID,
                TradingAccountID: current.TradingAccountID,
                Kind: consumer.OriginPaperMatcher,
            },
        )
        return err
    })
}
```

`GetOpenOrderForMatch` 必须约束 `state=OPEN AND version=expected`。CAS 不得先提交；Fill、Order、Position 和 reservation 中任一步失败时整个事务回滚。

- [ ] **Step 5: 固化 reduce-only 双重校验**

Place 测试：

```go
func TestPlaceRejectsAggregateReduceOnlyBeyondPosition(t *testing.T) {
    fixture := longPaperSwap(t, "1")
    fixture.placeReduceSell("0.8")
    _, err := fixture.placeReduceSell("0.8")
    require.ErrorIs(t, err, orderapp.ErrReduceOnly)
}
```

Match 测试：

```go
func TestMatchOrderCancelsReduceOnlyWhenPositionShrank(t *testing.T) {
    fixture := acceptedReduceOnly(t, "0.8", "1")
    fixture.setPosition("0.5")
    require.NoError(t, fixture.matcher.MatchOrder(
        context.Background(),
        fixture.order,
        fixture.fillDecision(),
    ))
    require.Equal(t, "CANCELED", fixture.storedOrder().State)
    require.Empty(t, fixture.fills())
    require.Equal(t, "0", fixture.storedOrder().RemainingReservedQuantity)
}
```

Match 使用最新实际仓位，只检查候选 remaining quantity，不重复扣候选自身。Paper 不做部分成交。

OrderService.Cancel 在读取和 BeginCancel 前获取同一 TradingAccount 锁；PaperAdapter 返回 CANCELED 后由 `ConfirmCancel` 事务释放 reservation。Matcher 已提交 Fill 时 Cancel 读取终态；Cancel 先提交时 Matcher 的 OPEN/version 校验失败。

- [ ] **Step 6: 写 CAS、故障注入与重启测试**

测试函数：

```go
func TestMatchCancelCASOnlyOneCommits(t *testing.T)
func TestMatchTransactionRollsBackFillOrderPositionAndReservation(t *testing.T)
func TestMatcherRecoversFirstDecisionAfterWakeLoss(t *testing.T)
func TestMatcherRecoversRestingGTCAfterRestart(t *testing.T)
func TestRepeatedMatcherTickCreatesOneFill(t *testing.T)
```

故障注入在 InsertFill、Position upsert 和 Order update 三处分别返回错误，断言数据库没有半提交事实。

- [ ] **Step 7: 实现唯一 PaperMatcher worker**

```go
type PaperMatcherWorker struct {
    Matcher  interface{ Scan(context.Context) error }
    Interval time.Duration
    wake     chan struct{}
    state    *paper.MatcherState
}
```

周期默认为一秒；wake channel 容量为 1，可合并、可丢失。worker 启动后 `matcher_ready=true`，意外退出立即 false 并记录 last error。Paper TradingSession 和 OrderService 都以该状态判断 Ready。

- [ ] **Step 8: Paper Session 改用 matcher readiness**

Paper 无 AccountEvents。启动顺序：

```text
LoadInstruments
rebuild AccountState from SQLite
verify matcher_ready
persist snapshot
SetReady(true)
```

matcher 退出时 Manager 的 `ReadyFor` 对 Paper 返回 false。Sampler degraded 不影响此判断。

- [ ] **Step 9: 删除旧执行接口与 Registry**

全部消费者切换到 `execution.ExecutionAdapter` 和 `ExecutionFactory` 后删除：

```text
modules/trade/internal/exchange/adapter.go
modules/trade/internal/exchange/registry.go
modules/trade/internal/exchange/registry_test.go
```

- [ ] **Step 10: 运行撮合与健康测试**

```bash
cd modules/trade
go test -count=1 ./internal/execution/paper ./internal/infra/store \
  ./internal/application/consumer ./internal/runtime ./internal/health \
  -run 'Paper|Match|Matcher|ReduceOnly|Readiness'
```

Expected: PASS。

- [ ] **Step 11: 提交原子撮合**

```bash
git add modules/trade/internal/execution/paper modules/trade/internal/infra/store \
  modules/trade/internal/application/consumer modules/trade/internal/runtime \
  modules/trade/internal/health modules/trade/internal/bootstrap \
  modules/trade/internal/exchange
git commit -m "feat(trade): match paper orders atomically"
```

---

### Task 8: 实现不可变 Paper 模拟生命周期

**Files:**
- Create: `modules/trade/internal/infra/store/paper_simulation.go`
- Create: `modules/trade/internal/infra/store/paper_simulation_test.go`
- Create: `modules/trade/internal/application/papersimulation/service.go`
- Create: `modules/trade/internal/application/papersimulation/service_test.go`
- Modify: `modules/trade/internal/application/account/service.go`
- Modify: `modules/trade/internal/application/account/service_test.go`
- Modify: `modules/trade/internal/application/logicalaccount/service.go`
- Modify: `modules/trade/internal/application/logicalaccount/service_test.go`
- Modify: `modules/trade/internal/runtime/manager.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: 写原子创建失败测试**

```go
func TestCreatePaperSimulationIsAtomic(t *testing.T) {
    service := newPaperSimulationFixture(t)
    got, err := service.Create(context.Background(), CreateCommand{
        SpaceID: "space-1",
        AccountName: "paper-account",
        LogicalAccountName: "paper-logical",
        Exchange: exchange.ExchangeBinance,
        MarketType: exchange.MarketTypeSpot,
        SettlementAsset: "USDT",
        InitialBalance: shared.MustDecimal("100000"),
        MakerFeeRate: shared.MustDecimal("0.001"),
        TakerFeeRate: shared.MustDecimal("0.002"),
        SlippageBPS: shared.MustDecimal("5"),
    })
    require.NoError(t, err)
    require.Equal(t, got.Account.ID, got.LogicalAccount.Members[0].TradingAccountID)
    require.Len(t, got.LogicalAccount.Members, 1)
}
```

再通过触发器让 LogicalAccount insert 失败，断言 TradingAccount 和 PaperConfig 同时回滚。

- [ ] **Step 2: 实现 CreatePaperSimulation**

一个 Store.Transaction 内依次：

```text
CreateTradingAccount(status=ENABLED, execution_mode=PAPER)
Insert immutable PaperConfig
CreateLogicalAccount(mode=PAPER, state=PAUSED)
Insert the only enabled member
```

Paper 不允许 Secret、初始持仓、第二成员或后续成员变更。

- [ ] **Step 3: 写关闭模拟失败测试**

```go
func TestClosePaperSimulationCancelsOrdersAndDisables(t *testing.T) {
    fixture := activePaperSimulation(t)
    fixture.openGTC("order-1")
    result, err := fixture.service.Close(context.Background(), "space-1", fixture.accountID)
    require.NoError(t, err)
    require.Equal(t, "DISABLED", result.Account.Status)
    require.Equal(t, "PAUSED", result.LogicalAccount.AutomationState)
    require.Equal(t, "CANCELED", fixture.order("order-1").State)
    require.Equal(t, "0", fixture.order("order-1").RemainingReservedQuantity)
}
```

另测重复 Close 幂等、Live account 拒绝、DISABLED Paper 不能重新 ENABLED。

- [ ] **Step 4: 实现 ClosePaperSimulation**

锁顺序固定：

```text
LockLogicalAccount
LockLogicalAccountExecution
LockTradingAccount
Store.Transaction
```

事务内取消全部非终态 Paper Order、释放 reservation、TradingAccount → DISABLED、LogicalAccount → PAUSED。迟到 target、ClaimOwner、Resume、Place、Submit 全部返回不可执行错误。

- [ ] **Step 5: 拒绝 Paper 通用修改**

`account.Service.Update` 对 Paper 拒绝名称、Secret、settlement、margin、sync symbols 和任何 `DISABLED -> ENABLED`。`logicalaccount.Service.AddMember/RemoveMember` 对 Paper 返回错误；Live 保持原有 PAUSED 成员管理。

- [ ] **Step 6: 运行生命周期测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/papersimulation \
  ./internal/application/account ./internal/application/logicalaccount \
  ./internal/infra/store ./internal/runtime \
  -run 'PaperSimulation|PaperMember|PaperAccount'
```

Expected: PASS。

- [ ] **Step 7: 提交 Paper 生命周期**

```bash
git add modules/trade/internal/application/papersimulation \
  modules/trade/internal/application/account \
  modules/trade/internal/application/logicalaccount \
  modules/trade/internal/infra/store modules/trade/internal/runtime \
  modules/trade/internal/bootstrap
git commit -m "feat(trade): manage immutable paper simulations"
```

---

### Task 9: 实现 Holding、双曲线与串行 EquitySampler

**Files:**
- Create: `modules/trade/internal/application/holding/service.go`
- Create: `modules/trade/internal/application/holding/service_test.go`
- Create: `modules/trade/internal/application/equity/service.go`
- Create: `modules/trade/internal/application/equity/service_test.go`
- Create: `modules/trade/internal/runtime/equity_sampler.go`
- Create: `modules/trade/internal/runtime/equity_sampler_test.go`
- Modify: `modules/trade/internal/application/consumer/fill.go`
- Modify: `modules/trade/internal/runtime/session.go`
- Modify: `modules/trade/internal/health/state.go`
- Modify: `modules/trade/internal/health/server_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/trade/config/trpc_go.yaml`

- [ ] **Step 1: 写 Spot Holding 与估值失败测试**

```go
func TestValueSpotAccountAndHoldings(t *testing.T) {
    fixture := spotEquityFixture(
        balance("USDT", "500"),
        balance("BTC", "0.01"),
        instrument("BTC-USDT-SPOT", "BTC", "USDT"),
        quote("50000"),
    )
    point, holdings, err := fixture.service.SampleAccount(context.Background(), fixture.accountID)
    require.NoError(t, err)
    require.Equal(t, "1000", point.Equity)
    require.Equal(t, "500", point.AvailableFunds)
    require.Equal(t, "0.01", holdings[0].Quantity.String())
    require.Equal(t, "500", holdings[0].MarketValue.String())
}
```

Live Spot 外部存量没有成本价时 `unrealized_pnl=nil`；Paper Spot 从 Fill 计算成本价。非零资产找不到结算币种报价时返回错误，不静默漏算。

- [ ] **Step 2: 写 Swap 与 LogicalAccount 曲线测试**

```go
func TestSampleLogicalAccountPersistsCurrentMembership(t *testing.T) {
    fixture := logicalEquityFixture(t, "account-1", "100", "account-2", "200")
    require.NoError(t, fixture.service.SampleLogicalAccount(
        context.Background(),
        fixture.logicalAccountID,
        fixture.bucket,
    ))
    point := fixture.logicalPoint()
    require.Equal(t, "300", point.Equity)
    fixture.removeMember("account-2")
    require.Equal(t, "300", fixture.logicalPoint().Equity)
}
```

任一启用成员缺点、Not Ready 或报价过期时不写 LogicalAccount 点。所有成员 `unrealized_pnl` 非 nil 才求和，否则逻辑点为 nil。

- [ ] **Step 3: 实现 Equity Service**

```go
type Service struct {
    Accounts    AccountSource
    Members     LogicalAccountSource
    Instruments InstrumentSource
    Quotes      QuoteSource
    Points      PointStore
    Now         func() time.Time
}

func minuteBucket(at time.Time) int64 {
    return at.UTC().Truncate(time.Minute).UnixMilli()
}
```

Account 点先持久化；随后读取采样时成员集合并写 LogicalAccount 点。查询 API 只读持久化曲线，不按当前成员回算历史。

- [ ] **Step 4: 写串行队列失败测试**

```go
func TestEquitySamplerCoalescesAllWakeSources(t *testing.T) {
    sampler := newSamplerFixture(t)
    sampler.Enqueue("account-1")
    sampler.Enqueue("account-1")
    sampler.EnqueueReadyAccounts(context.Background())
    require.NoError(t, sampler.RunPending(context.Background()))
    require.Equal(t, []string{"account-1"}, sampler.sampledIDs())
}
```

另测慢 timer、Fill wake、Session Ready 同时进入时不并发；一个账户失败不阻断下一账户；较旧 source_time 不能覆盖新点。

- [ ] **Step 5: 实现单 worker**

```go
type EquitySampler struct {
    Accounts AccountSource
    Service  AccountSampler
    signal   chan struct{}
    mu       sync.Mutex
    pending  map[string]struct{}
    degraded atomic.Bool
    lastError atomic.Value
}
```

Timer、Fill observer 和 Session Ready 只调用 Enqueue。worker 串行读取最新事实并按排序后的 account ID 采样。

- [ ] **Step 6: 注册 tRPC Timer**

在 `trpc_go.yaml` 新增：

```yaml
- name: trpc.moox.trade.equity_sample.timer
  port: 11210
  network: "0 * * * * *"
  protocol: timer
  timeout: 30000
```

不加 `?startAtOnce=1`。Bootstrap 使用：

```go
job, err := timerjob.New(
    "trade_equity_sample",
    30*time.Second,
    equitySampler.EnqueueReadyAccounts,
)
timer.RegisterHandlerService(
    serverInstance.Service("trpc.moox.trade.equity_sample.timer"),
    job.Handle,
)
```

- [ ] **Step 7: 接入 Fill/Ready wake 与健康**

Fill commit 成功后 enqueue TradingAccountID；Session 首次 Ready enqueue。Sampler degraded 只进入健康详情和指标，不使 Trade readiness 失败；PaperMatcher readiness 继续是硬门槛。

- [ ] **Step 8: 运行 Equity 与 Timer 测试**

```bash
cd modules/trade
go test -count=1 ./internal/application/holding ./internal/application/equity \
  ./internal/runtime ./internal/health ./internal/bootstrap \
  -run 'Holding|Equity|Timer|Readiness'
cd ../..
go test -count=1 ./packages/timerjob
```

Expected: PASS。

- [ ] **Step 9: 提交资金曲线**

```bash
git add modules/trade/internal/application/holding \
  modules/trade/internal/application/equity modules/trade/internal/runtime \
  modules/trade/internal/application/consumer modules/trade/internal/health \
  modules/trade/internal/bootstrap modules/trade/config/trpc_go.yaml
git commit -m "feat(trade): sample unified equity curves"
```

---

### Task 10: 收敛 TradeConsoleService、Proto、Admin 与 Strategy

**Files:**
- Modify: `modules/trade/proto/trade_service.proto`
- Modify: `modules/trade/proto/tradegen/validation.go`
- Modify: `modules/trade/proto/tradegen/logical_account_contract_test.go`
- Modify: `modules/trade/proto/tradegen/security_test.go`
- Create: `modules/trade/internal/rpc/console.go`
- Create: `modules/trade/internal/rpc/console_test.go`
- Create: `modules/trade/internal/rpc/capabilities.go`
- Create: `modules/trade/internal/rpc/capabilities_test.go`
- Modify: `modules/trade/internal/rpc/account.go`
- Modify: `modules/trade/internal/rpc/logical_account.go`
- Modify: `modules/trade/internal/rpc/execution.go`
- Modify: `modules/trade/internal/rpc/convert.go`
- Modify: `modules/trade/internal/rpc/register.go`
- Modify: `modules/trade/internal/rpc/register_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/config/trpc_go.yaml`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/acceptance_test.go`
- Modify: `modules/strategy/config/app.yaml`
- Modify: `modules/strategy/internal/bootstrap/config.go`
- Modify: `modules/strategy/internal/bootstrap/config_test.go`
- Modify: `modules/strategy/internal/bootstrap/logical_account.go`
- Modify: `modules/strategy/internal/bootstrap/logical_account_test.go`
- Modify: `modules/strategy/internal/bootstrap/logical_account_external_e2e_test.go`
- Modify: `config/setup/service-deployments.yaml`
- Modify: `scripts/test/e2e/test-strategy-trade-logical-account-e2e.sh`

- [ ] **Step 1: 写最终 Proto 契约失败测试**

```go
func TestTradeConsoleIsOnlyBusinessHTTPService(t *testing.T) {
    services := File_trade_service_proto.Services()
    require.NotNil(t, services.ByName("TradeConsoleService"))
    require.Nil(t, services.ByName("ExchangeAccountService"))
    require.Nil(t, services.ByName("TradingAccountService"))
    require.Nil(t, services.ByName("TradeExecutionService"))
    require.Nil(t, services.ByName("LogicalAccountService"))
}
```

另断言 `TradingAccount.trading_account_id`、`oneof execution_config`、Order/Fill/Position 双身份和 `Holding`、`EquityPoint` 存在。

- [ ] **Step 2: 定义最终账户与 Paper RPC**

```protobuf
message LiveConfig {
  AccountEnvironment environment = 1;
  string credential_secret_id = 2;
}

message PaperConfig {
  string initial_balance = 1;
  string maker_fee_rate = 2;
  string taker_fee_rate = 3;
  string slippage_bps = 4;
}

message TradingAccount {
  string trading_account_id = 1;
  string space_id = 2;
  string name = 3;
  Exchange exchange = 4;
  MarketType market_type = 5;
  ExecutionMode execution_mode = 6;
  string settlement_asset = 7;
  string margin_mode = 8;
  map<string, string> leverage_settings = 9;
  repeated string sync_symbols = 10;
  string status = 11;
  bool ready = 12;
  int64 last_sync_at = 13;
  int64 last_ready_at = 14;
  string last_error = 15;
  oneof execution_config {
    LiveConfig live = 16;
    PaperConfig paper = 17;
  }
  TradingAccountSnapshot snapshot = 18;
  int64 created_at = 19;
  int64 updated_at = 20;
}
```

`CreateTradingAccount` 只接收 LiveConfig。`CreatePaperSimulation` 同时接收账户名、LogicalAccount 名、Exchange、MarketType、settlement asset 和 PaperConfig。`ClosePaperSimulation` 接收 TradingAccountID，并返回账户与 LogicalAccount。

`AccountEnvironment` 只保留 UNSPECIFIED、TESTNET、PRODUCTION；删除 PAPER 枚举值。Paper 的行情环境由 `MarketDataEnvironment()` 固定映射为 PRODUCTION，不暴露为用户配置。

- [ ] **Step 3: 定义查询消息**

```protobuf
message Holding {
  string trading_account_id = 1;
  string instrument_id = 2;
  string exchange_symbol = 3;
  string asset = 4;
  string quantity = 5;
  string average_cost = 6;
  string mark_price = 7;
  string market_value = 8;
  optional string unrealized_pnl = 9;
  int64 source_time = 10;
}

message EquityPoint {
  int64 bucket_time = 1;
  string equity = 2;
  string available_funds = 3;
  string used_margin = 4;
  optional string unrealized_pnl = 5;
  int64 source_time = 6;
}

message ExecutionCapabilities {
  bool can_place_order = 1;
  string unavailable_reason = 2;
  repeated OrderType order_types = 3;
  repeated FillPolicy fill_policies = 4;
  bool can_close_paper_simulation = 5;
}
```

`QueryEquityCurveReq` 用 oneof 在 TradingAccountID 与 LogicalAccountID 中二选一，并带 start/end time。

- [ ] **Step 4: 定义单一 TradeConsoleService**

服务包含：

```text
CreateTradingAccount / UpdateTradingAccount / GetTradingAccount / ListTradingAccounts
SetLeverage / SyncTradingAccount
CreatePaperSimulation / ClosePaperSimulation
GetTradingAccountOverview / GetExecutionCapabilities / QueryEquityCurve
CreateLogicalAccount / GetLogicalAccount / ListLogicalAccounts / UpdateLogicalAccount
AddLogicalAccountMember / RemoveLogicalAccountMember
ClaimLogicalAccountOwner / ReleaseLogicalAccountOwner
PauseLogicalAccount / ResumeLogicalAccount / FlattenLogicalAccount
PlaceManualOrder / CancelOrder / GetOperatorAction / GetLogicalAccountTarget
GetOrder / ListOrders / ListFills / ListHoldings / ListPositions
```

`PlaceManualOrderReq`、订单和查询统一使用 canonical `instrument_id`；服务端返回 `exchange_symbol`。

- [ ] **Step 5: 重新生成并修复手写验证**

```bash
make -C modules/trade/proto all
cd modules/trade/proto/tradegen
go test -count=1 .
```

Expected: 生成成功；手写 validation 和 security tests 使用最终字段，不含 reserved 声明。

- [ ] **Step 6: 实现 ConsoleServer 组合与新查询**

现有 Handler 文件继续按账户、逻辑账户和执行职责保留；`ConsoleServer` 组合它们：

```go
type ConsoleServer struct {
    *AccountServer
    *LogicalAccountServer
    *ExecutionServer
    Paper        *papersimulation.Service
    Equity       EquityQuery
    Holdings     HoldingQuery
    Capabilities CapabilityQuery
}
```

`QueryEquityCurve` 只查持久化账户/LogicalAccount 曲线。`GetExecutionCapabilities` 对关闭的 Production 返回 `can_place_order=false`；不提供 EXIT_ONLY。`ClosePaperSimulation` 不出现在 Live capability 中。

- [ ] **Step 7: 收敛 tRPC 与部署**

`trpc_go.yaml` 只保留一个业务 HTTP 服务：

```yaml
- name: trpc.moox.trade.TradeConsoleService
  ip: 127.0.0.1
  port: 11200
  network: tcp
  protocol: http
  timeout: 15000
```

保留 DNS 11203、Health/metrics/equity timer 11210。Admin 默认部署以 `trade_console` 替换 `trade_exchange_account`、`trade_execution`、`trade_logical_account`；将三个旧名称加入 obsolete 列表。

- [ ] **Step 8: 更新 Strategy 客户端**

```go
client tradepb.TradeConsoleServiceClientProxy
```

默认 target 改为 `ip://127.0.0.1:11200`。Runner 仍只保存 `logical_account_id`，不增加 TradingAccountID。更新外部 RPC 测试服务名和注册方法。

- [ ] **Step 9: 运行协议、RPC、部署与 Strategy 测试**

```bash
cd modules/trade/proto/tradegen
go test -count=1 .
cd ../..
go test -count=1 ./internal/rpc ./internal/bootstrap
cd ../admin
go test -count=1 ./internal/service/sysdeploy/...
cd ../strategy
go test -count=1 ./internal/bootstrap/... ./internal/rpc/... ./internal/store/...
cd ../..
bash scripts/test/e2e/test-strategy-trade-logical-account-e2e.sh
```

Expected: PASS。

- [ ] **Step 10: 提交单一 Console**

```bash
git add modules/trade/proto modules/trade/internal/rpc \
  modules/trade/internal/bootstrap modules/trade/config/trpc_go.yaml \
  modules/admin/internal/service/sysdeploy modules/strategy/config \
  modules/strategy/internal/bootstrap config/setup/service-deployments.yaml \
  scripts/test/e2e/test-strategy-trade-logical-account-e2e.sh
git commit -m "refactor(trade): expose one console service"
```

---

### Task 11: 实现 Live/Paper 共用前端

**Files:**
- Create: `web/src/views/trading/account-overview/account-form.ts`
- Create: `web/src/views/trading/account-overview/account-form.test.ts`
- Create: `web/src/views/trading/logical-accounts/equity-curve.ts`
- Create: `web/src/views/trading/logical-accounts/equity-curve.test.ts`
- Create: `web/src/views/trading/logical-accounts/equity-curve.vue`
- Modify: `web/src/api/trade/http.ts`
- Modify: `web/src/api/trade/types.ts`
- Modify: `web/src/api/trade/index.ts`
- Modify: `web/src/api/trade/trade.test.ts`
- Modify: `web/src/views/trading/account-overview/account-overview.vue`
- Modify: `web/src/views/trading/logical-accounts/index.vue`
- Modify: `web/src/views/trading/trade-record/trade-record.vue`
- Modify: `web/src/views/trading/position-detail/position-detail.vue`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`
- Modify: `web/tests/strategy-console.spec.ts`

- [ ] **Step 1: 写单服务 API 失败测试**

```ts
it("uses one Trade console service", () => {
  expect(trade.tradeServiceMap).toEqual({ console: "trade_console" });
});

it("submits canonical instrument IDs", async () => {
  await trade.placeManualOrder({
    action_id: "action-1",
    trading_account_id: "account-1",
    client_order_id: "client-1",
    instrument_id: "BTC-USDT-SPOT",
    order_type: 1,
    fill_policy: 0,
    side: 1,
    position_side: 0,
    quantity: "0.01",
    reason: "manual",
  });
  expect(callTrade).toHaveBeenCalledWith("console", "PlaceManualOrder", expect.objectContaining({
    instrument_id: "BTC-USDT-SPOT",
  }));
});
```

Run:

```bash
cd web
pnpm exec vitest run --config vitest.config.ts src/api/trade/trade.test.ts
```

Expected: FAIL，仍使用三服务和 exchange_account_id/symbol。

- [ ] **Step 2: 重写 API 类型与方法**

`tradeServiceMap`：

```ts
export const tradeServiceMap = { console: "trade_console" } as const;
```

公开方法固定为：

```text
createTradingAccount
createPaperSimulation
closePaperSimulation
getTradingAccount / listTradingAccounts / syncTradingAccount
getTradingAccountOverview / getExecutionCapabilities
queryEquityCurve
listOrders / listFills / listHoldings / listPositions
placeManualOrder / cancelOrder
全部 LogicalAccount 生命周期方法
```

删除 AccountEnvironment 的 Paper 值；TradingAccount 使用 `live?` / `paper?` 互斥类型。

- [ ] **Step 3: 写账户请求构造测试**

```ts
it("builds live and paper requests without mixed config", () => {
  expect(buildLiveRequest(liveForm())).toEqual({
    name: "Live",
    exchange: 1,
    market_type: 1,
    settlement_asset: "USDT",
    live: { environment: 1, credential_secret_id: "secret-1" },
    sync_symbols: ["BTCUSDT"],
  });
  expect(buildPaperSimulationRequest(paperForm())).toEqual({
    account_name: "Paper Account",
    logical_account_name: "Paper Logical",
    exchange: 1,
    market_type: 1,
    settlement_asset: "USDT",
    paper: {
      initial_balance: "100000",
      maker_fee_rate: "0.001",
      taker_fee_rate: "0.002",
      slippage_bps: "5",
    },
  });
});
```

- [ ] **Step 4: 改造账户与模拟创建 UI**

`account-overview.vue` 标题改为 Trading Account。新增按钮根据模式调用：

```text
Live:
  CreateTradingAccount
  environment TESTNET/PRODUCTION
  credential secret
  sync symbols

Paper:
  CreatePaperSimulation
  account name + logical account name
  initial balance
  maker/taker fee
  slippage bps
```

Paper 表单不显示 Secret 或 PAPER environment；Live 表单不显示 PaperConfig。

- [ ] **Step 5: 实现模拟关闭交互**

LogicalAccount 详情中仅对 ENABLED Paper 显示“结束模拟”。如果有 owner，按钮禁用并提示先停用或改绑 Runner；确认后调用 ClosePaperSimulation。关闭后成员、人工下单、Resume 按钮全部禁用，历史 Order/Fill/curve 保留。

- [ ] **Step 6: 实现 capabilities 驱动下单**

打开手工下单时获取 `GetExecutionCapabilities`：

```text
can_place_order=false -> 禁用提交并显示 unavailable_reason
order_types -> 决定 MARKET/LIMIT
fill_policies -> 决定 GTC/IOC/FOK
instrument_id -> 唯一提交字段
SPOT -> position_side 不发送
SWAP -> position_side=NET
```

- [ ] **Step 7: 实现 Holding/Position 分视图**

`position-detail.vue` 根据 account.market_type：

```text
SPOT -> ListHoldings，显示 asset/quantity/average cost/mark value/unrealized PnL
SWAP -> ListPositions，显示 signed quantity/entry/mark/leverage/margin/PnL
```

Order/Fill 页面同时显示 canonical instrument ID 和原生 exchange symbol，筛选只发送 instrument_id。

- [ ] **Step 8: 实现资金曲线组件**

转换函数：

```ts
export function toEquitySeries(points: EquityPoint[]) {
  return [...points]
    .sort((a, b) => Number(a.bucket_time) - Number(b.bucket_time))
    .map(point => ({ time: Number(point.bucket_time), value: Number(point.equity) }));
}
```

VChart 使用 time x-axis 和 settlement asset y-axis。组件不判断 Live/Paper。LogicalAccount 详情查询持久化 logical curve。

- [ ] **Step 9: 更新 Vitest 与 Playwright**

测试：

```text
API 只使用 trade_console
Live/Paper 请求互斥
资金点排序和 nullable unrealized_pnl
Paper 关闭前提示 Runner
关闭后历史仍显示
SPOT 使用 Holding，SWAP 使用 Position
Production gate 原因可见
```

Playwright mock 路径统一改为 `**/api/admin/trade_console/*`，删除 Reset mock。

- [ ] **Step 10: 运行前端验证**

```bash
cd web
pnpm exec vitest run --config vitest.config.ts \
  src/api/trade/trade.test.ts \
  src/views/trading/account-overview/account-form.test.ts \
  src/views/trading/logical-accounts/equity-curve.test.ts
pnpm exec playwright test tests/strategy-console.spec.ts
pnpm build:prod
```

Expected: PASS。

- [ ] **Step 11: 提交共用前端**

```bash
git add web/src/api/trade web/src/views/trading web/src/lang \
  web/tests/strategy-console.spec.ts
git commit -m "feat(web): unify live and paper trading views"
```

---

### Task 12: 完成跨模式 E2E、文档、Testnet 与破坏式 cutover

**Files:**
- Create: `modules/trade/test/live_paper_parity_e2e_test.go`
- Create: `modules/trade/test/paper_matcher_restart_e2e_test.go`
- Create: `modules/trade/test/equity_sampler_e2e_test.go`
- Create: `modules/trade/test/close_paper_simulation_e2e_test.go`
- Modify: `modules/trade/test/e2e_helpers_test.go`
- Modify: `modules/trade/test/spot_market_e2e_test.go`
- Modify: `modules/trade/test/swap_execution_e2e_test.go`
- Modify: `modules/trade/test/strategy_target_e2e_test.go`
- Modify: `modules/trade/test/strategy_target_external_e2e_test.go`
- Modify: `modules/trade/test/logical_account_operator_e2e_test.go`
- Modify: `modules/trade/test/paper_swap_cursor_regression_test.go`
- Modify: `modules/strategy/test/strategy_trade_external_e2e_test.go`
- Modify: `scripts/test/e2e/test-strategy-trade-event-e2e.sh`
- Modify: `modules/trade/cmd/testnet-smoke/harness.go`
- Modify: `modules/trade/cmd/testnet-smoke/runtime.go`
- Modify: `modules/trade/cmd/testnet-smoke/private_probe.go`
- Modify: `modules/trade/README.md`
- Modify: `docs/交易模块架构设计.md`
- Modify: `docs/交易模块功能说明.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/运维/MooX-Trade运维.md`
- Modify: `docs/运维/MooX-EventBus运维.md`

- [ ] **Step 1: 写 Spot/Swap 跨模式同构 E2E**

```go
func TestLiveAndPaperProduceEquivalentSpotFacts(t *testing.T) {
    live := runTargetScenario(t, exchange.ExecutionModeLive, exchange.MarketTypeSpot)
    paper := runTargetScenario(t, exchange.ExecutionModePaper, exchange.MarketTypeSpot)
    require.Equal(t, live.OrderShape(), paper.OrderShape())
    require.Equal(t, live.FillShape(), paper.FillShape())
    require.Equal(t, live.HoldingShape(), paper.HoldingShape())
    require.NotEmpty(t, live.EquityPoints)
    require.NotEmpty(t, paper.EquityPoints)
}

func TestLiveAndPaperProduceEquivalentSwapFacts(t *testing.T) {
    live := runTargetScenario(t, exchange.ExecutionModeLive, exchange.MarketTypeSwap)
    paper := runTargetScenario(t, exchange.ExecutionModePaper, exchange.MarketTypeSwap)
    require.Equal(t, live.OrderShape(), paper.OrderShape())
    require.Equal(t, live.FillShape(), paper.FillShape())
    require.Equal(t, live.PositionShape(), paper.PositionShape())
}
```

两种场景都从同一个 Strategy FULL target 进入 TargetExecutor 和 OrderService；禁止测试直接调用 PaperMatcher 绕过主流程。

- [ ] **Step 2: 写恢复、曲线和关闭 E2E**

固定流程：

```text
Paper GTC:
  Place -> OPEN -> close runtime/store -> reopen same SQLite
  set crossing quote -> matcher -> FILLED
  assert exactly one Fill

Equity:
  Session Ready enqueue -> one point
  Fill commit enqueue -> same minute newer point
  old source_time cannot overwrite

Close:
  open GTC -> ClosePaperSimulation
  assert CANCELED + reservation zero + account DISABLED + logical PAUSED
  restart -> no session/matcher/sampler work for closed account
```

- [ ] **Step 3: 更新 JetStream 外部 E2E**

使用新 TradeConsole 端口和 TradingAccount 字段。MOOX_TRADE 仍只有
`moox.event.trade.target.requested.v1.>`，durable 仍为 `trade_target_v1`。测试 Strategy outbox publish、Trade consume、ack、target convergence。

- [ ] **Step 4: 运行真实 Testnet smoke**

凭据和明确确认：

```bash
export MOOX_TRADE_TESTNET_CONFIRM=YES
export MOOX_BINANCE_TESTNET_SECRET_ID=<admin-secret-id>
export MOOX_OKX_TESTNET_SECRET_ID=<admin-secret-id>
export MOOX_BINANCE_TESTNET_SYMBOL=BTCUSDT
export MOOX_OKX_TESTNET_SYMBOL=BTC-USDT
export MOOX_TRADE_TESTNET_MAX_NOTIONAL=20
bash modules/trade/scripts/testnet-smoke.sh
```

必须覆盖 submit、query、account event stream、sync、restart、cleanup。该命令需要真实凭据，不加入无凭据 CI。

- [ ] **Step 5: 更新现役文档**

文档明确：

```text
单 Trade 进程 / 单 SQLite
TradingAccount oneof LiveConfig/PaperConfig
CreateTradingAccount 仅 Live
CreatePaperSimulation / ClosePaperSimulation
一个执行内核 + LiveAdapter/PaperAdapter
PaperMatcher 原子 MatchOrder
ReservationFacts 同事务 + reduce-only 容量
Holding/Position 分读模型
双持久化资金曲线 + tRPC Timer
Production 无 EXIT_ONLY
不支持 Reset、Archive、部分成交模拟或兼容迁移
```

- [ ] **Step 6: 写破坏式 cutover runbook**

`docs/运维/MooX-Trade运维.md` 加入：

```text
1. live_trading_enabled=true，停 Runner，Pause LogicalAccount
2. cancel Production orders，清理所有非零敞口
3. 等待 Strategy pending outbox 为空
4. 停 Strategy/Trade/outbox relay
5. 成对备份 Trade SQLite、Strategy SQLite、旧二进制、旧 Web
6. 删除并重建 Trade/Strategy SQLite
7. 删除 MOOX_TRADE Stream；EventBus 重建空 Stream
8. 启动 Trade，自动创建 trade_target_v1 consumer
9. 部署同版本 Strategy/Admin/Web，保持 live_trading_enabled=false
10. 手工重建 TradingAccount、PaperSimulation、Strategy、Runner 和 logical_account_id
11. 运行 Paper/mock Live/JetStream/Testnet/Web 验收
12. 验收通过后开启 Production
```

回滚只允许在新版本尚未产生 Production Order 时恢复成对备份；一旦产生 Production Order，只允许关闭下单、撤单、交易所对账并向前修复。

- [ ] **Step 7: 执行残留扫描**

```bash
rg -n 'ExchangeAccount|exchange_account_id|t_exchange_accounts|t_exchange_positions|paper_initial_balance|ResetPaper|portfolio\.go|SubscribePrivate|trade_exchange_account|trade_execution|trade_logical_account' \
  --glob '*.go' --glob '*.proto' --glob '*.sql' --glob '*.yaml' --glob '*.yml' \
  --glob '*.ts' --glob '*.vue' --glob '*.sh' \
  modules/trade modules/strategy/internal/bootstrap web/src/api/trade \
  web/src/views/trading modules/admin/internal/service/sysdeploy \
  config/setup/service-deployments.yaml scripts/test/e2e
```

Expected: 无输出。历史 `docs/superpowers/` 不纳入扫描。

- [ ] **Step 8: 运行 Trade 全量门禁**

```bash
cd modules/trade
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

Expected: PASS。

- [ ] **Step 9: 运行跨模块与前端门禁**

```bash
cd ../strategy
go test -count=1 ./...
cd ../admin
go test -count=1 ./internal/service/sysdeploy/...
cd ../../web
pnpm test
pnpm exec playwright test tests/strategy-console.spec.ts
pnpm build:prod
```

Expected: PASS。

- [ ] **Step 10: 运行仓库级最终验证**

在所有任务已提交且 worktree 干净后执行：

```bash
cd ..
make proto-check
go run ./scripts/check/check-trade-exchange-terminology.go
make check-boundaries
bash scripts/check/verify-event-contracts.sh
git diff --check
make verify
```

Expected:

```text
proto-check PASS
trade Exchange terminology passed
check-boundaries PASS
event contracts PASS
git diff --check 无输出
make verify PASS
```

- [ ] **Step 11: 提交 E2E 与运维文档**

```bash
git add modules/trade/test modules/trade/cmd/testnet-smoke \
  modules/strategy/test scripts/test/e2e \
  modules/trade/README.md docs/交易模块架构设计.md \
  docs/交易模块功能说明.md docs/架构总览.md \
  docs/运维/MooX-Trade运维.md docs/运维/MooX-EventBus运维.md
git commit -m "test(trade): verify unified execution end to end"
```

---

## 三、最终验收清单

- [ ] Live/Paper 的 Strategy target 和人工订单都进入同一个 OrderService。
- [ ] 核心 Order/Fill/Holding/Position reducer 不包含 `execution_mode` 分支。
- [ ] PaperConfig 按 TradingAccount 持久化且没有更新、Reset 或删除入口。
- [ ] Paper MARKET 使用持久化成交价、滑点和 taker fee。
- [ ] Paper LIMIT 立即成交为 taker；延迟 GTC 为 maker；IOC/FOK 不可全量成交则整单取消。
- [ ] first match wake 丢失和 OPEN GTC 重启后都能恢复。
- [ ] Match/CAS/Fill/Position/reservation 在一个 SQLite 事务中提交或回滚。
- [ ] Paper available 已扣全部活动 reservation，且不会再叠加 Live unreflected 算法。
- [ ] 多个 reduce-only 活动订单合计不能超过仓位；Match 前仓位不足时整单取消。
- [ ] Binance SPOT/SWAP 相同 native symbol 使用不同 QuoteKey 和行情接口。
- [ ] Live AccountEventSource 完成订阅、快照、缓冲回放后才 Ready，断线立即 Not Ready。
- [ ] PaperMatcher 停止后 Paper Not Ready；EquitySampler degraded 不阻止交易。
- [ ] Account 与 LogicalAccount 曲线持久化；后续成员变化不改写历史。
- [ ] Paper `ENABLED -> DISABLED` 不可逆，关闭后历史仍可查。
- [ ] `live_trading_enabled=false` 拒绝 Production Place/Submit，仍允许同步、查单和撤单。
- [ ] Browser 只访问 `trade_console`，SPOT 展示 Holding，SWAP 展示 Position。
- [ ] Trade/Strategy 破坏式 cutover、Testnet smoke 和全仓门禁均有可执行记录。

## 四、执行交接

计划实施时优先选择：

1. **Subagent-Driven（推荐）**：每个 Task 使用新的实现 worker，任务后先做 spec compliance review，再做 code quality review。
2. **Inline Execution**：使用 executing-plans，每完成一个 PR 阶段停下检查测试结果和 diff。

开始实施前必须从干净 worktree 创建 Task 1 分支；本计划本身不授权在当前脏工作区开始编码。
