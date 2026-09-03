# moox
一站式量化平台（web端/命令行）

MooX 当前定位为个人单用户系统。唯一登录用户拥有全部管理能力，不建设
`SUPER_ADMIN` 角色表或 RBAC；安全边界仍由登录、24 小时会话、请求签名、
nonce 防重放和密钥保护组成，单用户不等于取消认证。

仓库采用 Go Workspace 大仓组织，业务模块包括 Admin、Storage、Collector、
CloudNode、Trade、Factor、Strategy、Archive、Monitor、EventBus 和 HostAgent；
`packages/` 放置跨模块协议、鉴权、消息总线、运行时和健康检查等共享能力。
Factor 已包含定义导入、任务调度、Python worker 执行、Storage 读写和单次计算
CLI，不再是占位模块。

## 打包与部署

仓库有两个入口：

- `make release`：生成二进制归档包，包含 Gateway、核心服务二进制与配置、Storage schema、docs 和 `examples/` 示例元数据；Linux amd64/arm64 归档额外包含 hostagent 制品；不包含源码开发脚本或 Agent skills。
- `make release-binaries`：只编译并汇总所有可部署模块的二进制到 `release/moox-binaries-<version>-<goos>-<goarch>/bin`，同时刷新当前目标的 `release/bin`；产物包含 `manifest.txt` 校验清单和 `deploy/publish-release-binaries.sh` 发布脚本，适合后续只替换目标机器二进制。
- `make release-matrix`：一次生成 Linux amd64/arm64、macOS amd64/arm64 和 Windows amd64 归档，并为每个归档生成 SHA-256 校验文件。
- `make deploy`：通过 `scripts/deploy/deploy-moox.sh` 生成可运行部署目录并同步到本机或远端。Gateway 默认部署到每台机器；`--no-admin` 生成不含 Admin、浏览器资源、Admin schema 和 Admin 凭据的数据面节点。
- `make publish-release-binaries ARGS="--target user@host --dir /data/moox --restart"`：把 `release/bin` 中的二进制上传到已有部署目录；默认不覆盖配置、密钥、数据和日志，只有显式指定 `--restart` 才会重启服务。

`make release` 会打包 `cli`、`admin/admin-cli`、`gateway/gateway-cli`、`web-host`、`eventbus`、`cloudnode/cloudnode-cli`、`collector/collector-cli`、`factor/factor-cli`、`strategy/strategy-cli`、`trade/trade-cli`、`monitor/monitor-cli`、`storage/storage-cli` 和 `archive/archive-cli`；Linux amd64/arm64 还包含 HostAgent。需要把 SCF 入口也纳入平铺二进制发布时使用 `make release-binaries`，它会在 Linux amd64 额外生成 `moox-collector-scf`。`make deploy` 默认编排 Admin、Gateway、web-host、EventBus、CloudNode、Collector、Factor、Strategy、Monitor、Storage 和 Archive，可用 `--no-strategy`、`--no-monitor`、`--no-eventbus` 等开关关闭独立模块；Trade 当前通过 release 制品或模块构建单独部署。

`make release-binaries` 是二进制增量发布入口。Linux amd64 额外生成 `moox-collector-scf`；Linux 目标生成 HostAgent；Windows 二进制带 `.exe` 后缀。发布脚本使用 `rsync`，没有 `rsync` 时回退为 SSH+tar，不使用 `--delete`，因此不会清理目标机器上的历史文件。

