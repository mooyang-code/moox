import type { FactorSetStatus, MemberStatus, PeriodFactorStatus, RecalcJob, RecalcJobStatus } from "@/api/factor/types";

export interface StatusTag {
  label: string;
  color: string;
}

const setStatusTags: Record<FactorSetStatus, StatusTag> = {
  pending: { label: "创建中", color: "blue" },
  enabled: { label: "运行中", color: "green" },
  disabled: { label: "已停用", color: "orange" },
  deleting: { label: "清理中", color: "gray" }
};

const memberStatusTags: Record<MemberStatus, StatusTag> = {
  enabled: { label: "已启用", color: "green" },
  disabled: { label: "已停用", color: "orange" }
};

const periodStatusTags: Record<PeriodFactorStatus, StatusTag> = {
  complete: { label: "正常", color: "green" },
  degraded: { label: "降级", color: "orange" },
  skipped: { label: "已跳过", color: "gray" }
};

const jobStatusTags: Record<RecalcJobStatus, StatusTag> = {
  accepted: { label: "已受理", color: "orange" },
  running: { label: "执行中", color: "blue" },
  succeeded: { label: "已完成", color: "green" },
  failed: { label: "失败", color: "red" },
  cancelled: { label: "已取消", color: "gray" }
};

const unknownTag = (value: string): StatusTag => ({ label: value || "-", color: "gray" });

export const setStatusTag = (status: string) => setStatusTags[status as FactorSetStatus] ?? unknownTag(status);
export const memberStatusTag = (status: string) => memberStatusTags[status as MemberStatus] ?? unknownTag(status);
export const periodStatusTag = (status: string) => periodStatusTags[status as PeriodFactorStatus] ?? unknownTag(status);
export const jobStatusTag = (status: string) => jobStatusTags[status as RecalcJobStatus] ?? unknownTag(status);

export const factorTypeLabel = (type: string) =>
  (({ timeseries: "时序因子", cross_section: "截面因子" }) as Record<string, string>)[type] || type;

export const isActiveJob = (job: Pick<RecalcJob, "status">) => job.status === "accepted" || job.status === "running";

const DEGRADED_PREFIX = "degraded:";
const ENABLE_REQUEST_PREFIX = "factor-enable-";

/** 后端把分块降级说明写在成功任务的 error 字段，以 `degraded:` 为前缀；它不是错误。 */
export function splitJobNote(job: Pick<RecalcJob, "status" | "error">) {
  const text = (job.error || "").trim();
  if (!text) return { degraded: "", error: "" };
  if (text.startsWith(DEGRADED_PREFIX)) return { degraded: text.slice(DEGRADED_PREFIX.length).trim() || "部分降级", error: "" };
  return { degraded: "", error: text };
}

export const jobSourceLabel = (job: Pick<RecalcJob, "request_id">) =>
  job.request_id.startsWith(ENABLE_REQUEST_PREFIX) ? "启用回填" : "手动补算";

export function jobProgress(job: Pick<RecalcJob, "status" | "start_time" | "end_time" | "progress_time">) {
  if (job.status === "succeeded") return 100;
  const start = Date.parse(job.start_time);
  const end = Date.parse(job.end_time);
  const current = Date.parse(job.progress_time);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start || !Number.isFinite(current)) return 0;
  return Math.max(0, Math.min(100, Math.floor(((current - start) / (end - start)) * 100)));
}
