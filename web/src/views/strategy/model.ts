import type { InstrumentTarget, StrategyInstance, StrategyResult, StrategyTargetSnapshot } from "@/api/strategy-types";

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

/** 目标状态的标签与颜色：有效为绿色，过期与无法确认为橙色，其余为灰色或蓝色。 */
export function targetStateTag(state: TargetState): { label: string; color: string } {
  return (
    {
      valid: { label: "目标有效", color: "green" },
      zero: { label: "零仓位目标", color: "blue" },
      expired: { label: "目标已过期", color: "orange" },
      empty: { label: "尚无结果", color: "gray" },
      inactive: { label: "实例已停用", color: "gray" },
      unknown: { label: "无法确认", color: "orange" }
    } as const
  )[state];
}

/** 目标按权重绝对值从大到小排列，并给出相对最大权重的比例（0~1），用于权重条。 */
export function rankTargets(targets: InstrumentTarget[]): { target: InstrumentTarget; weight: number; ratio: number }[] {
  const rows = targets.map(target => ({ target, weight: Number(target.target_weight) }));
  const valid = rows.filter(row => Number.isFinite(row.weight));
  const max = Math.max(0, ...valid.map(row => Math.abs(row.weight)));
  return rows
    .map(row => ({ ...row, weight: Number.isFinite(row.weight) ? row.weight : 0 }))
    .sort((a, b) => Math.abs(b.weight) - Math.abs(a.weight) || a.target.instrument_id.localeCompare(b.target.instrument_id))
    .map(row => ({ ...row, ratio: max > 0 ? Math.abs(row.weight) / max : 0 }));
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
  no_data: "本期无数据",
  history_insufficient: "历史覆盖不足",
  out_of_order: "乱序周期",
  expired: "周期已过期",
  infra_retry_exhausted: "读取重试耗尽"
};

export function skipReasonLabel(reason?: string): string {
  if (!reason) return "-";
  return skipReasons[reason] ? `${skipReasons[reason]}（${reason}）` : reason;
}

const itemReasons: Record<string, string> = {
  filter: "filter 未通过",
  "select.where": "select.where 未通过",
  not_selected: "名次未入选",
  not_rebalanced: "非建仓 bar",
  filter_after: "filter_after 剔除",
  exit: "触发 exit",
  no_entry: "未触发 entry",
  entry_and_exit: "entry 与 exit 同时成立",
  min_age_bars: "上市时间不足",
  no_row: "本期无行",
  score_invalid: "分数无效",
  not_expected: "已不在预期集合",
  holding: "延续批次持有"
};

function reasonText(reason: string): string {
  if (reason.startsWith("holding:")) return `延续批次持有，本期${reasonText(reason.slice("holding:".length))}`;
  if (reason.startsWith("missing:")) return `缺少 ${reason.slice("missing:".length)}`;
  if (reason.startsWith("factor_failed:")) return `上游因子失败（${reason.slice("factor_failed:".length)}）`;
  if (reason.startsWith("source_failed:")) return `上游数据失败（${reason.slice("source_failed:".length)}）`;
  if (reason.startsWith("score_error:")) return `分数计算出错：${reason.slice("score_error:".length)}`;
  return itemReasons[reason] ?? reason;
}

/** 解释明细的原因：已知代码给出中文说明并附原文，带前缀的原因（holding:、missing:、factor_failed:、source_failed:、score_error:）逐段翻译。 */
export function itemReasonLabel(reason?: string): string {
  if (!reason) return "-";
  const text = reasonText(reason);
  return text === reason ? reason : `${text}（${reason}）`;
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

/** 解释明细的阶段顺序：入选的放最前面，其次是被剔除、打分未入选，最后是缺数、未触发、被过滤和年龄不足。 */
const stageOrder: Record<string, number> = { weighted: 0, dropped: 1, scored: 2, missing: 3, idle: 4, filtered: 5, aged_out: 6 };

/** 解释明细排序：按阶段，再按名次（无名次的在后），最后按标的；入选的几个标的不会淹没在几百个被过滤的标的里。 */
export function sortResultItems<T extends { stage: string; rank?: number; instrument_id: string; rule_id: string }>(
  items: T[]
): T[] {
  const rankOf = (item: T) => (item.rank && item.rank > 0 ? item.rank : Number.MAX_SAFE_INTEGER);
  return [...items].sort(
    (a, b) =>
      (stageOrder[a.stage] ?? 9) - (stageOrder[b.stage] ?? 9) ||
      rankOf(a) - rankOf(b) ||
      a.rule_id.localeCompare(b.rule_id) ||
      a.instrument_id.localeCompare(b.instrument_id)
  );
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
  /** 因子列所属的成员键。 */
  member: string;
  /** 成员类型：fixed 或 var。 */
  member_kind: string;
  factor_id: string;
  output_name: string;
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
  min_age_bars: number;
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
      member: binding?.member ?? "",
      member_kind: binding?.member_kind ?? "",
      factor_id: binding?.factor_id ?? "",
      output_name: binding?.output_name ?? "",
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
    min_age_bars: Number(value.min_age_bars ?? 0),
    columns
  };
}

