# Trade 实盘与模拟盘统一执行设计

> 状态：设计已确认（第二版）
>
> 日期：2026-08-25

## 1. 背景

MooX 面向个人量化，不追求高可用、分布式扩展或机构级撮合。设计目标是简洁、实用、易维护。
项目无需兼容历史协议和数据结构，可以直接调整命名、Proto 和 Schema。

Trade 当前已经通过 `exchange.Adapter` 统一部分 Live/Paper 流程，但 Paper V1 仍有明显限制：

- 初始资金来自进程级 `paper_initial_balance`，不能按账户设置。
- 成交费率固定为 0。
- 只支持 MARKET，并按参考价立即全量成交。
- Paper Adapter 同时承担行情、撮合、资金、持仓和重启恢复，职责过重。
- 没有 Live/Paper 共用的资金曲线。
- Paper 的运行态部分依赖内存 map，不适合作为唯一事实。

## 2. 目标

1. 实盘和模拟盘共用订单、安全校验、目标收敛、成交归并、持仓、查询和前端主流程。
2. 只有执行边界不同：实盘发送到真实交易所，模拟盘发送到进程内虚拟执行端。
3. 用户创建模拟账户时设置初始结算资金、maker/taker 费率和滑点。
4. 模拟盘支持 MARKET 和简化 LIMIT 撮合。
5. Live/Paper 使用相同的 Order、Fill、Position 和 EquityPoint 数据结构。
6. Live/Paper 使用同一组 API 和页面。
7. Trade 保持单进程、单 SQLite，不增加独立 Paper 服务。
8. Paper 账户及其经济参数创建后不可修改、重置或删除；新一轮模拟交易创建新账户。

## 3. 非目标

- 不实现盘口深度、排队优先级或部分成交。
- 不实现分布式锁、主从复制、跨进程调度或全局 exactly-once。
- 不拆分独立订单、风控、Paper 或资金服务。
- 不引入双式账本；Paper 资金由初始资金和 Fill 事实重建。
- 不让浏览器或 Paper Matcher 直接消费 JetStream；内存信号只负责唤醒。
- 不支持 Paper 账户配置更新、事实清空、Reset 或复用历史账户启动新实验。
- StrategyRunner 不直接关联 TradingAccount，继续关联 LogicalAccount。
- 本设计不解决 Strategy 自动读取 View 并触发 `RunOnce` 的上游调度问题。

## 4. 已确认的产品决策

- Paper 撮合采用简化模型：
  - MARKET 按最新可执行报价加减滑点后立即全量成交。
  - LIMIT 在提交时可成交则立即成交；否则进入 OPEN，行情穿价后全量成交。
  - 不模拟盘口和部分成交。
- Paper 分别配置 `maker_fee_rate` 和 `taker_fee_rate`。
- Paper 只配置结算资产初始资金，不导入初始持仓。
- 资金曲线每分钟采样，并在 Fill 入库后立即刷新当前分钟快照。
- PaperConfig 创建后不可修改。
- Paper LogicalAccount 固定只有一个 Paper TradingAccount 成员，创建后不更换成员。
- 新一轮模拟交易创建新的 Paper TradingAccount 和新的 Paper LogicalAccount。
- StrategyRunner 通过 `logical_account_id` 选择本次执行账户，Live/Paper 主流程保持一致。

## 5. 总体架构

```text
Strategy FULL Target / 人工订单
        |
        v
TargetExecutor / OperatorService
        |
        v
OrderService
  - 参数与标的校验
  - 资金预占
  - 幂等
  - PENDING 订单先落库
        |
        v
ExecutionAdapter
  +--------------------+----------------------+
  |                                           |
  v                                           v
LiveAdapter                               PaperAdapter
Binance / OKX REST + 私有流               MarketDataSource + PaperMatcher
  |                                           |
  v                                           v
规范化 Live 回报                         OPEN + version CAS
  |                                           |
  +--------------------+----------------------+
                       v
共用 SQLite reducer 事务
  - Fill
  - Order terminal state
  - Position / Holding facts
  - reservation
                       |
                       v
Order / Fill / Position / EquityPoint
```

Order 状态机、FillReducer、Position 投影和查询层不得根据 `execution_mode` 分叉。
账户生命周期、安全闸门、ReservationPolicy 和 Capabilities 可以通过 `ExecutionFactory`
注入模式相关策略，不允许在核心 reducer 中散落条件分支。

