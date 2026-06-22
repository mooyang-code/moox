# moox
一站式量化平台（web端/命令行）

## 打包发布

统一发布入口位于 `scripts/deploy-moox.sh`，支持发布到本机目录或远端机器目录。发布包会包含核心二进制、Control/Storage 配置、Storage schema、`examples/` 示例元数据，并在发布目录内生成 `start.sh`、`stop.sh`、`status.sh`。

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

因此 Control 的 SQLite 数据库会写到 `<deploy-dir>/data/moox.db`，Storage 的 Pebble/DuckDB/Bleve 等文件会写到 `<deploy-dir>/data/storage`，不会再落到源码目录。
