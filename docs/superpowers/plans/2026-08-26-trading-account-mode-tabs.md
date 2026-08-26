# 交易账户真实与模拟账户页签实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在交易账户页通过 URL 同步的页签区分全部、真实账户和模拟账户，并将创建与详情流程统一为中文展示。

**Architecture:** 继续复用 `ListTradingAccounts` 的服务端分页和 `execution_mode` 过滤，不新增后端契约。账户页负责路由页签、创建默认模式和表格展示；账户模式/文案转换放在小型纯函数模块中，便于单测。

**Tech Stack:** Vue 3 `<script setup>`、Vue Router、Arco Design Vue、TypeScript、Vitest、Playwright。

---

### Task 1: 增加账户模式与中文展示转换

**Files:**
- Create: `web/src/views/trading/account-overview/account-mode.ts`
- Create: `web/src/views/trading/account-overview/account-mode.test.ts`
- Modify: `web/src/views/trading/account-overview/account-display.ts`
- Modify: `web/src/views/trading/account-overview/account-display.test.ts`

- [ ] **Step 1: 写账户模式转换测试**

覆盖以下契约：`all` 映射为无 `execution_mode`，`live` 映射为 `2`，`paper` 映射为 `1`；未知或缺失的 URL 值回退 `all`；账户类型显示为“真实账户/模拟账户”。

```ts
expect(accountModeFromQuery(undefined)).toBe("all");
expect(accountModeFromQuery("live")).toBe("live");
expect(accountModeFromQuery("paper")).toBe("paper");
expect(accountModeToExecutionMode("all")).toBeUndefined();
expect(accountModeToExecutionMode("live")).toBe(2);
expect(accountModeToExecutionMode("paper")).toBe(1);
```

- [ ] **Step 2: 运行测试确认新模块尚未实现**

运行：`pnpm --dir web exec vitest run src/views/trading/account-overview/account-mode.test.ts`

预期：因模块和导出不存在而失败。

- [ ] **Step 3: 实现纯函数与页签定义**

在 `account-mode.ts` 定义 `AccountModeTab = "all" | "live" | "paper"`、中文页签数组、`accountModeFromQuery`、`accountModeToExecutionMode` 和 `accountTypeLabel`。未知账户 `execution_mode` 显示“未知账户”，不把后端枚举泄漏到页面。

- [ ] **Step 4: 将账户环境和状态文案改为中文**

`accountEnvironmentView` 返回“模拟环境”“测试环境”“生产环境”；`accountStatusView` 的未知状态返回“未知”。保留其颜色和能力判断逻辑，不改变请求字段。

- [ ] **Step 5: 运行账户显示与模式测试**

运行：`pnpm --dir web exec vitest run src/views/trading/account-overview/account-mode.test.ts src/views/trading/account-overview/account-display.test.ts`

预期：全部通过。

### Task 2: 改造账户列表页签和服务端过滤

**Files:**
- Modify: `web/src/views/trading/account-overview/account-overview.vue`

- [ ] **Step 1: 添加路由页签和查询状态**

引入 `useRoute`、`PageTitleTabs` 和 Task 1 的转换函数；定义 `activeMode = ref(accountModeFromQuery(route.query.mode))`，页签切换用：

```ts
void router.replace({
  query: { ...route.query, mode: mode === "all" ? undefined : mode }
});
```

切换或外部修改 `route.query.mode` 时重置 `pagination.current = 1` 并重新加载。

- [ ] **Step 2: 将账户类型传给服务端查询**

`loadAccounts` 构造：

```ts
const executionMode = accountModeToExecutionMode(activeMode.value);
const response = await listTradingAccounts({
  ...(executionMode ? { execution_mode: executionMode } : {}),
  page: { page: pagination.current, size: pagination.pageSize }
});
```

保留最新请求保护和当前错误处理；切换页签不在前端二次过滤。

- [ ] **Step 3: 添加页签和表格账户类型列**

在页面标题下渲染 `PageTitleTabs`，项目为“全部/真实账户/模拟账户”。表格增加“账户类型”列；把“执行配置”改成“运行环境”，内容改用中文环境转换。

- [ ] **Step 4: 让创建弹窗沿用当前页签模式**

`openCreate` 默认使用 `activeMode === "live" ? 2 : 1`；“全部”默认模拟账户。创建成功后保留当前页签并重新查询。

- [ ] **Step 5: 清理创建、详情、确认和操作文案**

将 Paper、Live、SPOT、SWAP、Testnet、Production、Secret ID、TradingAccount、LogicalAccount、Ready、Not Ready、Equity、Available、PnL、Instrument ID 等用户可见文案替换为中文；保留 Binance、OKX、BTCUSDT 等交易所和标的原名。请求函数名、字段名和后端枚举值不改。

### Task 3: 补齐单元和 E2E 契约

**Files:**
- Modify: `web/src/views/trading/account-overview/account-overview.test.ts`
- Modify: `web/src/views/trading/account-overview/account-form.test.ts`
- Modify: `web/tests/trading-accounts.spec.ts`
- Modify: `web/tests/page-layout-standard-contract.test.ts`

- [ ] **Step 1: 增加账户页源代码契约**

断言包含 `PageTitleTabs`、三个中文页签、“账户类型”和“运行环境”，并断言不包含 `Exchange / 市场`、`创建 Paper 模拟` 等旧展示文案。

- [ ] **Step 2: 更新创建流程 E2E 选择器**

将现有 `Live/Paper/Production` 选择器替换为“真实账户/模拟账户/生产环境”，继续验证真实请求不含模拟字段、模拟请求不含真实字段。

- [ ] **Step 3: 增加页签过滤 E2E**

在路由 mock 中记录 `ListTradingAccounts` 请求，验证：默认请求无 `execution_mode`；点击“真实账户”发送 `execution_mode: 2`；点击“模拟账户”发送 `execution_mode: 1`；深链接 `?mode=paper` 直接选中模拟账户。

- [ ] **Step 4: 运行相关测试**

运行：`pnpm --dir web exec vitest run src/views/trading/account-overview/account-overview.test.ts src/views/trading/account-overview/account-form.test.ts src/views/trading/account-overview/account-display.test.ts src/views/trading/account-overview/account-mode.test.ts`。

运行：`pnpm --dir web exec playwright test tests/trading-accounts.spec.ts --project=chromium`。

预期：单元测试和交易账户 E2E 全部通过。

### Task 4: 类型检查、构建与交付验证

**Files:**
- Verify: `web/src/views/trading/account-overview/account-overview.vue`
- Verify: `web-host/internal/statik/statik.go`（仅在发布构建时重新生成）

- [ ] **Step 1: 运行类型检查和格式检查**

运行：`pnpm --dir web exec vue-tsc --noEmit`；`pnpm --dir web exec prettier --check src/views/trading/account-overview/account-overview.vue src/views/trading/account-overview/account-mode.ts`。

- [ ] **Step 2: 运行生产构建**

运行：`pnpm --dir web run build:prod`。预期构建成功，允许已有 Browserslist、Sass 和 chunk size 警告。

- [ ] **Step 3: 汇总变更并提交**

执行 `git diff --check`，仅暂存本计划涉及的前端文件；不暂存工作区中既有的因子、监控和交易底层未关联改动。提交信息使用：`feat(trade): add account mode tabs`。
