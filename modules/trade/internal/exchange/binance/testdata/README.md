# 币安私有接口样例

抓取日期 2026-11-03。

- `spot_execution_report_new.json`、`spot_outbound_account_position.json`、`spot_event_stream_terminated.json`：现货官方文档原文（https://github.com/binance/binance-spot-api-docs/blob/master/user-data-stream.md）。
- `spot_my_trades.json`：现货官方文档原文（rest-api.md，`GET /api/v3/myTrades`）。
- `spot_execution_report_trade.json`、`spot_execution_report_canceled.json`：由官方 NEW 样例改写。TRADE 改了 `x`、`X`、`l`、`z`、`L`、`n`、`N`、`t`、`m`、`Z`、`T`；CANCELED 改了 `x`、`X`、`T`，并把原单号放进 `C`，`c` 换成撤单请求自己的单号。官方文档只给了 NEW 一条样例。
- `futures_*.json`：U 本位合约文档站是单页应用，抓不到原文，按官方字段集整理（`userTrades` 的字段名 `buyer`、`maker`、`side` 已与官方文档核对；`listenKeyExpired` 的 `E` 按新版文档为字符串）。
- 测试网录到原文后（执行计划 D5）替换上面整理和改写的样例。
- `gct_*.json`：取自 gocryptotrader 仓库录制的币安响应（https://github.com/thrasher-corp/gocryptotrader/blob/master/exchanges/binance/testdata/http.json ，2026-11-03 取），`exchangeInfo` 只保留了几个标的。包括现货挂单、查单、撤单（`clientOrderId` 是撤单请求自己的单号，原单号在 `origClientOrderId`），现货与合约的标的规则，以及合约全市场盘口。

## 1B 新增样例（2026-11-06 抓取）

- 来源 A：币安官方 SDK `binance/binance-connector-js`（master，抓取时 `01d123b3e92d1f55315d594468f1ab7ee37be7ea`）各客户端 `tests/unit/rest-api/*.test.ts` 里的响应样例。这些样例由币安官方 OpenAPI 规范生成，字段名、大小写和类型与官方文档的响应示例一致；不是真实账户的响应，也不是手写。接口路径和参数名取自同一仓库 `src/rest-api/modules/*-api.ts`。
  - `api_restrictions.json`：`GET /sapi/v1/account/apiRestrictions`（wallet 客户端）。
  - `fapi_*.json`：U 本位合约（derivatives-trading-usds-futures 客户端）：`fapi_account_v3`（`/fapi/v3/account`）、`fapi_position_risk_v3`（`/fapi/v3/positionRisk`）、`fapi_order_new` / `fapi_order_cancel` / `fapi_order_query` / `fapi_open_orders` / `fapi_user_trades` / `fapi_leverage`、`fapi_position_dual`（`/fapi/v1/positionSide/dual`）、`fapi_multi_assets`（`/fapi/v1/multiAssetsMargin`）、`fapi_fee_burn_status` / `fapi_fee_burn_toggle`（`/fapi/v1/feeBurn`）、`fapi_income`（`/fapi/v1/income`）。
  - `sapi_*.json`：钱包（wallet 客户端）：`sapi_transfer_new`（`POST /sapi/v1/asset/transfer`）、`sapi_transfer_history`（`GET /sapi/v1/asset/transfer`）、`sapi_bnb_burn`（`POST /sapi/v1/bnbBurn`）、`sapi_dust_btc`（`POST /sapi/v1/asset/dust-btc`）、`sapi_dust`（`POST /sapi/v1/asset/dust`）。
  - `papi_*.json`：统一账户（derivatives-trading-portfolio-margin 客户端）：`papi_balance`、`papi_account`、`papi_um_position_risk`、`papi_margin_order_*`（`/papi/v1/margin/order`、`openOrders`、`myTrades`）、`papi_um_order_*`（`/papi/v1/um/order`、`openOrders`、`userTrades`）、`papi_um_income`、`papi_um_leverage`、`papi_um_position_dual`、`papi_um_fee_burn_*`、`papi_asset_collection`、`papi_listen_key`。
