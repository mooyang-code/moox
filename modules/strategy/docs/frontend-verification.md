# Strategy 前端管理台验收

## 验收边界

前端必须直接展示当前生产事实：

- Strategy 不可变制品。
- StrategyRunner 配置、状态、健康和当前理论 FULL 目标。
- StrategyResult 已接受输出。
- LogicalAccount 自动化状态、readiness、成员和当前 LogicalAccountTarget。
- Trade 的 Order、Fill、Position 与 OperatorAction。

不得从浏览器推导另一套健康状态，也不得展示没有生产写入来源的策略内部状态、绩效
投影或本地交易余额投影。

## 自动化验证

```bash
(cd web && pnpm exec vitest run \
  src/api/strategy.test.ts \
  src/api/trade/trade.test.ts \
  src/views/strategy/components/strategy-operation-panel.test.ts)
(cd web && pnpm exec vue-tsc --noEmit)
(cd web && pnpm exec playwright test tests/strategy-console.spec.ts)
(cd web && pnpm build:prod)
(cd modules/strategy && go test -count=1 ./test)
```

## 必测场景

- Strategy、Runner、StrategyResult 与 targets 查询字段匹配 Proto。
- `InstrumentTarget.quantity` 显示为绝对目标持仓量。
- `hold` 保留当前目标；空 `rebalance` 显示为全部目标归零。
- PAUSED 时新 LogicalAccountTarget 显示“已保存，尚未执行”。
- 人工下单必须提供 `action_id` 和 reason，并提示暂停整个 LogicalAccount。
- 逐账户清仓展示每个成员的剩余持仓或错误。
- Resume 明确提示可能按已保存目标重新开仓。
- 观察型 Runner 不显示执行操作。
- Strategy 或 Trade 服务不可达时显示独立错误，而不是空数据。

详细交互见[策略前端管理台设计](../../../docs/策略前端管理台设计.md)。
