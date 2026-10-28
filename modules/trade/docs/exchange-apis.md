# Exchange API 映射

Trade 应用层只依赖统一 `execution.ExecutionAdapter`；行情和账户事件分别通过
`execution.MarketDataSource`、`execution.AccountEventSource` 注入。Binance 和 OKX 的
REST/WebSocket 差异在各自适配器内完成，不进入应用层。本文记录当前仓库的调用映射，不声明交易所最新接口规范；实现分别在 `internal/exchange/binance/` 与 `internal/exchange/okx/`。

## 公共能力

| 能力 | Binance | OKX |
| --- | --- | --- |
| SPOT instrument | `/api/v3/exchangeInfo` | `/api/v5/public/instruments?instType=SPOT` |
| USDT 线性 SWAP instrument | `/fapi/v1/exchangeInfo` | `/api/v5/public/instruments?instType=SWAP` |
| 目标换算参考价 | `/api/v3/ticker/price` 或 `/fapi/v1/ticker/price` 的 `price` | `/api/v5/market/ticker` 的 `last` |
| 撮合报价 | `/api/v3/ticker/24hr` 或 `/fapi/v1/ticker/24hr` | `/api/v5/market/ticker` |
| MARKET / LIMIT 下单 | `POST /api/v3/order` 或 `/fapi/v1/order` | `POST /api/v5/trade/order` |
| 按 client order ID 查询 | origClientOrderId | clOrdId |
| 撤单 / 查询未完成订单 | order/openOrders | cancel-order/orders-pending |
| Account / Position / RecentFill 快照 | `/api/v3/account`、`/api/v3/myTrades`；SWAP 为 `/fapi/v3/account`、`/fapi/v3/positionRisk`、`/fapi/v1/userTrades` | `/api/v5/account/balance`、`/api/v5/account/positions`、`/api/v5/trade/fills-history` |
| 私有订单与成交流 | SPOT 使用 WebSocket API `userDataStream.subscribe.signature`；SWAP 使用 `/fapi/v1/listenKey` 与私有流 | private `orders` channel |

## 归一化

- Symbol 在领域层使用 Exchange 原生 symbol，并由 instrument 记录绑定
  `instrument_id`。
- 公共 quantity 是基础资产数量。
- Binance 当前的 SPOT 与 USDT 线性 SWAP 直接以基础资产数量传递 `quantity`，按 instrument 的步长、精度与最小量校验；不做 OKX 式的张数换算。
- OKX SWAP 适配器用 `ctVal` 和 `ctValCcy` 在基础数量与 `sz` 之间双向转换。
- 接口与持久化边界的时间戳统一为毫秒；适配器内部使用 `time.Time`。
- Exchange order ID、trade ID 和 client order ID 原样保存，用于恢复与幂等。
- live SWAP 的账户、保证金、强平价和 PnL 直接采用 Exchange 返回值；paper 使用本地
  成交回放和公开参考价，不伪造强平价。

## 错误分类

明确的参数、余额或权限拒绝是终态错误。网络 EOF、超时、HTTP 429 和 HTTP 5xx 表示
提交结果未知：订单保持 `SUBMIT_UNKNOWN`，下一步必须按 client order ID 查询。只有
按 client order ID 查询返回不存在、最近成交里也找不到该 ID，并且距首次提交已达到 `UnknownLookupWindow`（未设置时 30 秒），订单才回到 `PENDING`，后续用同一个 client order ID 重试；等待窗口内保持未知，不立即重发。

## 环境

- Binance REST：PRODUCTION 为 `https://api.binance.com`（SPOT）、`https://fapi.binance.com`（SWAP）；TESTNET 为 `https://testnet.binance.vision`、`https://demo-fapi.binance.com`。
- OKX REST 两种环境共用 `https://openapi.okx.com`，TESTNET 请求带 `x-simulated-trading: 1`。
- 私有 WebSocket 按环境选择固定端点：Binance SPOT 为 WebSocket API，SWAP 为 fstream；OKX TESTNET 使用 `wspap.okx.com`，PRODUCTION 使用 `ws.okx.com`。完整地址以各适配器的 `privateStreamEndpoint` 为准。

## 凭据

live 适配器接收由 Admin Secret 解密得到的临时内存凭据，SQLite 只保存 Secret 引用；
paper 适配器不读取 Secret。日志、错误和 telemetry 不得输出 API key、secret 或 OKX
passphrase。
