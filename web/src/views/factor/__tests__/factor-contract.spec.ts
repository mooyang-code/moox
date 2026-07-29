import { describe, expect, it } from "vitest";
import type { EngineStatus, FactorDef, RecalcFactorReq } from "@/api/factor/types";
import { validateFactorParamsJSON } from "@/views/factor/definitions/factor-form";
import factorDefinitionsView from "@/views/factor/definitions/index.vue?raw";

describe("factor management contract", () => {
  it("uses explicit generic time-series fields", () => {
    const factor: FactorDef = {
      factor_id: "bias",
      name: "Bias",
      source_code: "def compute(df, params): return {}",
      input_columns: ["nav", "benchmark_return"],
      outputs: ["excess_return", "rolling_rank"],
      params_json: `{"window":20}`,
      lookback_rows: 100,
      status: "enabled"
    };
    expect(factor.input_columns).toEqual(["nav", "benchmark_return"]);
    expect(factor.outputs).toEqual(["excess_return", "rolling_rank"]);
  });

  it("uses synchronous half-open range recalc", () => {
    const request: RecalcFactorReq = {
      space_id: "crypto",
      source_dataset: "bars",
      subject_id: "BTC-USDT",
      freq: "1m",
      start_time: "2026-07-26T00:00:00Z",
      end_time: "2026-07-27T00:00:00Z"
    };
    expect(new Date(request.start_time).getTime()).toBeLessThan(new Date(request.end_time).getTime());
  });

  it("exposes only aggregate queue status", () => {
    const status: EngineStatus = {
      ret_info: { code: 0, msg: "success" },
      queue_depth: 2,
      queue_overflow_count: 1
    };
    expect(status).toEqual(expect.objectContaining({ queue_depth: 2, queue_overflow_count: 1 }));
  });

  it("validates params without rewriting large JSON numbers", () => {
    const raw = ` { "large": 9007199254740993, "huge": 1e400 } `;
    expect(validateFactorParamsJSON(raw)).toBe(raw.trim());
    expect(validateFactorParamsJSON("  ")).toBe("{}");
    expect(() => validateFactorParamsJSON("[]")).toThrow("JSON object");
  });

  it("changes status only through SetFactorStatus", () => {
    expect(factorDefinitionsView).toContain('<a-select v-model="form.status" disabled>');
    expect(factorDefinitionsView).toContain("await setFactorStatus(record.factor_id, next)");
  });
});