## 6. 核心端口

### 6.1 MarketDataSource

```go
type MarketDataSource interface {
    LoadInstruments(context.Context) ([]Instrument, error)
    GetQuote(context.Context, ExchangeSymbol) (MarketQuote, error)
}
```

`MarketQuote` 至少包含 `bid`、`ask`、`last` 和 `source_time`。当数据源没有 bid/ask 时，
允许回退到 last，但必须保留来源时间并执行 freshness 校验。

规范标的和交易所原生代码使用不同类型：

```go
type InstrumentID string
type ExchangeSymbol string
```

应用层和 Strategy 只传 `InstrumentID`；`InstrumentResolver` 在账户、Exchange、行情环境和
MarketType 上下文中解析 `ExchangeSymbol`。ExecutionAdapter 只接收 `ExchangeSymbol`。

Paper V1 固定使用所选 Exchange 的生产公共行情，不需要交易凭据。

### 6.2 ExecutionAdapter

```go
type ExecutionAdapter interface {
    GetAccountSnapshot(context.Context) (AccountSnapshot, error)
    ListPositionSnapshots(context.Context) ([]Position, error)
    ListOpenOrders(context.Context) ([]Order, error)
    ListRecentFills(context.Context, ExchangeSymbol, string) ([]Fill, string, error)
    GetOrder(context.Context, ExchangeSymbol, string) (Order, error)
    PlaceOrder(context.Context, OrderRequest) (Order, error)
    CancelOrder(context.Context, ExchangeSymbol, string) (Order, error)
    SetLeverage(context.Context, ExchangeSymbol, Decimal) error
    SetMarginMode(context.Context, ExchangeSymbol, MarginMode) error
    SubscribePrivate(context.Context, EventHandler) error
}
```

`OrderRequest` 在执行边界只携带 `ExchangeSymbol`；canonical `InstrumentID` 已保存在本地
Order 事实中，不由 Exchange 适配器解释。

- `LiveAdapter` 封装 Binance/OKX 的真实执行接口。
- `PaperAdapter` 使用 SQLite 中的共享订单事实和进程内 `PaperMatcher`。
- Live 回报通过现有私有流进入共用 reducer；Paper 撮合直接调用共用事务 reducer。

### 6.3 ExecutionBundle 与 ReservationPolicy

`ExecutionFactory` 返回完整的账户执行依赖，不只返回下单接口：

```go
type ExecutionBundle struct {
    Adapter            ExecutionAdapter
    MarketData         MarketDataSource
    ReservationPolicy ReservationPolicy
    InstrumentResolver InstrumentResolver
}

type ReservationPolicy interface {
    Reserve(context.Context, TradingAccount, OrderSpec, MarketQuote) (Reservation, error)
}
```

- Live ReservationPolicy 保留现有交易所快照 + 本地未反映 reservation 语义。
- Paper ReservationPolicy 以初始资金、Fill 和所有活动订单预占为权威。
- Paper MARKET BUY 按 `ask + slippage + taker fee` 预占。
- Paper GTC LIMIT BUY 按 `limit + max(maker,taker fee)` 预占。
- SELL 预占基础资产 quantity；Swap 按保证金和最坏费率预占结算资产。

## 7. 代码组织

```text
modules/trade/internal/execution/
  adapter.go
  marketdata.go
  factory.go
  paper/
    adapter.go
    matcher.go
    portfolio.go

modules/trade/internal/exchange/
  binance/  # 保留现有真实 Exchange 实现
  okx/      # 保留现有真实 Exchange 实现
```

现有 `application/order`、`application/target`、`application/operator`、
`application/accountsync` 和 `domain/order` 保持共用，不复制 Paper 版本。
Binance/OKX 文件不做无收益搬迁；它们直接实现 `execution.ExecutionAdapter`。

## 8. 账户模型

`ExchangeAccount` 重命名为 `TradingAccount`。

```protobuf
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

message LiveConfig {
  AccountEnvironment environment = 1; // TESTNET | PRODUCTION
  string credential_secret_id = 2;
}

message PaperConfig {
  string initial_balance = 1;
  string maker_fee_rate = 2;
  string taker_fee_rate = 3;
  string slippage_bps = 4;
}
```

约束：

