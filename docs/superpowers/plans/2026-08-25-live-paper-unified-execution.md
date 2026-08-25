# Trade 实盘与模拟盘统一执行 Implementation Plan

> **状态：已失效，禁止执行。** 设计第二版已删除 Reset，并新增 ReservationPolicy、
> Paper 原子撮合、LogicalAccount 持久曲线和 Production 安全闸门；本计划必须重新生成。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在单个 Trade 进程和单个 SQLite 中实现共用交易内核，通过 `LiveAdapter` 真实下单、通过 `PaperAdapter` 虚拟撮合，并向两种模式提供相同的订单、成交、持仓、资金曲线、API 和前端页面。

**Architecture:** 应用层只依赖 `execution.ExecutionAdapter` 和 `execution.MarketDataSource`。现有 Binance/OKX 实现直接满足 Live Adapter 契约；Paper Adapter 使用真实公共行情、共享订单事实和进程内 `PaperMatcher` 产生相同的 Order/Fill 回报。`ExecutionFactory` 是唯一允许根据 `execution_mode` 分流的装配点。

**Tech Stack:** Go 1.25、tRPC-Go、Protocol Buffers、SQLite、GORM、Vue 3、TypeScript、Arco Design、VChart、Vitest、Playwright。

---

## 一、实施约束

1. 不兼容旧 Proto、旧表、旧字段、旧服务名或旧页面请求。
2. 不增加独立 Paper 服务、消息流、双式账本、盘口深度或部分成交。
3. `Exchange` 仍表示 Binance/OKX；交易域禁止使用 `provider`、`broker`、`venue`、`platform` 作为 Exchange 同义词。
4. Live/Paper 分支只允许出现在 `ExecutionFactory` 和模式相关配置展示中。
5. 每个任务先写失败测试，确认失败原因，再实现最小代码。
6. 每个任务完成后独立提交；任何提交不得留下编译失败或生成代码未同步。

## 二、文件结构锁定

### 2.1 新增文件

- `modules/trade/schema/paper_account_config.sql`：Paper 账户初始资金、maker/taker 费率和滑点。
- `modules/trade/schema/equity.sql`：Live/Paper 共用账户资金曲线。
- `modules/trade/internal/infra/store/paper_account_config.go`：PaperConfig 读写。
- `modules/trade/internal/infra/store/paper_account_config_test.go`：PaperConfig 约束与 CRUD。
- `modules/trade/internal/infra/store/equity_point.go`：资金点 upsert 和时间范围查询。
- `modules/trade/internal/infra/store/equity_point_test.go`：分钟覆盖和排序测试。
- `modules/trade/internal/infra/store/paper_reset.go`：Paper LogicalAccount 原子重置。
- `modules/trade/internal/infra/store/paper_reset_test.go`：重置原子性与事实清理测试。
- `modules/trade/internal/execution/adapter.go`：统一执行接口。
- `modules/trade/internal/execution/marketdata.go`：`MarketQuote` 和公共行情接口。
- `modules/trade/internal/execution/factory.go`：Live/Paper 装配。
- `modules/trade/internal/execution/factory_test.go`：模式分派测试。
- `modules/trade/internal/execution/adapter_contract_test.go`：Live fake 与 Paper 共用执行契约。
- `modules/trade/internal/execution/paper/adapter.go`：Paper 执行入口和私有回报通道。
- `modules/trade/internal/execution/paper/adapter_test.go`：下单、撤单和回报测试。
- `modules/trade/internal/execution/paper/portfolio.go`：Spot/Swap 资金、持仓、手续费计算。
- `modules/trade/internal/execution/paper/portfolio_test.go`：资金与持仓数学测试。
- `modules/trade/internal/execution/paper/matcher.go`：MARKET 和简化 LIMIT 撮合。
- `modules/trade/internal/execution/paper/matcher_test.go`：GTC/IOC/FOK、滑点、费率和重启测试。
- `modules/trade/internal/application/equity/service.go`：账户估值和资金点写入。
- `modules/trade/internal/application/equity/service_test.go`：Spot/Swap 共用估值测试。
- `modules/trade/internal/runtime/equity_worker.go`：分钟采样和 Fill wake。
- `modules/trade/internal/runtime/equity_worker_test.go`：调度、防重入和单账户失败隔离。
- `modules/trade/internal/application/operator/reset_paper.go`：Paper LogicalAccount 重置用例。
- `modules/trade/internal/application/operator/reset_paper_test.go`：前置条件、幂等和恢复测试。
- `modules/trade/internal/rpc/console.go`：统一 `TradeConsoleService`。
- `modules/trade/internal/rpc/console_test.go`：Capabilities、EquityCurve 和 Reset RPC。
- `web/src/views/trading/account-overview/account-form.ts`：Live/Paper 创建请求构造与校验。
- `web/src/views/trading/account-overview/account-form.test.ts`：oneof 请求测试。
- `web/src/views/trading/logical-accounts/equity-curve.vue`：共用资金曲线。
- `web/src/views/trading/logical-accounts/equity-curve.ts`：曲线数据转换。
- `web/src/views/trading/logical-accounts/equity-curve.test.ts`：曲线排序和空值测试。

### 2.2 主要修改文件

- `modules/trade/proto/trade_service.proto`
- `modules/trade/proto/tradegen/validation.go`
- `modules/trade/proto/tradegen/security_test.go`
- `modules/trade/proto/tradegen/logical_account_contract_test.go`
- `modules/trade/schema/account.sql`
- `modules/trade/schema/instrument.sql`
- `modules/trade/schema/logical_account.sql`
- `modules/trade/schema/execution.sql`
- `modules/trade/schema/schema.go`
- `modules/trade/schema/schema_test.go`
- `modules/trade/internal/domain/exchangeaccount/*`（迁移为 `domain/tradingaccount/*`）
- `modules/trade/internal/domain/order/*`
- `modules/trade/internal/domain/logicalaccount/*`
- `modules/trade/internal/domain/operator/*`
- `modules/trade/internal/infra/store/account.go`
- `modules/trade/internal/infra/store/fact.go`
- `modules/trade/internal/infra/store/logical_account.go`
- `modules/trade/internal/infra/store/reservation.go`
- `modules/trade/internal/infra/store/store.go`
- `modules/trade/internal/application/account/*`
- `modules/trade/internal/application/order/*`
- `modules/trade/internal/application/accountsync/*`
- `modules/trade/internal/application/consumer/fill.go`
- `modules/trade/internal/application/operator/*`
- `modules/trade/internal/application/target/*`
- `modules/trade/internal/runtime/manager.go`
- `modules/trade/internal/runtime/session.go`
- `modules/trade/internal/bootstrap/bootstrap.go`
- `modules/trade/internal/bootstrap/bootstrap_test.go`
- `modules/trade/internal/rpc/account.go`
- `modules/trade/internal/rpc/execution.go`
- `modules/trade/internal/rpc/logical_account.go`
- `modules/trade/internal/rpc/convert.go`
- `modules/trade/internal/rpc/register.go`
- `modules/trade/config/app.yaml`
- `modules/trade/config/trpc_go.yaml`
- `modules/admin/internal/service/sysdeploy/defaults.go`
- `modules/admin/internal/service/sysdeploy/defaults_test.go`
- `modules/admin/internal/service/sysdeploy/acceptance_test.go`
- `modules/strategy/config/app.yaml`
- `modules/strategy/internal/bootstrap/config.go`
- `modules/strategy/internal/bootstrap/logical_account.go`
- `examples/setup/default/service-deployments.yaml`
- `scripts/tests/e2e/test-strategy-trade-logical-account-e2e.sh`
- `web/src/api/trade/http.ts`
- `web/src/api/trade/types.ts`
- `web/src/api/trade/index.ts`
- `web/src/api/trade/trade.test.ts`
- `web/src/views/trading/account-overview/account-overview.vue`
- `web/src/views/trading/logical-accounts/index.vue`
- `web/src/views/trading/trade-record/trade-record.vue`
- `web/src/views/trading/position-detail/position-detail.vue`
- `web/tests/strategy-console.spec.ts`

### 2.3 删除文件

- `modules/trade/internal/exchange/adapter.go`
- `modules/trade/internal/exchange/registry.go`
- `modules/trade/internal/exchange/registry_test.go`
- `modules/trade/internal/exchange/paper/paper.go`
- `modules/trade/internal/exchange/paper/paper_test.go`

Binance/OKX 代码保留在 `internal/exchange/binance` 和 `internal/exchange/okx`，不做无收益目录搬迁。

## 三、任务依赖

```text
Task 1 账户术语绿场重命名
  -> Task 2 PaperConfig / Equity Schema 与 Store
  -> Task 3 ExecutionAdapter / MarketDataSource
  -> Task 4 Paper Portfolio
  -> Task 5 Paper Matcher
  -> Task 6 应用层与 TradingSession 接线
  -> Task 7 EquitySampler
  -> Task 8 ResetPaperLogicalAccount
  -> Task 9 TradeConsoleService 与部署收敛
  -> Task 10 共用前端
  -> Task 11 E2E、文档与最终门禁
```

建议按依赖拆成四个顺序 PR，避免单个 PR 同时承载协议、撮合和前端：

```text
PR 1: Task 1-3  账户、持久化、ExecutionAdapter 基础
PR 2: Task 4-6  Paper Portfolio、Matcher、共用订单接线
PR 3: Task 7-9  EquitySampler、Reset、TradeConsoleService
PR 4: Task 10-11 共用前端、跨模式 E2E、文档与门禁
```

后一 PR 以上一 PR 的远程分支为基线；不把四组未审查改动堆在同一 PR。

---

### Task 1: 绿场重命名 TradingAccount

