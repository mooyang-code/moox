# moox-storage

数据存储：primary（元数据、字段级事实、周期完成事件转发、周期账本、保留期清理）与 view（可重建 View）两个角色。外部身份（云函数、Skill）经主机网关的外部入口访问。

设计文档：[存储](../../docs/模块/存储.md)

## 构建与测试

```bash
./scripts/build/build.sh storage
./scripts/build/build.sh storage-cli
go test -count=1 ./modules/storage/...
```

## 配置

`config/storage*.yaml` 与 `config/trpc_go*.yaml` 按角色区分，`config/storage_view/` 为 View 角色配置；元数据 schema 在 `schema/metadata.sql`。需要 DuckDB 的 Linux 制品在本机的 Docker 编译镜像里构建（`bash scripts/build/build-linux-local.sh storage`，见 [构建手册](../../docs/构建手册.md)）。

Metadata 20100、PrimaryStore 20102 与 DataView 20103 仅提供 loopback 原生 tRPC，没有调用方密钥：访问控制由主机网关按服务目录做。健康检查是独立的 HTTP 端口，只监听本机。

Storage policy 分别配置 Primary 保留期、View 根数与 View 写入年龄上限例外。View 默认每序列保留最近 10000 根，更旧的行定时删除；单个 View 文件超过 4GiB 只告警，不动数据；实时、补采及重建回填都会过滤超过 View 写入年龄上限的行（默认由 `view_rows_per_series` 向上取整到整天推导，10000 根的 1m 为 7d，有交易时段空间取数据集保留期；Primary 1m 默认 35d）。年龄过滤不返回写入错误，也不主动按年龄清理已有索引，具体默认值与边界见设计文档。
