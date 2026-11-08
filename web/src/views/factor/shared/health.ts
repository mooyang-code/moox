import { formatInstant } from "@/utils/time-zone";
import type { FactorTask, TaskRunSummary } from "@/api/factor/types";
import { taskStatusTag } from "./status";

export type SetHealthKey = "pending" | "deleting" | "disabled" | "idle" | "failed" | "degraded" | "lagging" | "ok";

export interface SetHealth {
  key: SetHealthKey;
  label: string;
  color: string;
}

const FREQ_UNIT_SECONDS: Record<string, number> = { m: 60, h: 3600, d: 86400 };

/** 频率字符串（如 1m、4h、1d）对应的秒数；无法解析返回 0。 */
export function freqSeconds(freq: string) {
  const match = freq.trim().match(/^(\d+)([mhd])$/i);
  if (!match) return 0;
  return Number(match[1]) * FREQ_UNIT_SECONDS[match[2].toLowerCase()];
}

/** 落后几根已收盘 K 线及时的结果才判定为滞后：落后 1 根属于“刚收盘、正在计算”，不算问题。 */
const LAG_TOLERANCE_BARS = 2;

export interface BarLag {
  /** 落后的已收盘 K 线根数；0 表示已追平。 */
  bars: number;
  /** 落后的秒数（根数 × 周期）。 */
  seconds: number;
}

/**
 * 引擎汇总里的落后量。lag_seconds 由引擎按市场时钟算好：落后的已收盘 K 线根数 × 周期（收盘后的计算宽限、
 * A 股的午休、隔夜与周末都已扣除），这里只换算成根数，不依赖浏览器时钟，也不按 7×24 小时推算。
 */
export function runBarLag(freq: string, lastRun?: Pick<TaskRunSummary, "last_period_time" | "lag_seconds">): BarLag {
  const period = freqSeconds(freq);
  const lag = Number(lastRun?.lag_seconds);
  if (period <= 0 || !lastRun?.last_period_time || !Number.isFinite(lag)) return { bars: 0, seconds: 0 };
  const bars = Math.max(0, Math.round(lag / period));
  return { bars, seconds: bars * period };
}

/** 展示用文案：已追平，或“落后 N 根（X 分钟）”；还没有周期时为空。 */
export function barLagText(freq: string, lastRun?: Pick<TaskRunSummary, "last_period_time" | "lag_seconds">): string {
  if (!lastRun?.last_period_time) return "";
  const { bars, seconds } = runBarLag(freq, lastRun);
  return bars === 0 ? "已追平" : `落后 ${bars} 根（${formatLag(seconds)}）`;
}

export function setHealth(set: Pick<FactorTask, "status" | "freq">, lastRun?: TaskRunSummary): SetHealth {
  if (set.status === "pending" || set.status === "disabled") {
    const tag = taskStatusTag(set.status);
    return { key: set.status, ...tag };
  }
  if (!lastRun || !lastRun.last_period_time) return { key: "idle", label: "暂无周期", color: "gray" };
  if (lastRun.last_status === "failed") return { key: "failed", label: "失败", color: "red" };
  if (lastRun.last_status === "degraded") return { key: "degraded", label: "降级", color: "orange" };
  if (runBarLag(set.freq, lastRun).bars >= LAG_TOLERANCE_BARS) return { key: "lagging", label: "滞后", color: "red" };
  return { key: "ok", label: "正常", color: "green" };
}

// int64 字段在 ProtoJSON 里是字符串，这里统一转成数字再格式化。
export function formatLag(raw?: number | string) {
  const seconds = typeof raw === "string" && raw.trim() !== "" ? Number(raw) : raw;
  if (typeof seconds !== "number" || !Number.isFinite(seconds)) return "-";
  const value = Math.max(0, Math.floor(seconds));
  if (value < 60) return `${value} 秒`;
  if (value < 3600) return `${Math.floor(value / 60)} 分钟`;
  if (value < 86400) return `${(value / 3600).toFixed(1)} 小时`;
  return `${(value / 86400).toFixed(1)} 天`;
}

/** 因子周期（Unix 秒）按空间时区带后缀显示。 */
export function formatPeriod(raw: number | string | undefined, zone: string) {
  const unixSeconds = Number(raw);
  return unixSeconds ? formatInstant(unixSeconds * 1000, zone) : "-";
}
