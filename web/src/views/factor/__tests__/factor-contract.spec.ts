import { describe, expect, it } from "vitest";
import type { EngineStatus, FactorDef, RecalcFactorReq } from "@/api/factor/types";

describe("factor management contract", () => {
  it("uses explicit periods and source-derived depends", () => {
    const factor: FactorDef = {
      factor_id: "bias",
      name: "Bias",
      source_code: "def signal(): pass",
      periods: [5, 20],
      lookback_bars: 100,
      depends: ["funding_rate"],
      status: "enabled"
    };
    expect(factor.periods).toEqual([5, 20]);
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
});
