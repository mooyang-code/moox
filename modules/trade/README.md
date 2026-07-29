# moox-trade

Trade 是 Exchange 账户、订单、成交和持仓的事实源。V1 面向个人量化，支持 Binance
和 OKX 的 SPOT、USDT 线性 SWAP，以及 MARKET、LIMIT 两种订单。

## 公开服务

`ExchangeAccountService` 管理账户配置、杠杆、暂停状态和 `SyncAccount`。
`TradeExecutionService` 提供下单、撤单、目标数量提交，以及 Order、Fill、Position
查询。Trade 只使用 `Exchange` 表示交易所，不提供其他平行抽象。

## 执行语义

- 数量统一表示基础资产数量；Exchange 适配器负责合约张数换算。
- SPOT 使用 `position_side=UNSPECIFIED`，SWAP V1 仅支持 NET 和 CROSS。
- MARKET 不接受限价且 `time_in_force=UNSPECIFIED`。
- LIMIT 必须提供限价，支持 GTC、IOC 和 FOK。
- SWAP 支持 `reduce_only`；SPOT 拒绝该字段。
- POST 超时、EOF、429 或 5xx 进入 `SUBMIT_UNKNOWN`，先按客户端订单号查询再决定后续动作。
- 撤单确认后仍接受晚到 Fill，订单可收敛为 `PARTIALLY_CANCELED`。

每个 `ExchangeAccount` 对应一个串行 `ExchangeSession`。启动、重连和手工
`SyncAccount` 都会读取 Account、Position、OpenOrder 和 RecentFill 快照；全部成功前
账户保持 Not Ready。SWAP 的权益、保证金、标记价、强平价和 PnL 以 Exchange 快照为准。

## 策略目标

Strategy 通过 `moox.trade.target.requested.v1` 发布最终基础资产目标数量。Trade 持久化
`TargetExecution`，按最新 command sequence 收敛，并复用普通订单内核。Trade 不接收
权重、资金规模或报价资产预算。过期命令被拒绝，新命令覆盖尚未完成的旧目标。

## 配置

- `database.path`：独立 SQLite 数据库。
- `admin`：读取 Admin Secret 的网关和服务认证。
- `eventbus`：JetStream 地址及 Trade 专属凭据。
- `runtime.encryption_key`：live 凭据访问门禁值；生产通过
  `MOOX_TRADE_ENCRYPTION_KEY` 注入，缺失或强度不足时禁止读取 Secret。
- `runtime.paper_initial_balance`：paper 账户的初始结算资产余额。

live Exchange API 密钥只保存为 Admin Secret 引用，不写入 YAML 或 Trade 表；paper
账户不需要 Secret。

## 验证

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```
