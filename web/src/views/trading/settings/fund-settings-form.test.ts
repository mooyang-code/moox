import { describe, expect, it } from "vitest";
import {
  buildFundSettingsRequest,
  fundSettingsToModel,
  parseAssetList,
  showsLiveOnlyFields,
  sumDecimals,
  validateFundSettingsModel
} from "./fund-settings-form";

describe("fund settings form", () => {
  it("passes with defaults", () => {
    expect(validateFundSettingsModel(fundSettingsToModel())).toBe("");
  });

  it("sums decimals exactly", () => {
    expect(sumDecimals(["0.1", "0.2"])).toBe("0.3");
    expect(sumDecimals(["0.5", "0.25", "0.25"])).toBe("1");
    expect(sumDecimals(["0.3333", "0.3333", "0.3333"])).toBe("0.9999");
  });

  it("validates stablecoin margin like the server", () => {
    const model = (rows: Array<[string, string]>) => ({
      ...fundSettingsToModel(),
      stablecoin_margin: rows.map(([asset, value]) => ({ asset, value }))
    });
    expect(
      validateFundSettingsModel(
        model([
          ["USDT", "0.6"],
          ["USDC", "0.4"]
        ])
      )
    ).toBe("");
    expect(
      validateFundSettingsModel(
        model([
          ["USDT", "0.5"],
          ["USDC", "0.4"]
        ])
      )
    ).toContain("之和必须等于 1");
    expect(
      validateFundSettingsModel(
        model([
          ["USDC", "0.5"],
          ["FDUSD", "0.5"]
        ])
      )
    ).toContain("必须包含 USDT");
    expect(
      validateFundSettingsModel(
        model([
          ["usdt", "0.5"],
          ["USDT", "0.5"]
        ])
      )
    ).toContain("重复");
    expect(
      validateFundSettingsModel(
        model([
          ["USDT", "0"],
          ["USDC", "1"]
        ])
      )
    ).toContain("大于 0");
  });

  it("rejects overlapping asset groups and bad numbers", () => {
    const base = fundSettingsToModel();
    expect(
      validateFundSettingsModel({ ...base, margin_coins: [{ asset: "eth", value: "10" }], arbitrage_base_assets: "ETH" })
    ).toContain("同时出现");
    expect(validateFundSettingsModel({ ...base, reserve_ratio: "1" })).toContain("资金预留比例");
    expect(validateFundSettingsModel({ ...base, bnb_reserve_amount: "-1" })).toContain("BNB 保留金额");
    expect(validateFundSettingsModel({ ...base, dust_convert_interval_days: 0 })).toContain("间隔");
  });

  it("builds an upper-cased request and omits empty groups", () => {
    const model = {
      ...fundSettingsToModel(),
      stablecoin_margin: [
        { asset: "usdt", value: "0.6" },
        { asset: "usdc", value: "0.4" }
      ],
      arbitrage_base_assets: "btc, eth  sol"
    };
    const request = buildFundSettingsRequest(model);
    expect(request.stablecoin_margin).toEqual({ USDT: "0.6", USDC: "0.4" });
    expect(request.arbitrage_base_assets).toEqual(["BTC", "ETH", "SOL"]);
    expect(request.margin_coins).toBeUndefined();
    expect(parseAssetList("")).toEqual([]);
  });

  it("matches the server on settlement asset, BNB overlap and the BNB reserve floor", () => {
    const base = fundSettingsToModel();
    expect(validateFundSettingsModel({ ...base, margin_coins: [{ asset: "usdt", value: "10" }] })).toContain("结算币");
    expect(validateFundSettingsModel({ ...base, arbitrage_base_assets: "USDT" })).toContain("结算币");
    expect(
      validateFundSettingsModel({ ...base, bnb_reserve_amount: "200", margin_coins: [{ asset: "BNB", value: "10" }] })
    ).toContain("BNB 不能同时出现");
    expect(validateFundSettingsModel({ ...base, margin_coins: [{ asset: "BNB", value: "10" }] })).toBe("");
    for (const amount of ["0.5", "10"]) {
      expect(validateFundSettingsModel({ ...base, bnb_reserve_amount: amount })).toContain("大于 10");
    }
    expect(validateFundSettingsModel({ ...base, bnb_reserve_amount: "10.5" })).toBe("");
  });

  it("hides and ignores live-only fields for paper and non-Binance combos", () => {
    const stale = {
      ...fundSettingsToModel({
        reserve_ratio: "0.05",
        bnb_discount: true,
        bnb_reserve_amount: "100",
        dust_convert_enabled: true,
        dust_convert_interval_days: 15,
        stablecoin_margin: { USDT: "0.5", USDC: "0.4" }
      })
    };
    for (const scope of [{ paper: true }, { nonBinance: true }]) {
      expect(showsLiveOnlyFields(scope)).toBe(false);
      // 隐藏项的旧值不参与校验，也不会被提交。
      expect(validateFundSettingsModel(stale, scope)).toBe("");
      const request = buildFundSettingsRequest(stale, scope);
      expect(request).toMatchObject({
        reserve_ratio: "0.05",
        bnb_discount: false,
        bnb_reserve_amount: "0",
        dust_convert_enabled: false
      });
      expect(request.stablecoin_margin).toBeUndefined();
    }
    expect(showsLiveOnlyFields()).toBe(true);
    expect(validateFundSettingsModel(stale)).toContain("之和必须等于 1");
  });
});
