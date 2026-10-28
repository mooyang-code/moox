# moox-storage

数据存储：元数据与路由（primary）、字段级事实与 Outbox（node）、可重建 View（view）三个角色。外部访问由独立的 `modules/access` 模块代理。

设计文档：[存储](../../docs/模块/存储.md)

## 构建与测试

```bash
./scripts/build/build.sh storage
./scripts/build/build.sh storage-cli
go test -count=1 ./modules/storage/...
```

## 配置

`config/storage*.yaml` 与 `config/trpc_go*.yaml` 按角色区分，`config/storage_view/` 为 View 角色配置；Access 配置在 `modules/access/config/`，元数据 schema 在 `schema/metadata.sql`。需要 DuckDB 的 Linux 制品在本机的 Docker 编译镜像里构建（`moox-cli setup build-linux`，见 [构建手册](../../docs/构建手册.md)）。

Metadata 20100、PrimaryStore 20101/20102 与 DataView 20103 仅提供 loopback 原生 tRPC；20200/20201/20202 已删除。CLI 运维经操作员 SSH 网关访问，独立健康 HTTP 保留。

Storage policy 分别配置 Primary 保留期、View 根数与 View 写入年龄上限。View 默认目标为每序列 10000 根、超过 12000 根整理，文件阈值 4GiB；实时、补采及重建回填都会过滤超过 `[storage_view.max_age]` 的行（默认 1m 7d，Primary 1m 35d）。年龄过滤不返回写入错误，也不主动按年龄清理已有索引，具体默认值与边界见设计文档。
