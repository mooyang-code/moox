# Strategy 前端管理台验收记录

## 页面边界

- 管理台不提供 Python 源码输入、在线编辑、Git/CI 或版本回滚。
- 管理台只展示版本、源码 hash、运行状态、决策、目标/持仓偏差和分来源绩效。
- 前端只能通过 Admin Gateway 调用 StrategyMgr，不能直接访问 SQLite、Trade 或 Storage。

## 自动化验证

```bash
cd modules/strategy && GOWORK=off go test ./... -count=1
cd modules/strategy && GOWORK=off go test -race ./... -count=1
cd web && pnpm exec vue-tsc --noEmit
cd web && pnpm test:unit
cd web && pnpm check:menu
cd web && pnpm exec playwright test tests/strategy-console.spec.ts tests/strategy-console-performance.spec.ts
cd web && pnpm build:prod
pnpm docs:build
```

E2E mock 场景覆盖：

1. 登录后进入策略运行概览。
2. 展示运行策略、版本和 Paper 健康状态。
3. 页面不出现源码编辑和版本回滚入口。
4. API 错误、空列表、stale 和 partial 状态使用独立文案。

## 数据验收

- 绩效必须带 `performance_source`，Backtest/Observe/Paper/Live 不合并。
- 没有数据返回 `insufficient_data`，不能用数值 0 代替。
- 查询响应带 `data_revision`、`as_of` 和数据新鲜度。
- 绩效点、日汇总、运行指标和操作审计使用唯一键幂等写入。
- Space 过滤在服务端完成；前端筛选不能替代权限校验。

## 性能门槛

正式压测数据集应包含至少 10,000 条运行记录和 365 天 daily performance。目标是列表第一页接口 p95 小于 500ms、详情首屏接口 p95 小于 800ms；前端不得一次加载全部历史曲线。

## 当前限制

Strategy 首版的真实 Storage PIT、Trade live 对账、完整 Performance 聚合和组合持仓快照仍需要基础设施联调。没有真实账本或持仓快照时，页面必须显示 `insufficient_data`、`stale` 或“暂无组合快照”，不能推测实盘表现。
