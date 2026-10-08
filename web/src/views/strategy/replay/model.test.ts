import { describe, expect, it } from "vitest";
import { equitySeries, parseMetrics, positionsSummary, replayStatusLabel } from "./model";

describe("replay model helpers", () => {
  it("parses finished metrics and ignores empty ones", () => {
    const metrics = parseMetrics(
      '{"bars":10,"ok_bars":9,"skipped_bars":1,"total_return":0.12,"max_drawdown":0.05,"limitations":["研究回放"],"factors":{"ma":"sha256:x"}}'
    );
    expect(metrics).toMatchObject({ bars: 10, ok_bars: 9, total_return: 0.12, limitations: ["研究回放"] });
    expect(parseMetrics("{}")).toBeNull();
    expect(parseMetrics("oops")).toBeNull();
  });

  it("builds an equity series and summarizes the ledger", () => {
    const series = equitySeries([
      {
        bar_end_time: "2026-09-01T01:00:00Z",
        status: "ok",
        targets: [],
        positions_json: "{}",
        summary_json: "{}",
        bar_return: 0,
        equity: 1,
        turnover: 0,
        fee: 0
      },
      {
        bar_end_time: "bad",
        status: "ok",
        targets: [],
        positions_json: "{}",
        summary_json: "{}",
        bar_return: 0,
        equity: 1.1,
        turnover: 0,
        fee: 0
      }
    ]);
    expect(series).toHaveLength(1);
    expect(positionsSummary('{"cash":0.2,"positions":{"A":{"frozen":true},"B":{}}}')).toEqual({
      cash: 0.2,
      holdings: 2,
      frozen: 1
    });
    expect(replayStatusLabel("running")).toBe("运行中");
  });
});