**Files:**
- Move: `modules/trade/internal/domain/exchangeaccount/account.go` → `modules/trade/internal/domain/tradingaccount/account.go`
- Move: `modules/trade/internal/domain/exchangeaccount/account_test.go` → `modules/trade/internal/domain/tradingaccount/account_test.go`
- Modify: `modules/trade/proto/trade_service.proto`
- Modify: `modules/trade/proto/tradegen/validation.go`
- Modify: `modules/trade/proto/tradegen/logical_account_contract_test.go`
- Modify: `modules/trade/proto/tradegen/security_test.go`
- Modify: `modules/trade/schema/account.sql`
- Modify: `modules/trade/schema/logical_account.sql`
- Modify: `modules/trade/schema/execution.sql`
- Modify: `modules/trade/schema/schema_test.go`
- Modify: `modules/trade/internal/infra/store/*.go`
- Modify: `modules/trade/internal/application/**/*.go`
- Modify: `modules/trade/internal/runtime/*.go`
- Modify: `modules/trade/internal/health/state.go`
- Modify: `modules/trade/internal/health/state_test.go`
- Modify: `modules/trade/internal/rpc/*.go`
- Modify: `modules/trade/internal/exchange/**/*.go`
- Modify: `modules/trade/test/*.go`
- Modify: `modules/trade/cmd/testnet-smoke/*.go`
- Modify: `web/src/api/trade/types.ts`
- Modify: `web/src/views/trading/**/*.vue`

- [ ] **Step 1: 先把 Schema 测试改成最终身份**

在 `modules/trade/schema/schema_test.go` 中把账户身份断言改为：

```go
func TestAllSQLUsesTradingAccountIdentity(t *testing.T) {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.Exec(AllSQL()).Error)

    require.True(t, hasUniqueIndex(
        t,
        db,
        "t_trading_accounts",
        []string{"c_space_id", "c_trading_account_id"},
    ))
    require.True(t, hasUniqueIndex(
        t,
        db,
        "t_trade_orders",
        []string{"c_space_id", "c_trading_account_id", "c_client_order_id"},
    ))
}
```

- [ ] **Step 2: 运行 Schema 测试并确认旧表名导致失败**

Run:

```bash
cd modules/trade
go test -count=1 ./schema -run 'TestAllSQLUsesTradingAccountIdentity|TestAllSQLCreates'
```

Expected: FAIL，错误包含 `t_trading_accounts` 不存在。

- [ ] **Step 3: 完成数据库身份的机械重命名**

执行以下一一映射，不保留别名：

```text
t_exchange_accounts       -> t_trading_accounts
c_exchange_account_id     -> c_trading_account_id
t_exchange_positions      -> t_trading_positions
ExchangeAccountRecord     -> TradingAccountRecord
ExchangeAccountSnapshot   -> TradingAccountSnapshot
ExchangeAccountID         -> TradingAccountID
LockExchangeAccount       -> LockTradingAccount
GetExchangeAccount        -> GetTradingAccount
GetExchangeAccountByID    -> GetTradingAccountByID
ListExchangeAccounts      -> ListTradingAccounts
ListEnabledExchangeAccounts -> ListEnabledTradingAccounts
```

同步更新四个 schema 文件中的主键、外键、索引名和 `store.validateExistingTradeSchema` 的 approved 表。

- [ ] **Step 4: 固化 canonical instrument 与 Exchange symbol 双身份**

先在 `schema/schema_test.go` 增加测试：同一 Binance SPOT symbol 可以在 TESTNET 与
PRODUCTION 保存不同规则；Order/Fill/Position 均同时拥有 canonical instrument 和原生 symbol。

最终持久化字段：

```text
t_exchange_instruments:
  c_exchange
  c_environment
  c_market_type
  c_exchange_symbol
  c_instrument_id

t_trade_orders / t_order_fills / t_trading_positions:
  c_instrument_id
  c_exchange_symbol
```

`t_exchange_instruments` 主键固定为：

```sql
PRIMARY KEY (
    c_exchange,
    c_environment,
    c_market_type,
    c_exchange_symbol
)
```

领域订单规格固定为：

```go
type ClientOrderSpec struct {
    TradingAccountID string
    ClientOrderID    string
    InstrumentID     string
    ExchangeSymbol   string
    Side             exchange.Side
    PositionSide     exchange.PositionSide
    Type             exchange.OrderType
    FillPolicy       exchange.FillPolicy
    Quantity         shared.Decimal
    LimitPrice       *shared.Decimal
}
```

应用层只接收 canonical `InstrumentID`；Validator 根据 TradingAccount 的 Exchange、
行情环境和 MarketType 解析 `ExchangeSymbol`。ExecutionAdapter 只接收原生 symbol。
Paper 的行情环境在 Task 2 固定映射为 PRODUCTION。

- [ ] **Step 5: 迁移 Domain 包和应用层字段**

最终 Domain 结构必须为：

```go
package tradingaccount

type Account struct {
    ID                 string
    SpaceID            string
    Name               string
    Exchange           exchange.Exchange
    MarketType         exchange.MarketType
    ExecutionMode      exchange.ExecutionMode
    Environment        exchange.AccountEnvironment
    CredentialSecretID string
    SettlementAsset    string
    MarginMode         exchange.MarginMode
    Status             exchange.AccountStatus
    Ready              bool
    SyncSymbols        []string
    LeverageSettings   map[string]shared.Decimal
    Snapshot           exchange.AccountSnapshot
    SnapshotSourceTime time.Time
    LastSyncAt         time.Time
    LastReadyAt        time.Time
    LastError          string
}
```

把 `domain/order.OrderSpec`、`LogicalAccountMember`、Operator、Target、AccountSync、runtime 和 Exchange 实现中的账户 ID 字段统一改为 `TradingAccountID`。

- [ ] **Step 6: 修改 Proto 契约测试并确认失败**

在 `logical_account_contract_test.go` 增加：

```go
func TestTradingAccountServiceUsesTradingAccountIdentity(t *testing.T) {
    descriptor := File_trade_service_proto
    service := descriptor.Services().ByName("TradingAccountService")
    if service == nil {
        t.Fatal("TradingAccountService is required")
    }
    account := descriptor.Messages().ByName("TradingAccount")
    if account == nil || account.Fields().ByName("trading_account_id") == nil {
        t.Fatal("TradingAccount.trading_account_id is required")
    }
    if account.Fields().ByName("exchange_account_id") != nil {
        t.Fatal("legacy exchange_account_id must be removed")
    }
}
```

Run:

```bash
cd modules/trade/proto/tradegen
go test -count=1 . -run TestTradingAccountServiceUsesTradingAccountIdentity
```

Expected: FAIL，错误为 `TradingAccountService is required`。

- [ ] **Step 7: 重命名 Proto 消息、字段和账户服务**

`trade_service.proto` 的最终账户入口使用：

```protobuf
message TradingAccountSnapshot {
  repeated AssetBalance balances = 1;
  string equity = 2;
  string available_funds = 3;
  string used_margin = 4;
  string maintenance_margin = 5;
  string unrealized_pnl = 6;
  int64 exchange_updated_at = 7;
}

message TradingAccount {
  string trading_account_id = 1;
  string space_id = 2;
  string name = 3;
  Exchange exchange = 4;
  MarketType market_type = 5;
  ExecutionMode execution_mode = 6;
  AccountEnvironment environment = 7;
  string credential_secret_id = 8;
  string settlement_asset = 9;
  string margin_mode = 10;
  string status = 11;
  bool ready = 12;
  map<string, string> leverage_settings = 13;
  TradingAccountSnapshot snapshot = 14;
  int64 last_sync_at = 15;
  int64 last_ready_at = 16;
  string last_error = 17;
  int64 created_at = 18;
  int64 updated_at = 19;
  repeated string sync_symbols = 20;
}

service TradingAccountService {
  rpc CreateTradingAccount(CreateTradingAccountReq) returns (CreateTradingAccountRsp);
  rpc UpdateTradingAccount(UpdateTradingAccountReq) returns (UpdateTradingAccountRsp);
  rpc GetTradingAccount(GetTradingAccountReq) returns (GetTradingAccountRsp);
  rpc ListTradingAccounts(ListTradingAccountsReq) returns (ListTradingAccountsRsp);
  rpc SetLeverage(SetLeverageReq) returns (SetLeverageRsp);
  rpc SyncTradingAccount(SyncTradingAccountReq) returns (SyncTradingAccountRsp);
}
```

所有 Order、Fill、Position、LogicalAccountMember 和请求中的 `exchange_account_id` 同步改为
`trading_account_id`。
Order、Fill、Position 同时返回 `instrument_id` 和 `exchange_symbol`；
`PlaceManualOrderReq`、列表筛选只接收 `instrument_id`。

- [ ] **Step 8: 重新生成 Proto 并修复手写验证**

Run:

```bash
make -C modules/trade/proto all
```

在 `tradegen/validation.go` 中统一使用 `TradingAccountId` getter 和
`"trading_account_id"` 错误文案。禁止手工编辑 `trade_service.pb.go` 与
`trade_service.trpc.go`。

- [ ] **Step 9: 修复 RPC、Runtime、测试和前端的编译引用**

账户 RPC 名固定为：

```go
const TradingAccountServiceName = "trpc.moox.trade.TradingAccountService"
```

同步把临时部署 ID 从 `trade_exchange_account` 改为 `trade_trading_account`，
并更新 `trpc_go.yaml`、Admin SysDeploy、默认部署 YAML 和 Web service map。
Task 9 会把三个临时拆分服务最终收敛为 `trade_console`。

前端基础类型固定为：

```ts
export interface TradingAccount {
  trading_account_id: string;
  space_id: string;
  name: string;
  exchange: Exchange;
  market_type: MarketType;
  execution_mode: ExecutionMode;
  environment: AccountEnvironment;
  credential_secret_id: string;
  settlement_asset: string;
  margin_mode: string;
  status: string;
  ready: boolean;
  leverage_settings: Record<string, string>;
  snapshot?: TradingAccountSnapshot;
  last_sync_at: string;
  last_ready_at: string;
  last_error: string;
  created_at: string;
  updated_at: string;
  sync_symbols: string[];
}
```

- [ ] **Step 10: 执行残留扫描**

Run:

```bash
rg -n 'ExchangeAccount|exchange_account_id|exchange_accounts|t_exchange_accounts|t_exchange_positions|trade_exchange_account' \
  --glob '*.go' --glob '*.proto' --glob '*.sql' --glob '*.yaml' --glob '*.yml' \
  --glob '*.ts' --glob '*.vue' --glob '*.sh' \
  modules/trade \
  modules/strategy/internal/bootstrap \
  web/src/api/trade \
  web/src/views/trading \
  modules/admin/internal/service/sysdeploy \
  examples/setup/default/service-deployments.yaml \
  scripts/tests/e2e
```

Expected: 无输出。历史 `docs/superpowers/` 不纳入该扫描。

Run:

```bash
rg -n 'c_symbol|\\.Symbol\\b' modules/trade/schema modules/trade/internal/infra/store
```

