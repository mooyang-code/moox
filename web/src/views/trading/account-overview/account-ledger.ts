import type { LedgerType } from "@/api/trade";

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

/** 默认查近 7 天：[开始, 结束] 毫秒时间戳 */
export function defaultLedgerRange(now = Date.now()): number[] {
  return [now - 7 * DAY_MS, now];
}

/** 交易所只保留约 3 个月的账单，后端也限制单次最长 90 天 */
export const MAX_LEDGER_DAYS = 90;

export function ledgerRangeError(range: number[]): string {
  if (range.length !== 2 || !(range[0] < range[1])) return "请选择有效的时间范围";
  if (range[1] - range[0] > MAX_LEDGER_DAYS * DAY_MS) return `时间范围不能超过 ${MAX_LEDGER_DAYS} 天`;
  return "";
}

export function amountClass(amount: string): string {
  const value = Number(amount);
  if (!Number.isFinite(value) || value === 0) return "";
  return value > 0 ? "amount-positive" : "amount-negative";
}
