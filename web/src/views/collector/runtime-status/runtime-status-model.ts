import type {
  CollectorDatasetStatus,
  CollectorFunctionStatus,
  GetCollectorStatusResponse,
  HealthLevel,
  KlineBackfillJob
} from "@/api/collector/runtime-status";

export { activityTimeText as localTimeText } from "@/components/activity-log/activity-log";

const levels: Record<HealthLevel, { text: string; color: string; icon: string }> = {
  ok: { text: "正常", color: "green", icon: "icon-check-circle" },
  warn: { text: "需关注", color: "orangered", icon: "icon-exclamation-circle" },
  error: { text: "异常", color: "red", icon: "icon-close-circle" }
};

export function levelInfo(level?: string) {
  return levels[(level as HealthLevel) || "ok"] || levels.ok;
}

export function levelRank(level?: string) {
  return level === "error" ? 2 : level === "warn" ? 1 : 0;
}

const functionStates: Record<string, { text: string; color: string }> = {
  applied: { text: "已生效", color: "green" },
  pending: { text: "下发中", color: "orangered" },
  retiring: { text: "退役中", color: "gray" }
};

export function functionStateInfo(state?: string) {
  return functionStates[state || ""] || { text: state || "未知", color: "gray" };
}

const jobStatuses: Record<string, { text: string; color: string }> = {
  pending: { text: "等待中", color: "gray" },
  running: { text: "补采中", color: "arcoblue" },
  succeeded: { text: "已完成", color: "green" },
  failed: { text: "有失败", color: "red" },
  canceled: { text: "已取消", color: "gray" }
};

export function jobStatusInfo(status?: string) {
  return jobStatuses[status || ""] || { text: status || "未知", color: "gray" };
}

const jobKinds: Record<string, string> = { gap: "按缺口", force: "整段重拉", history: "确认历史缺口" };

export function jobKindText(kind?: string) {
  return jobKinds[kind || ""] || kind || "-";
}

export function jobSourceText(source?: string) {
  return source === "auto" ? "自动" : "手动";
}

/** 作业还能取消：没有结束。 */
export function backfillCancelable(job: KlineBackfillJob) {
  return job.status === "pending" || job.status === "running";
}

/** 作业可以重试：已结束且有失败分块。 */
export function backfillRetryable(job: KlineBackfillJob) {
  return job.status === "failed" && (job.failed || 0) > 0;
}

/** 分段进度：成功、失败、执行中、未开始与取消各占的百分比（保留一位小数）。 */
export function jobSegments(job: KlineBackfillJob) {
  const succeeded = job.succeeded || 0;
  const failed = job.failed || 0;
  const running = job.running || 0;
  const rest = (job.pending || 0) + (job.canceled || 0);
  const total = succeeded + failed + running + rest;
  const percent = (value: number) => (total === 0 ? 0 : Math.round((value / total) * 1000) / 10);
  return {
    total,
    done: succeeded + failed + (job.canceled || 0),
    succeeded: percent(succeeded),
    failed: percent(failed),
    running: percent(running)
  };
}

/** 数据集与频率的组合键，与后端的“数据集|频率”一致。 */
export function datasetKey(item: { dataset_id?: string; frequency?: string }) {
  return `${item.dataset_id || ""}|${item.frequency || ""}`;
}

export function jobScopeText(job: KlineBackfillJob) {
  if (!job.dataset_id) return job.subject_id ? `全部数据集 · ${job.subject_id}` : "全部数据集";
  const base = job.frequency ? `${job.dataset_id} · ${job.frequency}` : job.dataset_id;
  return job.subject_id ? `${base} · ${job.subject_id}` : base;
}

/** UTC 时间的短格式：同一年省略年份。 */
export function utcText(value?: string, now = new Date()) {
  if (!value) return "-";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  const iso = parsed.toISOString();
  const text = `${iso.slice(5, 10)} ${iso.slice(11, 16)}`;
  return parsed.getUTCFullYear() === now.getUTCFullYear() ? text : `${iso.slice(0, 4)}-${text}`;
}

export function jobRangeText(job: KlineBackfillJob, now = new Date()) {
  return `${utcText(job.from, now)} ~ ${utcText(job.to, now)} UTC`;
}

