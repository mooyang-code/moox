# Host Agent 运维

Host Agent 是只支持 Linux `amd64`/`arm64` 的独立用户态服务。它只采集 CPU、内存、文件系统、磁盘和网络，使用共享 JetStream 客户端把 `HostMetric` 发布到 EventBus；不保存 SQLite、outbox 或重放队列。

## 首次发布

先在管理端生成或导出 host-agent 角色凭据和私有 CA，再把两个临时文件交给部署脚本。凭据文件必须是普通用户可读的 `0600` 文件，不要把 token 放进命令行参数或 release archive。

```bash
./skills/moox/scripts/hostagent-release.sh
./skills/moox/scripts/hostagent-deploy.sh user@host release/moox-host-agent-<version>-linux-amd64.tar.gz \
  --eventbus-file /tmp/hostagent-eventbus.yaml --ca-file /tmp/eventbus-ca.pem
```

部署脚本不使用 root，安装到 `~/.local/lib/moox/hostagent`，凭据写到 `~/.config/moox/hostagent`，identity 写到 `~/.local/state/moox/hostagent/identity.yaml`，并通过 `systemctl --user` 管理服务。

## 验收和升级

```bash
curl --fail http://127.0.0.1:11425/healthz
systemctl --user status moox-host-agent.service
```

升级会复用 identity 文件；发布新 archive 后重复执行 deploy 即可原子切换 `current` 链接。回滚时把 `current` 指向旧 release 目录并执行 `systemctl --user restart moox-host-agent.service`。

## 凭据轮换

EventBus role token 或 CA 轮换前，应先提示影响范围。轮换不会假设无中断：重新导出 host-agent 凭据、再次执行 deploy，确认 health 和 Monitor 的新样本到达后再删除旧凭据。
