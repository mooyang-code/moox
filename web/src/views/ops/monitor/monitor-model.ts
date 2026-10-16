import type {
  AlertIncident,
  HealthAlert,
  HealthBusinessCheck,
  HealthComponent,
  HealthDataset,
  HealthHost,
  HealthOverview
} from "@/api/monitor";
import {
  humanizeDuration,
  prettifyIds,
  relativeTime,
  shortTime,
  toMillis,
  toSeconds,
  type NameBook,
  type Tone
} from "@/views/home/home-model";
import { alertLink, hostHref, statusLabel } from "./health-display";

/** 触发时间相差不超过 60 秒的告警多半是同一个原因（服务重启、网络抖动），合并成一组展示。 */
export const SAME_CAUSE_WINDOW_MS = 60_000;
/** 1 分钟内自动恢复的告警经过算作闪断，默认折叠。 */
export const FLAP_MS = 60_000;

const TONE_RANK: Record<Tone, number> = { bad: 0, warn: 1, idle: 2, ok: 3 };

function worst(tones: Tone[]): Tone {
  return tones.reduce<Tone>((acc, tone) => (TONE_RANK[tone] < TONE_RANK[acc] ? tone : acc), "ok");
}

export function severityTone(severity?: string): Tone {
  return severity === "warning" ? "warn" : "bad";
}

export function severityLabel(severity?: string): string {
  return severity === "warning" ? "警告" : "严重";
}

/** 概览的服务端时钟；没有时用浏览器时钟。持续时长都按它计算，避免浏览器时钟偏差。 */
export function serverNow(overview: HealthOverview | undefined, fallbackMs: number): number {
  return toMillis(overview?.generated_at) ?? fallbackMs;
}

/** 毫秒时间戳转 ISO 字符串：首页的时间工具把数字当作 Unix 秒。 */
const iso = (ms: number) => new Date(ms).toISOString();

function since(startMs: number | null, nowMs: number): string {
  return startMs === null ? "—" : humanizeDuration(Math.max(0, Math.floor((nowMs - startMs) / 1000)));
}

const LAYER_LABELS: Record<string, string> = { component: "组件", dataset: "数据链路", host: "主机", business: "业务检查" };

// ---------- 当前告警 ----------

export interface Fact {
  label: string;
  value: string;
}

export interface AlertLine {
  key: string;
  tone: Tone;
  severity: string;
  title: string;
  reason: string;
  rawError: string;
  layer: string;
  startedMs: number | null;
  duration: string;
  recovering: boolean;
  facts: Fact[];
  link: { href: string; label: string };
}

export interface AlertGroup {
  key: string;
  tone: Tone;
  startedMs: number | null;
  duration: string;
  lines: AlertLine[];
}

function datasetFacts(dataset: HealthDataset | undefined, nowMs: number): Fact[] {
  if (!dataset) return [];
  const facts: Fact[] = [];
  if (dataset.output_watermark_at) facts.push({ label: "最新数据", value: shortTime(dataset.output_watermark_at, nowMs) });
  const lag = toSeconds(dataset.lag_seconds);
  if (lag !== null) facts.push({ label: "已落后", value: humanizeDuration(lag) });
  const limit = toSeconds(dataset.lag_limit_seconds);
  if (limit) facts.push({ label: "允许落后", value: humanizeDuration(limit) });
  if (dataset.last_success_at) facts.push({ label: "最近成功", value: relativeTime(dataset.last_success_at, nowMs) });
  return facts;
}

function alertLine(alert: HealthAlert, names: NameBook, nowMs: number): AlertLine {
  const startedMs = toMillis(alert.triggered_at);
  const facts: Fact[] = [
    { label: "触发时间", value: shortTime(alert.triggered_at, nowMs) },
    { label: "最新检查", value: relativeTime(alert.last_checked_at, nowMs) },
    ...datasetFacts(alert.dataset, nowMs)
  ];
  return {
    key: alert.id || alert.title || "",
    tone: severityTone(alert.severity),
    severity: severityLabel(alert.severity),
    title: prettifyIds(alert.title || alert.id || "告警", names),
    reason: prettifyIds(alert.reason || "", names),
    rawError: alert.raw_error || "",
    layer: LAYER_LABELS[alert.object?.type || ""] || "业务检查",
    startedMs,
    duration: since(startedMs, nowMs),
    recovering: Boolean(alert.recovering),
    facts,
    link: alertLink(alert.object)
  };
}

