import { MARKET_ORDER, marketLabel } from "@/views/strategy/market-mode";
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

function marketRank(market: string): number {
  const index = MARKET_ORDER.indexOf(market as (typeof MARKET_ORDER)[number]);
  return index < 0 ? MARKET_ORDER.length : index;
}

/** 目标按权重绝对值从大到小排列，并给出相对最大权重的比例（0~1），用于权重条。 */
export function rankTargets(targets: InstrumentTarget[]): { target: InstrumentTarget; weight: number; ratio: number }[] {
  const rows = targets.map(target => ({ target, weight: Number(target.target_weight) }));
  const valid = rows.filter(row => Number.isFinite(row.weight));
  const max = Math.max(0, ...valid.map(row => Math.abs(row.weight)));
  return rows
    .map(row => ({ ...row, weight: Number.isFinite(row.weight) ? row.weight : 0 }))
    .sort(
      (a, b) =>
        Math.abs(b.weight) - Math.abs(a.weight) ||
        a.target.instrument_id.localeCompare(b.target.instrument_id) ||
        marketRank(a.target.market) - marketRank(b.target.market)
    )
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
  infra_retry_exhausted: "读取重试耗尽",
  market_missing: "另一个市场的数据未到",
  market_failed: "市场整体失败（因子失败比例过高）",
  catchup: "恢复追赶中，不发目标（就绪时照常求值、只推进规则状态）",
  pool_warmup_failed: "策略池预热失败"
};

/** 跳过原因的下一步：已知原因给出怎么处理；会自动恢复的写明不用管。 */
const skipNextSteps: Record<string, string> = {
  factor_changed: "停用再启用实例，按新的因子定义重新固化",
  previous_version_unknown: "通常下一期自动恢复；持续出现时停用再启用实例",
  factor_missing: "等上游计算任务产出，通常自动恢复；持续出现时查看计算任务",
  factor_skipped: "上游因子本期跳过，通常自动恢复；持续出现时查看计算任务",
  market_missing: "等另一个市场的计算任务产出，通常自动恢复",
  market_failed: "上游因子失败比例过高，查看计算任务的失败原因",
  no_data: "本期没有数据，通常自动恢复；持续出现时查看采集和计算任务",
  universe_too_small: "可用标的太少：检查数据覆盖，或调整策略的标的池",
  too_many_missing: "缺数比例过高：检查采集和计算任务的覆盖",
  config_error: "检查策略定义，改好后停用再启用实例",
  ambiguous_series: "同一标的有多个序列：检查计算任务用的数据集",
  history_insufficient: "历史覆盖不足：等数据积累，或补采、补算历史",
  out_of_order: "周期乱序，通常自动恢复",
  expired: "周期已过期，通常自动恢复",
  infra_retry_exhausted: "读取重试耗尽，通常下一期自动恢复；持续出现时查看存储和网关",
  catchup: "恢复追赶中，追上后自动恢复发目标",
  pool_warmup_failed: "检查策略池成员是否可用，改好后停用再启用实例"
};

/** 最近一期被跳过时的说明：原因和下一步。 */
export function skipAdvice(reason?: string): string {
  if (!reason) return "最近一期被跳过，请在下方历史结果里看原因。";
  const next = skipNextSteps[reason];
  return `最近一期被跳过：${skipReasonLabel(reason)}。${next ? `${next}。` : "请在下方历史结果里看详情。"}`;
}

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
  tranche_filter_after: "被全部子策略的后置过滤剔除",
  volume_zero: "本期无成交（volume=0），不作为候选",
  exit: "触发 exit",
  no_entry: "未触发 entry",
  entry_and_exit: "entry 与 exit 同时成立",
  min_age_bars: "上市时间不足",
  no_row: "本期无行",
  score_invalid: "分数无效",
  not_expected: "已不在预期集合",
  period_skipped: "本期整期跳过，未求值",
  holding: "延续批次持有",
  prev_hold: "只缺上一根，保持持仓不动",
  market_hold: "所在市场本期不可用，保持持仓不动",
  stop_loss: "固定止损",
  trailing_stop: "跟踪止损",
  take_profit: "止盈",
  cooldown: "止损后冷却中，不能入场",
  aged_out_max: "已不是新标的（超过 max_age_bars）",
  age_markets: "没在全部绑定市场上市，或某个市场本期不可用（age_markets: all）",
  scale_invalid: "仓位系数无效，按缺数处理",
  missing_kept: "本期缺数，按 on_missing 保持持仓不动",
  group_full: "所属组名额已满",
  group_none: "分组值为空，不属于任何组",
  "select.then": "二次排序未取到",
  hold_kept: "继续持有",
  hold_entered: "新入场",
  hold_not_entered: "入场条件不成立",
  hold_cooling: "出场后冷却中，不能入场",
  exit_condition: "触发 hold.exit 出场",
  exit_max_bars: "持满 max_bars 出场",
  exit_filter: "不再满足 filter，出场",
  exit_missing: "缺数出场"
};

