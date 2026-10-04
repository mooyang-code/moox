import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });

let jobs: Array<Record<string, unknown>> = [];

const factorSet = {
  set_id: "fset_binance_kline_1m",
  space_id: "crypto",
  source_dataset_id: "dataset_binance_kline_1m",
  freq: "1m",
  subject_mode: "all",
  subjects: [],
  result_dataset_id: "dataset_factor_binance_kline_1m",
  status: "enabled"
};

const factor = {
  factor_id: "bias",
  set_id: factorSet.set_id,
  factor_type: "timeseries",
  name: "Bias",
  source_code: "def compute(df, params, context): return df",
  source_hash: "sha256:abcd",
  input_columns: ["close"],
  outputs: ["bias_5"],
  params_json: "{}",
  lookback_periods: 5,
  status: "enabled"
};

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
            dataset_id: "dataset_binance_kline_1m",
            name: "现货K线",
            status: "active",
            freqs: ["1m"],
            attributes: { owner_module: "collector", dataset_role: "raw_collection" }
          },
          {
            space_id: "crypto",
            dataset_id: factorSet.result_dataset_id,
            name: "现货K线因子结果",
            status: "active",
            freqs: ["1m"],
            attributes: { owner_module: "factor", dataset_role: "factor_result" }
          }
        ],
        page_result: { page: 1, size: 20, total: 2, has_more: false }
      })
    });
  }
  if (method === "ListFactorSets") {
    return route.fulfill({
      json: ok({
        factor_sets: [{ factor_set: factorSet, factors: [factor], last_run: { set_id: factorSet.set_id, last_period_time: 1791086400, last_status: "complete", lag_seconds: 30 } }],
        page_result: { page: 1, size: 500, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListFactors") {
    return route.fulfill({ json: ok({ factors: [factor], page_result: { page: 1, size: 20, total: 1, has_more: false } }) });
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
  if (method === "RecalcFactors") {
    const job = {
      job_id: body.request_id || "job-1",
      request_id: body.request_id || "job-1",
      set_id: body.set_id,
      factor_ids: body.factor_ids || [],
      subjects: body.subjects || [],
      start_time: body.start_time,
      end_time: body.end_time,
      status: "accepted",
      progress_time: "",
      error: ""
    };
    jobs = [job, ...jobs.filter(item => item.job_id !== job.job_id)];
    return route.fulfill({ json: ok({ job }) });
  }
  if (method === "GetRecalcJob") {
    const job = jobs.find(item => item.job_id === body.job_id) || {
      job_id: body.job_id,
      status: "accepted",
      request_id: body.job_id
    };
    return route.fulfill({ json: ok({ job }) });
  }
  if (method === "GetStatus") {
    return route.fulfill({
      json: ok({
        consumer_running: true,
        python_workers: 0,
        python_busy: 0,
        lanes: [],
        recent_runs: []
      })
    });
  }
  if (method === "ListViews") {
    return route.fulfill({
      json: ok({
        views: [{ space_id: "crypto", view_id: "view_factor_binance_kline_1m", name: "现货K线因子结果", dataset_id: factorSet.result_dataset_id, status: "active", attributes: { owner_module: "factor", view_role: "factor_result" } }],
        page_result: { page: 1, size: 200, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListViewColumns") {
    return route.fulfill({
      json: ok({
        columns: [
          { view_id: "view_factor_binance_kline_1m", column_name: "subject_id" },
          { view_id: "view_factor_binance_kline_1m", column_name: "data_time" },
          { view_id: "view_factor_binance_kline_1m", column_name: "close" },
          { view_id: "view_factor_binance_kline_1m", column_name: "bias_5", attributes: { origin_factor_id: "bias", factor_output: "bias_5" } }
        ],
        page_result: { page: 1, size: 500, total: 4, has_more: false }
      })
    });
  }
  if (method === "ListViewData") return route.fulfill({ json: ok({ rows: [], page_result: { page: 1, size: 100, total: 0, has_more: false } }) });
  return route.fulfill({ json: ok() });
}

test.beforeEach(async ({ page }) => {
  jobs = [];
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test("factor sets scope definitions, results and async recalculation", async ({ page }) => {
  await page.goto("/#/factor/sets");
  await expect(page.getByText(factorSet.set_id)).toBeVisible();
  await expect(page.getByText(factorSet.result_dataset_id)).toBeVisible();
  await page.goto("/#/factor/definitions");
  await expect(page.getByText("Bias")).toBeVisible();
  await page.goto("/#/factor/results");
  await expect(page.getByText(`Storage 结果 View view_factor_binance_kline_1m`)).toBeVisible();
  await page.goto("/#/factor/tasks");
  await expect(page.getByText("实时消费运行中")).toBeVisible();
  await page.getByPlaceholder("留空自动生成").fill("recalc-e2e");
  await page.getByRole("button", { name: "提交补算" }).click();
  await expect(page.getByText("已受理")).toBeVisible();
});