Expected: 无输出；Store 事实统一使用 `InstrumentID` / `ExchangeSymbol`。

- [ ] **Step 11: 运行受影响测试**

Run:

```bash
cd modules/trade
go test -count=1 ./...
cd ../strategy
go test -count=1 ./internal/bootstrap/...
cd ../../web
pnpm exec vitest run --config vitest.config.ts src/api/trade/trade.test.ts
```

Expected: 全部 PASS。

- [ ] **Step 12: 提交绿场重命名**

```bash
git add modules/trade modules/strategy/internal/bootstrap web/src/api/trade \
  web/src/views/trading modules/admin/internal/service/sysdeploy \
  examples/setup/default/service-deployments.yaml scripts/tests/e2e
git commit -m "refactor(trade): rename execution accounts"
```

---

### Task 2: 持久化 LiveConfig、PaperConfig 与 EquityPoint

**Files:**
- Create: `modules/trade/schema/paper_account_config.sql`
- Create: `modules/trade/schema/equity.sql`
- Create: `modules/trade/internal/infra/store/paper_account_config.go`
- Create: `modules/trade/internal/infra/store/paper_account_config_test.go`
- Create: `modules/trade/internal/infra/store/equity_point.go`
- Create: `modules/trade/internal/infra/store/equity_point_test.go`
- Modify: `modules/trade/schema/account.sql`
- Modify: `modules/trade/schema/schema.go`
- Modify: `modules/trade/schema/schema_test.go`
- Modify: `modules/trade/internal/infra/store/account.go`
- Modify: `modules/trade/internal/infra/store/store.go`
- Modify: `modules/trade/proto/trade_service.proto`
- Modify: `modules/trade/proto/tradegen/validation.go`
- Modify: `modules/trade/internal/domain/tradingaccount/account.go`
- Modify: `modules/trade/internal/application/account/repository.go`
- Modify: `modules/trade/internal/application/account/service.go`
- Modify: `modules/trade/internal/config/app.go`
- Modify: `modules/trade/config/app.yaml`

- [ ] **Step 1: 写 PaperConfig 和 EquityPoint 的失败 Schema 测试**

```go
func TestAllSQLCreatesPaperConfigAndEquityTables(t *testing.T) {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    require.NoError(t, err)
    require.NoError(t, db.Exec(AllSQL()).Error)

    for _, table := range []string{
        "t_paper_account_configs",
        "t_account_equity_points",
    } {
        var count int64
        require.NoError(t, db.Raw(
            `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
            table,
        ).Scan(&count).Error)
        require.Equal(t, int64(1), count, table)
    }
}
```

Run:

```bash
cd modules/trade
go test -count=1 ./schema -run TestAllSQLCreatesPaperConfigAndEquityTables
```

Expected: FAIL，缺少两个表。

- [ ] **Step 2: 创建 PaperConfig Schema**

`paper_account_config.sql` 使用完整定义：

```sql
CREATE TABLE IF NOT EXISTS t_paper_account_configs (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_initial_balance TEXT NOT NULL,
    c_maker_fee_rate TEXT NOT NULL,
    c_taker_fee_rate TEXT NOT NULL,
    c_slippage_bps TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
        ON DELETE CASCADE
);
```

- [ ] **Step 3: 创建 EquityPoint Schema**

`equity.sql` 使用：

```sql
CREATE TABLE IF NOT EXISTS t_account_equity_points (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_bucket_time INTEGER NOT NULL,
    c_equity TEXT NOT NULL,
    c_available_funds TEXT NOT NULL,
    c_used_margin TEXT NOT NULL,
    c_unrealized_pnl TEXT NOT NULL,
    c_source_time INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_trading_account_id, c_bucket_time),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_account_equity_points_time
ON t_account_equity_points (c_space_id, c_bucket_time);
```

在 `schema.go` 中按
`accountSQL → paperAccountConfigSQL → instrumentSQL → logicalAccountSQL → executionSQL → equitySQL`
拼接。

- [ ] **Step 4: 写 Store 失败测试**

`paper_account_config_test.go`：

```go
func TestPaperAccountConfigRoundTrip(t *testing.T) {
    s := openTestStore(t)
    seedTradingAccount(t, s, "PAPER")
    want := PaperAccountConfigRecord{
        SpaceID: "space-1", TradingAccountID: "account-1",
        InitialBalance: "100000",
        MakerFeeRate: "0.001", TakerFeeRate: "0.002",
        SlippageBPS: "5",
    }
    require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
        return tx.PutPaperAccountConfig(want)
    }))
    got, err := s.GetPaperAccountConfig(context.Background(), "space-1", "account-1")
    require.NoError(t, err)
    require.Equal(t, want.InitialBalance, got.InitialBalance)
    require.Equal(t, want.TakerFeeRate, got.TakerFeeRate)
}
```

`equity_point_test.go`：

```go
func TestUpsertEquityPointReplacesSameMinute(t *testing.T) {
    s := openTestStore(t)
    seedTradingAccount(t, s, "PAPER")
    first := EquityPointRecord{
        SpaceID: "space-1", TradingAccountID: "account-1",
        BucketTime: 1_700_000_000_000,
        Equity: "100000", AvailableFunds: "100000",
        UsedMargin: "0", UnrealizedPnL: "0",
        SourceTime: 1_700_000_000_100,
    }
    require.NoError(t, s.UpsertEquityPoint(context.Background(), first))
    first.Equity = "100010"
    first.SourceTime++
    require.NoError(t, s.UpsertEquityPoint(context.Background(), first))
    rows, err := s.ListEquityPoints(
        context.Background(), "space-1", "account-1", 0, 0,
    )
    require.NoError(t, err)
    require.Len(t, rows, 1)
    require.Equal(t, "100010", rows[0].Equity)
}
```

- [ ] **Step 5: 实现 Store 记录和十进制校验**

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
    BucketTime       int64
    Equity           string
    AvailableFunds   string
    UsedMargin       string
    UnrealizedPnL    string
    SourceTime       int64
}
```

`PutPaperAccountConfig` 必须使用 `shared.ParseDecimal` 校验：

```go
initial, err := shared.ParseDecimal(record.InitialBalance)
if err != nil || initial.Cmp(shared.Zero()) <= 0 {
    return fmt.Errorf("%w: initial balance must be positive", ErrInvalidRecord)
}
for label, raw := range map[string]string{
    "maker fee rate": record.MakerFeeRate,
    "taker fee rate": record.TakerFeeRate,
    "slippage bps": record.SlippageBPS,
} {
    value, parseErr := shared.ParseDecimal(raw)
    if parseErr != nil || value.IsNegative() {
        return fmt.Errorf("%w: %s must be non-negative", ErrInvalidRecord, label)
    }
}
```

maker/taker 费率还必须 `< 1`。

- [ ] **Step 6: 把账户配置改成 oneof 领域模型**

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
    ID               string
    SpaceID          string
    Name             string
    Exchange         exchange.Exchange
    MarketType       exchange.MarketType
    ExecutionMode    exchange.ExecutionMode
    SettlementAsset  string
    MarginMode       exchange.MarginMode
    Status           exchange.AccountStatus
    Live             *LiveConfig
    Paper            *PaperConfig
    Ready            bool
    SyncSymbols      []string
    LeverageSettings map[string]shared.Decimal
    Snapshot         exchange.AccountSnapshot
}
```

`Validate()` 要求 Live/Paper 恰好一个非 nil。

账户用于查询标的目录和报价的环境统一通过：

```go
func (a Account) MarketDataEnvironment() exchange.AccountEnvironment {
    if a.Paper != nil {
        return exchange.AccountEnvironmentProduction
    }
    return a.Live.Environment
}
```

- [ ] **Step 7: 原子创建账户和 PaperConfig**

把 `application/account.Repository.Create` 改为单事务：

```go
func (r Repository) Create(ctx context.Context, value tradingaccount.Account) error {
    return r.Store.Transaction(ctx, func(tx *store.Tx) error {
        if err := tx.CreateTradingAccount(accountRecord(value)); err != nil {
            return err
        }
        if value.Paper == nil {
            return nil
        }
        return tx.PutPaperAccountConfig(paperConfigRecord(value))
    })
}
```

- [ ] **Step 8: 删除进程级 Paper 初始资金**

从以下位置删除 `PaperInitialBalance` / `paper_initial_balance`：

```text
modules/trade/internal/config/app.go
modules/trade/internal/config/app_test.go
modules/trade/config/app.yaml
modules/trade/internal/bootstrap/bootstrap.go
```

- [ ] **Step 9: 修改 Proto 并重新生成**

新增：

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

message CreateTradingAccountReq {
  string name = 1;
  Exchange exchange = 2;
  MarketType market_type = 3;
  string settlement_asset = 4;
  string margin_mode = 5;
  repeated string sync_symbols = 6;
  oneof execution_config {
    LiveConfig live = 7;
    PaperConfig paper = 8;
  }
}
```

`TradingAccount` 和 `CreateTradingAccountReq` 使用 `oneof execution_config`。

Run:

```bash
make -C modules/trade/proto all
cd modules/trade
go test -count=1 ./schema ./internal/infra/store ./internal/domain/tradingaccount ./internal/application/account
cd proto/tradegen
go test -count=1 .
```

Expected: PASS。

- [ ] **Step 10: 提交持久化基础**

```bash
git add modules/trade/schema modules/trade/internal/infra/store \
  modules/trade/internal/domain/tradingaccount modules/trade/internal/application/account \
  modules/trade/internal/config modules/trade/config modules/trade/proto
git commit -m "feat(trade): persist paper account and equity settings"
```

---

### Task 3: 引入 ExecutionAdapter 与 MarketDataSource

