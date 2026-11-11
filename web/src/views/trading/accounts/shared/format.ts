/** 数量：最多 8 位小数并去掉末尾的零。 */
export function fmtQty(value: number | string | undefined | null): string {
  if (value === undefined || value === null || value === "") return "—";
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  if (n === 0) return "0";
  return String(Number(n.toFixed(8)));
}

/**
 * 表格里的数量：只留够看的位数，不让一长串小数把列撑成两行。
 * 一千以上留 2 位小数，一以上留 4 位，小于一留 4 位有效数字；末尾的零去掉。精确值在下单框和订单详情里看。
 */
export function fmtQtyShort(value: number | string | undefined | null): string {
  if (value === undefined || value === null || value === "") return "—";
  const raw = Number(value);
  if (!Number.isFinite(raw)) return "—";
  // 先按 8 位小数取整：浮点相加减留下的残差（如 5.5e-17）算 0
  const n = Number(raw.toFixed(8));
  if (n === 0) return "0";
  const size = Math.abs(n);
  if (size >= 1000) return n.toLocaleString("en-US", { maximumFractionDigits: 2 });
  if (size >= 1) return String(Number(n.toFixed(4)));
  return n.toLocaleString("en-US", { maximumSignificantDigits: 4, useGrouping: false });
}

/** 价格：按大小留位数，一以上留 4 位小数，小于一留 5 位有效数字。 */
export function fmtPrice(value: number | string | undefined | null): string {
  if (value === undefined || value === null || value === "") return "—";
  const n = Number(value);
  if (!Number.isFinite(n) || n === 0) return "—";
  if (Math.abs(n) >= 1) return n.toLocaleString("en-US", { maximumFractionDigits: 4 });
  return String(Number(n.toPrecision(5)));
}

/** 金额：千分位，默认 2 位小数。 */
export function fmtMoney(value: number | string | undefined | null, digits = 2): string {
  if (value === undefined || value === null || value === "") return "—";
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  return n.toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

export function fmtSigned(value: number | string | undefined | null, digits = 2): string {
  const n = Number(value);
  if (value === undefined || value === null || value === "" || !Number.isFinite(n)) return "—";
  return `${n > 0 ? "+" : ""}${fmtMoney(n, digits)}`;
}

export function fmtPercent(ratio: number | undefined | null, digits = 2): string {
  if (ratio === undefined || ratio === null || !Number.isFinite(ratio)) return "—";
  return `${ratio > 0 ? "+" : ""}${(ratio * 100).toFixed(digits)}%`;
}

/** 盈亏的样式类：盈为 tx-pos（绿），亏为 tx-neg（红），其余为空。 */
export function pnlClass(value: number | string | undefined | null): "tx-pos" | "tx-neg" | "" {
  const n = Number(value);
  if (!Number.isFinite(n) || n === 0) return "";
  return n > 0 ? "tx-pos" : "tx-neg";
}
