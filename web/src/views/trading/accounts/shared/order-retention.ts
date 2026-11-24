import type { TradingAccount } from "@/api/trade";

/** 模拟账户订单保留天数的默认值与上限，与交易模块一致；0 表示不清理。 */
export const DEFAULT_ORDER_RETENTION_DAYS = 5;
export const MAX_ORDER_RETENTION_DAYS = 365;

/** 表单下面的说明：哪些账户会被清理、清理后什么还在。 */
export const orderRetentionHint =
  "结束超过这么多天的订单和成交明细会被删掉，0 表示不清理。所有模拟账户都按它清理（实盘不清）；收益拆解的手续费、已实现盈亏和成交笔数按小时汇总保留，收益曲线不受影响。";

/** 校验输入的保留天数，返回错误文字；合法时返回空串。 */
export function orderRetentionError(value: string): string {
  const text = value.trim();
  if (!/^\d+$/.test(text) || Number(text) > MAX_ORDER_RETENTION_DAYS) {
    return `订单保留天数要是 0～${MAX_ORDER_RETENTION_DAYS} 的整数（0 表示不清理）`;
  }
  return "";
}

/** 账户当前的保留天数；后端没给时按默认值。 */
export function orderRetentionDaysOf(account: Pick<TradingAccount, "paper">): number {
  return account.paper?.order_retention_days ?? DEFAULT_ORDER_RETENTION_DAYS;
}

/** 展示用文字：0 显示“不清理”。 */
export function fmtOrderRetention(days: number): string {
  return days === 0 ? "不清理" : `${days} 天`;
}
