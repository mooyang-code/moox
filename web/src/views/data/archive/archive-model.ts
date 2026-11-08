/**
 * 数据归档页的纯函数：文案、校验、筛选、URL 状态。规则与 modules/archive 的后端实现同口径，见 docs/模块/归档.md。
 */
import type { LocationQuery, LocationQueryRaw } from "vue-router";
import type {
  ArchiveHealth,
  ArchivePartitionStatus,
  ArchiveRemoteState,
  ArchiveRun,
  ArchiveRunKind,
  ArchiveRunStatus,
  ArchiveTask
} from "@/api/archive";
import type { Dataset } from "@/api/storage/types";
import { freqSeconds } from "@/views/factor/shared/health";
import { formatInstant, toInstantMs } from "@/utils/time-zone";

const DAY_SECONDS = 86_400;

/** uint64 经 protojson 是字符串：统一转成数字，读不出时为 0。 */
export function toNum(value: number | string | undefined | null): number {
  const n = Number(value ?? 0);
  return Number.isFinite(n) ? n : 0;
}

// ---- 文案 ----

const KIND_LABELS: Record<ArchiveRunKind, string> = {
  scheduled: "复核",
  manual: "立即运行",
  tick: "到点拉取",
  range: "补拉区间"
};
export const kindLabel = (kind?: string) => KIND_LABELS[kind as ArchiveRunKind] ?? (kind || "—");

const RUN_STATUS_LABELS: Record<ArchiveRunStatus, string> = {
  queued: "排队中",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  canceled: "已取消"
};
export const RUN_STATUSES = Object.keys(RUN_STATUS_LABELS) as ArchiveRunStatus[];
export const runStatusLabel = (status?: string) => RUN_STATUS_LABELS[status as ArchiveRunStatus] ?? (status || "—");
export const runActive = (run?: ArchiveRun) => run?.status === "queued" || run?.status === "running";

const HEALTH_LABELS: Record<ArchiveHealth, string> = { ok: "正常", warning: "需关注", error: "异常", paused: "已停用" };
export const healthLabel = (health?: string) => HEALTH_LABELS[health as ArchiveHealth] ?? (health || "—");

/** 状态文字的着色：和交易页 status-mark 一样只给文字上色。 */
export type Tone = "ok" | "warn" | "bad" | "muted";
export function healthTone(health?: string): Tone {
  if (health === "ok") return "ok";
  if (health === "warning") return "warn";
  if (health === "error") return "bad";
  return "muted";
}
export function runTone(status?: string): Tone {
  if (status === "succeeded") return "ok";
  if (status === "failed") return "bad";
  if (status === "queued" || status === "running") return "warn";
  return "muted";
}

const PARTITION_LABELS: Record<ArchivePartitionStatus, string> = { open: "暂存中", exported: "已导出", final: "已定稿" };
export const partitionStatusLabel = (status?: string) => PARTITION_LABELS[status as ArchivePartitionStatus] ?? (status || "—");

/** 批 2 之后才有的远端副本状态；off 表示没启用对象存储，不显示。 */
export function remoteStateLabel(state?: ArchiveRemoteState | string, remoteKey?: string): string {
  switch (state) {
    case "verified":
      return "已校验";
    case "pending":
      return remoteKey ? "待上传（远端只有旧版本）" : "待上传";
    case "failed":
      return "失败";
    case "none":
      return "—";
    default:
      return "";
  }
}

export function partitionUnitText(unit?: string): string {
  if (unit === "day") return "按空间本地日";
  if (unit === "month") return "按月（data_time + 12 小时归月）";
  return unit || "—";
}

/** 频率是否按天及以上（每天拉取一次，拉取即复核）。 */
export function isDailyFreq(freq?: string): boolean {
  return freqSeconds(freq || "") >= DAY_SECONDS;
}

/** “拉取”列第一行：每收盘 N 根 · 下次 12:41 UTC；1d 及以上写“每天收盘后”；停用写“已停用”。下次拉取到分钟。 */
export function pullText(task: ArchiveTask, zone: string): string {
  if (task.status === "paused") return "已停用";
  const head = isDailyFreq(task.freq) ? "每天收盘后" : `每收盘 ${task.pull_every_bars || 1} 根`;
  return task.next_pull_at ? `${head} · 下次 ${formatInstant(task.next_pull_at, zone, "minute")}` : head;
}

/** “拉取”列第二行：复核规律。 */
export function reviewText(task: ArchiveTask): string {
  return isDailyFreq(task.freq) ? "拉取即复核" : "每小时第 34 分复核";
}

/** 复核间隔：3600 → 每小时，86400 → 每天。 */
export function intervalText(seconds?: number): string {
  if (seconds === 3600) return "每小时";
  if (seconds === DAY_SECONDS) return "每天";
  return seconds ? durationText(seconds) : "—";
}

