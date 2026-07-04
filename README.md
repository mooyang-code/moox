# moox
一站式量化平台（web端/命令行）

## 打包与部署

仓库有两个入口：

- `make release`：生成二进制归档包，包含核心二进制、admin/cloudnode/collector/storage 配置、Storage schema、docs 和 `examples/` 示例元数据；不包含源码开发脚本或 Agent skills。
- `make deploy`：通过 `scripts/deploy-moox.sh` 生成可运行部署目录并同步到本机或远端，包含 admin/cli/web-host 以及按开关启用的 cloudnode/collector/storage，附带配置、Storage schema、`examples` 示例元数据，以及 `start.sh`、`stop.sh`、`status.sh`。

`make release` 会打包 `cli/admin/web-host/cloudnode/collector/collector-scf/factor/trade/storage` 二进制；配置目录当前随包包含 admin、cloudnode、collector、storage。`make deploy` 当前负责 admin、web-host、cloudnode、collector、storage 的可运行部署，不启动 trade/factor；trade/factor 需要按模块 README 独立运行。

Admin、CloudNode、Collector、Trade 的 SQLite schema 已内嵌进各自二进制，启动时自动应用；部署包只保留 Storage metadata 初始化所需的 `storage/schema/metadata.sql`。

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