例如生成 Linux amd64 制品：

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 STORAGE_CGO_ENABLED=0 VERSION=v0.1.0 make release-binaries
```

Storage 启用 DuckDB 的正式 Linux 制品仍需要 Linux 编译机和 `STORAGE_CGO_ENABLED=1`；可以在该编译环境直接运行本脚本，或先按既有 `make build-storage-linux` 流程准备 Storage，再将其他模块二进制一并放入 `bin/` 后使用 `--skip-build` 汇总。

给版本打 Tag 并推送后，GitHub Actions 和 CNB 会分别创建 Release 并上传同一组跨平台归档；对应配置是 `.github/workflows/release.yml` 和 `.cnb.yml`。本地只构建不发布时可运行 `VERSION=v0.1.0 make release-matrix`，也可用 `RELEASE_PLATFORMS=linux/amd64,windows/amd64` 缩小矩阵。跨平台构建默认对 Storage 使用 no-CGO fallback；具备目标平台 C 工具链时可显式设置 `STORAGE_CGO_ENABLED=1`。

macOS 不直接交叉编译启用 DuckDB CGO 的 Storage Linux 二进制。配置根目录 `moox.toml` 的 `[compile_host]` 后，运行 `make build-storage-linux`，脚本会通过 `moox-cli setup hosts` 获取脱敏主机信息，同步源码到该 Linux 主机并在那里使用原生 Go、GCC/G++ 编译；不会同步 `moox.toml`。

新系统初始化时，根目录 `moox.toml` 的 `[eventbus]` 只填写 SCF 可访问的公网
IPv4/DNS、端口和 TLS 开关。`setup deploy-control` 在控制节点部署 Admin、Gateway、
Web、EventBus、CloudNode、Collector 和 Monitor；EventBus token、私有 CA 与
`cloudnode-worker.yaml` 均由系统生成，不属于用户配置。

控制面和 Storage 部署完成后，用一个命令导入默认 A 股、加密货币与内部监控元数据：

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

命令会从固定的 `metadata.yaml` 同步 Admin 业务空间和 Storage
Space、DataSource、Dataset、Field、Column、View，完成 Dataset 激活后再次校验。
它可以重复执行；声明不一致时失败，不覆盖已有配置。完整流程见
[系统初始化](docs/setup.md)。

部署必须显式提供节点 ID、中央控制面 URL、只含公钥证书的 peer CA bundle，以及权限为 `0600` 的集群 control/service key 文件。control key 在 Admin 和所有 Gateway 间相同，service key 在所有 Gateway 和调用方间相同。

提交前统一运行 `make verify`。它会检查模块和 package 依赖边界、tRPC Context、gofmt、Prettier 与零 warning ESLint，遍历 `go.work` 执行所有 Go 测试和 vet，并完成管理台测试、生产构建、文档构建、发布契约、Gateway/Strategy 部署和 Caddy 契约检查；所有格式与 lint 门禁都是只读检查，不在 CI 中执行 `--fix` 或 `--write`。

Admin、CloudNode、Collector、Trade 的 SQLite schema 已内嵌进各自二进制，启动时自动应用；部署包只保留 Storage metadata 初始化所需的 `storage/schema/metadata.sql`。

## 管理面入口

MooX 的公开入口由 EdgeOne 和部署内置的 Caddy 提供。中央站点把 `/api/admin/*` 和 `/api/gateway-control/*` 转发到 Admin `127.0.0.1:11000`；每台机器都把 `/api/service/*` 转发到本机独立 Gateway `127.0.0.1:11002`。Gateway 健康端口通常固定为 `127.0.0.1:11012`；control profile 为 SCF Sentinel 将它绑定到 `0.0.0.0:11012`，并仅依靠独立 health HMAC 接受签名诊断请求。`--no-admin` 节点只启用 service HTTPS site，不启用浏览器 site 或控制面路由。

管理台登录使用 bcrypt 密码、一次性登录挑战、24 小时 JWT/session，登录后每个管理请求还必须带 nonce 防重放的会话 HMAC。后台接口使用独立 service HMAC，诊断端口使用独立 health HMAC。详见 [认证鉴权](docs/认证鉴权.md) 和 [管理台 HTTPS 与证书](docs/运维/管理台HTTPS与证书.md)。

中央控制面、每台机器独立 Gateway、节点路由和可用性边界见[节点服务网关架构](docs/节点服务网关架构.md)；两节点部署与互检操作见[Node Gateway 运维手册](docs/ops/node-gateway.md)。

## EventBus 与指标监控

`moox-eventbus` 是唯一的生产 NATS JetStream 所有者。Storage、CloudNode、Factor、Strategy
和各 tRPC 服务通过统一 `EventMessage` 直接连接 EventBus；发布包不会携带
JetStream 运行态数据。部署脚本先启动 EventBus、Storage、Monitor 和其他业务服务；
控制面与 Storage 都可用后，再显式执行 `moox-cli setup init` 导入并校验默认元数据。

当前公共事件固定为五个：CloudNode 任务命令、两类 metrics、Storage committed upsert
和 Strategy 调仓命令。Collector 闭合 K 线后直接写 Storage；Storage 写入触发
View/Factor/Archive；Strategy 只为 paper/live execution binding 发布调仓命令，Trade
提交 Inbox 和调仓计划后由本地 wake 立即推进。EventBus 只创建 Stream/KV，各业务服务
通过 `NewConsumer` 创建并拥有自己的 Consumer。

每个服务的本地 timer 每 30 秒主动上报 Prometheus registry 快照到 EventBus，
Monitor 消费后把历史写入 Storage 并提供 MooX 看板和结构化多指标阈值告警。
系统不部署 Prometheus Server、Pushgateway，也不提供手工监控 target API。
Storage 部署流程只负责注册 DataNode；随后 `setup init` 导入直接包含
`data_node_id` 的逻辑元数据。Dataset 默认 disabled，必须完成只读激活检查后显式激活。详见
[`docs/运维/MooX-EventBus运维.md`](docs/运维/MooX-EventBus运维.md) 和
[`docs/运维/MooX指标监控.md`](docs/运维/MooX指标监控.md)。

运行数据不是统一永久保留：JetStream Stream/KV、CloudNode 短期任务历史、
Monitor 检查历史和 Storage 派生 View 都有时间或容量边界；行情事实和 Archive
Parquet 则不会被通用清理器静默删除。各类数据的默认保留时间、自动清理方式、
仍会持续增长的目录及磁盘巡检方法见
[`docs/运维/数据保留与磁盘空间.md`](docs/运维/数据保留与磁盘空间.md)。

## 服务监控

`moox-monitor` 是单实例监控事实存储，通过 SysDeploy 同步内置 `mooxsys` 检查，并提供有界 `GetDoctorContext`。初始部署后可手工运行 `moox-cli doctor bootstrap --format json`；日常定位运行 `moox-cli doctor diagnose --format json`。V1 没有 Monitor HA、Doctor 守护进程、自动修复、Trade 模拟盘或 Full Canary。Storage 正在独立重构，Doctor 只检查其 inventory 和现有 health/Reporter 事实，功能水位固定显示为延期。所有独立部署进程的 `/healthz`、`/readyz` 和 `/metrics` 都是内部诊断面，需要独立 health HMAC；公开 Caddy 端口对诊断路由返回 `404`。详见 [MooX Doctor 运维](docs/运维/MooX-Doctor运维.md)。

`moox-host-agent` 是独立的 Linux amd64/arm64 用户进程，只读取 CPU、内存、文件系统、磁盘 I/O 和网络 ABI，通过私有 CA TLS 的 EventBus best-effort 上报到 Monitor；Agent 不持久化样本。发布和 rootless 部署入口位于 `skills/moox/scripts/hostagent-release.sh` 与 `hostagent-deploy.sh`，EventBus 凭据由 Admin `t_secrets` CLI 统一生成和轮换。

本机发布并拉起：

```bash
make deploy ARGS="--target localhost --dir /data/moox/dev --node-id gateway-dev --gateway-control-url http://127.0.0.1:11000 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key"
```

只生成发布目录，不启动服务：

```bash
make deploy ARGS="--target localhost --dir /tmp/moox --skip-build --no-start --node-id gateway-dev --gateway-control-url http://127.0.0.1:11000 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key"
```

远端发布并拉起：

```bash
make deploy ARGS="--target user@host --dir /data/moox --goos linux --goarch amd64 --public-host node.example.com --node-id gateway-node-1 --gateway-control-url https://admin.example.com:9527 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key"
```

公开部署应加 `--public-host <IP-or-DNS>`。部署会自动安装 checksum 校验的固定版本 Caddy、创建私有 CA、配置同机后端信任并做 HTTPS 验收；浏览器所在机器仍需使用 `skills/moox/scripts/caddy-ca.sh` 显式安装 CA 信任。

发布目录中的数据、日志、运行态文件固定放在：

```text
<deploy-dir>/data
<deploy-dir>/logs
<deploy-dir>/run
```

因此 Admin 的 SQLite 数据库会写到 `<deploy-dir>/data/admin.db`，Storage 的 Pebble/DuckDB/Bleve 等文件会写到 `<deploy-dir>/data/storage`，不会再落到源码目录。
