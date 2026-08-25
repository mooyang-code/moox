# Trade 实盘与模拟盘统一执行设计

> 状态：设计已确认
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

1. 实盘和模拟盘共用订单、风控、目标收敛、成交归并、持仓、查询和前端主流程。
2. 只有执行边界不同：实盘发送到真实交易所，模拟盘发送到进程内虚拟执行端。
3. 用户创建模拟账户时设置初始结算资金、maker/taker 费率和滑点。
4. 模拟盘支持 MARKET 和简化 LIMIT 撮合。
5. Live/Paper 使用相同的 Order、Fill、Position 和 EquityPoint 数据结构。
6. Live/Paper 使用同一组 API 和页面。
7. Trade 保持单进程、单 SQLite，不增加独立 Paper 服务。

## 3. 非目标

- 不实现盘口深度、排队优先级或部分成交。
- 不实现分布式锁、主从复制、跨进程调度或全局 exactly-once。
- 不拆分独立订单、风控、Paper 或资金服务。
- 不引入双式账本；Paper 资金由初始资金和 Fill 事实重建。
- 不让浏览器或 Paper Matcher 直接消费 JetStream。
- 本设计不解决 Strategy 自动读取 View 并触发 `RunOnce` 的上游调度问题。

## 4. 已确认的产品决策

- Paper 撮合采用简化模型：
  - MARKET 按最新可执行报价加减滑点后立即全量成交。
  - LIMIT 在提交时可成交则立即成交；否则进入 OPEN，行情穿价后全量成交。
  - 不模拟盘口和部分成交。
- Paper 分别配置 `maker_fee_rate` 和 `taker_fee_rate`。
- Paper 只配置结算资产初始资金，不导入初始持仓。
- 资金曲线每分钟采样，并在 Fill 入库后立即刷新当前分钟快照。
- 重置以整个 Paper LogicalAccount 为单位，清空全部成员事实和当前目标。

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
ExecutionVenue
  +--------------------+----------------------+
  |                                           |
  v                                           v
LiveVenue                                 PaperVenue
Binance / OKX REST + 私有流               MarketDataSource + PaperMatcher
  |                                           |
  +--------------------+----------------------+
                       |
                       v
统一 OrderEvent / FillEvent / AccountSnapshot
                       |
                       v
AccountSync + FillReducer
                       |
                       v
Order / Fill / Position / EquityPoint
```

应用层不得根据 `execution_mode` 分叉订单或持仓流程。模式判断只允许出现在 `VenueFactory` 装配阶段。

## 6. 核心端口

### 6.1 MarketDataSource

```go
type MarketDataSource interface {
    LoadInstruments(context.Context) ([]Instrument, error)
    GetQuote(context.Context, string) (MarketQuote, error)
}
```

`MarketQuote` 至少包含 `bid`、`ask`、`last` 和 `source_time`。当数据源没有 bid/ask 时，
允许回退到 last，但必须保留来源时间并执行 freshness 校验。

Paper V1 固定使用所选 Exchange 的生产公共行情，不需要交易凭据。

### 6.2 ExecutionVenue

```go
type ExecutionVenue interface {
    GetAccountSnapshot(context.Context) (AccountSnapshot, error)
    ListPositionSnapshots(context.Context) ([]Position, error)
    ListOpenOrders(context.Context) ([]Order, error)
    ListRecentFills(context.Context, string, string) ([]Fill, string, error)
    GetOrder(context.Context, string, string) (Order, error)
    PlaceOrder(context.Context, OrderRequest) (Order, error)
    CancelOrder(context.Context, string, string) (Order, error)
    SetLeverage(context.Context, string, Decimal) error
    SetMarginMode(context.Context, string, MarginMode) error
    SubscribePrivate(context.Context, EventHandler) error
}
```

- `LiveVenue` 封装 Binance/OKX 的真实执行接口。
- `PaperVenue` 使用 SQLite 中的共享订单事实和进程内 `PaperMatcher`。
- 两者通过相同 `EventHandler` 发送 Order/Fill 回报。

## 7. 代码组织

```text
modules/trade/internal/venue/
  venue.go
  marketdata.go
  factory.go
  live/
    binance/
    okx/
  paper/
    venue.go
    matcher.go
    portfolio.go
