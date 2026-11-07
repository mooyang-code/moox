import type { FundSettings } from "@/api/trade/settings";

/** 资产加一个数值的一行：稳定币的目标比例，或保证金币的配置值。 */
export interface AssetRow {
  asset: string;
  value: string;
}

export interface FundSettingsModel {
  reserve_ratio: string;
  bnb_discount: boolean;
  bnb_reserve_amount: string;
  dust_convert_enabled: boolean;
  dust_convert_interval_days: number;
  stablecoin_margin: AssetRow[];
  margin_coins: AssetRow[];
  /** 套利底仓，逗号或空格分隔 */
  arbitrage_base_assets: string;
  /** 以下三项空表示用默认值 */
  margin_ratio_threshold: string;
  unified_mmr_threshold: string;
  margin_reduce_ratio: string;
  margin_monitor_disabled: boolean;
}

/** 哪些项在表单里显示：BNB、小额兑换、稳定币保证金只对币安实盘组合生效，模拟盘和含非币安成员的组合不显示；保证金率监控只对生产环境的币安实盘生效，模拟盘、测试网和含 OKX 成员的组合不显示。 */
export interface FundFormScope {
  /** 模拟盘组合没有资金维护任务 */
  paper?: boolean;
  /** 含非币安成员：BNB 与稳定币相关项只适用于币安 */
  nonBinance?: boolean;
  /** 测试网组合：保证金率监控不监控测试网 */
  testnet?: boolean;
  /** 含 OKX 成员（包括停用的）：OKX 的保证金率监控还没接入 */
  hasOkx?: boolean;
  /** 组合的结算币，默认 USDT：稳定币保证金必须含它，保证金币和套利底仓不能写它 */
  settlementAsset?: string;
}

/** BNB 补买的容差：市值低于“保留金额 − 10”才补买，所以保留金额要大于 10。 */
const BNB_RESERVE_SLACK = 10;

export function showsLiveOnlyFields(scope: FundFormScope = {}): boolean {
  return !scope.paper && !scope.nonBinance;
}

/** 保证金率监控只对生产环境的币安实盘生效：模拟盘、测试网不监控，OKX 暂未接入，含 OKX 成员的组合也不显示。 */
export function showsMarginMonitor(scope: FundFormScope = {}): boolean {
  return !scope.paper && !scope.testnet && !scope.hasOkx;
}

/** 保证金率监控不显示时给用户的说明；显示时返回空串。模拟盘的整体说明由表单另给，这里也返回空串。 */
export function marginMonitorHiddenNote(scope: FundFormScope = {}): string {
  if (showsMarginMonitor(scope) || scope.paper) return "";
  if (scope.hasOkx) return "OKX 账户的保证金率监控暂未接入，这个组合没有保证金率保护";
  return "保证金率监控只对生产环境的实盘组合生效，测试网组合不显示";
}

export function defaultFundSettings(): FundSettings {
  return {
    reserve_ratio: "0",
    bnb_discount: false,
    bnb_reserve_amount: "0",
    dust_convert_enabled: false,
    dust_convert_interval_days: 15,
    margin_ratio_threshold: "",
    unified_mmr_threshold: "",
    margin_reduce_ratio: "",
    margin_monitor_disabled: false
  };
}

