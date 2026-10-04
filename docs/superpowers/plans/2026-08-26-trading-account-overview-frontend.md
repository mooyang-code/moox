# Trading Account Overview Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `/#/trading/accounts` 调整为与当前 Trade `TradingAccount`、Paper/Live、readiness 和 LogicalAccount 契约一致的简洁账户工作台，并恢复前端可编译、可验证的完整交易账户流程。

**Architecture:** 保留现有单页账户表格作为入口，不增加复杂 Dashboard、审批流或多余筛选。列表展示物理交易账户的身份、执行模式、环境、资金快照和 readiness；详情抽屉承载同步、Live 配置维护、杠杆和跨页面跳转；创建 Paper 继续调用现有 `CreatePaperSimulation`，明确该 RPC 同时创建 TradingAccount 与 LogicalAccount。前端不复制 Trade 的执行规则，Production gate、账户 readiness 和 Paper 能力全部以服务端返回为准。

**Tech Stack:** Vue 3、TypeScript、Arco Design、Vue Router、Vitest、Vue Test Utils、Playwright、Trade tRPC console API。

**Source of truth:** `modules/trade/DESIGN.md`、`modules/trade/proto/trade_service.proto`、`web/src/api/trade/types.ts`。这是新项目，不保留 `CreateAccount`、`ExchangeAccount`、`exchange_account_id` 等旧兼容字段、旧 API 别名或 Paper 环境枚举。

---

## 1. Scope and Invariants

### 1.1 当前基线

- `web/src/api/trade/index.ts` 当前工作区改动仍引用旧的 `CreateAccountReq`、`ExchangeAccount`、`ListAccountsReq`、`UpdateAccountReq` 和 `exchange_account_id`，与当前 proto/types 不一致；这是首个阻断点。
- `web/src/views/trading/account-overview/account-overview.vue` 已经有统一 Paper/Live 入口，但表单混用 Live 专属字段、打开创建窗口时状态未完整重置、同步响应的 `ready`/warnings 未展示、Paper 返回的 LogicalAccount 未被利用。
- `position-detail.vue`、`trade-record.vue` 和 `logical-accounts/index.vue` 使用统一 API，但尚未消费账户详情跳转所需的 URL query。
- 当前 `pnpm exec vue-tsc --noEmit` 应先失败于上述 API 名称不匹配；页面契约测试可运行不等于类型检查通过。

### 1.2 不变量

```
TradingAccount.trading_account_id 是物理账户唯一标识。
CreateTradingAccount.live 只承载 Live 配置；CreatePaperSimulation 不发送 live、credential 或 sync_symbols。
CreatePaperSimulation 返回 TradingAccount 与 LogicalAccount，前端必须给出可见反馈和跳转。
AccountEnvironment 只有 UNSPECIFIED(0)、TESTNET(1)、PRODUCTION(2)；Paper 不是一种 environment。
服务端返回的 ready、last_error、GetExecutionCapabilities 才是可下单/可关闭 Paper 的权威。
前端只负责展示和发起操作，不在浏览器重新实现 Production gate 或交易风控。
```

### 1.3 明确不做

- 不保留任何旧 API 兼容别名或旧字段映射。
- 不新增复杂资产 Dashboard、审批流、批量账户管理、实时行情图或真实 Production 下单 E2E。
- 不在前端保存或回显 credential secret；只提交服务端认可的 Secret ID。
- 不把客户端校验结果当作服务端 readiness 或执行资格。

## 2. File Map

### 修改

