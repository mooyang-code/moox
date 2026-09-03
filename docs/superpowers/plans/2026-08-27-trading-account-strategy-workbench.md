# 交易账户与策略账户工作台合并实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将交易账户和策略账户合并到 `/trading/accounts` 一个工作台中，保留真实账户、模拟账户和策略账户的完整能力，同时让页面层级、路由和中文命名与当前交易模块设计一致。

**Architecture:** 新增一个只负责页面级切换的交易账户工作台容器。工作台通过 `view=trading|strategy` 查询参数选择两个内嵌子视图：交易账户视图复用现有账户总览，策略账户视图复用现有逻辑账户能力。前端把 `LogicalAccount` 展示为“策略账户”，后端领域模型、API 和数据库字段继续使用 `LogicalAccount`，不新增兼容字段或重复模型。旧的 `/trading/logical-accounts` 仅保留为重定向入口。

**Tech Stack:** Vue 3 `<script setup>`、TypeScript、Vue Router、Arco Design、Vitest、Playwright、Vite、现有 Go embed 构建流程。

## 设计约束

1. 页面只保留一级工作台页签：“交易账户”“策略账户”；删除页面内重复的二级大标题。
2. “真实账户/模拟账户”是交易账户视图内的账户类型筛选，不再与工作台页签并列。
3. 一个启用中的 `StrategyRunner` 对应一个 `LogicalAccount`，前端称为一个策略账户；不新增 `strategy_id` 反向唯一约束，沿用现有启用 Runner 与逻辑账户的约束。
4. 一个策略账户可以关联多个实际交易账户，但关联账户必须满足现有执行模式、市场类型、结算资产和环境校验。
5. 实际账户选择继续使用“优先级 + 容量不足时切换下一账户”的既有执行语义；不引入资金权重、比例分配或额外调度模型。
6. 保留策略账户暂停、恢复、平仓、手动下单、成员增删和目标查询能力；成员变更仍要求策略账户处于暂停状态。
7. 所有用户可见文案使用中文：“策略账户”“实际交易账户”等；后端字段和内部函数名不为文案重命名。

## 文件边界

**新增：**

- `web/src/views/trading/account-workbench/index.vue`
- `web/src/views/trading/account-workbench/account-workbench.test.ts`
- `web/src/views/trading/logical-accounts/logical-account-contract.test.ts`

**修改：**

- `web/src/views/trading/account-overview/account-overview.vue`
- `web/src/views/trading/account-overview/account-overview.test.ts`
- `web/src/views/trading/logical-accounts/index.vue`
- `web/src/router/route.ts`
- `web/src/api/modules/system/static-menu.ts`
- `web/src/lang/modules/zhCN.ts`
- `web/src/lang/modules/enUS.ts`
- `web/tests/trading-accounts.spec.ts`
- `web/tests/trading-workbench.spec.ts`
- `web/tests/strategy-console.spec.ts`
- `web/tests/page-layout-standard-contract.test.ts`

**明确不修改：** `modules/trade/proto/trade_service.proto`、`modules/trade/schema/logical_account.sql`、`modules/strategy/schema/strategy.sql` 以及交易执行器和持仓/订单业务逻辑。本次改动是页面组织和路由收敛，不改变后端账户模型与资金分配算法。

## 实施步骤

### 1. 先建立工作台路由和契约测试

- [ ] 在 `web/src/views/trading/account-workbench/account-workbench.test.ts` 增加失败优先的源码契约，覆盖工作台必须包含“交易账户/策略账户”两个页签、通过 `view` 查询参数切换、默认进入交易账户，以及只保留一组页面级页签。
- [ ] 为 `/trading/logical-accounts` 增加路由契约，明确旧入口只能重定向到 `/trading/accounts?view=strategy`，并保留原请求中的 `logical_account_id` 等有效查询参数。
- [ ] 在 `web/src/router/route.ts` 新增工作台路由组件，定义 `WorkbenchView = "trading" | "strategy"` 和查询参数解析函数。未知或缺失的 `view` 统一回落为 `trading`。
- [ ] 将原 `/trading/logical-accounts` 路由改为重定向，不再直接挂载独立页面；重定向时不丢失 `logical_account_id`，以支持从账户列表进入策略账户详情。
- [ ] 运行针对性的 Vitest，确认新增契约在工作台实现前按预期失败，然后提交：`test(trade): define unified account workbench contract`。

