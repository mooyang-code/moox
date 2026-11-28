# moox-strategy

声明式策略 DSL：按已闭合的 K 线周期计算组合账户的完整目标权重，保存结果并异步发送给 Trade。
追赶与过期周期在输入就绪且成功求值时只推进规则状态，记为 `skipped(catchup / expired)`，不发送目标或下一期因子参数。

设计文档：[策略](../../docs/模块/策略.md)

## 构建与测试

```bash
./scripts/build/build.sh strategy
go test -count=1 ./modules/strategy/...
```

## 配置

`config/app.yaml`（EventBus、`gateway_client`、访问 Trade、FactorMgr 与 Storage 的超时、结果与回放的保留期、历史回放参数）、`config/trpc_go.yaml`；schema 在 `schema/`。

StrategyMgr 使用 loopback 原生 tRPC 11430，经主机网关调用；健康端口 11431。访问 Storage、FactorMgr 与 Trade 统一经本机主机网关，以 `strategy` 的身份签名。历史回放（`replay`）用同一套 DSL 逐根重算，因子值由因子引擎的 EngineControl 提供。
