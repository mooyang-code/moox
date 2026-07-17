import { describe, expect, it } from "vitest";
import { formatMetric, formatPercent } from "./performance-format";

describe("strategy performance format", () => {
  it("formats decimal strings as percentages", () => {
    expect(formatPercent("0.0342")).toBe("3.42%");
  });
  it("does not turn insufficient data into zero", () => {
    expect(formatMetric({ status: "insufficient_data", value: null })).toBe("数据不足");
  });
});