- `web/src/api/trade/index.ts`：统一交易账户 RPC、请求字段、枚举标签和返回类型。
- `web/src/api/trade/trade.test.ts`：锁定统一 API 名称与环境标签。
- `web/src/api/trade/types.ts`：仅在当前 proto 缺少前端表现所需的非兼容展示类型时补充类型。
- `web/src/views/trading/account-overview/account-form.ts`：表单默认值、规范化、校验、Paper/Live 请求构造。
- `web/src/views/trading/account-overview/account-form.test.ts`：表单纯函数的 TDD 覆盖。
- `web/src/views/trading/account-overview/account-overview.vue`：列表、创建 Modal、详情 Drawer、同步和 Paper 生命周期。
- `web/src/views/trading/logical-accounts/index.vue`：消费 `logical_account_id` 深链接。
- `web/src/views/trading/position-detail/position-detail.vue`：消费 `trading_account_id` 深链接。
- `web/src/views/trading/trade-record/trade-record.vue`：消费 `trading_account_id` 深链接。
- `web/tests/page-layout-standard-contract.test.ts`、`web/tests/page-toolbar-cleanup-contract.test.ts`：更新账户页的页面结构契约。

### 新增

- `web/src/views/trading/account-overview/account-display.ts`：状态、环境、快照的纯展示函数。
- `web/src/views/trading/account-overview/account-display.test.ts`：展示函数测试。
- `web/src/views/trading/account-overview/account-overview.test.ts`：组件交互与 API mock 测试。
- `web/tests/trading-accounts.spec.ts`：浏览器级账户工作台 E2E。

## 3. Implementation Tasks

### Task 1: 恢复统一 Trade 前端 API 契约

**Files:**

- Modify: `web/src/api/trade/index.ts`
- Modify: `web/src/api/trade/trade.test.ts`
- Verify: `web/src/api/trade/types.ts`

- [ ] **Step 1: 记录当前失败基线**

Run:

```sh
cd web
pnpm exec vue-tsc --noEmit
```

Expected: FAIL，错误集中在旧的 `CreateAccountReq`、`ExchangeAccount`、`ListAccountsReq`、`UpdateAccountReq`、`createAccount`、`listAccounts` 和缺失的统一 API 导出。不要通过增加旧类型或别名来消除错误。

- [ ] **Step 2: 先写统一 RPC 名称和枚举标签测试**

在 `web/src/api/trade/trade.test.ts` 增加如下断言（沿用现有 `callTrade` mock）：

```ts
it("uses canonical unified account RPC names", async () => {
  await trade.listTradingAccounts({ page: { page: 1, size: 20 } });
  expect(callTrade).toHaveBeenLastCalledWith("console", "ListTradingAccounts", {
    page: { page: 1, size: 20 },
  });

  await trade.syncTradingAccount("account-1");
  expect(callTrade).toHaveBeenLastCalledWith("console", "SyncTradingAccount", {
    trading_account_id: "account-1",
  });
});

it("keeps Paper execution mode separate from AccountEnvironment", () => {
  expect(trade.executionModeLabels).toEqual({ 0: "-", 1: "Paper", 2: "Live" });
  expect(trade.environmentLabels).toEqual({
    0: "-",
    1: "Testnet",
    2: "Production",
  });
});
```

- [ ] **Step 3: 删除旧 API 并导出当前 RPC**

保留并逐一核对这些导出：

```ts
createTradingAccount
updateTradingAccount
getTradingAccount
listTradingAccounts
syncTradingAccount
createPaperSimulation
closePaperSimulation
getExecutionCapabilities
queryEquityCurve
listHoldings
listLogicalAccounts
getLogicalAccount
listOrders
listFills
listPositions
```

删除 `createAccount`、`updateAccount`、`getAccount`、`listAccounts`、`syncAccount` 及其旧请求/响应类型。所有账户操作参数统一使用 `trading_account_id`；不得重新引入 `exchange_account_id`。

- [ ] **Step 4: 校准环境类型和请求字段**

检查 `AccountEnvironment` 只映射 `0/1/2 = unspecified/testnet/production`。Live 请求包含 `environment`、`credential_secret_id`、`settlement_asset`、`margin_mode`、`sync_symbols` 等 proto 字段；Paper 请求只包含 `initial_balance`、费用、滑点和 LogicalAccount 名称，不附带 Live oneof 字段。

- [ ] **Step 5: 运行 API 测试和类型检查**

```sh
cd web
pnpm test -- --run src/api/trade/trade.test.ts
pnpm exec vue-tsc --noEmit
```

