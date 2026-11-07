import { expect, test, type Locator, type Page } from "@playwright/test";
import { installTradingMock, type Calls } from "./trading-accounts-mock";

// 交易账户页的浏览器契约测试，数据来自 trading-accounts-mock.ts。
let calls: Calls;

test.beforeEach(async ({ page }) => {
  calls = await installTradingMock(page);
});

const field = (scope: Locator, label: string) =>
  scope.locator(".arco-form-item").filter({ hasText: label }).locator("input").first();

const detailTab = (page: Page, label: string) =>
  page.locator(".detail-tabs .arco-tabs-tab").filter({ hasText: new RegExp(`^${label}$`) });

async function openDetail(page: Page, id: string, tab = "") {
  await page.goto(`/#/trading/accounts/${id}${tab ? `?tab=${tab}` : ""}`);
}

test("列表按环境分页签，只列组合账户，三角展开执行账户并显示市场", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await expect(page.getByRole("tab", { name: "模拟盘" })).toHaveAttribute("aria-selected", "true");
  for (const title of ["组合账户", "交易所", "关联策略", "自动执行", "权益", "近 24 小时", "持仓", "就绪"]) {
    await expect(page.getByRole("columnheader", { name: title, exact: true })).toBeVisible();
  }
  await expect(page.getByText("默认策略模拟组合")).toBeVisible();
  await expect(page.getByText("动量轮动")).toBeVisible();
  await expect(page.getByText("币安测试网")).toHaveCount(0);
  // 未加入组合的执行账户不在列表里，收进右上角链接
  await expect(page.getByText("OKX 模拟（未加入）")).toHaveCount(0);
  await expect(page.getByText("未加入组合的执行账户 1 个")).toBeVisible();
  await page.locator(".arco-table-expand-btn").first().click();
  await expect(page.getByText("币安模拟").first()).toBeVisible();
  await expect(page.getByText("Binance · 现货、永续")).toBeVisible();
  await expect(page).toHaveURL(/#\/trading\/accounts$/);

  await page.getByRole("tab", { name: "测试网" }).click();
  await expect(page).toHaveURL(/env=testnet/);
  await expect(page.getByText("测试网组合")).toBeVisible();
  await expect(page.getByText("默认策略模拟组合")).toHaveCount(0);
  await page.getByRole("tab", { name: "生产环境" }).click();
  await expect(page.getByText("生产组合")).toBeVisible();
});

test("未加入组合的执行账户在弹窗里加入组合", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByText("未加入组合的执行账户 1 个").click();
  const dialog = page.locator(".arco-modal:visible");
  await expect(dialog.getByText("OKX 模拟（未加入）")).toBeVisible();
  await dialog.getByRole("button", { name: "加入组合" }).first().click();
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

test("新建模拟账户四步，只开放模拟盘，请求带市场集合", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "新建账户" }).click();
  const dialog = page.locator(".arco-modal:visible");
  await expect(dialog.getByText("1 类型").or(dialog.getByText("类型", { exact: true }))).toBeVisible();
  await expect(dialog.locator('input[value="testnet"]')).toBeDisabled();
  await expect(dialog.locator('input[value="prod"]')).toBeDisabled();
  await expect(dialog.getByText("即将开放")).toHaveCount(2);
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
    ["settings", "执行账户与设置"]
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
  await expect(page.getByText("已拦下：低于最小下单额")).toBeVisible();
  await expect(page.getByText("目标外")).toBeVisible();
});

test("生产环境清仓要输入组合名称，其他环境不用", async ({ page }) => {
  await openDetail(page, "la-prod-1", "settings");
  await expect(page.locator(".tx-env--prod").first()).toBeVisible();
  await page.getByRole("button", { name: "清仓", exact: true }).first().click();
  const dialog = page.locator(".arco-modal:visible");
  await expect(dialog.getByText("这是生产环境，会使用真实资金。")).toBeVisible();
  await field(dialog, "原因").fill("人工检查");
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
  await field(dialog, "原因").fill("风险处理");
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
  await page.goto("/#/trading/accounts");
  await expect(page.getByText("默认策略模拟组合")).toBeVisible();
  const before = calls.listLogicals;
  await page.getByText("默认策略模拟组合").click();
  await expect(page.getByRole("heading", { name: "默认策略模拟组合" })).toBeVisible();
  await page.getByRole("button", { name: "模拟盘" }).first().click();
  await expect(page.getByText("未加入组合的执行账户 1 个")).toBeVisible();
  await expect.poll(() => calls.listLogicals).toBeGreaterThan(before);
});
