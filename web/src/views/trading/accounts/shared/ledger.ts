import type { LedgerType, MarketType } from "@/api/trade";
import { addDays, msToWallClock, wallClockToMs } from "@/utils/time-zone";

const DAY_MS = 24 * 60 * 60 * 1000;

export const ledgerTypeOptions: { value: LedgerType; label: string }[] = [
  { value: "TRADE", label: "交易" },
  { value: "FEE", label: "手续费" },
  { value: "REALIZED_PNL", label: "已实现盈亏" },
  { value: "FUNDING_FEE", label: "资金费" },
  { value: "TRANSFER", label: "划转" },
  { value: "LIQUIDATION", label: "强平" },
  { value: "INTEREST", label: "利息" },
  { value: "OTHER", label: "其他" }
];

export function ledgerTypeLabel(value: string): string {
  return ledgerTypeOptions.find(item => item.value === value)?.label || value || "-";
}

/**
 * 多市场账户的默认流水市场：优先永续。后端只在账户级给“能否查流水”，不说明哪个市场支持；
 * 币安现货没有统一流水接口、永续有，OKX 两个市场都有，所以默认永续最不容易落到不支持的市场。
 */
export function defaultLedgerMarket(markets: readonly number[] | undefined): MarketType {
  return (markets?.includes(2) ? 2 : (markets?.[0] ?? 1)) as MarketType;
}

/** 默认查近 7 天（含今天，按显示时区的整天）：[6 天前零点, 明天零点)，毫秒时间戳。 */
export function defaultLedgerRange(zone: string, now = Date.now()): [number, number] {
  const today = msToWallClock(now, zone, "date");
  return [wallClockToMs(addDays(today, -6), zone)!, wallClockToMs(addDays(today, 1), zone)!];
}

/** 交易所只保留约 3 个月的账单，后端也限制单次最长 90 天 */
export const MAX_LEDGER_DAYS = 90;

export function ledgerRangeError(range: [number, number] | null): string {
  if (!range || !(range[0] < range[1])) return "请选择有效的时间范围";
  if (range[1] - range[0] > MAX_LEDGER_DAYS * DAY_MS) return `时间范围不能超过 ${MAX_LEDGER_DAYS} 天`;
  return "";
}

export function amountClass(amount: string): string {
  const value = Number(amount);
  if (!Number.isFinite(value) || value === 0) return "";
  return value > 0 ? "tx-pos" : "tx-neg";
}
