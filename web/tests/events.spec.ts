import { expect, test, type Page } from "@playwright/test";
import { installEventsMock } from "./events-mock";

// 事件中心（事件 / 订阅 / 执行记录三个页签）的浏览器测试（本地跑，不进 CI），数据来自 events-mock.ts。
test.beforeEach(async ({ page }) => {
  await installEventsMock(page);
});

const pageTab = (page: Page, label: string) => page.locator(".page-title-tab").filter({ hasText: new RegExp(`^${label}$`) });

test("菜单：事件中心在交易之后、运维之前，是一页三个页签", async ({ page }) => {
  await page.goto("/#/events");
  await expect(pageTab(page, "事件")).toHaveAttribute("aria-selected", "true");
  const menu = await page.locator(".arco-menu").first().innerText();
  const order = ["交易管理", "事件中心", "资源与运维"].map(name => menu.indexOf(name));
  expect(order.every((position, index) => position >= 0 && (index === 0 || position > order[index - 1]))).toBe(true);
  for (const old of ["事件订阅", "处理记录", "下架处理"]) expect(menu).not.toContain(old);
  await expect(page.locator(".arco-table").getByText("标的即将下架").first()).toBeVisible();
  await pageTab(page, "订阅").click();
  await expect(page).toHaveURL(/tab=subscriptions/);
  await expect(page.getByRole("button", { name: "新建订阅" })).toBeVisible();
  await pageTab(page, "执行记录").click();
  await expect(page).toHaveURL(/tab=executions/);
});

test("事件页签：下架事件的详情写处理方式和系统订阅状态；“订阅”打开新建并预选事件；“手工登记”打开登记弹窗", async ({ page }) => {
  await page.goto("/#/events");
  const row = page.locator(".arco-table-tr").filter({ hasText: "标的即将下架" });
  await row.getByRole("button", { name: "详情" }).click();
  const drawer = page.locator(".arco-drawer").filter({ hasText: "系统怎么处理" });
  await expect(drawer.getByText("系统订阅「卖出策略持有的下架币」：已启用")).toBeVisible();
  await drawer.locator(".arco-drawer-close-btn").click();
  await row.getByRole("button", { name: "手工登记" }).click();
  await expect(page.locator(".arco-modal:visible")).toBeVisible();
  await page.keyboard.press("Escape");
  await row.getByRole("button", { name: "订阅" }).click();
  await expect(page).toHaveURL(/tab=subscriptions$/);
  const dialog = page.locator(".arco-modal").filter({ hasText: "新建订阅" });
  await expect(dialog.locator(".arco-select-view-value").first()).toContainText("标的即将下架");
});

test("事件订阅：新建时显示字段说明和示例；列表里点事件名打开同样的说明", async ({ page }) => {
  await page.goto("/#/events?tab=subscriptions");
  await page.getByRole("button", { name: "新建订阅" }).click();
  const dialog = page.locator(".arco-modal").filter({ hasText: "新建订阅" });
  await expect(dialog.getByText("交易所：binance 或 okx")).toBeVisible();
  await expect(dialog.locator("pre")).toContainText('"exchange_symbol": "FOOUSDT"');
  await dialog.getByRole("button", { name: "取消" }).click();

  await page.locator(".arco-table").getByText("标的即将下架").first().click();
  const drawer = page.locator(".arco-drawer").filter({ hasText: "data 里的字段" });
  await expect(drawer.getByText("下架时刻（UTC）")).toBeVisible();
});

test("执行记录：从告警带失败筛选进来；打开的记录进 URL，刷新后还在", async ({ page }) => {
  await page.goto("/#/events?tab=executions&status=failed");
  await expect(page.locator(".arco-table-tr").filter({ hasText: "对方返回 502" })).toBeVisible();
  await expect(page.locator(".arco-table").getByText("卖出策略持有的下架币")).toHaveCount(0);
  await page.getByRole("button", { name: "详情" }).click();
  await expect(page).toHaveURL(/execution=x-failed/);
  await page.reload();
  await expect(page.locator(".arco-drawer").getByText("执行详情")).toBeVisible();
  await expect(page.locator(".arco-drawer").getByText("bad gateway")).toBeVisible();
  await expect(page).toHaveURL(/status=failed/);
});
