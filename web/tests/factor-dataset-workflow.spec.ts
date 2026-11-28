import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });

let jobs: Array<Record<string, unknown>> = [];

const factorTask = {
  task_id: "ftask_binance_kline_1m",
  space_id: "crypto",
  source_dataset_id: "dataset_binance_kline_1m",
  frequency: "1m",
  result_dataset_id: "dataset_factor_binance_kline_1m",
  status: "enabled"
};

const baseDef = {
  name: "Bias",
  factor_type: "timeseries",
  spec: "outputs:\n  bias: close / ts_mean(close, 5)\n",
  spec_hash: "sha256:abcd",
  input_columns: ["close"],
  lookback_periods: 5,
  definition_hash: "sha256:definition",
  allow_partial_universe: false,
  created_at: String(Date.UTC(2026, 9, 1)),
  updated_at: String(Date.UTC(2026, 9, 1))
};

type Def = typeof baseDef & { factor_id: string; outputs: string[] };
type Member = {
  task_id: string;
  member_key: string;
  factor_id: string;
  status: "enabled" | "disabled";
  kind: "fixed" | "var";
  output_columns: string[];
  factor: Def;
  created_at: string;
  updated_at: string;
};

let defs: Def[] = [];
let members: Member[] = [];
let createdFactor: Record<string, unknown> | null = null;

function member(def: Def, status: Member["status"]): Member {
  return {
    task_id: factorTask.task_id,
    member_key: def.factor_id,
    factor_id: def.factor_id,
    status,
    kind: "fixed",
    output_columns: def.outputs,
    factor: def,
    created_at: baseDef.created_at,
    updated_at: baseDef.updated_at
  };
}

