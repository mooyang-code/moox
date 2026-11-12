import type { LogBucket, LogRecord } from "@/api/monitor";

/** 级别的中文：关键事件按 info 级写出，单独叫“事件”。 */
export function levelLabel(record: LogRecord): string {
  if (record.event && levelTone(record) !== "error") return "事件";
  const names: Record<string, string> = {
    error: "错误",
    fatal: "致命",
    panic: "致命",
    dpanic: "致命",
    warn: "警告",
    info: "信息",
    debug: "调试"
  };
  return names[record.level ?? ""] ?? record.level ?? "--";
}

/** 级别的颜色：错误和致命标红，其余不强调。 */
export function levelTone(record: LogRecord): "error" | "event" {
  return ["error", "fatal", "panic", "dpanic"].includes(record.level ?? "") ? "error" : "event";
}

/** 正文的第一行（列表里只显示一行，完整内容在详情里）。 */
export function firstLine(message?: string): string {
  return (message ?? "").split("\n", 1)[0];
}

/** 被合并掉的条数；接口里的 int32 可能是数字或字符串。 */
export function mergedCount(record: LogRecord): number {
  const value = Number(record.merged_count ?? 0);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

/** 字段按键名排序，便于对比两条日志。 */
export function fieldEntries(record: LogRecord): Array<[string, string]> {
  return Object.entries(record.fields ?? {}).sort(([a], [b]) => a.localeCompare(b));
}

// ---------- 时间范围与分布 ----------

export type LogRange = "24h" | "7d" | "30d";

const HOUR_MS = 3_600_000;
const RANGE_HOURS: Record<LogRange, number> = { "24h": 24, "7d": 7 * 24, "30d": 30 * 24 };

/** 一段时间窗口（毫秒，含起点、不含终点）。 */
export interface LogWindow {
  startMs: number;
  endMs: number;
}

/** 预设范围对应的窗口：终点取下一个整点，起点往前推这么多小时，柱子正好落在整点上。 */
export function rangeWindow(range: LogRange, nowMs: number): LogWindow {
  const endMs = Math.floor(nowMs / HOUR_MS) * HOUR_MS + HOUR_MS;
  return { startMs: endMs - RANGE_HOURS[range] * HOUR_MS, endMs };
}

/** 某条日志前后各 minutes 分钟的窗口；时间无效时为 null。 */
export function aroundWindow(loggedAt: string | undefined, minutes = 5): LogWindow | null {
  const at = Date.parse(loggedAt ?? "");
  if (!Number.isFinite(at)) return null;
  return { startMs: at - minutes * 60_000, endMs: at + minutes * 60_000 + 1 };
}

export interface LogBar extends LogWindow {
  errors: number;
  events: number;
}

/**
 * 把按小时的条数归并成柱子：窗口不超过 2 天时每小时一根，不超过 7 天时每 6 小时一根，更长时每天一根（按 UTC 对齐）。
 * 没有记录的时段也占一根（高度为 0），时间轴才连续。窗口不足一小时（看某条日志的前后）时不画。
 */
export function logBars(buckets: LogBucket[] | undefined, window: LogWindow): LogBar[] {
  const span = window.endMs - window.startMs;
  if (span < HOUR_MS) return [];
  const stepMs = (span <= 48 * HOUR_MS ? 1 : span <= 7 * 24 * HOUR_MS ? 6 : 24) * HOUR_MS;
  const first = Math.floor(window.startMs / stepMs) * stepMs;
  const bars: LogBar[] = [];
  // 首尾两根按窗口裁掉多出来的部分：点它们时查的就是柱子统计的那一段，时间轴两端也正好是窗口的起止。
  for (let startMs = first; startMs < window.endMs; startMs += stepMs)
    bars.push({
      startMs: Math.max(startMs, window.startMs),
      endMs: Math.min(startMs + stepMs, window.endMs),
      errors: 0,
      events: 0
    });
  for (const bucket of buckets ?? []) {
    const at = Date.parse(bucket.start_at ?? "");
    const bar = Number.isFinite(at) ? bars[Math.floor((at - first) / stepMs)] : undefined;
    if (!bar) continue;
    bar.errors += Number(bucket.errors ?? 0) || 0;
    bar.events += Number(bucket.events ?? 0) || 0;
  }
  return bars;
}

/** 柱子里最大的条数（至少为 1，免得除以 0）。 */
export function barsPeak(bars: LogBar[]): number {
  return Math.max(1, ...bars.map(bar => bar.errors + bar.events));
}
