# moox-cloudnode

云账户、SCF 节点、代码包与函数发布，供 Collector 管理采集函数。

设计文档：[云节点](../../docs/模块/云节点.md)

## 构建与测试

```bash
./scripts/build/build.sh cloudnode
./scripts/build/build.sh cloudnode-cli
go test -count=1 ./modules/cloudnode/...
```

## 配置

`config/app.yaml`、`config/trpc_go.yaml`；schema 在 `schema/`。
