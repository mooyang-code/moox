# 构建

在仓库根目录执行构建和边界检查：

```bash
make build
make check-boundaries
```

`make build` 把二进制写到 `bin/`：

| 二进制 | 说明 |
| --- | --- |
| `moox-cli` | 运维命令行 |
| `moox-admin`、`moox-admin-cli` | 管理后台及其离线命令 |
| `moox-host-gateway`、`moox-host-gateway-cli` | 主机网关 |
| `moox-access` | 外部接入 |
| `moox-egress-proxy` | 出口代理 |
| `moox-web-host` | 控制台前端 |
| `moox-eventbus` | 消息总线 |
| `moox-cloudnode`、`moox-collector`、`moox-factor-mgr`、`moox-factor-engine`、`moox-strategy`、`moox-trade`、`moox-monitor`、`moox-archive`，以及各自的 `-cli`（有的话） | 业务组件 |
| `moox-storage-primary`、`moox-storage-node`、`moox-storage-view`、`moox-storage-cli` | 存储，按角色拆成三个二进制 |
| `moox-host-agent`、`moox-host-agent-cli` | 主机采集器（只构建 Linux amd64/arm64） |

单个目标：`./scripts/build/build.sh <目标>`，例如 `admin`、`host-gateway`、`access`、`egress-proxy`、`storage`、`host-agent`。SCF 运行时 `moox-collector-scf`
不在 `make build` 里，用 `./scripts/build/build.sh collector-scf` 单独构建，再由 `scripts/build/build-collector-scf-package.sh` 打包。

Pebble 是在线有序存储 PrimaryStore 使用的引擎，不依赖外部的 C++ KV 库。DuckDB 需要 CGO：Storage 的 Linux 二进制
在 `[compile_host]` 指定的编译主机上构建（`scripts/build/build-storage-linux.sh`，部署命令会自动调用，也可以单独执行
`moox-cli setup build-linux`），不要在 macOS 上用 `GOOS=linux CGO_ENABLED=1` 交叉编译。

控制台前端在编译主机上用 `pnpm build:prod` 构建，再在 `web-host` 里 `make statik` 嵌入静态资源，见 `docs/模块/前端.md`。
