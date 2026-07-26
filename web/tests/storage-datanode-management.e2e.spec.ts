import { expect, test, type Locator, type Page, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

type JsonRecord = Record<string, unknown>;

type RpcCall = {
  method: string;
  body: JsonRecord;
};

type Dataset = {
  space_id: string;
  dataset_id: string;
  data_source_id: string;
  name: string;
  description: string;
  data_kind: string;
  freqs: string[];
  keep_duration: string;
  status: string;
  data_node_id: string;
  binding_locked: boolean;
  revision: number;
  attributes: JsonRecord;
  updated_at: string;
};

const ok = (data: JsonRecord = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const fail = (code: number, msg: string) => ({ ret_info: { code, msg } });

const datasetNames = ["超长行情数据集A", "跨市场行情数据集B", "日内成交明细数据集C"];

function makeDataset(overrides: Partial<Dataset> = {}): Dataset {
  return {
    space_id: "space-a",
    dataset_id: "dataset-a",
    data_source_id: "source-a",
    name: "现货行情",
    description: "浏览器 E2E 数据集",
    data_kind: "DATA_KIND_TIME_SERIES",
    freqs: ["1m"],
    keep_duration: "30d",
    status: "disabled",
    data_node_id: "node-a",
    binding_locked: false,
    revision: 7,
    attributes: {},
    updated_at: "2026-07-22T10:00:00Z",
    ...overrides
  };
}

function makeFixture() {
  const calls: RpcCall[] = [];
  const nodes = [
    {
      node: {
        node_id: "node-a",
        name: "行情节点 A",
        service_target: "trpc://storage-a:20200",
        status: "active",
        updated_at: "2026-07-22T10:00:00Z"
      },
      datasets: datasetNames.map((name, index) => ({
        space_id: "space-a",
        dataset_id: `dataset-${index + 1}`,
        name,
        data_kind: "DATA_KIND_TIME_SERIES",
        keep_duration: "30d",
        status: "active"
      }))
    },
    {
      node: {
        node_id: "node-b",
        name: "备用节点 B",
        service_target: "trpc://storage-b:20200",
        status: "disabled",
        updated_at: "2026-07-22T10:01:00Z"
      },
      datasets: []
    }
  ];
  const datasets: Dataset[] = [
    makeDataset(),
    makeDataset({
      dataset_id: "dataset-rebind",
      name: "待更换节点",
      revision: 12
    }),
    makeDataset({
      dataset_id: "dataset-locked",
      name: "已锁定数据集",
      binding_locked: true,
      revision: 11
    })
  ];

  return { calls, nodes, datasets };
}

function methodOf(route: Route) {
  const pathname = new URL(route.request().url()).pathname;
  return pathname.split("/").filter(Boolean).at(-1) || "";
}

function bodyOf(route: Route): JsonRecord {
  try {
    const body = route.request().postDataJSON();
    return body && typeof body === "object" ? (body as JsonRecord) : {};
  } catch {
    return {};
  }
}

async function installGatewayFixtures(page: Page, fixture: ReturnType<typeof makeFixture>) {
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?.*)?$/, async route => {
    const method = methodOf(route);
    const body = bodyOf(route);
    fixture.calls.push({ method, body });

    if (method === "GetUserInfo") {
      return route.fulfill({
        json: ok({ user_info: { user_id: "e2e", username: "reviewer", nickname: "Reviewer", role: 3, status: 1 } })
      });
    }
    if (method === "ListSpaces") {
      return route.fulfill({
        json: ok({
          spaces: [{ space_id: "space-a", name: "A 股市场", owner: "e2e", status: "active" }],
          page_result: { page: 1, size: 200, total: 1, has_more: false }
        })
      });
    }
    if (method === "ListDataNodes") {
      return route.fulfill({
        json: ok({
          items: fixture.nodes,
          page_result: { page: 1, size: 500, total: fixture.nodes.length, has_more: false }
        })
      });
    }
    if (method === "ListDataSources") {
      return route.fulfill({
        json: ok({
          data_sources: [{ data_source_id: "source-a", name: "行情源", status: "active" }],
          page_result: { page: 1, size: 200, total: 1, has_more: false }
        })
      });
    }
    if (method === "ListDatasets") {
      return route.fulfill({
        json: ok({
          datasets: fixture.datasets,
          page_result: { page: 1, size: 500, total: fixture.datasets.length, has_more: false }
        })
      });
    }
    if (method === "UpdateDataNode") {
      const node = fixture.nodes.find(item => item.node.node_id === body.node_id);
      if (node) {
        node.node.name = String(body.name || node.node.name);
        node.node.status = String(body.status || node.node.status);
      }
      return route.fulfill({ json: ok({ node: node?.node }) });
    }
    if (method === "DeleteDataNode") {
      return route.fulfill({ status: 200, json: fail(3, "节点仍有关联 Dataset") });
    }
    if (method === "CreateDataset") {
      const payload = body.dataset as Dataset;
      const created = makeDataset({
        ...payload,
        status: "disabled",
        binding_locked: false,
        revision: 1,
        updated_at: "2026-07-22T10:02:00Z"
      });
      fixture.datasets.push(created);
      return route.fulfill({ json: ok({ dataset: created }) });
    }
    if (method === "CheckDatasetActivation") {
      const datasetId = String(body.dataset_id || "");
      const dataset = fixture.datasets.find(item => item.dataset_id === datasetId) || fixture.datasets[0];
      return route.fulfill({
        json: ok({
          dataset_revision: String(dataset.revision),
          checks: [
            { check_id: "dataset_state", ready: true, summary: "Dataset 状态允许激活" },
            { check_id: "dataset_schema", ready: true, summary: "Schema 合法" },
            { check_id: "keep_duration", ready: true, summary: "保留时长合法" },
            { check_id: "data_node", ready: true, summary: "DataNode 可用" },
            { check_id: "service_target", ready: true, summary: "服务目标可访问" },
            { check_id: "data_node_readiness", ready: true, summary: "DataNode 已就绪" },
            { check_id: "data_node_identity", ready: true, summary: "节点身份一致" }
          ],
          ready: true
        })
      });
    }
    if (method === "ActivateDataset") {
      const datasetId = String(body.dataset_id || "");
      const dataset = fixture.datasets.find(item => item.dataset_id === datasetId) || fixture.datasets[0];
      const activated = { ...dataset, status: "active", binding_locked: true, revision: dataset.revision + 1 };
      const index = fixture.datasets.findIndex(item => item.dataset_id === dataset.dataset_id);
      fixture.datasets.splice(index, 1, activated);
      return route.fulfill({ json: ok({ dataset: activated, checks: [] }) });
    }
    if (method === "RebindDatasetDataNode") {
      const datasetId = String(body.dataset_id || "");
      const dataset = fixture.datasets.find(item => item.dataset_id === datasetId) || fixture.datasets[0];
      const rebound = {
        ...dataset,
        data_node_id: String(body.data_node_id || dataset.data_node_id),
        revision: dataset.revision + 1
      };
      const index = fixture.datasets.findIndex(item => item.dataset_id === dataset.dataset_id);
      fixture.datasets.splice(index, 1, rebound);
      return route.fulfill({ json: ok({ dataset: rebound }) });
    }

    return route.fulfill({ json: ok() });
  });
}

