import { expect, test, type Locator, type Page } from "@playwright/test";
import { installTradingMock, type Calls } from "./trading-accounts-mock";

// 交易账户页的浏览器契约测试，数据来自 trading-accounts-mock.ts。
let calls: Calls;

test.beforeEach(async ({ page }) => {
  calls = await installTradingMock(page);
});

// 按标签文字完全相等定位表单项（“数量”不会命中“交易标的”里的“数量步长…”提示）
const field = (scope: Locator, label: string) =>
  scope
    .locator(".arco-form-item")
    .filter({ has: scope.page().locator(".arco-form-item-label", { hasText: new RegExp(`^\\s*${label}\\s*$`) }) })
    .locator("input")
    .first();

const detailTab = (page: Page, label: string) =>
  page.locator(".detail-tabs .arco-tabs-tab").filter({ hasText: new RegExp(`^${label}$`) });

async function openDetail(page: Page, id: string, tab = "") {
  await page.goto(`/#/trading/accounts/${id}${tab ? `?tab=${tab}` : ""}`);
}

const listTab = (page: Page, label: string) =>
  page.locator(".list-tabs .arco-tabs-tab").filter({ hasText: new RegExp(`^${label}`) });

test("三个环境各一个菜单，组合账户、执行账户分两个子页签，执行账户显示市场和所属组合", async ({ page }) => {
  await page.goto("/#/trading/paper");
  await expect(page.getByRole("heading", { name: "模拟账户" })).toBeVisible();
  for (const title of ["组合账户", "交易所", "关联策略", "自动执行", "权益", "近 24 小时", "持仓", "就绪"]) {
    await expect(page.getByRole("columnheader", { name: title, exact: true })).toBeVisible();
  }
  await expect(page.getByText("默认策略模拟组合")).toBeVisible();
  await expect(page.getByText("动量轮动")).toBeVisible();
  await expect(page.getByText("币安测试网")).toHaveCount(0);
  // 执行账户在自己的子页签里，组合账户页签不列
  await expect(page.getByText("OKX 模拟（未加入）")).toHaveCount(0);
  await listTab(page, "执行账户").click();
  await expect(page).toHaveURL(/list=execution/);
  await expect(page.getByText("OKX 模拟（未加入）")).toBeVisible();
  await expect(page.getByText("币安模拟").first()).toBeVisible();
  await expect(page.getByText("币安 · 现货、永续")).toBeVisible();
  await expect(page.getByRole("row", { name: /OKX 模拟（未加入）/ }).getByText("未加入", { exact: true })).toBeVisible();
  await listTab(page, "组合账户").click();
  await expect(page).toHaveURL(/#\/trading\/paper$/);

  await page.goto("/#/trading/testnet");
  await expect(page.getByRole("heading", { name: "测试账户" })).toBeVisible();
  await expect(page.getByText("测试网组合")).toBeVisible();
  await expect(page.getByText("默认策略模拟组合")).toHaveCount(0);
  await page.goto("/#/trading/live");
  await expect(page.getByRole("heading", { name: "实盘账户" })).toBeVisible();
  await expect(page.getByText("生产组合")).toBeVisible();
});

test("只带组合编号的地址打开详情后，换到所属环境的地址", async ({ page }) => {
  await openDetail(page, "la-paper-1");
  await expect(page.getByRole("heading", { name: "默认策略模拟组合" })).toBeVisible();
  await expect(page).toHaveURL(/#\/trading\/paper\/la-paper-1/);
});

test("未加入组合的执行账户在执行账户页签的行上加入组合", async ({ page }) => {
  await page.goto("/#/trading/paper?list=execution");
  await page
    .getByRole("row", { name: /OKX 模拟（未加入）/ })
    .getByRole("button", { name: "加入组合" })
    .click();
  const dialog = page.locator(".arco-modal:visible");
  await expect(dialog.getByText("把「OKX 模拟（未加入）」加入组合")).toBeVisible();
  // 运行中的组合不能加成员，只剩已暂停的手动组合
  await dialog.locator(".arco-select").first().click();
  const options = page.locator(".arco-select-option");
  await expect(options.filter({ hasText: "手动模拟组合" })).toBeVisible();
  await expect(options.filter({ hasText: "默认策略模拟组合" })).toHaveCount(0);
  await options.filter({ hasText: "手动模拟组合" }).click();
  await dialog.getByRole("button", { name: "加入", exact: true }).click();
  await expect.poll(() => calls.members.length).toBe(1);
  expect(calls.members[0]).toMatchObject({
    logical_account_id: "la-paper-manual",
    trading_account_id: "ta-paper-free",
    enabled: true
  });
});

test("新建模拟账户四步，测试网和生产环境也可选，请求带市场集合", async ({ page }) => {
  await page.goto("/#/trading/paper");
  await page.getByRole("button", { name: "新建账户" }).click();
  const dialog = page.locator(".arco-modal:visible");
  await expect(dialog.getByText("1 类型").or(dialog.getByText("类型", { exact: true }))).toBeVisible();
  await expect(dialog.locator('input[value="testnet"]')).toBeEnabled();
  await expect(dialog.locator('input[value="prod"]')).toBeEnabled();
  await expect(dialog.getByText("即将开放")).toHaveCount(0);
  await dialog.getByRole("button", { name: "下一步" }).click();
  await field(dialog, "账户名称").fill("演示账户");
  await dialog.getByRole("button", { name: "下一步" }).click();
  // 默认只开永续；再勾现货
  await dialog.getByText("现货", { exact: true }).click();
  await dialog.getByRole("button", { name: "下一步" }).click();
  await field(dialog, "组合名称").fill("演示组合");
  await dialog.getByRole("button", { name: "创建" }).click();
  await expect.poll(() => calls.paper.length).toBe(1);
  expect(calls.paper[0]).toMatchObject({
    account_name: "演示账户",
    logical_account_name: "演示组合",
    exchange: 1,
    markets: [1, 2],
    margin_mode: "CROSS",
    settlement_asset: "USDT"
  });
});

test("详情页六个页签用 ?tab= 直达，切换页签不重建页面", async ({ page }) => {
  await openDetail(page, "la-paper-1");
  await expect(page.getByRole("heading", { name: "默认策略模拟组合" })).toBeVisible();
  await expect(page.getByText("动量轮动").first()).toBeVisible();
  await page.evaluate(() => document.querySelector(".tx-detail")?.setAttribute("data-marker", "kept"));
  for (const [tab, label] of [
    ["positions", "持仓与目标"],
    ["orders", "订单"],
    ["fills", "成交"],
    ["ledger", "资金流水"],
    ["settings", "成员与设置"]
  ]) {
    await detailTab(page, label).click();
    await expect(page).toHaveURL(new RegExp(`tab=${tab}`));
  }
  await expect(page.locator(".tx-detail")).toHaveAttribute("data-marker", "kept");

  await openDetail(page, "la-paper-1", "positions");
  await expect(detailTab(page, "持仓与目标")).toHaveClass(/arco-tabs-tab-active/);
  // 按币一行：BTC 同时有现货和永续目标
  const btc = page.getByRole("row", { name: /BTC/ }).first();
  await expect(btc).toBeVisible();
  await expect(page.getByText("未执行：低于最小下单额")).toBeVisible();
  await expect(page.getByText("不在目标内")).toBeVisible();
});

test("生产环境清仓要输入组合名称，其他环境不用", async ({ page }) => {
  await openDetail(page, "la-prod-1", "settings");
  await expect(page.locator(".tx-env--prod").first()).toBeVisible();
  await page.getByRole("button", { name: "清仓", exact: true }).first().click();
  const dialog = page.locator(".arco-modal:visible");
  await expect(dialog.getByText("这是生产环境，会使用真实资金。")).toBeVisible();
  await field(dialog, "原因（必填）").fill("人工检查");
  const confirm = dialog.getByRole("button", { name: "开始清仓" });
  await expect(confirm).toBeDisabled();
  await field(dialog, "输入组合名称「生产组合」确认").fill("生产组");
  await expect(confirm).toBeDisabled();
  await field(dialog, "输入组合名称「生产组合」确认").fill("生产组合");
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect.poll(() => calls.flatten.length).toBe(1);
  expect(calls.flatten[0]).toMatchObject({ logical_account_id: "la-prod-1", reason: "人工检查" });
  await expect(page.getByText("清仓进行中")).toBeVisible();
});

test("模拟盘清仓不要输入组合名称", async ({ page }) => {
  await openDetail(page, "la-paper-1");
  await page.getByRole("button", { name: "清仓", exact: true }).click();
  const dialog = page.locator(".arco-modal:visible");
  await expect(dialog.getByText("输入组合名称")).toHaveCount(0);
  await field(dialog, "原因（必填）").fill("风险处理");
  await expect(dialog.getByRole("button", { name: "开始清仓" })).toBeEnabled();
});

test("订单、成交、资金流水页签读数据，订单行点开看详情", async ({ page }) => {
  await openDetail(page, "la-paper-1", "orders");
  await expect(page.getByText("ETH-USDT").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "撤单" })).toHaveCount(1);
  await page.getByText("BTC-USDT").first().click();
  await expect(page.locator(".arco-drawer:visible").getByText("成交明细")).toBeVisible();
  await page.keyboard.press("Escape");
  await openDetail(page, "la-paper-1", "fills");
  await expect(page.getByText("0.016 USDT")).toBeVisible();
  await openDetail(page, "la-paper-1", "ledger");
  await expect(page.getByText("手续费").first()).toBeVisible();
});

