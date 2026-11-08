/** 数量：最多 8 位小数并去掉末尾的零。 */
export function fmtQty(value: number | string | undefined | null): string {
  if (value === undefined || value === null || value === "") return "—";
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  if (n === 0) return "0";
  return String(Number(n.toFixed(8)));
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

const pad = (n: number) => String(n).padStart(2, "0");

/** 毫秒时间戳转成 月-日 时:分(:秒)；缺失或为 0 时返回 —。 */
export function fmtClock(value: number | string | undefined | null, withSeconds = false): string {
  const ms = Number(value);
  if (!Number.isFinite(ms) || ms <= 0) return "—";
  const d = new Date(ms);
  const base = `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return withSeconds ? `${base}:${pad(d.getSeconds())}` : base;
}
