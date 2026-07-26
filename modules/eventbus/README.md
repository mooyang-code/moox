# MooX EventBus

`moox-eventbus` 是内置 NATS JetStream 的唯一运行时所有者。业务服务直接发布和消费
`EventMessage`；EventBus 管理面只读，不代理发布，也不创建业务 Consumer。

## 端点

- NATS：`nats://127.0.0.1:4222`
- tRPC/HTTP：`trpc.moox.eventbus.EventBusMgr`，端口 `11420`
- 健康、就绪、指标：`11419/healthz`、`/readyz`、`/metrics`

## 配置所有权

`config/app.yaml` 只声明 broker、internal client、health、四个 Stream 和一个 KV。
Event 名称、版本、payload factory、owner、Stream 和 subject family 在
`packages/events/registry.go` 中以 Go 代码声明。

EventBus 在就绪前对账 Stream/KV。Archive、Factor、Storage View、Monitor、Trade 和
CloudNode 分别调用 `NewConsumer` 创建并拥有自己的 Consumer；可变参数会就地更新，
filter 或 deliver policy 等不可变参数冲突会显式失败，不会删除已有 Consumer。

## 容量边界

| 资源 | 时间边界 | 字节边界 |
| --- | --- | --- |
| `MOOX_STORAGE` | 72 小时 | 1 GiB |
| `MOOX_METRICS` | 24 小时 | 256 MiB |
| `MOOX_CLOUDNODE_EXEC` | 72 小时 | 256 MiB |
| `MOOX_TRADE` | 7 天 | 256 MiB |
| `MOOX_CLOUDNODE_JOB_ACTIVE` KV | 48 小时 | 无独立字节上限 |

`limits` Stream 统一使用 `discard: old`；CloudNode 和 Trade 使用 work queue。这里的边界
是个人系统的磁盘容量策略，允许旧消息自然淘汰，不会删除 Storage、Trade、Strategy 或
Archive 已提交的业务事实。

`store_dir` 是运行态目录，不进入发布包。部署时实际路径为
`data/eventbus/jetstream`。

## 验证

```bash
./scripts/build.sh eventbus
go test -count=1 ./modules/eventbus/...
./scripts/verify-event-contracts.sh
```

运维说明见[EventBus 运维](../../docs/运维/MooX-EventBus运维.md)和
[数据保留与磁盘空间](../../docs/运维/数据保留与磁盘空间.md)。
