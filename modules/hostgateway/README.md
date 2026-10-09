# moox-host-gateway

每台机器一个的节点服务网关：校验服务签名，只把请求转发到 Admin 为本节点登记的本机服务。

设计文档：[节点网关](../../docs/模块/节点网关.md)

## 构建与测试

```bash
./scripts/build/build.sh host-gateway
go test -count=1 ./modules/hostgateway/...
```

## 配置

`config/app.yaml`（节点 ID、控制面地址、密钥文件、路由缓存目录）、`config/trpc_go.yaml`。
