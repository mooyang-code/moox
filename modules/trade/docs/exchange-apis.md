# Trade 模块 - 交易所接口对接清单

本文档汇总 `trade` 模块对接 **Binance / OKX** 所需的交易所接口，覆盖账户、订单、成交、持仓、
通道五大能力，并给出 trade 模块内部统一的 **交易所适配抽象接口（ExchangeAdapter）**，方便后续做适配层。

参考来源：
- 本地 `gocryptotrader/exchanges/{binance,okx}`（已有封装，标注 ✅）
- Binance / OKX 官方 REST 文档（gocryptotrader 缺失项，标注 ⚠️/❌ 并给出官方端点）

约定：
- Binance 拆三套市场：**现货(Spot)** / **U本位合约(USDⓈ-M, 方法名 U 前缀)** / **币本位合约(COIN-M, 方法名 Futures 前缀)**，base path 分别 `/api`、`/fapi`、`/dapi`。
- OKX 为 v5 统一账户，单套接口靠 `instType`/`instId` 区分市场。
- ✅=gocryptotrader 已封装可直接复用；⚠️=部分覆盖需补；❌=缺失需自行封装。

---

## 一、账户 / 余额（→ `AccountSvc` / `BalanceSvc`）

| trade 能力 | Binance 现货 | Binance U本位 | Binance 币本位 | OKX | 官方端点（缺失项） |
|---|---|---|---|---|---|
| 查询账户信息 | ✅ `GetAccount` | ✅ `UAccountInformationV2` | ✅ `GetFuturesAccountInfo` | ✅ `GetAccountConfiguration` | — |
| 查询账户余额 | ✅ `GetAccount`(balances) | ✅ `UAccountBalanceV2` | ✅ `GetFuturesAccountBalance` | ✅ `AccountBalance` | OKX `GET /api/v5/account/balance` |
| 杠杆账户 | ✅ `GetMarginAccount` | — | — | (统一账户) | — |
| 手续费率 | ✅ `GetFee` | ✅ `UGetCommissionRates` | — | ✅ `GetTradeFee` | — |
| 账户风险 | — | — | — | ✅ `GetAccountAndPositionRisk` | — |

> API 凭证（`ApiKeySvc`）：交易所侧无"创建 Key"接口（网页生成），trade 仅本地**加密存储**并用于签名；
> 连通性校验（`ChannelSvc.TestChannel`）可调用任一私有接口（如查余额）实现。

---

## 二、资金流水 / 划转 / 充提（→ `FundSvc`）

| trade 能力 | Binance | OKX | 官方端点（缺失项） |
|---|---|---|---|
| 资金流水/账单 | ✅ `UAccountIncomeHistory` / `FuturesIncomeHistory` | ✅ `GetBillsDetailLast7Days` / `GetBillsDetail3Months` | — |
| 划转 | (wrapper `TransferFunds`) | ✅ `FundsTransfer` | OKX `POST /api/v5/asset/transfer` |
| 划转状态 | — | ✅ `GetFundsTransferState` | — |
| 充值记录/地址 | ✅ `DepositHistory` / `GetDepositAddressForCurrency` | ✅ `GetCurrencyDepositHistory` / `GetCurrencyDepositAddress` | — |
| 提现/记录/撤销 | ✅ `WithdrawCrypto` / `WithdrawHistory` | ✅ `Withdrawal` / `GetWithdrawalHistory` / `CancelWithdrawal` | — |

---

## 三、下单 / 撤单 / 改单 / 调杠杆（→ `TradeOpSvc`）