Expected: 两者 PASS，且 `rg -n "createAccount|listAccounts|exchange_account_id|ExchangeAccount|CreateAccountReq" src web` 不再命中交易前端生产代码。

- [ ] **Step 6: 提交独立变更**

```sh
git add web/src/api/trade/index.ts web/src/api/trade/trade.test.ts web/src/api/trade/types.ts
git commit -m "fix(web): align trade account api contract"
```

### Task 2: 抽离表单默认值、规范化与校验

**Files:**

- Modify: `web/src/views/trading/account-overview/account-form.ts`
- Modify: `web/src/views/trading/account-overview/account-form.test.ts`
- Create: `web/src/views/trading/account-overview/account-display.ts`
- Create: `web/src/views/trading/account-overview/account-display.test.ts`

- [ ] **Step 1: 为默认值和校验写失败测试**

在 `account-form.test.ts` 覆盖以下行为：

```ts
it("creates a fresh default form for every modal open", () => {
  expect(createDefaultAccountForm()).toEqual({
    name: "",
    exchange: 1,
    market_type: 1,
    execution_mode: 1,
    environment: 1,
    credential_secret_id: "",
    settlement_asset: "USDT",
    margin_mode: "CROSS",
    initial_balance: "100000",
    maker_fee_rate: "0",
    taker_fee_rate: "0",
    slippage_bps: "0",
    logical_account_name: "",
    sync_symbols: "",
  });
});

it("rejects invalid Paper decimals and missing Live symbols", () => {
  const paper = createDefaultAccountForm();
  paper.execution_mode = 1;
  paper.initial_balance = "0";
  expect(validateAccountForm(paper)).toContain("初始资金");

  const live = createDefaultAccountForm();
  live.execution_mode = 2;
  live.credential_secret_id = "secret-1";
  live.sync_symbols = "";
  expect(validateAccountForm(live)).toContain("交易标的");
});

it("does not send Live-only fields in a Paper request", () => {
  const form = createDefaultAccountForm();
  form.name = "paper";
  form.credential_secret_id = "must-not-send";
  form.sync_symbols = "BTCUSDT";
  const request = buildPaperSimulationRequest(form);
  expect(request).not.toHaveProperty("live");
  expect(request).not.toHaveProperty("credential_secret_id");
  expect(request).not.toHaveProperty("sync_symbols");
});
```

- [ ] **Step 2: 实现可复用默认值和校验函数**

在 `account-form.ts` 导出以下签名，并让组件只通过这些函数构造请求：

```ts
export function createDefaultAccountForm(): AccountFormModel {
  return {
    name: "",
    exchange: 1,
    market_type: 1,
    execution_mode: 1,
    environment: 1,
    credential_secret_id: "",
    settlement_asset: "USDT",
    margin_mode: "CROSS",
    initial_balance: "100000",
    maker_fee_rate: "0",
    taker_fee_rate: "0",
    slippage_bps: "0",
    logical_account_name: "",
    sync_symbols: "",
  };
}

export function validateAccountForm(form: AccountFormModel): string {
  if (!form.name.trim()) return "请输入账户名称";
  if (!form.settlement_asset.trim()) return "请输入结算资产";
  if (form.execution_mode === 2 && !form.credential_secret_id.trim()) {
    return "请输入 Live Secret ID";
  }
  if (form.execution_mode === 1 && Number(form.initial_balance) <= 0) {
    return "Paper 初始资金必须大于 0";
  }
  if (form.execution_mode === 2 && form.market_type === 1 && normalizeSymbols(form.sync_symbols).length === 0) {
    return "请至少填写一个交易标的";
  }
  for (const value of [form.maker_fee_rate, form.taker_fee_rate, form.slippage_bps]) {
    if (!/^(?:0|[1-9]\d*)(?:\.\d+)?$/.test(value.trim())) return "费率和滑点必须是非负数字";
  }
  if (Number(form.slippage_bps) >= 10000) return "滑点必须小于 10000 bps";
  if (form.market_type === 2 && (form.settlement_asset !== "USDT" || form.margin_mode !== "CROSS")) {
    return "SWAP 仅支持 USDT/CROSS";
  }
  return "";
}
```

