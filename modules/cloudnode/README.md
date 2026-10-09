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

CloudNodeMgr 仅在 loopback 11401 提供原生 tRPC，由主机网关转发；空间标识读取 `X-Space-Id` 元数据。Collector、操作员 CLI、Admin 垃圾回收分别使用 `collector`、`moox-cli`、`admin` 身份。RPC 超时上限为 960 秒，保留 SCF 同步调用的完整预算；COS 预签名上传使用 HTTPS。本模块无需 CGO，在本机编译或交叉编译。
