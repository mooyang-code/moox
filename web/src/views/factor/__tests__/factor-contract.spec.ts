import { describe, expect, it } from "vitest";
import type { EngineStatus, FactorDef, FactorSet, RecalcFactorsReq, RecalcJob } from "@/api/factor/types";
import { validateFactorParamsJSON } from "@/views/factor/definitions/factor-form";
import factorDefinitionsView from "@/views/factor/definitions/index.vue?raw";
import factorResultsView from "@/views/factor/results/index.vue?raw";
import factorSetsView from "@/views/factor/sets/index.vue?raw";
import factorTasksView from "@/views/factor/tasks/index.vue?raw";

describe("factor management contract", () => {
  it("uses explicit generic time-series fields", () => {
    const factor: FactorDef = {
      factor_id: "Bias",
      set_id: "factor_set_crypto_1m",
      factor_type: "timeseries",
      name: "Bias",
      source_code: "def compute(df, params): return {}",
      input_columns: ["nav", "benchmark_return"],
      outputs: ["excess_return", "rolling_rank"],
      params_json: `{"window":20}`,
      lookback_periods: 100,
      status: "enabled"
    };
    expect(factor.input_columns).toEqual(["nav", "benchmark_return"]);
    expect(factor.outputs).toEqual(["excess_return", "rolling_rank"]);
    expect(factor.lookback_periods).toBe(100);
  });

  it("accepts set-scoped async recalc jobs with a request id", () => {
    const request: RecalcFactorsReq = {
      set_id: "factor_set_crypto_1m",
      factor_ids: ["Bias"],
      subjects: ["BTC-USDT"],
      request_id: "recalc-1",
      start_time: "2026-07-26T00:00:00Z",
      end_time: "2026-07-27T00:00:00Z"
    };
    expect(request.request_id).toBe("recalc-1");
    expect(new Date(request.start_time).getTime()).toBeLessThan(new Date(request.end_time).getTime());
    const job: RecalcJob = {
      job_id: "job-1",
      request_id: "recalc-1",
      set_id: request.set_id,
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
    expect(job.progress_time).toBe(request.start_time);
  });

  it("exposes global Python worker and task status", () => {
    const status: EngineStatus = {
      ret_info: { code: 0, msg: "success" },
      python_workers: 100,
      python_busy: 2,
      lanes: [],
      recent_runs: []
    };
    expect(status).toEqual(expect.objectContaining({ python_workers: 100, python_busy: 2, lanes: [] }));
    expect(factorResultsView).toContain('Consumer {{ engineStatus.consumer_running ? "运行中" : "停止" }}');
    expect(factorResultsView).toContain("engineStatus.python_busy");
    expect(factorResultsView).toContain("listFactorSets");
    expect(factorResultsView).not.toContain("ViewDefinitions");
    expect(factorResultsView).toContain("listViews");
    expect(factorResultsView).toContain('view.attributes?.view_role === "factor_result"');
  });

  it("lists factor sets and scopes definitions to the selected set", () => {
    const set: FactorSet = {
      set_id: "factor_set_crypto_1m",
      space_id: "crypto",
      source_dataset_id: "dataset_crypto_kline_1m",
      freq: "1m",
      subject_mode: "all",
      subjects: [],
      result_dataset_id: "dataset_crypto_kline_1m_factor",
      status: "enabled"
    };
    expect(set.result_dataset_id).toMatch(/^dataset_/);
    expect(factorSetsView).toContain("listFactorSets");
    expect(factorSetsView).toContain("createFactorSet");
    expect(factorSetsView).toContain("setFactorSetStatus");
    expect(factorDefinitionsView).toContain("selectedSetId");
    expect(factorDefinitionsView).toContain("listFactors");
    expect(factorDefinitionsView).toContain("set_id: selectedSetId.value");
    expect(factorDefinitionsView).toContain("record.status !== 'disabled'");
  });

  it("submits, polls, and cancels set-scoped recalc jobs", () => {
    expect(factorTasksView).toContain("recalcFactors");
    expect(factorTasksView).toContain("getRecalcJob");
    expect(factorTasksView).toContain("cancelRecalcJob");
    expect(factorTasksView).toContain("progress_time");
    expect(factorTasksView).toContain("set_id");
  });

  it("validates params without rewriting large JSON numbers", () => {
    const raw = ` { "large": 9007199254740993, "huge": 1e400 } `;
    expect(validateFactorParamsJSON(raw)).toBe(raw.trim());
    expect(validateFactorParamsJSON("  ")).toBe("{}");
    expect(() => validateFactorParamsJSON("[]")).toThrow("JSON object");
  });

  it("changes status only through SetFactorStatus and edits disabled definitions", () => {
    expect(factorDefinitionsView).toContain('<a-select v-model="form.status" disabled>');
    expect(factorDefinitionsView).toContain("await setFactorStatus(record.factor_id, next)");
    expect(factorDefinitionsView).toContain("record.status !== 'disabled'");
  });

  it("uses the period-based runtime contract in the editor", () => {
    expect(factorDefinitionsView).toContain('data-index="lookback_periods"');
    expect(factorDefinitionsView).toContain("return result");
  });

  it("declares the execution type in the definition editor", () => {
    expect(factorDefinitionsView).toContain('v-model="form.factor_type"');
    expect(factorDefinitionsView).toContain('value="timeseries"');
    expect(factorDefinitionsView).toContain('value="cross_section"');
    expect(factorDefinitionsView).toContain('factor_type: "timeseries"');
  });

  it("keeps source hash and source code in the factor detail drawer", () => {
    expect(factorDefinitionsView).not.toContain('title="源码Hash" data-index="source_hash"');
    expect(factorDefinitionsView).toContain('label="源码Hash"');
    expect(factorDefinitionsView).toContain("CodeBlock");
    expect(factorDefinitionsView).toContain('language="python"');
  });
});