校验规则固定为：名称、结算资产、Live Secret ID 必填；Live SPOT 至少一个规范化 symbol；Paper 初始资金大于 0；费率和滑点为非负十进制；滑点小于 10000 bps；SWAP 强制 USDT + CROSS。symbol 处理为 trim、逗号/空白分隔、转大写、去重。

- [ ] **Step 3: 明确两种请求的字段边界**

`buildLiveRequest` 只发送 `CreateTradingAccountRequest.live` 所需字段；`buildPaperSimulationRequest` 只发送 Paper 配置。用显式对象构造，禁止把整个表单对象 spread 到请求：

```ts
return {
  name: normalized.name,
  exchange: normalized.exchange,
  market_type: normalized.market_type,
  execution_mode: 1,
  live: {
    environment: normalized.environment,
    credential_secret_id: normalized.credential_secret_id,
    settlement_asset: normalized.settlement_asset,
    margin_mode: normalized.margin_mode,
    sync_symbols: normalized.sync_symbols,
  },
};
```

- [ ] **Step 4: 增加纯展示函数并测试未知值**

`account-display.ts` 导出：

```ts
export function accountStatusView(status: number, ready: boolean): {
  label: string;
  color: "green" | "orange" | "red" | "gray";
};
export function accountEnvironmentView(account: TradingAccount): string;
export function snapshotValue(value?: string): string;
export function snapshotPnlClass(value?: string): "positive" | "negative" | "";
```

覆盖 ENABLED、DISABLED、ERROR、未知状态、未 ready、Paper 无 environment、空快照和正负 PnL；未知枚举必须显示 `-` 或 `Unknown`，不得把 Paper 误标为 Testnet。

- [ ] **Step 5: 运行纯函数测试**

```sh
cd web
pnpm test -- --run src/views/trading/account-overview/account-form.test.ts src/views/trading/account-overview/account-display.test.ts
```

Expected: PASS。

- [ ] **Step 6: 提交表单边界变更**

```sh
git add web/src/views/trading/account-overview/account-form.ts web/src/views/trading/account-overview/account-form.test.ts web/src/views/trading/account-overview/account-display.ts web/src/views/trading/account-overview/account-display.test.ts
git commit -m "refactor(web): centralize trading account form rules"
```

### Task 3: 实现账户列表、创建 Modal 和详情 Drawer

**Files:**

- Modify: `web/src/views/trading/account-overview/account-overview.vue`
- Create: `web/src/views/trading/account-overview/account-overview.test.ts`

- [ ] **Step 1: 写组件交互测试**

用 `vi.mock("@/api/trade")`、Vue Test Utils、Arco 组件 stub 覆盖：

```ts
it("renders snapshot, readiness and last error from the server", async () => {
  listTradingAccounts.mockResolvedValue({ trading_accounts: [notReadyLiveAccount] });
  const wrapper = mountAccountOverview();
  await flushPromises();
  expect(wrapper.text()).toContain("未就绪");
  expect(wrapper.text()).toContain(notReadyLiveAccount.last_error);
  expect(wrapper.text()).toContain(notReadyLiveAccount.snapshot.equity);
});

it("resets every field when the create modal opens twice", async () => {
  const wrapper = mountAccountOverview();
  await wrapper.find("[data-test=create-account]").trigger("click");
  await wrapper.find("[data-test=account-name]").setValue("stale-name");
  await wrapper.find("[data-test=close-create]").trigger("click");
  await wrapper.find("[data-test=create-account]").trigger("click");
  expect(wrapper.find("[data-test=account-name]").element).toHaveValue("");
});

it("shows sync symbols only for Live mode", async () => {
  const wrapper = mountAccountOverview();
  await wrapper.find("[data-test=create-account]").trigger("click");
  expect(wrapper.find("[data-test=sync-symbols]").exists()).toBe(false);
  await wrapper.find("[data-test=execution-mode]").setValue(2);
  expect(wrapper.find("[data-test=sync-symbols]").exists()).toBe(true);
});

it("surfaces Paper logical account returned by the server", async () => {
  createPaperSimulation.mockResolvedValue({ trading_account: paperAccount, logical_account: logicalAccount });
  const wrapper = mountAccountOverview();
  await submitPaperForm(wrapper);
  expect(wrapper.text()).toContain(logicalAccount.logical_account_id);
  expect(wrapper.find("[data-test=view-logical-account]").exists()).toBe(true);
});

it("shows sync warnings and the returned ready flag", async () => {
  syncTradingAccount.mockResolvedValue({ ready: false, warnings: ["symbol not found"], synced_symbol_count: 1 });
  const wrapper = mountAccountOverview();
  await openDetailsAndSync(wrapper, liveAccount.trading_account_id);
  expect(wrapper.text()).toContain("未就绪");
  expect(wrapper.text()).toContain("symbol not found");
});

it("does not allow closing Paper when capabilities deny it", async () => {
  getExecutionCapabilities.mockResolvedValue({ can_close_paper_simulation: false });
  const wrapper = mountAccountOverview();
  await openPaperDetails(wrapper, paperAccount.trading_account_id);
  expect(wrapper.find("[data-test=close-paper]").attributes("disabled")).toBeDefined();
});
```

