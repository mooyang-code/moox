# moox-strategy

声明式策略 DSL：按已闭合的 K 线周期计算组合账户的完整目标权重，保存结果并异步发送给 Trade。

设计文档：[策略](../../docs/模块/策略.md)

## 构建与测试

```bash
./scripts/build/build.sh strategy
./scripts/build/build.sh strategy-cli
go test -count=1 ./modules/strategy/...
```

## 配置

`config/app.yaml`（Trade 接线、EventBus）、`config/trpc_go.yaml`；schema 在 `schema/`。

Trade 的逻辑账户读取与授权复用进程级网关客户端，使用原生 PB 和空间元数据；只保留 `trade.timeout` 业务预算，删除专用目标、CA 和环境覆盖。Storage、Factor、Trade 共用部署签名身份与客户端生命周期。

StrategyMgr 仅监听 loopback 原生 tRPC 11430，控制台与 CLI 经主机网关访问；健康 HTTP 11431 保留。
