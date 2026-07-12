# Strategy Frontend Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 `web` 管理台增加只读策略运行监控、目标/持仓对比、分来源绩效和受控运行操作；不提供策略源码输入、Git/CI 集成或版本回滚。

**Architecture:** Strategy 服务继续保存业务事实，前端只通过 Admin Gateway 调用 StrategyMgr 查询和控制接口。现有策略定义、Binding、运行、目标和执行表继续作为事实源；新增运行指标、健康摘要、绩效曲线/日汇总和操作审计表作为可重建查询模型。前端通过 Pinia 管理查询状态，详情页组合运行状态、目标持仓和绩效数据。

**Tech Stack:** Vue 3、TypeScript、Pinia、Arco Design、VChart、Axios、tRPC-Go、Go 1.24、SQLite/GORM。

---

## 约束与非目标

- 管理台不接收 `strategy.py`、`strategy.yaml` 或任意 Python 源码输入。
- 管理台不集成 Git、CI，也不展示源码编辑器；只展示版本、源码 hash 和发布信息。
- 管理台不提供已发布策略版本回滚；更换版本由后端导入新版本并配置新的 Binding 完成。
- 前端不直接访问 Strategy SQLite、Trade 数据库或 Storage 数据库。
- 前端不自行计算实盘收益；绩效由后端按照 `performance_source` 计算并返回。
- 不把回测、Observe、Paper、Live 四类绩效合并到一条默认曲线。
- Live 模式账户密钥、订单重试和未知订单处理不在本计划内。

## 交付边界

```text
Phase 1 只读运行监控
  -> Phase 2 目标/持仓与绩效
  -> Phase 3 暂停/恢复和 Observe/Paper 操作
```

每个阶段都能独立运行。数据尚不可用时显示 `insufficient_data` 或 `stale`，不伪造 0 值。

## 目标文件图

```text
modules/strategy/
├── proto/strategy.proto
├── schema/strategy.sql
├── internal/domain/frontend.go
├── internal/store/frontend_queries.go
├── internal/rpc/frontend_service.go
└── test/frontend_e2e_test.go

web/src/
├── api/strategy.ts
├── api/strategy-types.ts
├── store/modules/strategy.ts
├── views/strategy/overview/index.vue
├── views/strategy/running/index.vue
├── views/strategy/detail/index.vue
├── views/strategy/performance/index.vue
├── views/strategy/components/
│   ├── strategy-status-badge.vue
│   ├── strategy-health-panel.vue
│   ├── strategy-target-table.vue
│   ├── strategy-run-timeline.vue
│   └── strategy-performance-chart.vue
└── tests/strategy-console.spec.ts
```

## Task 1: 固定查询 DTO、状态和绩效枚举

**Files:**
- Create: `modules/strategy/internal/domain/frontend.go`
- Create: `modules/strategy/internal/domain/frontend_test.go`
- Modify: `modules/strategy/proto/strategy.proto`
- Modify: `modules/strategy/proto/strategygen/strategy.pb.go`
- Modify: `modules/strategy/proto/strategygen/strategy.trpc.go`

- [ ] **Step 1: 写来源隔离测试**

```go
func TestPerformanceSourceNeverMixesModes(t *testing.T) {
    p := domain.PerformancePoint{Source: "paper", BindingID: "b1"}
    if err := p.Validate(); err != nil { t.Fatal(err) }
    p.Source = "live+paper"
    if err := p.Validate(); err == nil { t.Fatal("mixed source must be rejected") }
}
```

- [ ] **Step 2: 定义查询模型**

`RunningStrategySummary` 包含策略 ID、版本、Binding、Space、View、频率、模式、状态、源码 hash、最近运行、最近数据 revision、错误和延迟。`PerformancePoint` 包含 Binding、来源、时间、NAV、收益、回撤、敞口、换手、费用、data revision 和计算时间。金额、权重、收益和敞口使用 decimal string。

- [ ] **Step 3: 扩展 StrategyMgr protobuf**

增加 `ListRunningStrategies`、`GetStrategyOverview`、`ListStrategyRuns`、`GetStrategyRun`、`ListStrategyTargets`、`GetStrategyStateSummary`、`GetStrategyHealth`、`GetStrategyPerformance`、`PauseBinding`、`ResumeBinding`、`SetExecutionMode`。列表响应统一带 `items/total/page/page_size`；绩效响应必须带 `performance_source/data_revision/as_of`。

- [ ] **Step 4: 重新生成并测试**

Run: `cd modules/strategy/proto && make && go test ./... -count=1`

Expected: 生成代码包含全部方法，资金字段没有 float 类型。

