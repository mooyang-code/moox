import { expect, test, type Locator, type Page, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

type JsonRecord = Record<string, unknown>;

type RpcCall = {
  method: string;
  body: JsonRecord;
};

const ok = (data: JsonRecord = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const fail = (code: number, msg: string) => ({ ret_info: { code, msg } });

const datasetNames = [
  "超长行情数据集A",
  "跨市场行情数据集B",
  "日内成交明细数据集C",
  ...Array.from({ length: 9 }, (_, index) => `分页测试数据集${index + 1}`)
];

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
  return { calls, nodes };
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
    const detailModal = page.getByTestId("data-node-detail-modal");
    await expect(detailModal).toBeVisible();
    await expect(detailModal).toContainText("Space");
    await expect(detailModal).toContainText(datasetNames[2]);
    const secondDetailPage = detailModal.locator("li.arco-pagination-item").filter({ hasText: "2" });
    await expect(secondDetailPage).toBeVisible();
    await expect(detailModal).not.toContainText(datasetNames.at(-1) || "");
    await secondDetailPage.click();
    await expect(detailModal).toContainText(datasetNames.at(-1) || "");
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
    const detailModal = page.getByTestId("data-node-detail-modal");
    await expect(detailModal).toBeVisible();
    await expectInViewport(page, detailModal);
    await page.keyboard.press("Escape");

    await nodeRow.getByRole("button", { name: "编辑" }).click();
    const editDialog = page.getByTestId("data-node-edit-modal");
    await expect(editDialog).toBeVisible();
    await expectInViewport(page, editDialog);
    await page.screenshot({ path: testInfo.outputPath("storage-datanode-mobile.png"), fullPage: true });
  });
});
