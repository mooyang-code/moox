import { expect, test, type Page } from "@playwright/test";

test.skip(process.env.MOOX_REMOTE_PLAYWRIGHT !== "1", "remote-only Storage browser verification");

test.describe.configure({ mode: "serial" });

type RpcBody = {
  ret_info?: { code?: number | string; msg?: string };
  items?: Array<{ node?: { node_id?: string }; datasets?: Array<{ dataset_id?: string; name?: string }> }>;
  spaces?: Array<{ space_id?: string; name?: string }>;
  fields?: Array<{ field_id?: string; name?: string }>;
};

type RpcExchange = {
  body: RpcBody;
  requestBody: Record<string, unknown>;
};

function waitForMethodExchange(page: Page, method: string, service = "storage", expectedSpaceID = "") {
  return page
    .waitForResponse(response => {
      if (response.request().method() !== "POST") return false;
      if (new URL(response.url()).pathname !== `/api/admin/${service}/${method}`) return false;
      if (!expectedSpaceID) return true;
      try {
        return (response.request().postDataJSON() as Record<string, unknown>).space_id === expectedSpaceID;
      } catch {
        return false;
      }
    })
    .then(async response => {
      expect(response.ok(), `${method} HTTP response`).toBeTruthy();
      const body = (await response.json()) as RpcBody;
      expect(body.ret_info?.code, `${method} ret_info.code`).toBe(0);
      let requestBody: Record<string, unknown> = {};
      try {
        requestBody = response.request().postDataJSON() as Record<string, unknown>;
      } catch {
        requestBody = {};
      }
      return { body, requestBody } satisfies RpcExchange;
    });
}

function waitForMethod(page: Page, method: string, service = "storage") {
  return waitForMethodExchange(page, method, service).then(exchange => exchange.body);
}

async function login(page: Page) {
  const username = process.env.MOOX_REMOTE_USERNAME;
  const password = process.env.MOOX_REMOTE_PASSWORD;
  if (!username || !password) throw new Error("remote_playwright_credentials_missing");

  await page.goto("/#/login");
  await page.getByPlaceholder("请输入账号").fill(username);
  await page.getByPlaceholder("请输入密码").fill(password);
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page).not.toHaveURL(/#\/login(?:$|[?])/);
}

async function expectInfoTooltip(page: Page, buttonName: string, text: string) {
  const info = page.getByRole("button", { name: buttonName });
  await expect(info).toBeVisible();
  await info.hover();
  await expect(page.getByRole("tooltip").filter({ hasText: text })).toBeVisible();
  await info.focus();
  await expect(page.getByRole("tooltip").filter({ hasText: text })).toBeVisible();
}

async function expectNoRouteSurface(page: Page) {
  await expect(page.getByText("主存路由", { exact: true })).toHaveCount(0);
  await expect(page.getByText("路由管理", { exact: true })).toHaveCount(0);
}

async function openDataNodePage(page: Page, query: string) {
  const nodesResponse = waitForMethod(page, "ListDataNodes");
  await page.goto(`/#/ops/storage/nodes?tab=${query}`);
  const body = await nodesResponse;
  await expect(page.getByRole("heading", { name: "数据节点" })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "节点ID", exact: true })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "服务目标", exact: true })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Dataset", exact: true })).toBeVisible();
  await expectNoRouteSurface(page);
  expect(body.items?.length || 0, "deployed Storage must expose at least one DataNode").toBeGreaterThan(0);
  return body;
}

