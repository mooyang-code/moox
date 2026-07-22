import { expect, test } from "@playwright/test";

test.skip(process.env.MOOX_REMOTE_PLAYWRIGHT !== "1", "remote-only Storage browser verification");

async function login(page: Parameters<typeof test>[0]["page"]) {
  const username = process.env.MOOX_REMOTE_USERNAME;
  const password = process.env.MOOX_REMOTE_PASSWORD;
  if (!username || !password) throw new Error("remote_playwright_credentials_missing");
  await page.goto("/#/login");
  await page.getByPlaceholder("请输入账号").fill(username);
  await page.getByPlaceholder("请输入密码").fill(password);
  await page.getByRole("button", { name: "登录" }).click();
  await page.waitForFunction(() => window.location.hash !== "#/login");
}

async function expectNodeSurface(page: Parameters<typeof test>[0]["page"]) {
  await page.goto("/#/ops/storage/nodes?tab=nodes");
  await expect(page.getByRole("heading", { name: "数据节点" })).toBeVisible();
  const info = page.getByRole("button", { name: "数据节点说明" });
  await info.hover();
  await expect(page.getByRole("tooltip").filter({ hasText: "Dataset 直接绑定 DataNode" })).toBeVisible();
  const rows = page.getByRole("row");
  await expect(rows).not.toHaveCount(0);
  await expect(page.getByText("节点ID", { exact: true })).toBeVisible();
}

test("remote desktop Storage DataNode surface is healthy", async ({ page }) => {
  await login(page);
  await expectNodeSurface(page);
  await expect(page.getByText("服务目标", { exact: true })).toBeVisible();
  await expect(page.getByText("Dataset", { exact: true })).toBeVisible();
});

test("remote mobile Storage DataNode surface remains usable", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  await expectNodeSurface(page);
  const heading = page.getByRole("heading", { name: "数据节点" });
  const box = await heading.boundingBox();
  expect(box).not.toBeNull();
  if (box) expect(box.x + box.width).toBeLessThanOrEqual(390);
});
