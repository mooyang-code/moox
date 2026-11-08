import { describe, expect, it } from "vitest";
import {
  equitySeries,
  equitySummary,
  replayRangeLabel,
  mergeBars,
  parseBarDetail,
  parseMetrics,
  recentRange,
  replayFeeText,
  replayStatusLabel,
  replayTasksText,
  replayUsesSwap
} from "./model";

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

  it("merges bars incrementally and reads skip reasons", () => {
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

  it("builds recent ranges ending at the current whole hour", () => {
    const now = Date.parse("2026-10-08T15:42:10Z");
    expect(recentRange(7, now)).toEqual([Date.parse("2026-10-01T15:00:00Z"), Date.parse("2026-10-08T15:00:00Z")]);
  });

  it("parses a bar's ledger and summary for the detail row", () => {
    const bar = {
      bar_end_time: "2026-09-01T01:00:00Z",
      status: "ok",
      targets: [],
      positions_json:
        '{"cash":0.5,"positions":{"B":{"quantity":2,"last_price":0.25,"value":0.5,"frozen":true,"missing_bars":2},"A":{"quantity":1,"last_price":1,"value":1}}}',
      summary_json: '{"decision":{"notes":["说明"]},"ledger":{"traded":0.3,"fee":0.001,"unfilled":["C"],"liquidated":["D"]}}',
      bar_return: 0,
      equity: 2,
      turnover: 0,
      fee: 0,
      holdings: 2,
      frozen: 1,
      skip_reason: "",
      unfilled: 1,
      liquidated: 1
    };
    const detail = parseBarDetail(bar);
    expect(detail.cash).toBe(0.25);
    expect(detail.positions.map(position => position.id)).toEqual(["A", "B"]);
    expect(detail.positions[1]).toMatchObject({ frozen: true, missing_bars: 2 });
    expect(detail).toMatchObject({
      traded: 0.3,
      fee: 0.001,
      unfilled: ["C"],
      liquidated: ["D"],
      notes: ["说明"],
      buyScale: null
    });
    expect(parseBarDetail({ ...bar, positions_json: "", summary_json: "oops" })).toMatchObject({
      positions: [],
      notes: [],
      cash: 0,
      buyScale: null
    });
    expect(parseBarDetail({ ...bar, summary_json: '{"ledger":{"buy_scale":0}}' }).buyScale).toBe(0);
    expect(parseBarDetail({ ...bar, summary_json: '{"ledger":{"buy_scale":0.4}}' }).buyScale).toBe(0.4);
  });

  it("labels positions by market, orders by absolute value and keeps shorts negative", () => {
    const bar = {
      bar_end_time: "2026-09-01T01:00:00Z",
      status: "ok",
      targets: [],
      positions_json:
        '{"cash":10,"positions":{"A|spot":{"quantity":1,"last_price":10,"value":10},"B|swap":{"quantity":-5,"last_price":20,"value":-100}}}',
      summary_json: '{"ledger":{"unfilled":["C|swap"],"liquidated":["D|spot"]}}',
      bar_return: 0,
      equity: 20,
      turnover: 0,
      fee: 0,
      spot_value: 10,
      swap_value: -100,
      holdings: 2,
      frozen: 0,
      skip_reason: "",
      unfilled: 1,
      liquidated: 1
    };
    const detail = parseBarDetail(bar);
    expect(detail.positions.map(position => position.label)).toEqual(["B（永续）", "A（现货）"]);
    expect(detail.positions[0]).toMatchObject({ id: "B|swap", value: -100, quantity: -5 });
    expect(detail.unfilled).toEqual(["C（永续）"]);
    expect(detail.liquidated).toEqual(["D（现货）"]);
  });

  it("describes the replay tasks and fees by market", () => {
    const single = { tasks: { spot: { factor_task_id: "ftask_a", view_id: "v" } }, spot_fee_bps: 10, swap_fee_bps: 0 };
    const mixed = {
      tasks: { swap: { factor_task_id: "ftask_b", view_id: "w" }, spot: { factor_task_id: "ftask_a", view_id: "v" } },
      spot_fee_bps: 10,
      swap_fee_bps: 5
    };
    expect(replayTasksText(single)).toBe("ftask_a");
    expect(replayTasksText(mixed)).toBe("现货 ftask_a、永续 ftask_b");
    expect(replayFeeText(single)).toBe("10 bps");
    expect(replayFeeText(mixed)).toBe("现货 10 bps、永续 5 bps");
    expect(replayUsesSwap(single)).toBe(false);
    expect(replayUsesSwap(mixed)).toBe(true);
    expect(replayUsesSwap(null)).toBe(false);
    const unified = { tasks: { multi: { factor_task_id: "ftask_u", view_id: "u" } }, spot_fee_bps: 10, swap_fee_bps: 5 };
    expect(replayUsesSwap(unified), "统一数据集的任务同时带现货与永续").toBe(true);
    expect(replayFeeText(unified)).toBe("现货 10 bps、永续 5 bps");
  });

  it("summarizes the equity curve in text", () => {
    const point = (time: string, equity: number) => ({
      bar_end_time: time,
      status: "ok",
      targets: [],
      positions_json: "{}",
      summary_json: "{}",
      bar_return: 0,
      equity,
      turnover: 0,
      fee: 0,
      holdings: 0,
      frozen: 0,
      skip_reason: "",
      unfilled: 0,
      liquidated: 0
    });
    expect(equitySummary([], "UTC")).toBe("暂无权益曲线");
    const points = [point("2026-09-01T01:00:00Z", 1), point("2026-09-01T02:00:00Z", 1.2), point("2026-09-01T03:00:00Z", 0.9)];
    expect(equitySummary(points, "UTC")).toBe(
      "权益曲线：共 3 根，2026-09-01 01:00:00 UTC 权益 1.0000，2026-09-01 03:00:00 UTC 权益 0.9000，最高 1.2000，最低 0.9000"
    );
    expect(equitySummary(points, "Asia/Shanghai")).toContain("2026-09-01 09:00:00 UTC+8 权益 1.0000");
  });

  it("labels A-share daily ranges with dates in the space zone, other ranges with times", () => {
    const sh = "Asia/Shanghai";
    expect(
      replayRangeLabel({ calendar: "cn_stock", start_time: "2026-08-31T16:00:00Z", end_time: "2026-09-30T16:00:00Z" }, sh)
    ).toBe("2026-09-01 UTC+8 → 2026-10-01 UTC+8（不含结束日）");
    expect(
      replayRangeLabel({ calendar: "crypto_24x7", start_time: "2026-09-01T00:00:00Z", end_time: "2026-09-02T00:00:00Z" }, "UTC")
    ).toBe("2026-09-01 00:00 UTC → 2026-09-02 00:00 UTC");
    // A 股日内的区间是真实时刻，按空间时区写时刻。
    expect(
      replayRangeLabel({ calendar: "cn_stock", start_time: "2026-10-08T03:00:00Z", end_time: "2026-10-08T05:30:00Z" }, sh)
    ).toBe("2026-10-08 11:00 UTC+8 → 2026-10-08 13:30 UTC+8");
  });
});