async function openFieldPage(page: Page, expected: { spaceID: string; spaceName: string; fieldID: string }) {
  const spacesResponse = waitForMethodExchange(page, "ListSpaces", "space");
  const groupsResponse = waitForMethodExchange(page, "ListFieldGroups");
  const fieldsResponse = waitForMethodExchange(page, "ListFields");
  await page.goto("/#/data/fields");
  const [spacesExchange, , initialFields] = await Promise.all([spacesResponse, groupsResponse, fieldsResponse]);
  expect(spacesExchange.requestBody.status, "global Space selector must request active Spaces").toBe("active");
  const spaces = spacesExchange.body.spaces || [];
  expect(
    spaces.some(item => item.space_id === expected.spaceID),
    `${expected.spaceID} must be available in the Space selector`
  ).toBeTruthy();

  const input = page.locator("input.arco-select-view-input").first();
  const selector = input.locator("..");
  await expect(selector).toBeVisible();
  let fieldsExchange = initialFields;
  if (initialFields.requestBody.space_id !== expected.spaceID) {
    const optionIndex = spaces.findIndex(space => space.space_id === expected.spaceID);
    expect(optionIndex, `${expected.spaceID} must have a selectable option`).toBeGreaterThanOrEqual(0);
    await selector.click();
    const options = page.locator(".arco-select-dropdown:visible .arco-select-option");
    await expect(options).toHaveCount(spaces.length);
    const option = options.nth(optionIndex);
    await expect(option).toBeVisible();
    await expect(option).toHaveText(expected.spaceName);
    const nextGroupsResponse = waitForMethodExchange(page, "ListFieldGroups");
    const nextFieldsResponse = waitForMethodExchange(page, "ListFields", "storage", expected.spaceID);
    await option.click();
    await Promise.all([nextGroupsResponse, nextFieldsResponse]);
    fieldsExchange = await nextFieldsResponse;
  }

  expect(fieldsExchange.requestBody.space_id).toBe(expected.spaceID);
  await expect(input).toHaveAttribute("placeholder", expected.spaceName);
  expect(
    fieldsExchange.body.fields?.some(item => item.field_id === expected.fieldID),
    `${expected.spaceID} must expose ${expected.fieldID}`
  ).toBeTruthy();
  await expect(page.getByRole("heading", { name: "字段管理" })).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: expected.fieldID })).toBeVisible();
  return fieldsExchange.body;
}

test("remote desktop covers DataNode details", async ({ page }) => {
  test.skip(process.env.MOOX_REMOTE_DEFAULT_SETUP === "1", "covered by the standard remote acceptance run");
  await login(page);
  const nodeBody = await openDataNodePage(page, "unknown");
  await expectInfoTooltip(page, "数据节点说明", "Dataset 直接绑定 DataNode");

  const firstNode = page.getByRole("row").nth(1);
  await expect(firstNode).toBeVisible();
  const firstNodeID = nodeBody.items?.[0]?.node?.node_id;
  if (firstNodeID) await expect(firstNode).toContainText(firstNodeID);
  for (const summary of nodeBody.items?.[0]?.datasets || []) {
    const label = summary.name || summary.dataset_id;
    if (label) await expect(firstNode).toContainText(label);
  }
  await firstNode.getByRole("button", { name: "查看" }).click();
  const detailModal = page.getByTestId("data-node-detail-modal");
  await expect(detailModal).toBeVisible();
  await expect(detailModal.getByRole("heading", { name: "Dataset" })).toBeVisible();
  await page.keyboard.press("Escape");
});

test("remote mobile keeps the DataNode workflow inside the viewport", async ({ page }) => {
  test.skip(process.env.MOOX_REMOTE_DEFAULT_SETUP === "1", "covered by the standard remote acceptance run");
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);

  await openDataNodePage(page, "routes");
  await expectInfoTooltip(page, "数据节点说明", "Dataset 直接绑定 DataNode");
  const nodeHeading = page.getByRole("heading", { name: "数据节点" });
  const nodeHeadingBox = await nodeHeading.boundingBox();
  expect(nodeHeadingBox).not.toBeNull();
  if (nodeHeadingBox) expect(nodeHeadingBox.x + nodeHeadingBox.width).toBeLessThanOrEqual(390);
  const nodeRow = page.getByRole("row").nth(1);
  await nodeRow.getByRole("button", { name: "查看" }).click();
  const detailModal = page.getByTestId("data-node-detail-modal");
  await expect(detailModal).toBeVisible();
  const modalBox = await detailModal.boundingBox();
  expect(modalBox?.width || 0).toBeLessThanOrEqual(391);
  await page.keyboard.press("Escape");
});

test("remote default setup exposes each business Space's Fields", async ({ page }) => {
  test.skip(process.env.MOOX_REMOTE_DEFAULT_SETUP !== "1", "default-setup acceptance is opt-in");
  await login(page);

  for (const expected of [
    { spaceID: "stockcn", spaceName: "A股市场", fieldID: "amount" },
    { spaceID: "crypto", spaceName: "加密货币市场", fieldID: "quote_volume" }
  ]) {
    await openFieldPage(page, expected);
  }
});