**Files:**
- Create: `modules/trade/internal/execution/adapter.go`
- Create: `modules/trade/internal/execution/marketdata.go`
- Create: `modules/trade/internal/execution/factory.go`
- Create: `modules/trade/internal/execution/factory_test.go`
- Create: `modules/trade/internal/execution/adapter_contract_test.go`
- Modify: `modules/trade/internal/exchange/binance/binance.go`
- Modify: `modules/trade/internal/exchange/binance/binance_test.go`
- Modify: `modules/trade/internal/exchange/okx/okx.go`
- Modify: `modules/trade/internal/exchange/okx/okx_test.go`
- Modify: `modules/trade/internal/runtime/manager.go`
- Modify: `modules/trade/internal/runtime/session.go`
- Modify: `modules/trade/internal/application/order/service.go`
- Modify: `modules/trade/internal/application/target/price.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: 写 MarketQuote 失败测试**

在 Binance/OKX 测试中分别增加：

```go
func assertQuoteResult(
    t *testing.T,
    quote execution.MarketQuote,
    err error,
) {
    t.Helper()
    require.NoError(t, err)
    require.Equal(t, "100", quote.Bid.String())
    require.Equal(t, "101", quote.Ask.String())
    require.Equal(t, "100.5", quote.Last.String())
    require.False(t, quote.SourceTime.IsZero())
}
```

Binance 测试沿用现有 `testAdapter` + `httptest.Server`，返回
`bidPrice=100, askPrice=101, price=100.5`；OKX 测试沿用现有
`newWithClient`，返回 `bidPx=100, askPx=101, last=100.5, ts=1700000000000`，
然后把 `GetQuote` 的返回值传给 `assertQuoteResult`。

Run:

```bash
cd modules/trade
go test -count=1 ./internal/exchange/binance ./internal/exchange/okx \
  -run TestGetQuoteReturnsBidAskLastAndSourceTime
```

Expected: FAIL，`GetQuote` 尚不存在。

- [ ] **Step 2: 创建统一接口**

`internal/execution/marketdata.go`：

```go
package execution

type MarketQuote struct {
    Bid        shared.Decimal
    Ask        shared.Decimal
    Last       shared.Decimal
    SourceTime time.Time
}

type MarketDataSource interface {
    LoadInstruments(context.Context) ([]exchange.Instrument, error)
    GetQuote(context.Context, string) (MarketQuote, error)
}
```

`internal/execution/adapter.go`：

```go
package execution

type ExecutionAdapter interface {
    Exchange() exchange.Exchange
    GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error)
    ListPositionSnapshots(context.Context) ([]exchange.Position, error)
    ListOpenOrders(context.Context) ([]exchange.Order, error)
    ListRecentFills(context.Context, string, string) ([]exchange.Fill, string, error)
    GetOrder(context.Context, string, string) (exchange.Order, error)
    PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error)
    CancelOrder(context.Context, string, string) (exchange.Order, error)
    SetLeverage(context.Context, string, shared.Decimal) error
    SetMarginMode(context.Context, string, exchange.MarginMode) error
    SubscribePrivate(context.Context, exchange.EventHandler) error
}
```

`adapter_contract_test.go` 提供同包复用断言：

```go
func assertExecutionAdapterContract(
    t *testing.T,
    newAdapter func(t *testing.T) ExecutionAdapter,
) {
    t.Helper()
    adapter := newAdapter(t)
    first, err := adapter.PlaceOrder(context.Background(), contractOrderRequest())
    require.NoError(t, err)
    replay, err := adapter.PlaceOrder(context.Background(), contractOrderRequest())
    require.NoError(t, err)
    require.Equal(t, first.ExchangeOrderID, replay.ExchangeOrderID)
    current, err := adapter.GetOrder(
        context.Background(),
        contractOrderRequest().Symbol,
        contractOrderRequest().ClientOrderID,
    )
    require.NoError(t, err)
    require.Equal(t, first.ExchangeOrderID, current.ExchangeOrderID)

    open, err := adapter.PlaceOrder(
        context.Background(),
        contractOpenOrderRequest(),
    )
    require.NoError(t, err)
    canceled, err := adapter.CancelOrder(
        context.Background(),
        contractOpenOrderRequest().Symbol,
        contractOpenOrderRequest().ClientOrderID,
    )
    require.NoError(t, err)
    require.Equal(t, open.ExchangeOrderID, canceled.ExchangeOrderID)
    replayCancel, err := adapter.CancelOrder(
        context.Background(),
        contractOpenOrderRequest().Symbol,
        contractOpenOrderRequest().ClientOrderID,
    )
    require.NoError(t, err)
    require.Equal(t, canceled.Status, replayCancel.Status)
}

func contractOrderRequest() exchange.OrderRequest {
    return exchange.OrderRequest{
        ClientOrderID: "contract-order-1",
        Symbol: "BTCUSDT",
        OrderType: exchange.OrderTypeMarket,
        Side: exchange.SideBuy,
        Quantity: shared.MustDecimal("0.01"),
        ReferencePrice: shared.MustDecimal("50000"),
    }
}

func contractOpenOrderRequest() exchange.OrderRequest {
    price := shared.MustDecimal("1")
    return exchange.OrderRequest{
        ClientOrderID: "contract-open-1",
        Symbol: "BTCUSDT",
        OrderType: exchange.OrderTypeLimit,
        FillPolicy: exchange.FillPolicyGTC,
        Side: exchange.SideBuy,
        Quantity: shared.MustDecimal("0.01"),
        LimitPrice: &price,
        ReferencePrice: shared.MustDecimal("50000"),
    }
}
```

Task 3 先让 fake Live Adapter 通过；Task 5 完成 LIMIT 后必须用同一函数验证 PaperAdapter。

- [ ] **Step 3: 实现 Binance/OKX GetQuote**

Binance 使用 book ticker；OKX 使用 ticker 的 bid/ask/last/ts。保留
`GetReferencePrice`，但内部改为调用 `GetQuote` 并优先返回 `Last`。

- [ ] **Step 4: 写 Factory 模式分派测试**

```go
func TestFactoryUsesSingleModeBranch(t *testing.T) {
    live := &adapterStub{}
    paper := &adapterStub{}
    factory := Factory{
        Configs: paperConfigSourceStub{config: store.PaperAccountConfigRecord{
            SpaceID: "space-1",
            TradingAccountID: "account-1",
            InitialBalance: "100000",
            MakerFeeRate: "0.001",
            TakerFeeRate: "0.002",
            SlippageBPS: "5",
        }},
        BindLive: func(exchange.AccountConfig, exchange.Credential) (ExecutionAdapter, MarketDataSource, error) {
            return live, liveMarketDataStub{}, nil
        },
        BindPaper: func(
            exchange.AccountConfig,
            store.PaperAccountConfigRecord,
            MarketDataSource,
        ) (ExecutionAdapter, error) {
            return paper, nil
        },
    }
    got, err := factory.Bind(context.Background(), paperAccountRecord(), exchange.Credential{})
    require.NoError(t, err)
    require.Same(t, paper, got.Adapter)
}

type adapterStub struct {
    ExecutionAdapter
}

type liveMarketDataStub struct {
    MarketDataSource
}

type paperConfigSourceStub struct {
    config store.PaperAccountConfigRecord
}

func (s paperConfigSourceStub) GetPaperAccountConfig(
    context.Context,
    string,
    string,
) (store.PaperAccountConfigRecord, error) {
    return s.config, nil
}

func paperAccountRecord() store.TradingAccountRecord {
    return store.TradingAccountRecord{
        SpaceID: "space-1",
        TradingAccountID: "account-1",
        Exchange: "BINANCE",
        MarketType: "SPOT",
        ExecutionMode: "PAPER",
        SettlementAsset: "USDT",
        Status: "ENABLED",
    }
}
```

- [ ] **Step 5: 实现注入式 ExecutionFactory**

Factory 不 import `execution/paper`，避免父子包循环。构造器由 bootstrap 注入：

```go
type Binding struct {
    Adapter  ExecutionAdapter
    Market   MarketDataSource
}

type LiveBinder func(
    exchange.AccountConfig,
    exchange.Credential,
) (ExecutionAdapter, MarketDataSource, error)

type PaperBinder func(
    exchange.AccountConfig,
    store.PaperAccountConfigRecord,
    MarketDataSource,
) (ExecutionAdapter, error)

type PaperConfigSource interface {
    GetPaperAccountConfig(context.Context, string, string) (store.PaperAccountConfigRecord, error)
}

type Factory struct {
    BindLive  LiveBinder
    BindPaper PaperBinder
    Configs   PaperConfigSource
}
```

- [ ] **Step 6: 迁移 Manager、TradingSession 和应用依赖**

映射：

```text
runtime.Manager.Adapter()        -> ExecutionAdapter()
runtime.ExchangeSession          -> TradingSession
ExchangeSession.ExchangeAdapter  -> TradingSession.ExecutionAdapter
order.AdapterSource              -> execution.AdapterSource
target.ExchangePriceSource       -> execution.QuoteSource
```

`TradingSession` 仍负责私有流缓冲、初始快照、定期同步和 Fill cursor。

- [ ] **Step 7: 删除旧接口和 Registry**

在所有调用切换后删除：

```text
modules/trade/internal/exchange/adapter.go
modules/trade/internal/exchange/registry.go
modules/trade/internal/exchange/registry_test.go
```

- [ ] **Step 8: 运行执行边界测试与术语门禁**

Run:

```bash
cd modules/trade
go test -count=1 ./internal/execution ./internal/exchange/... \
  ./internal/runtime ./internal/application/order ./internal/application/target \
  ./internal/bootstrap
cd ../..
go run ./scripts/checks/check-trade-exchange-terminology.go
```

Expected: 全部 PASS，术语检查输出 `trade Exchange terminology passed`。

- [ ] **Step 9: 提交执行端口**

```bash
git add modules/trade/internal/execution modules/trade/internal/exchange \
  modules/trade/internal/runtime modules/trade/internal/application/order \
  modules/trade/internal/application/target modules/trade/internal/bootstrap
git commit -m "refactor(trade): unify execution adapter boundary"
```

---

### Task 4: 实现 Paper Portfolio、手续费与 MARKET 成交

**Files:**
- Create: `modules/trade/internal/execution/paper/portfolio.go`
- Create: `modules/trade/internal/execution/paper/portfolio_test.go`
- Create: `modules/trade/internal/execution/paper/pricing.go`
- Create: `modules/trade/internal/execution/paper/pricing_test.go`
- Create: `modules/trade/internal/execution/paper/adapter.go`
- Create: `modules/trade/internal/execution/paper/adapter_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Delete: `modules/trade/internal/exchange/paper/paper.go`
- Delete: `modules/trade/internal/exchange/paper/paper_test.go`

- [ ] **Step 1: 写 MARKET 定价和手续费失败测试**

