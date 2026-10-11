import type { FetchFunctionState, KlineBackfillJob } from "@/api/collector/fetch-plan";

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

/** 哈希只展示前 8 位，完整值放在提示里。 */
export function shortHash(hash?: string) {
  return hash ? hash.slice(0, 8) : "-";
}

/** 补采作业的分块进度：已结束（成功、失败、取消）占总数的百分比。 */
export function backfillProgress(job: KlineBackfillJob) {
  const total = (job.pending || 0) + (job.running || 0) + (job.succeeded || 0) + (job.failed || 0) + (job.canceled || 0);
  if (total === 0) return 0;
  return Math.round((((job.succeeded || 0) + (job.failed || 0) + (job.canceled || 0)) / total) * 100);
}

/** 作业还能取消：没有结束。 */
export function backfillCancelable(job: KlineBackfillJob) {
  return job.status === "pending" || job.status === "running";
}

export function functionRowKey(item: FetchFunctionState) {
  return item.name || "";
}

/** 解析 RFC3339 时间输入；空串或非法返回 null。 */
export function toRFC3339(value: string): string | null {
  const text = value.trim();
  if (!text) return null;
  // 没有时区的输入一律按 UTC 解析，与表单标签一致。
  let normalized = text.replace(" ", "T");
  if (!/(Z|[+-]\d{2}:?\d{2})$/.test(normalized)) normalized += normalized.length <= 16 ? ":00Z" : "Z";
  const parsed = new Date(normalized);
  return Number.isNaN(parsed.getTime()) ? null : parsed.toISOString().replace(/\.\d{3}Z$/, "Z");
}