- `initial_balance > 0`
- `0 <= maker_fee_rate < 1`
- `0 <= taker_fee_rate < 1`
- `0 <= slippage_bps < 10000`
- Live 必须有 Secret；Paper 禁止配置 Secret。
- SPOT 不允许 margin mode 或 leverage。
- SWAP 使用 CROSS margin mode，并保留按原生 symbol 配置的 leverage。
- Live SPOT 必须保留 `sync_symbols`，以完成订单、成交和余额同步。
- Paper 配置创建后不可修改；不存在替换配置或重置入口。

## 9. 持久化

### 9.1 账户

- `t_trading_accounts`：Live/Paper 共用账户主体。
- `t_paper_account_configs`：Paper 一对一配置。

### 9.2 执行事实

以下表由 Live/Paper 共用：

- `t_trade_orders`
- `t_order_fills`
- `t_exchange_positions`（实现时同步重命名为 `t_trading_positions`）

三类事实同时保存 canonical `instrument_id` 和原生 `exchange_symbol`。Spot 不伪造成
衍生品 Position：Spot 持仓通过账户余额生成统一 `Holding` 读模型；Swap 继续使用 Position。

不新增 `t_paper_orders`、`t_paper_fills` 或独立资金账本。

### 9.3 资金曲线

新增账户和 LogicalAccount 两张分钟曲线表：

```text
t_account_equity_points
  space_id
  trading_account_id
  bucket_time
  equity
  available_funds
  used_margin
  unrealized_pnl
  source_time
  updated_at

t_logical_account_equity_points
  space_id
  logical_account_id
  bucket_time
  equity
  available_funds
  used_margin
  unrealized_pnl
  source_time
  updated_at
```

账户点唯一键为 `(space_id, trading_account_id, bucket_time)`；LogicalAccount 点唯一键为
`(space_id, logical_account_id, bucket_time)`。同一分钟的 timer 和 Fill wake 使用最新快照覆盖。

Sampler 在采样当时按成员关系生成 LogicalAccount 点并持久化，从而冻结历史成员语义。
任一启用成员缺少有效快照时，不写该 LogicalAccount 分钟点，不能把缺失成员按 0 计入。

### 9.4 Paper reservation

Paper AccountSnapshot 必须把全部非终态订单 reservation 纳入 `available` 和 `locked`。
活动订单预占来自共享 Order 表，不依赖 Fill：

```text
available = fill-derived balance - active order reservations
locked    = active order reservations
```

因此旧 OPEN GTC 订单即使早于最近一次 snapshot，也不能被排除。Paper 与 Live 可以使用不同
ReservationPolicy，但 OrderService 只消费统一 Reservation 结果。

## 10. Paper 撮合

### 10.1 MARKET

- BUY 基础价取 ask，缺少 ask 时取 last。
- SELL 基础价取 bid，缺少 bid 时取 last。
- BUY 成交价：`base_price * (1 + slippage_bps / 10000)`。
- SELL 成交价：`base_price * (1 - slippage_bps / 10000)`。
- 一次性全部成交。
- `liquidity_role = TAKER`，使用 `taker_fee_rate`。

### 10.2 LIMIT

提交时：

- BUY 的可执行价不高于 limit，立即全量成交。
- SELL 的可执行价不低于 limit，立即全量成交。
- 立即成交属于 TAKER，使用 `taker_fee_rate`。
- 成交价使用可执行报价，但不得劣于 limit。
- GTC 未立即成交时进入 OPEN。
- IOC/FOK 因不模拟部分成交而采用相同语义：可立即全量成交则成交，否则立即取消。

`PaperMatcher`：

- 只扫描 OPEN GTC Paper 订单。
- 默认每秒扫描一次。
- 按 Exchange + symbol 合并报价请求。
- BUY 在可执行报价不高于 limit 时成交。
- SELL 在可执行报价不低于 limit 时成交。
- 延迟成交属于 MAKER，使用 `maker_fee_rate`。
- 一次性全部成交，不产生部分成交。
- 报价缺失或过期时保持 OPEN。

### 10.3 Fee

```text
fee = abs(quantity * execution_price) * fee_rate
```

Fee 统一使用结算资产：

- Spot BUY：结算资产减少 `notional + fee`，基础资产增加 quantity。
- Spot SELL：基础资产减少 quantity，结算资产增加 `notional - fee`。
- Swap：`equity = initial_balance + realized_pnl + unrealized_pnl - cumulative_fee`。