```go
func TestMarketFillAppliesSlippageAndTakerFee(t *testing.T) {
    quote := execution.MarketQuote{
        Bid: shared.MustDecimal("100"),
        Ask: shared.MustDecimal("101"),
        Last: shared.MustDecimal("100.5"),
        SourceTime: time.Unix(1_700_000_000, 0),
    }
    price, err := marketExecutionPrice(
        exchange.SideBuy,
        quote,
        shared.MustDecimal("10"),
    )
    require.NoError(t, err)
    fee := executionFee(
        shared.MustDecimal("2"),
        price,
        shared.MustDecimal("0.002"),
    )
    require.Equal(t, "101.101", price.String())
    require.Equal(t, "0.404404", fee.String())
}
```

Expected price: ask `101 × 1.001 = 101.101`；fee: `2 × 101.101 × 0.002`。

Run:

```bash
cd modules/trade
go test -count=1 ./internal/execution/paper -run TestMarketFillAppliesSlippageAndTakerFee
```

Expected: FAIL，paper package 尚不存在。

- [ ] **Step 2: 写 Spot 资金数学测试**

```go
func TestSpotPortfolioSubtractsSettlementFee(t *testing.T) {
    portfolio := NewSpotPortfolio(
        shared.MustDecimal("1000"),
        "USDT",
        []exchange.Instrument{{
            Symbol: "BTCUSDT",
            BaseAsset: "BTC",
            QuoteAsset: "USDT",
        }},
    )
    require.NoError(t, portfolio.Apply(exchange.Fill{
        ExchangeTradeID: "fill-1",
        ExchangeOrderID: "order-1",
        Symbol: "BTCUSDT",
        Side: exchange.SideBuy,
        Quantity: shared.MustDecimal("2"),
        Price: shared.MustDecimal("100"),
        Fee: shared.MustDecimal("0.4"),
        FeeAsset: "USDT",
        TradedAt: time.Unix(1_700_000_000, 0),
    }))
    snapshot, err := portfolio.Snapshot(
        context.Background(),
        staticMarketData{quotes: map[string]execution.MarketQuote{
            "BTCUSDT": {
                Last: shared.MustDecimal("100"),
                SourceTime: time.Unix(1_700_000_000, 0),
            },
        }},
    )
    require.NoError(t, err)
    require.Equal(t, "799.6", balance(snapshot, "USDT").Total.String())
    require.Equal(t, "2", balance(snapshot, "BTC").Total.String())
}

type staticMarketData struct {
    instruments []exchange.Instrument
    quotes      map[string]execution.MarketQuote
}

func (s staticMarketData) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
    return append([]exchange.Instrument(nil), s.instruments...), nil
}

func (s staticMarketData) GetQuote(
    _ context.Context,
    symbol string,
) (execution.MarketQuote, error) {
    quote, ok := s.quotes[symbol]
    if !ok {
        return execution.MarketQuote{}, errors.New("quote not found")
    }
    return quote, nil
}

func balance(snapshot exchange.AccountSnapshot, asset string) exchange.AssetBalance {
    for _, item := range snapshot.Balances {
        if item.Asset == asset {
            return item
        }
    }
    return exchange.AssetBalance{}
}
```

- [ ] **Step 3: 写 Swap 资金数学测试**

覆盖：

```text
开多 2 @ 100
加多 1 @ 110
减多 1 @ 120
反向到空 1 @ 90
每次 Fill 扣 settlement fee
equity = initial + realized + unrealized - cumulative fee
```

断言每一步 quantity、entry price、realized PnL、used margin 和 equity。

- [ ] **Step 4: 实现纯 Portfolio**

`portfolio.go` 不调用网络、不持有 Store，只接收 Config、Instrument、Fill 和 Quote。
Spot/Swap 都从 Fill 重建，方法固定为：

```go
type Portfolio interface {
    Apply(exchange.Fill) error
    Snapshot(context.Context, execution.MarketDataSource) (exchange.AccountSnapshot, error)
    Positions(context.Context, execution.MarketDataSource) ([]exchange.Position, error)
}
```

- [ ] **Step 5: 实现 PaperAdapter MARKET**

PaperAdapter：

```go
type Adapter struct {
    config           store.PaperAccountConfigRecord
    account          store.TradingAccountRecord
    market           execution.MarketDataSource
    facts            FactSource
    events           chan privateEvent
    mu               sync.Mutex
}
```

MARKET 必须：

1. 读取新鲜 quote。
2. BUY 使用 ask、SELL 使用 bid，缺失时使用 last。
3. 加减滑点。
4. 生成确定性 ExchangeOrderID / ExchangeTradeID。
5. 返回 FILLED Order。
6. 向私有通道发送 OrderEvent 和 FillEvent。

- [ ] **Step 6: 运行 Paper MARKET 与恢复测试**

Run:

```bash
cd modules/trade
go test -count=1 ./internal/execution/paper \
  -run 'TestMarket|TestSpotPortfolio|TestSwapPortfolio|TestAdapterRestores'
```

Expected: PASS。

- [ ] **Step 7: 提交 Paper MARKET**

```bash
git add modules/trade/internal/execution/paper modules/trade/internal/bootstrap \
  modules/trade/internal/exchange/paper
git commit -m "feat(trade): add configurable paper market execution"
```

---

### Task 5: 实现简化 LIMIT 撮合与重启恢复

**Files:**
- Create: `modules/trade/internal/execution/paper/matcher.go`
- Create: `modules/trade/internal/execution/paper/matcher_test.go`
- Modify: `modules/trade/internal/execution/paper/adapter.go`
- Modify: `modules/trade/internal/infra/store/fact.go`
- Modify: `modules/trade/internal/infra/store/fact_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: 写 LIMIT 规则表驱动失败测试**

```go
func TestLimitPolicies(t *testing.T) {
    tests := []struct {
        name       string
        policy     exchange.FillPolicy
        immediatelyMarketable bool
        wantStatus exchange.OrderStatus
        wantRole   string
    }{
        {"gtc immediate", exchange.FillPolicyGTC, true, exchange.OrderStatusFilled, "TAKER"},
        {"gtc rests", exchange.FillPolicyGTC, false, exchange.OrderStatusOpen, ""},
        {"ioc immediate", exchange.FillPolicyIOC, true, exchange.OrderStatusFilled, "TAKER"},
        {"ioc cancels", exchange.FillPolicyIOC, false, exchange.OrderStatusCanceled, ""},
        {"fok immediate", exchange.FillPolicyFOK, true, exchange.OrderStatusFilled, "TAKER"},
        {"fok cancels", exchange.FillPolicyFOK, false, exchange.OrderStatusCanceled, ""},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := executeLimitFixture(t, tt.policy, tt.immediatelyMarketable)
            require.Equal(t, tt.wantStatus, got.Order.Status)
            require.Equal(t, tt.wantRole, got.FillRole)
        })
    }
}
```

`executeLimitFixture` 必须在同一测试文件中创建内存 Trade Store、固定报价
`bid=100/ask=101/last=100.5`、PaperAdapter 和回报记录器；`immediatelyMarketable=false`
时 BUY limit 使用 `99`，为 true 时使用 `101`。返回值包含最终 Order 和可选 Fill role。

- [ ] **Step 2: 写延迟 GTC 穿价测试**

```go
func TestMatcherFillsRestingGTCAsMaker(t *testing.T) {
    fixture := newMatcherFixture(t)
    order := fixture.placeLimit(exchange.SideBuy, "99", exchange.FillPolicyGTC)
    require.Equal(t, exchange.OrderStatusOpen, order.Status)
    fixture.quotes.Set("BTCUSDT", quote("98", "99", "98.5"))
    require.NoError(t, fixture.matcher.RunOnce(context.Background()))
    fill := fixture.singleFill()
    require.Equal(t, "MAKER", fill.LiquidityRole)
    require.Equal(t, "99", fill.Price.String())
}
```

`newMatcherFixture` 必须使用真实临时 SQLite，先通过 OrderService 写入 OPEN GTC，
`quotes.Set` 更新线程安全的 `MarketDataSource`，`singleFill` 从共享 Fill 表读取并要求总数为 1。

- [ ] **Step 3: 增加 OPEN Paper 订单查询**

在 Store 增加：

```go
func (s *Store) ListOpenPaperOrders(
    ctx context.Context,
) ([]OrderRecord, error)
```

SQL 必须 join `t_trading_accounts`，约束：

```sql
a.c_execution_mode = 'PAPER'
AND o.c_state IN ('OPEN', 'PARTIALLY_FILLED')
AND o.c_order_type = 'LIMIT'
AND o.c_time_in_force = 'GTC'
```

尽管 Paper 不产生部分成交，保留 `PARTIALLY_FILLED` 可让异常数据安全进入恢复路径。

- [ ] **Step 4: 实现单个进程内 PaperMatcher**

```go
type Matcher struct {
    Orders   OpenPaperOrderSource
    Adapters PaperAdapterSource
    Interval time.Duration
    wake     chan struct{}
}
```

`RunOnce` 按 Exchange + symbol 归组，每组只取一次 quote，再按
`submitted_at, order_id` 稳定排序。它不使用 EventBus。

- [ ] **Step 5: 保证匹配与取消幂等**

生成 ID：

```go
func paperIDs(tradingAccountID, clientOrderID string) (string, string) {
    sum := sha256.Sum256([]byte(tradingAccountID + "\x00" + clientOrderID))
    suffix := hex.EncodeToString(sum[:12])
    return "paper-order-" + suffix, "paper-fill-" + suffix
}
```

重复 matcher tick 允许重复发送同一 Fill，`FillReducer` 必须依靠确定性 Fill ID 吸收重复。

- [ ] **Step 6: 写重启恢复测试**

测试只预置 SQLite OPEN GTC 订单，不预置内存状态；创建新 Matcher 后必须在行情穿价时成交。

- [ ] **Step 7: 运行 Matcher 测试**

Run:

```bash
cd modules/trade
go test -count=1 ./internal/execution/paper ./internal/infra/store \
  -run 'TestLimit|TestMatcher|TestListOpenPaperOrders|TestPaperAdapterContract'
```

Expected: PASS。

- [ ] **Step 8: 提交 LIMIT 撮合**

```bash
git add modules/trade/internal/execution/paper \
  modules/trade/internal/infra/store modules/trade/internal/bootstrap