test("从详情页返回列表时重新读取，不显示缓存的旧数据", async ({ page }) => {
  await page.goto("/#/trading/paper");
  await expect(page.getByText("默认策略模拟组合")).toBeVisible();
  const before = calls.listLogicals;
  await page.getByText("默认策略模拟组合").click();
  await expect(page.getByRole("heading", { name: "默认策略模拟组合" })).toBeVisible();
  await page.getByRole("button", { name: "模拟账户" }).first().click();
  await expect(page.locator(".list-tabs")).toBeVisible();
  await expect.poll(() => calls.listLogicals).toBeGreaterThan(before);
});

test("1280 宽时组合和执行账户列表的每一列都保持自己的宽度，数字不折行", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto("/#/trading/paper");
  await expect(page.getByText("默认策略模拟组合")).toBeVisible();
  const widths = await page
    .locator(".tx-accounts th.arco-table-th")
    .evaluateAll(items => items.map(item => item.getBoundingClientRect().width));
  expect(widths.length).toBeGreaterThanOrEqual(8);
  // 每一列至少 60 像素；关联策略列有自己的宽度
  for (const width of widths) expect(width).toBeGreaterThanOrEqual(60);
  const strategy = await page.getByRole("columnheader", { name: "关联策略", exact: true }).boundingBox();
  expect(strategy?.width).toBeGreaterThanOrEqual(190);
  await expect(page.getByText("动量轮动")).toBeVisible();

  await listTab(page, "执行账户").click();
  const equity = page.locator(".tx-accounts .arco-table-td .tx-num").first();
  await expect(equity).toBeVisible();
  expect((await equity.boundingBox())?.height).toBeLessThan(28);
});

