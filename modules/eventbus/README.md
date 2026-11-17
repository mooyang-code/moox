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

部署时 `broker.tls` 指向所在主机的主机证书（`secrets/host.crt`、`host.tls.key`）与 MooX CA 根证书，EventBus 没有自己的证书；
真实进程联调见 `scripts/test/gates/test-eventbus-host-cert-e2e.sh`。

EventBusMgr 管理接口使用 loopback 原生 tRPC 11420，经主机网关调用；健康检查是独立的 HTTP 端口 11419，只监听本机。
