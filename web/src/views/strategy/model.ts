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
  if (!snapshot.bar_end_time && !snapshot.valid_until && snapshot.targets.length === 0) return "empty";
  if (snapshot.session_id !== instance.session_id) return "unknown";
  const barEnd = Date.parse(snapshot.bar_end_time);
  const validUntil = Date.parse(snapshot.valid_until);
  if (!Number.isFinite(barEnd) || !Number.isFinite(validUntil)) return "unknown";
  if (validUntil <= nowMs) return "expired";
  return snapshot.targets.length === 0 ? "zero" : "valid";
}

export function targetWeightPercent(target: InstrumentTarget): string {
  return percent(target.target_weight);
}

/** 把小数文本格式化为百分比；无法解析时返回 "-"。 */
export function percent(value: string | number | undefined, digits = 2): string {
  if (value === undefined || value === "") return "-";
  const number = Number(value);
  if (!Number.isFinite(number)) return "-";
  return `${(number * 100).toFixed(digits)}%`;
}

export function formatStrategyTime(value?: string): string {
  if (!value) return "-";
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? new Date(timestamp).toLocaleString() : "时间未知";
}

export function shortHash(value?: string): string {
  if (!value) return "-";
  const hex = value.replace(/^sha256:/, "");
  return hex.length > 12 ? `${hex.slice(0, 12)}…` : hex;
}

const skipReasons: Record<string, string> = {
  factor_missing: "因子状态缺失",
  factor_changed: "因子定义已变化",
  previous_version_unknown: "上一根版本未知",
  factor_skipped: "因子本期跳过",
  universe_too_small: "可用标的太少",
  too_many_missing: "缺数比例过高",
  config_error: "配置错误",
  ambiguous_series: "同一标的多个序列",
  out_of_order: "乱序周期",
  expired: "周期已过期",
  infra_retry_exhausted: "读取重试耗尽"
};

export function skipReasonLabel(reason?: string): string {
  if (!reason) return "-";
  return skipReasons[reason] ? `${skipReasons[reason]}（${reason}）` : reason;
}

const stages: Record<string, { label: string; color: string }> = {
  aged_out: { label: "年龄不足", color: "gray" },
  missing: { label: "缺数", color: "red" },
  filtered: { label: "被过滤", color: "gray" },
  scored: { label: "未入选", color: "blue" },
  idle: { label: "未触发", color: "gray" },
  weighted: { label: "入选", color: "green" },
  dropped: { label: "被剔除", color: "orange" }
};

export function stageLabel(stage: string): string {
  return stages[stage]?.label ?? stage;
}

export function stageColor(stage: string): string {
  return stages[stage]?.color ?? "blue";
}

export interface RuleSummary {
  expected: number;
  aged_out: number;
  available: number;
  missing: number;
  filtered: number;
  scored: number;
  selected: number;
  weighted: number;
  budget: string;
  allocated: string;
}

export interface ResultSummary {
  universe: number;
  rules: Record<string, RuleSummary>;
  gross: string;
  net: string;
  cash: string;
  turnover: string;
  notes: string[];
}

function parseObject(raw?: string): Record<string, any> {
  if (!raw) return {};
  try {
    const value = JSON.parse(raw);
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  } catch {
    return {};
  }
}

export function parseSummary(raw?: string): ResultSummary {
  const value = parseObject(raw);
  return {
    universe: Number(value.universe ?? 0),
    rules: value.rules && typeof value.rules === "object" ? value.rules : {},
    gross: value.gross ?? "",
    net: value.net ?? "",
    cash: value.cash ?? "",
    turnover: value.turnover ?? "",
    notes: Array.isArray(value.notes) ? value.notes : []
  };
}

export interface ResolvedColumn {
  name: string;
  source: string;
  factor_id: string;
  factor_output: string;
  definition_hash: string;
}

export interface ResolvedBinding {
  view_id: string;
  dataset_id: string;
  source_dataset_id: string;
  bar: string;
  calendar: string;
  market_type: string;
  spot: boolean;
  uses_previous_bar: boolean;
  min_age_bars: number;
  retention_bars: number;
  columns: ResolvedColumn[];
}

/** 解析启用时固化的绑定（resolved_json）；未启用过的实例返回 null。 */
export function parseResolved(raw?: string): ResolvedBinding | null {
  const value = parseObject(raw);
  if (!value.view_id) return null;
  const columns = Object.entries((value.columns ?? {}) as Record<string, any>)
    .map(([name, binding]) => ({
      name,
      source: binding?.source ?? "",
      factor_id: binding?.factor_id ?? "",
      factor_output: binding?.factor_output ?? "",
      definition_hash: binding?.definition_hash ?? ""
    }))
    .sort((a, b) => a.name.localeCompare(b.name));
  return {
    view_id: value.view_id,
    dataset_id: value.dataset_id ?? "",
    source_dataset_id: value.source_dataset_id ?? "",
    bar: value.bar ?? "",
    calendar: value.calendar ?? "",
    market_type: value.market_type ?? "",
    spot: Boolean(value.spot),
    uses_previous_bar: Boolean(value.uses_previous_bar),
    min_age_bars: Number(value.min_age_bars ?? 0),
    retention_bars: Number(value.retention_bars ?? 0),
    columns
  };
}