### 2. 实现 `/trading/accounts` 工作台容器

- [ ] 创建 `web/src/views/trading/account-workbench/index.vue`，采用 `/factor/results` 的页面组织方式：`moox-page` 外层、`moox-inner` 内容容器、一个 `PageTitleTabs` 页面级页签和可伸缩内容区。
- [ ] 工作台页签只维护 `trading`、`strategy` 两个值；切换时通过 Vue Router 同步 `view` 查询参数，保留无冲突的其它查询参数，并在切换到交易账户时清理 `logical_account_id`、切换到策略账户时清理交易账户的 `mode` 查询参数。
- [ ] 使用 `v-if` 挂载当前子视图，确保未选中的账户列表不会继续轮询、重复请求或保留未显示的交互状态。
- [ ] 以 `embedded` 属性挂载现有账户总览和逻辑账户组件；工作台负责页面级背景、间距、溢出和高度，子视图负责各自的业务操作。
- [ ] 增加 `:deep(.moox-page)` 样式重置，去掉内嵌子页面的重复 padding、背景、边框和阴影；保持 `display:flex`、`min-height:0`、`overflow:hidden`，避免表格和抽屉布局被压缩。
- [ ] 将 `/trading/accounts` 路由组件指向工作台，更新工作台契约测试并运行：`pnpm --dir web exec vitest run src/views/trading/account-workbench/account-workbench.test.ts`。
- [ ] 提交：`feat(trade): add unified account workbench`。

### 3. 将交易账户总览改为可内嵌子视图

- [ ] 在 `account-overview.vue` 增加 `embedded?: boolean` 属性，删除页面级 `<h2>交易账户</h2>`，保留刷新、创建账户、表格、详情抽屉和配置弹窗等业务操作。
- [ ] 将现有“全部/真实账户/模拟账户”页签改成账户类型区域内的紧凑 `a-radio-group type="button"`，旁边显示中文标签“账户类型”；继续用 `mode` 查询参数驱动筛选，并保持向 API 映射为 `execution_mode` 的既有语义：全部不传、真实账户为 `1`、模拟账户为 `2`。
- [ ] 将账户列表中进入逻辑账户的导航改为 `/trading/accounts?view=strategy&logical_account_id=...`，不再生成新的独立页面地址。
- [ ] 在 `account-overview.test.ts` 更新标题、筛选控件和新导航契约；补充真实/模拟/全部三种筛选对应 API 参数的断言（真实账户为 `execution_mode=2`，模拟账户为 `execution_mode=1`）。
- [ ] 内嵌模式只移除外层页面样式，不改变表格列宽、排序、分页、账户详情和创建 Paper 模拟账户流程。
- [ ] 运行账户总览相关 Vitest，提交：`refactor(trade): embed trading accounts in workbench`。

### 4. 将逻辑账户能力改为策略账户子视图

- [ ] 在 `web/src/views/trading/logical-accounts/index.vue` 增加 `embedded?: boolean` 属性，删除页面级 `<h2>逻辑账户</h2>` 和重复说明文字，保留刷新、新建、筛选、汇总卡片、表格和所有抽屉操作。
- [ ] 仅替换用户可见文案：`逻辑账户`→`策略账户`、`新建逻辑账户`→`新建策略账户`、`逻辑账户详情`→`策略账户详情`、`物理交易账户`→`实际交易账户`；变量、API、类型和后端字段名保持 `LogicalAccount`。
- [ ] 在成员区域补充一条简洁中文说明：“多个实际账户按优先级执行，当前账户容量不足时自动切换下一账户。”不增加资金权重输入、比例分配或新的调度配置。
- [ ] 确保暂停/恢复、平仓、手动下单、成员新增/移除、目标查询和权益曲线请求链路不变；成员变更前继续由后端校验暂停状态。
- [ ] 新增 `logical-account-contract.test.ts`，断言页面不再显示旧的大标题，显示“策略账户/实际交易账户”，并保留 pause、resume、flatten、manual order、member add/remove 等 API 调用。
- [ ] 运行策略账户契约及相关 Vitest，提交：`refactor(trade): embed strategy accounts in workbench`。

