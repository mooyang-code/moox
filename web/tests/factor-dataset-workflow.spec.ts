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

const baseDef = {
  name: "Bias",
  factor_type: "timeseries",
  source_code: "def compute(df, params, context): return df",
  source_hash: "sha256:abcd",
  input_columns: ["close"],
  params_json: "{}",
  lookback_periods: 5,
  allow_partial_universe: false,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z"
};

type Def = typeof baseDef & { factor_id: string; outputs: string[] };
type Member = {
  set_id: string;
  factor_id: string;
  status: "enabled" | "disabled";
  factor: Def;
  created_at: string;
  updated_at: string;
};

let defs: Def[] = [];
let members: Member[] = [];
let createdFactor: Record<string, unknown> | null = null;

function member(def: Def, status: Member["status"]): Member {
  return {
    set_id: factorSet.set_id,
    factor_id: def.factor_id,
    status,
    factor: def,
    created_at: baseDef.created_at,
    updated_at: baseDef.updated_at
  };
}

function resetState() {
  defs = [
    { ...baseDef, factor_id: "bias", outputs: ["bias_5"] },
    { ...baseDef, factor_id: "rsi", name: "Rsi", outputs: ["rsi_14"] }
  ];
  members = [member(defs[0], "enabled")];
  jobs = [];
  createdFactor = null;
}

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
        factor_sets: [
          {
            factor_set: factorSet,
            members,
            last_run: { set_id: factorSet.set_id, last_period_time: 1791086400, last_status: "complete", lag_seconds: 30 }
          }
        ],
        page_result: { page: 1, size: 500, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListFactors") {
    const factors = defs.map(def => ({
      factor: def,
      usages: members.filter(item => item.factor_id === def.factor_id).map(item => ({ set_id: item.set_id, status: item.status }))
    }));
    return route.fulfill({ json: ok({ factors, page_result: { page: 1, size: 200, total: factors.length, has_more: false } }) });
  }
  if (method === "GetFactor") {
    const def = defs.find(item => item.factor_id === body.factor_id);
    const usages = members
      .filter(item => item.factor_id === body.factor_id)
      .map(item => ({ set_id: item.set_id, status: item.status }));
    return route.fulfill({ json: ok({ factor: def, usages }) });
  }
  if (method === "CreateFactor") {
    createdFactor = body.factor;
    defs = [...defs, { ...baseDef, ...body.factor }];
    return route.fulfill({ json: ok({ factor: body.factor }) });
  }
  if (method === "AddFactorToSet") {
    const def = defs.find(item => item.factor_id === body.factor_id)!;
    const added = member(def, "disabled");
    members = [...members, added];
    return route.fulfill({ json: ok({ member: added }) });
  }
  if (method === "RemoveFactorFromSet") {
    members = members.filter(item => item.factor_id !== body.factor_id);
    return route.fulfill({ json: ok() });
  }
  if (method === "SetFactorMemberStatus") {
    members = members.map(item => (item.factor_id === body.factor_id ? { ...item, status: body.status } : item));
    const updated = members.find(item => item.factor_id === body.factor_id);
    const backfill_job =
      body.status === "enabled"
        ? {
            job_id: "backfill-1",
            request_id: "backfill-1",
            set_id: factorSet.set_id,
            factor_ids: [body.factor_id],
            subjects: [],
            start_time: "",
            end_time: "",
            status: "accepted",
            progress_time: "",
            error: ""
          }
        : undefined;
    if (backfill_job) jobs = [backfill_job, ...jobs];
    return route.fulfill({ json: ok({ member: updated, backfill_job }) });
  }
  if (method === "GetView") {
    return route.fulfill({
      json: ok({
        view: {
          space_id: "crypto",
          view_id: "view_factor_binance_kline_1m",
          name: "现货K线因子结果",
          dataset_id: factorSet.result_dataset_id,
          status: "active",
          attributes: { owner_module: "factor", view_role: "factor_result" }
        }
      })
    });
  }
  if (method === "GetDataset") {
    return route.fulfill({
      json: ok({
        dataset: {
          space_id: "crypto",
          dataset_id: body.dataset_id,
          name: "现货K线",
          status: "active",
          freqs: ["1m"],
          attributes: {}
        }
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
  if (method === "ListRecalcJobs") {
    const items = jobs.filter(item => item.set_id === body.set_id);
    return route.fulfill({ json: ok({ jobs: items, page_result: { page: 1, size: 20, total: items.length, has_more: false } }) });
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
        views: [
          {
            space_id: "crypto",
            view_id: "view_factor_binance_kline_1m",
            name: "现货K线因子结果",
            dataset_id: factorSet.result_dataset_id,
            status: "active",
            attributes: { owner_module: "factor", view_role: "factor_result" }
          }
        ],
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
          {
            view_id: "view_factor_binance_kline_1m",
            column_name: "bias_5",
            attributes: { origin_factor_id: "bias", factor_output: "bias_5" }
          }
        ],
        page_result: { page: 1, size: 500, total: 4, has_more: false }
      })
    });
  }
  if (method === "ListViewData")
    return route.fulfill({ json: ok({ rows: [], page_result: { page: 1, size: 100, total: 0, has_more: false } }) });
  return route.fulfill({ json: ok() });
}