test("390 窄屏下列表、详情和订单抽屉不产生页面横向溢出", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  await page.goto("/#/trading/paper");
  await expect(page.getByText("默认策略模拟组合")).toBeVisible();
  expect(await overflow()).toBeLessThanOrEqual(1);
  await openDetail(page, "la-paper-1");
  await expect(page.getByRole("heading", { name: "默认策略模拟组合" })).toBeVisible();
  expect(await overflow()).toBeLessThanOrEqual(1);
  await openDetail(page, "la-paper-1", "orders");
  await page.getByText("BTC-USDT").first().click();
  const drawer = page.locator(".arco-drawer:visible");
  await expect(drawer.getByText("成交明细")).toBeVisible();
  expect((await drawer.boundingBox())?.width).toBeLessThanOrEqual(390);
  expect(await overflow()).toBeLessThanOrEqual(1);
});

test("资金流水：模拟盘没有市场下拉；交易所没有流水接口时直接说明", async ({ page }) => {
  await openDetail(page, "la-paper-1", "ledger");
  await expect(page.getByText("手续费").first()).toBeVisible();
  // 账户开通了现货和永续，但模拟盘后端不看市场，不显示下拉
  await expect(page.locator(".tx-detail .arco-select")).toHaveCount(1);
  await openDetail(page, "la-prod-1", "ledger");
  await expect(page.getByText("没有资金流水接口")).toBeVisible();
});

test("订单的市场下拉在没输入币时置灰", async ({ page }) => {
  await openDetail(page, "la-paper-1", "orders");
  const disabled = page.locator(".tx-detail .arco-select-view-disabled");
  await expect(disabled).toHaveCount(1);
  await page.getByPlaceholder("币，如 BTC").fill("BTC");
  await expect(disabled).toHaveCount(0);
});

