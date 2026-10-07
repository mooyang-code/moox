# moox-archive

行情归档：消费 Storage 行变更，写本地 Journal 并按月物化为 Parquet，可选同步 COS。当前环境未部署。

设计文档：[归档](../../docs/模块/归档.md)

## 构建与测试

```bash
./scripts/build/build.sh archive
go test -count=1 ./modules/archive/...
```

## 配置

`config/app.yaml`（归档的空间与数据集、EventBus、COS）、`config/trpc_go.yaml`。
