# OKX 私有接口样例

- `account_push.json`：官方文档 WebSocket account 频道的推送样例原文（https://www.okx.com/docs-v5/en/ ，2026-11-03 抓取），去掉了文档里的行内注释。
- 其他 OKX 样例（orders、positions 推送和 REST）待测试网录制原文后补充（执行计划 D5）。

## 1B 新增样例

- `account_config.json`：`GET /api/v5/account/config` 的官方响应样例原文（https://www.okx.com/docs-v5/en/ ，2026-11-06 抓取）。这份官方样例本身带 `perm=read_only,withdraw,trade` 和 `posMode=long_short_mode`，所以同时覆盖“有提现权限”和“双向持仓”两个阻断项；检测测试里的“正常”“acctLv=1”等情况由这份样例改写 `perm`、`posMode`、`acctLv` 得到，只改这三个字段。
- 未核对：OKX 鉴权类错误码（50100～50114）沿用 `classifyOKXCode` 里已有的集合；官方错误码表不在文档页的同一份 HTML 里，没有逐字核对。50110（IP 不在密钥白名单）按鉴权类处理，也未逐字核对。

## T8 新增样例（2026-11-06 抓取，均为官方文档页 https://www.okx.com/docs-v5/en/ 的响应样例原文）

- `order_place.json`（POST /api/v5/trade/order）、`order_cancel.json`（POST /api/v5/trade/cancel-order）、`order_details.json`（GET /api/v5/trade/order）、`order_list.json`（GET /api/v5/trade/orders-pending）、`fills.json`（GET /api/v5/trade/fills-history）、`account_balance.json`（GET /api/v5/account/balance）、`account_positions.json`（GET /api/v5/account/positions）、`account_set_leverage.json`（POST /api/v5/account/set-leverage）。
- 核对结论：
  - 余额数量取 `details[].cashBal`（官方定义 Cash balance，不含未实现盈亏），`eq` 是币种权益，含未实现盈亏，不能当数量。
  - 未实现盈亏取 `details[].upl`（官方定义：该币种所有保证金与衍生品持仓的未实现盈亏之和）；账户级 `upl` 是折算 USD 的全账户值，不用；`totalEq` 只记入 ExchangeEquity 展示。结算币是 USDT 的永续账户里，`details[USDT].upl` 等于永续持仓 `upl` 之和。
  - 官方持仓样例是 MARGIN 逐仓的，持仓测试把 `instType`、`instId`、`mgnMode` 改成永续全仓，其余字段原样。
- 未核对：官方样例没有永续（SWAP）的下单、持仓原文，永续字段按同一接口的字段说明与上面的改写样例核对；OKX 一个账户同时做两个市场时，适配器按 instType 各开一条私有连接（orders 频道按 instType，account 频道两边都订阅，推送内容相同，合并是幂等的），没有改成单连接。
