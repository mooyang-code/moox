# Trade 实盘与模拟盘统一执行设计

> 状态：设计已确认（第三版）
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

### 1.1 设计原则与取舍

本设计按个人、单机、少量账户的真实使用规模取舍：

1. **先保证交易正确，再减少组件。** 资金预占、订单状态、成交归并和持仓更新必须原子化；订阅 Ready
   必须反映真实可执行状态。这些边界直接决定是否会重复下单、透支或丢失成交，不能为了少写代码而省略。
2. **模式差异停留在执行边界。** Live/Paper 共用 OrderService、状态机、reducer、查询和页面；
   行情来源、执行适配器、预占策略和账户事件来源由 ExecutionBundle 注入。
3. **单进程问题用单进程方案。** 使用 SQLite 事务、进程内账户锁、一个 PaperMatcher 和一个
   EquitySampler 队列；不引入分布式锁、独立撮合服务或跨进程一致性协议。
4. **历史不可抹除，实验可以替换。** PaperConfig 不更新，已经产生的 Fill 和历史曲线不删除、
   不改写；新实验创建新账户，旧实验通过不可逆 DISABLED 结束。
5. **破坏式升级代替兼容层。** Trade 和 Strategy 一次停机后同时重建，事件流同时清空；
   不实现旧 Schema 迁移、双读、事件 generation 或 Runner 自动重绑。
6. **安全开关语义单一。** `live_trading_enabled` 只表达“是否允许 Production 新订单”；
   关闭时允许观察和撤单，不额外实现容易误判的 EXIT_ONLY 通道。
7. **只实现当前需要的能力。** V1 不增加 Archive、Paper 重新启用、自动回滚、盘口或部分成交模拟。

选择这些约束，是为了把代码集中在会影响交易结果的正确性上。个人量化可以接受停机重建、手工重建
少量配置和串行 worker，不能接受资金重复扣减、订单永久悬挂或账户错误地进入 Ready。

## 2. 目标

1. 实盘和模拟盘共用订单、安全校验、目标收敛、成交归并、持仓、查询和前端主流程。
2. 只有执行边界不同：实盘发送到真实交易所，模拟盘发送到进程内虚拟执行端。
3. 用户创建模拟账户时设置初始结算资金、maker/taker 费率和滑点。
4. 模拟盘支持 MARKET 和简化 LIMIT 撮合。
5. Live/Paper 使用相同的 Order、Fill、Holding/Position 和 EquityPoint 数据结构。
6. Live/Paper 使用同一组 API 和页面。
7. Trade 保持单进程、单 SQLite，不增加独立 Paper 服务。
8. Paper 账户标识和经济参数创建后不可修改、重置或删除；生命周期只允许一次
   `ENABLED -> DISABLED`。

## 3. 非目标

- Paper 不实现盘口深度、排队优先级或部分成交；Live 继续归并交易所返回的部分成交。
- 不实现分布式锁、主从复制、跨进程调度或全局 exactly-once。
- 不拆分独立订单、风控、Paper 或资金服务。
- 不引入双式账本；Paper 资金由初始资金和 Fill 事实重建。
- 不让浏览器或 Paper Matcher 直接消费 JetStream；内存信号只负责唤醒。
- 不支持 Paper 账户配置更新、事实清空、Reset 或复用历史账户启动新实验。
- 不实现 EXIT_ONLY；Production 开关关闭后不允许任何新订单，包括清仓订单。
- 不实现旧 Trade/Strategy Schema 迁移、事件 generation、Runner 自动重绑或跨版本数据转换。
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
- EquitySampler 的周期采样使用 tRPC Timer，不自建 ticker。
- PaperConfig 创建后不可修改。
- Paper LogicalAccount 固定只有一个 Paper TradingAccount 成员，创建后不更换成员。
- 新一轮模拟交易创建新的 Paper TradingAccount 和新的 Paper LogicalAccount。
- Paper 模拟通过不可逆 `ENABLED -> DISABLED` 结束；DISABLED 后只保留历史查询。
- `live_trading_enabled=false` 时，Production 只允许同步、查询和撤单。
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
Binance / OKX REST + 账户事件流           MarketDataSource + PaperMatcher
  |                                           |
  v                                           v