/** 按触发时间先后把相隔不超过 SAME_CAUSE_WINDOW_MS 的项归为一组；没有触发时间的各自成组。 */
function groupByStart<T extends { startedMs: number | null }>(items: T[]): T[][] {
  const sorted = [...items].sort((a, b) => (a.startedMs ?? Infinity) - (b.startedMs ?? Infinity));
  const groups: T[][] = [];
  for (const item of sorted) {
    const current = groups[groups.length - 1];
    const first = current?.[0]?.startedMs;
    if (current && first != null && item.startedMs != null && item.startedMs - first <= SAME_CAUSE_WINDOW_MS) current.push(item);
    else groups.push([item]);
  }
  return groups;
}

/** 当前告警：同一分钟内触发的合并成一组，严重的排在组内前面；持续最久的组排最前。 */
export function alertGroups(overview: HealthOverview | undefined, names: NameBook, nowMs: number): AlertGroup[] {
  const lines = (overview?.alerts || []).map(alert => alertLine(alert, names, nowMs));
  return groupByStart(lines).map(group => {
    const ordered = [...group].sort((a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone]);
    const startedMs = group[0].startedMs;
    return {
      key: ordered.map(line => line.key).join("|"),
      tone: worst(group.map(line => line.tone)),
      startedMs,
      duration: since(startedMs, nowMs),
      lines: ordered
    };
  });
}

// ---------- 分层健康 ----------

export type LayerKey = "components" | "pipeline" | "business" | "hosts";

export interface LayerSummary {
  key: LayerKey;
  title: string;
  unit: string;
  total: number;
  healthy: number;
  attention: number;
  /** 已停用或不探测，不计入正常比例。 */
  inactive: number;
  tone: Tone;
}

interface Classified {
  tone: Tone;
  attention: boolean;
  inactive: boolean;
}

/** 状态分类：异常、需关注、未知算需要关注；已停用、不探测不计入正常比例。 */
export function classify(status?: string): Classified {
  switch (status) {
    case "healthy":
      return { tone: "ok", attention: false, inactive: false };
    case "degraded":
      return { tone: "warn", attention: true, inactive: false };
    case "down":
      return { tone: "bad", attention: true, inactive: false };
    case "disabled":
    case "unchecked":
      return { tone: "idle", attention: false, inactive: true };
    default:
      return { tone: "idle", attention: true, inactive: false };
  }
}

/** 组件状态：不探测的组件按指标上报判断；两者都不观测时不计入正常比例。 */
export function componentClass(item: HealthComponent): Classified {
  if (item.status !== "unchecked") return classify(item.status);
  const reporter = item.reporter?.status;
  return reporter && reporter !== "unchecked" ? classify(reporter) : classify("unchecked");
}

function summarize(key: LayerKey, title: string, unit: string, items: Classified[]): LayerSummary {
  const attention = items.filter(item => item.attention);
  return {
    key,
    title,
    unit,
    total: items.length,
    healthy: items.filter(item => item.tone === "ok").length,
    attention: attention.length,
    inactive: items.filter(item => item.inactive).length,
    tone: attention.length ? worst(attention.map(item => (item.tone === "idle" ? "warn" : item.tone))) : "ok"
  };
}

export function layerSummaries(overview: HealthOverview | undefined): LayerSummary[] {
  const components = (overview?.components || []).map(componentClass);
  const unregistered = (overview?.unregistered || []).map(() => classify("degraded"));
  const datasets = (overview?.pipeline || []).flatMap(stage => stage.datasets || []).map(item => classify(item.status));
  const business = (overview?.business_checks || []).map(item => classify(item.status));
  const hosts = (overview?.hosts || []).map(item => classify(item.status));
  return [
    summarize("components", "组件", "个组件", [...components, ...unregistered]),
    summarize("pipeline", "数据链路", "条数据链路", datasets),
    summarize("business", "业务检查", "项业务检查", business),
    summarize("hosts", "主机", "台主机", hosts)
  ];
}

