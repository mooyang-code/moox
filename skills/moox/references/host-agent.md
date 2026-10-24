# Host Agent 运维

Host Agent 是只支持 Linux `amd64`/`arm64` 的主机服务。它只采集 CPU、内存、文件系统、磁盘和网络，使用共享 JetStream 客户端把 `HostMetric` 发布到 EventBus；不保存 SQLite、outbox 或重放队列。

## 安装

Host Agent 与 Host Gateway 一起组成每台主机的 `host` 单元（`/data/moox/host/current`，由 `moox-runtime` 管理），
不单独安装。首次安装用 `moox-cli setup deploy-host --host <主机ID>`（control 由 `setup bootstrap --stage core` 安装），
命令从运行中的 Admin 导出该主机的身份和 `hostagent-publisher` 凭据，写进发布目录的 `host-agent/config/eventbus.yaml`。
完整顺序见 `docs/部署与运维.md` 第 2 节。

## 验收和升级

```bash
curl --fail http://127.0.0.1:11425/healthz
```

升级二进制按 [`native-release.md`](native-release.md) 发布 `host` 单元；只换二进制不更新配置和凭据。

## 凭据轮换

EventBus CA 或 `hostagent-publisher` 口令轮换会立刻让旧副本失效。只升级 Host Agent 二进制、只在 control 上 `export`、
或只重启 EventBus，都不够。完整副本清单和验收步骤见 [`eventbus-credentials.md`](eventbus-credentials.md)。

对 **moox.toml 里每一台主机** 都要重新安装 `host` 单元，包括海外机、因子机等只出站的主机。确认 Monitor 收到新样本，
且日志里没有 `Authorization Violation` / `certificate signature failure` 后才算完成。
