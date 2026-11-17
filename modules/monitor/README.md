# moox-monitor

健康检查与告警：组件上报、服务指标与主机快照接入、数据集与行情新鲜度、企业微信、飞书、钉钉与通用 Webhook 通知。

设计文档：[监控](../../docs/模块/监控.md)

## 构建与测试

```bash
./scripts/build/build.sh monitor
go test -count=1 ./modules/monitor/...
```

## 配置

`config/app.yaml`、`config/trpc_go.yaml`；schema 在 `schema/monitor.sql`，服务启动时应用。

Collector 任务结果清单与 Storage 调用共用进程持有的网关客户端，按 Directory 选择 CollectMgr。清单路由不从配置或环境变量指定；只启用 K 线新鲜度检查时同样初始化并关闭网关客户端。

SysDeploy 的 `GetCatalog`、`ListHosts`、`ListDeployments`、`GetHostRoutes` 也复用上述进程客户端；按需 Doctor 调用使用同一客户端。Monitor 校验组件目录 SHA-256，分页读取主机与部署，并保存完整拓扑与网关观测。

MonitorMgr 管理接口使用 loopback 原生 tRPC 11410，经主机网关调用；健康端口 11409 只听 127.0.0.1。
