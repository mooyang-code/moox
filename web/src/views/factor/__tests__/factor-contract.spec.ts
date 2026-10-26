import { describe, expect, it } from "vitest";
import type {
  EngineStatus,
  FactorDef,
  FactorInfo,
  FactorMember,
  FactorTask,
  ListRecalcJobsReq,
  RecalcFactorsReq,
  RecalcJob,
  TaskRunSummary
} from "@/api/factor/types";
import taskHost from "@/views/factor/task-management/index.vue?raw";
import factorScope from "@/views/factor/shared/use-factor-scope.ts?raw";
import computeTasks from "@/views/factor/compute-tasks/index.vue?raw";
import memberSection from "@/views/factor/compute-tasks/member-section.vue?raw";
import memberModel from "@/views/factor/compute-tasks/compute-tasks-model.ts?raw";
import addFactorModal from "@/views/factor/compute-tasks/add-factor-modal.vue?raw";
import definitionsPage from "@/views/factor/definitions/index.vue?raw";
import factorEditor from "@/views/factor/editor/index.vue?raw";
import recalcTab from "@/views/factor/recalc/index.vue?raw";
import recalcModel from "@/views/factor/recalc/recalc-model.ts?raw";
import resultsTab from "@/views/factor/results/index.vue?raw";
import resultsModel from "@/views/factor/results/results-model.ts?raw";
import overviewPage from "@/views/factor/overview/index.vue?raw";

describe("factor API contract", () => {
  it("uses explicit generic time-series fields", () => {
    const factor: FactorDef = {
      factor_id: "Bias",
      factor_type: "timeseries",
      name: "Bias",
      spec: "outputs:\n  excess_return: nav - benchmark_return\n  rolling_rank: ts_rank(nav, 100)\n",
      spec_hash: "sha256:hash",
      input_columns: ["benchmark_return", "nav"],
      outputs: ["excess_return", "rolling_rank"],
      lookback_periods: 100,
      definition_hash: "sha256:definition",
      allow_partial_universe: false,
      created_at: "2026-10-04T00:00:00Z",
      updated_at: "2026-10-04T00:00:00Z"
    };
    const info: FactorInfo = { factor, usages: [{ task_id: "factor_task_crypto_1m", status: "enabled" }] };
    const member: FactorMember = {
      task_id: "factor_task_crypto_1m",
      factor_id: "Bias",
      status: "enabled",
      factor,
      created_at: factor.created_at,
      updated_at: factor.updated_at
    };
    expect(factor.input_columns).toEqual(["benchmark_return", "nav"]);
    expect(factor.lookback_periods).toBe(100);
    expect(factor).not.toHaveProperty("task_id");
    expect(factor).not.toHaveProperty("status");
    expect(info.usages[0].status).toBe(member.status);
  });

  it("accepts set-scoped async recalc jobs with a request id", () => {
    const request: RecalcFactorsReq = {
      task_id: "factor_task_crypto_1m",
      factor_ids: ["Bias"],
      subjects: ["BTC-USDT"],
      request_id: "recalc-1",
      start_time: "2026-07-26T00:00:00Z",
      end_time: "2026-07-27T00:00:00Z"
    };
    const job: RecalcJob = {
      job_id: "job-1",
      request_id: request.request_id,
      task_id: request.task_id,
      factor_ids: request.factor_ids,
      subjects: request.subjects,
      start_time: request.start_time,
      end_time: request.end_time,
      status: "running",
      progress_time: request.start_time,
      error: "",
      created_at: request.start_time,
      updated_at: request.start_time
    };
    const list: ListRecalcJobsReq = { task_id: job.task_id, statuses: ["accepted", "running"] };
    expect(list.task_id).toBe(request.task_id);
    expect(job.progress_time).toBe(request.start_time);
  });

  it("exposes per-factor period states and the deleting set status", () => {
    const set: FactorTask = {
      task_id: "factor_task_crypto_1m",
      space_id: "crypto",
      source_dataset_id: "dataset_crypto_kline_1m",
      freq: "1m",
      result_dataset_id: "dataset_factor_crypto_1m",
      status: "deleting"
    };
    const run: TaskRunSummary = {
      task_id: set.task_id,
      last_period_time: 1,
      last_status: "degraded",
      lag_seconds: 3,
      factors: [{ factor_id: "Bias", status: "degraded", failed_subjects: ["BTC-USDT"] }]
    };
    const engine: EngineStatus = {
      ret_info: { code: 0, msg: "success" },
      consumer_running: true,
      compute_workers: 4,
      engine_expr_version: "expr/1",
      manager_expr_version: "expr/1",
      runners: [],
      recent_runs: [run]
    };
    expect(set.result_dataset_id).toMatch(/^dataset_/);
    expect(engine.recent_runs[0].factors?.[0].status).toBe("degraded");
  });
});