- 来源 B：现货官方文档原文（`binance/binance-spot-api-docs` 的 `rest-api.md`，抓取时 `2bae3f5bd622ebfdea593daed066df14dc7e7362`）：`spot_account.json`（`GET /api/v3/account`）、`spot_order_new_result.json`（`POST /api/v3/order` 的 RESULT 响应，去掉了行内注释）。
- 错误码取自现货官方文档 `errors.md`：-2015 REJECTED_MBX_KEY（Invalid API-key, IP, or permissions for action.）、-2014、-1002、-1022 是鉴权类；-1003、-1006、-1007、-1021 是限频、超时、时间戳。
- 未核对：
  - 官方文档没有写明开通统一账户后 fapi、以及普通账户调用 papi 具体返回的错误码，检测按社区报告都是 -2015；遇到其他错误码一律“没有结论”，不会误判账户类型。
  - -2008（密钥 ID 无效）是合约文档的错误码，现货 `errors.md` 里没有，按鉴权类处理。
  - 币安对统一账户没有测试网，papi 的真实行为只能靠这些样例和用户本人小额验证。

## 审查后补充

- 统一账户私有流的测试事件不是统一账户官方原文：现货事件用的是现货 `executionReport` 官方样例，合约事件用的是整理的合约 `ORDER_TRADE_UPDATE`、`ACCOUNT_UPDATE`（`ACCOUNT_UPDATE` 另外删掉了 `mt` 字段，统一账户的持仓行没有它）并插入 `fs`。字段集合按官方 SDK 的统一账户事件类型（`websocket-streams/types`）核对，没有原文样例。
- `GET /papi/v1/margin/openOrders` 不传 symbol：官方 SDK 里转录的文档原文写明不传则返回全部标的的挂单，但同时说明限频权重按“当前交易中的标的数量”计（传 symbol 权重 5）；SDK 的参数校验却把 symbol 标成必填，两者不一致。只有会话启动、增量同步和手动全量同步以文档说明为准（不传）；周期对账（30 秒）按本地未结束订单、持有资产和目标里的现货标的逐个传 symbol 查（权重 5），没有这些标的上的外部现货挂单要等全量同步才发现。真实行为和权重消耗未实测，V3 小额实测要核对；如果必填，全量路径也改成按相关标的逐个查询。
- 统一账户同时开合约时，现货买单的本地资金校验用账户级可用资金（含 UM 钱包）。如果交易所要求现货买单只能花杠杆钱包里的资金（需要先 `/papi/v1/asset-collection` 归集），带 `NO_SIDE_EFFECT` 的买单会被交易所拒绝；这一点未核对，V3 小额实测必须覆盖。
- 统一账户的现货标的按 `isMarginTradingAllowed` 过滤（该字段在现货 `exchangeInfo` 的官方文档里有，样例里取值全为 true）。
- 普通账户合约钱包的可划转额度取 `/fapi/v3/account` 资产行的 `maxWithdrawAmount`（官方 SDK 转录的字段说明：Maximum transferable/withdrawable amount），不取 `availableBalance`（持仓浮盈、多资产模式下两者可以不一致）；字段缺失才退回可用余额。该字段的取值规则以官方说明为准，未用真实账户核对。
- 资金流水去重假定合约收益流水里 `incomeType=TRANSFER` 的 `tranId` 就是万向划转的 `tranId`，用它避免已完成的划转重复出现；两者不是同一个编号时会重复。未核对，V3 实测要核对。
- 等现货钱包快照追上划转用现货账户 `GET /api/v3/account` 的 `updateTime`（`binance.go` 里取 `updateTime`）：如果划转不更新这个时间，每笔划转后要等满 2 分钟（`snapshotGrace`）才放行买入，只是慢，不会多花钱。未核对，V3 实测要核对。
- 保证金率监控的读数（阶段 4）：统一账户直接取 `GET /papi/v1/account` 的 `uniMMR` 比阈值（官方样例 5167.92171923）；币安常见问答说明 uniMMR = 调整后权益 ÷ 维持保证金，调整后权益计入抵押率、再扣掉挂单浮亏（openLoss）；接口文档里 `accountEquity` 是计入抵押率的账户权益，`actualEquity` 是不计抵押率的权益，所以分子对应 `accountEquity` 而不是 `actualEquity`。`uniMMR` 为空时才退回自己算的 `accountEquity ÷ accountMaintMargin`（没扣挂单浮亏，有挂单时可能略高于币安的 uniMMR）。官方样例里 `uniMMR`、`accountEquity`、`actualEquity`、`accountMaintMargin` 彼此对不上，只是示例填充值，不能据此推断公式。普通账户取 `/fapi/v3/account` 的 `totalMarginBalance`（总权益）和合约持仓名义。未核对：V3 小额实测时日志同时记交易所的 `uniMMR` 和自己算的值，对照两者是否一致。
