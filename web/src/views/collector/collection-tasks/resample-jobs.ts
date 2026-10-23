import type { ResampleJob } from "@/api/collector";
import { frequencyMinutes } from "@/utils/frequency";

export const RESAMPLE_JOB_STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  syncing: "同步中",
  succeeded: "已完成",
  failed: "失败",
  cancelled: "已取消"
};

export function resampleJobStatusLabel(status: string): string {
  return RESAMPLE_JOB_STATUS_LABELS[status] || status || "-";
}

export function resampleJobStatusColor(status: string): string {
  if (status === "succeeded") return "green";
  if (status === "failed") return "red";
  if (status === "cancelled") return "gray";
  return "arcoblue";
}

/** 作业还会继续推进（排队、运行、同步），可以取消、需要轮询。 */
export function isActiveResampleJob(status: string): boolean {
  return status === "queued" || status === "running" || status === "syncing";
}

export function resampleJobOriginLabel(origin: string): string {
  return origin === "source_backfill" ? "补采触发" : "手动";
}

/** 进度百分比：已完成桶数 / 总桶数；总数未知（排队中）时为 0，已完成固定 100。 */
export function resampleJobProgress(job: Pick<ResampleJob, "status" | "total_buckets" | "done_buckets">): number {
  if (job.status === "succeeded") return 100;
  const total = Number(job.total_buckets || 0);
  if (total <= 0) return 0;
  return Math.min(100, Math.floor((Number(job.done_buckets || 0) / total) * 100));
}

/**
 * 候选源任务是否能合成目标频率：目标必须比源周期长且是它的整数倍。
 * 目标频率还没填或源周期不是固定时长时不置灰（交给后端的预览校验）；返回置灰原因，可用时返回空串。
 */
export function sourceCandidateReason(sourceFrequency: string, targetFrequency: string): string {
  const source = frequencyMinutes(sourceFrequency);
  const target = frequencyMinutes(targetFrequency);
  if (!source) return sourceFrequency ? `源周期 ${sourceFrequency} 不是 1m～1d 的固定周期` : "";
  if (!target) return "";
  if (target <= source) return `目标频率 ${targetFrequency} 必须长于源周期 ${sourceFrequency}`;
  if (target % source !== 0) return `目标频率 ${targetFrequency} 不是源周期 ${sourceFrequency} 的整数倍`;
  return "";
}

/** 提交作业的 request_id：每次提交生成新的；重复点击同一个表单状态不会重复建作业。 */
export function newResampleRequestId(random: () => number = Math.random): string {
  let text = "";
  for (let i = 0; i < 16; i += 1) text += Math.floor(random() * 16).toString(16);
  return `ui-${text}`;
}

/** 表单标注 UTC：没带时区的日期时间按 UTC 解析，避免按浏览器本地时区偏移。 */
export function parseUtcInput(raw: string): Date {
  const text = String(raw || "").trim();
  const hasZone = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(text);
  return new Date(
    !hasZone && /\d{2}:\d{2}/.test(text) ? `${text.replace(" ", "T")}Z` : hasZone || !text ? text : `${text}T00:00:00Z`
  );
}

export function formatUtcInput(date: Date | null): string {
  return date && !Number.isNaN(date.getTime()) ? date.toISOString().replace(".000Z", "Z") : "";
}

/** 默认范围：最近 3 天（UTC 零点对齐），结束时间是今天零点。 */
export function defaultResampleRange(now = new Date()): { start: string; end: string } {
  const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
  return { start: formatUtcInput(new Date(end.getTime() - 3 * 24 * 3600 * 1000)), end: formatUtcInput(end) };
}

/** 校验提交范围：返回错误文案，通过时返回空串。 */
export function validateResampleRange(startRaw: string, endRaw: string): string {
  const start = parseUtcInput(startRaw);
  const end = parseUtcInput(endRaw);
  if (Number.isNaN(start.getTime())) return "起始时间格式不正确（UTC，例如 2026-10-20 或 2026-10-20 08:00）";
  if (Number.isNaN(end.getTime())) return "结束时间格式不正确（UTC，例如 2026-10-21 或 2026-10-21 08:00）";
  if (end <= start) return "结束时间必须晚于起始时间";
  return "";
}

/** 标的输入（逗号、空格或换行分隔）转标的列表；空表示全部标的。 */
export function parseSubjectInput(raw: string): string[] {
  return [
    ...new Set(
      String(raw || "")
        .split(/[\s,，;；]+/)
        .map(item => item.trim())
        .filter(Boolean)
    )
  ];
}
