import { describe, expect, it } from "vitest";
import { equitySeries, formatUtcTime, mergeBars, parseMetrics, parseUtcInput, recentUtcRange, replayStatusLabel } from "./model";

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
        fee: 0,
        holdings: 0,
        frozen: 0,
        skip_reason: "",
        unfilled: 0,
        liquidated: 0
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
        fee: 0,
        holdings: 0,
        frozen: 0,
        skip_reason: "",
        unfilled: 0,
        liquidated: 0
      }
    ]);
    expect(series).toHaveLength(1);
    expect(replayStatusLabel("running")).toBe("运行中");
  });

  it("keeps annualized return optional", () => {
    expect(parseMetrics('{"bars":3,"annualized_return":0.5}')?.annualized_return).toBe(0.5);
    expect(parseMetrics('{"bars":3}')?.annualized_return).toBeNull();
  });

  it("formats UTC times, merges bars incrementally and reads skip reasons", () => {
    expect(formatUtcTime("2026-09-01T03:00:00Z")).toBe("2026-09-01 03:00 UTC");
    expect(formatUtcTime("")).toBe("-");
    const bar = (time: string) => ({
      bar_end_time: time,
      status: "ok",
      targets: [],
      positions_json: "{}",
      summary_json: "{}",
      bar_return: 0,
      equity: 1,
      turnover: 0,
      fee: 0,
      holdings: 0,
      frozen: 0,
      skip_reason: "",
      unfilled: 0,
      liquidated: 0
    });
    const known = [bar("2026-09-01T01:00:00Z"), bar("2026-09-01T02:00:00Z")];
    const merged = mergeBars(known, [bar("2026-09-01T02:00:00Z"), bar("2026-09-01T03:00:00Z")]);
    expect(merged.map(item => item.bar_end_time)).toEqual([
      "2026-09-01T01:00:00Z",
      "2026-09-01T02:00:00Z",
      "2026-09-01T03:00:00Z"
    ]);
    expect(mergeBars(known, [])).toBe(known);
  });

  it("parses UTC text inputs and builds recent ranges", () => {
    expect(parseUtcInput("2026-09-01 08:30")).toBe("2026-09-01T08:30:00Z");
    expect(parseUtcInput("2026-09-01")).toBe("2026-09-01T00:00:00Z");
    expect(parseUtcInput("2026-02-30 00:00")).toBeNull();
    expect(parseUtcInput("2026-09-01 24:00")).toBeNull();
    expect(parseUtcInput("09/01/2026")).toBeNull();
    expect(recentUtcRange(7, new Date("2026-10-08T15:42:10Z"))).toEqual(["2026-10-01 15:00", "2026-10-08 15:00"]);
  });
});