规范化 Live 回报                         MatchOrder + OPEN/version CAS
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
Order / Fill / Holding / Position / EquityPoint
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

type QuoteKey struct {
    Exchange       Exchange
    MarketType     MarketType
    ExchangeSymbol ExchangeSymbol
}
```

应用层和 Strategy 只传 `InstrumentID`；`InstrumentResolver` 在账户、Exchange、行情环境和
MarketType 上下文中解析 `ExchangeSymbol`。ExecutionAdapter 只接收 `ExchangeSymbol`。

ExecutionBundle 中的 MarketDataSource 实例绑定一个 Exchange 和 MarketType，因此 `GetQuote`
只需接收 ExchangeSymbol。PaperMatcher 跨账户合并请求或缓存报价时必须使用完整 QuoteKey，
不能只按 Exchange + ExchangeSymbol 合并。Binance SPOT 和 SWAP 都可能使用 `BTCUSDT`，
但对应不同公共行情接口。

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
}
```

`OrderRequest` 在执行边界只携带 `ExchangeSymbol`；canonical `InstrumentID` 已保存在本地
Order 事实中，不由 Exchange 适配器解释。

- `LiveAdapter` 封装 Binance/OKX 的真实执行接口。
- `PaperAdapter` 使用 SQLite 中的共享订单事实和进程内 `PaperMatcher`。
- Live 回报通过账户事件流进入共用 reducer；Paper 撮合直接调用共用事务 reducer。

### 6.3 AccountEventSource

账户订单、成交、持仓和余额的实时推送使用可选接口：

```go
type AccountEventSource interface {
    Subscribe(context.Context, AccountEventHandler) error
}

type AccountEventHandler interface {
    OnSubscribed()
    OnOrder(context.Context, Order) error
    OnFill(context.Context, Fill) error
    OnPosition(context.Context, Position) error
    OnAccountSnapshot(context.Context, AccountSnapshot) error
}
```

接口名已经限定“账户事件来源”，因此成员函数只叫 `Subscribe`，不重复 `AccountEvents`。

- Binance/OKX LiveAdapter 实现 `AccountEventSource`。
- PaperAdapter 不实现；PaperMatcher 直接调用共用 SQLite reducer。
- TradingSession 仅在 ExecutionBundle 提供 `AccountEvents` 时启动订阅。
- Paper TradingSession 加载 instrument、重建 AccountState，并确认 `matcher_ready` 后直接 Ready；
  不创建伪事件流或伪订阅 ACK。
- `AccountEventSource` 完成连接和交易所订阅确认后调用 `OnSubscribed`；该信号只表示事件流已建立，
  不表示 TradingAccount 已 Ready。
- TradingSession 先加载并持久化 instrument metadata，再启动 `Subscribe`。订阅前发生的变化由
  后续 REST 快照和成交补拉覆盖，因此不再保留单独的 metadata gate 接口。
- handler 从订阅启动时开始缓冲事件。Session 收到 `OnSubscribed` 后拉取 REST 快照，提交快照，
  再按顺序回放缓冲事件；全部成功后才把 TradingAccount 设为 Ready。
- `Subscribe` 持续运行到 context 取消或连接失败；意外返回立即把账户设为 Not Ready。
  周期 REST 同步负责对账，不能代替订阅就绪握手。

### 6.4 ExecutionBundle 与 ReservationPolicy

`ExecutionFactory` 返回完整的账户执行依赖，不只返回下单接口：

