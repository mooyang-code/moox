# 币安私有接口样例

抓取日期 2026-11-03。

- `spot_execution_report_new.json`、`spot_outbound_account_position.json`、`spot_event_stream_terminated.json`：现货官方文档原文（https://github.com/binance/binance-spot-api-docs/blob/master/user-data-stream.md）。
- `spot_my_trades.json`：现货官方文档原文（rest-api.md，`GET /api/v3/myTrades`）。
- `spot_execution_report_trade.json`、`spot_execution_report_canceled.json`：由官方 NEW 样例改写。TRADE 改了 `x`、`X`、`l`、`z`、`L`、`n`、`N`、`t`、`m`、`Z`、`T`；CANCELED 改了 `x`、`X`、`T`，并把原单号放进 `C`，`c` 换成撤单请求自己的单号。官方文档只给了 NEW 一条样例。
- `futures_*.json`：U 本位合约文档站是单页应用，抓不到原文，按官方字段集整理（`userTrades` 的字段名 `buyer`、`maker`、`side` 已与官方文档核对；`listenKeyExpired` 的 `E` 按新版文档为字符串）。
- 测试网录到原文后（执行计划 D5）替换上面整理和改写的样例。
- `gct_*.json`：取自 gocryptotrader 仓库录制的币安响应（https://github.com/thrasher-corp/gocryptotrader/blob/master/exchanges/binance/testdata/http.json ，2026-11-03 取），`exchangeInfo` 只保留了几个标的。包括现货挂单、查单、撤单（`clientOrderId` 是撤单请求自己的单号，原单号在 `origClientOrderId`），现货与合约的标的规则，以及合约全市场盘口。