async function preparePage(page: Page, fixture: ReturnType<typeof makeFixture>, spaceId = "space-a") {
  await installE2ESession(page, spaceId);
  await installGatewayFixtures(page, fixture);
}

async function expectInViewport(page: Page, locator: Locator) {
  const box = await locator.boundingBox();
  expect(box).not.toBeNull();
  if (!box) return;
  const viewport = page.viewportSize();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport?.width || 0);
  expect(box.y + box.height).toBeLessThanOrEqual(viewport?.height || 0);
}

async function expectButtonsDoNotOverlap(locator: Locator) {
  const boxes = (await Promise.all((await locator.getByRole("button").all()).map(button => button.boundingBox()))).filter(
    Boolean
  ) as {
    x: number;
    y: number;
    width: number;
    height: number;
  }[];
  for (let index = 0; index < boxes.length; index += 1) {
    for (let next = index + 1; next < boxes.length; next += 1) {
      const left = boxes[index];
      const right = boxes[next];
      const overlaps =
        left.x < right.x + right.width &&
        left.x + left.width > right.x &&
        left.y < right.y + right.height &&
        left.y + left.height > right.y;
      expect(overlaps).toBe(false);
    }
  }
}

function callsFor(fixture: ReturnType<typeof makeFixture>, method: string) {
  return fixture.calls.filter(call => call.method === method);
}

