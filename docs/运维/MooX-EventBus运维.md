# MooX EventBus 运维

`moox-eventbus` 是 MooX 唯一的生产 NATS JetStream 所有者。业务进程直接连接 NATS
发布或消费 `EventMessage`；EventBus 管理面只读，不代替业务 Consumer 处理消息。

## 启停顺序

1. 启动 EventBus，等待 `http://127.0.0.1:11419/readyz` 返回 200。
2. 启动 Storage，完成 Metadata seed、激活检查和显式激活。
3. 启动 Monitor、Collector、Factor、Strategy、Trade、CloudNode、Archive 等业务进程。

停止时按相反顺序。不要删除 `data/eventbus/jetstream` 来处理积压。

```bash
./start.sh eventbus
./healthcheck.sh eventbus
./stop.sh eventbus
```

## 基础拓扑

EventBus 配置只声明 Stream/KV。普通模块用 `NewConsumer` 创建和维护 Consumer；
CloudNode 作业队列由 CloudNode ensure，SCF worker 只能 bind。

| 资源 | Subject/用途 | 容量边界 |
| --- | --- | --- |
| `MOOX_STORAGE` | `event.storage.dataset.rows.upserted` | 72 小时或 1 GiB |
| `MOOX_METRICS` | 两类 metrics 事件 | 24 小时或 256 MiB |
| `MOOX_CLOUDNODE_EXEC` | 云任务 work queue | 72 小时或 256 MiB |
| `MOOX_TRADE` | Strategy 调仓 work queue | 7 天或 256 MiB |
| `MOOX_CLOUDNODE_JOB_ACTIVE` | active JobItem KV | 48 小时 |

`limits + discard old` 是个人系统的磁盘容量策略：达到时间或字节上限后允许最旧消息自然
淘汰。它不删除已提交到 Storage、Trade、Strategy 或 Archive 的事实。服务离线超过消息
保留窗口可能产生不可恢复的消费缺口，按实际可接受的离线时间调整边界即可，不需要引入
复杂的高可靠方案。

事件名称、版本、payload、owner、Stream 和 subject family 由
`packages/events/registry.go` 唯一声明。新项目不保留旧 Subject 兼容别名。

## 积压和错误

消息按至少一次投递处理。Consumer 必须使用 `event_id`/业务幂等键收敛重复投递；
`Nats-Msg-Id` 只能提供短窗口传输去重。

诊断顺序：

1. 永久错误：查看对应 Consumer 的 structured error log 和 terminated counter。
2. Archive 数据错误：查看 Archive 本地 quarantine。
3. 临时错误：查看 `NumRedelivered`、`AckPending` 和模块 retry counter。
4. 积压持续增长：检查 Consumer 是否运行、`AckWait` 是否覆盖最长处理时间，以及下游
   Storage/数据库是否超时。

系统不提供共享 DLQ 或集中重放协议。永久坏消息直接 TERM；临时错误留在原 Consumer
重投，Archive 额外保留本地隔离文件。

## 数据目录与恢复

JetStream 数据位于 `<deploy-dir>/data/eventbus/jetstream`，不进入发布包。备份应在业务
服务停止后执行，或使用一致性快照：

```bash
./stop.sh
tar -C data/eventbus -czf eventbus-jetstream-$(date +%Y%m%d%H%M%S).tar.gz jetstream
```

恢复时停止依赖服务，将目录放回原路径，校验属主和权限，然后先启动 EventBus。不要混用
另一套 Broker 的运行目录。

## 容量与安全

至少监控 Stream bytes/messages、Consumer pending/redelivered、数据盘使用率、readiness、
发布失败和连接数。磁盘达到 70% 时评估增长，达到 85% 前扩容或缩短消息窗口。

认证/TLS 在 `config/app.yaml` 和目标机 service environment 中配置。远程部署脚本不把
NATS 密码、credentials 或 TLS 私钥拼进 SSH 命令行。

初始化服务目录的 `eventbus.extra_config.nats_url` 是客户端 URL 唯一静态真源。用户在
`moox.toml` 的 `[eventbus]` 中只填写公网地址、端口和 TLS 开关；setup CLI 将其传给
部署脚本。脚本根据 `MOOX_EVENTBUS_PUBLIC_IP` 和 `MOOX_EVENTBUS_PORT` 写入
`tls://<host>:<port>`，并推导 Broker bind：
loopback 为 `127.0.0.1`，其他地址为 `0.0.0.0`。

设置 `MOOX_EVENTBUS_ENABLE_TLS=1` 时，部署流程 ensure/export 最小权限角色和私有 CA。
`cloudnode-eventbus` 负责发布和 ensure；`cloudnode-worker` 只允许 Consumer
INFO/FETCH/ACK，不允许 CREATE/DELETE、Stream 枚举、KV 或业务消息发布。公网连接必须
使用导出的 username/token/CA，不得使用 NATS JWT `--creds` 参数。用户不配置这些
凭据；系统生成并以 `0600` 导出 `cloudnode-worker.yaml`。

## 验证

```bash
./scripts/test/contract/test-deploy-moox-eventbus.sh
./scripts/check/verify-event-contracts.sh
```

测试使用临时 Broker 和临时持久化目录。
