# moox-trade

Trade 是账户、订单、成交、账本、仓位和调仓的唯一事实源。它只消费 Strategy 发布的
`trade.rebalance.requested` 命令，不消费自己产生的订单、成交或进度事件。

## 主流程

Strategy 调仓命令经过 Consumer 解码和校验后，Trade 在同一数据库事务中写入 Inbox、
调仓 run 和 legs。事务提交后，本地 wake channel 立即推进调仓和订单状态机。下单、
成交和调仓进度只写 Trade 数据库；私有 WebSocket 提供低延迟成交回报，REST 对账补齐
断线缺口。

Trade 从交易所公开 instrument 接口读取最新价格、精度和报价资产，新标的第一次调仓
不依赖 Trade 历史订单。FullTarget 会把命令中省略的已有持仓收敛到 0；空 targets
表示全部平仓。现货不允许负权重，永续合约允许做空。

`paper` 只能使用模拟通道，订单通过本地即时成交处理器复用账本和仓位状态机，绝不调用
真实交易所下单；`live` 只能使用真实通道。

本地 wake 采用容量为 1 的合并通知，状态始终以数据库为准。Timer 不是业务主触发器：

- 成交缺口对账每 30 秒运行。
- 订单、Saga 和调仓恢复每 15 秒运行，并在启动时立即执行。

## 配置

- `database.path`：Trade SQLite 路径。
- `eventbus.urls`：JetStream 地址。
- `eventbus.credential_file`：Trade 专属 EventBus 凭据。
- `eventbus.rebalance_consumer`：Strategy 调仓命令 Consumer 名称。
- `security.encryption_key`：交易所凭证 AES-GCM 密钥。

`ReconcileNow` 直接执行一次有界对账，不发布事件。健康检查覆盖数据库、EventBus、
未知订单和私有流状态，不再包含 Trade outbox。

## 验证

```bash
make -C proto all
go test -count=1 ./...
go test -count=1 ./test -v
```

`internal/bootstrap/kernel_workers_test.go` 覆盖调仓命令解析、Inbox 幂等、重试分类和本地
状态推进；跨 Strategy、JetStream、Trade 的端到端验证由事件系统 E2E 执行。