test.beforeEach(async ({ page }) => {
  jobs = [];
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test("factor workbench scopes factors, results and async recalculation to one set", async ({ page }) => {
  await page.goto("/#/factor/workbench");
  await expect(page.getByText(factorSet.set_id)).toBeVisible();
  await expect(page.getByText(factorSet.result_dataset_id)).toBeVisible();
  await page.getByRole("tab", { name: "因子" }).click();
  await expect(page.getByText("Bias", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "结果" }).click();
  await expect(page.getByText("结果 View view_factor_binance_kline_1m")).toBeVisible();
  await page.getByRole("tab", { name: "补算" }).click();
  await page.getByPlaceholder("留空自动生成").fill("recalc-e2e");
  await page.getByRole("button", { name: "提交补算" }).click();
  await expect(page.getByText("已受理")).toBeVisible();
});

test.beforeEach(async ({ page }) => {
  resetState();
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test("compute task drawer lists members and locks enabled factors", async ({ page }) => {
  await page.goto("/#/factor/tasks");
  await page
    .getByRole("link", { name: /dataset_binance_kline_1m|现货K线/ })
    .first()
    .click();
  const drawer = page.locator(".arco-drawer");
  await expect(drawer.getByText(factorSet.result_dataset_id)).toBeVisible();
  await expect(drawer.getByText("bias", { exact: true })).toBeVisible();
  await expect(drawer.getByRole("button", { name: "编辑定义" })).toBeDisabled();
  await expect(drawer.getByRole("button", { name: "移除" })).toBeDisabled();
});

test("definitions page shows usage chips and locks edit and delete", async ({ page }) => {
  await page.goto("/#/factor/definitions");
  const bias = page.getByRole("row").filter({ hasText: "bias" });
  await expect(bias.locator(".usage-chip")).toBeVisible();
  await expect(bias.getByRole("button", { name: "编辑" })).toBeDisabled();
  await expect(bias.getByRole("button", { name: "删除" })).toBeDisabled();
  const rsi = page.getByRole("row").filter({ hasText: "rsi" });
  await expect(rsi.getByText("未被使用")).toBeVisible();
  await expect(rsi.getByRole("button", { name: "编辑" })).toBeEnabled();
});

test("results tab embeds the result view", async ({ page }) => {
  await page.goto("/#/factor/tasks?tab=results");
  await expect(page.getByText("共 1 个计算任务")).toBeVisible();
});

test("recalc tab accepts a manual recalculation", async ({ page }) => {
  await page.goto("/#/factor/tasks?tab=recalc");
  await page.getByRole("button", { name: "新建补算" }).click();
  await page.locator(".arco-drawer .arco-select-view").first().click();
  await page.getByRole("option", { name: /bias/ }).click();
  await page.keyboard.press("Escape");
  await page.getByPlaceholder("留空自动生成").fill("recalc-e2e");
  await page
    .locator(".arco-drawer")
    .getByRole("button", { name: /^(提交|确定|提交补算)/ })
    .click();
  await expect(page.getByText("已受理").first()).toBeVisible();
});

test("new definition is created without a set and highlights the definitions menu", async ({ page }) => {
  await page.goto("/#/factor/definitions/new");
  await expect(page.locator(".arco-menu-selected")).toContainText("因子定义");
  await page.getByPlaceholder("Bias").first().fill("momentum");
  await page.getByPlaceholder("Bias").nth(1).fill("Momentum");
  await page.locator(".cm-content").first().click();
  await page.getByRole("button", { name: "插入模板" }).click();
  await page.getByPlaceholder("输入列名后回车").fill("close");
  await page.keyboard.press("Enter");
  await page.getByPlaceholder("输入输出列名后回车").fill("momentum_5");
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "保存因子" }).click();
  await expect.poll(() => createdFactor?.factor_id).toBe("momentum");
  expect(createdFactor).not.toHaveProperty("set_id");
});

test("adding a factor to a task then enabling it reports the backfill", async ({ page }) => {
  await page.goto("/#/factor/tasks");
  await page
    .getByRole("link", { name: /dataset_binance_kline_1m|现货K线/ })
    .first()
    .click();
  const drawer = page.locator(".arco-drawer");
  await drawer.getByRole("button", { name: "添加因子" }).click();
  await page.locator(".arco-modal").getByText("rsi", { exact: true }).click();
  await page
    .locator(".arco-modal")
    .getByRole("button", { name: /^添加（1）/ })
    .click();
  await expect(drawer.getByText("rsi", { exact: true })).toBeVisible();
  await drawer.getByRole("button", { name: "启用" }).last().click();
  await expect(drawer.getByText(/历史回填任务 backfill-1/)).toBeVisible();
});

test("overview page is read-only and links to tasks", async ({ page }) => {
  await page.goto("/#/factor/overview");
  await expect(page.getByRole("heading", { name: "因子总览" })).toBeVisible();
  await expect(page.getByText(factorSet.set_id).or(page.getByText("现货K线")).first()).toBeVisible();
});