## Task 2: 增加查询辅助表和索引

**Files:**
- Modify: `modules/strategy/schema/strategy.sql`
- Modify: `modules/strategy/schema/schema_test.go`
- Create: `modules/strategy/schema/frontend_schema_test.go`
- Modify: `modules/strategy/internal/domain/frontend.go`

- [ ] **Step 1: 写 schema 对象测试**

断言 `t_strategy_run_metrics`、`t_strategy_binding_health`、`t_strategy_performance_points`、`t_strategy_performance_daily`、`t_strategy_operation_audits` 存在，重复执行 schema 不报错。

- [ ] **Step 2: 增加运行指标和健康表**

`t_strategy_run_metrics` 以 `c_run_id` 为主键，保存 queue/snapshot/compute/validate/total duration、input rows、output targets、worker id。`t_strategy_binding_health` 以 `c_binding_id` 为主键，保存 status、mode、last run/success/error、data revision/cutoff、worker status、outbox lag 和 observed_at。健康表是可重建缓存，不是新的状态事实源。

- [ ] **Step 3: 增加绩效表**

`t_strategy_performance_points` 主键为 `(c_binding_id,c_performance_source,c_point_time)`，保存 NAV、累计收益、回撤、敞口、换手、费用、data revision 和计算时间。`t_strategy_performance_daily` 主键为 `(c_binding_id,c_performance_source,c_trade_date)`，保存日初末净值、收益、回撤、换手、费用、胜负次数和样本数。

- [ ] **Step 4: 增加审计表**

`t_strategy_operation_audits` 以 `c_operation_id` 为主键，追加保存 operator、action、binding、old/new value、reason、request id 和时间；不提供删除审计记录的接口。

- [ ] **Step 5: 执行 schema 测试**

Run: `cd modules/strategy && GOWORK=off go test ./schema -count=1`

Expected: 空 SQLite 数据库可创建全部表，唯一键阻止重复绩效点。

## Task 3: 实现查询 repository

**Files:**
- Create: `modules/strategy/internal/store/frontend_queries.go`
- Create: `modules/strategy/internal/store/frontend_queries_test.go`
- Modify: `modules/strategy/internal/store/repository.go`

- [ ] **Step 1: 写真实 SQLite 测试**

覆盖 `ListRunningStrategies` 分页和 Space 过滤、绩效来源隔离、stale 判定、操作幂等键四种场景。

- [ ] **Step 2: 实现运行、目标、状态查询**

实现 `ListRunningStrategies`、`ListRuns`、`GetRun`、`ListRunTargets`、`GetStateSummary`。SQL 层完成 Space 过滤和分页，禁止一次返回完整历史。

- [ ] **Step 3: 实现健康和绩效查询**

`GetHealth` 根据成功时间、data cutoff、Outbox lag 和 Trade unknown 计算 running/paused/waiting_data/failed/unknown/stale。`GetPerformance` 按 source 分组，优先读取 daily，曲线请求再读取 points，不跨来源合并。

- [ ] **Step 4: 执行 race 测试**

Run: `cd modules/strategy && GOWORK=off go test ./internal/store -race -count=1`

Expected: 分页、来源隔离、幂等和 stale 判定全部通过。

## Task 4: 实现查询 RPC、操作 RPC 和权限边界

**Files:**
- Create: `modules/strategy/internal/rpc/frontend_service.go`
- Create: `modules/strategy/internal/rpc/frontend_service_test.go`
- Modify: `modules/strategy/internal/rpc/service.go`
- Modify: `modules/admin/internal/gateway/authorize.go`
- Modify: `modules/admin/internal/gateway/forward.go`
- Modify: `modules/admin/config/gateway.yaml`

- [ ] **Step 1: 写 RPC 测试**

覆盖查询分页、绩效 source、暂停/恢复审计、viewer 权限拒绝、Live 模式权限拒绝和 Space 越权拒绝。

- [ ] **Step 2: 实现只读查询 RPC**

RPC 只做参数校验、权限检查、分页和 DTO 转换，不计算 NAV、不读取源码、不直接访问数据库表外部接口。

- [ ] **Step 3: 实现受控运行操作**

`PauseBinding`、`ResumeBinding`、`SetExecutionMode` 使用请求幂等键，在同一事务更新 Binding/ExecutionBinding 并写审计。明确不增加 `RollbackVersion` 或 `EditSource` RPC。

- [ ] **Step 4: 接入 Admin Gateway**

增加 StrategyMgr 转发和权限映射；请求带当前 Space，服务端再次校验 Binding 所属 Space，不能只依赖前端筛选。

- [ ] **Step 5: 执行测试**

