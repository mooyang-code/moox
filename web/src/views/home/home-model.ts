import type { ActivityLogItem } from "@/api/admin/activity";
import type { HistorySeed } from "@/api/collector/runtime-status";
import { ACTIVITY_CATEGORY_LABELS } from "@/components/activity-log/activity-log";
import type { FactorTaskInfo } from "@/api/factor/types";
import type { HealthAlert, HealthHost, HealthOverview, LifecycleColor } from "@/api/monitor";
import type { StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import { seedEtaText } from "@/views/collector/runtime-status/runtime-status-model";
import { freqSeconds, runBarLag, taskHealth } from "@/views/factor/shared/health";
import { statusLabel } from "@/views/ops/monitor/health-display";
import { collectorTasksPath, factorTasksPath, opsHostsPath, opsMonitorPath } from "@/router/links";
import { detailLink, instancesLink, type StrategyLink } from "@/views/strategy/links";
import { percent, rankTargets, targetStateTag } from "@/views/strategy/model";
import { pendingItems, sortInstancesByPriority, stateOf } from "@/views/strategy/attention-model";
import { isTrialInstance } from "@/views/strategy/running/running-model";
import { formatInstant, marketTime, toInstantMs } from "@/utils/time-zone";

/** 首页统一的状态色：正常、需要关注、异常、无观测。 */
export type Tone = "ok" | "warn" | "bad" | "idle";

export type HomeTarget = string | StrategyLink;

/** 数据集的显示名称，用来把监控、日志里的内部 ID 换成人能读的名字。 */
export interface NameBook {
  datasets: Record<string, string>;
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

/** 时刻换成毫秒：数字与数字字符串按 Unix 毫秒（墙钟时刻的写法），文本按 RFC3339；无法解析返回 null。Unix 秒（数据坐标）由调用方先乘 1000。 */
export function toMillis(value?: string | number | null): number | null {
  return toInstantMs(value);
}

/** 相对时间旁的完整时间（悬停用）：和 relativeTime 一样，数字按 Unix 毫秒理解。 */
export function fullTime(value: string | number | null | undefined, zone = "UTC"): string {
  return formatInstant(toMillis(value), zone);
}

/** 相对时间：刚刚、N 分钟前、N 小时前、N 天前；晚于现在（时钟偏差）时写 zone 下的绝对时间。 */
export function relativeTime(value: string | number | null | undefined, nowMs: number, zone = "UTC"): string {
  return relativeMs(toMillis(value), nowMs, zone);
}

/** 同 relativeTime，入参是已解析好的 Unix 毫秒（null 表示没有）。 */
export function relativeMs(at: number | null, nowMs: number, zone = "UTC"): string {
  if (at === null) return "—";
  const diff = Math.round((nowMs - at) / 1000);
  if (diff < 0) return marketTime(at, zone, nowMs);
  if (diff < 5) return "刚刚";
  if (diff < 60) return `${diff} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

/** 把文本里的数据集内部 ID 换成名称（名称未知时保留原 ID）。 */
export function prettifyIds(text: string, names: NameBook): string {
  return text.replace(/\bdataset_[a-z0-9_]+\b/gi, id => names.datasets[id] || id);
}

const STAGE_LABELS: Record<string, string> = {
  collector: "采集",
  storage: "入库",
  storage_view: "视图",
  factor: "因子",
  strategy: "策略"
};

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
  /** 数据已写到的最近一根 K 线的收盘时刻（毫秒）；没有观测为 null。 */
  closeMs: number | null;
}

const TONE_RANK: Record<Tone, number> = { bad: 0, warn: 1, idle: 2, ok: 3 };
/** 数据新鲜度的排序：没有观测的放最后，不挡在正常项前面。 */
const FRESHNESS_RANK: Record<Tone, number> = { bad: 0, warn: 1, ok: 2, idle: 3 };

/**
 * 数据新鲜度：每个数据集在各环节是否追平，异常的排前面，其次等得久的。
 * 后端按市场交易日历算出此刻应已收线的最新一根：已经到了就是“已追平”；还没到的显示从它收线起已等待多久。
 * 不用“当前时间 − 最新 K 线时间”，休市、午休、节假日不会被当作落后。
 */
export function freshnessRows(overview: HealthOverview | undefined, names: NameBook, nowMs: number = Date.now()): FreshnessRow[] {
  const clockMs = toMillis(overview?.generated_at) ?? nowMs;
  const entries = (overview?.pipeline || []).flatMap(stage =>
    (stage.datasets || []).map(item => {
      const id = item.dataset_id || "";
      const producer = item.producer || "";
      const tone = toneOf(item.status);
      const waiting = Number(item.waiting_bars) || 0;
      const since = toMillis(item.waiting_since);
      // 状态未知时不下“已追平”的结论；有还没到的根时写已等待多久（按服务端时钟），否则已追平。
      const behind =
        tone === "idle"
          ? null
          : waiting > 0
            ? since === null
              ? toSeconds(item.overdue_seconds) || 0
              : Math.max(0, Math.floor((clockMs - since) / 1000))
            : 0;
      // 数据已写到的最近一根的收线时刻，由后端按交易日历算出。
      const closeMs = tone === "idle" ? null : toMillis(item.latest_bar_close_at);
      const row: FreshnessRow = {
        key: `${producer}/${item.space_id}/${id}/${item.frequency}`,
        datasetId: id,
        stage: STAGE_LABELS[producer] || stage.name || producer,
        name: names.datasets[id] || id,
        freq: item.frequency || "",
        lag: behind === null ? "—" : waiting > 0 ? `等待 ${humanizeDuration(behind)}` : "已追平",
        tone,
        status: statusLabel(item.status),
        reason: item.reason || "",
        path: producer === "factor" ? factorTasksPath({ tab: "results" }) : collectorTasksPath({ tab: "results" }),
        seconds: behind,
        closeMs
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
  taskId: string;
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

/** 因子计算任务：任务名（没有时用数据集名称）· 频率、启用因子数、最近周期与滞后。 */
/** zone 是当前空间的时区，最近周期按它显示。 */
export function factorRows(sets: FactorTaskInfo[], spaceId: string, names: NameBook, nowMs: number, zone: string): FactorRow[] {
  return sets
    .filter(info => info.factor_task.space_id === spaceId)
    .map(info => {
      const set = info.factor_task;
      const health = taskHealth(set, info.last_run);
      const behindSeconds = info.last_run?.last_period_time ? runBarLag(set.frequency, info.last_run).seconds : null;
      // last_period_time 是数据坐标的 Unix 秒
      const periodOpenSeconds = toSeconds(info.last_run?.last_period_time);
      const periodOpenMs = periodOpenSeconds ? periodOpenSeconds * 1000 : null;
      const periodEndMs = periodOpenMs === null ? null : periodOpenMs + freqSeconds(set.frequency) * 1000;
      return {
        taskId: set.task_id,
        label: `${set.name || names.datasets[set.source_dataset_id] || set.source_dataset_id} · ${set.frequency}`,
        factors: (info.members || []).filter(member => member.status === "enabled").length,
        health,
        tone: FACTOR_TONE[health.key] || "idle",
        lastPeriod: periodOpenMs === null ? "—" : marketTime(periodOpenMs, zone, nowMs),
        lag: behindSeconds === null ? "—" : behindText(behindSeconds),
        behindSeconds,
        periodEndMs,
        path: factorTasksPath({ tab: "results", task: set.task_id })
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
  /** lastBar 的完整时间（悬停用） */
  lastBarTitle: string;
  link: StrategyLink;
}

const HOLDING_LIMIT = 4;

/** 策略实例：当前要持有什么（按权重排前几个）、目标是否有效、最近一期是多久前。 */
export function strategyRows(
  instances: StrategyInstance[],
  targets: Record<string, StrategyTargetSnapshot>,
  nowMs: number,
  zone = "UTC"
): StrategyRow[] {
  return sortInstancesByPriority(instances).map(instance => {
    const state = stateOf(instance, targets, nowMs);
    const snapshot = targets[instance.instance_id];
    const ranked = snapshot ? rankTargets(snapshot.targets) : [];
    const gross = snapshot ? ranked.reduce((total, row) => total + Math.abs(row.weight), 0) : null;
    const net = snapshot ? ranked.reduce((total, row) => total + row.weight, 0) : 0;
    const cashRatio = snapshot ? Math.max(0, 1 - net) : 0;
    const barMax = Math.max(...ranked.map(row => Math.abs(row.weight)), cashRatio, 0);
    const tone: Tone =
      instance.health === "degraded"
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
      name: instance.strategy_name || instance.strategy_id,
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
      lastBar: relativeMs(toInstantMs(snapshot?.bar_end_time), nowMs, zone),
      lastBarTitle: toInstantMs(snapshot?.bar_end_time) === null ? "" : formatInstant(snapshot?.bar_end_time, zone),
      link: detailLink(instance.instance_id)
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
  /** when 的完整时间（悬停用） */
  whenTitle?: string;
  target: HomeTarget;
  /** 告警对象所在的空间：和当前空间不同时，点开前先切过去（例如别的空间的数据集告警）。 */
  spaceId?: string;
}

function alertTone(alert: HealthAlert): "warn" | "bad" {
  return /critical|error|fatal|p0|p1|high|严重|紧急/i.test(alert.severity || "") ? "bad" : "warn";
}

/** 待处理：监控告警、策略实例与计算任务的问题合在一起，严重的在前。 */
export function attentionItems(input: {
  alerts: HealthAlert[];
  strategy: { instances: StrategyInstance[]; targets: Record<string, StrategyTargetSnapshot> };
  factors: FactorRow[];
  freshness: FreshnessRow[];
  names: NameBook;
  nowMs: number;
  alertTarget: (alert: HealthAlert) => string;
  /** 告警所属空间的显示名；属于当前空间或不属于任何空间时返回空串，不加前缀。 */
  spaceLabel?: (spaceId: string) => string;
}): AttentionItem[] {
  const items: AttentionItem[] = input.alerts.map(alert => {
    const space = alert.object?.space_id ? input.spaceLabel?.(alert.object.space_id) || "" : "";
    const title = prettifyIds(alert.title || alert.id || "告警", input.names);
    return {
      key: `alert-${alert.id || alert.title}`,
      tone: alertTone(alert),
      source: "监控",
      title: space ? `${space} · ${title}` : title,
      detail: prettifyIds(alert.reason || "", input.names),
      when: relativeTime(alert.triggered_at, input.nowMs),
      whenTitle: fullTime(alert.triggered_at),
      target: input.alertTarget(alert),
      ...(alert.object?.space_id ? { spaceId: alert.object.space_id } : {})
    };
  });
  for (const item of pendingItems({ ...input.strategy, nowMs: input.nowMs })) {
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
      .map(alert => `${alert.object?.dataset_id}/${alert.object?.frequency}`)
  );
  for (const row of input.freshness) {
    if (row.tone !== "warn" && row.tone !== "bad") continue;
    if (alerted.has(`${row.datasetId}/${row.freq}`)) continue;
    items.push({
      key: `data-${row.key}`,
      tone: row.tone,
      source: "数据",
      title: `${row.stage} · ${row.name}${row.freq ? ` · ${row.freq}` : ""}`,
      detail: row.lag === "已追平" || row.lag === "—" ? row.reason || row.status : row.lag,
      when: "",
      target: row.path
    });
  }
  for (const row of input.factors) {
    if (row.tone !== "warn" && row.tone !== "bad") continue;
    items.push({
      key: `factor-${row.taskId}`,
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
      key: host.host_id || "",
      name: host.hostname || host.host_id || "",
      tone: status === "ok" && (hot.cpu || hot.memory || hot.disk) ? "warn" : status,
      cpu: percentText(host.cpu_percent, host.cpu_available),
      memory: percentText(host.memory_percent, host.memory_available),
      disk: percentText(host.disk_percent, host.disk_available),
      hot,
      path: opsHostsPath({ tab: "monitor", hostId: host.host_id || undefined })
    };
  });
}

export interface ActivityRow {
  key: string;
  when: string;
  /** when 的完整时间（悬停用） */
  whenTitle: string;
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

/** zone 是当前空间的时区。 */
export function activityRows(items: ActivityLogItem[], names: NameBook, nowMs: number, zone: string): ActivityRow[] {
  return items
    .filter(item => !isNoChange(item))
    .map((item, index) => ({
      key: String(item.id ?? index),
      when: marketTime(toInstantMs(item.created_at), zone, nowMs),
      whenTitle: formatInstant(item.created_at, zone),
      category: ACTIVITY_CATEGORY_LABELS[item.category || ""] || item.category || item.source || "",
      title: compactTitle(prettifyIds(item.title || "", names)),
      tone: item.level === "error" ? "bad" : item.level === "warning" ? "warn" : "idle"
    }));
}

/**
 * 顶部状态句：有待处理时写“N 项需要处理”，否则写“运行正常”；监控不可用时如实说明。
 * failed 是监控以外读取失败的项数（因子、策略、动态）：读不到的部分不能算作正常。
 */
export function statusLine(input: {
  attention: AttentionItem[];
  overview?: HealthOverview;
  monitorError: string;
  failed?: number;
}): {
  tone: Tone;
  text: string;
} {
  const failed = input.failed ?? 0;
  const suffix = failed ? ` · ${failed} 项读取失败` : "";
  const bad = input.attention.filter(item => item.tone === "bad").length;
  if (input.attention.length) return { tone: bad ? "bad" : "warn", text: `${input.attention.length} 项需要处理${suffix}` };
  if (input.monitorError) return { tone: "idle", text: `监控数据暂不可用${suffix}` };
  if (failed) return { tone: "warn", text: `${failed} 项读取失败` };
  if (!input.overview) return { tone: "idle", text: "正在读取状态" };
  return { tone: "ok", text: "运行正常" };
}

/**
 * 只保留属于某个空间的数据链路：首页的数据新鲜度按当前空间看，告警和主机看全部空间。
 * 不属于任何空间的条目照旧保留（以前按空间读概览时，后端也会一并返回它们）。
 */
export function scopeOverviewToSpace(overview: HealthOverview | undefined, spaceId: string): HealthOverview | undefined {
  if (!overview) return overview;
  return {
    ...overview,
    pipeline: (overview.pipeline || []).map(stage => ({
      ...stage,
      datasets: (stage.datasets || []).filter(item => !item.space_id || item.space_id === spaceId)
    }))
  };
}

/** 运行阶段的中文名。 */
const PHASE_LABELS: Record<string, string> = {
  unknown: "状态未知",
  running: "运行中",
  paused: "已暂停",
  partially_paused: "部分暂停",
  resuming: "恢复中"
};

export interface SystemStage {
  /** 圆点颜色：红（有模块未就绪或停止上报）、青（恢复追赶）、橙（暂停）、黄（有实例试运行）、绿（运行）、灰。 */
  color: LifecycleColor;
  /** 形如「运行中」「运行中 · 2 个实例试运行」「已暂停」。 */
  label: string;
  /** 受影响的模块与原因，最多显示 3 条，其余折叠为一条“另有 N 项”。 */
  issues: string[];
}

/**
 * 首页的系统状态：健康面与运行阶段来自 Monitor 汇总的各模块状态；“试运行”只看策略实例的确认列与启用状态
 * （挂了账户、还没确认实盘的启用实例），不读任何阶段。没有模块上报生命周期状态时返回 undefined（不显示）。
 */
export function systemStage(
  overview?: HealthOverview,
  instances: Pick<StrategyInstance, "enabled" | "logical_account_id" | "confirmed_at">[] = []
): SystemStage | undefined {
  const system = overview?.system;
  if (!system || !system.phase) return undefined;
  let label = PHASE_LABELS[system.phase] || system.phase;
  let color: LifecycleColor = system.color || "gray";
  const trial = instances.filter(isTrialInstance).length;
  if (system.health === "attention") label = `${label} · ${attentionLabel(system)}`;
  else if (system.phase === "resuming") {
    // 恢复追赶：显示「追平几个、共几个」，全部追平后提示待放行。
    const total = system.resuming_modules || 0;
    const caught = system.caught_up_modules || 0;
    label += total > 0 && caught >= total ? " · 已追平，待放行" : ` · 追赶 ${caught}/${total} 个模块`;
  } else if (system.phase === "running" && trial > 0) {
    label += ` · ${trial} 个实例试运行`;
    color = "yellow";
  }
  const all = [...(system.issues || []), ...(system.phase === "resuming" ? system.catchup_notes || [] : [])];
  const issues = all.slice(0, 3);
  if (all.length > 3) issues.push(`另有 ${all.length - 3} 项`);
  return { color, label, issues };
}

export interface SeedSummary {
  /** 补齐中的数据集个数。 */
  datasets: number;
  doneChunks: number;
  totalChunks: number;
  /** 补齐中各数据集里最长的预计剩余；都不能估计时为“估算中”。 */
  etaText: string;
}

/**
 * 一个空间的历史补齐汇总：只统计 running 的记录；没有补齐中的数据集时返回 undefined（不显示）。
 * 补齐中不算异常，不参与任何红黄绿判断。
 */
export function seedSummary(seeds: HistorySeed[] | undefined): SeedSummary | undefined {
  const running = (seeds || []).filter(item => item.status === "running");
  if (!running.length) return undefined;
  const longest = Math.max(...running.map(item => Number(item.eta_seconds || 0)));
  return {
    datasets: running.length,
    doneChunks: running.reduce((sum, item) => sum + Number(item.done_chunks || 0), 0),
    totalChunks: running.reduce((sum, item) => sum + Number(item.total_chunks || 0), 0),
    etaText: seedEtaText(longest)
  };
}

/** 首页那一行的文字：历史补齐中（不生效）· 已完成 x/y 分块 · 预计剩余。 */
export function seedSummaryText(summary: SeedSummary): string {
  const eta = summary.etaText === "估算中" ? "预计剩余估算中" : `预计剩余${summary.etaText}`;
  return `${summary.datasets} 个数据集 · 已完成 ${summary.doneChunks}/${summary.totalChunks} 分块 · ${eta}`;
}

/** 健康面需要处理时的说明：未就绪与停止上报都来自各模块的上报。 */
function attentionLabel(system: NonNullable<HealthOverview["system"]>): string {
  const notReady = system.not_ready_modules || 0;
  const silent = system.silent_modules || 0;
  if (notReady && silent) return "有模块未就绪或停止上报";
  return silent ? "有模块停止上报" : "有模块未就绪";
}

/** 磁贴里固定显示的条数，其余折叠为“另有 N 项”。 */
export const TILE_LIMITS = { attention: 5, freshness: 4, factors: 3, hosts: 3, activity: 6 } as const;

export function fold<T>(items: T[], limit: number): { shown: T[]; hidden: number } {
  return { shown: items.slice(0, limit), hidden: Math.max(0, items.length - limit) };
}

/** 落后量的写法：0 为“已追平”，否则“落后 X”。 */
function behindText(seconds: number): string {
  return seconds === 0 ? "已追平" : `落后 ${humanizeDuration(seconds)}`;
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

interface FactCandidate {
  tone: Tone;
  atMs: number;
  /** 落后的已收盘 K 线时长（秒），没有这个概念的写 0。 */
  behind: number;
}

/** 选出指标卡要展示的那一行：状态最差的优先，同样差时落后最多的优先；都正常则取时间最新的。 */
function pickFact(candidates: FactCandidate[]): FactCandidate | undefined {
  return [...candidates].sort(
    (a, b) => FRESHNESS_RANK[a.tone] - FRESHNESS_RANK[b.tone] || b.behind - a.behind || b.atMs - a.atMs
  )[0];
}

function factorFact(factors: FactorRow[]): FactCandidate | undefined {
  return pickFact(
    factors
      .filter(row => row.behindSeconds !== null && row.periodEndMs !== null)
      .map(row => ({ tone: row.tone, atMs: row.periodEndMs as number, behind: row.behindSeconds as number }))
  );
}

function dataFact(freshness: FreshnessRow[]): FactCandidate | undefined {
  return pickFact(
    freshness
      .filter(row => row.tone !== "idle" && row.closeMs !== null)
      .map(row => ({ tone: row.tone, atMs: row.closeMs as number, behind: row.seconds ?? 0 }))
  );
}

/** 指标卡里的时间：写最近一根已收盘 K 线的收盘时间，精确到秒；由人对照当前时间判断是否异常，异常的换色。 */
function closeClock(fact: FactCandidate | undefined, nowMs: number, zone: string): string {
  return fact ? marketTime(fact.atMs, zone, nowMs, { seconds: true }) : "—";
}

export interface KpiCard {
  key: string;
  label: string;
  icon: string;
  tone: Tone;
  value: string;
  unit: string;
  note: string;
  /** 鼠标悬停时说明说明文字里时间的口径。 */
  hint?: string;
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
  nowMs: number;
  /** 当前空间的时区：收盘时间按它显示 */
  zone: string;
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
  const factorTime = closeClock(factorFact(input.factors), input.nowMs, input.zone);
  const dataTime = closeClock(dataFact(input.freshness), input.nowMs, input.zone);
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
      target: instancesLink()
    },
    {
      key: "factor",
      label: "因子计算",
      icon: "icon-experiment",
      tone: !input.factorLoaded || !input.factors.length ? "idle" : factorBad ? "warn" : "ok",
      value: input.factorLoaded ? String(input.factors.length) : "—",
      unit: input.factorLoaded ? " 个任务" : "",
      note: !input.factorLoaded
        ? "读取中"
        : factorBad
          ? `${factorBad} 个异常 · 收盘 ${factorTime}`
          : `${factorCount} 个因子 · 收盘 ${factorTime}`,
      hint: "计算任务最近一个已计算周期的收盘时间；有异常的任务时是异常最重的那一个",
      target: factorTasksPath({ tab: "results" })
    },
    {
      key: "freshness",
      label: "数据新鲜度",
      icon: "icon-storage",
      tone: monitorDown || !observed.length ? "idle" : stale ? "warn" : "ok",
      value: monitorDown || !observed.length ? "—" : String(fresh),
      unit: monitorDown || !observed.length ? "" : ` / ${observed.length} 正常`,
      note: monitorDown
        ? "监控数据暂不可用"
        : !observed.length
          ? "暂无数据链路观测"
          : stale
            ? `${stale} 条落后 · 收盘 ${dataTime}`
            : `收盘 ${dataTime}`,
      hint: "数据链路已写到的最近一根 K 线的收盘时间；有落后的数据集时是落后最多的那一个",
      target: collectorTasksPath({ tab: "results" })
    },
    {
      key: "alerts",
      label: "当前告警",
      icon: "icon-notification",
      tone: monitorDown || input.alertCount === undefined ? "idle" : input.alertCount > 0 ? "bad" : "idle",
      value: monitorDown || input.alertCount === undefined ? "—" : String(input.alertCount),
      unit: "",
      note: monitorDown ? "监控数据暂不可用" : "组件、数据与主机",
      target: opsMonitorPath()
    }
  ];
}
