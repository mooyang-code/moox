import { expect, test, type Page } from "@playwright/test";

test.skip(process.env.MOOX_REMOTE_PLAYWRIGHT !== "1", "remote-only Storage browser verification");

type RpcBody = {
  ret_info?: { code?: number | string; msg?: string };
  items?: Array<{ node?: { node_id?: string }; datasets?: Array<{ dataset_id?: string; name?: string }> }>;
  spaces?: Array<{ space_id?: string }>;
  datasets?: Array<{ dataset_id?: string; status?: string; binding_locked?: boolean }>;
};

function waitForMethod(page: Page, method: string, service = "storage") {
  return page
    .waitForResponse(response => {
      if (response.request().method() !== "POST") return false;
      return new URL(response.url()).pathname === `/api/admin/${service}/${method}`;
    })
    .then(async response => {
      expect(response.ok(), `${method} HTTP response`).toBeTruthy();
      const body = (await response.json()) as RpcBody;
      expect(body.ret_info?.code, `${method} ret_info.code`).toBe(0);
      return body;
    });
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
  await expect(page.getByText("节点ID", { exact: true })).toBeVisible();
  await expect(page.getByText("服务目标", { exact: true })).toBeVisible();
  await expect(page.getByText("Dataset", { exact: true })).toBeVisible();
  await expectNoRouteSurface(page);
  expect(body.items?.length || 0, "deployed Storage must expose at least one DataNode").toBeGreaterThan(0);
  return body;
}

async function openDatasetPage(page: Page) {
  const spacesResponse = waitForMethod(page, "ListSpaces", "space");
  const nodesResponse = waitForMethod(page, "ListDataNodes");
  const datasetsResponse = waitForMethod(page, "ListDatasets");
  await page.goto("/#/data/datasets");
  const [spaces, nodes, datasets] = await Promise.all([spacesResponse, nodesResponse, datasetsResponse]);
  await expect(page.getByRole("heading", { name: "数据集" })).toBeVisible();
  await expect(page.getByText("数据集ID", { exact: true })).toBeVisible();
  expect(spaces.spaces?.length || 0, "deployed Storage must expose a business Space").toBeGreaterThan(0);
  expect(nodes.items?.length || 0, "Dataset page must resolve DataNodes").toBeGreaterThan(0);
  expect(datasets.datasets?.length || 0, "deployed Storage must expose a Dataset").toBeGreaterThan(0);
  await expectInfoTooltip(page, "数据集绑定规则说明", "数据集必须绑定一个 DataNode");
  return datasets;
}

function firstDatasetRow(page: Page, datasetId: string) {
  return page.getByRole("row").filter({ hasText: datasetId }).first();
}

test("remote desktop covers DataNode details and Dataset lifecycle UI", async ({ page }) => {
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
  const detailDrawer = page.getByTestId("data-node-detail-drawer");
  await expect(detailDrawer).toBeVisible();
  await expect(detailDrawer.getByRole("heading", { name: "Dataset" })).toBeVisible();
  await page.keyboard.press("Escape");

  const datasets = await openDatasetPage(page);
  const firstDataset = datasets.datasets?.[0];
  if (!firstDataset?.dataset_id) throw new Error("remote_dataset_id_missing");
  const datasetRow = firstDatasetRow(page, firstDataset.dataset_id);
  await expect(datasetRow).toBeVisible();
  await datasetRow.getByRole("button", { name: "列/对象" }).click();
  await expect(page.getByText(`数据集配置：${firstDataset.dataset_id}`, { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");

  const disabledDataset = datasets.datasets?.find(item => item.status === "disabled" && item.dataset_id);
  expect(disabledDataset?.dataset_id, "remote UI must expose a disabled Dataset for activation verification").toBeTruthy();
  const activationRow = firstDatasetRow(page, disabledDataset?.dataset_id || "");
  const checkResponse = waitForMethod(page, "CheckDatasetActivation");
  await activationRow.getByRole("button", { name: "激活" }).click();
  await expect(page.getByTestId("dataset-activation-modal")).toBeVisible();
  await checkResponse;
  await expect(page.getByTestId("dataset-activation-modal").getByText("dataset_state", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
});

test("remote mobile keeps DataNode and Dataset workflows inside the viewport", async ({ page }) => {
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
  const detailDrawer = page.getByTestId("data-node-detail-drawer");
  await expect(detailDrawer).toBeVisible();
  const drawerBox = await detailDrawer.boundingBox();
  expect(drawerBox?.width || 0).toBeLessThanOrEqual(391);
  await page.keyboard.press("Escape");

  const datasets = await openDatasetPage(page);
  const firstDataset = datasets.datasets?.[0];
  if (!firstDataset?.dataset_id) throw new Error("remote_dataset_id_missing");
  const datasetRow = firstDatasetRow(page, firstDataset.dataset_id);
  await datasetRow.getByRole("button", { name: "列/对象" }).click();
  await expect(page.getByText(`数据集配置：${firstDataset.dataset_id}`, { exact: true })).toBeVisible();
  const manageBox = await page.locator(".arco-drawer").filter({ hasText: firstDataset.dataset_id }).boundingBox();
  expect(manageBox?.width || 0).toBeLessThanOrEqual(391);
  await page.keyboard.press("Escape");

  const disabledDataset = datasets.datasets?.find(item => item.status === "disabled" && item.dataset_id);
  expect(disabledDataset?.dataset_id, "remote UI must expose a disabled Dataset for activation verification").toBeTruthy();
  const activationRow = firstDatasetRow(page, disabledDataset?.dataset_id || "");
  const checkResponse = waitForMethod(page, "CheckDatasetActivation");
  await activationRow.getByRole("button", { name: "激活" }).click();
  const activationModal = page.getByTestId("dataset-activation-modal");
  await expect(activationModal).toBeVisible();
  await checkResponse;
  const activationBox = await activationModal.boundingBox();
  expect(activationBox?.width || 0).toBeLessThanOrEqual(391);
  await page.keyboard.press("Escape");
});
