import { expect, test, type Page } from "@playwright/test";

test.skip(process.env.MOOX_REMOTE_PLAYWRIGHT !== "1", "remote-only Storage browser verification");

test.describe.configure({ mode: "serial" });

type BrowserFixture = {
  namespace: string;
  space_id: string;
  space_name: string;
  data_source_id: string;
  dataset_id: string;
  dataset_name: string;
};

type RpcBody = {
  ret_info?: { code?: number | string; msg?: string };
  items?: Array<{ node?: { node_id?: string }; datasets?: Array<{ dataset_id?: string; name?: string }> }>;
  spaces?: Array<{ space_id?: string; name?: string }>;
  datasets?: Array<{ dataset_id?: string; status?: string; binding_locked?: boolean; name?: string }>;
  dataset?: { dataset_id?: string; status?: string; binding_locked?: boolean };
  fields?: Array<{ field_id?: string; name?: string }>;
};

type RpcExchange = {
  body: RpcBody;
  requestBody: Record<string, unknown>;
};

function browserFixture(): BrowserFixture {
  const raw = process.env.MOOX_REMOTE_STORAGE_FIXTURE;
  if (!raw) throw new Error("remote_storage_fixture_missing");
  let fixture: BrowserFixture;
  try {
    fixture = JSON.parse(raw) as BrowserFixture;
  } catch {
    throw new Error("remote_storage_fixture_invalid");
  }
  for (const key of ["namespace", "space_id", "space_name", "data_source_id", "dataset_id", "dataset_name"] as const) {
    if (!fixture[key]) throw new Error(`remote_storage_fixture_${key}_missing`);
  }
  return fixture;
}

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

async function openDatasetPage(page: Page, fixture: BrowserFixture) {
  const spacesResponse = waitForMethod(page, "ListSpaces", "space");
  const nodesResponse = waitForMethod(page, "ListDataNodes");
  const datasetsResponse = waitForMethodExchange(page, "ListDatasets");
  await page.goto("/#/collector/data-management?tab=datasets");
  const [spaces, initialNodes, initialDatasets] = await Promise.all([spacesResponse, nodesResponse, datasetsResponse]);
  await expect(page.getByRole("heading", { name: "数据集" })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "数据集ID", exact: true })).toBeVisible();
  expect(
    spaces.spaces?.some(item => item.space_id === fixture.space_id),
    "browser fixture Space must be listed by Admin"
  ).toBeTruthy();
  const { nodes, datasets } = await selectBusinessSpace(
    page,
    { spaceID: fixture.space_id, spaceName: fixture.space_name },
    spaces.spaces || [],
    initialNodes,
    initialDatasets
  );
  expect(nodes.items?.length || 0, "Dataset page must resolve DataNodes").toBeGreaterThan(0);
  expect(
    datasets.datasets?.some(item => item.dataset_id === fixture.dataset_id),
    "browser fixture Dataset must be listed"
  ).toBeTruthy();
  await expectInfoTooltip(page, "数据集绑定规则说明", "数据集必须绑定一个 DataNode");
  return datasets;
}

function firstDatasetRow(page: Page, datasetId: string) {
  return page.getByRole("row").filter({ hasText: datasetId }).first();
}

async function selectBusinessSpace(
  page: Page,
  expected: { spaceID: string; spaceName: string },
  spaces: NonNullable<RpcBody["spaces"]>,
  initialNodes: RpcBody,
  initialDatasets: RpcExchange
) {
  const input = page.locator("input.arco-select-view-input").first();
  const selector = input.locator("..");
  await expect(selector).toBeVisible();
  if (initialDatasets.requestBody.space_id === expected.spaceID) {
    return { nodes: initialNodes, datasets: initialDatasets.body };
  }

  const optionIndex = spaces.findIndex(space => space.space_id === expected.spaceID);
  expect(optionIndex, `${expected.spaceID} must have a selectable option`).toBeGreaterThanOrEqual(0);
  await selector.click();
  const options = page.locator(".arco-select-dropdown:visible .arco-select-option");
  await expect(options).toHaveCount(spaces.length);
  const option = options.nth(optionIndex);
  await expect(option).toBeVisible();
  await expect(option).toHaveText(expected.spaceName);
  const nodesResponse = waitForMethod(page, "ListDataNodes");
  const datasetsResponse = waitForMethodExchange(page, "ListDatasets", "storage", expected.spaceID);
  await option.click();
  const [nodes, datasetsExchange] = await Promise.all([nodesResponse, datasetsResponse]);
  expect(datasetsExchange.requestBody.space_id).toBe(expected.spaceID);
  await expect(input).toHaveAttribute("placeholder", expected.spaceName);
  return { nodes, datasets: datasetsExchange.body };
}