git commit -m "feat(trade): match paper limit orders"
```

---

### Task 6: 接入共用订单校验、资金预占和 TradingSession

**Files:**
- Modify: `modules/trade/internal/application/order/validator.go`
- Modify: `modules/trade/internal/application/order/validator_test.go`
- Modify: `modules/trade/internal/application/order/service.go`
- Modify: `modules/trade/internal/application/order/service_test.go`
- Modify: `modules/trade/internal/application/consumer/fill.go`
- Modify: `modules/trade/internal/application/consumer/fill_test.go`
- Modify: `modules/trade/internal/runtime/session.go`
- Modify: `modules/trade/internal/runtime/manager.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap_test.go`

- [ ] **Step 1: 把 Paper LIMIT 防御测试改为允许能力**

删除 `ErrPaperLimit` 预期，新增：

```go
func TestPaperLimitUsesConfiguredMaximumFeeForReservation(t *testing.T) {
    validator := paperValidator(
        store.PaperAccountConfigRecord{
            InitialBalance: "1000",
            MakerFeeRate: "0.001",
            TakerFeeRate: "0.002",
            SlippageBPS: "0",
        },
    )
    validation, err := validator.Validate(
        context.Background(),
        "space-1",
        limitBuySpec("100", "2"),
    )
    require.NoError(t, err)
    require.Equal(t, "200.4", validation.ReservedQuantity.String())
}
```

`paperValidator` 必须装配真实 `order.Validator`、PaperConfig Store stub、Ready Paper
TradingAccount、BTC-USDT-SPOT Instrument 和余额 1000 USDT；`limitBuySpec("100", "2")`
必须生成 BUY LIMIT GTC、quantity=2、limit/reference price=100 的完整 OrderSpec。

LIMIT 预占使用 `max(maker_fee_rate, taker_fee_rate)`；MARKET 使用 taker。

- [ ] **Step 2: 引入 FeePolicy**

```go
type FeePolicy interface {
    ReservationRate(
        context.Context,
        tradingaccount.Account,
        orderdomain.OrderSpec,
    ) (shared.Decimal, error)
}
```

- Live：返回现有保守默认费率。
- Paper MARKET：taker。
- Paper LIMIT：maker/taker 最大值。

- [ ] **Step 3: 消费实际手续费**

修改 `consumeReservation`：

```go
if fill.FeeAsset == record.ReservedAsset {
    used = used.Add(fill.Fee)
}
```

Spot BUY 和 Swap 开仓都必须覆盖该分支；剩余预占在 FILLED 后归零。

- [ ] **Step 4: 用 ExecutionFactory 创建 TradingSession**

Bootstrap 的核心装配改为：

```go
binding, err := executionFactory.Bind(ctx, record, credential)
if err != nil {
    return nil, err
}
return &traderuntime.TradingSession{
    Account: record,
    Adapter: binding.Adapter,
    Market: binding.Market,
    Sync: syncService,
    SyncInterval: 30 * time.Second,
}, nil
```

删除 `paper.New(baseAdapter, ..., cfg.Runtime.PaperInitialBalance, ...)` 包装分支。

- [ ] **Step 5: 让 FillReducer 发出成功回调**

```go
type FillAppliedObserver interface {
    FillApplied(context.Context, Source) error
}

type Reducer struct {
    Store   *store.Store
    Now     func() time.Time
    Applied FillAppliedObserver
}
```

只在事务提交成功且 `applied == true` 后调用 observer；重复 Fill 不触发。

- [ ] **Step 6: 运行应用层和 Runtime 测试**

Run:

```bash
cd modules/trade
go test -count=1 ./internal/application/order \
  ./internal/application/consumer ./internal/runtime ./internal/bootstrap
```

Expected: PASS。

- [ ] **Step 7: 运行 Paper 端到端子集**

Run:

```bash
cd modules/trade
go test -count=1 ./test -run 'TestSpotPaper|TestPaperSwap|TestStrategyTarget' -timeout 5m
```

Expected: PASS。

- [ ] **Step 8: 提交主流程接线**

```bash
git add modules/trade/internal/application/order \
  modules/trade/internal/application/consumer modules/trade/internal/runtime \
  modules/trade/internal/bootstrap modules/trade/test
git commit -m "feat(trade): route live and paper through one execution flow"
```

---

### Task 7: 实现 Live/Paper 共用 EquitySampler

**Files:**
- Create: `modules/trade/internal/application/equity/service.go`
- Create: `modules/trade/internal/application/equity/service_test.go`
- Create: `modules/trade/internal/runtime/equity_worker.go`
- Create: `modules/trade/internal/runtime/equity_worker_test.go`
- Modify: `modules/trade/internal/application/consumer/fill.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/health/state.go`
- Modify: `modules/trade/internal/health/server_test.go`

- [ ] **Step 1: 写 Spot 共用估值失败测试**

```go
func TestValueSpotAccountUsesSettlementQuotes(t *testing.T) {
    service := fixtureService(
        balances(
            balance("USDT", "500"),
            balance("BTC", "0.01"),
        ),
        instrument("BTCUSDT", "BTC", "USDT"),
        quote("50000"),
    )
    point, err := service.Sample(context.Background(), "account-1")
    require.NoError(t, err)
    require.Equal(t, "1000", point.Equity)
    require.Equal(t, "500", point.AvailableFunds)
}
```

`fixtureService` 必须使用真实 `equity.Service`，账户源返回一个 Ready SPOT
TradingAccount，instrument source 返回传入标的，quote source 返回传入价格，point store
记录最后写入值。`balances`、`balance`、`instrument` 和 `quote` 只负责构造对应领域值。

- [ ] **Step 2: 写 Swap 快照测试**

```go
func TestValueSwapAccountUsesSnapshotEquity(t *testing.T) {
    service := swapFixture("101000", "99000", "2000", "1000")
    point, err := service.Sample(context.Background(), "account-1")
    require.NoError(t, err)
    require.Equal(t, "101000", point.Equity)
    require.Equal(t, "99000", point.AvailableFunds)
    require.Equal(t, "2000", point.UsedMargin)
    require.Equal(t, "1000", point.UnrealizedPnL)
}
```

`swapFixture` 必须返回同一个 `equity.Service`，但账户为 SWAP，Snapshot 直接携带传入的
equity、available、used margin 和 unrealized PnL。

- [ ] **Step 3: 实现应用服务**

```go
type Service struct {
    Accounts    AccountSource
    Instruments InstrumentSource
    Quotes      QuoteSource
    Points      EquityPointStore
    Now         func() time.Time
}

func minuteBucket(at time.Time) int64 {
    return at.UTC().Truncate(time.Minute).UnixMilli()
}
```

Spot 只估值能够唯一映射到结算资产的余额；无法估值的非零资产返回错误，不静默漏算。

- [ ] **Step 4: 写 Worker 唤醒与隔离测试**

```go
func TestEquityWorkerCoalescesFillWake(t *testing.T) {
    sampler := &samplerStub{}
    worker := &EquityWorker{Sampler: sampler, Interval: time.Hour}
    worker.Wake("account-1")
    worker.Wake("account-1")
    require.NoError(t, worker.runPending(context.Background()))
    require.Equal(t, []string{"account-1"}, sampler.ids)
}
```

另测一个账户采样失败不阻断其他账户。

- [ ] **Step 5: 实现单 Worker**

```go
type EquityWorker struct {
    Accounts AccountSource
    Sampler  AccountSampler
    Interval time.Duration
    signal   chan struct{}
    mu       sync.Mutex
    pending  map[string]struct{}
}
```

每分钟扫描 Ready 账户；Fill observer 把 account ID 放入 `pending` set，再向容量 1 的
`signal` 发送唤醒，避免不同账户因 channel 满而丢失。

- [ ] **Step 6: 接入 Bootstrap 与 Health**

启动一个 `EquityWorker.Run` goroutine，并在 readiness 中增加 `EquityWorker` 快照。

- [ ] **Step 7: 运行测试并提交**

Run:

```bash
cd modules/trade
go test -count=1 ./internal/application/equity ./internal/runtime \
  ./internal/application/consumer ./internal/bootstrap ./internal/health
```

Expected: PASS。

Commit:

```bash
git add modules/trade/internal/application/equity \
  modules/trade/internal/runtime/equity_worker* \
  modules/trade/internal/application/consumer modules/trade/internal/bootstrap \
  modules/trade/internal/health
git commit -m "feat(trade): record unified equity curves"
```

---

### Task 8: 实现 ResetPaperLogicalAccount

**Files:**
- Create: `modules/trade/internal/infra/store/paper_reset.go`
- Create: `modules/trade/internal/infra/store/paper_reset_test.go`
- Create: `modules/trade/internal/application/operator/reset_paper.go`
- Create: `modules/trade/internal/application/operator/reset_paper_test.go`
- Modify: `modules/trade/internal/domain/operator/action.go`
- Modify: `modules/trade/internal/domain/operator/action_test.go`
- Modify: `modules/trade/internal/runtime/operator_worker.go`
- Modify: `modules/trade/internal/application/operator/service.go`
- Modify: `modules/trade/schema/logical_account.sql`
- Modify: `modules/trade/proto/trade_service.proto`
- Modify: `modules/trade/proto/tradegen/validation.go`
- Modify: `modules/trade/internal/rpc/logical_account.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: 写重置前置条件失败测试**

```go
func TestResetPaperLogicalAccountRequiresPausedUnownedPaperAccount(t *testing.T) {
    tests := []struct {
        name string
        mutate func(*resetFixture)
    }{
        {"active", func(f *resetFixture) { f.logical.AutomationState = "ACTIVE" }},
        {"owned", func(f *resetFixture) { f.logical.OwnerRunnerID = "runner-1" }},
        {"live member", func(f *resetFixture) { f.member.ExecutionMode = "LIVE" }},
        {"running action", func(f *resetFixture) { f.runningActions = 1 }},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            fixture := newResetFixture(t)
            tt.mutate(fixture)
            _, err := fixture.service.ResetPaperLogicalAccount(
                context.Background(),
                fixture.command(),
            )
            require.ErrorIs(t, err, ErrInvalidCommand)
        })
    }
}
```

`newResetFixture` 必须使用真实临时 SQLite 和 `operator.Service`，预置 PAUSED、无 owner、
无运行 action、一个 PAPER 成员；`mutate` 修改后必须同步写回 Store，确保测试覆盖真实查询而非内存副本。

- [ ] **Step 2: 增加 RESET_PAPER ActionType**

```go
const ActionResetPaper ActionType = "RESET_PAPER"
```

同步更新 `logical_account.sql` 的 CHECK：