function rows(values?: Record<string, string>): AssetRow[] {
  return Object.entries(values || {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([asset, value]) => ({ asset, value }));
}

export function fundSettingsToModel(settings?: FundSettings): FundSettingsModel {
  const value = { ...defaultFundSettings(), ...settings };
  return {
    reserve_ratio: value.reserve_ratio,
    bnb_discount: value.bnb_discount,
    bnb_reserve_amount: value.bnb_reserve_amount,
    dust_convert_enabled: value.dust_convert_enabled,
    dust_convert_interval_days: value.dust_convert_interval_days || 15,
    stablecoin_margin: rows(value.stablecoin_margin),
    margin_coins: rows(value.margin_coins),
    arbitrage_base_assets: (value.arbitrage_base_assets || []).join(", "),
    margin_ratio_threshold: value.margin_ratio_threshold ?? "",
    unified_mmr_threshold: value.unified_mmr_threshold ?? "",
    margin_reduce_ratio: value.margin_reduce_ratio ?? "",
    margin_monitor_disabled: Boolean(value.margin_monitor_disabled)
  };
}

const decimalPattern = /^(?:0|[1-9]\d*)(?:\.\d+)?$/;
const assetPattern = /^[A-Z0-9]{1,20}$/;

function isDecimal(value: string): boolean {
  return decimalPattern.test(value.trim());
}

/** 保证金率监控的三个数：留空取默认值；填了必须是区间内的数。区间和后端一致，开关关闭时也照样校验。 */
const marginFields = [
  {
    key: "margin_ratio_threshold",
    label: "普通账户触发阈值",
    range: "大于 0 且不超过 10",
    valid: (n: number) => n > 0 && n <= 10
  },
  {
    key: "unified_mmr_threshold",
    label: "统一账户触发阈值",
    range: "大于 1 且不超过 100",
    valid: (n: number) => n > 1 && n <= 100
  },
  {
    key: "margin_reduce_ratio",
    label: "触发后减仓比例",
    range: "大于 0 且小于 1",
    valid: (n: number) => n > 0 && n < 1
  }
] as const;

function validateMarginMonitor(model: FundSettingsModel): string {
  for (const field of marginFields) {
    const text = model[field.key].trim();
    if (text !== "" && (!isDecimal(text) || !field.valid(Number(text)))) {
      return `${field.label}必须是${field.range}的数字，留空表示用默认值`;
    }
  }
  return "";
}

/** 精确求和：按最长小数位放大成整数，避免 0.1 + 0.2 这类浮点误差。 */
export function sumDecimals(values: string[]): string {
  const scale = Math.max(0, ...values.map(value => (value.split(".")[1] || "").length));
  const total = values.reduce((sum, value) => {
    const [whole, fraction = ""] = value.trim().split(".");
    return sum + BigInt(whole + fraction.padEnd(scale, "0"));
  }, 0n);
  const digits = total.toString().padStart(scale + 1, "0");
  if (scale === 0) return digits;
  const text = `${digits.slice(0, -scale)}.${digits.slice(-scale)}`.replace(/0+$/, "");
  return text.endsWith(".") ? text.slice(0, -1) : text;
}

export function parseAssetList(text: string): string[] {
  return text
    .split(/[\s,，]+/)
    .map(item => item.trim().toUpperCase())
    .filter(Boolean);
}

/** 提交前的本地校验，规则和后端 domain/fundsettings 一致；返回第一条错误，空串表示通过。 */
export function validateFundSettingsModel(model: FundSettingsModel, scope: FundFormScope = {}): string {
  const liveOnly = showsLiveOnlyFields(scope);
  if (!isDecimal(model.reserve_ratio) || Number(model.reserve_ratio) >= 1) return "资金预留比例必须在 0 到 1 之间（不含 1）";
  if (liveOnly) {
    if (!isDecimal(model.bnb_reserve_amount)) return "BNB 保留金额必须是不小于 0 的数字";
    const reserve = Number(model.bnb_reserve_amount);
    if (reserve > 0 && reserve <= BNB_RESERVE_SLACK) {
      return `BNB 保留金额要大于 ${BNB_RESERVE_SLACK}：市值低于保留金额减 ${BNB_RESERVE_SLACK} 才会补买，不大于 ${BNB_RESERVE_SLACK} 的设置永远不会补买`;
    }
    if (
      !Number.isInteger(model.dust_convert_interval_days) ||
      model.dust_convert_interval_days < 1 ||
      model.dust_convert_interval_days > 365
    ) {
      return "小额兑换 BNB 的间隔必须是 1 到 365 天的整数";
    }
  }
  const settlement = (scope.settlementAsset || "USDT").trim().toUpperCase();
  const owners = new Map<string, string>();
  const claim = (asset: string, group: string): string => {
    if (!assetPattern.test(asset)) return `资产名 ${asset || "（空）"} 不合法（只允许字母和数字）`;
    if (asset === settlement && group !== "稳定币保证金") return `${settlement} 是这个组合的结算币，不能写进${group}`;
    const previous = owners.get(asset);
    if (previous)
      return previous === group ? `${group}里的资产 ${asset} 重复` : `资产 ${asset} 同时出现在${previous}和${group}里`;
    owners.set(asset, group);
    return "";
  };
  const stable = (liveOnly ? model.stablecoin_margin : []).map(row => ({
    asset: row.asset.trim().toUpperCase(),
    value: row.value.trim()
  }));
  for (const row of stable) {
    const error = claim(row.asset, "稳定币保证金");
    if (error) return error;
    if (!isDecimal(row.value) || Number(row.value) <= 0 || Number(row.value) > 1) {
      return `稳定币 ${row.asset} 的目标比例必须是大于 0 且不超过 1 的数字`;
    }
  }
  if (stable.length > 0) {
    if (!stable.some(row => row.asset === settlement)) return `稳定币保证金必须包含结算币 ${settlement}`;
    const sum = sumDecimals(stable.map(row => row.value));
    if (sum !== "1") return `稳定币保证金的目标比例之和必须等于 1，现在是 ${sum}`;
  }
  for (const row of model.margin_coins) {
    const asset = row.asset.trim().toUpperCase();
    const error = claim(asset, "保证金币");
    if (error) return error;
    if (!isDecimal(row.value)) return `保证金币 ${asset} 的配置值必须是不小于 0 的数字`;
  }
  for (const asset of parseAssetList(model.arbitrage_base_assets)) {
    const error = claim(asset, "套利底仓");
    if (error) return error;
  }
  if (liveOnly && Number(model.bnb_reserve_amount) > 0 && owners.has("BNB")) {
    return `开启 BNB 保留时，BNB 不能同时出现在${owners.get("BNB")}里`;
  }
  return showsMarginMonitor(scope) ? validateMarginMonitor(model) : "";
}

function recordOf(list: AssetRow[]): Record<string, string> | undefined {
  if (list.length === 0) return undefined;
  return Object.fromEntries(list.map(row => [row.asset.trim().toUpperCase(), row.value.trim()]));
}

/** 空串按“用默认值”提交：不带这个字段。 */
const orUndefined = (value: string): string | undefined => value.trim() || undefined;

/** 不显示的项（模拟盘、测试网、含 OKX 或非币安成员）一律按默认值提交，不带过期的旧值。 */
export function buildFundSettingsRequest(model: FundSettingsModel, scope: FundFormScope = {}): FundSettings {
  const base = parseAssetList(model.arbitrage_base_assets);
  const liveOnly = showsLiveOnlyFields(scope);
  const defaults = defaultFundSettings();
  const margin = showsMarginMonitor(scope);
  return {
    reserve_ratio: model.reserve_ratio.trim(),
    bnb_discount: liveOnly ? model.bnb_discount : defaults.bnb_discount,
    bnb_reserve_amount: liveOnly ? model.bnb_reserve_amount.trim() : defaults.bnb_reserve_amount,
    dust_convert_enabled: liveOnly ? model.dust_convert_enabled : defaults.dust_convert_enabled,
    dust_convert_interval_days: liveOnly ? model.dust_convert_interval_days : defaults.dust_convert_interval_days,
    stablecoin_margin: liveOnly ? recordOf(model.stablecoin_margin) : undefined,
    margin_coins: recordOf(model.margin_coins),
    arbitrage_base_assets: base.length > 0 ? base : undefined,
    margin_ratio_threshold: margin ? orUndefined(model.margin_ratio_threshold) : undefined,
    unified_mmr_threshold: margin ? orUndefined(model.unified_mmr_threshold) : undefined,
    margin_reduce_ratio: margin ? orUndefined(model.margin_reduce_ratio) : undefined,
    margin_monitor_disabled: margin ? model.margin_monitor_disabled : false
  };
}
