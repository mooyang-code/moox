import { describe, expect, it } from "vitest";
import {
  buildOrderSettingsRequest,
  defaultOrderSettings,
  orderSettingsToModel,
  validateOrderSettingsModel
} from "./order-settings-form";

describe("order settings form", () => {
  it("fills defaults when the server returns no settings", () => {
    const model = orderSettingsToModel();
    expect(model.rebalance_threshold_mode).toBe("TARGET_RATIO");
    expect(model.order_mode).toBe("PSEUDO_MARKET");
    expect(model.hourly_cap_spot).toEqual([]);
    expect(validateOrderSettingsModel(model)).toBe("");
  });

  it("turns cap rows into an upper-cased map and omits empty maps", () => {
    const model = orderSettingsToModel({
      ...defaultOrderSettings(),
      hourly_cap_swap: { "BTC-USDT-SWAP": "5000", "*": "1000" }
    });
    model.hourly_cap_swap.find(row => row.key === "BTC-USDT-SWAP")!.key = " btc-usdt-swap ";
    const request = buildOrderSettingsRequest(model);
    expect(request.hourly_cap_swap).toEqual({ "BTC-USDT-SWAP": "5000", "*": "1000" });
    expect(request.hourly_cap_spot).toBeUndefined();
  });

  it("rejects values the server would reject", () => {
    const bad = (patch: Partial<ReturnType<typeof orderSettingsToModel>>) =>
      validateOrderSettingsModel({ ...orderSettingsToModel(), ...patch });
    expect(bad({ resting_ratio: "1.5" })).toContain("挂单比例");
    expect(bad({ rebalance_threshold_ratio: "abc" })).toContain("调仓阈值比例");
    expect(bad({ max_child_notional: "0" })).toContain("单笔上限");
    expect(bad({ slippage_step: "0.02", slippage_max: "0.01" })).toContain("滑点上限");
    expect(bad({ max_attempts: 0 })).toContain("尝试次数");
    expect(bad({ batch_interval_seconds: -1 })).toContain("批间隔");
    expect(bad({ hourly_cap_spot: [{ key: "BTC-USDT-SWAP", value: "1" }] })).toContain("现货");
    expect(
      bad({
        hourly_cap_swap: [
          { key: "btc-usdt-swap", value: "1" },
          { key: "BTC-USDT-SWAP", value: "2" }
        ]
      })
    ).toContain("重复");
  });

  it("A 股组合只有一项最小下单额：标签不带市场，不校验合约那项", () => {
    const model = { ...orderSettingsToModel(), min_notional_swap: "abc" };
    expect(validateOrderSettingsModel(model)).toContain("合约最小下单额");
    expect(validateOrderSettingsModel(model, true)).toBe("");
    expect(validateOrderSettingsModel({ ...model, min_notional_spot: "x" }, true)).toBe("最小下单额必须是不小于 0 的数字");
  });

  it("A 股组合只有一组每小时上限：标的写股票代码，不要求 -SPOT/-SWAP 后缀，不校验合约那组", () => {
    const base = orderSettingsToModel();
    const stock = (patch: Partial<typeof base>) => validateOrderSettingsModel({ ...base, ...patch }, true);
    expect(stock({ hourly_cap_spot: [{ key: "600519.xshg", value: "100000" }] })).toBe("");
    expect(stock({ hourly_cap_spot: [{ key: "*", value: "1" }] })).toBe("");
    expect(stock({ hourly_cap_spot: [{ key: "BTC-USDT-SPOT", value: "1" }] })).toBe("每小时上限的标的写 * 或形如 600519.XSHG");
    expect(stock({ hourly_cap_swap: [{ key: "abc", value: "1" }] })).toBe("");
    // 加密组合仍按后缀校验
    expect(validateOrderSettingsModel({ ...base, hourly_cap_spot: [{ key: "600519.XSHG", value: "1" }] })).toContain("现货");
  });
});
