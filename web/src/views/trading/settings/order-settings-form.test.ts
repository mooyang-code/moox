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
});
