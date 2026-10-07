# moox-storage

数据存储：元数据（primary）、字段级事实与 Outbox（node）、可重建 View（view）和外部访问代理（access）四个角色。

设计文档：[存储](../../docs/模块/存储.md)

## 构建与测试

```bash
./scripts/build/build.sh storage
./scripts/build/build.sh storage-cli
go test -count=1 ./modules/storage/...
```

## 配置

`config/storage*.yaml` 与 `config/trpc_go*.yaml` 按角色区分，`config/access/`、`config/storage_view/` 为对应角色配置；元数据 schema 在 `schema/metadata.sql`。需要 DuckDB 的 Linux 制品在编译机上构建（`moox-cli setup build-linux`）。
