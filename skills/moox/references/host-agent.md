# Host Agent 运维

Host Agent 是只支持 Linux `amd64`/`arm64` 的独立用户态服务。它只采集 CPU、内存、文件系统、磁盘和网络，使用共享 JetStream 客户端把 `HostMetric` 发布到 EventBus；不保存 SQLite、outbox 或重放队列。

## 首次发布

先在管理端生成或导出 host-agent 角色凭据和私有 CA，再把两个临时文件交给部署脚本。凭据文件必须是普通用户可读的 `0600` 文件，不要把 token 放进命令行参数或 release archive。

```bash
./skills/moox/scripts/hostagent-release.sh
./skills/moox/scripts/hostagent-deploy.sh user@host release/moox-host-agent-<version>-linux-amd64.tar.gz \
  --eventbus-file /tmp/hostagent-publisher.yaml \
  --ca-file /tmp/eventbus-ca.pem \
  --health-auth-file /tmp/hostagent-health-auth.env
```

部署脚本不使用 root，安装到 `~/.local/lib/moox/hostagent`，凭据写到 `~/.config/moox/hostagent`，identity 写到 `~/.local/state/moox/hostagent/identity.yaml`，并通过 `systemctl --user` 管理服务。Control 上的 `~/.config/moox/eventbus/` 与远程 Host Agent 的 `~/.config/moox/hostagent/` 是两套副本；`deploy-control` / `deploy-service` 不会覆盖后者。

## 验收和升级

```bash
curl --fail http://127.0.0.1:11425/healthz
systemctl --user status moox-host-agent.service
```

升级会复用 identity 文件；发布新 archive 后重复执行 deploy 即可原子切换 `current` 链接。回滚时把 `current` 指向旧 release 目录并执行 `systemctl --user restart moox-host-agent.service`。

## 凭据轮换

EventBus CA 或任一 role token 轮换都会立刻让旧副本失效。只升级 Host Agent 二进制、只在 control 上 `export`、或只重启 EventBus，都不够。完整消费者清单和验收步骤见 [`eventbus-credentials.md`](eventbus-credentials.md)。

二进制已在目标机时，用 `--credentials-only` 同步 CA 和 `hostagent-publisher` 口令：

```bash
./skills/moox/scripts/hostagent-deploy.sh user@host --credentials-only \
  --eventbus-file /tmp/hostagent-publisher.yaml \
  --ca-file /tmp/eventbus-ca.pem
```

对 **moox.toml 里每一台跑了 Host Agent 的主机** 都要执行，包括 compute 节点。确认 `systemctl --user is-active` 为 `active`、日志出现 `sample.timer launch success`、Monitor 收到新样本，且没有 `Authorization Violation` / `certificate signature failure` 后再删除本机临时凭据文件。
