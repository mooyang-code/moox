# Observability 投递策略

## 配置来源

初始化配置通过以下字段声明 Monitor 的消费策略：

```toml
[observability]
deliver_policy = "all"
```

只允许 `all`、`new`，首次部署缺省为 `all`。`all` 从创建消费者时仍保留的消息开始；`new` 只接收创建消费者之后的消息。已有 durable 的正常重启沿用其确认进度，不会因 `all` 每次重放全部数据。

发布脚本将策略写入目标机 release 的运行配置，并向 Monitor 传递 `MOOX_OBSERVABILITY_DELIVER_POLICY`。覆盖发布没有显式指定策略时，保留目标机已有配置；不依赖操作员 shell 的临时 export。Monitor 在配置加载时校验策略，然后显式传给消费者。

独立运行 Monitor 时也可在 `config/app.yaml` 的 `observability.deliver_policy` 中配置。非空环境变量覆盖 YAML；非法值使配置校验失败，不会静默退回 `all`。

## 升级边界

`monitor_observability_ingest_v1` 是固定 durable。发布前必须检查实际消费者的 DeliverPolicy；期望值与实际值不同会返回 `ErrConsumerConfigConflict`，不会自动修改或删除消费者。

策略切换需要独立运维操作：停止 Monitor，记录消费者配置、确认进度和积压，明确是否允许丢弃历史积压，再按批准的切换方案重建。不能把删除 durable 或清空 stream 放进普通发布或健康检查重启流程。

## 验证

1. 检查发布归档和目标机 `config/runtime.env`、`config/monitor-runtime.env` 中的策略，不输出凭据。
2. 检查 Monitor 进程中的 `MOOX_OBSERVABILITY_DELIVER_POLICY` 与实际 durable 配置一致。
3. 重启 Monitor，确认 Observability ingestion ready、Reporter 持续刷新。
4. 覆盖发布其他组件后再次验证，确认策略没有被缺省值覆盖。

本次代码验证不代替上述线上验收；不会自动重置现有消费进度。
