import { expect, test } from "@playwright/test";
import { installArchiveMock, type ArchiveCalls } from "./archive-mock";

// 数据归档（归档任务、归档存储）的浏览器测试（本地跑，不进 CI），数据来自 archive-mock.ts。
let calls: ArchiveCalls;

test.beforeEach(async ({ page }) => {
  calls = await installArchiveMock(page);
});

test("归档任务页：执行器一行（在线、所在主机）、未归档的数据集、任务表都在", async ({ page }) => {
  await page.goto("/#/archive/tasks");
  const line = page.locator(".worker-line");
  await expect(line).toContainText("归档执行器 在线");
  await expect(line).toContainText("所在主机 因子机（192.168.0.103）");
  await expect(line).toContainText("本空间运行中 0 · 排队 1");
  await expect(page.getByRole("heading", { name: "未归档的数据集" })).toBeVisible();
  await expect(page.getByText("现货 5 分钟 K 线")).toBeVisible();
  await expect(page.getByText("每收盘 1 根 · 下次")).toBeVisible();
  await line.getByRole("link", { name: "存放位置与磁盘" }).click();
  await expect(page).toHaveURL(/#\/archive\/storage/);
});

test("建归档任务：目录名默认建议值、路径预览、冲突标红；建好后打开详情", async ({ page }) => {
  await page.goto("/#/archive/tasks");
  await page.getByRole("button", { name: "建归档任务", exact: true }).click();
  const drawer = page.locator(".arco-drawer").filter({ hasText: "新建归档任务" });
  await expect(drawer.locator(".arco-select-view")).toContainText("现货 5 分钟 K 线 · 5m · 35 天");
  await expect(drawer.locator(".zoned-picker input")).not.toHaveValue("");
  const dirInput = drawer.locator(".arco-input-wrapper input").first();
  await expect(dirInput).toHaveValue("binance-spot-kline-5m");
  await expect(drawer).toContainText("crypto/collect/binance-spot-kline-5m/<yyyy>/<yyyymmdd>.csv.gz");
  await expect(drawer).toContainText("每小时 :58:00～:04:00 不拉取，这段时间的边界顺延到 :04:25");
  await expect(drawer.getByText("接下来 5 次拉取")).toBeVisible();
  // 改成已有任务的目录名：防抖后检查，标红
  await dirInput.fill("binance-spot-kline-1m");
  await expect(drawer).toContainText("目录名已被这个空间的其他任务使用");
  expect(calls.bodies.PrepareArchiveTask?.at(-1)).toMatchObject({ dir_name: "binance-spot-kline-1m" });
  await dirInput.fill("binance-spot-kline-5m");
  await expect(drawer).not.toContainText("目录名已被这个空间的其他任务使用");
  await drawer.getByRole("button", { name: "创建" }).click();
  await expect(page).toHaveURL(/task=ds_kline_5m/);
  expect(calls.bodies.CreateArchiveTask?.[0]).toMatchObject({
    dataset_id: "ds_kline_5m",
    paused: false,
    dir_name: "binance-spot-kline-5m"
  });
  expect(calls.bodies.CreateArchiveTask?.[0]).not.toHaveProperty("space_id");
});

test("任务详情：分区页签和月份筛选进 URL，刷新后不变，后退回到上一个状态", async ({ page }) => {
  await page.goto("/#/archive/tasks?task=ds_kline_1m");
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

test("分区下载：已校验的取 ticket 后下载，未就绪的置灰", async ({ page }) => {
  await page.goto("/#/archive/tasks?task=ds_kline_1m&tab=partitions");
  const table = page.locator(".task-partitions");
  await expect(table).toContainText("crypto/collect/binance-spot-kline-1m/2026/20261107.csv.gz");
  const buttons = table.getByRole("button", { name: "下载" });
  await expect(buttons.nth(1)).toBeDisabled();
  const download = page.waitForEvent("download");
  await buttons.first().click();
  expect((await download).suggestedFilename()).toBe("binance-spot-kline-1m_2026-11-07.csv.gz");
  expect(calls.bodies.IssueRawSessionTicket?.at(-1)).toMatchObject({
    operation: "archive_download",
    session_id: "crypto/ds_kline_1m"
  });
  expect(calls.bodies.PartitionDownload?.at(-1)).toMatchObject({
    space: "crypto",
    dataset: "ds_kline_1m",
    partition: "2026-11-07"
  });
});

test("最新数据：标的进 URL，表头写已完整读取到，更早翻页每次重新取 ticket", async ({ page }) => {
  await page.goto("/#/archive/tasks?task=ds_kline_1m&tab=recent&subject=BTC-USDT");
  const drawer = page.locator(".arco-drawer");
  await expect(drawer.locator(".arco-tabs-tab-active")).toHaveText("最新数据");
  await expect(drawer).toContainText("暂存库已完整读取到");
  await expect(drawer.locator(".task-recent tbody tr")).toHaveCount(3);
  expect(calls.bodies.RecentRows?.at(-1)).toMatchObject({ space: "crypto", dataset: "ds_kline_1m", subject: "BTC-USDT" });
  expect(calls.bodies.IssueRawSessionTicket?.at(-1)).toMatchObject({ operation: "archive_recent" });
});

test("归档任务页：归档服务返回 500 时显示失败和重试、没有“正常”，两轮轮询后全局错误提示不超过一条", async ({ page }) => {
  calls.fail = true;
  await page.clock.install();
  await page.goto("/#/archive/tasks");
  await expect(page.getByText("无法读取归档服务")).toBeVisible();
  await expect(page.getByText("无法读取归档任务")).toBeVisible();
  await expect(page.locator(".moox-page")).not.toContainText("正常");
  await page.clock.fastForward(31_000);
  await page.clock.fastForward(31_000);
  expect(await page.locator(".arco-message").count()).toBeLessThanOrEqual(1);
});

test("归档存储页：所在主机、根目录、磁盘、内置对象存储、避让时段", async ({ page }) => {
  await page.goto("/#/archive/storage");
  const main = page.locator(".moox-page");
  await expect(main).toContainText("因子机（192.168.0.103）");
  await expect(main).toContainText("/data/moox/archive");
  await expect(main).toContainText("已用 137 GB / 共 1.76 TB，可用 1.53 TB");
  await expect(main).toContainText("内置 SeaweedFS");
  await expect(main).toContainText("每分钟只在第 25～55 秒读取");
  await expect(main).toContainText("每小时 :58:00～:04:00 不拉取");
  await expect(main).toContainText("datasets.csv");
});

for (const path of ["/#/archive/tasks?task=ds_kline_1m", "/#/archive/storage"]) {
  test(`375 宽度不溢出：${path}`, async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(path);
    await expect(page.locator(".moox-page")).toBeVisible();
    if (path.includes("task=")) {
      const drawer = page.locator(".arco-drawer");
      await expect(drawer).toBeVisible();
      const box = await drawer.boundingBox();
      // 抽屉滑入动画中宽度有亚像素误差，取整再比
      expect(Math.round(box?.width ?? Infinity)).toBeLessThanOrEqual(375);
    }
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow).toBeLessThanOrEqual(0);
    const name = path.includes("storage") ? "archive-storage-375.png" : "archive-tasks-375.png";
    await page.screenshot({ path: `test-results/${name}`, fullPage: true });
  });
}
