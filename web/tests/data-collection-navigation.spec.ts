import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });

async function mockGateway(route: Route) {
  const method = route.request().url().split("/").pop();
  if (method === "GetUserInfo") {
    return route.fulfill({
      json: ok({ user_info: { user_id: "e2e", username: "reviewer", nickname: "Reviewer", role: 3, status: 1 } })
    });
  }
  if (method === "ListSpaces") {
    return route.fulfill({
      json: ok({
        spaces: [{ space_id: "crypto", name: "加密货币", owner: "e2e", status: "active" }],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListDatasets") {
    return route.fulfill({
      json: ok({
        datasets: [
          {
            space_id: "crypto",
            dataset_id: "dataset_binance_spot_kline_1m",
            name: "现货K线",
            data_kind: "DATA_KIND_TIME_SERIES",
            freqs: ["1m"],
            status: "active",
            attributes: { owner_module: "collector", dataset_role: "raw_collection" }
          }
        ],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListViews") {
    return route.fulfill({
      json: ok({
        views: [
          {
            space_id: "crypto",
            view_id: "view_binance_spot_kline_1m",
            name: "现货K线视图",
            dataset_id: "dataset_binance_spot_kline_1m",
            status: "active",
            active_index_id: "index-a",
            attributes: { owner_module: "collector", view_role: "collection_browse" }
          }
        ],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListFields") {
    return route.fulfill({
      json: ok({
        fields: [{ space_id: "crypto", field_id: "close", name: "收盘价", status: "active" }],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListFieldGroups") {
    return route.fulfill({
      json: ok({
        field_groups: [{ space_id: "crypto", group_id: "market", name: "市场数据", status: "active" }],
        field_counts: { market: 1 },
        total_field_count: 1,
        ungrouped_field_count: 0,
        page_result: { page: 1, size: 200, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListDatasetColumns") {
    return route.fulfill({
      json: ok({
        columns: [{ column_name: "close", origin_type: "DATASET_COLUMN_ORIGIN_TYPE_FIELD", origin_id: "close" }],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListViewColumns") {
    return route.fulfill({
      json: ok({
        columns: [
          { column_name: "open", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
          { column_name: "high", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
          { column_name: "low", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
          { column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
          { column_name: "volume", value_type: "FIELD_VALUE_TYPE_DOUBLE" }
        ],
        page_result: { page: 1, size: 20, total: 5, has_more: false }
      })
    });
  }
  if (method === "QueryTimeSeriesRows") {
    return route.fulfill({
      json: ok({
        rows: [
          {
            key: {
              space_id: "crypto",
              dataset_id: "dataset_binance_spot_kline_1m",
              subject_id: "BTC-USDT",
              freq: "1m",
              data_time: "2026-09-18T06:57:00Z",
              series_tag: "venue:binance"
            },
            fields: [
              { field_id: "open", value: { double_value: 123.4 } },
              { field_id: "high", value: { double_value: 124.0 } },
              { field_id: "low", value: { double_value: 123.0 } },
              { field_id: "close", value: { double_value: 123.45 } },
              { field_id: "volume", value: { double_value: 12.3 } }
            ]
          }
        ],
        page_result: { page: 1, size: 25, total: 1, has_more: false }
      })
    });
  }
  return route.fulfill({ json: ok() });
}

test.beforeEach(async ({ page }) => {
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test("data collection owns base assets and has no top-level data assets menu", async ({ page }) => {
  await page.goto("/#/home");
  await expect(page.getByRole("heading", { name: "量化系统驾驶舱" })).toBeVisible();
  await expect(page.getByText("数据资产", { exact: true })).toHaveCount(0);
  await expect(page.getByText("数据采集", { exact: true })).toBeVisible();

  await page.goto("/#/data/sources");
  await expect(page.getByRole("heading", { name: "数据源" })).toBeVisible();
  await page.goto("/#/data/subjects");
  await expect(page.getByRole("heading", { name: "数据对象" })).toBeVisible();
  await page.goto("/#/data/fields");
  await expect(page.getByRole("heading", { name: "字段管理" })).toBeVisible();
  await page.goto("/#/collector/rules");
  await expect(page.getByLabel("采集任务")).toBeVisible();
  await page.goto("/#/collector/data-management");
  await expect(page.getByLabel("基础数据集")).toBeVisible();
});

test("refresh and direct routes stay available for collection pages", async ({ page }) => {
  await page.goto("/#/data/fields");
  await expect(page.getByRole("heading", { name: "字段管理" })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "字段管理" })).toBeVisible();
  await page.goto("/#/collector/data-management");
  await expect(page.getByLabel("基础数据集")).toBeVisible();
  await expect(page.getByText("数据视图", { exact: true })).toHaveCount(0);
});

test("keeps the dataset definition and data browse tabs visible", async ({ page }) => {
  await page.goto("/#/collector/data-management");

  await expect(page.getByText("集合定义", { exact: true })).toBeVisible();
  await expect(page.getByText("查看数据", { exact: true })).toBeVisible();
  await page.getByText("查看数据", { exact: true }).click();

  await expect(page.getByText("时序视图 / DuckDB", { exact: true })).toBeVisible();
  await expect(page.getByText("BTC-USDT", { exact: true })).toBeVisible();
  await expect(page.getByText("123.45", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "K线", exact: true })).toBeVisible();

  const subjectFilter = page.locator(".filter-item").filter({ hasText: "数据ID" }).locator("input");
  const seriesFilter = page.locator(".filter-item").filter({ hasText: "序列标签" }).locator("input");
  await subjectFilter.fill("BTC-USDT");
  await seriesFilter.fill("venue:binance");
  await page.getByRole("button", { name: "K线", exact: true }).click();
  await expect(page.getByText("BTC-USDT K线", { exact: true })).toBeVisible();
});

test("desktop and mobile collection toolbars do not overlap", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/#/data/fields");
  const search = page.getByPlaceholder("搜索字段 ID、中文名或描述");
  const create = page.getByRole("button", { name: "新建字段" });
  const [searchBox, createBox] = await Promise.all([search.boundingBox(), create.boundingBox()]);
  expect(searchBox && createBox).toBeTruthy();
  expect((createBox?.x || 0) + 8).toBeGreaterThan((searchBox?.x || 0) + (searchBox?.width || 0));

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/#/data/fields");
  const mobileSearch = page.getByPlaceholder("搜索字段 ID、中文名或描述");
  const mobileCreate = page.getByRole("button", { name: "新建字段" });
  await expect(mobileSearch).toBeVisible();
  await expect(mobileCreate).toBeVisible();
  const [ms, mc] = await Promise.all([mobileSearch.boundingBox(), mobileCreate.boundingBox()]);
  const searchBottom = (ms?.y || 0) + (ms?.height || 0);
  const createTop = mc?.y || 0;
  expect(createTop + 1).toBeGreaterThanOrEqual(searchBottom - 4);
});
