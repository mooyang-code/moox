# moox
一站式量化平台（web端/命令行）

## 打包与部署

仓库有两个入口：

- `make release`：生成二进制归档包，包含核心二进制、admin/eventbus/cloudnode/collector/storage 配置、Storage schema、docs 和 `examples/` 示例元数据；Linux amd64/arm64 归档额外包含 hostagent 制品；不包含源码开发脚本或 Agent skills。
- `make deploy`：通过 `scripts/deploy-moox.sh` 生成可运行部署目录并同步到本机或远端，包含 admin/cli/web-host/eventbus 以及按开关启用的 cloudnode/collector/storage，附带配置、Storage schema、`examples` 示例元数据，以及 `start.sh`、`stop.sh`、`status.sh`。

`make release` 会打包 `cli/admin/web-host/eventbus/cloudnode/collector/collector-scf/factor/trade/monitor/storage` 二进制；配置目录随包包含 admin、eventbus、cloudnode、collector、factor、monitor、storage。`make deploy` 默认负责 admin、web-host、eventbus、cloudnode、collector、factor、monitor、storage 的可运行部署，可用 `--no-monitor`、`--no-eventbus` 等开关关闭独立模块。

Admin、CloudNode、Collector、Trade 的 SQLite schema 已内嵌进各自二进制，启动时自动应用；部署包只保留 Storage metadata 初始化所需的 `storage/schema/metadata.sql`。

## 管理面入口

MooX 的公开入口由 EdgeOne 和部署内置的 Caddy 提供：EdgeOne 负责 CNAME、WAF、CC/Bot 和缓存；浏览器使用 `https://<host>:9527`，后台/SCF 使用 `https://<host>:11001/api/service/*`。Caddy 把站点请求转发到 `127.0.0.1:9528`，把 `/api/admin/*` 转发到 `127.0.0.1:11000`，把 `/api/service/*` 转发到 `127.0.0.1:11002`。web-host 仅提供静态文件，不代理 API；其余端口必须保持私有。接入与回滚见 [EdgeOne 运维手册](docs/运维/EdgeOne接入与应急回滚.md)。

管理台登录使用 bcrypt 密码、一次性登录挑战、24 小时 JWT/session，登录后每个管理请求还必须带 nonce 防重放的会话 HMAC。后台接口使用独立 service HMAC，诊断端口使用独立 health HMAC。详见 [认证鉴权](docs/认证鉴权.md) 和 [管理台 HTTPS 与证书](docs/运维/管理台HTTPS与证书.md)。

## EventBus 与指标监控

`moox-eventbus` 是唯一的生产 NATS JetStream 所有者。Storage、CloudNode、Factor
和各 tRPC 服务通过统一 `MooxMessage` 直接连接 EventBus；发布包不会携带
JetStream 运行态数据。部署启动顺序为 EventBus -> Storage -> Metadata
`metadata apply` 预检 -> Monitor -> 其他业务服务。

每个服务的本地 timer 每 30 秒主动上报 Prometheus registry 快照到 EventBus，
Monitor 消费后把历史写入 Storage 并提供 MooX 看板和结构化多指标阈值告警。
系统不部署 Prometheus Server、Pushgateway，也不提供手工监控 target API。
外部或多机 Storage 部署需要设置 `MOOX_METRICS_STORAGE_ROUTE_SEED`；单机部署
默认使用 `examples/metadata-monitor-metrics-local-route.seed.yaml`。详见
[`docs/运维/MooX-EventBus运维.md`](docs/运维/MooX-EventBus运维.md) 和
[`docs/运维/MooX指标监控.md`](docs/运维/MooX指标监控.md)。

## 服务监控

`moox-monitor` 是独立 HTTP/TCP 可用性监控模块，和 Admin 内原有主机资源监控并存。它通过 SysDeploy 同步内置 `moox-system` 检查，也支持手动检查、webhook 告警和多 monitor 实例 peer 去重。所有独立部署进程的 `/healthz`、`/readyz` 和 `/metrics` 都是内部诊断面，需要独立 health HMAC；公开 Caddy 端口对诊断路由返回 `404`。

`moox-host-agent` 是独立的 Linux amd64/arm64 用户进程，只读取 CPU、内存、文件系统、磁盘 I/O 和网络 ABI，通过私有 CA TLS 的 EventBus best-effort 上报到 Monitor；Agent 不持久化样本。发布和 rootless 部署入口位于 `skills/moox/scripts/hostagent-release.sh` 与 `hostagent-deploy.sh`，EventBus 凭据由 Admin `t_secrets` CLI 统一生成和轮换。

本机发布并拉起：

```bash
make deploy ARGS="--target localhost --dir ~/moox/dev"
```

只生成发布目录，不启动服务：

```bash
make deploy ARGS="--target localhost --dir /tmp/moox --skip-build --no-start"
```

远端发布并拉起：

```bash
make deploy ARGS="--target user@host --dir ~/moox/prod --goos linux --goarch amd64"
```

公开部署应加 `--public-host <IP-or-DNS>`。部署会自动安装 checksum 校验的固定版本 Caddy、创建私有 CA、配置同机后端信任并做 HTTPS 验收；浏览器所在机器仍需使用 `skills/moox/scripts/caddy-ca.sh` 显式安装 CA 信任。

发布目录中的数据、日志、运行态文件固定放在：

```text
<deploy-dir>/data
<deploy-dir>/logs
<deploy-dir>/run
```

因此 Admin 的 SQLite 数据库会写到 `<deploy-dir>/data/admin.db`，Storage 的 Pebble/DuckDB/Bleve 等文件会写到 `<deploy-dir>/data/storage`，不会再落到源码目录。
