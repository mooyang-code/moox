import { describe, expect, it } from "vitest";
import { instrumentRuleHint, orderRuleError } from "./order-rules";

const instrument = {
  instrument_id: "BTC-USDT-SWAP",
  quantity_step: "0.001",
  min_quantity: "0.002",
  min_notional: "5",
  price_tick: "0.1"
};

describe("下单规则", () => {
  it("接受符合步长的数量和限价", () => {
    expect(orderRuleError("0.003", "50000.1", instrument)).toBe("");
    expect(orderRuleError("1", undefined, instrument)).toBe("");
  });

  it("提交前拦截不合规的数量和限价", () => {
    expect(orderRuleError("0.0015", undefined, instrument)).toContain("步长");
    expect(orderRuleError("0.001", undefined, instrument)).toContain("不能小于");
    expect(orderRuleError("0.003", "50000.15", instrument)).toContain("价格步长");
    expect(orderRuleError("abc", undefined, instrument)).toContain("正数");
    // A 股卖出零股不在前端拦，由服务端按持仓判断
    expect(orderRuleError("0.0015", undefined, instrument, true)).toBe("");
  });

  it("没有规则时只检查数量格式", () => {
    expect(orderRuleError("0.12345", undefined)).toBe("");
  });

  it("显示规则提示", () => {
    expect(instrumentRuleHint(instrument)).toContain("最小金额 5");
  });
});
