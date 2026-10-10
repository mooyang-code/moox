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
  first_bar_end: string;
  last_bar_end: string;
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
      limitations: Array.isArray(value.limitations) ? value.limitations : [],
      first_bar_end: value.first_bar_end ?? "",
      last_bar_end: value.last_bar_end ?? ""
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

/** 把 "YYYY-MM-DD HH:mm"（或只有日期）的 UTC 文本转为 ISO 时间；格式或日期无效时返回 null。 */
export function parseUtcInput(text: string): string | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2}))?$/.exec(text.trim());
  if (!match) return null;
  const [, year, month, day, hour = "00", minute = "00"] = match;
  const value = new Date(Date.UTC(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute)));
  if (
    value.getUTCFullYear() !== Number(year) ||
    value.getUTCMonth() !== Number(month) - 1 ||
    value.getUTCDate() !== Number(day) ||
    value.getUTCHours() !== Number(hour) ||
    value.getUTCMinutes() !== Number(minute)
  )
    return null;
  return value.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** 把时间格式化为 UTC 的 "YYYY-MM-DD HH:mm"，用于输入框。 */
export function formatUtcInput(value: Date): string {
  return value.toISOString().slice(0, 16).replace("T", " ");
}

/** 最近 days 天的 UTC 区间（终点取当前整点）。 */
export function recentUtcRange(days: number, now = new Date()): [string, string] {
  const end = new Date(now.getTime());
  end.setUTCMinutes(0, 0, 0);
  const start = new Date(end.getTime() - days * 24 * 3600 * 1000);
  return [formatUtcInput(start), formatUtcInput(end)];
}

/** 按 UTC 显示时间（回放区间以 UTC 输入，列表与周期表保持一致）。 */
export function formatUtcTime(value?: string): string {
  if (!value) return "-";
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return "时间未知";
  return `${new Date(timestamp).toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** 时刻所在的上海日期（YYYY-MM-DD）。 */
function shanghaiDate(value: string): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return "时间未知";
  return new Date(timestamp + 8 * 3600 * 1000).toISOString().slice(0, 10);
}

/** 回放区间的写法：A 股日线按上海日期（含起始日、不含结束日），其余按 UTC 时间。 */
export function replayRangeLabel(replay: { calendar?: string; start_time: string; end_time: string }): string {
  if (replay.calendar === "cn_stock" && replay.start_time && replay.end_time) {
    return `${shanghaiDate(replay.start_time)} → ${shanghaiDate(replay.end_time)}（上海日期，不含结束日）`;
  }
  return `${formatUtcTime(replay.start_time)} → ${formatUtcTime(replay.end_time)}`;
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

/** 一期理论账本里的一个持仓。 */
export interface ReplayPosition {
  id: string;
  quantity: number;
  last_price: number;
  value: number;
  frozen: boolean;
  missing_bars: number;
}

/** 一期的持仓账本与摘要：现金占期末权益的比例、持仓、成交、未达成与清算的标的、求值说明。 */
export interface ReplayBarDetail {
  cash: number;
  positions: ReplayPosition[];
  traded: number;
  fee: number;
  unfilled: string[];
  liquidated: string[];
  /** 现金不足时买入实际执行的比例（0~1）；没有缩减时为 null。 */
  buyScale: number | null;
  notes: string[];
}

function parseObject(raw?: string): any {
  if (!raw) return {};
  try {
    const value = JSON.parse(raw);
    return value && typeof value === "object" ? value : {};
  } catch {
    return {};
  }
}

/** 解析一期的完整记录（positions_json 与 summary_json）；brief 记录或无法解析时返回空明细。 */
export function parseBarDetail(bar: ReplayBar): ReplayBarDetail {
  const positions = parseObject(bar.positions_json);
  const summary = parseObject(bar.summary_json);
  const ledger = summary.ledger && typeof summary.ledger === "object" ? summary.ledger : {};
  const items: ReplayPosition[] = Object.entries(
    positions.positions && typeof positions.positions === "object" ? positions.positions : {}
  ).map(([id, value]: [string, any]) => ({
    id,
    quantity: Number(value?.quantity ?? 0),
    last_price: Number(value?.last_price ?? 0),
    value: Number(value?.value ?? 0),
    frozen: Boolean(value?.frozen),
    missing_bars: Number(value?.missing_bars ?? 0)
  }));
  items.sort((a, b) => b.value - a.value || a.id.localeCompare(b.id));
  const equity = bar.equity > 0 ? bar.equity : 1;
  const list = (value: unknown) => (Array.isArray(value) ? value.map(String) : []);
  return {
    cash: Number(positions.cash ?? 0) / equity,
    positions: items,
    traded: Number(ledger.traded ?? 0),
    fee: Number(ledger.fee ?? 0),
    unfilled: list(ledger.unfilled),
    liquidated: list(ledger.liquidated),
    buyScale: ledger.buy_scale === undefined || ledger.buy_scale === null ? null : Number(ledger.buy_scale),
    notes: list(summary.decision?.notes)
  };
}

/** 权益曲线的文字摘要，供屏幕阅读器等无法查看图形的场景使用。 */
export function equitySummary(bars: ReplayBar[]): string {
  const points = equitySeries(bars);
  if (!points.length) return "暂无权益曲线";
  const values = points.map(point => point.value);
  const first = points[0];
  const last = points[points.length - 1];
  const label = (time: number) => formatUtcTime(new Date(time).toISOString());
  return `权益曲线：共 ${points.length} 根，${label(first.time)} 权益 ${first.value.toFixed(4)}，${label(last.time)} 权益 ${last.value.toFixed(4)}，最高 ${Math.max(...values).toFixed(4)}，最低 ${Math.min(...values).toFixed(4)}`;
}