test("remote desktop covers DataNode details and Dataset lifecycle UI", async ({ page }) => {
  test.skip(process.env.MOOX_REMOTE_DEFAULT_SETUP === "1", "covered by the standard remote acceptance run");
  await login(page);
  const fixture = browserFixture();
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
  const detailDrawer = page.getByTestId("data-node-detail-drawer");
  await expect(detailDrawer).toBeVisible();
  await expect(detailDrawer.getByRole("heading", { name: "Dataset" })).toBeVisible();
  await page.keyboard.press("Escape");

  await openDatasetPage(page, fixture);
  const datasetRow = firstDatasetRow(page, fixture.dataset_id);
  await expect(datasetRow).toBeVisible();
  await expect(datasetRow).toContainText(fixture.dataset_name);
  await datasetRow.getByRole("button", { name: "列/对象" }).click();
  await expect(page.getByText(`数据集配置：${fixture.dataset_id}`, { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");

  const activationRow = firstDatasetRow(page, fixture.dataset_id);
  const checkResponse = waitForMethod(page, "CheckDatasetActivation");
  await activationRow.getByRole("button", { name: "激活" }).click();
  await expect(page.getByTestId("dataset-activation-modal")).toBeVisible();
  const checkBody = await checkResponse;
  expect(checkBody.ret_info?.code).toBe(0);
  await expect(page.getByTestId("dataset-activation-modal").getByText("dataset_state", { exact: true })).toBeVisible();
  const activateResponse = waitForMethod(page, "ActivateDataset");
  await page.getByTestId("dataset-activation-modal").getByRole("button", { name: "确定" }).click();
  const activateBody = await activateResponse;
  expect(activateBody.dataset?.dataset_id).toBe(fixture.dataset_id);
  expect(activateBody.dataset?.status).toBe("active");
  expect(activateBody.dataset?.binding_locked).toBeTruthy();
  await expect(page.getByTestId("dataset-activation-modal")).toBeHidden();
  await expect(activationRow).toContainText("active");
  await page.keyboard.press("Escape");
});

test("remote mobile keeps DataNode and Dataset workflows inside the viewport", async ({ page }) => {
  test.skip(process.env.MOOX_REMOTE_DEFAULT_SETUP === "1", "covered by the standard remote acceptance run");
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  const fixture = browserFixture();

  await openDataNodePage(page, "routes");
  await expectInfoTooltip(page, "数据节点说明", "Dataset 直接绑定 DataNode");
  const nodeHeading = page.getByRole("heading", { name: "数据节点" });
  const nodeHeadingBox = await nodeHeading.boundingBox();
  expect(nodeHeadingBox).not.toBeNull();
  if (nodeHeadingBox) expect(nodeHeadingBox.x + nodeHeadingBox.width).toBeLessThanOrEqual(390);
  const nodeRow = page.getByRole("row").nth(1);
  await nodeRow.getByRole("button", { name: "查看" }).click();
  const detailDrawer = page.getByTestId("data-node-detail-drawer");
  await expect(detailDrawer).toBeVisible();
  const drawerBox = await detailDrawer.boundingBox();
  expect(drawerBox?.width || 0).toBeLessThanOrEqual(391);
  await page.keyboard.press("Escape");

  await openDatasetPage(page, fixture);
  const datasetRow = firstDatasetRow(page, fixture.dataset_id);
  await datasetRow.getByRole("button", { name: "列/对象" }).click();
  await expect(page.getByText(`数据集配置：${fixture.dataset_id}`, { exact: true })).toBeVisible();
  const manageBox = await page.locator(".arco-drawer").filter({ hasText: fixture.dataset_id }).boundingBox();
  expect(manageBox?.width || 0).toBeLessThanOrEqual(391);
  await page.keyboard.press("Escape");

  await expect(datasetRow).toContainText("active");
  await expect(datasetRow).toContainText("已锁定");
});

test("remote default setup exposes each business Space with Datasets and Fields", async ({ page }) => {
  test.skip(process.env.MOOX_REMOTE_DEFAULT_SETUP !== "1", "default-setup acceptance is opt-in");
  await login(page);

  for (const expected of [
    { spaceID: "stock_cn", spaceName: "A股市场", datasetID: "stock_kline", fieldID: "amount" },
    { spaceID: "crypto", spaceName: "加密货币市场", datasetID: "spot_kline_1h", fieldID: "quote_volume" }
  ]) {
    const spacesResponse = waitForMethod(page, "ListSpaces", "space");
    const nodesResponse = waitForMethod(page, "ListDataNodes");
    const datasetsResponse = waitForMethodExchange(page, "ListDatasets");
    await page.goto("/#/collector/data-management?tab=datasets");
    const [spaces, initialNodes, initialDatasets] = await Promise.all([spacesResponse, nodesResponse, datasetsResponse]);
    expect(
      spaces.spaces?.some(item => item.space_id === expected.spaceID && item.name === expected.spaceName),
      `${expected.spaceID} must be available in the Space selector`
    ).toBeTruthy();
    const { nodes, datasets } = await selectBusinessSpace(page, expected, spaces.spaces || [], initialNodes, initialDatasets);
    expect(nodes.items?.length || 0, `${expected.spaceID} must resolve a DataNode`).toBeGreaterThan(0);
    expect(
      datasets.datasets?.some(item => item.dataset_id === expected.datasetID),
      `${expected.spaceID} must expose ${expected.datasetID}`
    ).toBeTruthy();
    await expect(page.locator("input.arco-select-view-input").first()).toHaveAttribute("placeholder", expected.spaceName);
    await expect(page.getByRole("row").filter({ hasText: expected.datasetID })).toBeVisible();

    const groupsResponse = waitForMethod(page, "ListFieldGroups");
    const fieldsResponse = waitForMethod(page, "ListFields");
    await page.goto("/#/data/fields");
    await Promise.all([groupsResponse, fieldsResponse]);
    const fields = await fieldsResponse;
    expect(
      fields.fields?.some(item => item.field_id === expected.fieldID),
      `${expected.spaceID} must expose ${expected.fieldID}`
    ).toBeTruthy();
    await expect(page.getByRole("heading", { name: "字段管理" })).toBeVisible();
    await expect(page.locator("input.arco-select-view-input").first()).toHaveAttribute("placeholder", expected.spaceName);
    await expect(page.getByRole("row").filter({ hasText: expected.fieldID })).toBeVisible();
  }
});