/** 时长：45 秒、2 分 13 秒、1 小时 16 分、2 天 3 小时。 */
export function durationText(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s} 秒`;
  if (s < 3600) return `${Math.floor(s / 60)} 分${s % 60 ? ` ${s % 60} 秒` : ""}`;
  if (s < DAY_SECONDS) {
    const minutes = Math.floor((s % 3600) / 60);
    return `${Math.floor(s / 3600)} 小时${minutes ? ` ${minutes} 分` : ""}`;
  }
  const hours = Math.floor((s % DAY_SECONDS) / 3600);
  return `${Math.floor(s / DAY_SECONDS)} 天${hours ? ` ${hours} 小时` : ""}`;
}

/** 一次运行的耗时：结束了用结束 − 开始，运行中用现在 − 开始；没开始为 “—”。 */
export function runDurationText(run: ArchiveRun | undefined, nowMs: number): string {
  const start = toInstantMs(run?.started_at);
  if (start === null) return "—";
  const end = toInstantMs(run?.finished_at) ?? (run?.status === "running" ? nowMs : null);
  return end === null ? "—" : durationText((end - start) / 1000);
}

// ---- 数据集 ----

/** 时序数据集：data_kind 是枚举名或数字 2。 */
function isTimeSeries(dataset: Pick<Dataset, "data_kind">): boolean {
  return dataset.data_kind === "DATA_KIND_TIME_SERIES" || dataset.data_kind === 2;
}

/**
 * 应该归档的数据集：时序、active、不是系统空间、有限的线上保留期。
 * 和 Monitor 的 ListArchivableDatasets（modules/monitor/internal/metrics/storage.go）同口径。
 */
export function archivableDataset(dataset: Dataset, spaceId: string): boolean {
  const retention = (dataset.retention || "").trim();
  return (
    isTimeSeries(dataset) &&
    (dataset.status || "").toLowerCase() === "active" &&
    spaceId !== "mooxsys" &&
    retention !== "" &&
    retention !== "forever"
  );
}

/** 应该归档、但还没有任务的数据集。 */
export function unarchivedDatasets(datasets: Dataset[], tasks: ArchiveTask[], spaceId: string): Dataset[] {
  const archived = new Set(tasks.map(task => task.dataset_id));
  return datasets.filter(dataset => archivableDataset(dataset, spaceId) && !archived.has(dataset.dataset_id));
}

/** 状态行下方的提醒：以“<任务的 dataset_id>：”开头的是任务健康，任务表已经显示，过滤掉。 */
export function systemAlerts(alerts: string[] | undefined, taskIds: string[]): string[] {
  return (alerts || []).filter(alert => !taskIds.some(id => id && alert.startsWith(`${id}：`)));
}

// ---- 校验 ----

/** 拉取间隔 N：1～1440 的整数，且“频率 × N”不超过 1 天；1d 及以上不校验（每天一次）。和后端 pullEveryBars 同口径。 */
export function pullEveryBarsError(freq: string, n: number): string {
  if (isDailyFreq(freq)) return "";
  if (!Number.isInteger(n) || n < 1 || n > 1440) return "拉取间隔要是 1～1440 的整数";
  const span = freqSeconds(freq);
  if (span <= 0) return `无法识别频率 ${freq}`;
  return span * n > DAY_SECONDS ? `频率 × 拉取间隔不能超过 1 天（${freq} 最多 ${Math.floor(DAY_SECONDS / span)} 根）` : "";
}

/** 回补起点：不能早于线上还保留的最早时刻，不能晚于现在；为空表示不指定。 */
export function backfillFromError(value: number | null, earliestMs: number | null, nowMs: number): string {
  if (value === null) return "";
  if (earliestMs !== null && value < earliestMs) return `不能早于 ${formatInstantUtc(earliestMs)}（线上还保留的最早数据）`;
  if (value > nowMs) return "不能晚于现在";
  return "";
}

/** 补拉区间：起点早于终点，不早于下界（今天 − 线上保留期 + 1 天），终点不晚于现在。 */
export function backfillRangeError(range: [number, number] | null, earliestMs: number | null, nowMs: number): string {
  if (!range) return "请选择补拉区间";
  const [start, end] = range;
  if (end <= start) return "结束时间必须晚于开始时间";
  if (earliestMs !== null && start < earliestMs) return `开始不能早于 ${formatInstantUtc(earliestMs)}（线上还保留的最早数据）`;
  if (end > nowMs) return "结束不能晚于现在";
  return "";
}

const formatInstantUtc = (ms: number) => formatInstant(ms, "UTC", "minute");

/** 编辑时是否还能改回补起点：首次复核成功后 mgr 会清空它，worker 有了水位也不再读，改了没有效果。 */
export function showBackfillField(task: Pick<ArchiveTask, "last_success_at">): boolean {
  return !task.last_success_at;
}

/**
 * 停用多少天以内不会漏数据：和健康等级异常门槛同一公式（modules/archive/internal/service/health.go 的 evaluateHealth），
 * max(保留期 − 48 小时, 保留期 / 2)，向下取整到天；永久保留或读不出保留期时为 null。
 */
export function pauseSafeDays(retention?: string): number | null {
  const match = /^(\d+)h$/.exec((retention || "").trim());
  if (!match) return null;
  const hours = Number(match[1]);
  return Math.floor(Math.max(hours - 48, hours / 2) / 24);
}

export function pauseConfirmText(retention?: string, retentionText?: string): string {
  const days = pauseSafeDays(retention);
  const base = "停用后不再拉取和复核。";
  const risk =
    days === null
      ? ""
      : `线上只保留 ${retentionText || retention}：停用超过约 ${days} 天，最早的数据会在归档前被删除、无法补齐。`;
  return `${base}${risk}重新启用后会立即复核。`;
}

// ---- 分区预览 ----

/** 解析分区预览（表头加前 20 行的 CSV，RFC 4180：引号、引号内的逗号与换行、"" 转义）。不是完整的 CSV 时返回 null。 */
export function parsePreviewCsv(text: string): { header: string[]; rows: string[][] } | null {
  if (!text.trim()) return null;
  const records: string[][] = [];
  let field = "";
  let record: string[] = [];
  let quoted = false;
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i += 1;
        } else quoted = false;
      } else field += c;
      continue;
    }
    if (c === '"' && field === "") quoted = true;
    else if (c === ",") {
      record.push(field);
      field = "";
    } else if (c === "\n" || c === "\r") {
      if (c === "\r" && text[i + 1] === "\n") i += 1;
      record.push(field);
      records.push(record);
      record = [];
      field = "";
    } else field += c;
  }
  if (quoted) return null;
  if (field !== "" || record.length) {
    record.push(field);
    records.push(record);
  }
  const [header, ...rows] = records;
  if (!header?.length) return null;
  return { header, rows: rows.filter(row => row.length === header.length) };
}

// ---- URL 状态 ----

export type DetailTab = "runs" | "partitions";

export interface ArchiveQuery {
  /** 任务详情的 dataset_id */
  task: string;
  tab: DetailTab;
  /** 运行记录的状态筛选，空表示全部 */
  status: ArchiveRunStatus | "";
  /** 是否列出成功的到点拉取 */
  ticks: boolean;
  rpage: number;
  ppage: number;
  /** 分区起止月 YYYY-MM */
  from: string;
  to: string;
  /** 预览的分区键 */
  partition: string;
  /** 打开新建抽屉并预选的数据集 */
  create: string;
}

const first = (value: LocationQuery[string]) => (Array.isArray(value) ? value[0] : value) ?? "";
const month = (value: string) => (/^\d{4}-(0[1-9]|1[0-2])$/.test(value) ? value : "");
const pageNo = (value: string) => (/^[1-9]\d*$/.test(value) ? Number(value) : 1);

export function parseArchiveQuery(query: LocationQuery): ArchiveQuery {
  const status = first(query.status);
  return {
    task: first(query.task),
    tab: first(query.tab) === "partitions" ? "partitions" : "runs",
    status: (RUN_STATUSES as string[]).includes(status) ? (status as ArchiveRunStatus) : "",
    ticks: first(query.ticks) === "1",
    rpage: pageNo(first(query.rpage)),
    ppage: pageNo(first(query.ppage)),
    from: month(first(query.from)),
    to: month(first(query.to)),
    partition: first(query.partition),
    create: first(query.create)
  };
}

/** 序列化：默认值不写进 URL；没有打开任务详情时详情相关的参数都不写。 */
export function archiveQueryFor(state: Partial<ArchiveQuery>): LocationQueryRaw {
  const out: LocationQueryRaw = {};
  if (state.create) out.create = state.create;
  if (!state.task) return out;
  out.task = state.task;
  if (state.tab === "partitions") {
    out.tab = "partitions";
    if (state.from) out.from = state.from;
    if (state.to) out.to = state.to;
    if (state.ppage && state.ppage > 1) out.ppage = String(state.ppage);
    if (state.partition) out.partition = state.partition;
  } else {
    if (state.status) out.status = state.status;
    if (state.ticks) out.ticks = "1";
    if (state.rpage && state.rpage > 1) out.rpage = String(state.rpage);
  }
  return out;
}
