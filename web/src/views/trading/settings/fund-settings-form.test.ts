import { describe, expect, it } from "vitest";
import {
  buildFundSettingsRequest,
  defaultFundSettings,
  fundSettingsToModel,
  parseAssetList,
  showsLiveOnlyFields,
  showsMarginMonitor,
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
    ).toContain("必须包含结算币 USDT");
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

  it("follows the logical account settlement asset instead of hard-coding USDT", () => {
    const base = fundSettingsToModel();
    const usdc = { settlementAsset: "usdc" };
    expect(validateFundSettingsModel({ ...base, margin_coins: [{ asset: "USDT", value: "10" }] }, usdc)).toBe("");
    expect(validateFundSettingsModel({ ...base, margin_coins: [{ asset: "USDC", value: "10" }] }, usdc)).toContain("结算币");
    const stable = (rows: Array<[string, string]>) => ({
      ...base,
      stablecoin_margin: rows.map(([asset, value]) => ({ asset, value }))
    });
    expect(
      validateFundSettingsModel(
        stable([
          ["USDT", "0.5"],
          ["FDUSD", "0.5"]
        ]),
        usdc
      )
    ).toContain("必须包含结算币 USDC");
    expect(
      validateFundSettingsModel(
        stable([
          ["USDC", "0.5"],
          ["USDT", "0.5"]
        ]),
        usdc
      )
    ).toBe("");
  });

  describe("保证金率监控", () => {
    const withMargin = (extra: Partial<ReturnType<typeof fundSettingsToModel>>) => ({ ...fundSettingsToModel(), ...extra });

    it("读不到的字段按默认值显示：三个阈值留空、监控开着", () => {
      const model = fundSettingsToModel({ reserve_ratio: "0" } as Parameters<typeof fundSettingsToModel>[0]);
      expect(model).toMatchObject({
        margin_ratio_threshold: "",
        unified_mmr_threshold: "",
        margin_reduce_ratio: "",
        margin_monitor_disabled: false
      });
      expect(
        fundSettingsToModel({
          reserve_ratio: "0",
          bnb_discount: false,
          bnb_reserve_amount: "0",
          dust_convert_enabled: false,
          dust_convert_interval_days: 15,
          margin_ratio_threshold: "0.4",
          unified_mmr_threshold: "1.3",
          margin_reduce_ratio: "0.2",
          margin_monitor_disabled: true
        })
      ).toMatchObject({
        margin_ratio_threshold: "0.4",
        unified_mmr_threshold: "1.3",
        margin_reduce_ratio: "0.2",
        margin_monitor_disabled: true
      });
    });

    it("留空取默认值，校验通过", () => {
      expect(validateFundSettingsModel(withMargin({}))).toBe("");
      expect(validateFundSettingsModel(withMargin({ margin_ratio_threshold: "  " }))).toBe("");
    });

    it("保证金率阈值必须大于 0 且不超过 10", () => {
      for (const value of ["0", "10.01", "-1", "abc", "1e-1"]) {
        expect(validateFundSettingsModel(withMargin({ margin_ratio_threshold: value }))).toContain("普通账户触发阈值");
      }
      for (const value of ["0.01", "0.5", "10"]) {
        expect(validateFundSettingsModel(withMargin({ margin_ratio_threshold: value }))).toBe("");
      }
    });

    it("uniMMR 阈值必须大于 1 且不超过 100", () => {
      for (const value of ["1", "0.9", "100.5", "x"]) {
        expect(validateFundSettingsModel(withMargin({ unified_mmr_threshold: value }))).toContain("统一账户触发阈值");
      }
      for (const value of ["1.01", "1.5", "100"]) {
        expect(validateFundSettingsModel(withMargin({ unified_mmr_threshold: value }))).toBe("");
      }
    });

    it("减仓比例必须大于 0 且小于 1", () => {
      for (const value of ["0", "1", "1.5", "-0.1", "x"]) {
        expect(validateFundSettingsModel(withMargin({ margin_reduce_ratio: value }))).toContain("触发后减仓比例");
      }
      for (const value of ["0.0001", "0.1", "0.9999"]) {
        expect(validateFundSettingsModel(withMargin({ margin_reduce_ratio: value }))).toBe("");
      }
    });

    it("监控关闭时填了不合法的值也照样拒绝，和后端一致", () => {
      expect(validateFundSettingsModel(withMargin({ margin_monitor_disabled: true, margin_reduce_ratio: "1" }))).toContain(
        "触发后减仓比例"
      );
    });

    it("只有模拟盘不显示；含非币安成员的实盘组合照常显示和校验", () => {
      expect(showsMarginMonitor()).toBe(true);
      expect(showsMarginMonitor({ nonBinance: true })).toBe(true);
      expect(showsMarginMonitor({ paper: true })).toBe(false);
      const bad = withMargin({ margin_reduce_ratio: "1" });
      expect(validateFundSettingsModel(bad, { nonBinance: true })).toContain("触发后减仓比例");
      expect(validateFundSettingsModel(bad, { paper: true })).toBe("");
    });

    it("提交时去掉空白，空值不带字段；开关按所填提交", () => {
      const request = buildFundSettingsRequest(
        withMargin({
          margin_ratio_threshold: " 0.4 ",
          unified_mmr_threshold: "",
          margin_reduce_ratio: "0.2",
          margin_monitor_disabled: true
        })
      );
      expect(request).toMatchObject({
        margin_ratio_threshold: "0.4",
        margin_reduce_ratio: "0.2",
        margin_monitor_disabled: true
      });
      expect(request.unified_mmr_threshold).toBeUndefined();
      // 含非币安成员的实盘组合（例如 OKX）照样提交
      expect(buildFundSettingsRequest(withMargin({ margin_ratio_threshold: "0.4" }), { nonBinance: true })).toMatchObject({
        margin_ratio_threshold: "0.4"
      });
    });

    it("模拟盘按默认值提交，不带过期的旧值", () => {
      const stale = withMargin({
        margin_ratio_threshold: "0.4",
        unified_mmr_threshold: "1.3",
        margin_reduce_ratio: "0.2",
        margin_monitor_disabled: true
      });
      const request = buildFundSettingsRequest(stale, { paper: true });
      expect(request.margin_ratio_threshold).toBeUndefined();
      expect(request.unified_mmr_threshold).toBeUndefined();
      expect(request.margin_reduce_ratio).toBeUndefined();
      expect(request.margin_monitor_disabled).toBe(false);
    });

    it("读不出来时按默认值重置：监控开着、阈值都用默认", () => {
      const request = buildFundSettingsRequest(fundSettingsToModel(defaultFundSettings()));
      expect(request.margin_monitor_disabled).toBe(false);
      expect(request.margin_ratio_threshold).toBeUndefined();
    });
  });
});
