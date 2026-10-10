# moox-eventbus

内嵌 NATS JetStream 的事件总线，声明 Stream 并提供只读管理接口 `EventBusMgr`。

设计文档：[事件总线](../../docs/模块/事件总线.md)

## 构建与测试

```bash
./scripts/build/build.sh eventbus
go test -count=1 ./modules/eventbus/...
./scripts/check/verify-event-contracts.sh
```

## 配置

`config/app.yaml`（broker、Stream、TLS 与鉴权）、`config/trpc_go.yaml`。

EventBusMgr 管理接口使用 loopback 原生 tRPC 11420，经共享网关调用；健康检查继续使用原有独立 HTTP 端点。