```go
type ExecutionBundle struct {
    Adapter            ExecutionAdapter
    AccountEvents      AccountEventSource // Paper 为 nil
    MarketData         MarketDataSource
    ReservationPolicy ReservationPolicy
    InstrumentResolver InstrumentResolver
}

type ReservationFacts struct {
    AvailableByAsset           map[string]Decimal
    AvailableFunds             Decimal
    SignedPositionQuantity     Decimal
    AvailableReducibleQuantity Decimal
    Leverage                   Decimal
}

type ReservationPolicy interface {
    Evaluate(TradingAccount, Instrument, OrderSpec, MarketQuote, ReservationFacts) (Reservation, error)
}
```

ReservationPolicy 是纯计算接口，不持有 Store，也不发起数据库查询。OrderService 持有账户锁并
开启一个 Store.Transaction，在该事务内通过 `*store.Tx` 加载 ReservationFacts，再调用 Policy，
最后创建携带 Reservation 的 Order：

```text
Store.Transaction
  -> tx.LoadReservationFacts(account, instrument, order_spec)
  -> reservation_policy.Evaluate(account, instrument, order_spec, quote, facts)
  -> tx.CreateOrder(order_with_reservation)
```

OrderService 在事务外完成 instrument 解析、报价请求和 freshness 校验，避免持有 SQLite 事务执行
网络 I/O；进入事务后只加载本地 Facts、执行纯计算并创建 Order。

`ReservationFacts.AvailableByAsset` 和 `AvailableFunds` 都是扣除现有活动预占后的净可用量：

- Live facts 使用交易所快照减去 `GetUnreflectedReservation`。
- Paper facts 使用初始资金和 Fill 重建余额，再减去全部活动订单 reservation；Paper 不调用
  `GetUnreflectedReservation`。

Paper Swap reduce-only 还必须预占“可减仓容量”。`AvailableReducibleQuantity` 是针对当前
account、instrument 和订单 side 的非负数量：

```text
available_reducible_quantity =
  max(0, abs(signed_position_quantity)
         - sum(active_reduce_only_order.remaining_quantity))

remaining_quantity = order.quantity - order.filled_quantity
```

只有 SELL 减少多仓或 BUY 减少空仓时该值才可能大于 0。求和范围包括同方向的全部非终态
reduce-only 订单，包括 CANCELING 和 CANCEL_UNKNOWN；不能使用只表示手续费的
`remaining_reserved_quantity`。OrderService 在同一事务中加载这些订单，新订单尚未创建，
因此不会重复扣除自身。

Policy 只根据传入事实计算所需预占并判断余额，不得先按 Facts 扣减后再套用另一套余额算法：

- Spot MARKET BUY 按持久化成交价加 taker fee 预占结算资产；GTC LIMIT BUY 按
  `limit × quantity × (1 + max(maker_fee_rate, taker_fee_rate))` 预占。
- Spot SELL 预占基础资产 quantity。
- Paper Swap 只为增加敞口的部分预占
  `worst_notional / leverage + worst_notional × fee_rate`；reduce-only 部分只预占手续费，
  并要求 `order.quantity <= available_reducible_quantity`。超出时拒绝 Place，避免多个活动订单
  合计穿过零仓位。

事务内禁止通过普通 Store 查询；当前 SQLite 只有一个连接，嵌套普通查询会等待事务自己占用的连接。
账户锁负责串行化同账户的本地下单与 Paper Match。Reservation 结果随 Order 持久化，后续
Fill/Cancel 只消费或释放，不重新估算。

MARKET 的 `reference_price`、`reference_price_at` 和应用滑点后的成交价随 Order 持久化。
MatchOrder 必须使用该成交价，保证实际成交金额不超过已预占金额。LIMIT 延迟成交以 limit 作为
最坏名义价值，因此行情穿价后仍不会突破预占。

## 7. 代码组织

```text
modules/trade/internal/execution/
  adapter.go
  marketdata.go
  factory.go
  paper/
    adapter.go
    matcher.go
    account_state.go

modules/trade/internal/exchange/
  binance/  # 保留现有真实 Exchange 实现
  okx/      # 保留现有真实 Exchange 实现
```