Paper 下单校验必须使用对应 maker/taker 费率预留资金，不能继续使用与账户配置无关的固定
`FeeBufferRate`。Fill 继续保存实际 `fee` 和 `fee_asset`；资金曲线只保存账户快照，
费用明细通过 Fill 查询，不在 EquityPoint 重复持久化。

## 11. Paper 唯一写入者与原子撮合

SQLite 是 Paper 唯一事实源。内存 channel 只能发送可丢失 wake，不能承载唯一 Order/Fill 事实。

```text
OrderService
  -> PENDING / SUBMITTING / OPEN 持久化
  -> PaperAdapter 返回接受结果
  -> Matcher wake（可丢失，周期扫描兜底）
  -> OPEN + version CAS
  -> 共用 reducer SQLite 事务
       Insert Fill
       Update Order terminal state
       Update Position / Holding facts
       Consume / release reservation
```

协议要求：

- PaperAdapter 不在 `PlaceOrder` 持账户锁期间同步回调 Fill。
- PaperMatcher 只处理 SQLite 中的候选订单。
- Matcher 用 `order_id + expected_version + OPEN` CAS 抢占；Cancel 和 Match 只有一方成功。
- Fill 插入、Order 终态、Swap Position 和 reservation 更新复用 FillReducer 的同一个事务函数。
- 进程崩溃前未提交的 Match 不产生事实；已提交事实不依赖内存事件补写。
- OPEN GTC 订单在重启后由周期扫描继续撮合。
- `client_order_id`、`order_id` 和 `fill_id` 保持确定性与幂等。

## 12. EquitySampler

`EquitySampler` 同时服务 Live/Paper：

- 每分钟采样所有启用且 Ready 的 TradingAccount。
- Fill 成功入库后 Wake 对应账户，立即刷新当前分钟。
- Spot 统一按“结算资产余额 + 非结算资产余额 × 新鲜报价”估值，解决部分真实交易所
  Spot 快照不直接返回 equity 的问题。
- Swap 优先使用账户快照 equity、available funds、used margin 和 unrealized PnL。
- Live/Paper 都通过同一个估值函数生成 EquityPoint；Paper 不保留私有曲线算法。
- 账户点写入后，Sampler 使用采样当时的成员集合生成并持久化 LogicalAccount 点。
- 任一成员 Not Ready、报价过期或采样失败时，跳过本次 LogicalAccount 点并记录指标。

Sampler 是进程内单 worker，不使用 EventBus 或分布式调度。

## 13. Paper 模拟生命周期

Paper 不提供 Reset、配置修改或事实删除。一次模拟运行对应一组不可变资源：

```text
Paper TradingAccount 1 ── 1 Paper LogicalAccount
StrategyRunner       0..1 ──> LogicalAccount
```

开始新模拟：

1. 创建新的 Paper TradingAccount，写入不可变 PaperConfig。
2. 同一事务创建新的 Paper LogicalAccount，并把该账户作为唯一成员。
3. 用户在 StrategyRunner 中选择新的 `logical_account_id`。
4. 旧账户、旧 LogicalAccount、订单、成交、持仓和曲线保持只读。

V1 不提供删除或归档。若只读历史数量明显影响使用，再增加严格的 Archive 生命周期；Archive
不得修改经济参数或删除事实。

`AddLogicalAccountMember` 和 `RemoveLogicalAccountMember` 对 Paper LogicalAccount 返回拒绝；
Live LogicalAccount 保留现有 PAUSED 状态下的成员管理能力。

## 14. API 与前端

浏览器统一使用 `TradeConsoleService`：

```text
CreateTradingAccount
CreatePaperSimulation
GetTradingAccountOverview
GetExecutionCapabilities
QueryEquityCurve
ListOrders
ListFills
ListHoldings
ListPositions
PlaceManualOrder
CancelOrder
```

`CreateTradingAccount` 只创建 Live 账户。Paper 必须通过 `CreatePaperSimulation` 创建，
该 RPC 在一个事务中写入 TradingAccount、PaperConfig、LogicalAccount 和唯一成员关系，
避免产生无法执行的孤立 Paper 账户。

交互原则：

- 不创建两套交易页面。
- 创建账户时根据模式展示 LiveConfig 或 PaperConfig。
- 所有列表和详情返回 `execution_mode`，仅用于标签和能力展示。
- Paper/Live 共用订单、成交、持仓和资金曲线组件。
- 下单只提交 canonical `instrument_id`，服务端解析 Exchange symbol。
- `CreatePaperSimulation` 原子创建不可变 Paper TradingAccount 和单成员 Paper LogicalAccount。
- StrategyRunner 选择 `logical_account_id`，不直接持有物理账户 ID。
- Spot 页面展示 Holding；Swap 页面展示 Position。