/** 层的计数文案，例如“21 / 22 正常 · 1 项停用或不探测”。 */
export function layerCountText(layer: LayerSummary): string {
  const active = layer.total - layer.inactive;
  if (!layer.total) return "暂无观测";
  const base = active ? `${layer.healthy} / ${active} 正常` : "全部停用或不探测";
  return layer.inactive && active ? `${base} · ${layer.inactive} 项停用或不探测` : base;
}

// ---------- 结论 ----------

export interface Verdict {
  tone: Tone;
  title: string;
  detail: string;
}

export function verdict(input: {
  overview?: HealthOverview;
  loaded: boolean;
  error: string;
  groups: AlertGroup[];
  layers: LayerSummary[];
  nowMs: number;
}): Verdict {
  const { overview, loaded, error, groups, layers, nowMs } = input;
  if (!loaded)
    return error
      ? { tone: "idle", title: "监控数据暂不可用", detail: error }
      : { tone: "idle", title: "正在读取监控数据…", detail: "" };
  if (error) {
    return {
      tone: "idle",
      title: "监控数据刷新失败",
      detail: `${error}。下面是 ${relativeTime(overview?.generated_at, nowMs)}读取的数据，不代表当前状态`
    };
  }
  const count = groups.reduce((sum, group) => sum + group.lines.length, 0);
  if (count) {
    const parts = [`最长已持续 ${groups[0].duration}`];
    const shared = groups.filter(group => group.lines.length > 1);
    if (shared.length) {
      const sharedCount = shared.reduce((sum, group) => sum + group.lines.length, 0);
      parts.push(`其中 ${sharedCount} 项同一时间触发，可能是同一个原因`);
    }
    return { tone: worst(groups.map(group => group.tone)), title: `${count} 项告警进行中`, detail: parts.join(" · ") };
  }
  if (!overview?.topology_known) {
    return { tone: "idle", title: "部署清单尚未同步", detail: "暂时无法判断完整的组件健康状态" };
  }
  const attention = layers.filter(layer => layer.attention);
  if (attention.length) {
    const total = attention.reduce((sum, layer) => sum + layer.attention, 0);
    return {
      tone: "warn",
      title: `没有告警，${total} 项需要关注`,
      detail: attention.map(layer => `${layer.title} ${layer.attention}`).join(" · ")
    };
  }
  return {
    tone: "ok",
    title: "全部正常",
    detail: layers
      .filter(layer => layer.total)
      .map(layer => `${layer.total - layer.inactive} ${layer.unit}`)
      .join(" · ")
  };
}

// ---------- 告警经过 ----------

export interface IncidentRow {
  key: string;
  tone: Tone;
  startedMs: number | null;
  time: string;
  title: string;
  /** 同一时间触发的其他告警标题。 */
  others: string[];
  reason: string;
  outcome: string;
  active: boolean;
  flap: boolean;
  notifyFailed: boolean;
  reminders: number;
}

export interface IncidentTimeline {
  windowHours: number;
  /** 告警经过的条数（合并前）。 */
  total: number;
  rows: IncidentRow[];
  flaps: number;
  active: number;
}

interface IncidentItem {
  incident: AlertIncident;
  startedMs: number | null;
  endedMs: number | null;
  tone: Tone;
  title: string;
}

type IncidentState = "active" | "resolved" | "revoked";

function incidentState(item: IncidentItem): IncidentState {
  if (item.incident.active) return "active";
  return item.endedMs !== null ? "resolved" : "revoked";
}