现有 `application/order`、`application/target`、`application/operator`、
`application/accountsync` 和 `domain/order` 保持共用，不复制 Paper 版本。
Binance/OKX 文件不做无收益搬迁；它们直接实现 `execution.ExecutionAdapter`。
`account_state.go` 定义 `AccountState`，只负责根据初始资金、Fill、活动 reservation 和报价
计算 Paper 余额、持仓和 equity；不再使用容易与 LogicalAccount 混淆的 Portfolio 命名。

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
- Paper TradingAccount 只允许一次 `ENABLED -> DISABLED`；通用账户更新接口不得重新启用它。
  复用 DISABLED 可以避免新增只服务 V1 的 CLOSED 状态，同时保留不可逆结束语义。

### 8.1 settlement_asset

`settlement_asset` 是账户统一的结算、资金和估值资产，例如 `USDT`：

- Spot：初始资金以它计价；买卖使用以它为报价资产的标的；账户 equity 也换算到该资产。
- Swap：保证金、手续费、已实现盈亏和未实现盈亏都以它结算。
- PaperConfig 的 `initial_balance` 必须以它为单位。
- 同一 LogicalAccount 的所有成员必须使用相同 `settlement_asset`，才能直接聚合资金和曲线。

它不是标的基础资产。例如 `BTC-USDT-SPOT` 中 BTC 是基础资产，USDT 才是 settlement asset。

### 8.2 slippage_bps

`slippage_bps` 表示 Paper MARKET 的不利滑点，单位是 basis point：

```text
1 bps = 0.01% = 0.0001
5 bps = 0.05%
```

若行情为 50000、配置为 5 bps：

```text
BUY  = 50000 × (1 + 5/10000) = 50025
SELL = 50000 × (1 - 5/10000) = 49975
```

滑点不是手续费。MARKET 应用滑点后再单独计算 taker fee；LIMIT 使用不劣于 limit 的
可执行报价，不允许滑点突破用户限价。

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

Paper Order 额外持久化提交报价、计算后的 MARKET 成交价和“首次撮合待处理”标记。该标记使
MARKET、IOC/FOK 和可立即成交 LIMIT 在 wake 丢失或进程重启后仍能恢复首次决策；它不是内存状态。

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
  unrealized_pnl (nullable)
  source_time
  updated_at

t_logical_account_equity_points
  space_id
  logical_account_id
  bucket_time
  equity
  available_funds
  used_margin
  unrealized_pnl (nullable)
  source_time
  updated_at
```

账户点唯一键为 `(space_id, trading_account_id, bucket_time)`；LogicalAccount 点唯一键为
`(space_id, logical_account_id, bucket_time)`。同一分钟只在新点的 `source_time` 不早于旧点时
执行 upsert，防止较慢的旧采样覆盖 Fill 后的新快照。

Sampler 在采样当时按成员关系生成 LogicalAccount 点并持久化，从而冻结历史成员语义。
任一启用成员缺少有效快照时，不写该 LogicalAccount 分钟点，不能把缺失成员按 0 计入。

`unrealized_pnl` 表示当前仍未平仓或未卖出敞口按最新价格计算的浮动盈亏：

```text
Swap 多仓 = (mark_price - entry_price) × quantity
Swap 空仓 = (entry_price - mark_price) × abs(quantity)
```

Swap 可以直接使用交易所或 Paper 持仓快照。Paper Spot 从完整 Fill 历史计算成本价，因此也能计算；
Live Spot 的外部存量资产可能没有成本价，此时字段必须为 null，不能伪造为 0。LogicalAccount
只在所有成员都提供该值时求和，否则组合 `unrealized_pnl` 为 null；这不影响 equity 曲线本身。

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

- `Run` 持续运行 worker；`Scan` 扫描一轮候选订单；`MatchOrder` 原子处理其中一笔订单。
- 首次决策处理所有 OPEN 且“首次撮合待处理”的 Paper 订单，不限订单类型或 FillPolicy。
- MARKET 使用提交时持久化的成交价并全量成交。
- LIMIT 首次使用提交报价判断：可成交则按 TAKER 成交；GTC 不可成交则清除首次标记并保持 OPEN；
  IOC/FOK 不可成交则取消。
- 首次决策完成后，只继续扫描 OPEN GTC。
- 默认每秒扫描一次。
- 周期扫描同时覆盖丢失的首次 wake 和延迟 GTC；按
  Exchange + MarketType + ExchangeSymbol 合并新报价请求和缓存。
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
  -> PENDING / SUBMITTING / OPEN + first_match_pending 持久化
  -> PaperAdapter 返回接受结果
  -> 释放账户锁
  -> Matcher wake（可合并、可丢失，周期扫描兜底）
  -> MatchOrder SQLite 事务
       Reload OPEN order + expected version
       Recheck reduce-only position capacity
       Insert Fill or cancel whole order
       Update Order terminal state
       Update Position / Holding facts
       Consume / release reservation
```