test.describe("DataNode management browser workflows", () => {
  test("covers the desktop node workflow and direct Dataset detail data", async ({ page }, testInfo) => {
    const fixture = makeFixture();
    await preparePage(page, fixture);
    await page.goto("/#/ops/storage/nodes?tab=unknown");

    await expect(page.getByRole("heading", { name: "数据节点" })).toBeVisible();
    await expect(page.getByText(datasetNames[0], { exact: true })).toBeVisible();
    await expect(page.getByText(datasetNames[1], { exact: true })).toBeVisible();

    const infoButton = page.getByRole("button", { name: "数据节点说明" });
    await infoButton.hover();
    const infoTooltip = page.getByRole("tooltip").filter({ hasText: "节点身份和服务目标由部署流程拥有" });
    await expect(infoTooltip).toBeVisible();
    await expectInViewport(page, infoTooltip);
    await infoButton.focus();
    await expect(infoTooltip).toBeVisible();

    const nodeRow = page.getByRole("row", { name: /node-a/ });
    const tagBoxes = await Promise.all(datasetNames.map(name => page.getByText(name, { exact: true }).boundingBox()));
    const tagRows = new Set(tagBoxes.filter(Boolean).map(box => Math.round((box as { y: number }).y)));
    expect(tagRows.size).toBeGreaterThan(1);
    const serviceBox = await nodeRow.getByText("trpc://storage-a:20200", { exact: true }).boundingBox();
    const actionCell = nodeRow.getByRole("cell").last();
    const actionBox = await actionCell.boundingBox();
    expect(serviceBox).not.toBeNull();
    expect(actionBox).not.toBeNull();
    if (serviceBox && actionBox) expect(serviceBox.x + serviceBox.width).toBeLessThanOrEqual(actionBox.x + 2);

    const otherActionCell = page
      .getByRole("row", { name: /node-b/ })
      .getByRole("cell")
      .last();
    const otherActionBox = await otherActionCell.boundingBox();
    expect(actionBox).not.toBeNull();
    expect(otherActionBox).not.toBeNull();
    if (actionBox && otherActionBox) expect(Math.abs(actionBox.width - otherActionBox.width)).toBeLessThanOrEqual(2);

    await page.screenshot({ path: testInfo.outputPath("storage-datanode-desktop.png"), fullPage: true });

    await nodeRow.getByRole("button", { name: "查看" }).click();
    const detailDrawer = page.getByTestId("data-node-detail-drawer");
    await expect(detailDrawer).toBeVisible();
    await expect(detailDrawer).toContainText("Space");
    await expect(detailDrawer).toContainText(datasetNames[2]);
    expect(callsFor(fixture, "ListDataNodes")).toHaveLength(1);
    expect(callsFor(fixture, "ListDatasets")).toHaveLength(0);
    expect(callsFor(fixture, "GetDataNode")).toHaveLength(0);
    await page.keyboard.press("Escape");

    await nodeRow.getByRole("button", { name: "编辑" }).click();
    const editDialog = page.getByTestId("data-node-edit-modal");
    await expect(editDialog).toBeVisible();
    await editDialog.getByPlaceholder("节点名称").fill("行情节点 A（管理员）");
    await editDialog.getByRole("button", { name: "确定" }).click();
    await expect(editDialog).toBeHidden();
    const updateCall = callsFor(fixture, "UpdateDataNode").at(-1);
    expect(updateCall?.body).toMatchObject({ node_id: "node-a", name: "行情节点 A（管理员）", status: "active" });
    expect(updateCall?.body).not.toHaveProperty("service_target");
    expect(updateCall?.body).not.toHaveProperty("node");

    const disabledRow = page.getByRole("row", { name: /node-b/ });
    await disabledRow.getByRole("button", { name: "删除数据节点" }).click();
    await expect(page.getByText("节点仍有关联 Dataset", { exact: true })).toBeVisible();
  });

  test("keeps the node surface usable at mobile width", async ({ page }, testInfo) => {
    const fixture = makeFixture();
    await page.setViewportSize({ width: 390, height: 844 });
    await preparePage(page, fixture);
    await page.goto("/#/ops/storage/nodes?tab=routes");

    await expect(page.getByRole("heading", { name: "数据节点" })).toBeVisible();
    const infoButton = page.getByRole("button", { name: "数据节点说明" });
    await infoButton.focus();
    const infoTooltip = page.getByRole("tooltip").filter({ hasText: "Dataset 直接绑定 DataNode" });
    await expect(infoTooltip).toBeVisible();
    await expectInViewport(page, infoTooltip);

    const nodeRow = page.getByRole("row", { name: /node-a/ });
    const tagBoxes = await Promise.all(datasetNames.map(name => page.getByText(name, { exact: true }).boundingBox()));
    const tagRows = new Set(tagBoxes.filter(Boolean).map(box => Math.round((box as { y: number }).y)));
    expect(tagRows.size).toBeGreaterThan(1);
    const actionCell = nodeRow.getByRole("cell").last();
    const actionBox = await actionCell.boundingBox();
    expect(actionBox?.width || 0).toBeGreaterThanOrEqual(170);
    await expectButtonsDoNotOverlap(actionCell);

    await nodeRow.getByRole("button", { name: "查看" }).click();
    const detailDrawer = page.getByTestId("data-node-detail-drawer");
    await expect(detailDrawer).toBeVisible();
    await expectInViewport(page, detailDrawer);
    await page.keyboard.press("Escape");

    await nodeRow.getByRole("button", { name: "编辑" }).click();
    const editDialog = page.getByTestId("data-node-edit-modal");
    await expect(editDialog).toBeVisible();
    await expectInViewport(page, editDialog);
    await page.screenshot({ path: testInfo.outputPath("storage-datanode-mobile.png"), fullPage: true });
  });

  test("covers Dataset creation, check-before-activate, revision CAS, and binding rules", async ({ page }, testInfo) => {
    const fixture = makeFixture();
    fixture.nodes[1].node.status = "active";
    await preparePage(page, fixture);
    await page.goto("/#/collector/data-management?tab=datasets");

    await expect(page.getByRole("heading", { name: "数据集" })).toBeVisible();
    const lockedRow = page.getByRole("row", { name: /dataset-locked/ });
    await expect(lockedRow.getByRole("button", { name: "更换节点" })).toHaveCount(0);

    await page.getByRole("button", { name: "新增数据集" }).click();
    const createDialog = page.getByTestId("create-dataset-modal");
    await expect(createDialog).toBeVisible();
    await createDialog.getByPlaceholder("例如 kline").fill("dataset_created");
    await createDialog.getByPlaceholder("选择或输入来源ID").click();
    await page.getByText("行情源 (source-a)", { exact: true }).click();
    await createDialog.getByPlaceholder("例如 现货K线").fill("新建行情");
    await createDialog.getByPlaceholder("0 表示永久保存，例如 24h").fill("7d");
    const dataNodeSelect = createDialog.getByPlaceholder("选择 active DataNode");
    await dataNodeSelect.click();
    await dataNodeSelect.press("ArrowDown");
    await dataNodeSelect.press("Enter");
    await createDialog.getByRole("button", { name: "确定" }).click();
    await expect(createDialog).toBeHidden();
    const createCall = callsFor(fixture, "CreateDataset").at(-1);
    const createdPayload = createCall?.body.dataset as JsonRecord | undefined;
    expect(createdPayload).toMatchObject({
      space_id: "space-a",
      dataset_id: "dataset_created",
      data_source_id: "source-a",
      name: "新建行情",
      keep_duration: "7d",
      data_node_id: "node-b",
      status: "disabled"
    });
    expect(createdPayload).not.toHaveProperty("binding_locked");
    expect(createdPayload).not.toHaveProperty("revision");

    const datasetRow = page.getByRole("row", { name: /dataset-a/ });
    await datasetRow.getByRole("button", { name: "激活" }).click();
    const checkDialog = page.getByTestId("dataset-activation-modal");
    await expect(checkDialog).toBeVisible();
    await expect(checkDialog.getByText("dataset_state", { exact: true })).toBeVisible();
    expect(callsFor(fixture, "ActivateDataset")).toHaveLength(0);
    const checkCall = callsFor(fixture, "CheckDatasetActivation").at(-1);
    expect(checkCall?.body).toMatchObject({ space_id: "space-a", dataset_id: "dataset-a" });
    await page.screenshot({ path: testInfo.outputPath("storage-dataset-activation.png"), fullPage: true });

    await checkDialog.getByRole("button", { name: "确定" }).click();
    await expect(checkDialog).toBeHidden();
    const activateCall = callsFor(fixture, "ActivateDataset").at(-1);
    expect(String(activateCall?.body.expected_revision)).toBe("7");
    const checkIndex = fixture.calls.findIndex(call => call.method === "CheckDatasetActivation");
    const activateIndex = fixture.calls.findIndex(call => call.method === "ActivateDataset");
    expect(checkIndex).toBeGreaterThanOrEqual(0);
    expect(activateIndex).toBeGreaterThan(checkIndex);
    await expect(datasetRow.getByRole("button", { name: "激活" })).toHaveCount(0);

    const rebindRow = page.getByRole("row", { name: /dataset-rebind/ });
    await rebindRow.getByRole("button", { name: "更换节点" }).click();
    const rebindDialog = page.getByTestId("dataset-rebind-modal");
    await expect(rebindDialog).toBeVisible();
    await rebindDialog.getByRole("button", { name: "确定" }).click();
    await expect(rebindDialog).toBeHidden();
    const rebindCall = callsFor(fixture, "RebindDatasetDataNode").at(-1);
    expect(rebindCall?.body).toMatchObject({ space_id: "space-a", dataset_id: "dataset-rebind", data_node_id: "node-b" });
    expect(String(rebindCall?.body.expected_revision)).toBe("12");
  });

  test("keeps Dataset dialogs and the binding tooltip inside a mobile viewport", async ({ page }) => {
    const fixture = makeFixture();
    fixture.nodes[1].node.status = "active";
    await page.setViewportSize({ width: 390, height: 844 });
    await preparePage(page, fixture);
    await page.goto("/#/collector/data-management?tab=datasets");

    const infoButton = page.getByRole("button", { name: "数据集绑定规则说明" });
    await infoButton.focus();
    const infoTooltip = page.getByRole("tooltip").filter({ hasText: "数据集必须绑定一个 DataNode" });
    await expect(infoTooltip).toBeVisible();
    await expectInViewport(page, infoTooltip);

    const datasetRow = page.getByRole("row", { name: /dataset-a/ });
    await datasetRow.getByRole("button", { name: "激活" }).click();
    const activationDialog = page.getByTestId("dataset-activation-modal");
    await expect(activationDialog).toBeVisible();
    await expectInViewport(page, activationDialog);
  });
});
