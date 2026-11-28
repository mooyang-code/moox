import type { KlineBackfillJob } from "@/api/collector/runtime-status";

const jobStatuses: Record<string, { text: string; color: string }> = {
  queued: { text: "等待中", color: "gray" },
  running: { text: "补采中", color: "arcoblue" },
  succeeded: { text: "已完成", color: "green" },
  failed: { text: "有失败", color: "red" },
  cancelled: { text: "已取消", color: "gray" }
};

export function jobStatusInfo(status?: string) {
  return jobStatuses[status || ""] || { text: status || "未知", color: "gray" };
}

const jobKinds: Record<string, string> = { gap: "按缺口", force: "整段重拉" };

export function jobKindText(kind?: string) {
  return jobKinds[kind || ""] || kind || "-";
}

const jobSources: Record<string, { text: string; color: string }> = {
  manual: { text: "手动", color: "gray" },
  auto: { text: "自动", color: "arcoblue" },
  seed: { text: "历史补齐", color: "purple" },
  close_refetch: { text: "收市重取", color: "orange" },
  catchup: { text: "恢复追赶", color: "cyan" }
};

export function jobSourceInfo(source?: string) {
  return jobSources[source || ""] || { text: source || "手动", color: "gray" };
}

export function jobSourceText(source?: string) {
  return jobSourceInfo(source).text;
}

/** 补采作业的筛选分段：需关注（未结束或有失败）、全部，以及五种正交作业来源。 */
export const JOB_SEGMENTS = [
  { key: "attention", label: "需关注" },
  { key: "all", label: "全部" },
  { key: "manual", label: "手动" },
  { key: "auto", label: "自动" },
  { key: "seed", label: "历史补齐" },
  { key: "close_refetch", label: "收市重取" },
  { key: "catchup", label: "恢复追赶" }
] as const;

export type JobSegment = (typeof JOB_SEGMENTS)[number]["key"];

export function normalizeSegment(value: unknown): JobSegment {
  return JOB_SEGMENTS.some(item => item.key === value) ? (value as JobSegment) : "attention";
}

/** 作业还在进行：没有结束。 */
export function jobActive(job: KlineBackfillJob) {
  return job.status === "queued" || job.status === "running";
}

/** 作业可以重试：已结束且有失败分块。 */
export function jobRetryable(job: KlineBackfillJob) {
  return job.status === "failed" && (job.failed || 0) > 0;
}

/** 分段进度：成功、失败、执行中各占的百分比（保留一位小数）与已结束的分块数。 */
export function jobSegments(job: KlineBackfillJob) {
  const succeeded = job.succeeded || 0;
  const failed = job.failed || 0;
  const running = job.running || 0;
  const rest = (job.queued || 0) + (job.cancelled || 0);
  const total = succeeded + failed + running + rest;
  const percent = (value: number) => (total === 0 ? 0 : Math.round((value / total) * 1000) / 10);
  return {
    total,
    done: succeeded + failed + (job.cancelled || 0),
    succeeded: percent(succeeded),
    failed: percent(failed),
    running: percent(running)
  };
}

/** 作业的数据集展示：优先用展示名，其次数据集 ID；不限数据集时为“全部数据集”。 */
export function jobDatasetText(job: KlineBackfillJob) {
  if (!job.dataset_id) return "全部数据集";
  return job.dataset_name || job.dataset_id;
}

/** 快捷范围：最近 hours 小时，结束时间取整到分钟；返回 [起, 止) 两个毫秒时刻，和显示时区无关。 */
export function quickRange(hours: number, now = Date.now()): [number, number] {
  const end = Math.floor(now / 60000) * 60000;
  return [end - hours * 3600 * 1000, end];
}

/**
 * 补采范围大时（没选数据集，或者勾了整段重拉）先给出范围摘要再确认：数据集数、标的、时长。
 * 返回 null 表示不需要额外确认。
 */
export function backfillScopeSummary(input: {
  datasetLabel: string;
  datasetCount: number;
  subjectId: string;
  from: string;
  to: string;
  force: boolean;
}): string | null {
  if (input.datasetLabel && !input.force) return null;
  const hours = Math.max(0, (Date.parse(input.to) - Date.parse(input.from)) / 3_600_000);
  const span = hours >= 48 ? `${Math.round(hours / 24)} 天` : `${Math.round(hours)} 小时`;
  const datasets = input.datasetLabel ? `数据集「${input.datasetLabel}」` : `全部 ${input.datasetCount} 个数据集`;
  const subjects = input.subjectId ? `标的 ${input.subjectId}` : "全部标的";
  const mode = input.force ? "范围内应有的 K 线全部重拉（不检查缺口）" : "只补缺口";
  return `补采范围：${datasets}、${subjects}，共 ${span}；${mode}。`;
}