协议要求：

- PaperAdapter 只返回接受结果，不同步回调 Fill。Matcher 可以提前收到 wake，但必须等待
  OrderService 释放账户锁后执行 MatchOrder。
- PaperMatcher 只处理 SQLite 中的候选订单。首次 wake 丢失时，周期扫描仍处理 MARKET、
  IOC/FOK 和首次 LIMIT；重启不会留下永久 OPEN 的即时订单。
- MatchOrder 在同一个 SQLite 事务内重新读取 Order，并校验
  `order_id + expected_version + OPEN`。CAS 不得先提交；Cancel 和 Match 只有一个事务成功。
- 对 reduce-only Swap Order，MatchOrder 在插入 Fill 前重新读取最新 Position，并校验订单 side
  仍在减仓且 `order.remaining_quantity <= abs(current_position)`。这里使用实际仓位，不重复扣除
  当前候选订单；一个 Match 提交仓位后，下一个候选再读取更新后的仓位。
- 若最新仓位无法容纳整笔 reduce-only 剩余数量，MatchOrder 在同一事务中把 Order 置为 CANCELED
  并释放手续费 reservation，不插入 Fill。Paper 不为此场景引入部分成交。
- Fill 插入、Order 终态、Holding/Position 和 reservation 更新复用 FillReducer 的事务内核心函数。
  现有 `ApplyFill` 只负责为 Live 路径包裹外层事务，Paper 不能在 MatchOrder 内再次开启事务。
- Fill 来源增加 `PAPER_MATCHER`，与 `ACCOUNT_EVENT`、`REST_SYNC` 一起进入同一归一化校验和指标维度。
- GTC 首次不成交时，清除首次撮合标记并更新 Order version；IOC/FOK 首次不成交时，在同一事务
  进入 CANCELED 并释放 reservation。
- 进程崩溃前未提交的 Match 不产生事实；已提交事实不依赖内存事件补写。
- OPEN GTC 订单在重启后由周期扫描继续撮合；所有报价请求都在事务外执行，事务内只验证候选版本
  并提交确定的撮合结果。
- `client_order_id`、`order_id` 和 `fill_id` 保持确定性与幂等。

PaperMatcher 是 Paper 执行所必需的唯一 worker。worker 启动成功后设置 `matcher_ready=true`；
意外退出立即清零该状态，并使所有启用的 Paper TradingAccount 变为 Not Ready。OrderService
据此拒绝新 Paper Submit，避免接受永远无法撮合的订单。V1 不增加复杂 supervisor 状态机。

## 12. EquitySampler

`EquitySampler` 同时服务 Live/Paper：