```

现有 `application/order`、`application/target`、`application/operator`、
`application/accountsync` 和 `domain/order` 保持共用，不复制 Paper 版本。

## 8. 账户模型

`ExchangeAccount` 重命名为 `TradingAccount`。

```protobuf
message TradingAccount {
  string trading_account_id = 1;
  string name = 2;
  Exchange exchange = 3;
  MarketType market_type = 4;
  ExecutionMode execution_mode = 5;
  string settlement_asset = 6;
  string status = 7;
  oneof execution_config {
    LiveConfig live = 8;
    PaperConfig paper = 9;
  }
  TradingAccountSnapshot snapshot = 10;
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
- `slippage_bps >= 0`
- Live 必须有 Secret；Paper 禁止配置 Secret。
- Paper 配置创建后不可直接修改，只能通过 LogicalAccount 级重置替换。

## 9. 持久化

### 9.1 账户

- `t_trading_accounts`：Live/Paper 共用账户主体。
- `t_paper_account_configs`：Paper 一对一配置。

### 9.2 执行事实

以下表由 Live/Paper 共用：

- `t_trade_orders`
- `t_order_fills`
- `t_exchange_positions`（实现时同步重命名为 `t_trading_positions`）

不新增 `t_paper_orders`、`t_paper_fills` 或独立资金账本。

### 9.3 资金曲线

新增 `t_account_equity_points`：

```text
space_id
trading_account_id
bucket_time
equity
available_funds
used_margin
unrealized_pnl
source_time
updated_at
```

唯一键为 `(space_id, trading_account_id, bucket_time)`。同一分钟的 timer 和 Fill wake
使用最新账户快照覆盖该分钟数据。

LogicalAccount 曲线在查询时按分钟聚合成员账户；成员已有相同 `settlement_asset` 约束，
不额外保存组合曲线。

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

不能立即成交时，订单进入 OPEN。

`PaperMatcher`：

- 只在存在 OPEN Paper 订单时运行。
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

## 11. 事件与恢复

PaperVenue 内部维护有界事件通道，实现与真实私有流相同的 `SubscribePrivate`。

```text
PaperVenue Place/Cancel/Match
  -> OrderEvent / FillEvent
  -> TradingSession
  -> AccountSync / FillReducer
```

SQLite 是权威：

- OPEN Paper 订单保存在共享订单表。
- 进程启动后 `PaperMatcher` 重新加载 OPEN Paper 订单继续撮合。
- `client_order_id`、`order_id` 和 `fill_id` 保持确定性与幂等。
- 内存 map 只能作为缓存，不能作为恢复依据。

## 12. EquitySampler

`EquitySampler` 同时服务 Live/Paper：

- 每分钟采样所有启用且 Ready 的 TradingAccount。
- Fill 成功入库后 Wake 对应账户，立即刷新当前分钟。
- Spot 统一按“结算资产余额 + 非结算资产余额 × 新鲜报价”估值，解决部分真实交易所
  Spot 快照不直接返回 equity 的问题。
- Swap 优先使用账户快照 equity、available funds、used margin 和 unrealized PnL。
- Live/Paper 都通过同一个估值函数生成 EquityPoint；Paper 不保留私有曲线算法。

Sampler 是进程内单 worker，不使用 EventBus 或分布式调度。

## 13. Paper LogicalAccount 重置

新增 `ResetPaperLogicalAccount`，要求：

- LogicalAccount 为 PAPER 且处于 PAUSED。
- 所有成员均为 Paper TradingAccount。
- `owner_runner_id` 必须为空；用户应先停用 StrategyRunner 并释放归属。
- 不存在运行中的 OperatorAction。
- 请求必须为每个启用成员提供且仅提供一份新的 PaperConfig。

操作步骤：

1. 以调用方提供的 `action_id` 幂等创建 RUNNING `RESET_PAPER` OperatorAction。
2. 撤销全部 OPEN Paper 订单。
3. 在一个 SQLite 事务中删除成员账户的 Fill、Order、Position、EquityPoint
   和该 LogicalAccount 的旧 OperatorAction，但保留当前 `RESET_PAPER` Action。
4. 清除成员 snapshot、cursor 和 readiness。
5. 清除 LogicalAccount 当前 FULL target。
6. 写入新的 PaperConfig。
7. 把当前 `RESET_PAPER` OperatorAction 更新为完成状态。
8. 重建 TradingSession，完成初始同步后 Ready。
9. LogicalAccount 最终保持 PAUSED，必须由用户主动 Resume。

## 14. API 与前端

浏览器统一使用 `TradeConsoleService`：

```text
CreateTradingAccount
GetTradingAccountOverview
GetExecutionCapabilities
ResetPaperLogicalAccount
QueryEquityCurve
ListOrders
ListFills
ListPositions
PlaceManualOrder
CancelOrder
```

交互原则：

- 不创建两套交易页面。
- 创建账户时根据模式展示 LiveConfig 或 PaperConfig。
- 所有列表和详情返回 `execution_mode`，仅用于标签和能力展示。
- Paper/Live 共用订单、成交、持仓和资金曲线组件。
- 下单只提交 canonical `instrument_id`，服务端解析 Exchange symbol。
- Paper 重置需要明确的破坏性确认，完成后保持 PAUSED。

## 15. 错误处理

- Live Place/Cancel 不自动重试；保留现有 UNKNOWN 状态恢复。
- Paper Place/Cancel 必须确定性返回，不产生 UNKNOWN。
- Paper 报价过期时：
  - MARKET 拒绝下单。
  - LIMIT 保持 OPEN，等待新鲜报价。
- Venue 错误统一转换为现有 Trade 领域错误，不向应用层暴露 Binance/OKX/Paper 私有错误。

## 16. 测试与验收

### 16.1 共用 Venue 契约

Live fake venue 与 PaperVenue 必须通过同一套测试：

- Place 幂等
- Cancel 幂等
- GetOrder
- ListOpenOrders
- Fill 回报
- 重启恢复

### 16.2 Paper 专项

- MARKET BUY/SELL 滑点
- LIMIT 立即成交与延迟成交
- maker/taker 费率
- Spot 资金与持仓
- Swap 开仓、加仓、减仓、反向与费用
- 报价过期
- 进程重启后续撮合
- LogicalAccount 重置
- 分钟采样与 Fill wake

### 16.3 E2E

使用同一个 Strategy FULL 目标分别运行：

1. PaperVenue
2. mock LiveVenue

验收两条链路生成相同结构的 Order、Fill、Position 和 EquityPoint，并能由相同前端 API 查询。

## 17. 简洁性约束

- 只有一个 Trade 二进制。
- 只有一个 Trade SQLite。
- 只有一套应用服务和事实表。
- 只有 VenueFactory 知道 Live/Paper 差异。
- PaperMatcher 只有一个进程内 worker。
- 资金曲线只有一个 EquitySampler。
- 模拟盘不实现盘口、部分成交、撮合优先级或独立账本。
