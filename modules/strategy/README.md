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
