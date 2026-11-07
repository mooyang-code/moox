import type { OrderSettings } from "@/api/trade/settings";

/** 每小时上限里的一行：标的 ID（或 *）和金额。 */
export interface CapRow {
  key: string;
  value: string;
}

export interface OrderSettingsModel {
  rebalance_threshold_mode: string;
  rebalance_threshold_ratio: string;
  min_notional_spot: string;
  min_notional_swap: string;
  hourly_cap_spot: CapRow[];
  hourly_cap_swap: CapRow[];
  max_child_notional: string;
  randomize_split: boolean;
  batch_interval_seconds: number;
  order_mode: string;
  slippage_step: string;
  slippage_max: string;
  max_attempts: number;
  resting_ratio: string;
  resting_ttl_seconds: number;
}

export const thresholdModeOptions = [
  { value: "TARGET_RATIO", label: "按目标数量：差额不超过目标数量 × 比例就不调" },
  { value: "POSITION_RATIO", label: "按持仓金额：调仓金额低于持仓金额 × 比例就不调" },
  { value: "EQUITY_RATIO", label: "按组合权益：调仓金额低于权益 × 比例就不调" },
  { value: "ALWAYS", label: "每期都调" }
];

export const orderModeOptions = [
  { value: "PSEUDO_MARKET", label: "伪市价单（限价 + IOC，滑点逐次放大）" },
  { value: "MARKET", label: "市价单" }
];

/** 后端的默认值，用于接口没返回设置时的表单初值。 */
export function defaultOrderSettings(): OrderSettings {
  return {
    rebalance_threshold_mode: "TARGET_RATIO",
    rebalance_threshold_ratio: "0.05",
    min_notional_spot: "10",
    min_notional_swap: "5",
    max_child_notional: "100000",
    randomize_split: false,
    batch_interval_seconds: 0,
    order_mode: "PSEUDO_MARKET",
    slippage_step: "0.015",
    slippage_max: "0.06",
    max_attempts: 4,
    resting_ratio: "0",
    resting_ttl_seconds: 0
  };
}

function capRows(caps?: Record<string, string>): CapRow[] {
  return Object.entries(caps || {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => ({ key, value }));
}

export function orderSettingsToModel(settings?: OrderSettings): OrderSettingsModel {
  const value = { ...defaultOrderSettings(), ...settings };
  return {
    rebalance_threshold_mode: value.rebalance_threshold_mode,
    rebalance_threshold_ratio: value.rebalance_threshold_ratio,
    min_notional_spot: value.min_notional_spot,
    min_notional_swap: value.min_notional_swap,
    hourly_cap_spot: capRows(value.hourly_cap_spot),
    hourly_cap_swap: capRows(value.hourly_cap_swap),
    max_child_notional: value.max_child_notional,
    randomize_split: value.randomize_split,
    batch_interval_seconds: value.batch_interval_seconds,
    order_mode: value.order_mode,
    slippage_step: value.slippage_step,
    slippage_max: value.slippage_max,
    max_attempts: value.max_attempts,
    resting_ratio: value.resting_ratio,
    resting_ttl_seconds: value.resting_ttl_seconds
  };
}

const decimalPattern = /^(?:0|[1-9]\d*)(?:\.\d+)?$/;

function isDecimal(value: string): boolean {
  return decimalPattern.test(value.trim());
}

function validateCaps(rows: CapRow[], market: "SPOT" | "SWAP", label: string): string {
  const seen = new Set<string>();
  for (const row of rows) {
    const key = row.key.trim().toUpperCase();
    if (key !== "*" && !key.endsWith(`-${market}`)) return `${label}每小时上限的标的写 * 或形如 BTC-USDT-${market}`;
    if (seen.has(key)) return `${label}每小时上限的标的 ${key} 重复`;
    seen.add(key);
    if (!isDecimal(row.value) || Number(row.value) <= 0) return `${label}每小时上限 ${key} 必须是大于 0 的数字`;
  }
  return "";
}

/** 提交前的本地校验，规则和后端 domain/ordersettings 一致；返回第一条错误，空串表示通过。 */
export function validateOrderSettingsModel(model: OrderSettingsModel): string {
  const ratioIn01 = (value: string) => isDecimal(value) && Number(value) <= 1;
  if (!ratioIn01(model.rebalance_threshold_ratio)) return "调仓阈值比例必须在 0 到 1 之间";
  if (!isDecimal(model.min_notional_spot)) return "现货最小下单额必须是不小于 0 的数字";
  if (!isDecimal(model.min_notional_swap)) return "合约最小下单额必须是不小于 0 的数字";
  const capError = validateCaps(model.hourly_cap_spot, "SPOT", "现货") || validateCaps(model.hourly_cap_swap, "SWAP", "合约");
  if (capError) return capError;
  if (
    !isDecimal(model.max_child_notional) ||
    Number(model.max_child_notional) <= 0 ||
    Number(model.max_child_notional) > 1_000_000
  ) {
    return "单笔上限必须大于 0 且不超过 1000000";
  }
  if (
    !Number.isInteger(model.batch_interval_seconds) ||
    model.batch_interval_seconds < 0 ||
    model.batch_interval_seconds > 3600
  ) {
    return "批间隔必须是 0 到 3600 秒的整数（0 表示用默认值）";
  }
  if (!isDecimal(model.slippage_step) || Number(model.slippage_step) <= 0 || Number(model.slippage_step) >= 0.5) {
    return "伪市价滑点必须大于 0 且小于 0.5";
  }
  if (
    !isDecimal(model.slippage_max) ||
    Number(model.slippage_max) < Number(model.slippage_step) ||
    Number(model.slippage_max) >= 0.5
  ) {
    return "伪市价滑点上限必须不小于滑点且小于 0.5";
  }
  if (!Number.isInteger(model.max_attempts) || model.max_attempts < 1 || model.max_attempts > 10) {
    return "伪市价最多尝试次数必须是 1 到 10 的整数";
  }
  if (!ratioIn01(model.resting_ratio)) return "挂单比例必须在 0 到 1 之间";
  if (
    !Number.isInteger(model.resting_ttl_seconds) ||
    model.resting_ttl_seconds < 0 ||
    model.resting_ttl_seconds > 7 * 24 * 3600
  ) {
    return "挂单寿命必须是 0 到 604800 秒的整数（0 表示保留到下一份目标到达）";
  }
  return "";
}

function capsRequest(rows: CapRow[]): Record<string, string> | undefined {
  if (rows.length === 0) return undefined;
  return Object.fromEntries(rows.map(row => [row.key.trim().toUpperCase(), row.value.trim()]));
}

export function buildOrderSettingsRequest(model: OrderSettingsModel): OrderSettings {
  return {
    rebalance_threshold_mode: model.rebalance_threshold_mode,
    rebalance_threshold_ratio: model.rebalance_threshold_ratio.trim(),
    min_notional_spot: model.min_notional_spot.trim(),
    min_notional_swap: model.min_notional_swap.trim(),
    hourly_cap_spot: capsRequest(model.hourly_cap_spot),
    hourly_cap_swap: capsRequest(model.hourly_cap_swap),
    max_child_notional: model.max_child_notional.trim(),
    randomize_split: model.randomize_split,
    batch_interval_seconds: model.batch_interval_seconds,
    order_mode: model.order_mode,
    slippage_step: model.slippage_step.trim(),
    slippage_max: model.slippage_max.trim(),
    max_attempts: model.max_attempts,
    resting_ratio: model.resting_ratio.trim(),
    resting_ttl_seconds: model.resting_ttl_seconds
  };
}
