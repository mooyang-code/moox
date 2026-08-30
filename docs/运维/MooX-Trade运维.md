# MooX Trade 运维

## 健康检查

Trade 健康端口默认为 `11210`。启动时必须成功打开 Trade Store，并连接配置的 `MOOX_TRADE` JetStream；任一初始化失败都应让进程启动失败，不允许降级为无事件总线运行。

## 统一执行检查项

1. 检查 `trade_target_weight_v1` Consumer 的 pending、redelivery 和 ack pending。
2. 检查 `SUBMIT_UNKNOWN`、`CANCEL_UNKNOWN`、重复成交和余额对账差异。
3. 检查每个 `TradingAccount` 的 Ready、最近快照时间和 PaperMatcher 状态。
4. 资金曲线只通过 `TradeConsoleService.QueryEquityCurve` 查询，不直接修改事实表。

## 运维操作

- 暂停 LogicalAccount：调用 `PauseLogicalAccount`；恢复使用 `ResumeLogicalAccount`。
- 立即同步账户：调用 `SyncTradingAccount`，禁止直接修改订单、成交或持仓事实。
- 私有流：Live 账户断线必须立即 Not Ready；Paper 账户由单一 PaperMatcher 提供 Ready。
- Paper 关闭：只调用 `ClosePaperSimulation`，关闭不可逆；历史订单、成交和资金曲线保留。

## 故障恢复

- JetStream 短暂不可用：Strategy 调仓命令留在其 Outbox/Stream 中；Trade 恢复连接后继续消费。
- 本地 Wake 遗漏或进程重启：恢复 Timer 扫描已提交但未完成的工作并重新唤醒 Worker。
- Worker 在交易所调用后崩溃：重启后先按 `client_order_id` 查询交易所，禁止直接重试下单。
- 私有成交流缺口：supervisor 自动重连，成交对账按交易所成交号补录，重复回报不会重复结算。
- 本地余额与交易所不一致：执行余额对账；本地 `frozen` 和 `margin` 在途资金必须保留。
- 目标中断：`trade_target_weight_v1` consumer 从 Outbox/JetStream 重放，订单仍由同一个 OrderService 收敛。

## 破坏式 cutover

1. 停止 Runner，暂停 LogicalAccount，取消 Production 活动订单并清理非零敞口。
2. 等待 Strategy pending outbox 为空后停止 Strategy、Trade 和 relay。
3. 成对备份 Trade/Strategy SQLite、旧二进制和旧 Web；删除并重建两套 SQLite。
4. 删除 `MOOX_TRADE` Stream，启动 Trade 自动创建 `trade_target_weight_v1` consumer。
5. 部署同版本 Strategy/Admin/Web，保持 `live_trading_enabled=false`。
6. 手工重建 TradingAccount、PaperSimulation、Strategy、Runner 和 logical_account_id。
7. 运行 Paper、mock Live、JetStream、Web 和 Testnet 验收；通过后再开启 Production。

不提供 Reset、Archive、部分成交模拟或兼容迁移。新版本产生 Production Order 后不允许回滚旧 SQLite，只能关闭下单、撤单、对账并向前修复。

## 发布前门禁

```bash
go test -count=1 ./modules/trade/...
go test -count=1 ./modules/trade/test -v
go vet ./modules/trade/...
```

当前项目未上线，不执行历史数据迁移。表结构变化通过废弃开发数据库并重新初始化验证。
