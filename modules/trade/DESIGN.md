# Trade 通用交易执行内核

## 边界

Trade 是 MooX 的交易事实源，负责 ExchangeAccount、ExchangeSession、Order、Fill、
Position、TargetExecution 和三表双式账本。Strategy 负责生成最终目标数量，Admin
负责 Secret，EventBus 只负责传输。V1 不做资金划转、零钱兑换、网格、TWAP、组合
多步骤改单编排、全局 exactly-once 或多进程协调。

```text
RPC / TargetIntent consumer
            |
account / order / target / accountsync
            |
ExchangeSession -> Binance / OKX
            |
SQLite: account, instrument, order, fill, position,
        target execution, ledger transaction/entry/projection
```

## ExchangeAccount 与 ExchangeSession

账户记录保存 Exchange、SPOT/SWAP、paper/live、可选 Secret 引用、暂停和 readiness
状态；只有 live 账户必须提供 Secret。
更新影响会话的配置后立即置为 Not Ready。一个账户只有一个 ExchangeSession；命令、
私有 Fill、快照、重连和 `SyncAccount` 在该会话内串行处理。

readiness 需要 Account、Position、OpenOrder 和 RecentFill 四类快照全部成功。同步会
导入 Trade 进程外创建的订单和成交。WebSocket 用于低延迟通知，REST 快照负责恢复，
两者都不能绕过同一个持久化 reducer。

## 订单

公共数量始终是基础资产数量。适配器读取 instrument 规则并完成精度取整以及 Binance、
OKX 合约数量换算。支持矩阵：

| 市场 | 类型 | 价格 | TIF | position_side | reduce_only |
| --- | --- | --- | --- | --- | --- |
| SPOT | MARKET | 禁止 | UNSPECIFIED | UNSPECIFIED | 禁止 |
| SPOT | LIMIT | 必填 | GTC / IOC / FOK | UNSPECIFIED | 禁止 |
| SWAP | MARKET | 禁止 | UNSPECIFIED | NET | 可选 |
| SWAP | LIMIT | 必填 | GTC / IOC / FOK | NET | 可选 |

调用 Exchange 前先持久化订单。明确成功或拒绝直接推进状态；EOF、timeout、429 和 5xx
进入 `SUBMIT_UNKNOWN`。恢复时按 client order ID 查询，禁止盲目重发。Fill 按 Exchange
trade ID 幂等；累计成交不能倒退。撤单确认不封死成交事实，晚到 Fill 可把状态细化为
`PARTIALLY_CANCELED`。

## SWAP

V1 只支持 USDT 线性 SWAP、NET 持仓和 CROSS margin。live 账户以 Exchange 快照中的
权益、保证金、entry/mark/liquidation price、leverage 和 realized/unrealized PnL
为权威；paper 账户由本地成交回放并使用公开参考价估值。目标从多翻空时先平后开，
避免在 NET 模式中产生非预期敞口。

## TargetIntent

Strategy 发布 `TargetIntent`，包含 execution、binding、account、command sequence、
有效期、数据 revision 和每个 instrument 的基础资产目标数量。Trade 校验 sequence 和
有效期，持久化最新 `TargetExecution`，结合当前仓位与活动订单继续收敛。新目标覆盖旧
目标，重连后只恢复最新目标。

## 持久化

SQLite 固定九张业务表。Order 与 Fill 分表，因为 Order 是意图和状态机，Fill 是可能
多次到达、独立幂等的 Exchange 成交事实。账本保留 transaction、entry、projection
三表，以最小结构保证借贷平衡并支持快速余额查询。新项目不迁移旧表；数据库出现当前
九表以外的业务表时启动失败。