function resetState() {
  defs = [
    { ...baseDef, factor_id: "bias", outputs: ["bias_5"] },
    // 只有一个输出的因子，输出名就是因子 ID
    { ...baseDef, factor_id: "rsi", name: "Rsi", spec: "outputs:\n  rsi: ts_mean(close, 14)\n", outputs: ["rsi"] }
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
            frequency: "1m",
            attributes: { owner_module: "collector", dataset_role: "raw_collection" }
          },
          {
            space_id: "crypto",
            dataset_id: factorTask.result_dataset_id,
            name: "现货K线因子结果",
            status: "active",
            frequency: "1m",
            attributes: { owner_module: "factor", dataset_role: "factor_result" }
          }
        ],
        page_result: { page: 1, size: 20, total: 2, has_more: false }
      })
    });
  }
  if (method === "ListFactorTasks") {
    return route.fulfill({
      json: ok({
        factor_tasks: [
          {
            factor_task: factorTask,
            members,
            last_run: { task_id: factorTask.task_id, last_period_time: "1791086400", last_status: "complete", lag_seconds: "30" }
          }
        ],
        page_result: { page: 1, size: 500, total: 1, has_more: false }
      })
    });
  }
  if (method === "ListFactors") {
    const factors = defs.map(def => ({
      factor: def,
      usages: members
        .filter(item => item.factor_id === def.factor_id)
        .map(item => ({ task_id: item.task_id, status: item.status }))
    }));
    return route.fulfill({ json: ok({ factors, page_result: { page: 1, size: 200, total: factors.length, has_more: false } }) });
  }
  if (method === "GetFactor") {
    const def = defs.find(item => item.factor_id === body.factor_id);
    const usages = members
      .filter(item => item.factor_id === body.factor_id)
      .map(item => ({ task_id: item.task_id, status: item.status }));
    return route.fulfill({ json: ok({ factor: def, usages }) });
  }
  if (method === "CreateFactor") {
    createdFactor = body.factor;
    defs = [...defs, { ...baseDef, ...body.factor }];
    return route.fulfill({ json: ok({ factor: body.factor }) });
  }
  if (method === "AddTaskMember") {
    const def = defs.find(item => item.factor_id === body.factor_id)!;
    const added = member(def, "disabled");
    members = [...members, added];
    return route.fulfill({ json: ok({ member: added }) });
  }
  if (method === "RemoveTaskMember") {
    members = members.filter(item => item.member_key !== body.member_key);
    return route.fulfill({ json: ok() });
  }
  if (method === "SetTaskMemberStatus") {
    members = members.map(item => (item.member_key === body.member_key ? { ...item, status: body.status } : item));
    const updated = members.find(item => item.member_key === body.member_key);
    return route.fulfill({ json: ok({ member: updated }) });
  }
  if (method === "GetDataset") {
    return route.fulfill({
      json: ok({
        dataset: {
          space_id: "crypto",
          dataset_id: body.dataset_id,
          name: "现货K线",
          status: "active",
          frequency: "1m",
          attributes: {}
        }
      })
    });
  }
  if (method === "ListDatasetColumns") {
    // 结果数据集的列带因子输出；源数据集只有行情列与参考列。
    const columns =
      body.dataset_id === factorTask.result_dataset_id
        ? [
            { column_name: "close", display_name: "收盘价", status: "active" },
            {
              column_name: "bias_5",
              display_name: "bias_5",
              status: "active",
              attributes: { factor_id: "bias", member_key: "bias__5", column_kind: "output" }
            }
          ]
        : [
            { column_name: "close", display_name: "收盘价", status: "active" },
            { column_name: "bias5", display_name: "乖离", status: "active", attributes: { column_kind: "output" } }
          ];
    return route.fulfill({
      json: ok({
        columns,
        page_result: { page: 1, size: 20, total: 2, has_more: false }
      })
    });
  }
  if (method === "RecalcFactors") {
    const job = {
      job_id: body.request_id || "job-1",
      request_id: body.request_id || "job-1",
      task_id: body.task_id,
      members: body.members || [],
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
  if (method === "ValidateFactorSpec") {
    return route.fulfill({
      json: ok({ valid: true, factor_type: "timeseries", input_columns: ["close"], outputs: ["momentum"], lookback_periods: 5 })
    });
  }
  if (method === "ListRecalcJobs") {
    const items = jobs.filter(item => item.task_id === body.task_id);
    return route.fulfill({ json: ok({ jobs: items, page_result: { page: 1, size: 20, total: items.length, has_more: false } }) });
  }
  if (method === "GetStatus") {
    return route.fulfill({
      json: ok({
        consumer_running: true,
        compute_workers: 4,
        engine_expr_version: "expr/1",
        manager_expr_version: "expr/1",
        runners: [],
        recent_runs: []
      })
    });
  }
  return route.fulfill({ json: ok() });
}

test.beforeEach(async ({ page }) => {
  resetState();
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test("compute task drawer lists members and locks enabled factors", async ({ page }) => {
  await page.goto("/#/factor/tasks");
  await page
    .getByRole("row", { name: /ftask_binance_kline_1m/ })
    .getByText("详情", { exact: true })
    .click();
  const drawer = page.locator(".arco-drawer");
  await expect(drawer.getByText(factorTask.result_dataset_id)).toBeVisible();
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

test("definitions page hosts output fields tab with task columns and factor detail drawer", async ({ page }) => {
  await page.goto("/#/factor/definitions");
  await page.getByRole("tab", { name: "产出字段" }).click();
  await expect(page).toHaveURL(/tab=fields/);
  await expect(page.getByText("bias_5", { exact: true })).toBeVisible();
  await expect(page.getByText("共 1 个产出字段")).toBeVisible();

  await page.getByRole("row").filter({ hasText: "bias_5" }).getByText("Bias", { exact: true }).click();
  const drawer = page.locator(".arco-drawer");
  await expect(drawer.getByText("因子详情")).toBeVisible();
  await expect(drawer.getByText("产出字段", { exact: true })).toBeVisible();
});

test("results tab embeds the result dataset", async ({ page }) => {
  await page.goto("/#/factor/tasks?tab=results");
  await expect(page.getByText("(dataset_factor_binance_kline_1m)")).toBeVisible();
});

test("recalc tab accepts a manual recalculation", async ({ page }) => {
  await page.goto("/#/factor/tasks?tab=recalc");
  await page.getByRole("button", { name: "新建补算" }).click();
  // 只有一个已启用的因子 bias，表单已默认选上
  await expect(page.locator(".arco-drawer .arco-form-item").filter({ hasText: "因子（仅已启用成员）" })).toContainText("bias");
  await page.getByPlaceholder("留空自动生成").fill("recalc-e2e");
  await page
    .locator(".arco-drawer")
    .getByRole("button", { name: /^(提交|确定|提交补算)/ })
    .click();
  await expect(page.getByText("已受理").first()).toBeVisible();
});

test("new definition is created without a set and highlights the definitions menu", async ({ page }) => {
  await page.goto("/#/factor/definitions/new");
  await expect(page.locator(".arco-menu-item.arco-menu-selected")).toHaveText("因子定义");
  await page.getByPlaceholder("bias_20").fill("momentum");
  await page.getByPlaceholder("Bias 偏离").fill("Momentum");
  await page.getByRole("button", { name: "时序模板" }).click();
  await page.getByRole("button", { name: "保存因子" }).click();
  await expect.poll(() => createdFactor?.factor_id).toBe("momentum");
  expect(createdFactor).not.toHaveProperty("task_id");
});

test("adding a factor to a task then enabling it does not start a backfill", async ({ page }) => {
  await page.goto("/#/factor/tasks");
  await page
    .getByRole("row", { name: /ftask_binance_kline_1m/ })
    .getByText("详情", { exact: true })
    .click();
  const drawer = page.locator(".arco-drawer");
  await drawer.getByRole("button", { name: "添加因子" }).click();
  await page.locator(".arco-modal .add-factor__row").filter({ hasText: "rsi" }).locator("strong").click();
  await page
    .locator(".arco-modal")
    .getByRole("button", { name: /^添加（1）/ })
    .click();
  // 添加后立即启用（默认勾上），提示历史要手动补算
  await expect(page.getByText(/已添加并启用 1 个因子。历史数据需要补算/)).toBeVisible();
  await expect(drawer.getByText(/历史回填任务/)).toHaveCount(0);
});

test("计算任务页签顶部显示引擎状态", async ({ page }) => {
  await page.goto("/#/factor/tasks");
  await expect(page.getByRole("region", { name: "引擎与补算" })).toBeVisible();
  await expect(page.getByText("计算引擎", { exact: true })).toBeVisible();
});
