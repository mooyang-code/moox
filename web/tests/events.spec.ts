import { expect, test } from "@playwright/test";
import { installEventsMock } from "./events-mock";

// 事件中心三个页面的浏览器测试（本地跑，不进 CI），数据来自 events-mock.ts。
test.beforeEach(async ({ page }) => {
  await installEventsMock(page);
});

test("菜单：事件中心在交易之后、运维之前，三个子页都能打开", async ({ page }) => {
  await page.goto("/#/events/subscriptions");
  await expect(page.getByRole("heading", { name: "事件订阅" })).toBeVisible();
  const menu = await page.locator(".arco-menu").first().innerText();
  const order = ["交易管理", "事件中心", "资源与运维"].map(name => menu.indexOf(name));
  expect(order.every((position, index) => position >= 0 && (index === 0 || position > order[index - 1]))).toBe(true);
  await page.goto("/#/events/executions");
  await expect(page.getByRole("heading", { name: "处理记录" })).toBeVisible();
  await page.goto("/#/events/delist");
  await expect(page.getByRole("heading", { name: "下架处理" })).toBeVisible();
  await expect(page.getByText("系统订阅「卖出策略持有的下架币」：已启用")).toBeVisible();
});

test("事件订阅：新建时显示字段说明和示例；列表里点事件名打开同样的说明", async ({ page }) => {
  await page.goto("/#/events/subscriptions");
  await page.getByRole("button", { name: "新建订阅" }).click();
  const dialog = page.locator(".arco-modal").filter({ hasText: "新建订阅" });
  await expect(dialog.getByText("交易所：binance 或 okx")).toBeVisible();
  await expect(dialog.locator("pre")).toContainText('"exchange_symbol": "FOOUSDT"');
  await dialog.getByRole("button", { name: "取消" }).click();

  await page.locator(".arco-table").getByText("标的即将下架").first().click();
  const drawer = page.locator(".arco-drawer").filter({ hasText: "data 里的字段" });
  await expect(drawer.getByText("下架时刻（UTC）")).toBeVisible();
});

test("处理记录：从告警带失败筛选进来；打开的记录进 URL，刷新后还在", async ({ page }) => {
  await page.goto("/#/events/executions?status=failed");
  await expect(page.locator(".arco-table-tr").filter({ hasText: "对方返回 502" })).toBeVisible();
  await expect(page.locator(".arco-table").getByText("卖出策略持有的下架币")).toHaveCount(0);
  await page.getByRole("button", { name: "详情" }).click();
  await expect(page).toHaveURL(/execution=x-failed/);
  await page.reload();
  await expect(page.locator(".arco-drawer").getByText("处理详情")).toBeVisible();
  await expect(page.locator(".arco-drawer").getByText("bad gateway")).toBeVisible();
  await expect(page).toHaveURL(/status=failed/);
});
