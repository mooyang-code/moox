# 加密小时行情恢复记录

## 故障与证据

- 影响 `view_crypto_spot_kline_1h`、`view_crypto_swap_kline_1h`。
- 修复前 BTC 现货和永续最新小时分别停在 `2026-09-10T06:00:00Z`、`05:00:00Z`；Primary 与 View 一致，不是 View 单独漏消费。
- 广州永续 SCF 请求 `fapi.binance.com` 及备用域名持续超时；相同请求在新加坡成功。短时批次触发熔断后，剩余任务显示 `provider not found`，不能把该文本直接解释为未注册 Provider。
- 规则缺少 `instrument_type`、`source_id`，旧 SCF 包的现货默认身份残留在永续 Timer 环境。
- KlinePipeline 未转发 DNS 快照；补齐后又发现指定 IP 客户端每次新建 Transport，不能复用 TLS 连接，加剧短时批次的预算压力。

## 代码修复

- 内置规则显式设置 instrument identity；Reconciler 通过 market wiring 补齐缺失 source identity，不在上层硬编码交易所实现。
- 将 DNSRoutes 防御性复制并传入 Provider。
- 每 IP 复用 HTTP Transport；并发安全，最多缓存 32 项，淘汰时关闭闲置连接，保留 Host、TLS SNI 和系统 DNS fallback。
- 空间级 `region_blacklist` 同时约束发布计划和运行时 SCF 调度；Collector 接收 `scf_region_blacklists` 配置。

## 数据恢复

2026-09-10 17:30 左右（北京时间），经新加坡 SCF 对当前 1,013 个小时任务执行最近 10 根行情的幂等补采，未清空数据。

对 View 查询 `[2026-09-10T08:00:00Z, 09:00:00Z)`，得到：

| View | 最新已收盘小时 | Subject 数 |
| --- | --- | --- |
| 现货 1h | 北京时间 16:00 | 490 |
| 永续 1h | 北京时间 16:00 | 523 |

补采 Invoke 的个别 completion 发布超时经同批重试成功；这与 Timer 路径不同，Timer 不发布 scheduler completion。补采成功不替代下一次自然 Timer 的验证。

## 部署注意事项

- `crypto` 黑名单加入 `ap-guangzhou`，实际迁移到新加坡 18、香港 40，总 Timer 容量 58。新加坡 SCF 账号总配额为 50，已被 `stockcn` 占用其余槽位，因此未删除或影响其他空间函数。
- 黑名单是硬禁止：先禁用黑名单内已有 Timer，再分配允许地域；容量不足时告警，不恢复禁用地域。Collector 配置部署和 SCF 发布都必须覆盖这一约束。
- 单服务包也必须检查 ZIP 文件清单。本轮首次 Collector 包误带默认 app.yaml，导致服务打开开发默认库，已重新部署正确的 `../data/collector/moox_collector.db` 配置并从私有 manifest 重渲染 Trade DNSResolver；原业务库未清空。
- 校验运行日志中的实际 DB 路径、DNSResolver 和协调成功记录，不能仅以服务进程启动成功作为验收。

## 验证

- Collector 全模块测试通过。
- CLI 配置包及相关发布测试通过；CLI 命令全包存在会话前 metadata seed 状态修改引起的 `TestDefaultMetadataUsesUnifiedCryptoMarket` 失败，不属于本次补丁，未回退该修改。
- HTTP 缓存和行情身份/DNS 相关 race 测试通过。
- 独立 codeCR 对身份、DNS 透传和 Transport 缓存未发现 P1/P2。
- SCF 包 `crypto-20260910-17` 已在新加坡 18、香港 40 个 Timer 节点发布并通过部署门禁；新加坡、香港 Invoke 节点各 1 个。广州 39 个及北京 1 个旧 Timer/Invoke 节点已提交官方删除批次 `node-batch-2d3d5d7f-74aa-43fd-8844-f3bd046cb0fd`（异步处理中）。香港 Timer 手动触发返回成功响应但部分 1m 请求因 Binance 公网限时失败，需以整点 1h 批次作为最终验收。