```sql
CHECK (c_action_type IN (
    'MANUAL_ORDER',
    'CANCEL_ORDER',
    'FLATTEN',
    'RESET_PAPER'
))
```

- [ ] **Step 3: 写原子清理 Store 测试**

测试预置两个 Paper 成员、订单、Fill、Position、EquityPoint、旧 Action 和 target。
调用后断言：

```text
旧 Order/Fill/Position/EquityPoint = 0
旧 OperatorAction = 0
当前 RESET_PAPER Action = 1
LogicalAccountTarget = not found
两份 PaperConfig = 请求的新值
TradingAccount ready = false
LogicalAccount automation_state = PAUSED
```

- [ ] **Step 4: 实现 Tx.ResetPaperLogicalAccount**

```go
type PaperResetAccount struct {
    TradingAccountID string
    Config           PaperAccountConfigRecord
}

func (tx *Tx) ResetPaperLogicalAccount(
    spaceID string,
    logicalAccountID string,
    actionID string,
    accounts []PaperResetAccount,
) error
```

删除顺序必须满足 FK：

```text
t_order_fills
t_trade_orders
t_trading_positions
t_account_equity_points
t_operator_actions（排除当前 action_id）
t_logical_account_targets
```

随后更新 PaperConfig 和账户 snapshot/cursor/readiness。

- [ ] **Step 5: 实现幂等应用服务**

```go
type ResetPaperCommand struct {
    SpaceID         string
    ActionID        string
    LogicalAccountID string
    Reason          string
    Accounts        []PaperResetAccountConfig
}
```

相同 `action_id` + 相同请求返回已有结果；相同 ID 不同配置返回 `store.ErrConflict`。

- [ ] **Step 6: 接入 OperatorWorker 恢复**

`ResumeOperatorAction` 新增 `RESET_PAPER` 分支，并从 `request_json` 恢复完整 configs。

- [ ] **Step 7: 添加 Proto 请求和 RPC**

```protobuf
message PaperResetAccountConfig {
  string trading_account_id = 1;
  PaperConfig paper = 2;
}

message ResetPaperLogicalAccountReq {
  string action_id = 1;
  string logical_account_id = 2;
  string reason = 3;
  repeated PaperResetAccountConfig accounts = 4;
}
```

- [ ] **Step 8: 运行重置测试并提交**

Run:

```bash
make -C modules/trade/proto all
cd modules/trade
go test -count=1 ./internal/infra/store ./internal/application/operator \
  ./internal/domain/operator ./internal/runtime ./internal/rpc ./schema
```

Expected: PASS。

Commit:

```bash
git add modules/trade/internal/infra/store/paper_reset* \
  modules/trade/internal/application/operator modules/trade/internal/domain/operator \
  modules/trade/internal/runtime/operator_worker.go modules/trade/schema/logical_account.sql \
  modules/trade/proto modules/trade/internal/rpc modules/trade/internal/bootstrap
git commit -m "feat(trade): reset paper logical accounts"
```

---

### Task 9: 收敛 TradeConsoleService、Capabilities 和资金曲线查询

**Files:**
- Create: `modules/trade/internal/rpc/console.go`
- Create: `modules/trade/internal/rpc/console_test.go`
- Modify: `modules/trade/proto/trade_service.proto`
- Modify: `modules/trade/internal/rpc/register.go`
- Modify: `modules/trade/internal/rpc/register_test.go`
- Modify: `modules/trade/config/trpc_go.yaml`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/acceptance_test.go`
- Modify: `modules/strategy/config/app.yaml`
- Modify: `modules/strategy/internal/bootstrap/config.go`
- Modify: `modules/strategy/internal/bootstrap/config_test.go`
- Modify: `modules/strategy/internal/bootstrap/logical_account.go`
- Modify: `examples/setup/default/service-deployments.yaml`
- Modify: `scripts/tests/e2e/test-strategy-trade-logical-account-e2e.sh`

- [ ] **Step 1: 写最终服务契约失败测试**

```go
func TestTradeConsoleIsOnlyBusinessHTTPService(t *testing.T) {
    services := File_trade_service_proto.Services()
    for _, name := range []protoreflect.Name{
        "TradingAccountService",
        "TradeExecutionService",
        "LogicalAccountService",
    } {
        if services.ByName(name) != nil {
            t.Fatalf("split service %s must be removed", name)
        }
    }
    console := services.ByName("TradeConsoleService")
    if console == nil {
        t.Fatal("TradeConsoleService is required")
    }
}
```

- [ ] **Step 2: 定义最终 TradeConsoleService**

服务必须包含原三个服务的全部方法，并增加：

```protobuf
rpc GetExecutionCapabilities(GetExecutionCapabilitiesReq)
    returns (GetExecutionCapabilitiesRsp);
rpc GetTradingAccountOverview(GetTradingAccountOverviewReq)
    returns (GetTradingAccountOverviewRsp);
rpc QueryEquityCurve(QueryEquityCurveReq)
    returns (QueryEquityCurveRsp);
rpc ResetPaperLogicalAccount(ResetPaperLogicalAccountReq)
    returns (ResetPaperLogicalAccountRsp);
```

`QueryEquityCurveReq` 必须在 `trading_account_id` 与 `logical_account_id` 中恰好选择一个，
并带 `start_time`、`end_time`。

新增消息至少包含：

```protobuf
message ExecutionCapabilities {
  repeated OrderType order_types = 1;
  repeated FillPolicy fill_policies = 2;
  bool live_trading_enabled = 3;
  bool paper_reset_supported = 4;
}

message EquityPoint {
  int64 bucket_time = 1;
  string equity = 2;
  string available_funds = 3;
  string used_margin = 4;
  string unrealized_pnl = 5;
  int64 source_time = 6;
}

message QueryEquityCurveRsp {
  common.RetInfo ret_info = 1;
  repeated EquityPoint points = 2;
}
```

- [ ] **Step 3: 实现 ConsoleServer 组合**

```go
type EquityQuery interface {
    QueryAccount(
        context.Context,
        string,
        string,
        int64,
        int64,
    ) ([]store.EquityPointRecord, error)
    QueryLogicalAccount(
        context.Context,
        string,
        string,
        int64,
        int64,
    ) ([]store.EquityPointRecord, error)
}

type CapabilityQuery interface {
    Get(context.Context, string, string) (tradepb.ExecutionCapabilities, error)
}

type ConsoleServer struct {
    *AccountServer
    *LogicalAccountServer
    *ExecutionServer
    Equity EquityQuery
    Capabilities CapabilityQuery
}
```

现有三个 Handler 文件继续按职责保留；只收敛网络服务，不把业务代码合并成大文件。

`console_test.go` 必须覆盖：

```go
func TestPaperCapabilitiesExposeMarketLimitAndReset(t *testing.T) {
    response, err := serverFixture(t, "PAPER").GetExecutionCapabilities(
        context.Background(),
        &tradepb.GetExecutionCapabilitiesReq{TradingAccountId: "paper-1"},
    )
    require.NoError(t, err)
    require.ElementsMatch(t, []tradepb.OrderType{
        tradepb.OrderType_ORDER_TYPE_MARKET,
        tradepb.OrderType_ORDER_TYPE_LIMIT,
    }, response.GetCapabilities().GetOrderTypes())
    require.True(t, response.GetCapabilities().GetPaperResetSupported())
}

func TestLogicalEquityCurveAggregatesMemberBuckets(t *testing.T) {
    response, err := serverFixture(t, "PAPER").QueryEquityCurve(
        context.Background(),
        &tradepb.QueryEquityCurveReq{
            LogicalAccountId: "logical-1",
            StartTime: 1_000,
            EndTime: 2_000,
        },
    )
    require.NoError(t, err)
    require.Equal(t, "200", response.GetPoints()[0].GetEquity())
}
```

`serverFixture` 必须组合真实 `ConsoleServer` 与内存 Store。PAPER 模式预置两个同结算资产成员，
每个成员在 bucket=1000 的 equity=100，以验证 LogicalAccount 聚合结果为 200。

聚合查询只返回所有启用成员都存在快照的完整 bucket；成员缺点时跳过该 bucket，避免低估组合权益。

- [ ] **Step 4: 收敛 tRPC 配置**

最终业务端口：

```yaml
- name: trpc.moox.trade.TradeConsoleService
  ip: 127.0.0.1
  port: 11200
  network: tcp
  protocol: http
  timeout: 15000
```

删除 11201/11202 服务；保留 DNS 11203、Health/Timer 11210。

- [ ] **Step 5: 更新 Strategy owner client**

`logical_account.go` 使用：

```go
client tradepb.TradeConsoleServiceClientProxy
```

默认目标改为 `ip://127.0.0.1:11200`。

- [ ] **Step 6: 收敛 Admin 部署**

最终只保留：

```go
deployment(
    "trade_console",
    "trade",
    "http",
    "127.0.0.1",
    11200,
    "trpc.moox.trade.TradeConsoleService",
    "internal",
    "交易账户、执行、持仓与资金曲线服务",
)
```

删除 `trade_exchange_account`、`trade_execution`、`trade_logical_account`。

- [ ] **Step 7: 运行协议、RPC、部署和 Strategy 测试**

Run:

```bash
make -C modules/trade/proto all
cd modules/trade/proto/tradegen
go test -count=1 .
cd ../..
go test -count=1 ./internal/rpc ./internal/bootstrap
cd ../admin
go test -count=1 ./internal/service/sysdeploy/...
cd ../strategy
go test -count=1 ./internal/bootstrap/...
```

Expected: PASS。

- [ ] **Step 8: 运行部署契约**

Run:

```bash
cd ../..
bash scripts/tests/e2e/test-strategy-trade-logical-account-e2e.sh
```

Expected: PASS，脚本只替换 11200/11203/11210。

- [ ] **Step 9: 提交服务收敛**

```bash
git add modules/trade/proto modules/trade/internal/rpc modules/trade/config \
  modules/trade/internal/bootstrap modules/admin/internal/service/sysdeploy \
  modules/strategy/config modules/strategy/internal/bootstrap \
  examples/setup/default/service-deployments.yaml \
  scripts/tests/e2e/test-strategy-trade-logical-account-e2e.sh
git commit -m "refactor(trade): expose one console service"
```

---

### Task 10: 实现 Live/Paper 共用前端