### 5. 收敛主导航和语言资源

- [ ] 在 `web/src/api/modules/system/static-menu.ts` 移除独立的 `0504` `/trading/logical-accounts` 菜单项，保留 `/trading/accounts` 作为交易模块唯一主入口；旧 URL 仍由路由重定向处理。
- [ ] 在 `web/src/lang/modules/zhCN.ts` 将交易模块入口和旧 key 的展示文案统一为“账户总览/策略账户”；在 `enUS.ts` 同步为 `Account Overview/Strategy Accounts`，避免语言 key 回退到过时名称。
- [ ] 在 `web/tests/page-layout-standard-contract.test.ts` 增加菜单唯一性和页面级页签契约，防止后续重新出现独立逻辑账户入口或重复标题。
- [ ] 运行静态菜单、语言和布局契约测试，提交：`refactor(trade): remove standalone strategy account menu`。

### 6. 更新端到端验证

- [ ] 更新 `web/tests/trading-accounts.spec.ts`：默认打开 `/trading/accounts` 后断言页面级“交易账户”页签，切换账户类型单选控件并验证 API 的 `execution_mode` 参数；切换到“策略账户”后只请求策略账户列表，再切回交易账户。
- [ ] 更新 `web/tests/trading-workbench.spec.ts` 和 `web/tests/strategy-console.spec.ts`：将旧 `/trading/logical-accounts` 入口改为工作台策略账户视图，验证新建、详情、暂停/恢复、平仓、手动下单、成员操作等现有流程仍可用。
- [ ] 增加旧链接兼容性测试：访问 `/trading/logical-accounts?logical_account_id=abc` 后最终地址为 `/trading/accounts?view=strategy&logical_account_id=abc`，并显示对应策略账户详情。
- [ ] 增加交易账户列表跳转策略账户的测试；持仓和订单页面链接保持不变。
- [ ] 运行：
  ```bash
  pnpm --dir web exec playwright test tests/trading-accounts.spec.ts tests/trading-workbench.spec.ts tests/strategy-console.spec.ts --project=chromium
  ```
- [ ] 提交：`test(trade): cover unified account workbench flows`。

### 7. 完成检查、构建和发布准备

- [ ] 运行工作台、账户总览、策略账户和页面布局契约的定向 Vitest：
  ```bash
  pnpm --dir web exec vitest run \
    src/views/trading/account-workbench/account-workbench.test.ts \
    src/views/trading/account-overview/account-overview.test.ts \
    src/views/trading/logical-accounts/logical-account-contract.test.ts \
    tests/page-layout-standard-contract.test.ts
  ```
- [ ] 运行前端完整测试：`pnpm --dir web test`。
- [ ] 运行类型检查：`pnpm --dir web exec vue-tsc --noEmit`。
- [ ] 对所有本次修改的 Vue、TypeScript 和语言文件运行格式化检查，并执行 `git diff --check`。
- [ ] 运行生产构建：`pnpm --dir web run build:prod`。
- [ ] 按现有发布流程重新生成 Go embed 静态资源并编译：
  ```bash
  make -C web-host statik
  TARGET_GOOS=linux TARGET_GOARCH=amd64 VERSION=$(git rev-parse HEAD) ./scripts/build/build.sh web-host
  ```
  只提交确实由本次前端变更生成且仓库已跟踪的 embed 文件，不提交本地 `dist` 或二进制产物。
- [ ] 检查 `git status --short --branch` 和最近提交，确保只包含本计划相关文件，不触碰工作区中已有的其它修改。

## 完成标准

- `/trading/accounts` 是交易模块唯一主导航入口，页面级只显示“交易账户/策略账户”两项。
- 交易账户视图可区分真实账户和模拟账户，策略账户视图可以管理一个策略对应的逻辑账户及其多个实际交易账户。
- 旧 `/trading/logical-accounts` 链接可重定向到策略账户视图，但不再作为独立页面或菜单项存在。
- 现有账户创建、详情、策略账户执行控制、成员管理、持仓和订单跳转能力不回归。
- 没有新增资金权重或比例分配模型；实际账户执行仍遵循优先级和容量回退。
- 定向测试、完整前端测试、类型检查、格式检查和生产构建全部通过。
