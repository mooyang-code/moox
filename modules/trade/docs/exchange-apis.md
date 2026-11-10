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
| 撮合报价 | `ticker/price` 加 `ticker/bookTicker`（现货 `/api/v3`，合约 `/fapi/v1`），时间取本地收到时间 | `/api/v5/market/ticker`，时间取本地收到时间 |
| MARKET / LIMIT 下单 | `POST /api/v3/order` 或 `/fapi/v1/order` | `POST /api/v5/trade/order` |
| 按 client order ID 查询 | origClientOrderId | clOrdId |
| 撤单 / 查询未完成订单 | order/openOrders | cancel-order/orders-pending |
| Account / Position / RecentFill 快照 | `/api/v3/account`、`/api/v3/myTrades`；SWAP 为 `/fapi/v3/account`、`/fapi/v3/positionRisk`、`/fapi/v1/userTrades` | `/api/v5/account/balance`、`/api/v5/account/positions`、`/api/v5/trade/fills-history` |
| 私有订单与成交流 | SPOT 使用 WebSocket API `userDataStream.subscribe.signature`；SWAP 使用 `/fapi/v1/listenKey` 与私有流 | private `orders` channel |

## 币安统一账户（组合保证金）

统一账户只有一把账户级的 papi，一个适配器（`binance.PortfolioMargin`）同时服务现货（杠杆账户）和合约（UM），不拆成两个：拆开会重复订阅同一条私有流，余额也会重复计算。公共行情仍走现货、合约域名，签名私有接口走 `https://papi.binance.com`。**只支持生产环境**：币安对统一账户没有测试网（测试网也没有 sapi、papi），测试网下构造适配器会直接报错“币安测试网没有统一账户”。

路径映射（`pmPaths`，普通账户路径换成 papi 路径，其余不变）：

| 能力 | 普通账户 | 统一账户 |
| --- | --- | --- |
| 现货下单、撤单、查单 | `/api/v3/order` | `/papi/v1/margin/order`（杠杆账户） |
| 现货挂单 | `/api/v3/openOrders` | `/papi/v1/margin/openOrders` |
| 现货成交 | `/api/v3/myTrades` | `/papi/v1/margin/myTrades` |
| 合约下单、撤单、查单 | `/fapi/v1/order` | `/papi/v1/um/order` |
| 合约挂单 | `/fapi/v1/openOrders` | `/papi/v1/um/openOrders` |
| 合约成交 | `/fapi/v1/userTrades` | `/papi/v1/um/userTrades` |
| 合约杠杆 | `/fapi/v1/leverage` | `/papi/v1/um/leverage` |
| 合约资金流水 | `/fapi/v1/income` | `/papi/v1/um/income` |
| 账户与余额 | `/api/v3/account`、`/fapi/v3/account` | `/papi/v1/balance`、`/papi/v1/account`（两个都读） |
| 合约持仓 | `/fapi/v3/positionRisk` | `/papi/v1/um/positionRisk` |
| 持仓模式检测 | `/fapi/v1/positionSide/dual` | `/papi/v1/um/positionSide/dual`（只支持单向） |
| 私有流 | 现货 WebSocket API；合约 `/fapi/v1/listenKey` | `POST /papi/v1/listenKey`，连 `wss://fstream.binance.com/pm/ws/<listenKey>`，一条连接同时收现货和合约事件 |
| BNB 抵扣（合约） | `/fapi/v1/feeBurn` | `/papi/v1/um/feeBurn` |

要点：

- 现货下单走杠杆账户，每笔请求都显式带 `sideEffectType=NO_SIDE_EFFECT`（不借币、不自动还款），不依赖官方默认值。`exchangeInfo` 里 `isMarginTradingAllowed` 为假的标的不上架。
- 合约一律全仓，没有切换保证金模式的接口，`SetMarginMode` 是空操作；只支持单向持仓。
- 余额：杠杆钱包净额取 `crossMarginAsset` 减借款和利息，UM 钱包取 `umWalletBalance`；未实现盈亏只取 UM 持仓接口的 `unRealizedProfit` 之和。UM 钱包的余额不记入现货可用（是否能直接给杠杆账户买单用未核对，见冒烟清单），账户级可用资金放在快照的 `AvailableFunds`，合约保证金按它校验。
- 私有流 listenKey 60 分钟过期，每 30 分钟 `PUT /papi/v1/listenKey` 续期（限流暂停期间跳过，1 分钟后再试）；心跳超时 4 分钟。事件里没有市场字段，按事件类型分派：`executionReport`、`outboundAccountPosition` 是现货，`ORDER_TRADE_UPDATE`、`ACCOUNT_UPDATE`（`fs` 为 UM）是合约，币本位（CM）事件忽略。
- 周期对账（30 秒）的现货挂单按标的逐个传 `symbol` 查（权重 5）；会话启动和手动全量同步不传 `symbol`，按官方文档说明返回全部（权重按交易中标的数量计，真实行为未核对）。合约挂单始终列全部。
- 不是统一账户却调用 papi（或相反）时返回什么错误码，官方没有写明，账户类型检测只认 -2015、-2014、-2008、-1002、-1022 这类鉴权错误，其余一律“没有结论”，不会误判。
- 统一账户不支持钱包划转（没有普通账户的现货与合约两个钱包）；BNB 抵扣、小额兑换见下节。