- 每分钟采样所有启用且 Ready 的 TradingAccount。
- Fill 成功入库后 enqueue 对应账户，立即刷新当前分钟。
- Spot 统一按“结算资产余额 + 非结算资产余额 × 新鲜报价”估值，解决部分真实交易所
  Spot 快照不直接返回 equity 的问题。
- Swap 优先使用账户快照 equity、available funds、used margin 和 unrealized PnL。
- Live/Paper 都通过同一个估值函数生成 EquityPoint；Paper 不保留私有曲线算法。
- 账户点写入后，Sampler 使用采样当时的成员集合生成并持久化 LogicalAccount 点。
- 任一成员 Not Ready、报价过期或采样失败时，跳过本次 LogicalAccount 点并记录指标。

周期采样使用 tRPC Timer：

```yaml
- name: trpc.moox.trade.equity_sample.timer
  port: 11210
  network: "0 * * * * *"
  protocol: timer
  timeout: 30000
```

Timer Handler 通过 `timerjob.Job` 复用项目现有的执行超时和 Timer 防重入能力，并调用
`EquitySampler.EnqueueReadyAccounts`。`timerjob.Job` 不负责协调 Fill/Ready wake。

EquitySampler 内部只有一个进程内队列和一个 worker。Timer、Fill 和 Session Ready 都只 enqueue
`trading_account_id`；队列按账户合并重复请求，worker 串行读取最新事实、计算账户点，再更新相关
LogicalAccount 点。这个全局串行模型符合个人账户规模，也消除了三种入口并发覆盖的问题。

配置不附加 `?startAtOnce=1`。账户 Session 首次 Ready 时 enqueue 首次采样，避免 Timer 在服务尚未
Ready 时阻塞启动。同一分钟 upsert 继续使用 `source_time` 单调条件作为最后一道保护。

Sampler 失败不影响下单，只记录 degraded 健康状态、失败计数和最后错误；下一次 wake 或 Timer
继续重试。资金曲线是观测能力，PaperMatcher 才是执行可用性的硬依赖。

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
4. 创建新模拟不会自动关闭其他模拟；个人可以按需并行运行少量 Paper 实验。

结束模拟使用 `ClosePaperSimulation`：

1. 前端先禁用或改绑关联的 StrategyRunner。
2. Trade 获取 Paper TradingAccount 和 LogicalAccount 的执行锁。
3. 在一个 SQLite 事务中取消全部活动 Paper 订单、释放 reservation、把 TradingAccount 设为
   DISABLED，并把 LogicalAccount 固定为 PAUSED。
4. 关闭后拒绝 Place、Submit、ClaimOwner 和 Resume；迟到的 Strategy target 直接拒绝。
5. Session、PaperMatcher 和 EquitySampler 只处理 ENABLED 账户；历史订单、成交、持仓和曲线
   继续查询。

Paper 的 DISABLED 是不可逆终态，通用更新接口不能重新启用。复用现有状态可以避免引入 CLOSED、
ARCHIVED 和额外状态机。V1 不提供删除或归档；历史数量影响使用时再单独设计查询归档。
以上不可删除约束适用于同一 Schema 版本的产品生命周期；第 17 节的一次性破坏式升级可以整体丢弃旧库。

`AddLogicalAccountMember` 和 `RemoveLogicalAccountMember` 对 Paper LogicalAccount 返回拒绝；
Live LogicalAccount 保留现有 PAUSED 状态下的成员管理能力。

## 14. API 与前端

浏览器统一使用 `TradeConsoleService`：

