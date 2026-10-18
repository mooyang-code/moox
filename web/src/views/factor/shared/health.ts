import type { FactorSet, SetRunSummary } from "@/api/factor/types";
import { setStatusTag } from "./status";

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
/** 收盘后给计算留的宽限（秒）：不超过半个周期，避免刚收盘的几秒被误判为落后。 */
const CLOSE_GRACE_SECONDS = 30;

export interface BarLag {
  /** 落后的已收盘 K 线根数；0 表示已追平。 */
  bars: number;
  /** 落后的秒数（根数 × 周期）。 */
  seconds: number;
}

/**
 * 以“最近一根已收盘的 K 线”为基准计算落后量，而不是用当前时间减去上一期的开盘时间。
 * 开盘于 T 的 K 线在 T+周期 才收盘，所以用“现在 − 上一期开盘时间”衡量时，一个完全追平的任务也永远显示 1～2 个周期的“滞后”。
 * 这里先算出此刻应当已经产出的最新一期（扣除计算宽限），再看实际产出的最新一期落后了几根。
 */
export function closedBarLag(freq: string, lastPeriodSec: number, nowSec: number): BarLag {
  const period = freqSeconds(freq);
  if (period <= 0 || !Number.isFinite(lastPeriodSec) || !Number.isFinite(nowSec)) return { bars: 0, seconds: 0 };
  const grace = Math.min(CLOSE_GRACE_SECONDS, period / 2);
  const expectedOpen = Math.floor((nowSec - grace) / period) * period - period;
  const behind = Math.max(0, expectedOpen - lastPeriodSec);
  const bars = Math.round(behind / period);
  return { bars, seconds: bars * period };
}

/** 引擎汇总里的落后量：汇总的“现在”是 上一期时间 + lag_seconds（服务端计算它时的时钟），不依赖浏览器时钟。 */
export function runBarLag(freq: string, lastRun?: Pick<SetRunSummary, "last_period_time" | "lag_seconds">): BarLag {
  const last = Number(lastRun?.last_period_time);
  const lag = Number(lastRun?.lag_seconds);
  if (!last || !Number.isFinite(lag)) return { bars: 0, seconds: 0 };
  return closedBarLag(freq, last, last + Math.max(0, lag));
}

/** 展示用文案：已追平，或“落后 N 根（X 分钟）”；还没有周期时为空。 */
export function barLagText(freq: string, lastRun?: Pick<SetRunSummary, "last_period_time" | "lag_seconds">): string {
  if (!lastRun?.last_period_time) return "";
  const { bars, seconds } = runBarLag(freq, lastRun);
  return bars === 0 ? "已追平" : `落后 ${bars} 根（${formatLag(seconds)}）`;
}

export function setHealth(set: Pick<FactorSet, "status" | "freq">, lastRun?: SetRunSummary): SetHealth {
  if (set.status === "deleting" || set.status === "pending" || set.status === "disabled") {
    const tag = setStatusTag(set.status);
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

export function formatPeriod(raw?: number | string) {
  const unixSeconds = Number(raw);
  return unixSeconds ? new Date(unixSeconds * 1000).toLocaleString() : "-";
}
