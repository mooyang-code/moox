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

const LAG_TOLERANCE_PERIODS = 2;

export function setHealth(set: Pick<FactorSet, "status" | "freq">, lastRun?: SetRunSummary): SetHealth {
  if (set.status === "deleting" || set.status === "pending" || set.status === "disabled") {
    const tag = setStatusTag(set.status);
    return { key: set.status, ...tag };
  }
  if (!lastRun || !lastRun.last_period_time) return { key: "idle", label: "暂无周期", color: "gray" };
  if (lastRun.last_status === "failed") return { key: "failed", label: "失败", color: "red" };
  if (lastRun.last_status === "degraded") return { key: "degraded", label: "降级", color: "orange" };
  const period = freqSeconds(set.freq);
  if (period > 0 && lastRun.lag_seconds > period * LAG_TOLERANCE_PERIODS) return { key: "lagging", label: "滞后", color: "red" };
  return { key: "ok", label: "正常", color: "green" };
}

export function formatLag(seconds?: number) {
  if (typeof seconds !== "number" || !Number.isFinite(seconds)) return "-";
  const value = Math.max(0, Math.floor(seconds));
  if (value < 60) return `${value} 秒`;
  if (value < 3600) return `${Math.floor(value / 60)} 分钟`;
  if (value < 86400) return `${(value / 3600).toFixed(1)} 小时`;
  return `${(value / 86400).toFixed(1)} 天`;
}

export function formatPeriod(unixSeconds?: number) {
  return unixSeconds ? new Date(unixSeconds * 1000).toLocaleString() : "-";
}
