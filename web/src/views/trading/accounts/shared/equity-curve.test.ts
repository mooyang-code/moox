import { describe, expect, it } from "vitest";
import { equityChartParts, toEquitySeries } from "./equity-curve";

describe("toEquitySeries", () => {
  it("sorts persisted points and keeps nullable pnl irrelevant to chart values", () => {
    expect(
      toEquitySeries([
        { bucket_time: "2000", equity: "101", available_funds: "0", used_margin: "0", source_time: "0" },
        {
          bucket_time: "1000",
          equity: "100",
          available_funds: "0",
          used_margin: "0",
          unrealized_pnl: undefined,
          source_time: "0"
        }
      ])
    ).toEqual([
      { time: 1000, value: 100 },
      { time: 2000, value: 101 }
    ]);
  });
});

describe("equityChartParts", () => {
  it("formats the axis and tooltip in the display zone and names the zone on the axis", () => {
    const at = Date.parse("2026-11-07T01:30:00Z");
    const parts = equityChartParts([{ time: at, value: 100 }], "Asia/Shanghai");
    expect(parts.axes[0].title.text).toBe("时间（UTC+8）");
    expect(parts.axes[0].label.formatMethod(String(at))).toBe("11-07 09:30");
    expect(parts.tooltip.mark.title.value({ time: at })).toBe("2026-11-07 09:30:00 UTC+8");
  });
});
