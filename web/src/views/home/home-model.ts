import type { ActivityLogItem } from "@/api/admin/activity";
import type { FactorSetInfo } from "@/api/factor/types";
import type { HealthAlert, HealthHost, HealthOverview } from "@/api/monitor";
import type { Strategy, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import { closedBarLag, freqSeconds, runBarLag, setHealth } from "@/views/factor/shared/health";
import { statusLabel } from "@/views/ops/monitor/health-display";
import { resultsLink, type StrategyLink } from "@/views/strategy/links";
import { percent, rankTargets, targetStateTag } from "@/views/strategy/model";
import { pendingItems, stateOf } from "@/views/strategy/overview/overview-model";
import { sortInstancesForResults } from "@/views/strategy/results/results-model";

/** 首页统一的状态色：正常、需要关注、异常、无观测。 */
export type Tone = "ok" | "warn" | "bad" | "idle";

export type HomeTarget = string | StrategyLink;

/** 数据集与 View 的显示名称，用来把监控、日志里的内部 ID 换成人能读的名字。 */
export interface NameBook {
  datasets: Record<string, string>;
  views: Record<string, string>;
}

export function toneOf(status?: string): Tone {
  if (status === "healthy") return "ok";
  if (status === "degraded") return "warn";
  if (status === "down") return "bad";
  return "idle";
}

/** int64 在 ProtoJSON 里是字符串：统一转成数字，无效时为 null。 */
export function toSeconds(raw?: number | string | null): number | null {
  if (raw === undefined || raw === null || (typeof raw === "string" && raw.trim() === "")) return null;
  const value = Number(raw);
  return Number.isFinite(value) ? Math.max(0, Math.floor(value)) : null;
}

/** 时长的中文写法：秒、分钟、小时（带分钟）、天。 */
export function humanizeDuration(seconds: number | null): string {
  if (seconds === null) return "—";
  if (seconds < 60) return `${seconds} 秒`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`;
  if (seconds < 86400) {
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    return minutes ? `${hours} 小时 ${minutes} 分` : `${hours} 小时`;
  }
  return `${(seconds / 86400).toFixed(1)} 天`;
}

/** 解析 ISO 时间或 Unix 秒；无法解析返回 null。 */
export function toMillis(value?: string | number | null): number | null {
  if (value === undefined || value === null || value === "") return null;
  if (typeof value === "number" || /^\d+$/.test(String(value))) {
    const seconds = Number(value);
    return seconds > 0 ? seconds * 1000 : null;
  }
  const parsed = Date.parse(String(value));
  return Number.isFinite(parsed) ? parsed : null;
}

/** 相对时间：刚刚、N 分钟前、N 小时前、N 天前。 */
export function relativeTime(value: string | number | null | undefined, nowMs: number): string {
  const at = toMillis(value);
  if (at === null) return "—";
  const diff = Math.round((nowMs - at) / 1000);
  if (diff < 0) return shortTime(value, nowMs);
  if (diff < 5) return "刚刚";
  if (diff < 60) return `${diff} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

/** 短时间：当天只写时分，其他日期写月-日 时分。 */
export function shortTime(value: string | number | null | undefined, nowMs: number): string {
  const at = toMillis(value);
  if (at === null) return "—";
  const date = new Date(at);
  const now = new Date(nowMs);
  const pad = (n: number) => String(n).padStart(2, "0");
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`;
  return date.toDateString() === now.toDateString() ? time : `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${time}`;
}

/** 精确到秒的时间：当天只写时分秒，其他日期写月-日 时分秒（本地时区）。 */
export function clockTime(value: string | number | null | undefined, nowMs: number): string {
  const at = toMillis(value);
  if (at === null) return "—";
  const date = new Date(at);
  const pad = (n: number) => String(n).padStart(2, "0");
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
  return date.toDateString() === new Date(nowMs).toDateString()
    ? time
    : `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${time}`;
}

/** 把文本里的数据集 / View 内部 ID 换成名称（名称未知时保留原 ID）。 */
export function prettifyIds(text: string, names: NameBook): string {
  return text.replace(/\b(dataset|view)_[a-z0-9_]+\b/gi, id => names.datasets[id] || names.views[id] || id);
}

const STAGE_LABELS: Record<string, string> = { collector: "采集", storage: "入库", storage_view: "视图", factor: "因子" };

export interface FreshnessRow {
  key: string;
  datasetId: string;
  stage: string;
  name: string;
  freq: string;
  lag: string;
  tone: Tone;
  status: string;
  reason: string;
  path: string;
  /** 延迟秒数；没有观测为 null。 */
  seconds: number | null;
}

const TONE_RANK: Record<Tone, number> = { bad: 0, warn: 1, idle: 2, ok: 3 };
/** 数据新鲜度的排序：没有观测的放最后，不挡在正常项前面。 */
const FRESHNESS_RANK: Record<Tone, number> = { bad: 0, warn: 1, ok: 2, idle: 3 };

/**
 * 数据新鲜度：每个数据集在各环节落后了多少，异常的排前面，其次落后多的。
 * 落后量以“最近一根已收盘的 K 线”为基准，不是 现在 − 水位时间：水位是 K 线的开盘时间，
 * 用当前时间去减，一个完全追平的 1m 数据集也会常年显示 1～2 分钟。时钟取监控概览的生成时间，避免浏览器时钟偏差。
 */
export function freshnessRows(overview: HealthOverview | undefined, names: NameBook, nowMs: number = Date.now()): FreshnessRow[] {
  const clockSeconds = (toMillis(overview?.generated_at) ?? nowMs) / 1000;
  const entries = (overview?.pipeline || []).flatMap(stage =>
    (stage.datasets || []).map(item => {
      const id = item.dataset_id || "";
      const producer = item.producer || "";
      const tone = toneOf(item.status);
      const watermark = toMillis(item.output_watermark_at);
      const rawSeconds = toSeconds(item.lag_seconds);
      const barLag =
        watermark !== null && freqSeconds(item.freq || "") > 0
          ? closedBarLag(item.freq || "", watermark / 1000, clockSeconds).seconds
          : null;
      // 状态未知时服务端给的 0 不代表“已追平”，不显示；正常即已追平；异常时优先写落后的 K 线时长，没有水位时退回原始延迟。
      const behind = tone === "idle" ? null : tone === "ok" ? 0 : barLag || rawSeconds;
      const row: FreshnessRow = {
        key: `${producer}/${item.space_id}/${id}/${item.freq}`,
        datasetId: id,
        stage: STAGE_LABELS[producer] || stage.name || producer,
        name: names.datasets[id] || names.views[id] || id,
        freq: item.freq || "",
        lag: behind === null ? "—" : behind === 0 ? "已追平" : humanizeDuration(behind),
        tone,
        status: statusLabel(item.status),
        reason: item.reason || "",
        path: producer === "factor" ? "/factor/tasks?tab=results" : "/collector/tasks?tab=results",
        seconds: behind
      };
      return { row, seconds: behind ?? -1 };
    })
  );
  // 同为已追平时不再比较，保持管线顺序（入库 → 视图 → 因子），Array.sort 是稳定排序。
  return entries
    .sort((a, b) => FRESHNESS_RANK[a.row.tone] - FRESHNESS_RANK[b.row.tone] || b.seconds - a.seconds)
    .map(entry => entry.row);
}

export interface FactorRow {
  setId: string;
  label: string;
  factors: number;
  health: { key: string; label: string; color: string };
  tone: Tone;
  lastPeriod: string;
  lag: string;
  /** 落后的已收盘 K 线时长（秒）；没有周期为 null，0 表示已追平。 */
  behindSeconds: number | null;
  /** 最近一个已计算周期的收盘时刻（毫秒）；没有周期为 null。 */
  periodEndMs: number | null;
  path: string;
}

const FACTOR_TONE: Record<string, Tone> = { ok: "ok", lagging: "warn", degraded: "warn", failed: "bad" };

/** 因子计算任务：数据集名称 · 频率、启用因子数、最近周期与滞后。 */
export function factorRows(sets: FactorSetInfo[], spaceId: string, names: NameBook, nowMs: number): FactorRow[] {
  return sets
    .filter(info => info.factor_set.space_id === spaceId)
    .map(info => {
      const set = info.factor_set;
      const health = setHealth(set, info.last_run);
      const behindSeconds = info.last_run?.last_period_time ? runBarLag(set.freq, info.last_run).seconds : null;
      const periodOpenMs = toMillis(info.last_run?.last_period_time);
      const periodEndMs = periodOpenMs === null ? null : periodOpenMs + freqSeconds(set.freq) * 1000;
      return {
        setId: set.set_id,
        label: `${names.datasets[set.source_dataset_id] || set.source_dataset_id} · ${set.freq}`,
        factors: (info.members || []).filter(member => member.status === "enabled").length,
        health,
        tone: FACTOR_TONE[health.key] || "idle",
        lastPeriod: info.last_run?.last_period_time ? shortTime(info.last_run.last_period_time, nowMs) : "—",
        lag: behindSeconds === null ? "—" : behindText(behindSeconds),
        behindSeconds,
        periodEndMs,
        path: `/factor/tasks?tab=results&set=${encodeURIComponent(set.set_id)}`
      };
    })
    .sort((a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone] || a.label.localeCompare(b.label));
}

export interface StrategyRow {
  instanceId: string;
  name: string;
  enabled: boolean;
  health: string;
  state: { label: string; color: string };
  tone: Tone;
  /** 按权重绝对值从大到小；ratio 是权重条长度（0~1，相对本实例最大的一条，含现金）。 */
  holdings: { id: string; weight: string; short: boolean; ratio: number }[];
  more: number;
  holdingCount: number;
  /** 持仓合计（权重绝对值之和）与现金，0~1；没有目标快照为 null。 */
  gross: number | null;
  cash: { text: string; ratio: number } | null;
  lastBar: string;
  lastBarMs: number | null;
  link: StrategyLink;
}

const HOLDING_LIMIT = 4;

/** 策略实例：当前要持有什么（按权重排前几个）、目标是否有效、最近一期是多久前。 */
export function strategyRows(
  instances: StrategyInstance[],
  targets: Record<string, StrategyTargetSnapshot>,
  catalog: Strategy[],
  nowMs: number
): StrategyRow[] {
  return sortInstancesForResults(instances).map(instance => {
    const state = stateOf(instance, targets, nowMs);
    const snapshot = targets[instance.instance_id];
    const ranked = snapshot ? rankTargets(snapshot.targets) : [];
    const gross = snapshot ? ranked.reduce((total, row) => total + Math.abs(row.weight), 0) : null;
    const net = snapshot ? ranked.reduce((total, row) => total + row.weight, 0) : 0;
    const cashRatio = snapshot ? Math.max(0, 1 - net) : 0;
    const barMax = Math.max(...ranked.map(row => Math.abs(row.weight)), cashRatio, 0);
    const tone: Tone =
      instance.health === "degraded" || instance.health === "session_unverified"
        ? "warn"
        : !instance.enabled
          ? "idle"
          : state === "valid" || state === "zero"
            ? "ok"
            : state === "expired"
              ? "warn"
              : "idle";
    return {
      instanceId: instance.instance_id,
      name: catalog.find(item => item.strategy_id === instance.strategy_id)?.name || instance.strategy_id,
      enabled: instance.enabled,
      health: instance.health,
      state: instance.enabled ? targetStateTag(state) : { label: "已停用", color: "gray" },
      tone,
      holdings: ranked.slice(0, HOLDING_LIMIT).map(row => ({
        id: row.target.instrument_id,
        weight: percent(row.target.target_weight, 1),
        short: row.weight < 0,
        ratio: barMax > 0 ? Math.abs(row.weight) / barMax : 0
      })),
      more: Math.max(0, ranked.length - HOLDING_LIMIT),
      holdingCount: ranked.length,
      gross,
      cash: snapshot && instance.enabled ? { text: percent(cashRatio, 1), ratio: barMax > 0 ? cashRatio / barMax : 0 } : null,
      lastBar: snapshot?.bar_end_time ? relativeTime(snapshot.bar_end_time, nowMs) : "—",
      lastBarMs: toMillis(snapshot?.bar_end_time),
      link: resultsLink(instance.instance_id)
    };
  });
}

export interface AttentionItem {
  key: string;
  tone: "warn" | "bad";
  source: string;
  title: string;
  detail: string;
  when: string;
  target: HomeTarget;
}

function alertTone(alert: HealthAlert): "warn" | "bad" {
  return /critical|error|fatal|p0|p1|high|严重|紧急/i.test(alert.severity || "") ? "bad" : "warn";
}

/** 待处理：监控告警、策略实例与因子任务的问题合在一起，严重的在前。 */
export function attentionItems(input: {
  alerts: HealthAlert[];
  strategy: { instances: StrategyInstance[]; targets: Record<string, StrategyTargetSnapshot>; catalog: Strategy[] };
  factors: FactorRow[];
  freshness: FreshnessRow[];
  names: NameBook;
  nowMs: number;
  alertTarget: (alert: HealthAlert) => string;
}): AttentionItem[] {
  const items: AttentionItem[] = input.alerts.map(alert => ({
    key: `alert-${alert.id || alert.title}`,
    tone: alertTone(alert),
    source: "监控",
    title: prettifyIds(alert.title || alert.id || "告警", input.names),
    detail: prettifyIds(alert.reason || "", input.names),
    when: relativeTime(alert.triggered_at, input.nowMs),
    target: input.alertTarget(alert)
  }));
  for (const item of pendingItems({ ...input.strategy, replays: [], nowMs: input.nowMs })) {
    items.push({
      key: `strategy-${item.kind}-${item.instanceId}`,
      tone: item.kind === "degraded" ? "bad" : "warn",
      source: "策略",
      title: item.title,
      detail: item.text,
      when: "",
      target: item.target
    });
  }
  // 数据落后：监控已为同一数据集出过告警的不重复列出。
  const alerted = new Set(
    input.alerts
      .filter(alert => alert.object?.type === "dataset")
      .map(alert => `${alert.object?.dataset_id}/${alert.object?.freq}`)
  );
  for (const row of input.freshness) {
    if (row.tone !== "warn" && row.tone !== "bad") continue;
    if (alerted.has(`${row.datasetId}/${row.freq}`)) continue;
    items.push({
      key: `data-${row.key}`,
      tone: row.tone,
      source: "数据",
      title: `${row.stage} · ${row.name}${row.freq ? ` · ${row.freq}` : ""}`,
      detail: `落后 ${row.lag}`,
      when: "",
      target: row.path
    });
  }
  for (const row of input.factors) {
    if (row.tone !== "warn" && row.tone !== "bad") continue;
    items.push({
      key: `factor-${row.setId}`,
      tone: row.tone,
      source: "因子",
      title: row.label,
      detail: `计算${row.health.label}，最近周期 ${row.lastPeriod}，${row.lag}`,
      when: "",
      target: row.path
    });
  }
  return items.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === "bad" ? -1 : 1));
}

export interface HostRow {
  key: string;
  name: string;
  tone: Tone;
  cpu: string;
  memory: string;
  disk: string;
  hot: { cpu: boolean; memory: boolean; disk: boolean };
  path: string;
}

const HOT_PERCENT = 85;

function percentText(value: number | undefined, available: boolean | undefined) {
  return available && typeof value === "number" && Number.isFinite(value) ? `${Math.round(value)}%` : "—";
}

export function hostRows(hosts: HealthHost[]): HostRow[] {
  return hosts.map(host => {
    const hot = {
      cpu: Boolean(host.cpu_available && (host.cpu_percent ?? 0) >= HOT_PERCENT),
      memory: Boolean(host.memory_available && (host.memory_percent ?? 0) >= HOT_PERCENT),
      disk: Boolean(host.disk_available && (host.disk_percent ?? 0) >= HOT_PERCENT)
    };
    const status = toneOf(host.status);
    return {
      key: host.host_id || host.agent_id || host.hostname || "",
      name: host.hostname || host.host_id || host.agent_id || "",
      tone: status === "ok" && (hot.cpu || hot.memory || hot.disk) ? "warn" : status,
      cpu: percentText(host.cpu_percent, host.cpu_available),
      memory: percentText(host.memory_percent, host.memory_available),
      disk: percentText(host.disk_percent, host.disk_available),
      hot,
      path: `/ops/hosts?tab=monitor&host_id=${encodeURIComponent(host.host_id || "")}&agent_id=${encodeURIComponent(host.agent_id || "")}`
    };
  });
}

const CATEGORY_LABELS: Record<string, string> = {
  "collector.fetch_plan_applied": "采集计划",
  "collector.subject_change": "标的变更"
};

export interface ActivityRow {
  key: string;
  when: string;
  category: string;
  title: string;
  tone: Tone;
}

/** 标的变更里“新增 0 个、移除 0 个”的记录没有信息量，首页不展示。 */
function isNoChange(item: ActivityLogItem): boolean {
  return item.category === "collector.subject_change" && /新增 0 个、移除 0 个/.test(item.title || "");
}

/** 活动标题压短：生效时间去掉年份，“，将于 2026-10-13 09:00 生效”写成“，10-13 09:00 生效”。 */
function compactTitle(title: string): string {
  return title.replace(/，将于 \d{4}-(\d{2}-\d{2} \d{2}:\d{2}) 生效/, "，$1 生效");
}

export function activityRows(items: ActivityLogItem[], names: NameBook, nowMs: number): ActivityRow[] {
  return items
    .filter(item => !isNoChange(item))
    .map((item, index) => ({
      key: String(item.id ?? index),
      when: shortTime(item.created_at, nowMs),
      category: CATEGORY_LABELS[item.category || ""] || item.category || item.source || "",
      title: compactTitle(prettifyIds(item.title || "", names)),
      tone: item.level === "error" ? "bad" : item.level === "warning" ? "warn" : "idle"
    }));
}

/** 顶部状态句：有待处理时写“N 项需要处理”，否则写“运行正常”；监控不可用时如实说明。 */
export function statusLine(input: { attention: AttentionItem[]; overview?: HealthOverview; monitorError: string }): {
  tone: Tone;
  text: string;
} {
  const bad = input.attention.filter(item => item.tone === "bad").length;
  if (input.attention.length) return { tone: bad ? "bad" : "warn", text: `${input.attention.length} 项需要处理` };
  if (input.monitorError) return { tone: "idle", text: "监控数据暂不可用" };
  if (!input.overview) return { tone: "idle", text: "正在读取状态" };
  return { tone: "ok", text: "运行正常" };
}

/** 磁贴里固定显示的条数，其余折叠为“另有 N 项”。 */
export const TILE_LIMITS = { attention: 5, freshness: 4, factors: 3, hosts: 3, activity: 6 } as const;

export function fold<T>(items: T[], limit: number): { shown: T[]; hidden: number } {
  return { shown: items.slice(0, limit), hidden: Math.max(0, items.length - limit) };
}

const TONE_ICON: Record<Tone, string> = {
  ok: "icon-check-circle-fill",
  warn: "icon-exclamation-circle-fill",
  bad: "icon-close-circle-fill",
  idle: "icon-minus-circle-fill"
};

export function toneIcon(tone: Tone): string {
  return TONE_ICON[tone];
}

function worst(tones: Tone[]): Tone {
  return tones.reduce<Tone>((acc, tone) => (TONE_RANK[tone] < TONE_RANK[acc] ? tone : acc), "ok");
}

export interface BannerFact {
  key: string;
  label: string;
  value: string;
  tone: Tone;
  /** 鼠标悬停时说明这个数字的口径。 */
  hint?: string;
}

/** 落后量的写法：0 为“已追平”，否则“落后 X”。 */
function behindText(seconds: number): string {
  return seconds === 0 ? "已追平" : `落后 ${humanizeDuration(seconds)}`;
}

interface FactCandidate {
  tone: Tone;
  atMs: number;
  /** 落后的已收盘 K 线时长（秒），没有这个概念的写 0。 */
  behind: number;
}

/** 选出 Banner 要展示的那一行：状态最差的优先，同样差时落后最多的优先；都正常则取时间最新的。 */
function pickFact(candidates: FactCandidate[]): FactCandidate | undefined {
  return [...candidates].sort(
    (a, b) => FRESHNESS_RANK[a.tone] - FRESHNESS_RANK[b.tone] || b.behind - a.behind || b.atMs - a.atMs
  )[0];
}

/**
 * Banner 里的三项：策略最近一期、因子计算写具体的收盘时间（精确到秒，由人对照当前时间判断是否异常，异常的换色），
 * 数据新鲜度写相对最近一根已收盘 K 线的落后量；没有观测时写“—”。
 */
export function bannerFacts(input: {
  strategies: StrategyRow[];
  factors: FactorRow[];
  freshness: FreshnessRow[];
  nowMs: number;
}): BannerFact[] {
  const strategy = pickFact(
    input.strategies
      .filter(row => row.enabled && row.lastBarMs !== null)
      .map(row => ({ tone: row.tone, atMs: row.lastBarMs as number, behind: 0 }))
  );
  const factor = pickFact(
    input.factors
      .filter(row => row.behindSeconds !== null && row.periodEndMs !== null)
      .map(row => ({ tone: row.tone, atMs: row.periodEndMs as number, behind: row.behindSeconds as number }))
  );
  const dataRows = input.freshness.filter(row => row.tone !== "idle" && row.seconds !== null);
  const dataBehind = Math.max(0, ...dataRows.map(row => row.seconds as number));
  return [
    {
      key: "strategy",
      label: "策略最近一期",
      value: strategy ? clockTime(strategy.atMs / 1000, input.nowMs) : "—",
      tone: strategy ? strategy.tone : "idle",
      hint: "已启用实例最近一份目标所属 K 线的收盘时间；有异常实例时显示异常的那一个"
    },
    {
      key: "factor",
      label: "因子计算",
      value: factor ? clockTime(factor.atMs / 1000, input.nowMs) : "—",
      tone: factor ? factor.tone : "idle",
      hint: "因子任务最近一个已计算周期的收盘时间；有滞后的任务时显示落后最多的那一个"
    },
    {
      key: "data",
      label: "数据新鲜度",
      value: dataRows.length ? behindText(dataBehind) : "—",
      tone: dataRows.length ? worst(dataRows.map(row => row.tone)) : "idle"
    }
  ];
}

export interface KpiCard {
  key: string;
  label: string;
  icon: string;
  tone: Tone;
  value: string;
  unit: string;
  note: string;
  target: HomeTarget;
}

/** 四张指标卡：策略目标、因子计算、数据新鲜度、当前告警；每张卡一个数字、一句说明。 */
export function kpiCards(input: {
  strategies: StrategyRow[];
  strategyLoaded: boolean;
  factors: FactorRow[];
  factorLoaded: boolean;
  freshness: FreshnessRow[];
  alertCount: number | undefined;
  monitorError: string;
}): KpiCard[] {
  const enabled = input.strategies.filter(row => row.enabled);
  const valid = enabled.filter(row => row.tone === "ok").length;
  const holdings = enabled.reduce((total, row) => total + row.holdingCount, 0);
  const only = enabled.length === 1 ? enabled[0] : undefined;
  const factorCount = input.factors.reduce((total, row) => total + row.factors, 0);
  const factorBad = input.factors.filter(row => row.tone === "warn" || row.tone === "bad").length;
  const observed = input.freshness.filter(row => row.tone !== "idle");
  const fresh = observed.filter(row => row.tone === "ok").length;
  const stale = observed.filter(row => row.tone === "warn" || row.tone === "bad").length;
  const monitorDown = Boolean(input.monitorError);
  return [
    {
      key: "strategy",
      label: "策略目标",
      icon: "icon-compass",
      tone: !input.strategyLoaded || !enabled.length ? "idle" : valid === enabled.length ? "ok" : "warn",
      value: input.strategyLoaded ? String(valid) : "—",
      unit: input.strategyLoaded ? ` / ${enabled.length} 有效` : "",
      note: !input.strategyLoaded
        ? "读取中"
        : !enabled.length
          ? "没有运行中的实例"
          : `${holdings} 个标的${only && only.gross !== null ? ` · ${Math.round(only.gross * 100)}% 仓位` : ""}`,
      target: resultsLink()
    },
    {
      key: "factor",
      label: "因子计算",
      icon: "icon-experiment",
      tone: !input.factorLoaded || !input.factors.length ? "idle" : factorBad ? "warn" : "ok",
      value: input.factorLoaded ? String(input.factors.length) : "—",
      unit: input.factorLoaded ? " 个任务" : "",
      note: !input.factorLoaded ? "读取中" : factorBad ? `${factorBad} 个异常` : `${factorCount} 个因子 · 全部正常`,
      target: "/factor/tasks?tab=results"
    },
    {
      key: "freshness",
      label: "数据新鲜度",
      icon: "icon-storage",
      tone: monitorDown || !observed.length ? "idle" : stale ? "warn" : "ok",
      value: monitorDown || !observed.length ? "—" : String(fresh),
      unit: monitorDown || !observed.length ? "" : ` / ${observed.length} 正常`,
      note: monitorDown ? "监控数据暂不可用" : !observed.length ? "暂无数据链路观测" : stale ? `${stale} 条落后` : "全部及时",
      target: "/collector/tasks?tab=results"
    },
    {
      key: "alerts",
      label: "当前告警",
      icon: "icon-notification",
      tone: monitorDown || input.alertCount === undefined ? "idle" : input.alertCount > 0 ? "bad" : "idle",
      value: monitorDown || input.alertCount === undefined ? "—" : String(input.alertCount),
      unit: "",
      note: monitorDown ? "监控数据暂不可用" : "组件、数据与主机",
      target: "/ops/monitor"
    }
  ];
}
