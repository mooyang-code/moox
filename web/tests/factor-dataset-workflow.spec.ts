import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });

let jobs: Array<{ job_id: string; status: string; request_id: string }> = [];

async function mockGateway(route: Route) {
  const method = route.request().url().split("/").pop();
  const body = route.request().postDataJSON?.() || {};
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
            status: "active",
            freqs: ["1m"],
            attributes: { owner_module: "collector", dataset_role: "raw_collection" }
          },
          {
            space_id: "crypto",
            dataset_id: "mdataset_binance_kline_1m",
            name: "复合K线",
            status: "active",
            attributes: {
              owner_module: "factor",
              dataset_role: "merged_factor",
              merge_mode: "system",
              storage_resource_state: "created"
            }
          }
        ],
        page_result: { page: 1, size: 20, total: 2, has_more: false }
      })
    });
  }
  if (method === "ListDatasetColumns") {
    return route.fulfill({
      json: ok({
        columns: [
          { column_name: "close", origin_type: "field", attributes: { display_name: "收盘价", field_kind: "base" } },
          { column_name: "bias5", origin_type: "factor", attributes: { display_name: "乖离", field_kind: "factor_output" } }
        ],
        page_result: { page: 1, size: 20, total: 2, has_more: false }
      })
    });
  }
  if (method === "RecalcFactor") {
    const job = { job_id: body.request_id || "job-1", request_id: body.request_id || "job-1", status: "accepted" };
    jobs = [job, ...jobs.filter(item => item.job_id !== job.job_id)];
    return route.fulfill({ json: ok(job) });
  }
  if (method === "GetRecalcJob") {
    const job = jobs.find(item => item.job_id === body.job_id) || {
      job_id: body.job_id,
      status: "accepted",
      request_id: body.job_id
    };
    return route.fulfill({ json: ok(job) });
  }
  if (method === "GetEngineStatus") {
    return route.fulfill({
      json: ok({
        python_workers: 0,
        active_tasks: 0,
        pending_tasks: jobs.length,
        desired_revision: 4,
        applied_revision: 4,
        engine_id: "local-engine"
      })
    });
  }
  return route.fulfill({ json: ok() });
}

test.beforeEach(async ({ page }) => {
  jobs = [];
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test("factor pages show construct status, field ownership and async recalc", async ({ page }) => {
  await page.goto("/#/factor/datasets");
  await expect(page.getByText("mdataset_binance_kline_1m")).toBeVisible();
  await page.goto("/#/factor/construct");
  await expect(page.getByText("system")).toBeVisible();
  await expect(page.getByText("created")).toBeVisible();
  await expect(page.getByText("因子输出")).toBeVisible();
  await page.getByRole("button", { name: "重试恢复" }).click();
  await expect(page.getByText("created")).toBeVisible();
  await page.goto("/#/factor/tasks");
  await expect(page.getByText("local-engine")).toBeVisible();
  await page.getByPlaceholder("请求 ID").fill("recalc-e2e");
  await page.getByPlaceholder("输入数据集").fill("dataset_binance_spot_kline_1m");
  await page.getByPlaceholder("对象").fill("BTC-USDT");
  await page.getByPlaceholder("频率").fill("1m");
  await page.getByPlaceholder("开始时间").fill("2026-09-13T12:00:00Z");
  await page.getByPlaceholder("结束时间").fill("2026-09-13T12:01:00Z");
  await page.getByRole("button", { name: "提交补算" }).click();
  await expect(page.getByText("accepted")).toBeVisible();
});
