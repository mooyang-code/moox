# 主机采集器运维

主机采集器（组件 `host-agent`，二进制 `moox-host-agent`）是每台 MooX 主机自动部署的主机组件，只支持 Linux `amd64`/`arm64`。
它只采集 CPU、内存、文件系统、磁盘和网络，使用共享 JetStream 客户端把 `HostMetric` 发布到 EventBus；不保存 SQLite、outbox 或重放队列。

## 部署

主机采集器不用写进 `moox.toml` 的 `[placements]`，随主机一起部署：

```bash
moox-cli setup deploy-host --host <主机>                            # 整台主机
moox-cli setup deploy-host --host <主机> --components host-agent    # 只更新主机采集器
```

部署工具从 control 复制 `hostagent-publisher.yaml` 与 `ca.pem` 到该主机的 `secrets/eventbus/`，并下发 health HMAC。
凭据文件必须是普通用户可读的 `0600` 文件，不要把 token 放进命令行参数或发布包。identity 文件在
`~/.local/state/moox/hostagent/identity.yaml`，升级时复用。

## 验收、升级和回滚

```bash
moox-cli setup service status --host <主机> --components host-agent
moox-cli doctor diagnose --node <主机>
```

健康端口 `11425` 需要 health HMAC，不要用未签名的 `curl` 访问。升级用 `deploy-host` 重新部署，原子切换 `current` 链接；
回滚用 `moox-cli setup rollback --host <主机>`。

## 凭据轮换

EventBus CA 或任一 role token 轮换都会立刻让旧副本失效。只升级二进制、只在 control 上 `export`、或只重启 EventBus，都不够。
完整的消费者清单和验收步骤见 [`eventbus-credentials.md`](eventbus-credentials.md)。

二进制已在目标机上时，用 `--reuse-binaries` 只同步凭据：

```bash
moox-cli setup deploy-host --host <主机> --reuse-binaries
```

对部署表里每一台主机都要执行。确认 `moox-cli setup service status` 里组件是「运行中 … 就绪」、日志
`logs/host-agent/stdout.log` 出现 `sample.timer launch success`、Monitor 收到新样本，且没有 `Authorization Violation` /
`certificate signature failure`，再删除本机的临时凭据文件。