测试必须断言 API 调用参数，而不是只断言 DOM 文本；Paper 测试数据同时包含 `trading_account.id` 与 `logical_account.id`。

- [ ] **Step 2: 加入请求竞态保护和状态模型**

在组件内维护：

```ts
const accounts = ref<TradingAccount[]>([]);
const selectedAccount = ref<TradingAccount | null>(null);
const detailVisible = ref(false);
const createVisible = ref(false);
const syncing = ref(false);
const syncResult = ref<SyncTradingAccountResponse | null>(null);
let listRequestVersion = 0;
```

`loadAccounts` 每次递增版本号，只有最新请求允许写入 `accounts`；刷新期间不得清空已有列表。请求失败保留当前列表并显示可行动的错误消息。

- [ ] **Step 3: 调整列表信息密度**

列表列固定为：账户（名称 + 可复制/省略 ID）、Exchange/市场、执行配置（Paper 或 Live + Testnet/Production）、资金（Equity/Available）、状态、Readiness、最近同步、操作。使用 `account-display.ts`，Decimal 以字符串安全展示，不在浏览器用浮点数计算资金。

状态需同时展示 `status` 和 `ready`：`ready=false` 显示“未就绪”，并显示 `last_error` 摘要；不能仅因 status 为 ENABLED 就显示可交易。

- [ ] **Step 4: 让创建 Modal 按模式隔离字段**

Paper 表单仅显示初始资金、maker/taker fee、slippage 和 LogicalAccount 名称；Live 表单显示 Testnet/Production、Secret ID、结算资产、保证金模式和交易标的。模式切换时清理不适用字段，打开 Modal 时必须执行：

```ts
Object.assign(form, createDefaultAccountForm());
formErrors.value = [];
syncResult.value = null;
```

SWAP 将结算资产固定为 USDT、保证金模式固定为 CROSS，并给出说明；Production 创建按钮显示明确环境，提交前使用 `Modal.confirm`。服务端失败时 Modal 保持打开并展示下一步提示。

- [ ] **Step 5: 处理 Paper 创建、Live 创建和同步结果**

Paper 成功后展示两个 ID，并提供“查看 LogicalAccount”跳转；Live 成功后展示账户状态并调用同步。同步结果必须展示 `ready`、同步 symbol 数、持仓/订单计数、`warnings`；不要用前端推断覆盖服务端 `ready`。

```ts
const response = await createPaperSimulation(buildPaperSimulationRequest(form));
createdAccount.value = response.trading_account;
createdLogicalAccount.value = response.logical_account;
```

关闭 Paper 必须先调用 `getExecutionCapabilities`，只有 `can_close_paper_simulation=true` 才启用按钮；确认后调用 `closePaperSimulation({ trading_account_id })`，成功后刷新详情和列表。

- [ ] **Step 6: 实现详情 Drawer 操作**

