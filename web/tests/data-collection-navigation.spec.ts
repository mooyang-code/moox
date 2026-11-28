import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const tagMemberRequests: Array<Record<string, unknown>> = [];

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
  if (method === "GetTaskList" || method === "GetTaskDetail") {
    const task = {
      space_id: "crypto",
      task_id: "task-binance-spot-kline-1m",
      task_name: "Binance 现货 K 线 1m",
      data_type: "kline",
      provider: "binance",
      result: { result_name: "采集结果", dataset_id: "dataset_binance_kline_1m", status: "active", data_kind: "time_series" },
      enabled: true
    };
    if (method === "GetTaskDetail") return route.fulfill({ json: ok({ task }) });
    return route.fulfill({
      json: ok({
        tasks: [
          {
            space_id: "crypto",
            task_id: "task-binance-spot-kline-1m",
            task_name: "Binance 现货 K 线 1m",
            data_type: "kline",
            provider: "binance",
            result: {
              result_name: "采集结果",
              dataset_id: "dataset_binance_kline_1m",
              status: "active",
              data_kind: "time_series"
            },
            enabled: true
          }
        ],
        page: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  // 按 ID 读单个数据集：采集结果页按任务的结果数据集定位
  if (method === "GetDataset") {
    return route.fulfill({
      json: ok({
        dataset: {
          space_id: "crypto",
          dataset_id: "dataset_binance_kline_1m",
          name: "现货K线",
          data_kind: 2,
          frequency: "1m",
          status: "active",
          attributes: { owner_module: "collector", dataset_role: "raw_collection" }
        }
      })
    });
  }
  if (method === "ListDatasets") {
    return route.fulfill({
      json: ok({
        datasets: [
          {
            space_id: "crypto",
            dataset_id: "dataset_binance_kline_1m",
            name: "现货K线",
            data_kind: 2,
            frequency: "1m",
            status: "active",
            attributes: { owner_module: "collector", dataset_role: "raw_collection" }
          }
        ],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListTags") {
    return route.fulfill({
      json: ok({
        tags: [
          { space_id: "crypto", tag_id: "manual", tag_name: "手工标签", mode: "manual", active_count: 1, inactive_count: 0 }
        ],
        page_result: { page: 1, size: 200, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListTagMembers") {
    tagMemberRequests.push((route.request().postDataJSON() || {}) as Record<string, unknown>);
    return route.fulfill({
      json: ok({
        members: [
          {
            space_id: "crypto",
            subject: {
              space_id: "crypto",
              subject_id: "BTC-USDT",
              subject_type: "crypto_pair",
              name: "比特币",
              market: "crypto",
              status: "active"
            },
            tag_ids: ["manual"]
          }
        ],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListDatasetColumns") {
    return route.fulfill({
      json: ok({
        columns: [
          { column_name: "open", value_type: "FIELD_VALUE_TYPE_DOUBLE", display_name: "开盘价", status: "active" },
          { column_name: "high", value_type: "FIELD_VALUE_TYPE_DOUBLE", display_name: "最高价", status: "active" },
          { column_name: "low", value_type: "FIELD_VALUE_TYPE_DOUBLE", display_name: "最低价", status: "active" },
          { column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE", display_name: "收盘价", status: "active" },
          { column_name: "volume", value_type: "FIELD_VALUE_TYPE_DOUBLE", display_name: "成交量", status: "active" }
        ],
        page_result: { page: 1, size: 20, total: 5, has_more: false }
      })
    });
  }
  if (method === "GetDataTypeConfigWithFields") {
    return route.fulfill({
      json: ok({
        detail: {
          base_output_fields: [
            { field_id: "open", display_name: "开盘价", group: "行情价格", value_type: "double" },
            { field_id: "close", display_name: "收盘价", group: "行情价格", value_type: "double" }
          ],
          extra_fields: [
            {
              field_id: "funding_rate",
              display_name: "资金费率",
              group: "合约指标",
              market_id: "crypto",
              instrument_type: "swap",
              frequencies: ["1h", "1d"],
              provider_id: "binance",
              value_type: "double",
              fill: "zero"
            }
          ]
        }
      })
    });
  }
  if (method === "GetViewState") {
    return route.fulfill({ json: ok({ index_id: "index-a", generation: "1", columns: [] }) });
  }
  if (method === "QueryTimeSeriesRows") {
    return route.fulfill({
      json: ok({
        rows: [
          {
            key: {
              space_id: "crypto",
              dataset_id: "dataset_binance_kline_1m",
              time_series: {
                subject_id: "BTC-USDT",
                frequency: "1m",
                data_time: "2026-09-18T06:57:00Z",
                series_tag: "venue:binance"
              }
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
  tagMemberRequests.length = 0;
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test("data collection owns base assets and has no top-level data assets menu", async ({ page }) => {
  await page.goto("/#/home");
  await expect(page.getByRole("navigation", { name: "常用入口" })).toBeVisible();
  await expect(page.getByText("数据资产", { exact: true })).toHaveCount(0);
  await expect(page.getByText("数据采集", { exact: true })).toBeVisible();

  await page.goto("/#/data/sources");
  await expect(page.getByLabel("数据源")).toBeVisible();
  await page.goto("/#/data/subjects");
  await expect(page.getByLabel("数据对象")).toBeVisible();
  await page.goto("/#/collector/tasks");
  await expect(page.getByLabel("采集运行")).toBeVisible();
  await expect(page).toHaveURL(/tab=results/);
  await expect(page.getByText("采集结果", { exact: true })).toBeVisible();
});

test("refresh and direct routes stay available for collection pages", async ({ page }) => {
  await page.goto("/#/data/sources");
  await expect(page.getByLabel("数据源")).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("数据源")).toBeVisible();
  await page.goto("/#/collector/tasks?tab=results");
  await expect(page.getByText("Binance 现货 K 线 1m", { exact: true }).first()).toBeVisible();
});

test("data sources hosts field definitions tab with sidebar groups and provider link", async ({ page }) => {
  await page.goto("/#/data/sources");
  await expect(page.getByText("资金费率（币安）", { exact: true })).toBeVisible();
  await page.getByText(/共 1 个字段/).click();

  await expect(page).toHaveURL(/tab=fields/);
  await expect(page.getByText("来源：资金费率（币安）")).toBeVisible();
  await expect(page.getByText("funding_rate", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: /全部字段/ }).click();
  await page.locator(".arco-tag-close-btn").click();
  await expect(page.getByText("开盘价", { exact: true })).toBeVisible();

  await page.getByRole("row").filter({ hasText: "funding_rate" }).click();
  const drawer = page.locator(".arco-drawer");
  await expect(drawer.getByText("字段详情")).toBeVisible();
  await expect(drawer.getByText("补零")).toBeVisible();
});

test("runtime status hosts cloud accounts and code packages, not an executors tab", async ({ page }) => {
  await page.goto("/#/collector/tasks?tab=executors");
  await expect(page.getByLabel("采集运行")).toBeVisible();
  await expect(page.getByText("执行器", { exact: true })).toHaveCount(0);
  await page.goto("/#/collector/tasks?tab=status");
  await expect(page.getByRole("button", { name: "云账号", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "代码包", exact: true })).toBeVisible();
});

test("shows task-owned real data and the K-line modal", async ({ page }) => {
  await page.goto("/#/collector/tasks?tab=results");

  await expect(page.getByText("时序数据 / DuckDB", { exact: true })).toBeVisible();
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

test("data subjects status filter applies immediately", async ({ page }) => {
  await page.goto("/#/data/subjects");
  await page.getByText("标签成员", { exact: true }).click();

  const toolbar = page.locator(".data-subjects-page .filter-bar");
  const statusFilter = toolbar.locator(".status-select");
  await expect(statusFilter).toBeVisible();
  await expect(statusFilter).toContainText("全部");
  // 下拉即时生效：工具栏不再有“查询”按钮
  await expect(toolbar.getByRole("button", { name: "查询" })).toHaveCount(0);

  await statusFilter.click();
  await page.locator(".arco-select-dropdown").getByText("停用", { exact: true }).first().click();
  await expect.poll(() => tagMemberRequests.at(-1)?.status).toBe("disabled");
});