| trade 操作 | Binance 现货 | Binance U本位 | Binance 币本位 | OKX |
|---|---|---|---|---|
| **下单** PlaceOrder | ✅ `NewOrder`（测试 `NewOrderTest`） | ✅ `UFuturesNewOrder` | ✅ `FuturesNewOrder` | ✅ `PlaceOrder` |
| 批量下单 | — | ✅ `UPlaceBatchOrders` | ✅ `FuturesBatchOrder` | ✅ `PlaceMultipleOrders` |
| **撤单** CancelOrder | ✅ `CancelExistingOrder` | ✅ `UCancelOrder` | ✅ `FuturesCancelOrder` | ✅ `CancelSingleOrder` |
| 批量撤单 | — | ✅ `UCancelBatchOrders` | ✅ `FuturesBatchCancelOrders` | ✅ `CancelMultipleOrders` |
| **全撤** CancelAllOrders | ❌ | ✅ `UCancelAllOpenOrders` | ✅ `FuturesCancelAllOpenOrders` | ⚠️ 批量凑 |
| 倒计时全撤 | — | ✅ `UAutoCancelAllOpenOrders` | ✅ `AutoCancelAllOpenOrders` | ⚠️ 仅 Spread |
| **改单** AmendOrder | ❌ | ❌ | ❌ | ✅ `AmendOrder` / `AmendMultipleOrders` |
| **调杠杆** SetLeverage | — | ✅ `UChangeInitialLeverageRequest` | ✅ `FuturesChangeInitialLeverage` | ✅ `SetLeverageRate` |
| 调保证金模式 | — | ✅ `UChangeInitialMarginType` | ✅ `FuturesChangeMarginType` | (账户配置) |
| 调逐仓保证金 | — | ✅ `UModifyIsolatedPositionMarginReq` | ✅ `ModifyIsolatedPositionMargin` | — |
| 平仓（市价全平） | — | — | — | ✅ `ClosePositions` |
| 最大可下单量 | — | — | — | ✅ `GetMaximumBuySellAmountOROpenAmount` |

---

## 四、订单查询（→ `OrderSvc`）

| trade 能力 | Binance 现货 | Binance U本位 | Binance 币本位 | OKX |
|---|---|---|---|---|
| 单个订单详情 | ✅ `QueryOrder` | ✅ `UGetOrderData` / `UFetchOpenOrder` | ✅ `FuturesGetOrderData` / `FuturesOpenOrderData` | ✅ `GetOrderDetail` |
| 当前未结订单 | ✅ `OpenOrders` | ✅ `UAllAccountOpenOrders` | ✅ `GetFuturesAllOpenOrders` | ✅ `GetOrderList` |
| 历史订单 | ✅ `AllOrders` | ✅ `UAllAccountOrders` | ✅ `GetAllFuturesOrders` | ✅ `Get7DayOrderHistory` / `Get3MonthOrderHistory` |
| 强平订单 | — | ✅ `UAccountForcedOrders` | ✅ `FuturesForceOrders` | ✅ `GetLiquidationOrders` |

---

## 五、成交查询（→ `TradeQuerySvc`）

| trade 能力 | Binance 现货 | Binance U本位 | Binance 币本位 | OKX |
|---|---|---|---|---|
| 账户成交明细 | ❌（应用 myTrades） | ✅ `UAccountTradesHistory` | ✅ `FuturesTradeHistory` | ✅ `GetTransactionDetailsLast3Days` / `...Last3Months` |

---

## 六、持仓查询（→ `PositionSvc`）

| trade 能力 | Binance U本位 | Binance 币本位 | OKX |
|---|---|---|---|
| 当前持仓 | ✅ `UPositionsInfoV2` | ✅ `FuturesPositionsInfo` | ✅ `GetPositions` |
| 历史持仓 | — | — | ✅ `GetPositionsHistory` |
| 保证金变更历史 | ✅ `UPositionMarginChangeHistory` | ✅ `FuturesMarginChangeHistory` | — |
| ADL 预估 | ✅ `UPositionsADLEstimate` | ✅ `FuturesPositionsADLEstimate` | — |
| 杠杆档位 | ✅ `UGetNotionalAndLeverageBrackets` | ✅ `FuturesNotionalBracket` | ✅ `GetLeverageRate` / `GetPositionTiers` |

---

