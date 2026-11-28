# Host Agent 运维

Host Agent 是主机服务：程序只能在 Linux `amd64`、`arm64` 上运行，其他平台启动即报错；发布包只提供 linux/amd64，arm64 主机要自己编译后用 `setup deploy --binary-dir` 部署（见 [install.md](install.md)）。它只采集 CPU、内存、文件系统、磁盘和网络，每分钟经本机网关调 Monitor 的 `SyncHostAgent` 把 `HostMetric` 报给 Monitor 并同步告警渠道；不连 EventBus，不保存 SQLite、outbox 或重放队列。

## 安装

Host Agent 与 Host Gateway 是每台主机都有的主机级组件，不写在 `[deployments]` 里，随 `moox-cli setup deploy --host <主机ID>` 一起装（systemd 用户服务 `moox-host-agent`）。
部署给它这台主机的身份（主机签名密钥，经本机网关调用 Monitor 用），配置在 `<部署根>/config/host-agent/app.yaml`；没有 EventBus 凭据。
完整顺序见 `docs/部署与运维.md`。

## 验收和升级

```bash
./bin/moox-cli setup status --host <主机ID>          # host-agent 是 active
./bin/moox-cli setup logs --host <主机ID> --component host-agent --journal
```

健康端口只听 127.0.0.1、不验签：在目标机上 `curl http://127.0.0.1:11425/readyz` 即可，远程连不上；操作电脑上用 `moox-cli doctor` 经 SSH 代为请求。

升级：`setup deploy --host <主机ID> --component host-agent`（见 [`source-deploy.md`](source-deploy.md)），二进制、配置和凭据一起更新。

## 凭据

Host Agent 只用主机签名密钥（随身份包签发），没有 EventBus 令牌，EventBus 口令轮换与它无关。快照报不出去时先看本机网关与控制机是否可达
（`moox-cli setup logs --host <主机ID> --component host-agent`），非控制机连续 3 分钟拉不到主机策略会直推“控制机或 Monitor 不可达”。