## 币安划转、BNB 抵扣与小额兑换

这些是币安专属的可选能力（`execution.Supports`）；OKX 没有。

| 能力 | 接口 | 说明 |
| --- | --- | --- |
| 钱包划转 | `POST /sapi/v1/asset/transfer`，类型 `MAIN_UMFUTURE`（现货到合约）、`UMFUTURE_MAIN`（合约到现货） | 只放在普通账户的现货适配器上；官方接口没有客户端流水号，`clientRef` 不发给交易所，超时后靠记录对账 |
| 划转记录 | `GET /sapi/v1/asset/transfer` | 两个方向各查一次，一页 100 条翻页取完；状态 `PENDING` 不等于没到账，要等 `CONFIRMED` 或 `FAILED` |
| BNB 抵扣（现货） | `POST`、`GET /sapi/v1/bnbBurn`，参数 `spotBNBBurn`；统一账户同时设 `interestBNBBurn`（借币利息） | 现货适配器负责；统一账户读取时两项都开才算开启 |
| BNB 抵扣（合约） | 普通账户 `/fapi/v1/feeBurn`，统一账户 `/papi/v1/um/feeBurn` | 合约适配器负责；U 本位合约抵扣要用合约钱包里的 BNB |
| 小额兑换 | `POST /sapi/v1/asset/dust-btc`（列出）、`POST /sapi/v1/asset/dust`（兑换成 BNB，`asset` 重复传） | 只放在现货适配器上；`accountType` 普通账户为 `SPOT`，统一账户为 `MARGIN` |

sapi 接口在现货域名上，统一账户和普通账户共用，签名与其他私有接口一致。

## 资金费率与资金流水

- **资金费率**（模拟盘用）：币安 U 本位永续公共接口 `GET /fapi/v1/fundingRate`，不带 `symbol` 时返回时间窗内全部合约的结算记录；每次请求覆盖 4 小时，单页上限 1000 条，满页对半拆开重查，不依赖返回顺序，同一毫秒就超过 1000 条时报错、不返回不完整的结果。OKX 适配器没有这个能力。
- **资金流水**（实盘，`ListAccountLedger` 与 `SummarizeAccountPnl` 用）：
  - 币安：只有 U 本位永续有，`GET /fapi/v1/income`（统一账户 `/papi/v1/um/income`），单次最多 1000 条（权重 30），从结束时间往前分段，一次查询最多发 8 次请求；现货没有统一的资金流水接口。合约收益流水里 `incomeType=TRANSFER` 的 `tranId` 假定就是万向划转的 `tranId`，用来避免已完成的划转重复出现（未核对）。
  - OKX：`GET /api/v5/account/bills`（最近 7 天），更早的用 `GET /api/v5/account/bills-archive`（近 3 个月）；每页 100 条，用 `after=<上一页最后一条 billId>` 往更早翻页，最多翻 10 页、总时长不超过 10 秒（`bills` 限 5 次/秒、`bills-archive` 限 5 次/2 秒，页间按此留间隔）。请求不带 `instType`（划转、利息等账单没有它），返回后在本地只保留本市场的交易类账单。
  - 两家被限频或到点时，已取到的照样返回并标记还有更早的。

## 归一化

- Symbol 在领域层使用 Exchange 原生 symbol，并由 instrument 记录绑定
  `instrument_id`。
- 公共 quantity 是基础资产数量。
- Binance 当前的 SPOT 与 USDT 线性 SWAP 直接以基础资产数量传递 `quantity`，按 instrument 的步长、精度与最小量校验；不做 OKX 式的张数换算。
- OKX SWAP 适配器用 `ctVal` 和 `ctValCcy` 在基础数量与 `sz` 之间双向转换。
- Binance 合约持仓用 `/fapi/v3/positionRisk`，v3 不返回 `marginType`、`leverage`，全仓判断看 `isolatedWallet`（非零即逐仓），杠杆取本地配置。
- 接口与持久化边界的时间戳统一为毫秒；适配器内部使用 `time.Time`。
- Exchange order ID、trade ID 和 client order ID 原样保存，用于恢复与幂等。
- live SWAP 的账户、保证金、强平价和 PnL 直接采用 Exchange 返回值；paper 使用本地
  成交回放和公开参考价，不伪造强平价。

