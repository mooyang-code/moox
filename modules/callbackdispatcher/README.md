# moox-callback-dispatcher

事件中心：接收 EventBus 里可以配置规则的事件，按用户配置的规则向用户自己的接口发通知，或者执行 MooX 自带的系统动作。管理台页面叫「事件中心」。

设计文档：[事件中心](../../docs/模块/事件中心.md)

## 构建与测试

```bash
./scripts/build/build.sh callback-dispatcher
go test -count=1 ./modules/callbackdispatcher/...
```

## 配置

`config/app.yaml`、`config/trpc_go.yaml`；数据库结构在 `schema/`。事件中心是独立部署单元（组件 ID `callback-dispatcher`），由 `[deployments]` 决定装在哪台主机，默认海外机。部署主机必须能访问到用户填写的通知地址。

- 服务 `CallbackDispatcherService` 仅在 loopback 11460 提供原生 tRPC，由主机网关转发；调用方是 `console` 和 `moox-cli`。它是全局服务，不分空间。
- 健康检查 11461。只有数据库不可用才算没就绪；EventBus 连不上只在详情里体现，服务每分钟重试连接，EventBus 晚于事件中心启动也能自动连上。
- EventBus 身份 `callback-dispatcher-eventbus`：每个可订阅事件一个固定的 durable（名字由 `packages/events` 的 `CallbackDispatcherConsumerName` 生成，凭据生成也用它），没有任何事件主题的发布权限。

## 结构

| 包 | 职责 |
| --- | --- |
| `internal/catalog` | 可订阅事件目录：界面名、可限定的范围字段、示例内容 |
| `internal/eventconsumer` | 连接 EventBus、每个事件一个消费者 |
| `internal/dispatch` | 接收（解码、匹配规则、回执去重、新建或合并执行记录）与工作线程（通知投递、系统动作推进、重试） |
| `internal/delivery` | 通知请求格式与 HMAC 签名、出站防护（`packages/netguard`）、结果分类、重试时间表 |
| `internal/action` | 系统动作的注册点与调用 Trade 的客户端接口；`action/delist` 是下架处理器 |
| `internal/tradegateway` | 经网关调用 Trade 的客户端 |
| `internal/store` | SQLite：规则、事件回执、执行记录、通知尝试、下架处理步骤 |
| `internal/rpc` | `CallbackDispatcherService` 的实现 |
| `internal/telemetry` | 关注项 `moox_callbackdispatcher_attention_active` 与错误计数 |

## 系统动作

第一版唯一的系统动作“卖出策略持有的下架币”：`internal/action/delist` 是处理器（发现即禁止买入、下架前 24 小时平仓、2 小时复查、下架后核对），`internal/tradegateway` 经网关（配置里的 `gateway_client`）调用 Trade 的下架名单与下架清仓接口。服务启动时注册处理器，网关打不开则启动失败。流程、重试、取消与并发的约定见设计文档第 5 节。