Run: `cd modules/strategy && GOWORK=off go test ./internal/rpc ./test -race -count=1`

Expected: 查询、暂停/恢复、模式切换、权限拒绝和审计写入通过。

## Task 5: 实现绩效聚合写入器

**Files:**
- Create: `modules/strategy/internal/performance/types.go`
- Create: `modules/strategy/internal/performance/aggregator.go`
- Create: `modules/strategy/internal/performance/aggregator_test.go`
- Modify: `modules/strategy/internal/action/service.go`
- Modify: `modules/strategy/internal/execution/paper.go`
- Modify: `modules/strategy/internal/backtest/backtest.go`

- [ ] **Step 1: 写四类来源测试**

覆盖 Observe 使用理论目标价格、Paper 使用虚拟成交/费用、Live 只使用 Trade 事实、Backtest 与 Paper 不共享绩效行、重复点幂等重算。

- [ ] **Step 2: 实现统一输入**

```go
type PerformanceInput struct {
    BindingID, Source, DataRevision string
    PointTime time.Time
    NAV, Return, Drawdown, GrossExposure, NetExposure, Turnover, Fees string
}
```

Observe、Paper、Backtest、Live 只写自己的 source；计算失败保留旧数据并记录错误，不写入 0。

- [ ] **Step 3: 接入运行和执行端口**

策略成功运行写入运行成功率和目标点；Paper/Backtest/Trade 结果到达后写入 NAV、费用、回撤和换手；Observe 不创建 Trade 请求。

- [ ] **Step 4: 执行测试**

Run: `cd modules/strategy && GOWORK=off go test ./internal/performance ./internal/action ./internal/execution ./internal/backtest -race -count=1`

Expected: 四类 source 可独立查询，重复事件不会重复累计收益或费用。

## Task 6: 建立前端 API 类型和 Pinia store

**Files:**
- Create: `web/src/api/strategy.ts`
- Create: `web/src/api/strategy-types.ts`
- Create: `web/src/store/modules/strategy.ts`
- Create: `web/src/api/strategy.test.ts`
- Modify: `web/package.json`
- Modify: `web/pnpm-lock.yaml`
- Modify: `web/vite.config.ts`

- [ ] **Step 1: 写来源隔离和 decimal 展示测试**

```ts
it('keeps performance sources separate', () => {
  const result = normalizePerformance({ groups: [{ source: 'paper', points: [] }, { source: 'live', points: [] }] });
  expect(result.groups.map((x) => x.source)).toEqual(['paper', 'live']);
});
```

测试依赖固定为 `vitest`、`@vue/test-utils` 和 `jsdom`，通过 Vite 配置复用现有 Vue/TS 别名。

- [ ] **Step 2: 定义前端类型**

类型必须包含 `source_hash`、`data_revision`、`state_revision`、`performance_source`、`as_of` 和 `stale`；收益、净值、费用使用 `string`，展示层再转换为数值。

- [ ] **Step 3: 封装 `callControl` API**

固定函数：`listRunningStrategies`、`getStrategyOverview`、`listStrategyRuns`、`listStrategyTargets`、`getStrategyPerformance`、`pauseBinding`、`resumeBinding`、`setExecutionMode`。所有列表函数显式传 page/page_size/from/to。

- [ ] **Step 4: 实现 Pinia store**

Store 保存筛选条件、列表、详情、绩效条件和轮询状态。列表轮询 15 秒，详情运行状态轮询 5 秒；组件卸载清理 timer。请求失败保留上一份数据并设置 `error`，不清空页面。

- [ ] **Step 5: 执行类型检查**

Run: `cd web && pnpm add -D vitest @vue/test-utils jsdom`

Run: `cd web && pnpm exec vue-tsc --noEmit`

Expected: API、store 和现有页面类型检查通过。

## Task 7: 实现运行概览和运行策略列表

