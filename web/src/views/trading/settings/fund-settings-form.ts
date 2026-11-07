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
}

/** 哪些项在表单里显示：BNB、小额兑换、稳定币保证金只对币安实盘组合生效，模拟盘和含非币安成员的组合不显示。 */
export interface FundFormScope {
  /** 模拟盘组合没有资金维护任务 */
  paper?: boolean;
  /** 含非币安成员：BNB 与稳定币相关项只适用于币安 */
  nonBinance?: boolean;
}

/** BNB 补买的容差：市值低于“保留金额 − 10”才补买，所以保留金额要大于 10。 */
const BNB_RESERVE_SLACK = 10;

export function showsLiveOnlyFields(scope: FundFormScope = {}): boolean {
  return !scope.paper && !scope.nonBinance;
}

export function defaultFundSettings(): FundSettings {
  return {
    reserve_ratio: "0",
    bnb_discount: false,
    bnb_reserve_amount: "0",
    dust_convert_enabled: false,
    dust_convert_interval_days: 15
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
    arbitrage_base_assets: (value.arbitrage_base_assets || []).join(", ")
  };
}

const decimalPattern = /^(?:0|[1-9]\d*)(?:\.\d+)?$/;
const assetPattern = /^[A-Z0-9]{1,20}$/;

function isDecimal(value: string): boolean {
  return decimalPattern.test(value.trim());
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
  const owners = new Map<string, string>();
  const claim = (asset: string, group: string): string => {
    if (!assetPattern.test(asset)) return `资产名 ${asset || "（空）"} 不合法（只允许字母和数字）`;
    if (asset === "USDT" && group !== "稳定币保证金") return `USDT 是结算币，不能写进${group}`;
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
    if (!stable.some(row => row.asset === "USDT")) return "稳定币保证金必须包含 USDT";
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
  return "";
}

function recordOf(list: AssetRow[]): Record<string, string> | undefined {
  if (list.length === 0) return undefined;
  return Object.fromEntries(list.map(row => [row.asset.trim().toUpperCase(), row.value.trim()]));
}

/** 不显示的项（模拟盘、含非币安成员）一律按默认值提交，不带过期的旧值。 */
export function buildFundSettingsRequest(model: FundSettingsModel, scope: FundFormScope = {}): FundSettings {
  const base = parseAssetList(model.arbitrage_base_assets);
  const liveOnly = showsLiveOnlyFields(scope);
  const defaults = defaultFundSettings();
  return {
    reserve_ratio: model.reserve_ratio.trim(),
    bnb_discount: liveOnly ? model.bnb_discount : defaults.bnb_discount,
    bnb_reserve_amount: liveOnly ? model.bnb_reserve_amount.trim() : defaults.bnb_reserve_amount,
    dust_convert_enabled: liveOnly ? model.dust_convert_enabled : defaults.dust_convert_enabled,
    dust_convert_interval_days: liveOnly ? model.dust_convert_interval_days : defaults.dust_convert_interval_days,
    stablecoin_margin: liveOnly ? recordOf(model.stablecoin_margin) : undefined,
    margin_coins: recordOf(model.margin_coins),
    arbitrage_base_assets: base.length > 0 ? base : undefined
  };
}
