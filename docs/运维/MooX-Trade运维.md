# MooX Trade 运维

## 健康检查

Trade 健康端口默认为 `11210`。启动时必须成功打开 Trade Store，并连接配置的 `MOOX_TRADE` JetStream；任一初始化失败都应让进程启动失败，不允许降级为无事件总线运行。

## 关键检查项

1. 检查 `trade_rebalance_v1` Consumer 的 pending、redelivery 和 ack pending。
2. 检查调仓 Inbox、待执行 run、订单与本地 Worker 的积压。
3. 检查 `SUBMIT_UNKNOWN`、`CANCEL_UNKNOWN` 与可恢复 Saga 的滞留数量。
4. 检查对账差异、负余额拒绝和重复成交计数。
5. 调仓异常时按 run 查询 legs、依赖、关联 order 和 residual，不应绕过内核直接在数据库修改状态。

## 运维操作

- 暂停账户：调用 `TradeOpsSvc.SetPause`，`target_type=account`。暂停仅阻止新单，已有订单仍可成交或撤销。
- 暂停通道：调用 `TradeOpsSvc.SetPause`，`target_type=channel`。确认风险解除后以 `paused=false` 恢复。
- 立即对账：调用 `TradeOpsSvc.ReconcileNow`。请求写入本地状态并唤醒 Worker，禁止直接执行数据库修复。
- 检查 Saga：调用 `TradeOpsSvc.InspectSaga`，检查 state、version、replacement order 和 last_error。
- 私有流：健康详情中活动订单存在时 `private_stream_ready` 必须为 true；断线期间 REST 对账继续补齐成交。

## 故障恢复

- JetStream 短暂不可用：Strategy 调仓命令留在其 Outbox/Stream 中；Trade 恢复连接后继续消费。
- 本地 Wake 遗漏或进程重启：恢复 Timer 扫描已提交但未完成的工作并重新唤醒 Worker。
- Worker 在交易所调用后崩溃：重启后先按 `client_order_id` 查询交易所，禁止直接重试下单。
- 私有成交流缺口：supervisor 自动重连，成交对账按交易所成交号补录，重复回报不会重复结算。
- 本地余额与交易所不一致：执行余额对账；本地 `frozen` 和 `margin` 在途资金必须保留。
- 调仓中断：rebalance consumer 或恢复循环从已持久化 legs 继续，不重新运行规划算法。

## 发布前门禁

```bash
go test -count=1 ./modules/trade/...
go test -count=1 ./modules/trade/test -v
go vet ./modules/trade/...
```

当前项目未上线，不执行历史数据迁移。表结构变化通过废弃开发数据库并重新初始化验证。