function reasonText(reason: string): string {
  // 止损的标的会在原因后附上原有原因（例如 stop_loss;missing:x、tranche:2;stopped:1），逐段翻译。
  if (reason.includes(";")) return reason.split(";").map(reasonText).join("；");
  if (/^stopped:\d+$/.test(reason)) return `本期有 ${reason.slice("stopped:".length)} 个持仓单位止损`;
  if (reason.startsWith("holding:")) return `延续批次持有，本期${reasonText(reason.slice("holding:".length))}`;
  if (/^tranche:\d+$/.test(reason)) return `由 ${reason.slice("tranche:".length)} 个子策略持有`;
  if (reason.startsWith("missing:")) return `缺少 ${reason.slice("missing:".length)}`;
  if (reason.startsWith("factor_failed:")) return `上游因子失败（${reason.slice("factor_failed:".length)}）`;
  if (reason.startsWith("source_failed:")) return `上游数据失败（${reason.slice("source_failed:".length)}）`;
  if (reason.startsWith("market_unavailable:")) return `市场本期不可用（${reason.slice("market_unavailable:".length)}）`;
  if (reason.startsWith("score_error:")) return `分数计算出错：${reason.slice("score_error:".length)}`;
  if (reason.startsWith("exit_stop:")) return `止损出场：${reasonText(reason.slice("exit_stop:".length))}`;
  if (reason.startsWith("select.then:")) return `二次排序分数无效：${reasonText(reason.slice("select.then:".length))}`;
  return itemReasons[reason] ?? reason;
}

/** 解释明细的原因：已知代码给出中文说明并附原文，带前缀的原因（holding:、tranche:、missing:、factor_failed:、source_failed:、market_unavailable:、score_error:、exit_stop:、select.then:）逐段翻译。 */
export function itemReasonLabel(reason?: string): string {
  if (!reason) return "-";
  const text = reasonText(reason);
  return text === reason ? reason : `${text}（${reason}）`;
}

const stages: Record<string, { label: string; color: string }> = {
  aged_out: { label: "年龄剔除", color: "gray" },
  missing: { label: "缺数", color: "red" },
  filtered: { label: "被过滤", color: "gray" },
  scored: { label: "未入选", color: "blue" },
  idle: { label: "未触发", color: "gray" },
  weighted: { label: "入选", color: "green" },
  dropped: { label: "被剔除", color: "orange" },
  held: { label: "保持持仓", color: "orange" },
  stopped: { label: "止损止盈", color: "red" }
};

export function stageLabel(stage: string): string {
  return stages[stage]?.label ?? stage;
}

export function stageColor(stage: string): string {
  return stages[stage]?.color ?? "blue";
}

/** 解释明细的阶段顺序：入选的放最前面，其次是保持持仓、止损止盈、被剔除、打分未入选，最后是缺数、未触发、被过滤和年龄剔除（原因区分年龄不足、已不是新标的、未在全部市场上市）。 */
const stageOrder: Record<string, number> = {
  weighted: 0,
  held: 1,
  stopped: 2,
  dropped: 3,
  scored: 4,
  missing: 5,
  idle: 6,
  filtered: 7,
  aged_out: 8
};

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
  /** 本期触发的持仓单位数（开了 stop 的规则才有）。 */
  stop_loss?: number;
  trailing_stop?: number;
  take_profit?: number;
}

export interface ResultSummary {
  universe: number;
  rules: Record<string, RuleSummary>;
  gross: string;
  net: string;
  /** 混合市场策略分市场的净敞口；此时 cash 没有意义，后端不填。 */
  spotNet: string;
  swapNet: string;
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
    spotNet: value.spot_net ?? "",
    swapNet: value.swap_net ?? "",
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

/** 一个市场绑定的计算任务与结果 View。 */
export interface ResolvedTask {
  market: string;
  factor_task_id: string;
  view_id: string;
  dataset_id: string;
  source_dataset_id: string;
}

export interface ResolvedBinding {
  /** 市场模式：select 取 spot | swap | both | equity，prefer 取 spot | swap | equity。 */
  market: { select: string; prefer: string };
  tasks: ResolvedTask[];
  bar: string;
  calendar: string;
  min_age_bars: number;
  /** 各规则的年龄要求；只在有规则不同于策略级 min_age_bars 时出现。 */
  rule_ages: { rule_id: string; min_bars: number; max_bars: number; all_markets: boolean }[];
  columns: ResolvedColumn[];
}

/** 解析启用时固化的绑定（resolved_json）；未启用过的实例返回 null。 */
export function parseResolved(raw?: string): ResolvedBinding | null {
  const value = parseObject(raw);
  const tasks = Object.entries((value.tasks ?? {}) as Record<string, any>)
    .map(([market, task]) => ({
      market,
      factor_task_id: task?.factor_task_id ?? "",
      view_id: task?.view_id ?? "",
      dataset_id: task?.dataset_id ?? "",
      source_dataset_id: task?.source_dataset_id ?? ""
    }))
    .sort((a, b) => MARKET_ORDER.indexOf(a.market as any) - MARKET_ORDER.indexOf(b.market as any));
  if (!tasks.length) return null;
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
    market: { select: value.market?.select ?? "", prefer: value.market?.prefer ?? "" },
    tasks,
    bar: value.bar ?? "",
    calendar: value.calendar ?? "",
    min_age_bars: Number(value.min_age_bars ?? 0),
    rule_ages: Object.entries((value.rule_ages ?? {}) as Record<string, any>)
      .map(([ruleID, age]) => ({
        rule_id: ruleID,
        min_bars: Number(age?.min_bars ?? 0),
        max_bars: Number(age?.max_bars ?? 0),
        all_markets: Boolean(age?.all_markets)
      }))
      .sort((a, b) => a.rule_id.localeCompare(b.rule_id)),
    columns
  };
}

