# Trade 事件驱动交易内核设计

## 设计目标

Trade 是 MooX 内部的交易事实源。模块以独立数据库保存订单、成交、执行计划、Saga、双式账本、余额、持仓、调仓计划、Inbox 与 Outbox。Storage 只提供行情，不保存或投影交易事实。

本项目尚未上线，因此新内核不包含旧表兼容、双写或迁移逻辑。底层数据库实现只在 `internal/infra/store` 内可见，上层依赖 Store 接口与领域模型。

## 分层

```text
RPC / EventBus consumer
        |
application command / consumer / rebalance
        |
domain order / execution / ledger / position / rebalance
        |
infra store / bus / exchangebridge
        |
SQLite       JetStream       Binance / OKX
```

- `domain`：精确 Decimal、订单状态机、执行计划、账本、仓位和目标仓位规划，不依赖基础设施。
- `algorithm`：按 `(name, version)` 注册拆单、定价和执行策略；计划生成后完整持久化，不受后续注册表变化影响。
- `application`：原子创建交易意图，推进提交、成交结算、撤单换单 Saga 和调仓。
- `infra/store`：唯一事务边界，订单、账本投影与 Outbox 同事务提交。
- `infra/bus`：使用公共 `packages/messagepb` 和 `packages/jetstream`，不自行封装 NATS 协议。
- `infra/exchangebridge`：把账户通道解析与旧交易所 REST 适配器隔离在基础设施层。

## 主链路

1. `PlaceOrder` 校验 Decimal 和账户资金，在一个事务中创建订单、冻结资金并写入 Outbox。
2. Outbox Relay 发布 `moox.trade.execution.slice_ready.v1`。
3. durable consumer 在调用交易所前把订单推进到 `SUBMITTING`；明确拒绝、成功和结果未知分别落不同状态。
4. Binance/OKX 鉴权私有 WebSocket 将成交标准化；成交回报或 REST 缺口修复结果按交易所成交号幂等入库，并在同一事务内更新订单、账本余额和仓位。
5. 服务在 `SUBMITTING`、`SUBMIT_UNKNOWN`、`CANCELING` 或 `CANCEL_UNKNOWN` 重启时先查询交易所，再决定后续动作，禁止盲目重复下单。

定时循环只用于修复中断、补齐私有回报缺口和对账，不是订单或调仓的主触发方式。

私有流由生产 supervisor 按 Space+Channel 去重维护，连接退出后重试。Binance 维护 listen key；OKX 完成 login、orders channel 订阅和 ping/pong 保活。WebSocket 不是事实源，断线后的交易所 REST 对账仍是强制兜底。

## 资金与合约

- 现货买入冻结报价资产，卖出冻结基础资产。
- 合约开仓冻结报价资产名义金额，成交后转入 `margin` bucket。
- 合约 `reduce_only` 不冻结基础资产，避免把合约仓位错误建模为现货余额。
- 余额同步以交易所总额为锚，同时保留本地未完成订单的 `frozen` 和 `margin`，防止同步覆盖在途预留。
- 每一笔账本事务必须借贷平衡；业务引用和成交引用均有幂等键。

## 撤单换单

改单统一建模为持久化 Saga：

```text
CANCEL_REQUESTED -> CANCEL_CONFIRMED -> REPLACEMENT_CREATED -> REPLACEMENT_SUBMITTED
```

若交易所结果未知则进入 `CANCEL_UNKNOWN` 或 `REPLACEMENT_SUBMIT_UNKNOWN`。恢复器先查询交易所事实。原单确认撤销后才创建替代单，避免双重敞口。

## 目标仓位调仓

调仓规划支持 `FULL` 与 `PATCH`，确定性地产生带依赖关系的 legs。反向仓位拆为先平仓、后开仓；leg 的 `market_type`、`reduce_only`、价格快照和规则版本被持久化。

创建调仓在事务中写入 `moox.trade.rebalance.requested.v1`，专用 durable consumer 推进可执行 legs；恢复循环仅兜底。每个 leg 复用普通下单内核，因此共享幂等、风控、账本、成交和恢复语义。

## 一致性边界

- Space 是共享边界，不区分管理员与普通成员。
- Trade 数据库是命令与交易事实的唯一权威源。
- 数据库事务保证本地强一致；Inbox/Outbox 保证跨进程至少一次传递下的业务幂等。
- 模拟交易没有专用适配器前必须拒绝，不能回退到实盘或伪造成功。
