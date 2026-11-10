import { describe, expect, it } from "vitest";
import type { FundSettings } from "@/api/trade/settings";
import {
  buildFundSettingsRequest,
  defaultFundSettings,
  fundSettingsToModel,
  marginMonitorHiddenNote,
  marginMonitorMixedNote,
  parseAssetList,
  showsLiveOnlyFields,
  showsMarginMonitor,
  sumDecimals,
  turnsOffMarginMonitor,
  validateFundSettingsModel,
  withStoredMarginMonitor,
  type FundFormScope,
  type FundSettingsModel
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

    it("显示条件：模拟盘、测试网、没有币安生产实盘成员隐藏；混合组合和纯币安组合显示", () => {
      expect(showsMarginMonitor()).toBe(true);
      expect(showsMarginMonitor({ nonBinance: false, testnet: false, hasBinanceLive: true })).toBe(true);
      // 混合组合：有币安生产实盘成员，也有 OKX 成员
      expect(showsMarginMonitor({ nonBinance: true, hasBinanceLive: true })).toBe(true);
      expect(showsMarginMonitor({ paper: true, hasBinanceLive: true })).toBe(false);
      expect(showsMarginMonitor({ testnet: true, hasBinanceLive: true })).toBe(false);
      // 全是 OKX 等不被监控的成员
      expect(showsMarginMonitor({ nonBinance: true, hasBinanceLive: false })).toBe(false);
      expect(showsMarginMonitor({ paper: true, testnet: true, nonBinance: true, hasBinanceLive: false })).toBe(false);
    });

    it("校验：隐藏时不拦截保存，显示时照常拦截", () => {
      const bad = withMargin({ margin_reduce_ratio: "1" });
      expect(validateFundSettingsModel(bad, { hasBinanceLive: true, nonBinance: true })).toContain("触发后减仓比例");
      for (const scope of [{ paper: true }, { testnet: true }, { nonBinance: true, hasBinanceLive: false }]) {
        expect(validateFundSettingsModel(bad, scope)).toBe("");
      }
    });

    it("隐藏时的说明按原因分别写；模拟盘由整体说明负责；混合组合在区块里另有说明", () => {
      expect(marginMonitorHiddenNote()).toBe("");
      expect(marginMonitorHiddenNote({ paper: true })).toBe("");
      expect(marginMonitorHiddenNote({ hasBinanceLive: true, nonBinance: true })).toBe("");
      expect(marginMonitorHiddenNote({ testnet: true, hasBinanceLive: true })).toContain("只对生产环境的币安实盘组合生效");
      expect(marginMonitorHiddenNote({ testnet: true, hasBinanceLive: false })).toContain("只对生产环境");
      expect(marginMonitorHiddenNote({ nonBinance: true, hasBinanceLive: false })).toContain(
        "没有会被监控的币安账户（生产实盘、已启用、开通了永续），保证金率监控不生效"
      );
      expect(marginMonitorMixedNote({ nonBinance: true, hasBinanceLive: true })).toContain("对整组减仓（含 OKX 成员）");
      expect(marginMonitorMixedNote({ hasBinanceLive: true })).toBe("");
      expect(marginMonitorMixedNote({ nonBinance: true, hasBinanceLive: false })).toBe("");
      expect(marginMonitorMixedNote({ nonBinance: true, testnet: true, hasBinanceLive: true })).toBe("");
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
    });

    const stored: FundSettings = {
      ...defaultFundSettings(),
      margin_ratio_threshold: "0.2",
      unified_mmr_threshold: "1.3",
      margin_reduce_ratio: "0.3",
      margin_monitor_disabled: true
    };
    const submitWith = (scope: FundFormScope, edited?: FundSettingsModel) =>
      buildFundSettingsRequest(withStoredMarginMonitor(edited ?? fundSettingsToModel(stored), stored, scope), scope);

    it("隐藏时提交库里已存的值，不重置为默认值", () => {
      const scopes: FundFormScope[] = [{ paper: true }, { testnet: true }, { nonBinance: true, hasBinanceLive: false }];
      for (const scope of scopes) {
        expect(submitWith(scope)).toMatchObject({
          margin_ratio_threshold: "0.2",
          unified_mmr_threshold: "1.3",
          margin_reduce_ratio: "0.3",
          margin_monitor_disabled: true
        });
      }
    });

    it("隐藏时即使表单里的值被改过（显示状态切到隐藏），也只提交库里已存的值", () => {
      const edited = { ...fundSettingsToModel(stored), margin_ratio_threshold: "9", margin_monitor_disabled: false };
      const request = submitWith({ testnet: true }, edited);
      expect(request.margin_ratio_threshold).toBe("0.2");
      expect(request.margin_monitor_disabled).toBe(true);
    });

    it("库里没存阈值时隐藏也不带字段，开关保持开启监控", () => {
      const request = buildFundSettingsRequest(
        withStoredMarginMonitor(fundSettingsToModel(), defaultFundSettings(), { paper: true }),
        { paper: true }
      );
      expect(request.margin_ratio_threshold).toBeUndefined();
      expect(request.margin_monitor_disabled).toBe(false);
    });

    it("混合组合保存不重置阈值：已存 0.2 且关闭监控，改别的项保存后原样提交", () => {
      const scope: FundFormScope = { nonBinance: true, hasBinanceLive: true };
      const model = { ...fundSettingsToModel({ ...stored, margin_ratio_threshold: "0.2" }), reserve_ratio: "0.1" };
      expect(validateFundSettingsModel(model, scope)).toBe("");
      expect(submitWith(scope, model)).toMatchObject({
        reserve_ratio: "0.1",
        margin_ratio_threshold: "0.2",
        unified_mmr_threshold: "1.3",
        margin_reduce_ratio: "0.3",
        margin_monitor_disabled: true
      });
    });

    it("读不出来时按默认值重置：监控开着、阈值都用默认", () => {
      const request = buildFundSettingsRequest(fundSettingsToModel(defaultFundSettings()));
      expect(request.margin_monitor_disabled).toBe(false);
      expect(request.margin_ratio_threshold).toBeUndefined();
    });
  });

  it("只有把监控从开着改成关闭才需要确认；模拟盘、已经关闭、保持开着都不需要", () => {
    const on = { ...defaultFundSettings(), margin_monitor_disabled: false };
    const off = { ...defaultFundSettings(), margin_monitor_disabled: true };
    expect(turnsOffMarginMonitor(on, off, false)).toBe(true);
    expect(turnsOffMarginMonitor(undefined, off, false)).toBe(true);
    expect(turnsOffMarginMonitor(off, off, false)).toBe(false);
    expect(turnsOffMarginMonitor(off, on, false)).toBe(false);
    expect(turnsOffMarginMonitor(on, on, false)).toBe(false);
    expect(turnsOffMarginMonitor(on, off, true)).toBe(false);
  });
});