**Files:**
- Create: `web/src/views/trading/account-overview/account-form.ts`
- Create: `web/src/views/trading/account-overview/account-form.test.ts`
- Create: `web/src/views/trading/logical-accounts/equity-curve.vue`
- Create: `web/src/views/trading/logical-accounts/equity-curve.ts`
- Create: `web/src/views/trading/logical-accounts/equity-curve.test.ts`
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

- [ ] **Step 1: 写统一服务映射失败测试**

```ts
it("uses one Trade console service", () => {
  expect(trade.tradeServiceMap).toEqual({ console: "trade_console" });
});
```

Run:

```bash
cd web
pnpm exec vitest run --config vitest.config.ts src/api/trade/trade.test.ts
```

Expected: FAIL，仍有三个 service id。

- [ ] **Step 2: 收敛 Trade client**

```ts
export const tradeServiceMap = { console: "trade_console" } as const;
```

所有 API 调用使用 `callTrade("console", method, req)`。

- [ ] **Step 3: 写账户 oneof 请求测试**

```ts
it("builds paper account config without live credentials", () => {
  expect(
    buildCreateTradingAccountRequest({
      name: "Paper",
      exchange: 1,
      market_type: 1,
      execution_mode: 1,
      settlement_asset: "USDT",
      initial_balance: "100000",
      maker_fee_rate: "0.001",
      taker_fee_rate: "0.002",
      slippage_bps: "5",
    })
  ).toEqual({
    name: "Paper",
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

另测 Live 请求只含 `live.environment` 和 `live.credential_secret_id`。

- [ ] **Step 4: 重构账户创建表单**

Paper 显示：

```text
初始资金
maker fee rate
taker fee rate
slippage bps
```

Live 显示：

```text
TESTNET / PRODUCTION
Credential Secret
```

禁止两类字段同时出现在请求中。

- [ ] **Step 5: 写资金曲线转换测试**

```ts
it("sorts equity points and parses decimal strings", () => {
  expect(
    toEquitySeries([
      { bucket_time: "2000", equity: "101" },
      { bucket_time: "1000", equity: "100" },
    ])
  ).toEqual([
    { time: 1000, value: 100 },
    { time: 2000, value: 101 },
  ]);
});
```

- [ ] **Step 6: 实现共用资金曲线组件**

使用 `@visactor/vchart` 单折线：

```ts
const spec = {
  type: "line",
  xField: "time",
  yField: "value",
  data: [{ id: "equity", values: toEquitySeries(props.points) }],
  axes: [
    { orient: "bottom", type: "time" },
    { orient: "left", title: { visible: true, text: props.settlementAsset } },
  ],
};
```

组件不判断 Live/Paper。

- [ ] **Step 7: 接入 LogicalAccount Workspace**

打开详情时并行请求：

```text
GetLogicalAccount
GetLogicalAccountTarget
QueryEquityCurve(logical_account_id)
GetExecutionCapabilities
```

Paper 且 PAUSED、无 owner 时显示“重置模拟盘”；Live 不显示。

- [ ] **Step 8: 修正手工订单能力**

表单只提交 `instrument_id`。SPOT 不发送 `position_side`；SWAP 发送 NET。
Paper 与 Live 的订单类型、FillPolicy 都读取 capabilities。

- [ ] **Step 9: 增加 Paper Reset E2E**

在 `strategy-console.spec.ts` mock `ResetPaperLogicalAccount` 和 `QueryEquityCurve`，
断言：

```text
资金曲线 Live/Paper 使用同一组件
有 owner 时重置按钮禁用并提示先停用 Runner
无 owner、PAUSED 时可提交每个成员的新 PaperConfig
完成后仍显示 PAUSED
```

- [ ] **Step 10: 运行前端验证**

Run:

```bash
cd web
pnpm exec vitest run --config vitest.config.ts \
  src/api/trade/trade.test.ts \
  src/views/trading/account-overview/account-form.test.ts \
  src/views/trading/logical-accounts/equity-curve.test.ts
pnpm exec playwright test tests/strategy-console.spec.ts
pnpm build:prod
```

Expected: 全部 PASS。

- [ ] **Step 11: 提交共用前端**

```bash
git add web/src/api/trade web/src/views/trading web/src/lang \
  web/tests/strategy-console.spec.ts
git commit -m "feat(web): unify live and paper trading console"
```

---

### Task 11: 完成跨模式 E2E、清理和全仓验证

**Files:**
- Modify: `modules/trade/test/e2e_helpers_test.go`
- Modify: `modules/trade/test/spot_market_e2e_test.go`
- Modify: `modules/trade/test/swap_execution_e2e_test.go`
- Modify: `modules/trade/test/strategy_target_e2e_test.go`
- Modify: `modules/trade/test/logical_account_operator_e2e_test.go`
- Create: `modules/trade/test/live_paper_parity_e2e_test.go`
- Modify: `modules/trade/README.md`
- Modify: `docs/交易模块架构设计.md`
- Modify: `docs/交易模块功能说明.md`
- Modify: `docs/架构总览.md`
- Modify: `modules/README.md`
- Modify: `modules/trade/scripts/run.sh`
- Modify: `scripts/checks/check-trade-exchange-terminology.go` only when tests prove a false positive

- [ ] **Step 1: 写跨模式同构失败 E2E**

```go
func TestLiveAndPaperProduceEquivalentFacts(t *testing.T) {
    paper := runTargetScenario(t, exchange.ExecutionModePaper)
    live := runTargetScenario(t, exchange.ExecutionModeLive)

    require.Equal(t, live.OrderShape(), paper.OrderShape())
    require.Equal(t, live.FillShape(), paper.FillShape())
    require.Equal(t, live.PositionShape(), paper.PositionShape())
    require.NotEmpty(t, live.EquityPoints)
    require.NotEmpty(t, paper.EquityPoints)
}
```

在 `e2e_helpers_test.go` 新增 `newExecutionFixture(t, mode)`；`runTargetScenario` 必须通过
TargetExecutor 接受同一个 BTC-USDT-SPOT FULL target，等待一次完整成交，再从 Store 读取
Order、Fill、Position 和 EquityPoint 组成 `parityResult`。禁止直接调用 PaperMatcher
跳过 OrderService。

Shape 比较忽略 Exchange 生成 ID、时间和实际价格，只比较字段存在性、状态、方向、数量和归属。

- [ ] **Step 2: 增加 Paper 重启恢复 E2E**

流程：

```text
创建 Paper GTC LIMIT
确认 OPEN
关闭 Store/Runtime
重开同一个 SQLite
设置穿价行情
运行 Matcher
确认同一订单 FILLED 且只有一个 Fill
```

- [ ] **Step 3: 增加分钟采样和 Fill wake E2E**

使用固定时钟断言同一分钟只有一个 EquityPoint，Fill 后 equity 已更新。

- [ ] **Step 4: 清理旧符号和配置**

Run:

```bash
rg -n 'ExchangeAccount|exchange_account_id|exchange_accounts|t_exchange_accounts|t_exchange_positions|paper_initial_balance|trade_exchange_account|trade_execution|trade_logical_account' \
  --glob '*.go' --glob '*.proto' --glob '*.sql' --glob '*.yaml' --glob '*.yml' \
  --glob '*.ts' --glob '*.vue' --glob '*.sh' \
  modules/trade modules/strategy/internal/bootstrap web/src/api/trade \
  web/src/views/trading modules/admin/internal/service/sysdeploy \
  examples/setup/default/service-deployments.yaml scripts/tests/e2e
```

Expected: 无输出。

Run:

```bash
rg -n 'exchange/paper|exchange\.Adapter|exchange\.Registry' modules/trade
```

Expected: 无输出。

- [ ] **Step 5: 运行 Trade 全量测试**

```bash
cd modules/trade
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

Expected: 全部 PASS。

- [ ] **Step 6: 运行跨模块和前端测试**

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

Expected: 全部 PASS。

- [ ] **Step 7: 在提交前运行生成、术语、格式和边界门禁**

```bash
cd ..
make proto
go run ./scripts/checks/check-trade-exchange-terminology.go
git diff --check
make check-boundaries
```

Expected:

```text
make proto PASS，生成文件只包含预期差异
trade Exchange terminology passed
git diff --check 无输出
check-boundaries PASS
```

- [ ] **Step 8: 更新现役文档**

文档必须明确：

```text
单 Trade 进程 / 单 SQLite
TradingAccount oneof LiveConfig/PaperConfig
一个执行内核 + LiveAdapter/PaperAdapter
Paper MARKET 与简化 LIMIT
maker/taker 费率与滑点
共用 EquitySampler
LogicalAccount 级 Paper 重置
不支持盘口、部分成交、独立 Paper 服务
```

- [ ] **Step 9: 提交最终 E2E 与文档**

```bash
git add modules/trade/test modules/trade/README.md modules/trade/scripts \
  docs/交易模块架构设计.md docs/交易模块功能说明.md docs/架构总览.md \
  modules/README.md scripts/checks/check-trade-exchange-terminology.go
git commit -m "test(trade): verify live and paper execution parity"
```

- [ ] **Step 10: 在干净工作区运行最终验证**

```bash
git status --short
make proto-check
make verify
```

Expected:

```text
git status --short 无输出
proto-check PASS
make verify PASS
```

---

## 四、最终验收标准

- Live 与 Paper 都从 Strategy FULL target 和人工订单进入同一个 OrderService。
- 应用层不存在 `if execution_mode == PAPER` 的业务分支。
- Paper 初始资金、maker/taker 费率和滑点按 TradingAccount 持久化。
- Paper MARKET 使用滑点和 taker 费率。
- Paper LIMIT：立即成交为 taker；GTC 穿价成交为 maker；IOC/FOK 不可立即全量成交则取消。
- Paper 不产生部分成交。
- OPEN Paper GTC 订单重启后继续撮合。
- Live/Paper 使用同一 Order、Fill、Position、EquityPoint 表和 API。
- Spot Live/Paper 使用同一余额估值逻辑；Swap 使用账户 equity。
- EquityPoint 每分钟采样，Fill 后覆盖当前分钟。
- `ResetPaperLogicalAccount` 清空成员事实和 target，保持 PAUSED，要求先释放 owner。
- 浏览器只访问 `trade_console`，不再知道拆分服务。
- Live/Paper 共用前端页面和资金曲线组件。
- 交易术语门禁、Proto 生成、Go race、Web 测试、Playwright、构建和 `make verify` 全部通过。