详情展示身份、执行模式/环境、status、ready、last sync、last ready、last error、资金快照、symbols、杠杆配置和关联 LogicalAccount。操作包括同步、跳转持仓/订单、Live 配置编辑、SWAP 杠杆设置和 Paper 关闭；每个操作使用 canonical `trading_account_id`。不在详情页增加真实下单入口。

- [ ] **Step 7: 运行组件测试、类型检查和格式检查**

```sh
cd web
pnpm test -- --run src/views/trading/account-overview/account-overview.test.ts
pnpm exec vue-tsc --noEmit
pnpm exec prettier --check src/api/trade src/views/trading/account-overview
```

Expected: 全部 PASS。

- [ ] **Step 8: 提交账户工作台变更**

```sh
git add web/src/views/trading/account-overview/account-overview.vue web/src/views/trading/account-overview/account-overview.test.ts
git commit -m "feat(web): improve trading account workbench"
```
### Task 4: 补齐账户到 LogicalAccount、持仓和订单的导航

**Files:**

- Modify: `web/src/views/trading/account-overview/account-overview.vue`
- Modify: `web/src/views/trading/logical-accounts/index.vue`
- Modify: `web/src/views/trading/position-detail/position-detail.vue`
- Modify: `web/src/views/trading/trade-record/trade-record.vue`
- Create or extend: `web/src/views/trading/account-overview/account-overview.test.ts`

- [ ] **Step 1: 先写 URL query 行为测试**

测试以下 canonical query：

```ts
expect(router.push).toHaveBeenCalledWith({
  path: "/trading/positions",
  query: { trading_account_id: "ta-1" },
});
expect(router.push).toHaveBeenCalledWith({
  path: "/trading/orders",
  query: { trading_account_id: "ta-1" },
});
expect(router.push).toHaveBeenCalledWith({
  path: "/trading/logical-accounts",
  query: { logical_account_id: "la-1" },
});
```

断言页面加载时会消费 query 并把账户传给列表 RPC；只允许 `trading_account_id`、`logical_account_id`，不支持旧的 `exchange_account_id`。

- [ ] **Step 2: 给持仓和订单页接入账户 query**

在两个页面使用 `useRoute()` 和 `useRouter()`：

```ts
const route = useRoute();
const router = useRouter();
const selectedTradingAccountId = ref(
  typeof route.query.trading_account_id === "string"
    ? route.query.trading_account_id
    : "",
);

watch(
  () => route.query.trading_account_id,
  (value) => {
    selectedTradingAccountId.value = typeof value === "string" ? value : "";
    void load();
  },
  { immediate: true },
);

function onTradingAccountChange(id: string) {
  selectedTradingAccountId.value = id;
  void router.replace({ query: { ...route.query, trading_account_id: id } });
  void load();
}
```

加载账户列表后，如果 query 中的 ID 仍存在则选中它；如果账户不存在，显示“账户不存在或无权限”并清理无效 query，不静默切换到另一个账户。

- [ ] **Step 3: 给 LogicalAccount 页接入 LogicalAccount query**

页面加载 `logical_account_id` 后直接打开对应详情抽屉；详情关闭时使用 `router.replace` 删除该 query。账户成员列表仍使用 `listTradingAccounts` 返回的 `trading_account_id`，不新增前端映射层。

- [ ] **Step 4: 让账户详情使用可访问的显式操作**

账户表格和详情 Drawer 的“查看持仓”“查看订单”“查看 LogicalAccount”使用按钮或链接触发 `router.push`，不要用不可聚焦的 `div @click`。按钮必须携带明确文本或 `aria-label`，并在点击后保留 canonical ID。

- [ ] **Step 5: 运行导航回归**

```sh
cd web
pnpm test -- --run src/views/trading/account-overview/account-overview.test.ts src/views/trading/position-detail src/views/trading/trade-record src/views/trading/logical-accounts
pnpm exec vue-tsc --noEmit
```

Expected: PASS；从账户详情跳转后，目标页筛选条件、请求参数和 URL 三者一致。

- [ ] **Step 6: 提交导航变更**

