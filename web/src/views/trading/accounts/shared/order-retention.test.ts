import { describe, expect, it } from "vitest";
import { fmtOrderRetention, orderRetentionDaysOf, orderRetentionError } from "./order-retention";

describe("订单保留天数", () => {
  it("0～365 的整数合法，其余报错", () => {
    for (const value of ["0", "5", " 30 ", "365"]) expect(orderRetentionError(value)).toBe("");
    for (const value of ["", "-1", "366", "1.5", "abc"]) expect(orderRetentionError(value)).toContain("0～365");
  });

  it("后端没给天数时按默认 5 天，0 显示不清理", () => {
    expect(orderRetentionDaysOf({ paper: undefined })).toBe(5);
    expect(
      orderRetentionDaysOf({ paper: { initial_balance: "1", maker_fee_rate: "0", taker_fee_rate: "0", slippage_bps: "0" } })
    ).toBe(5);
    expect(
      orderRetentionDaysOf({
        paper: { initial_balance: "1", maker_fee_rate: "0", taker_fee_rate: "0", slippage_bps: "0", order_retention_days: 0 }
      })
    ).toBe(0);
    expect(fmtOrderRetention(0)).toBe("不清理");
    expect(fmtOrderRetention(7)).toBe("7 天");
  });
});