function incidentOutcome(state: IncidentState, group: IncidentItem[], startedMs: number | null, nowMs: number) {
  if (state === "active")
    return { outcome: startedMs === null ? "进行中" : `进行中 · 已持续 ${since(startedMs, nowMs)}`, flap: false };
  if (state === "revoked") return { outcome: "已撤销（检查停用或移除）", flap: false };
  const lastEnd = Math.max(...group.map(item => item.endedMs ?? 0));
  if (startedMs === null) return { outcome: `${shortTime(iso(lastEnd), nowMs)} 恢复`, flap: false };
  const lasted = Math.max(0, lastEnd - startedMs);
  return { outcome: `${humanizeDuration(Math.round(lasted / 1000))}后恢复`, flap: lasted <= FLAP_MS };
}

/**
 * 最近窗口内的告警经过：状态相同（进行中、已恢复、已撤销）且同一分钟内触发的合并为一行，
 * 1 分钟内自动恢复的标为闪断；最新的在前。
 */
export function incidentTimeline(overview: HealthOverview | undefined, names: NameBook, nowMs: number): IncidentTimeline {
  const incidents = overview?.incidents || [];
  const items: IncidentItem[] = incidents.map(incident => ({
    incident,
    startedMs: toMillis(incident.triggered_at),
    endedMs: toMillis(incident.resolved_at),
    tone: severityTone(incident.severity),
    title: prettifyIds(incident.title || incident.id || "告警", names)
  }));
  const states: IncidentState[] = ["active", "resolved", "revoked"];
  const rows = states.flatMap(state =>
    groupByStart(items.filter(item => incidentState(item) === state)).map(group => {
      const ordered = [...group].sort((a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone] || (a.startedMs ?? 0) - (b.startedMs ?? 0));
      const primary = ordered[0];
      const startedMs = group[0].startedMs;
      const { outcome, flap } = incidentOutcome(state, group, startedMs, nowMs);
      return {
        key: group.map(item => item.incident.id).join("|"),
        tone: state === "active" ? worst(group.map(item => item.tone)) : "idle",
        startedMs,
        time: startedMs === null ? "窗口之前" : shortTime(iso(startedMs), nowMs),
        title: primary.title,
        others: ordered.slice(1).map(item => item.title),
        reason: prettifyIds(primary.incident.reason || "", names),
        outcome,
        active: state === "active",
        flap,
        notifyFailed: group.some(item => item.incident.notify_failed),
        reminders: group.reduce((sum, item) => sum + (item.incident.reminder_count || 0), 0)
      } satisfies IncidentRow;
    })
  );
  rows.sort((a, b) => (b.startedMs ?? -Infinity) - (a.startedMs ?? -Infinity));
  return {
    windowHours: overview?.incident_window_hours || 48,
    total: incidents.length,
    rows,
    flaps: rows.filter(row => row.flap).length,
    active: rows.filter(row => row.active).length
  };
}

// ---------- 组件 ----------

export interface ComponentChip {
  key: string;
  name: string;
  tone: Tone;
  attention: boolean;
  status: string;
  reason: string;
  item: HealthComponent;
}

export interface HostComponents {
  hostId: string;
  chips: ComponentChip[];
}

function componentStatusText(item: HealthComponent): string {
  if (item.status === "unchecked") {
    const reporter = item.reporter?.status;
    return reporter && reporter !== "unchecked" ? `不探测 · 上报${statusLabel(reporter)}` : "不探测";
  }
  return statusLabel(item.status);
}

/** 组件按部署主机分组；每台主机内异常的排前面。 */
export function componentsByHost(overview: HealthOverview | undefined): HostComponents[] {
  const hosts = new Map<string, ComponentChip[]>();
  for (const item of overview?.components || []) {
    const hostId = item.host_id || "未知主机";
    const cls = componentClass(item);
    const chips = hosts.get(hostId) || [];
    chips.push({
      key: `${hostId}/${item.component_id}`,
      name: item.name || item.component_id || "",
      tone: cls.tone,
      attention: cls.attention,
      status: componentStatusText(item),
      reason: item.reason || "",
      item
    });
    hosts.set(hostId, chips);
  }
  return [...hosts.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([hostId, chips]) => ({ hostId, chips: chips.sort((a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone]) }));
}

// ---------- 数据链路 ----------