```sh
git add web/src/views/trading/account-overview/account-overview.vue web/src/views/trading/logical-accounts/index.vue web/src/views/trading/position-detail/position-detail.vue web/src/views/trading/trade-record/trade-record.vue web/src/views/trading/account-overview/account-overview.test.ts
git commit -m "feat(web): link trading account workbench details"
```

### Task 5: 补齐页面契约、表单可访问性和浏览器 E2E

**Files:**

- Modify: `web/tests/page-layout-standard-contract.test.ts`
- Modify: `web/tests/page-toolbar-cleanup-contract.test.ts`
- Create: `web/tests/trading-accounts.spec.ts`
- Modify: `web/src/views/trading/account-overview/account-overview.vue`

- [ ] **Step 1: 更新页面结构契约**

为账户页增加这些稳定断言：

```ts
expect(pageText).toContain("交易账户");
expect(headerActions).toContain("创建账户");
expect(accountTable).toContain("Readiness");
expect(accountTable).toContain("最近同步");
expect(accountOperations).toContain("详情");
```

同时断言 Paper/Live 专属字段不会同时显示、刷新按钮存在 loading 状态且刷新期间不清空已有表格。不要把具体 Arco DOM class 作为唯一契约。

- [ ] **Step 2: 修复表单和操作的可访问性**

表单控件使用稳定 `field`、`label`、`name` 和必要的 `autocomplete`：

```vue
<a-form-item field="name" label="名称" required>
  <a-input v-model="form.name" name="account_name" autocomplete="off" />
</a-form-item>
```

校验错误必须指出下一步动作，例如“请输入 Live Secret ID”；图标按钮必须有 `aria-label`；关闭 Drawer、确认 Modal、取消创建均可用键盘操作。Production 和 Paper 关闭这类破坏性操作必须经过确认。

- [ ] **Step 3: 编写浏览器 E2E**

`web/tests/trading-accounts.spec.ts` 使用测试服务或网络 mock，不连接真实交易所、不提交真实 Secret、不创建 Production 订单，覆盖：

```ts
test("lists Paper and Live accounts with distinct environment display", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await expect(page.getByText("Paper")).toBeVisible();
  await expect(page.getByText("Live")).toBeVisible();
  await expect(page.getByText("Testnet")).toBeVisible();
});

test("keeps Paper and Live fields isolated and resets modal state", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "创建账户" }).click();
  await expect(page.getByLabel("初始资金")).toBeVisible();
  await expect(page.getByLabel("Secret ID")).toHaveCount(0);
  await page.getByLabel("执行模式").selectOption("2");
  await expect(page.getByLabel("Secret ID")).toBeVisible();
  await page.getByRole("button", { name: "取消" }).click();
  await page.getByRole("button", { name: "创建账户" }).click();
  await expect(page.getByLabel("账户名称")).toHaveValue("");
});

test("requires confirmation before creating a Production account", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await openLiveProductionForm(page);
  await page.getByRole("button", { name: "创建 Production 账户" }).click();
  await expect(page.getByRole("dialog", { name: "确认创建 Production 账户" })).toBeVisible();
});

test("surfaces sync ready state, counts and warnings", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "详情" }).first().click();
  await page.getByRole("button", { name: "同步账户" }).click();
  await expect(page.getByText("未就绪")).toBeVisible();
  await expect(page.getByText("symbol not found")).toBeVisible();
});

test("requires capability before closing a Paper simulation", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await openPaperDetails(page);
  await expect(page.getByRole("button", { name: "关闭 Paper 模拟" })).toBeDisabled();
});

test("navigates to positions, orders and LogicalAccount with canonical ids", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "查看持仓" }).first().click();
  await expect(page).toHaveURL(/trading\/positions.*trading_account_id=ta-/);
  await page.goBack();
  await page.getByRole("button", { name: "查看订单" }).first().click();
  await expect(page).toHaveURL(/trading\/orders.*trading_account_id=ta-/);
});
```

每个测试必须等待可观察的页面状态或网络响应，禁止固定长时间 sleep；失败时保留 screenshot/trace。

