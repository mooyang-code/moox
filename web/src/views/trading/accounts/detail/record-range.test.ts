import { describe, expect, it } from "vitest";
import { pnlRanges } from "./pnl-model";
import { rangeFromQuery, rangeParams } from "./record-range";

describe("订单、成交的时间范围", () => {
  it("相对当前时间换算成起止毫秒；全部不带时间条件", () => {
    expect(rangeParams("1d", 100_000_000)).toEqual({ start_time: "13600000", end_time: "100000000" });
    expect(rangeParams("all", 100_000_000)).toEqual({});
  });

  it("收益拆解的可选范围不含“全部”，也不超过后端允许的 90 天", () => {
    expect(pnlRanges.length).toBeGreaterThan(0);
    for (const item of pnlRanges) {
      expect(item.ms).toBeGreaterThan(0);
      expect(item.ms).toBeLessThanOrEqual(90 * 86_400_000);
    }
  });
});

describe("地址里的时间范围", () => {
  it("认识的取原值，其余用近 7 天", () => {
    expect(rangeFromQuery("30d")).toBe("30d");
    expect(rangeFromQuery("all")).toBe("all");
    expect(rangeFromQuery("bad")).toBe("7d");
    expect(rangeFromQuery(undefined)).toBe("7d");
  });
});
