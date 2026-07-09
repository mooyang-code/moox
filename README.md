# moox
一站式量化平台（web端/命令行）

## 打包与部署

仓库有两个入口：

- `make release`：生成二进制归档包，包含核心二进制、admin/cloudnode/collector/storage 配置、Storage schema、docs 和 `examples/` 示例元数据；不包含源码开发脚本或 Agent skills。
- `make deploy`：通过 `scripts/deploy-moox.sh` 生成可运行部署目录并同步到本机或远端，包含 admin/cli/web-host 以及按开关启用的 cloudnode/collector/storage，附带配置、Storage schema、`examples` 示例元数据，以及 `start.sh`、`stop.sh`、`status.sh`。

`make release` 会打包 `cli/admin/web-host/cloudnode/collector/collector-scf/factor/trade/monitor/storage` 二进制；配置目录随包包含 admin、cloudnode、collector、factor、monitor、storage。`make deploy` 默认负责 admin、web-host、cloudnode、collector、factor、monitor、storage 的可运行部署，可用 `--no-monitor` 等开关关闭独立模块。

Admin、CloudNode、Collector、Trade 的 SQLite schema 已内嵌进各自二进制，启动时自动应用；部署包只保留 Storage metadata 初始化所需的 `storage/schema/metadata.sql`。

## 服务监控

`moox-monitor` 是独立 HTTP/TCP 可用性监控模块，和 Admin 内原有主机资源监控并存。它通过 SysDeploy 同步内置 `moox-system` 检查，也支持手动检查、webhook 告警和多 monitor 实例 peer 去重。所有独立部署进程提供标准 `/healthz`，monitor 自身也提供 `/healthz` 与 peer snapshot API。

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

发布目录中的数据、日志、运行态文件固定放在：

```text
<deploy-dir>/data
<deploy-dir>/logs
<deploy-dir>/run
```

因此 Admin 的 SQLite 数据库会写到 `<deploy-dir>/data/admin.db`，Storage 的 Pebble/DuckDB/Bleve 等文件会写到 `<deploy-dir>/data/storage`，不会再落到源码目录。