## 错误分类

明确的参数、余额或权限拒绝是终态错误。网络 EOF、超时、HTTP 408 和 HTTP 5xx 表示
提交结果未知：订单保持 `SUBMIT_UNKNOWN`，下一步必须按 client order ID 查询。
订单号重复说明原单可能已被受理，不能当拒单：币安现货 -2010（msg 含 `Duplicate order`）、合约 -4116、OKX 51016 归结果未知（保留原错误码），按 client order ID 查询认领。-1000（未知错误）、-1001（内部错误）官方没有保证订单未被受理，同样归结果未知；-1016 是确定未受理，保留拒单。-2010 其余按 msg 细分：含 `insufficient balance` 的归余额不足，其余归拒单。
限流类错误确定未受理，这类下单回到 `PENDING` 并清空提交时间，不计入拒单。两家的口径不同：

- 币安：HTTP 429、-1003 是 IP 级限流，418 是 IP 封禁，WAF 403 是网关拦截，三者都暂停同一域名（没有 `Retry-After` 时分别暂停 10 秒、2 分钟、30 秒，有则按它）；-1015 是账户下单限流，只暂停该账户下单（默认 10 秒）；-1008 是系统限流，只暂停该账户下单 2 秒。
- OKX：HTTP 429 是按接口和用户的限频，不是 IP 封禁（`okx.classifyRateLimit`），和 50011、50040、50061 一样归下单限流，只暂停本账户的下单，不暂停域名，也不影响查询。50011、50040 在 HTTP 200 里返回，由适配器识别并暂停下单 2 秒（OKX 限频窗口）；HTTP 429 没有 `Retry-After` 时暂停 10 秒。只有下单请求触发暂停，查询被限流不影响下单。

权重（币安）：永续每分钟 2400、现货 6000，按 IP 和域名计；永续下单 0 权重、现货 1。统一账户（papi 域名）的额度按 6000 估计，只用来算用量比例，官方没有给出整体额度原文（未核对，真实限频以响应头和 -1003 为准）。`httpclient` 记录 `X-MBX-USED-WEIGHT-1M`，同一分钟取最大值（部分接口如 `bookTicker` 的读数不准），新一分钟没有响应时读数作废。OKX 下单带 `expTime`（5 秒）。只有
按 client order ID 查询返回不存在、最近成交里也找不到该 ID，并且距首次提交已达到 `UnknownLookupWindow`（未设置时 2 分钟），订单才回到 `PENDING`，后续用同一个 client order ID 重试；等待窗口内保持未知，不立即重发。
“不存在”只认明确的错误码：币安 -2013、OKX 51603（`exchange.IsConfirmedOrderMissing`）；裸 HTTP 404 不算，仍保持未知。

## 时钟检测

账户检测里带一项本机时钟偏差，只检测、不校准（适配器不持有时间偏移，也不改签名时间）。取交易所时间：币安现货 `/api/v3/time`、合约 `/fapi/v1/time`（统一账户共用），OKX `/api/v5/public/time`，都不签名；偏差取请求往返的中点估算，往返超过 1 秒的样本按没测到处理。偏差超过 1 秒给提示，达到 3 秒作为阻断项（`clock_skew`），阻断项文案固定、不带数字，数字只在检测结果的 `clock_skew_ms` 和提示里。

币安本机时钟偏快 1 秒以上时签名请求会被 -1021 拒绝，所以先测时钟，再做签名检测：签名检测没有结论而偏差已达到阻断阈值时，直接把时钟偏差作为结论返回（只有时钟阻断项）。下单或查询遇到币安 -1021、OKX 50102 时另记关注项 `clock_skew`。

## 环境

- Binance REST：PRODUCTION 为 `https://api.binance.com`（SPOT）、`https://fapi.binance.com`（SWAP）、`https://papi.binance.com`（统一账户）；TESTNET 为 `https://testnet.binance.vision`、`https://demo-fapi.binance.com`，没有统一账户。
- OKX REST 两种环境共用 `https://openapi.okx.com`，TESTNET 请求带 `x-simulated-trading: 1`。
- 私有 WebSocket 按环境选择固定端点：Binance SPOT 为 WebSocket API，SWAP 为 fstream（`wss://fstream.binance.com/ws/`），统一账户为 `wss://fstream.binance.com/pm/ws/`；OKX TESTNET 使用 `wspap.okx.com`，PRODUCTION 使用 `ws.okx.com`。完整地址以各适配器的 `privateStreamEndpoint` 为准。

## 凭据

live 适配器接收由 Admin Secret 解密得到的临时内存凭据，SQLite 只保存 Secret 引用；
paper 适配器不读取 Secret。日志、错误和 telemetry 不得输出 API key、secret 或 OKX
passphrase。