/** 一条规则年龄要求的说明：上市满 N 根、上市不满 M 根、按全部市场判断。 */
export function ruleAgeText(age: { min_bars: number; max_bars: number; all_markets: boolean }): string {
  const parts: string[] = [];
  if (age.min_bars) parts.push(`满 ${age.min_bars} 根`);
  if (age.max_bars) parts.push(`不满 ${age.max_bars} 根`);
  if (age.all_markets) parts.push("全部市场");
  return parts.length ? parts.join("，") : "不限";
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

/** 混合市场策略显示分市场净敞口（现金没有意义），其余显示现金比例。 */
export function cashOrNetText(summary: Pick<ResultSummary, "cash" | "spotNet" | "swapNet">): string {
  if (summary.spotNet || summary.swapNet) return `现货净 ${percent(summary.spotNet)} · 永续净 ${percent(summary.swapNet)}`;
  return percent(summary.cash);
}

/**
 * 按 K 线结束时间从旧到新比较同一会话内相邻的 ok 结果，得到每期的目标成员变化与产出延迟。
 * 比较的是目标集合（不含权重），按（标的、市场）区分：同一个币从现货换到永续也算一进一出；也不代表账户实际持仓。
 * 本页结果涉及多个市场时，变化里的标的带上市场名。
 * 不同会话的结果互不比较：每个会话的首期 comparable 为 false；分页边界上最旧的一期同理。
 * 跳过期不改变该会话的比较基线，成员变化跨过跳过期比较。
 */
export function buildResultInsights(results: ResultLike[]): Record<string, ResultInsight> {
  const ordered = [...results].sort((a, b) => Date.parse(a.bar_end_time) - Date.parse(b.bar_end_time));
  const insights: Record<string, ResultInsight> = {};
  const baselines = new Map<string, Set<string>>();
  const markets = new Set(ordered.flatMap(item => item.targets.map(target => target.market || "")));
  const keyOf = (target: InstrumentTarget) => `${target.instrument_id}\u0000${target.market || ""}`;
  const labelOf = (key: string) => {
    const [id, market] = key.split("\u0000");
    return markets.size > 1 && market ? `${id}（${marketLabel(market)}）` : id;
  };
  for (const item of ordered) {
    const barEnd = Date.parse(item.bar_end_time);
    const created = Date.parse(item.created_at);
    const delayMs = Number.isFinite(barEnd) && Number.isFinite(created) ? created - barEnd : null;
    const insight: ResultInsight = { added: [], removed: [], comparable: false, delayMs };
    if (item.status === "ok") {
      const current = new Set(item.targets.map(keyOf));
      const previous = baselines.get(item.session_id);
      if (previous) {
        insight.comparable = true;
        insight.added = [...current]
          .filter(key => !previous.has(key))
          .map(labelOf)
          .sort();
        insight.removed = [...previous]
          .filter(key => !current.has(key))
          .map(labelOf)
          .sort();
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

/**
 * 实例实际运行的 DSL 版本：取本会话最新结果的 dsl_hash；和定义当前的 dsl_hash 不同时说明要停用再启用才生效。
 * 本会话还没有结果时返回空串。
 */
export function runningVersionText(
  sessionId: string,
  results: Pick<StrategyResult, "session_id" | "dsl_hash">[],
  definitionHash?: string
) {
  const latest = results.find(item => item.session_id === sessionId && item.dsl_hash);
  if (!sessionId || !latest) return "";
  const running = `运行版本 ${shortHash(latest.dsl_hash)}`;
  return definitionHash && definitionHash !== latest.dsl_hash ? `${running}，定义已更新，停用再启用后生效` : running;
}