const STAGE_LABELS: Record<string, string> = { collection: "采集", storage: "存储与视图", factor: "因子", trade: "交易" };
const PRODUCER_LABELS: Record<string, string> = {
  collector: "采集",
  storage: "入库",
  storage_view: "视图",
  factor: "因子",
  trade: "交易"
};

export interface DatasetRow {
  key: string;
  name: string;
  producer: string;
  spaceId: string;
  freq: string;
  /** 输出水位落后多久；与告警判定用的是同一个量。 */
  lag: string;
  /** 允许落后多久，超过即告警。 */
  limit: string;
  /** 落后量相对允许值的色调：超过为红，超过八成为橙。 */
  lagTone: Tone;
  tone: Tone;
  status: string;
  reason: string;
  attention: boolean;
  /** 落后量占允许值的比例，用于排序；没有允许值为 0。 */
  ratio: number;
}

export interface StageSummary {
  id: string;
  label: string;
  tone: Tone;
  total: number;
  healthy: number;
  /** 没有数据集观测时的说明，例如“未部署”“服务正常，暂无数据集观测”。 */
  note: string;
  rows: DatasetRow[];
}

function datasetRow(item: HealthDataset, names: NameBook): DatasetRow {
  const id = item.dataset_id || "";
  const cls = classify(item.status);
  const lag = toSeconds(item.lag_seconds);
  const limit = toSeconds(item.lag_limit_seconds);
  const ratio = lag !== null && limit ? lag / limit : 0;
  const unobserved = item.status === "unknown" && !lag;
  return {
    key: `${item.producer}/${item.space_id}/${id}/${item.freq}`,
    name: names.datasets[id] || names.views[id] || id,
    producer: PRODUCER_LABELS[item.producer || ""] || item.producer || "",
    spaceId: item.space_id || "",
    freq: item.freq || "",
    lag: lag === null || unobserved ? "—" : humanizeDuration(lag),
    limit: limit ? humanizeDuration(limit) : "—",
    lagTone: unobserved || !limit ? "idle" : ratio > 1 ? "bad" : ratio > 0.8 ? "warn" : "ok",
    tone: cls.tone,
    status: statusLabel(item.status),
    reason: item.reason || "",
    attention: cls.attention,
    ratio
  };
}

/** 数据链路：采集 → 存储与视图 → 因子 → 交易；每个数据集的水位落后与允许落后，需要关注、落后多的在前。 */
export function pipelineStages(overview: HealthOverview | undefined, names: NameBook): StageSummary[] {
  return (overview?.pipeline || []).map(stage => {
    const rows = (stage.datasets || [])
      .map(item => datasetRow(item, names))
      .sort((a, b) => Number(b.attention) - Number(a.attention) || TONE_RANK[a.tone] - TONE_RANK[b.tone] || b.ratio - a.ratio);
    const id = stage.id || "";
    let note = "";
    if (!rows.length) note = stage.status === "disabled" ? "未部署" : `服务${statusLabel(stage.status)}，暂无数据集观测`;
    return {
      id,
      label: STAGE_LABELS[id] || stage.name || id,
      tone: rows.length ? worst(rows.map(row => row.tone)) : classify(stage.status).tone,
      total: rows.length,
      healthy: rows.filter(row => row.tone === "ok").length,
      note,
      rows
    };
  });
}

// ---------- 业务检查 ----------

const KIND_LABELS: Record<string, string> = {
  kline_freshness: "K 线新鲜度",
  market_canary: "行情探针",
  market_fetch: "采集计划对账",
  data_delivery: "数据投递",
  module: "模块运行",
  balance: "账户余额同步",
  "console-page": "控制台页面",
  subject_tag: "标的标签巡检"
};

export interface BusinessItem {
  key: string;
  title: string;
  tone: Tone;
  attention: boolean;
  inactive: boolean;
  status: string;
  reason: string;
  rawError: string;
  checkedAt: string;
  spaceId: string;
}

export interface BusinessKind {
  kind: string;
  label: string;
  tone: Tone;
  total: number;
  healthy: number;
  /** 已停用或移除的检查，不计入正常比例。 */
  inactive: number;
  items: BusinessItem[];
}