## 七、通道连通 / 交易规则（→ `ChannelSvc`）

| trade 能力 | Binance | OKX |
|---|---|---|
| 服务器时间 | ✅ `UServerTime` | (统一接口) |
| 交易规则/合约信息 | ✅ `GetExchangeInfo` / `UExchangeInfo` / `FuturesExchangeInfo` | ✅ `GetAccountInstruments` |
| 限频信息 | (`ratelimit.go`) | ✅ `GetTradeAccountRateLimit` |
| 连通性测试 | `NewOrderTest` 或查余额 | 查余额 |

---

## 八、需自行补封装的接口（gocryptotrader 缺失/不足）

> 这些接口 gocryptotrader 未封装或仅部分覆盖，trade 适配层需基于官方文档自行实现。

| 交易所/市场 | 缺失能力 | 官方端点 | 备注 |
|---|---|---|---|
| Binance 现货 | 全撤 | `DELETE /api/v3/openOrders` | 撤单一 symbol 全部挂单 |
| Binance 现货 | 改单 | `POST /api/v3/order/cancelReplace` | 原子"撤旧+下新" |
| Binance 现货 | 改单(保留优先级) | `PUT /api/v3/order/amend/keepPriority` | 仅改量，保留排队（2024 新增）|
| Binance 现货 | 账户成交明细 | `GET /api/v3/myTrades` | wrapper 现用 AllOrders 凑，应直连此接口 |
| Binance U本位 | 改单（原生） | `PUT /fapi/v1/order` | 合约**有原生改单**，无需撤补 |
| Binance U本位 | 批量改单 | `PUT /fapi/v1/batchOrders` | — |
| Binance 币本位 | 改单（原生） | `PUT /dapi/v1/order` | 同上 |
| Binance 币本位 | 批量改单 | `PUT /dapi/v1/batchOrders` | — |
| OKX | 按品种全撤 | `POST /api/v5/trade/cancel-all-orders` | gocryptotrader 用批量撤单凑 |
| OKX | 倒计时全撤 | `POST /api/v5/trade/cancel-all-after` | gocryptotrader 仅 Spread 版 |
| OKX | 最大可用余额 | `GET /api/v5/account/max-avail-size` | 仅有 max-size |

---

## 九、trade 模块统一适配抽象接口（ExchangeAdapter）

适配层位于 `modules/trade/internal/exchange/`，对上层 RPC（`TradeOpSvc` 等）暴露**交易所中立**接口，
对下按交易所 + 市场类型路由到上表具体方法。接口入参/出参为 trade 内部领域模型（与 proto 对齐，
金额用 `decimal`/`string`）。

