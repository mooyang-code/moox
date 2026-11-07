import { describe, expect, it } from "vitest";
import { rangeParams } from "./record-range";

describe("订单、成交的时间范围", () => {
  it("相对当前时间换算成起止毫秒；全部不带时间条件", () => {
    expect(rangeParams("1d", 100_000_000)).toEqual({ start_time: "13600000", end_time: "100000000" });
    expect(rangeParams("all", 100_000_000)).toEqual({});
  });
});