describe("factor pages contract", () => {
  it("keeps the selected set and tab in the URL query", () => {
    expect(taskHost).toContain("route.query.tab");
    expect(taskHost).toContain("PageTitleTabs");
    expect(factorScope).toContain("route.query.task");
    expect(factorScope).toContain("usePolling");
    expect(overviewPage).toContain("usePolling");
  });

  it("changes member status only through setFactorMemberStatus without a backfill job", () => {
    expect(memberSection).toContain("setFactorMemberStatus(member.task_id, alias, target)");
    expect(memberSection).toContain("removeFactorFromTask");
    expect(memberSection).not.toContain("backfill_job");
    expect(memberModel).toContain("需要先停用");
    expect(addFactorModal).toContain("addFactorToTask");
    expect(addFactorModal).toContain("listDatasetColumns");
    expect(definitionsPage).not.toContain("setFactorMemberStatus");
    expect(factorEditor).not.toContain('v-model="form.status"');
  });

  it("lays out the definitions page like the fields workbench", () => {
    // 与字段管理一致：标题、搜索、筛选、查询在左，新增在右；下方提示条；左侧分类树，右侧批量栏加表格
    const toolbar = definitionsPage.slice(
      definitionsPage.indexOf('class="toolbar"'),
      definitionsPage.indexOf("definitions-policy-alert")
    );
    const positions = [
      'class="page-title"',
      "a-input-search",
      'v-model="filters.type"',
      'v-model="usageFilter"',
      "查询",
      "toolbar-actions",
      "新增因子"
    ].map(text => toolbar.indexOf(text));
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    for (const text of ['class="definitions-workbench"', "<FactorCategoryTree", "<DefinitionBatchBar", "<DefinitionTable"]) {
      expect(definitionsPage).toContain(text);
    }
    expect(definitionsPage).toContain('filterDefinitions(items.value, { ...filters, category: "" })');
  });

  it("derives the factor type from the compiled spec and shows partial universe only for cross sections", () => {
    expect(factorEditor).toContain("listDatasetColumns");
    expect(factorEditor).toContain("derived.factor_type === 'cross_section'");
    expect(factorEditor).toContain("insertTemplate('timeseries')");
    expect(factorEditor).toContain("insertTemplate('cross_section')");
    expect(factorEditor).not.toContain('v-model="form.factor_type"');
    expect(factorEditor).not.toContain("createFactorTask");
    expect(factorEditor).toContain("onBeforeRouteLeave");
  });

  it("loads recalc jobs from the backend and renders degraded notes separately from errors", () => {
    expect(recalcTab).toContain("listRecalcJobs");
    expect(recalcTab).toContain("cancelRecalcJob");
    expect(recalcModel).toContain("splitJobNote");
    expect(recalcTab).toContain("部分降级");
    expect(recalcTab).not.toContain("getRecalcJob");
  });

  it("offers lifecycle actions for compute tasks including purge delete", () => {
    expect(computeTasks).toContain("deleteFactorTask(current.task_id, purge.value)");
    expect(computeTasks).toContain("setFactorTaskStatus");
  });

  it("scopes results to the factor result view of the selected set", () => {
    expect(resultsModel).toContain('view.attributes?.view_role === "factor_result"');
    expect(resultsModel).toContain("result_dataset_id");
    expect(resultsTab).not.toContain("ViewDefinitions");
    expect(resultsTab).toContain("result-toolbar");
    expect(resultsTab).toContain("#status-extra");
    expect(resultsTab).not.toContain("status-strip");
  });
});
