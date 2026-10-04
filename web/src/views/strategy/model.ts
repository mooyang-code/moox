import type { InstrumentTarget, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";

export type TargetState = "inactive" | "unknown" | "empty" | "expired" | "zero" | "valid";

export function deriveTargetState(
  instance: Pick<StrategyInstance, "enabled" | "session_id">,
  snapshot: StrategyTargetSnapshot | null,
  nowMs = Date.now()
): TargetState {
  if (!instance.enabled) return "inactive";
  if (!instance.session_id) return "unknown";
  if (!snapshot) return "unknown";
  if (!snapshot.session_id && !snapshot.bar_end_time && !snapshot.valid_until && snapshot.targets.length === 0) return "empty";
  if (snapshot.session_id !== instance.session_id) return "unknown";
  const barEnd = Date.parse(snapshot.bar_end_time);
  const validUntil = Date.parse(snapshot.valid_until);
  if (!Number.isFinite(barEnd) || !Number.isFinite(validUntil)) return "unknown";
  if (validUntil <= nowMs) return "expired";
  return snapshot.targets.length === 0 ? "zero" : "valid";
}

export function targetWeightPercent(target: InstrumentTarget): string {
  const value = Number(target.target_weight);
  if (!Number.isFinite(value)) return "未知";
  return `${(value * 100).toFixed(2)}%`;
}

export function formatStrategyTime(value?: string): string {
  if (!value) return "-";
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? new Date(timestamp).toLocaleString() : "时间未知";
}

export function parseBindings(raw: string): Record<string, unknown> {
  try {
    const value = JSON.parse(raw || "{}");
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  } catch {
    return {};
  }
}
