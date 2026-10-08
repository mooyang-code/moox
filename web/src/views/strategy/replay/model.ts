import type { ReplayBar } from "@/api/strategy-types";

export interface ReplayMetrics {
  bars: number;
  ok_bars: number;
  skipped_bars: number;
  skip_reasons: Record<string, number>;
  initial_equity: number;
  final_equity: number;
  total_return: number;
  /** 区间不足一周或结果溢出时后端省略年化，这里为 null。 */
  annualized_return: number | null;
  max_drawdown: number;
  average_turnover: number;
  average_holdings: number;
  total_fee: number;
  unfilled_bars: number;
  liquidations: number;
  factors: Record<string, string>;
  limitations: string[];
}

/** 解析回放指标；任务未完成或指标缺失时返回 null。 */
export function parseMetrics(raw?: string): ReplayMetrics | null {
  if (!raw) return null;
  try {
    const value = JSON.parse(raw);
    if (!value || typeof value !== "object" || !Number(value.bars)) return null;
    return {
      bars: Number(value.bars ?? 0),
      ok_bars: Number(value.ok_bars ?? 0),
      skipped_bars: Number(value.skipped_bars ?? 0),
      skip_reasons: value.skip_reasons ?? {},
      initial_equity: Number(value.initial_equity ?? 1),
      final_equity: Number(value.final_equity ?? 1),
      total_return: Number(value.total_return ?? 0),
      annualized_return:
        value.annualized_return === undefined || value.annualized_return === null ? null : Number(value.annualized_return),
      max_drawdown: Number(value.max_drawdown ?? 0),
      average_turnover: Number(value.average_turnover ?? 0),
      average_holdings: Number(value.average_holdings ?? 0),
      total_fee: Number(value.total_fee ?? 0),
      unfilled_bars: Number(value.unfilled_bars ?? 0),
      liquidations: Number(value.liquidations ?? 0),
      factors: value.factors ?? {},
      limitations: Array.isArray(value.limitations) ? value.limitations : []
    };
  } catch {
    return null;
  }
}

export function equitySeries(bars: ReplayBar[]): { time: number; value: number }[] {
  return bars
    .map(bar => ({ time: Date.parse(bar.bar_end_time), value: bar.equity }))
    .filter(point => Number.isFinite(point.time) && Number.isFinite(point.value));
}

export function replayStatusLabel(status: string): string {
  return (
    ({ pending: "排队中", running: "运行中", done: "已完成", failed: "失败", cancelled: "已取消" } as Record<string, string>)[
      status
    ] ?? status
  );
}

export function replayStatusColor(status: string): string {
  return (
    ({ pending: "gray", running: "arcoblue", done: "green", failed: "red", cancelled: "orange" } as Record<string, string>)[
      status
    ] ?? "gray"
  );
}

/** 每期持仓账本中的现金与持仓数。 */
export function positionsSummary(raw: string): { cash: number; holdings: number; frozen: number } {
  try {
    const value = JSON.parse(raw || "{}");
    const positions = Object.values((value.positions ?? {}) as Record<string, { frozen?: boolean }>);
    return {
      cash: Number(value.cash ?? 0),
      holdings: positions.length,
      frozen: positions.filter(position => position.frozen).length
    };
  } catch {
    return { cash: 0, holdings: 0, frozen: 0 };
  }
}

/** 按 UTC 显示时间（回放区间以 UTC 输入，列表与周期表保持一致）。 */
export function formatUtcTime(value?: string): string {
  if (!value) return "-";
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return "时间未知";
  return `${new Date(timestamp).toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** 把新读到的周期记录合并到已加载的列表：按 bar_end_time 去重并保持升序。 */
export function mergeBars(known: ReplayBar[], incoming: ReplayBar[]): ReplayBar[] {
  if (!incoming.length) return known;
  const seen = new Set(known.map(bar => bar.bar_end_time));
  const merged = [...known];
  for (const bar of incoming) {
    if (seen.has(bar.bar_end_time)) continue;
    seen.add(bar.bar_end_time);
    merged.push(bar);
  }
  return merged.sort((a, b) => Date.parse(a.bar_end_time) - Date.parse(b.bar_end_time));
}

/** 跳过周期的原因（summary_json.skip_reason）；无法解析时返回空串。 */
export function barSkipReason(summaryJSON: string): string {
  try {
    const value = JSON.parse(summaryJSON || "{}");
    return typeof value.skip_reason === "string" ? value.skip_reason : "";
  } catch {
    return "";
  }
}
