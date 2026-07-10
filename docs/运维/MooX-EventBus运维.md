# MooX EventBus 运维

`moox-eventbus` 是 MooX 唯一的生产 NATS JetStream 所有者。业务进程直接连接 NATS 发布 `MooxMessage`，不经过 tRPC/HTTP 发布代理。EventBus 管理面只读，不能代替业务消费者处理消息。

## 启动顺序

单机部署的顺序固定为：

1. 启动 `moox-eventbus`，等待 `http://127.0.0.1:11419/readyz` 返回 200。
2. 启动 Storage，等待 Access、Metadata 和 View 服务就绪。
3. 在 Monitor 启动前，由 `start.sh` 等待 Metadata HTTP 端口，并执行 `moox-cli metadata apply`。这个步骤只创建或校验声明式元数据，不覆盖不兼容的已有定义。
4. 启动 Monitor，随后启动 Collector、Factor、CloudNode 和 Web Host。

可单独控制生命周期：

```bash
./start.sh eventbus
./status.sh
./healthcheck.sh eventbus monitor
./stop.sh eventbus
```

关闭 EventBus 时先停止发布方和消费者，再停止 Broker；不要删除 `data/eventbus/jetstream` 来“修复”消费积压。

## 端点和数据目录

| 用途 | 默认地址 |
| --- | --- |
| NATS/JetStream | `nats://127.0.0.1:4222` |
| EventBus tRPC/HTTP 管理面 | `:11420` |
| liveness/readiness/metrics | `:11419/healthz`、`/readyz`、`/metrics` |

JetStream 持久化目录是 `<deploy-dir>/data/eventbus/jetstream`。发布包不包含运行态数据；备份必须在业务进程停止或使用一致性快照的情况下完成：

```bash
./stop.sh
tar -C data/eventbus -czf eventbus-jetstream-$(date +%Y%m%d%H%M%S).tar.gz jetstream
```

恢复时停止所有依赖服务，将 `jetstream` 目录恢复到原路径，确认属主和权限后先启动 EventBus，再启动其他服务。不要在恢复目录中混入另一套 Broker 的数据。

## Stream、KV 和 Topic

Stream/KV 由 `modules/eventbus/config/app.yaml` 声明并由 EventBus 在 readiness 前 reconcile。默认资源为：

| 资源 | Subject/用途 | 保留 |
| --- | --- | --- |
| `MOOX_STORAGE` | `moox.storage.>` | 72 小时，20 GiB |
| `MOOX_METRICS` | `moox.metrics.>` | 24 小时，10 GiB |
| `MOOX_CLOUDNODE_EXEC` | 云节点任务 | 72 小时，10 GiB |
| `MOOX_DLQ` | `moox.dlq.>` | 30 天，2 GiB |
| `MOOX_CLOUDNODE_JOB_ACTIVE` | JetStream KV | TTL 48 小时 |

Topic 必须遵循 `moox.<domain>.<entity>.<action>.v<major>`，且 `MooxMessage.topic` 必须等于实际 Subject。兼容字段追加可以复用当前 Topic；字段含义变化、重编号或 payload 替换必须发布新的 `.v2` Topic。先扩容再发布方，避免在消息已积压时缩小 `max_age` 或 `max_bytes`。Retention、Storage 和已有消息上的 Subject 删除会被拒绝，需要人工迁移。

## 积压、重复与 DLQ

查看积压和重投递：

```bash
curl -s http://127.0.0.1:11419/metrics \
  | egrep 'stream_messages|stream_bytes|consumer_pending|consumer_redelivered'
```

消息是至少一次投递。消费者必须以 `message_id` 做幂等键；重试必须复用同一 `message_id`，JetStream 的 `Nats-Msg-Id` 去重不能替代业务幂等。先确认消费者是否在运行、`AckWait` 是否小于处理耗时，再检查 Storage 或下游超时。不可解析的外层消息由消费方终止并写入 `moox.dlq.message.rejected.v1`；DLQ 也必须使用独立 durable consumer，不能在发布路径中同步调用回调。

## 容量和告警

至少监控：Stream bytes/messages、consumer pending/redelivered、JetStream 数据盘使用率、EventBus readiness、发布失败计数和连接数。达到 Stream 上限时会按 `DiscardOld` 丢弃最旧消息，生产环境应在 70% 和 85% 阈值分别告警并提前扩容。测试和故障演练必须使用临时目录与明确字节上限，不得扫描生产 JetStream 目录。

## 认证、TLS 与集群

认证/TLS 只在 `config/app.yaml` 中配置，发布方必须同步 `MOOX_EVENTBUS_NATS_URL` 及凭据。轮换凭据时先给 Broker 配置兼容的新账号，滚动重启发布方和消费者，确认连接恢复后再撤销旧账号。TLS 集群的 route 凭据和客户端凭据是两套连接语义，三节点部署要求每个节点唯一 `server_name`，Stream/KV 的 `replicas` 不得超过可达节点数。

远程 `deploy-moox.sh` 只转发不敏感的 EventBus URL；`MOOX_EVENTBUS_NATS_USERNAME`、`MOOX_EVENTBUS_NATS_PASSWORD`、`MOOX_EVENTBUS_NATS_CREDENTIALS` 和 TLS 文件路径必须在目标机的 service manager/environment 中配置，脚本不会把凭据拼进 SSH 命令行。

V1 不实现回调订阅。未来通知回调应作为独立 durable consumer/worker，具备重试、超时、幂等和 DLQ，不得改变当前发布方协议或阻塞发布确认。

## 验证

```bash
./scripts/test-deploy-moox-eventbus.sh
go test -count=1 ./modules/eventbus/... ./packages/messagepb ./packages/jetstream
```

上述测试只使用临时 Broker 和临时持久化目录；发布归档不应包含 `data/eventbus/jetstream`。
