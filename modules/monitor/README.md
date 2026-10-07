# moox-monitor

健康检查与告警：进程探测、服务指标与主机快照接入、数据集与行情新鲜度、企业微信 / 飞书通知。

设计文档：[监控](../../docs/模块/监控.md)

## 构建与测试

```bash
./scripts/build/build.sh monitor
./scripts/build/build.sh monitor-cli
go test -count=1 ./modules/monitor/...
```

## 配置

`config/app.yaml`、`config/trpc_go.yaml`；schema 在 `schema/monitor.sql`。schema 变化时需要同时发布服务和 `moox-monitor-cli`。