function businessTitle(item: HealthBusinessCheck, names: NameBook): string {
  if (item.name) return prettifyIds(item.name, names);
  const label = KIND_LABELS[item.kind || ""] || item.kind || "业务检查";
  return item.module ? `${label} · ${item.module}` : label;
}

/** 业务检查按类型归类；类型内异常的排前面。 */
export function businessKinds(overview: HealthOverview | undefined, names: NameBook, nowMs: number): BusinessKind[] {
  const kinds = new Map<string, BusinessItem[]>();
  for (const item of overview?.business_checks || []) {
    const cls = classify(item.status);
    const list = kinds.get(item.kind || "") || [];
    list.push({
      key: `${item.space_id}:${item.check_id}`,
      title: businessTitle(item, names),
      tone: cls.tone,
      attention: cls.attention,
      inactive: cls.inactive,
      status: statusLabel(item.status),
      reason: prettifyIds(item.reason || "", names),
      rawError: item.raw_error || "",
      checkedAt: relativeTime(item.checked_at, nowMs),
      spaceId: item.space_id || ""
    });
    kinds.set(item.kind || "", list);
  }
  return [...kinds.entries()].map(([kind, items]) => ({
    kind,
    label: KIND_LABELS[kind] || kind || "其他",
    tone: items.some(item => item.attention)
      ? worst(items.filter(item => item.attention).map(item => item.tone))
      : items.some(item => item.tone === "ok")
        ? "ok"
        : "idle",
    total: items.length,
    healthy: items.filter(item => item.tone === "ok").length,
    inactive: items.filter(item => item.inactive).length,
    items: items.sort((a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone])
  }));
}

// ---------- 主机 ----------

export interface Meter {
  label: string;
  /** 百分比；不可用为 null。 */
  value: number | null;
  /** 告警阈值；没有规则为 null。 */
  threshold: number | null;
  tone: Tone;
}

export interface HostCard {
  key: string;
  title: string;
  subtitle: string;
  tone: Tone;
  status: string;
  reason: string;
  meters: Meter[];
  gatewayIssues: Fact[];
  lastReported: string;
  href: string;
}

const GATEWAY_LABELS: Record<string, string> = { heartbeat: "网关心跳", instance_conflict: "网关实例", route_sync: "路由同步" };

function meter(label: string, available: boolean | undefined, value: number | undefined, threshold: number | undefined): Meter {
  const limit = threshold && threshold > 0 ? threshold : null;
  if (!available || value === undefined) return { label, value: null, threshold: limit, tone: "idle" };
  const tone: Tone = limit === null ? "ok" : value >= limit ? "bad" : value >= limit * 0.9 ? "warn" : "ok";
  return { label, value, threshold: limit, tone };
}

export function hostCards(overview: HealthOverview | undefined, nowMs: number): HostCard[] {
  return (overview?.hosts || []).map((host: HealthHost) => ({
    key: `${host.host_id}:${host.agent_id}`,
    title: host.host_id || host.agent_id || host.hostname || "",
    subtitle: [host.host_id ? "" : "未关联部署主机", host.address || host.hostname].filter(Boolean).join(" · "),
    tone: classify(host.status).tone,
    status: statusLabel(host.status),
    reason: host.reason || "",
    meters: [
      meter("CPU", host.cpu_available, host.cpu_percent, host.cpu_threshold),
      meter("内存", host.memory_available, host.memory_percent, host.memory_threshold),
      meter("磁盘", host.disk_available, host.disk_percent, host.disk_threshold)
    ],
    gatewayIssues: (host.gateway_signals || [])
      .filter(signal => signal.signal?.status && signal.signal.status !== "healthy")
      .map(signal => ({
        label: GATEWAY_LABELS[signal.kind || ""] || signal.kind || "网关",
        value: signal.signal?.reason || statusLabel(signal.signal?.status)
      })),
    lastReported: relativeTime(host.last_reported_at, nowMs),
    href: hostHref(host.agent_id, host.host_id)
  }));
}
