# moox
一站式量化平台（web端/命令行）

## 打包与部署

仓库有两个入口：

- `make release`：生成二进制归档包，包含 Gateway、核心服务配置、Storage schema、docs 和 `examples/` 示例元数据；Linux amd64/arm64 归档额外包含 hostagent 制品；不包含源码开发脚本或 Agent skills。
- `make deploy`：通过 `scripts/deploy-moox.sh` 生成可运行部署目录并同步到本机或远端。Gateway 默认部署到每台机器；`--no-admin` 生成不含 Admin、浏览器资源、Admin schema 和 Admin 凭据的数据面节点。

`make release` 会同时打包 `moox-gateway` 和 `moox-gateway-cli`。部署必须显式提供节点 ID、中央控制面 URL、只含公钥证书的 peer CA bundle，以及权限为 `0600` 的集群 control/service key 文件。control key 在 Admin 和所有 Gateway 间相同，service key 在所有 Gateway 和调用方间相同。

Admin、CloudNode、Collector、Trade 的 SQLite schema 已内嵌进各自二进制，启动时自动应用；部署包只保留 Storage metadata 初始化所需的 `storage/schema/metadata.sql`。

## 管理面入口

MooX 的公开入口由 EdgeOne 和部署内置的 Caddy 提供。中央站点把 `/api/admin/*` 和 `/api/gateway-control/*` 转发到 Admin `127.0.0.1:11000`；每台机器都把 `/api/service/*` 转发到本机独立 Gateway `127.0.0.1:11002`。Gateway 健康端口固定为 `127.0.0.1:11012`。`--no-admin` 节点只启用 service HTTPS site，不启用浏览器 site 或控制面路由。

管理台登录使用 bcrypt 密码、一次性登录挑战、24 小时 JWT/session，登录后每个管理请求还必须带 nonce 防重放的会话 HMAC。后台接口使用独立 service HMAC，诊断端口使用独立 health HMAC。详见 [认证鉴权](docs/认证鉴权.md) 和 [管理台 HTTPS 与证书](docs/运维/管理台HTTPS与证书.md)。

中央控制面、每台机器独立 Gateway、节点路由和可用性边界见[节点服务网关架构](docs/节点服务网关架构.md)；两节点部署与互检操作见[Node Gateway 运维手册](docs/ops/node-gateway.md)。

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
make deploy ARGS="--target localhost --dir ~/moox/dev --node-id gateway-dev --gateway-control-url http://127.0.0.1:11000 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key"
```

只生成发布目录，不启动服务：

```bash
make deploy ARGS="--target localhost --dir /tmp/moox --skip-build --no-start --node-id gateway-dev --gateway-control-url http://127.0.0.1:11000 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key"
```

远端发布并拉起：

```bash
make deploy ARGS="--target user@host --dir ~/moox/prod --goos linux --goarch amd64 --public-host node.example.com --node-id gateway-node-1 --gateway-control-url https://admin.example.com:9527 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key"
```

公开部署应加 `--public-host <IP-or-DNS>`。部署会自动安装 checksum 校验的固定版本 Caddy、创建私有 CA、配置同机后端信任并做 HTTPS 验收；浏览器所在机器仍需使用 `skills/moox/scripts/caddy-ca.sh` 显式安装 CA 信任。

发布目录中的数据、日志、运行态文件固定放在：

```text
<deploy-dir>/data
<deploy-dir>/logs
<deploy-dir>/run
```

因此 Admin 的 SQLite 数据库会写到 `<deploy-dir>/data/admin.db`，Storage 的 Pebble/DuckDB/Bleve 等文件会写到 `<deploy-dir>/data/storage`，不会再落到源码目录。
