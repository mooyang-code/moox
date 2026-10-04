import { describe, expect, it } from "vitest";
import { toEquitySeries } from "./equity-curve";

describe("toEquitySeries", () => {
  it("sorts persisted points and keeps nullable pnl irrelevant to chart values", () => {
    expect(toEquitySeries([
      { bucket_time: "2000", equity: "101", available_funds: "0", used_margin: "0", source_time: "0" },
      { bucket_time: "1000", equity: "100", available_funds: "0", used_margin: "0", unrealized_pnl: undefined, source_time: "0" }
    ])).toEqual([{ time: 1000, value: 100 }, { time: 2000, value: 101 }]);
  });
});
