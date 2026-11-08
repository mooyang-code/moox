import { expect, test } from "@playwright/test";
import { installArchiveMock, type ArchiveCalls } from "./data-archive-mock";

// 数据归档页的浏览器测试（本地跑，不进 CI），数据来自 data-archive-mock.ts。
let calls: ArchiveCalls;

test.beforeEach(async ({ page }) => {
  calls = await installArchiveMock(page);
});

test("打开页面：执行器状态行、未归档的数据集、任务表都在", async ({ page }) => {
  await page.goto("/#/data/archive");
  await expect(page.getByText("归档执行器 archive-worker@factor")).toBeVisible();
  await expect(page.getByText("本空间运行中 0 · 排队 1")).toBeVisible();
  await expect(page.getByRole("heading", { name: "未归档的数据集" })).toBeVisible();
  await expect(page.getByText("现货 5 分钟 K 线")).toBeVisible();
  await expect(page.getByText("每收盘 1 根 · 下次")).toBeVisible();
});

test("建归档任务：预选数据集、回补起点默认最早值、预览 5 次拉取 3 次复核，建好后打开详情", async ({ page }) => {
  await page.goto("/#/data/archive");
  await page.getByRole("button", { name: "建归档任务", exact: true }).click();
  const drawer = page.locator(".arco-drawer").filter({ hasText: "新建归档任务" });
  await expect(drawer.locator(".arco-select-view")).toContainText("现货 5 分钟 K 线 · 5m · 35 天");
  await expect(drawer.locator(".zoned-picker input")).not.toHaveValue("");
  await expect(drawer.getByText("接下来 5 次拉取")).toBeVisible();
  await expect(drawer.locator(".preview .block").first().locator("div")).toHaveCount(6);
  await expect(drawer.locator(".preview .block").nth(1).locator("div")).toHaveCount(4);
  await drawer.getByRole("button", { name: "创建" }).click();
  await expect(page).toHaveURL(/task=ds_kline_5m/);
  expect(calls.bodies.CreateArchiveTask?.[0]).toMatchObject({ dataset_id: "ds_kline_5m", paused: false });
  expect(calls.bodies.CreateArchiveTask?.[0]).not.toHaveProperty("space_id");
});

test("任务详情：分区页签和月份筛选进 URL，刷新后不变，后退回到上一个状态", async ({ page }) => {
  await page.goto("/#/data/archive?task=ds_kline_1m");
  const drawer = page.locator(".arco-drawer");
  // Arco 的页签没有 tab 角色，按类名找
  await drawer
    .locator(".arco-tabs-tab")
    .filter({ hasText: /^分区$/ })
    .click();
  await expect(page).toHaveURL(/tab=partitions/);
  const from = drawer.locator(".task-partitions .arco-picker input").first();
  await from.fill("2026-10");
  await from.press("Enter");
  await expect(page).toHaveURL(/from=2026-10/);
  expect(calls.bodies.ListArchivePartitions?.at(-1)).toMatchObject({ dataset_id: "ds_kline_1m", from_month: "2026-10" });
  await page.reload();
  await expect(page.locator(".arco-drawer .arco-tabs-tab-active")).toHaveText("分区");
  await expect(page).toHaveURL(/from=2026-10/);
  await page.goBack();
  await expect(page).not.toHaveURL(/from=2026-10/);
});

test("运行记录：勾选显示成功的到点拉取后请求带 include_ticks；取消运行要先确认", async ({ page }) => {
  await page.goto("/#/data/archive?task=ds_kline_1m");
  const drawer = page.locator(".arco-drawer");
  await drawer.getByText("显示成功的到点拉取").click();
  await expect(page).toHaveURL(/ticks=1/);
  await expect.poll(() => calls.bodies.ListArchiveRuns?.at(-1)?.include_ticks).toBe(true);
  await drawer.getByRole("button", { name: "取消", exact: true }).first().click();
  await expect(page.getByText("取消这次到点拉取？")).toBeVisible();
  expect(calls.bodies.CancelArchiveRun).toBeUndefined();
  await page.getByRole("button", { name: "取消运行" }).click();
  await expect.poll(() => calls.bodies.CancelArchiveRun?.length).toBe(1);
});

test("归档服务返回 500：显示失败和重试、没有“正常”，两轮轮询后全局错误提示不超过一条", async ({ page }) => {
  calls.fail = true;
  await page.clock.install();
  await page.goto("/#/data/archive");
  await expect(page.getByText("无法读取归档服务")).toBeVisible();
  await expect(page.getByText("无法读取归档任务")).toBeVisible();
  await expect(page.locator(".moox-page")).not.toContainText("正常");
  await page.clock.fastForward(31_000);
  await page.clock.fastForward(31_000);
  expect(await page.locator(".arco-message").count()).toBeLessThanOrEqual(1);
});

test("375 宽度：状态行换行，抽屉不溢出", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto("/#/data/archive?task=ds_kline_1m");
  const drawer = page.locator(".arco-drawer");
  await expect(drawer).toBeVisible();
  const box = await drawer.boundingBox();
  expect(box && box.width <= 375).toBe(true);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);
  await page.screenshot({ path: process.env.ARCHIVE_SHOT || "test-results/data-archive-375.png", fullPage: true });
});