**Files:**
- Create: `web/src/views/strategy/overview/index.vue`
- Create: `web/src/views/strategy/running/index.vue`
- Create: `web/src/views/strategy/components/strategy-status-badge.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: 写菜单契约测试**

Run: `cd web && pnpm check:menu`

Expected: Strategy 菜单只出现运行概览和运行策略，不出现源码编辑、Git/CI 或版本回滚入口。

- [ ] **Step 2: 实现概览摘要卡**

显示运行中、正常、延迟、异常和 Observe/Paper/Live 数量；点击卡片带筛选条件跳转列表。

- [ ] **Step 3: 实现运行策略表**

支持策略、Space、模式、状态和时间筛选，默认异常优先；列包含版本/hash、Binding、最近运行、数据 revision、目标偏差和表现摘要。

- [ ] **Step 4: 实现状态徽标和异常态**

统一 `running/paused/waiting_data/failed/unknown/stale/partial` 文案和颜色；stale 显示最后更新时间和刷新按钮。

- [ ] **Step 5: 构建验证**

Run: `cd web && pnpm build:dev`

Expected: 页面可构建，无 TypeScript 或路由错误。

## Task 8: 实现策略详情、决策和目标持仓页面

**Files:**
- Create: `web/src/views/strategy/detail/index.vue`
- Create: `web/src/views/strategy/components/strategy-health-panel.vue`
- Create: `web/src/views/strategy/components/strategy-run-timeline.vue`
- Create: `web/src/views/strategy/components/strategy-target-table.vue`
- Create: `web/src/views/strategy/components/strategy-state-summary.vue`

- [ ] **Step 1: 写详情数据拼装测试**

断言理论目标、组合目标和实际持仓使用不同的来源标签，不把理论目标显示成实际持仓。

- [ ] **Step 2: 实现基本信息和健康面板**

展示策略 ID、版本、API、源码 hash、Binding、模式、worker、最近运行和数据新鲜度；不请求或渲染源码内容。

- [ ] **Step 3: 实现运行时间线和决策记录**

展示快照、Python、校验、状态提交、目标生成和执行状态的耗时/错误；按时间分页显示 run ID、data revision、state revision、action、目标数量和执行关联 ID。

- [ ] **Step 4: 实现目标/组合/实际持仓对比**

用分组表格展示理论目标、组合目标、实际持仓、偏差、来源时间和 revision，禁止合并成一列“当前仓位”。

- [ ] **Step 5: 实现状态摘要**

展示 state revision、大小、最近运行 ID、快照 hash 和数据截止时间；不提供删除状态或手工编辑 JSON 的按钮。

- [ ] **Step 6: 执行组件测试**

Run: `cd web && pnpm exec vitest run src/views/strategy src/api/strategy.test.ts`

Expected: 详情处理 loading、empty、stale、partial、error 五种状态。

## Task 9: 实现策略表现页面

**Files:**
- Create: `web/src/views/strategy/performance/index.vue`
- Create: `web/src/views/strategy/components/strategy-performance-chart.vue`
- Create: `web/src/views/strategy/components/strategy-performance-summary.vue`
- Create: `web/src/views/strategy/performance/performance-format.ts`
- Create: `web/src/views/strategy/performance/performance-format.test.ts`

- [ ] **Step 1: 写格式化测试**

```ts
it('formats decimal strings without losing display precision', () => {
  expect(formatPercent('0.0342')).toBe('3.42%');
});
it('renders insufficient data instead of zero', () => {
  expect(formatMetric({ status: 'insufficient_data', value: null })).toBe('数据不足');
});
```

- [ ] **Step 2: 实现 source 选择器和摘要指标**

`performance_source` 使用分段控件；切换来源重新请求，不在浏览器内合并曲线。展示 NAV、累计收益、区间收益、最大回撤、换手、费用、波动率、胜率和 as-of 时间。

- [ ] **Step 3: 实现 VChart 曲线**

净值与回撤使用双图，目标偏差单独一图；大数据量按接口 interval 渲染。tooltip 显示 point time、source 和 data revision。

- [ ] **Step 4: 处理 stale/partial/error 并测试**

旧数据显示最后更新时间；部分账户显示分账户状态；查询失败保留旧曲线，不渲染空白 0。

Run: `cd web && pnpm exec vitest run src/views/strategy/performance && pnpm build:dev`

Expected: 四种 source 独立显示，空数据和大回撤布局不重叠。

## Task 10: 实现暂停/恢复和模式切换

**Files:**
- Create: `web/src/views/strategy/components/strategy-operation-panel.vue`
- Create: `web/src/views/strategy/components/strategy-operation-panel.test.ts`
- Modify: `web/src/store/modules/strategy.ts`

- [ ] **Step 1: 写高风险确认测试**

断言 live Binding 暂停必须填写 reason；viewer 不显示操作入口；risk 权限才可切换 Live。

- [ ] **Step 2: 实现暂停/恢复和 Observe/Paper 切换**

确认框显示 Binding、模式、账户和影响范围；操作成功后刷新详情和审计记录。页面不提供版本回滚、源码编辑和未知 Trade 重试按钮。

- [ ] **Step 3: 处理重复提交和权限**

每次操作生成 `operation_id`，提交中禁用按钮；重复响应显示已完成结果，不重复写审计。

- [ ] **Step 4: 执行测试**

Run: `cd web && pnpm exec vitest run src/views/strategy/components/strategy-operation-panel.test.ts`

Expected: viewer/operator/risk 权限与后端结果一致。

## Task 11: 完成前端根目录 E2E 和后端端到端测试

**Files:**
- Create: `web/tests/strategy-console.spec.ts`
- Create: `web/playwright.config.ts`
- Modify: `web/package.json`
- Modify: `web/pnpm-lock.yaml`
- Create: `modules/strategy/test/frontend_e2e_test.go`
- Create: `modules/strategy/test/testdata/strategy_console.json`

- [ ] **Step 1: 写后端真实 SQLite + RPC E2E**

插入一个策略、两个 Binding、三条 run、目标、paper/live 两类绩效和一条 stale health；调用 RPC 断言分页、来源隔离、目标来源和审计写入。

- [ ] **Step 2: 写 Playwright 页面 E2E**

使用 mock Admin Gateway，不依赖真实账户：登录 -> 运行概览 -> 筛选 paper -> 打开详情 -> 查看决策 -> 切换 performance source -> 查看目标/持仓偏差 -> 暂停 Binding -> 验证审计提示。

- [ ] **Step 3: 验证页面状态**

分别让 mock 返回延迟、空列表、HTTP 错误和部分账户失败，断言页面显示 loading/stale/empty/error/partial，不把缺失数据显示成 0。

- [ ] **Step 4: 执行 E2E**

Run: `cd web && pnpm add -D @playwright/test && pnpm exec playwright install chromium`

Run: `cd modules/strategy && GOWORK=off go test ./test -count=1 -v`

Run: `cd web && pnpm exec playwright test tests/strategy-console.spec.ts`

Expected: 后端和前端 E2E 全部通过，页面不出现源码编辑、Git/CI 或版本回滚入口。

## Task 12: 性能、权限和发布验收

**Files:**
- Create: `web/tests/strategy-console-performance.spec.ts`
- Create: `modules/strategy/docs/frontend-verification.md`
- Modify: `docs/策略前端管理台设计.md`

- [ ] **Step 1: 验证查询性能**

准备 10,000 条运行记录和 365 天 daily performance；断言列表第一页接口 p95 小于 500ms，详情首屏接口 p95 小于 800ms；前端不一次加载全部历史。

- [ ] **Step 2: 验证轮询和资源释放**

打开详情、切换路由、再次打开详情，断言 timer 只有一个；连续 10 次刷新不产生重复请求或未处理 Promise。

- [ ] **Step 3: 验证权限和脱敏**

断言 API 和 DOM 不出现源码内容、账户密钥和交易凭证；viewer 无操作入口；Space 越权请求被后端拒绝。

- [ ] **Step 4: 运行完整验证**

```bash
cd modules/strategy && GOWORK=off go test -race ./... -count=1
cd web && pnpm exec vue-tsc --noEmit
cd web && pnpm exec vitest run
cd web && pnpm exec playwright test tests/strategy-console.spec.ts
cd web && pnpm build:prod
npm run docs:build
git diff --check
```

Expected: Go、TypeScript、组件、Playwright、生产构建和文档构建全部通过。

- [ ] **Step 5: 更新验收手册**

`modules/strategy/docs/frontend-verification.md` 记录数据规模、接口 p95、页面首屏时间、轮询间隔、权限结果、stale/error 验证和已知限制；明确当前不支持源码编辑、Git/CI 和版本回滚。

## 最终验收清单

- 管理台能列出当前运行策略、版本、Binding、模式和健康状态。
- 详情能展示最近运行、输入 revision、状态 revision、目标、组合目标、实际持仓和偏差。
- Backtest、Observe、Paper、Live 绩效始终按 `performance_source` 分组。
- 没有数据显示 `insufficient_data`，数据过期显示 `stale`，不会用 0 代替。
- 运行指标、绩效点、日汇总和操作审计具备唯一键和幂等写入。
- Viewer、Operator、Risk、Admin 权限在后端和前端同时生效。
- 暂停/恢复和模式切换有原因、请求 ID 和审计记录。
- 页面不出现源码输入、Git/CI 集成或版本回滚入口。
- 前端只通过 Admin Gateway 访问策略接口。
- 10,000 条运行记录和 365 天绩效数据下，分页和曲线查询满足性能门槛。

## 明确不在本计划内

- 策略源码编辑器、在线上传和依赖安装。
- Git/CI、构建流水线和代码评审系统。
- 策略版本回滚。
- Live 交易重试、订单取消和账户密钥管理。
- 完整 Trade 对账服务实现；本计划只定义前端查询契约和接入边界。