/** 时长写成“N 分钟”“N 小时 M 分钟”“N 天”。 */
export function durationText(seconds?: number | string) {
  const minutes = Math.round(Number(seconds || 0) / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  if (minutes < 24 * 60)
    return minutes % 60 === 0 ? `${minutes / 60} 小时` : `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分钟`;
  return `${Math.floor(minutes / (24 * 60))} 天`;
}

/** 检查滞后的颜色：与后端阈值一致（超过 15 分钟需关注，超过 1 小时异常）。 */
export function lagColor(seconds?: number | string) {
  const value = Number(seconds || 0);
  if (value > 3600) return "rgb(var(--danger-6))";
  if (value > 900) return "rgb(var(--warning-6))";
  return "var(--color-text-2)";
}

export interface StatusSummary {
  functions: number;
  pendingFunctions: number;
  retiringFunctions: number;
  datasets: number;
  subjects: number;
  gapBars: number;
  gapSubjects: number;
}

export function statusSummary(status: GetCollectorStatusResponse): StatusSummary {
  const functions = status.functions || [];
  const datasets = status.datasets || [];
  return {
    functions: functions.length,
    pendingFunctions: functions.filter(item => item.state === "pending").length,
    retiringFunctions: functions.filter(item => item.state === "retiring").length,
    datasets: datasets.length,
    subjects: datasets.reduce((sum, item) => sum + (item.subject_count || 0), 0),
    gapBars: datasets.reduce((sum, item) => sum + (item.gap_bars_24h || 0), 0),
    gapSubjects: datasets.reduce((sum, item) => sum + (item.gap_subjects_24h || 0), 0)
  };
}

export interface FunctionFilters {
  keyword: string;
  region: string;
  market: string;
  abnormalOnly: boolean;
}

/** 函数筛选：关键字匹配函数名或服务的数据集；“只看异常”只保留下发中与退役中的函数。 */
export function filterFunctions(functions: CollectorFunctionStatus[], filters: FunctionFilters) {
  const keyword = filters.keyword.trim().toLowerCase();
  return functions.filter(item => {
    if (filters.region && item.region !== filters.region) return false;
    if (filters.market && item.market !== filters.market) return false;
    if (filters.abnormalOnly && (item.state || "applied") === "applied") return false;
    if (!keyword) return true;
    return (
      (item.name || "").toLowerCase().includes(keyword) ||
      (item.datasets || []).some(dataset => dataset.toLowerCase().includes(keyword))
    );
  });
}

/** 取函数的某个字段的去重取值，作为筛选下拉选项。 */
export function functionOptions(functions: CollectorFunctionStatus[], field: "region" | "market") {
  return [...new Set(functions.map(item => item[field] || "").filter(Boolean))].sort();
}

/** 数据集展开行里的函数：服务该数据集的函数名。 */
export function datasetFunctions(functions: CollectorFunctionStatus[], dataset: CollectorDatasetStatus) {
  const key = datasetKey(dataset);
  return functions.filter(item => (item.datasets || []).includes(key)).map(item => item.name || "");
}

/** 快捷范围：最近 hours 小时，按 UTC 写成 YYYY-MM-DD HH:mm，结束时间取整到分钟。 */
export function quickRange(hours: number, now = new Date()): [string, string] {
  const end = new Date(Math.floor(now.getTime() / 60000) * 60000);
  const start = new Date(end.getTime() - hours * 3600 * 1000);
  const text = (value: Date) => value.toISOString().slice(0, 16).replace("T", " ");
  return [text(start), text(end)];
}

/** 解析 RFC3339 时间输入；空串或非法返回 null。没有时区的输入按 UTC 解析。 */
export function toRFC3339(value: string): string | null {
  const text = value.trim();
  if (!text) return null;
  // 先校验形状：Date 的宽松解析会把 "not-a-timeZ" 之类的文本当成 2000 年的某个时刻。
  if (!/^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}/.test(text)) return null;
  let normalized = text.replace(" ", "T");
  if (!/(Z|[+-]\d{2}:?\d{2})$/.test(normalized)) normalized += normalized.length <= 16 ? ":00Z" : "Z";
  const parsed = new Date(normalized);
  return Number.isNaN(parsed.getTime()) ? null : parsed.toISOString().replace(/\.\d{3}Z$/, "Z");
}