export interface ResultInsight {
  /** 相对同一会话内上一期（时间更早的一期 ok 结果）新进的目标标的。 */
  added: string[];
  /** 相对上一期移出的标的。 */
  removed: string[];
  /** 同一会话内是否有上一期可比较；没有时 added/removed 不代表真实变化。 */
  comparable: boolean;
  /** 结果落库时间减 K 线结束时间，毫秒；任一时间无效时为 null。 */
  delayMs: number | null;
}

type ResultLike = Pick<
  StrategyResult,
  "result_id" | "session_id" | "status" | "bar_end_time" | "created_at" | "targets" | "skip_reason"
>;

/**
 * 按 K 线结束时间从旧到新比较同一会话内相邻的 ok 结果，得到每期的目标成员变化与产出延迟。
 * 比较的是目标标的集合（不含权重），也不代表账户实际持仓。
 * 不同会话的结果互不比较：每个会话的首期 comparable 为 false；分页边界上最旧的一期同理。
 * 跳过期不改变该会话的比较基线，成员变化跨过跳过期比较。
 */
export function buildResultInsights(results: ResultLike[]): Record<string, ResultInsight> {
  const ordered = [...results].sort((a, b) => Date.parse(a.bar_end_time) - Date.parse(b.bar_end_time));
  const insights: Record<string, ResultInsight> = {};
  const baselines = new Map<string, Set<string>>();
  for (const item of ordered) {
    const barEnd = Date.parse(item.bar_end_time);
    const created = Date.parse(item.created_at);
    const delayMs = Number.isFinite(barEnd) && Number.isFinite(created) ? created - barEnd : null;
    const insight: ResultInsight = { added: [], removed: [], comparable: false, delayMs };
    if (item.status === "ok") {
      const current = new Set(item.targets.map(target => target.instrument_id));
      const previous = baselines.get(item.session_id);
      if (previous) {
        insight.comparable = true;
        insight.added = [...current].filter(id => !previous.has(id)).sort();
        insight.removed = [...previous].filter(id => !current.has(id)).sort();
      }
      baselines.set(item.session_id, current);
    }
    insights[item.result_id] = insight;
  }
  return insights;
}

export interface ResultOverview {
  total: number;
  ok: number;
  skipped: number;
  /** 跳过原因计数，按次数降序。 */
  skipReasons: { reason: string; count: number }[];
  /** ok 结果的产出延迟（毫秒）：平均与最大；没有可用样本时为 null。 */
  avgDelayMs: number | null;
  maxDelayMs: number | null;
  /** 从旧到新的 ok 结果趋势点。 */
  trend: { time: string; targets: number; gross: number; turnover: number }[];
}

export function buildResultOverview(results: ResultLike[] & Pick<StrategyResult, "summary_json">[]): ResultOverview {
  const insights = buildResultInsights(results);
  const ordered = [...results].sort((a, b) => Date.parse(a.bar_end_time) - Date.parse(b.bar_end_time));
  const reasons = new Map<string, number>();
  const delays: number[] = [];
  const trend: ResultOverview["trend"] = [];
  let ok = 0;
  for (const item of ordered) {
    if (item.status === "ok") {
      ok += 1;
      const summary = parseSummary(item.summary_json);
      trend.push({
        time: item.bar_end_time,
        targets: item.targets.length,
        gross: Number(summary.gross) || 0,
        turnover: Number(summary.turnover) || 0
      });
      const delay = insights[item.result_id]?.delayMs;
      if (delay !== null && delay !== undefined && delay >= 0) delays.push(delay);
    } else {
      reasons.set(item.skip_reason || "unknown", (reasons.get(item.skip_reason || "unknown") ?? 0) + 1);
    }
  }
  return {
    total: results.length,
    ok,
    skipped: results.length - ok,
    skipReasons: [...reasons].map(([reason, count]) => ({ reason, count })).sort((a, b) => b.count - a.count),
    avgDelayMs: delays.length ? delays.reduce((sum, value) => sum + value, 0) / delays.length : null,
    maxDelayMs: delays.length ? Math.max(...delays) : null,
    trend
  };
}

/** 毫秒延迟的可读文本；null 显示 "-"。 */
export function formatDelay(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return "-";
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
}
