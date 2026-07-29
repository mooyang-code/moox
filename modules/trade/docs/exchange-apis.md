# Exchange API 映射

Trade 领域只依赖统一 `exchange.Adapter`。Binance 和 OKX 的 REST/WebSocket 差异在
各自适配器内完成，不进入应用层。

## 公共能力

| 能力 | Binance | OKX |
| --- | --- | --- |
| SPOT instrument | ExchangeInfo | Public Instruments SPOT |
| USDT 线性 SWAP instrument | Futures ExchangeInfo | Public Instruments SWAP |
| 参考价 | ticker/mark price | ticker/mark price |
| MARKET / LIMIT 下单 | order | trade/order |
| 按 client order ID 查询 | origClientOrderId | clOrdId |
| 撤单 / 查询未完成订单 | order/openOrders | cancel-order/orders-pending |
| Account / Position / RecentFill 快照 | account/position/trades | balance/positions/fills |
| 私有订单与成交流 | user data stream | private orders channel |

## 归一化

- Symbol 在领域层使用 Exchange 原生 symbol，并由 instrument 记录绑定
  `instrument_id`。
- 公共 quantity 是基础资产数量。
- Binance 适配器按合约规则转换 Exchange quantity。
- OKX SWAP 适配器用 `ctVal` 和 `ctValCcy` 在基础数量与 `sz` 之间双向转换。
- 时间戳统一为毫秒。
- Exchange order ID、trade ID 和 client order ID 原样保存，用于恢复与幂等。
- live SWAP 的账户、保证金、强平价和 PnL 直接采用 Exchange 返回值；paper 使用本地
  成交回放和公开参考价，不伪造强平价。

## 错误分类

明确的参数、余额或权限拒绝是终态错误。网络 EOF、超时、HTTP 429 和 HTTP 5xx 表示
提交结果未知：订单保持 `SUBMIT_UNKNOWN`，下一步必须按 client order ID 查询。只有
查询确认不存在且仍在允许窗口内时，才可使用同一个 client order ID 重试。

## 凭据

live 适配器接收由 Admin Secret 解密得到的临时内存凭据，SQLite 只保存 Secret 引用；
paper 适配器不读取 Secret。日志、错误和 telemetry 不得输出 API key、secret 或 OKX
passphrase。