```go
// Package exchange 定义交易所适配的统一抽象接口。
package exchange

import "context"

// MarketType 市场类型，用于在同一交易所内路由不同 base path（Binance 现货/U本位/币本位）。
type MarketType string

const (
    MarketSpot    MarketType = "spot"
    MarketMargin  MarketType = "margin"
    MarketSwap    MarketType = "swap"    // U本位永续
    MarketFutures MarketType = "futures" // 交割/币本位
)

// Credential 交易所 API 凭证（由 t_account_api_keys 解密后传入）。
type Credential struct {
    APIKey     string
    APISecret  string
    Passphrase string // OKX 需要
}

// ExchangeAdapter 是交易所适配统一抽象接口；每个交易所实现一份。
// 所有方法均按 (market, symbol) 路由到对应市场的底层 REST/WS 调用。
type ExchangeAdapter interface {
    // ---- 元信息 / 通道 ----
    Name() string                                                       // binance / okx
    Ping(ctx context.Context, cred Credential) (latencyMS int64, err error) // 连通性 + 签名校验
    GetInstruments(ctx context.Context, market MarketType) ([]Instrument, error)

    // ---- 账户 / 余额 ----
    GetAccountInfo(ctx context.Context, cred Credential, market MarketType) (*AccountInfo, error)
    GetBalances(ctx context.Context, cred Credential, market MarketType, currencies []string) ([]Balance, error)
    GetTradeFee(ctx context.Context, cred Credential, market MarketType, symbol string) (*FeeRate, error)

    // ---- 资金 ----
    ListFundFlows(ctx context.Context, cred Credential, req *FundFlowQuery) ([]FundFlow, error)
    Transfer(ctx context.Context, cred Credential, req *TransferReq) (*TransferResult, error)

    // ---- 下单 / 撤单 / 改单 / 杠杆 ----
    PlaceOrder(ctx context.Context, cred Credential, req *PlaceOrderReq) (*OrderResult, error)
    CancelOrder(ctx context.Context, cred Credential, req *CancelOrderReq) (*OrderResult, error)
    CancelAllOrders(ctx context.Context, cred Credential, market MarketType, symbol string) (canceled int, err error)
    AmendOrder(ctx context.Context, cred Credential, req *AmendOrderReq) (*OrderResult, error) // Binance 现货退化为撤+补
    SetLeverage(ctx context.Context, cred Credential, market MarketType, symbol, leverage string) error
    ClosePosition(ctx context.Context, cred Credential, market MarketType, symbol, posSide string) error

    // ---- 查询 ----
    GetOrder(ctx context.Context, cred Credential, req *GetOrderReq) (*Order, error)
    ListOpenOrders(ctx context.Context, cred Credential, req *ListOrdersReq) ([]Order, error)
    ListOrders(ctx context.Context, cred Credential, req *ListOrdersReq) ([]Order, error)
    ListTrades(ctx context.Context, cred Credential, req *ListTradesReq) ([]Trade, error)
    ListPositions(ctx context.Context, cred Credential, market MarketType, symbol string) ([]Position, error)
}

// PrivateStream 私有 WebSocket 回报（订单/成交/持仓/余额变更），用于实时回填本地表。
// 推荐 ws 为主、REST 查询为兜底。
type PrivateStream interface {
    // Subscribe 订阅账户私有频道；事件经 handler 回调写入 t_orders/t_trades/t_positions/t_account_balances。
    Subscribe(ctx context.Context, cred Credential, market MarketType, handler StreamHandler) error
    Close() error
}

// StreamHandler 处理私有频道推送事件。
type StreamHandler interface {
    OnOrderUpdate(evt *OrderEvent)
    OnTrade(evt *TradeEvent)
    OnPositionUpdate(evt *PositionEvent)
    OnBalanceUpdate(evt *BalanceEvent)
    OnError(err error)
}
```

### 适配层需要抹平的关键差异

1. **市场分裂**：Binance 现货/U本位/币本位是三套不同方法 + base path（`/api`、`/fapi`、`/dapi`），
   适配层按 `MarketType` 路由；OKX 单套接口靠 `instType`/`instId` 区分。
2. **改单**：OKX 原生 `amend-order`、Binance 合约原生 `PUT .../order`；
   **仅 Binance 现货**无原生改单 → `AmendOrder` 内部退化为"撤单 + 重下"（用 `client_order_id` 保幂等）。
3. **精度校验**：下单前用 `GetInstruments` 拿 tickSize/lotSize/minNotional 做本地校验，避免被交易所拒单。
4. **回报通道**：成交/订单状态优先走私有 WebSocket（`PrivateStream`）回填，REST 查询作兜底与对账。
5. **错误码归一**：各所错误码不同，适配层映射为 trade 统一错误码写入 `t_order_operations.c_error_code`。

### 落地目录建议

```
modules/trade/internal/exchange/
  exchange.go            # 本文 ExchangeAdapter / PrivateStream / 领域模型定义
  registry.go            # 按 exchange 名注册/获取 adapter
  binance/
    adapter.go           # 实现 ExchangeAdapter（路由现货/U本位/币本位）
    spot.go / ufutures.go / cfutures.go
    stream.go            # 私有 ws 回报
  okx/
    adapter.go
    stream.go
```