## 15. 错误处理

- Live Place/Cancel 不自动重试；保留现有 UNKNOWN 状态恢复。
- Paper Place/Cancel 必须确定性返回，不产生 UNKNOWN。
- Paper 报价过期时：
  - MARKET 拒绝下单。
  - GTC LIMIT 保持 OPEN，等待新鲜报价。
  - IOC/FOK LIMIT 立即取消。
- ExecutionAdapter 错误统一转换为现有 Trade 领域错误，不向应用层暴露 Binance/OKX/Paper 私有错误。

### 15.1 Production 安全闸门

保留 `live_trading_enabled=false` 默认值，并在以下边界 fail closed：

- 创建或启用 PRODUCTION Live TradingAccount。
- 每次 OrderService Submit。
- `GetExecutionCapabilities` 对浏览器返回不可下单原因。

关闭开关时仍允许构造只读 LiveAdapter，继续执行账户同步、查单、撤单和风险退出；不得因开关关闭
而失去观察或清理 Production 敞口的能力。

## 16. 测试与验收

### 16.1 共用 ExecutionAdapter 契约

Live fake adapter 与 PaperAdapter 必须通过同一套测试：

- Place 幂等
- Cancel 幂等
- GetOrder
- ListOpenOrders
- 重启恢复

Live 另外验证私有流回报归一化；Paper 另外验证 Matcher CAS 和共用 reducer 原子提交。
两种路径最终都必须生成同结构的本地 Order/Fill/Position 事实，但不强制使用同一种传输机制。

### 16.2 Paper 专项

- MARKET BUY/SELL 滑点
- LIMIT 立即成交与延迟成交
- maker/taker 费率
- Spot 资金与持仓
- Swap 开仓、加仓、减仓、反向与费用
- 报价过期
- 进程重启后续撮合
- MARKET/GTC LIMIT 最坏价格与手续费预占
- OPEN 订单 reservation 纳入 Paper available/locked
- Match/Cancel 使用 version CAS，只有一方提交
- Match 的 Fill/Order/Position/reservation 同事务
- PaperConfig 不可更新，新的模拟运行创建新账户和新 LogicalAccount
- 分钟采样与 Fill wake
- LogicalAccount 曲线不受后续成员变化影响

### 16.3 E2E

使用同一个 Strategy FULL 目标分别运行：

1. PaperAdapter
2. mock LiveAdapter

验收两条链路生成相同结构的 Order、Fill、Position 和 EquityPoint，并能由相同前端 API 查询。

此外必须运行现有真实 Testnet smoke，覆盖 Binance/OKX 的 submit、query、private stream、
sync、restart 和 cleanup。该验收需要显式凭据和确认变量，不作为无凭据 CI 的静默替代品。

## 17. 破坏式升级与回滚

项目无需数据迁移，但 Schema/Proto 重命名必须按破坏式升级发布：

1. 停止 StrategyRunner 产生新目标。
2. 暂停所有 LogicalAccount。
3. 取消 Live 活动订单并处理非零敞口；未处理完不得升级。
4. 停止 Strategy 和 Trade。
5. 对 Trade SQLite 执行一致备份，并保留对应旧二进制和 Web 制品。
6. 部署整套新 Trade、Strategy、Admin 路由和 Web；重建新的 Trade SQLite。
7. 执行 Paper E2E、mock Live E2E 和真实 Testnet smoke。

回滚时停止新进程，恢复旧二进制、旧 Web 和备份数据库。不得用新二进制打开旧库，也不得用旧二进制
打开新库；`validateExistingTradeSchema` 应继续拒绝结构不匹配。

## 18. 简洁性约束

- 只有一个 Trade 二进制。
- 只有一个 Trade SQLite。
- 只有一套应用服务和事实表。
- 核心 Order/Fill/Position reducer 不知道 Live/Paper；模式差异由 ExecutionBundle 注入。
- PaperMatcher 只有一个进程内 worker。
- 资金曲线只有一个 EquitySampler。
- Paper 账户和 PaperConfig 创建后不可修改或重置。
- 一个 Paper LogicalAccount 固定一个 Paper TradingAccount。
- 模拟盘不实现盘口、部分成交、撮合优先级或独立账本。
