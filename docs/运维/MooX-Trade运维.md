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

## 保留交易事实的版本切换

本节是发布门禁，不表示某个版本已满足门禁。完整执行清单和证据分别见交易完善计划及其进度文档。不得为了升级清空数据库、重建 Paper 账户或删除生产 Stream。

1. 核验实际节点、数据库路径、进程参数、源码与二进制 SHA，以及账户模式和待处理订单。独立 Trade 节点不能按 control 节点目录假定部署位置。
2. 停止新的策略求值/发布并暂停受影响的 LogicalAccount，记录 Outbox、Stream 积压和未知提交。暂停不等于撤单或清仓；既有活动订单、非零敞口的处置须单独确认，不自动产生反向交易。
3. 停止受影响的写入进程，制作一致性的 Trade/Strategy SQLite 备份，保留旧二进制和 Web。离线验证当前版本对实际已知 schema 的事务转换与失败回滚，比较账户、订单、成交等事实。未知 schema 必须拒绝，不绕过校验继续启动。
4. 对齐 Strategy 生产者、Trade 消费者、事件注册、Web 和管理接口版本。保留历史 receipt/Order/Fill；旧授权不猜测现代 session，先受控停用，再通过正式管理接口认领新 session 并等待新目标。
5. 保留 `MOOX_TRADE` Stream 及已有消息。核验实际 consumer filter、weight subject 和发布 ACL；停止旧生产者后记录旧积压处置决定，再更新配置，不以本地 YAML 变更代替服务端确认。
6. 独立 Trade 发布时，核验接收节点实际 applied route snapshot 中的 `trade_owner` 上游、方法白名单、Strategy 凭据及证书信任。浏览器 `trade_console` 和节点机器授权路由分别核验，不把健康服务登记成功当作 ownership 接线成功。
7. 在授权环境通过正式接口创建隔离 Paper 账户和策略实例，经实际 Processor、Outbox、JetStream、Trade Worker 到成交，核对订单、余额和持仓，再验证重启恢复。停用测试实例并保留测试事实，不触碰现有账户。

已有开关值须明确核对；测试不能自动开启实盘。Paper 验证不等于 Testnet 或真实交易所验收，后两者另需账户、标的和额度授权。

新版本产生任何新 Order/Fill 后，包括 Paper 成交，都不能直接恢复旧数据库快照。先停止新动作、保留新事实并对账，再选择兼容的应用回退或向前修复。旧二进制可能不接受新 schema，不能只替换二进制便宣称回滚成功。

## 发布前门禁

```bash
go test -count=1 ./modules/trade/...
go test -count=1 ./modules/trade/test -v
go vet ./modules/trade/...
```

以上 Go 命令只是本地基础检查，不代表完整发布验收。还须按交易执行计划完成相关 race、协议、Web、隔离事件链、新 Agent 审查及正式 Paper 验收。实际数据库是否已有交易事实必须现场核验，不能依赖“项目未上线”的历史假设。