test("持仓行下单：现货、永续各一个入口，预填方向和与目标的差额", async ({ page }) => {
  await openDetail(page, "la-paper-1", "positions");
  await expect(page.getByRole("button", { name: "下单·现货" }).first()).toBeVisible();
  await page.getByRole("row", { name: /BTC/ }).getByRole("button", { name: "下单·永续" }).click();
  const dialog = page.locator(".arco-modal:visible");
  // BTC 永续目标 1、实际 0.5：买入 0.5
  await expect(field(dialog, "数量")).toHaveValue("0.5");
  await expect(dialog.locator(".arco-radio-checked").filter({ hasText: "买入" })).toBeVisible();
});

test("新建模拟账户加入已有组合：一次请求带组合编号，不再单独加成员", async ({ page }) => {
  await page.goto("/#/trading/paper");
  await page.getByRole("button", { name: "新建账户" }).click();
  const dialog = page.locator(".arco-modal:visible");
  await dialog.getByRole("button", { name: "下一步" }).click();
  await field(dialog, "账户名称").fill("加入用账户");
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByText("加入已有组合", { exact: true }).click();
  await dialog.locator(".arco-select").first().click();
  await page.locator(".arco-select-option").filter({ hasText: "手动模拟组合" }).click();
  await dialog.getByRole("button", { name: "创建" }).click();
  await expect.poll(() => calls.paper.length).toBe(1);
  expect(calls.paper[0]).toMatchObject({ logical_account_id: "la-paper-manual", logical_account_name: "" });
  expect(calls.members).toHaveLength(0);
});

test("资金流水、关闭模拟账户对话框的提示不超出外框，成员表交易所·市场不折行", async ({ page }) => {
  await openDetail(page, "la-prod-1", "ledger");
  const alertText = "没有资金流水接口";
  const alert = page.locator(".tx-detail .arco-alert").filter({ hasText: alertText });
  await expect(alert).toBeVisible();
  const section = page.locator(".tx-detail .tx-section", { has: page.locator(".arco-alert", { hasText: alertText }) });
  const [a, b] = [await alert.boundingBox(), await section.boundingBox()];
  expect((a?.x ?? 0) + (a?.width ?? 0)).toBeLessThanOrEqual((b?.x ?? 0) + (b?.width ?? 0) + 0.5);

  await openDetail(page, "la-paper-1", "settings");
  const cell = page.locator(".tx-detail .arco-table-td .tx-nowrap").filter({ hasText: "币安" }).first();
  expect((await cell.boundingBox())?.height).toBeLessThan(26);
  await page.getByRole("button", { name: "关闭模拟账户" }).click();
  const dialog = page.locator(".arco-modal:visible");
  // 红色提示里写明会暂停整个组合账户，不再另叠一条橙色；唯一成员时再多一条蓝色说明
  await expect(dialog.locator(".arco-alert-error")).toHaveCount(1);
  await expect(dialog.locator(".arco-alert-warning")).toHaveCount(0);
  await expect(dialog.getByText("暂停整个组合账户的自动执行")).toBeVisible();
});

test("390 窄屏下持仓页右侧操作列不固定，不会盖住数据列", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await openDetail(page, "la-paper-1", "positions");
  await expect(page.getByRole("row", { name: /BTC/ })).toBeVisible();
  expect(await page.locator(".tx-detail .arco-table-col-fixed-right").count()).toBe(0);
});

// 详情页每 5 秒整体换一次组合对象：正在改的设置不能被悄悄还原（否则按保存会存下还原过的内容）。
test("下单设置、资金设置改到一半，轮询刷新组合后输入保持不变", async ({ page }) => {
  await openDetail(page, "la-paper-1", "settings");
  const detail = page.locator(".tx-detail");
  const minSpot = field(detail, "现货最小下单额");
  await expect(minSpot).toBeVisible();
  const original = await minSpot.inputValue();
  const reserve = field(detail, "资金预留比例");
  await expect(reserve).toBeVisible();
  const reserveOriginal = await reserve.inputValue();
  const edited = original === "20" ? "21" : "20";
  const reserveEdited = reserveOriginal === "0.2" ? "0.3" : "0.2";
  await minSpot.fill(edited);
  await reserve.fill(reserveEdited);
  const before = calls.getLogical;
  await page.waitForTimeout(6500);
  expect(calls.getLogical).toBeGreaterThan(before);
  await expect(minSpot).toHaveValue(edited);
  await expect(reserve).toHaveValue(reserveEdited);
});