- [ ] **Step 4: 运行前端定向验证**

```sh
cd web
pnpm test -- --run tests/page-layout-standard-contract.test.ts tests/page-toolbar-cleanup-contract.test.ts tests/trading-accounts.spec.ts
pnpm exec prettier --check src/api/trade src/views/trading/account-overview src/views/trading/logical-accounts src/views/trading/position-detail src/views/trading/trade-record tests
pnpm exec vue-tsc --noEmit
```

Expected: 全部 PASS；E2E 的 Production 场景只验证确认和服务端拒绝/接受反馈，不触发真实交易。

- [ ] **Step 5: 提交页面测试变更**

```sh
git add web/tests/page-layout-standard-contract.test.ts web/tests/page-toolbar-cleanup-contract.test.ts web/tests/trading-accounts.spec.ts web/src/views/trading/account-overview/account-overview.vue
git commit -m "test(web): cover trading account workbench"
```

### Task 6: 最终构建、后端回归和发布前验收

**Files:**

- Verify all files modified in Tasks 1-5.
- Do not modify unrelated existing worktree changes in factor, monitor, exchange or paper-simulation files.

- [ ] **Step 1: 运行完整前端测试和生产构建**

```sh
cd web
pnpm test
pnpm run build:prod
```

Expected: 全部测试 PASS；`build:prod` 中 `vue-tsc` 和 Vite 构建均成功。若失败，按失败文件修复后重新执行完整命令，不以跳过测试代替修复。

- [ ] **Step 2: 运行 Trade 相关 Go 回归**

```sh
cd modules/trade
go test -count=1 ./internal/rpc/... ./internal/application/account/... ./internal/application/papersimulation/...
```

Expected: 与账户创建、同步、Paper 生命周期和执行能力相关的 Go 测试 PASS；前端改动不得改变服务端 gate 语义。

- [ ] **Step 3: 做一次无真实交易的浏览器验收**

按以下顺序验收并记录结果：

1. 列表同时正确显示 Paper/Live、环境、snapshot、status、ready 和 last error。
2. 创建 Modal 连续打开两次，前一次名称、Secret、symbols、费用和模式不会残留。
3. Paper 创建反馈同时包含两个 ID，可跳转 LogicalAccount；请求没有 `live`/Secret/symbols。
4. Live Testnet 创建后同步，页面显示服务端 `ready`、计数和 warnings。
5. 详情跳转到持仓、订单、LogicalAccount 后，URL query 和请求 ID 一致。
6. Paper 关闭按钮受 `GetExecutionCapabilities` 控制，确认后状态不可逆地更新。
7. Production 只验证确认、服务端拒绝和错误提示，不下真实订单。

- [ ] **Step 4: 检查差异和工作区边界**

```sh
git diff --check
git status --short
git diff --stat HEAD~4..HEAD
rg -n "CreateAccountReq|ExchangeAccount|ListAccountsReq|UpdateAccountReq|createAccount|listAccounts|exchange_account_id" web/src web/tests
```

Expected: 交易前端不再出现旧兼容符号；计划范围内的变更可追踪；已有的无关工作区改动保持原样，不被删除或重写。

## 4. Definition of Done

- `pnpm exec vue-tsc --noEmit`、`pnpm test`、`pnpm run build:prod` 和 Trade 相关 Go 回归全部通过。
- 账户 API 只使用当前统一 RPC 和 `trading_account_id`，不保留旧兼容别名。
- Paper/Live 请求字段严格隔离；Paper 创建返回的 TradingAccount 与 LogicalAccount 都被展示或可导航。
- 列表和详情以服务端 `ready`、`last_error`、capabilities 为准，不伪造“可交易”状态。
- 同步 warnings、ready、计数、最近同步时间和错误信息可见且可行动。
- 账户详情到持仓、订单、LogicalAccount 的深链接在刷新后仍然有效。
- 表单可访问性、破坏性操作确认、错误下一步提示和 loading 状态均有测试。
- E2E 不使用真实 Production 凭证或下单；仅验证 UI、RPC 参数和服务端拒绝/反馈。
