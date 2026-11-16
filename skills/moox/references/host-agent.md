# Host Agent 运维

Host Agent 是只支持 Linux `amd64`/`arm64` 的主机服务。它只采集 CPU、内存、文件系统、磁盘和网络，使用共享 JetStream 客户端把 `HostMetric` 发布到 EventBus；不保存 SQLite、outbox 或重放队列。

## 安装

Host Agent 与 Host Gateway 是每台主机都有的主机级组件，不写在 `[deployments]` 里，随 `moox-cli setup deploy --host <主机ID>` 一起装（systemd 用户服务 `moox-host-agent`）。
部署从 Admin 导出这台主机的身份和 `hostagent-publisher` 凭据，写到 `<部署根>/secrets/eventbus/hostagent-publisher.yaml`，配置在 `<部署根>/config/host-agent/app.yaml`。
完整顺序见 `docs/部署与运维.md`。

## 验收和升级

```bash
./bin/moox-cli setup status --host <主机ID>          # host-agent 是 active
./bin/moox-cli setup logs --host <主机ID> --component host-agent --journal
```

健康端口只听 127.0.0.1、不验签：在目标机上 `curl http://127.0.0.1:11425/readyz` 即可，远程连不上；操作电脑上用 `moox-cli doctor` 经 SSH 代为请求。

升级：`setup deploy --host <主机ID> --component host-agent`（见 [`native-release.md`](native-release.md)），二进制、配置和凭据一起更新。

## 凭据轮换

`hostagent-publisher` 口令轮换会立刻让旧副本失效（EventBus 没有单独的 CA，用所在主机的主机证书）。只升级 Host Agent 二进制、只在 control 上 `export`、
或只重启 EventBus，都不够。完整副本清单和验收步骤见 [`eventbus-credentials.md`](eventbus-credentials.md)。

对 **moox.toml 里每一台主机** 都要重新部署（`setup deploy --host <主机ID>`），包括海外机、因子机等只出站的主机。确认 Monitor 收到新样本，
且日志里没有 `Authorization Violation` / `certificate signature failure` 后才算完成。