```text
CreateTradingAccount
CreatePaperSimulation
ClosePaperSimulation
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

`ClosePaperSimulation` 只接受 Paper 账户：ENABLED 时执行不可逆关闭，DISABLED 时幂等返回当前
结果，不重复产生取消事实。Live 账户继续使用现有账户管理和 Operator 流程。

交互原则：

- 不创建两套交易页面。
- 创建账户时根据模式展示 LiveConfig 或 PaperConfig。
- 所有列表和详情返回 `execution_mode`，仅用于标签和能力展示。
- Paper/Live 共用订单、成交、持仓和资金曲线组件。
- 下单只提交 canonical `instrument_id`，服务端解析 Exchange symbol。
- `CreatePaperSimulation` 原子创建不可变 Paper TradingAccount 和单成员 Paper LogicalAccount。
- `ClosePaperSimulation` 结束实验但保留全部历史。
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

- 每次 OrderService Place 和 Submit。
- `GetExecutionCapabilities` 对浏览器返回不可下单原因。

关闭开关时允许创建、启用和同步 Production Live TradingAccount，也允许查单和撤单；禁止人工下单、
Target 收敛、Flatten 和其他任何新 Production Order。需要通过 MooX 清仓时，用户必须显式重新开启
开关；紧急情况下也可以直接使用交易所控制台。

V1 不实现 EXIT_ONLY。可靠的 EXIT_ONLY 必须服务端核验 Spot 持有量、Swap 方向、数量上限、
穿零风险和审计来源，不能信任调用方传入的 `reduce_only`。对个人项目而言，这套旁路会扩大安全面，
却不能替代交易所控制台。TESTNET 不受该开关限制。

## 16. 测试与验收

### 16.1 分层契约

幂等权威保留在 OrderService，不下沉到 ExecutionAdapter：

- OrderService 测试相同 `client_order_id` 的 Place 幂等、参数冲突、重复 Submit/Cancel 和重启恢复。
- ExecutionAdapter 测试稳定传递 client ID、请求字段映射、错误归类、GetOrder、ListOpenOrders
  和 UNKNOWN 后通过查询恢复。
- Binance/OKX Adapter 每次调用可以发送真实 HTTP 请求，不要求重复 POST/DELETE 返回相同结果。
- AccountEventSource 测试订阅确认、启动缓冲、快照后回放和断线立即 Not Ready。
- PaperAdapter 测试确定性接受；PaperMatcher 测试 MatchOrder、CAS 和共用 reducer 原子提交。

Live/Paper 最终都生成同结构的本地 Order、Fill、Holding/Position 事实，但不强制使用同一种传输机制。

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
- ReservationFacts 仅通过当前 `*store.Tx` 加载；Policy 不访问普通 Store
- Paper Policy 替换 Live `GetUnreflectedReservation`，不会重复扣减
- Swap 增仓保证金与手续费预占；reduce-only 只预占手续费
- 1 BTC 多仓已有 0.8 BTC reduce-only SELL 时，第二笔 0.8 BTC 在 Place 阶段被拒绝
- 多笔活动 reduce-only 的剩余数量合计不超过当前可减仓数量
- reduce-only 接受后仓位发生变化时，MatchOrder 整单取消、不写 Fill 并释放手续费 reservation
- MARKET 使用持久化成交价，实际扣减不超过 reservation
- Binance SPOT/SWAP 使用相同 ExchangeSymbol 时分别请求、缓存并返回各自报价
- 首次 MatchOrder 覆盖 MARKET、LIMIT、IOC 和 FOK；丢失 wake 后可恢复
- Match/Cancel 使用 version CAS，只有一方提交
- CAS、Fill、Order、Holding/Position、reservation 同事务
- 注入任一步失败后整个 Match 事务回滚
- PaperConfig 不可更新，新的模拟运行创建新账户和新 LogicalAccount
- ClosePaperSimulation 原子取消活动订单并永久 DISABLED
- Matcher 停止后 Paper Not Ready，拒绝新 Submit
- tRPC equity timer、Session Ready 和 Fill wake 进入同一个串行队列
- 较旧 `source_time` 不能覆盖同分钟的新快照
- Live Spot 成本未知时 unrealized_pnl 为 null
- LogicalAccount 曲线不受后续成员变化影响

### 16.3 E2E

使用同一个 Strategy FULL 目标分别运行：

1. PaperAdapter
2. mock LiveAdapter

Spot 验收两条链路生成相同结构的 Order、Fill、Holding 和 EquityPoint；Swap 验收 Order、Fill、
Position 和 EquityPoint。两种 market type 都必须通过相同前端 API 查询。

此外必须运行现有真实 Testnet smoke，覆盖 Binance/OKX 的 submit、query、account event stream、
sync、restart 和 cleanup。该验收需要显式凭据和确认变量，不作为无凭据 CI 的静默替代品。

## 17. 破坏式升级与回滚

本项目不迁移旧运行数据。Trade 和 Strategy 作为一组执行破坏式升级，避免旧
`logical_account_id`、Strategy outbox 和 JetStream durable 指向已经重建的 Trade 事实。

升级步骤：

1. 保持 `live_trading_enabled=true`，停止所有 StrategyRunner，暂停 LogicalAccount。
2. 取消 Production 活动订单并清理非零敞口；未处理完不得继续。
3. 等待 Strategy pending outbox 清空，再停止 Strategy、Trade 和 outbox relay，确认不再产生目标事件。
4. 一致备份 Trade SQLite、Strategy SQLite、旧二进制和 Web 制品。备份只用于整组回滚，
   不向新库导入旧行。
5. 删除并重建 Trade SQLite 和 Strategy SQLite。旧账户、模拟历史、策略、Runner 和执行结果
   都不迁移；需要的少量策略和配置由用户重新创建。
6. 删除并重建 `MOOX_TRADE` JetStream Stream 及 `trade_target_v1` consumer。该 Stream 当前只承载
   `event.trade.target.requested`，因此可以直接清空，不增加 event generation 或 fencing 协议。
7. 部署同一版本的 Trade、Strategy、Admin 路由和 Web，并保持
   `live_trading_enabled=false`。
8. 重新创建 TradingAccount、LogicalAccount、Strategy 和 StrategyRunner，再建立新的
   `logical_account_id` 关联。
9. 执行 Paper E2E、mock Live E2E 和真实 Testnet smoke；全部通过后才允许开启 Production。

这套流程用一次停机和少量手工重建换掉跨库迁移、Outbox 转换、Runner 自动重绑和旧事件兼容。
个人部署的对象数量有限，手工重建成本低于长期维护兼容代码。

自动回滚只支持“新版本尚未产生任何 Production Order”的阶段：停止新进程，恢复成对备份的
Trade/Strategy SQLite、旧二进制和旧 Web，并重建空的 `MOOX_TRADE` Stream。新版本一旦产生
Production Order，就禁止直接恢复旧数据库；此时关闭 Production 下单、撤销活动订单、从交易所
对账并修复前进。`validateExistingTradeSchema` 继续拒绝新旧二进制打开不匹配的数据库。

## 18. 简洁性约束

- 只有一个 Trade 二进制。
- 只有一个 Trade SQLite。
- 只有一套应用服务和事实表。
- 核心 Order/Fill/Position reducer 不知道 Live/Paper；模式差异由 ExecutionBundle 注入。
- PaperMatcher 只有一个进程内 worker。
- 资金曲线只有一个串行 EquitySampler worker；周期触发复用 tRPC Timer，不自建调度器。
- Paper 账户标识和 PaperConfig 创建后不可修改或重置；生命周期只允许不可逆 DISABLED。
- Paper 复用不可逆 DISABLED，不增加 CLOSED 或 Archive 状态机。
- 一个 Paper LogicalAccount 固定一个 Paper TradingAccount。
- Paper 不实现盘口、部分成交、撮合优先级或独立账本；Live 保留真实部分成交归并。
- Production 开关关闭时不实现 EXIT_ONLY。
- Trade、Strategy 和目标事件流整体重建，不实现迁移、generation 或自动重绑。

简洁性不能越过三条正确性底线：Match/CAS/Fill/持仓/reservation 同事务，Paper 与 Live
reservation 不重复计算，Live 订阅完成快照和缓冲回放后才 Ready。
