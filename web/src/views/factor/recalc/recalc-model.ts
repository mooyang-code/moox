import type { FactorTaskInfo, RecalcJob, RecalcJobStatus, RecalcSubjectMode } from "@/api/factor/types";
import { freqSeconds } from "@/views/factor/shared/health";
import { jobProgress, jobStatusTag, splitJobNote } from "@/views/factor/shared/status";

export type StatusSegment = "all" | "running" | "succeeded" | "failed";

export const STATUS_SEGMENTS: ReadonlyArray<{ key: StatusSegment; label: string }> = [
  { key: "all", label: "全部" },
  { key: "running", label: "进行中" },
  { key: "succeeded", label: "已完成" },
  { key: "failed", label: "失败 / 取消" }
];

/** 状态分段对应的服务端 statuses 过滤；全部时不传。 */
export function segmentStatuses(segment: StatusSegment): RecalcJobStatus[] | undefined {
  return {
    all: undefined,
    running: ["accepted", "running"] as RecalcJobStatus[],
    succeeded: ["succeeded"] as RecalcJobStatus[],
    failed: ["failed", "cancelled"] as RecalcJobStatus[]
  }[segment];
}

/** 成功任务 error 里以 `degraded:` 开头的是降级说明，展示为「部分降级」而不是错误。 */
export function jobDisplay(job: Pick<RecalcJob, "status" | "error">) {
  const note = splitJobNote(job);
  return {
    statusLabel: jobStatusTag(job.status).label,
    statusColor: jobStatusTag(job.status).color,
    degraded: Boolean(note.degraded),
    degradedText: note.degraded,
    errorText: note.error
  };
}

export const progressPercent = jobProgress;

export function enabledMemberIds(info: Pick<FactorTaskInfo, "members">): string[] {
  return (info.members || []).filter(member => member.status === "enabled").map(member => member.member_key);
}

export interface TimeRange {
  start: number;
  end: number;
}

const DEFAULT_RANGE_PERIODS = 100;
const DAY_MS = 86_400_000;

function intervalMs(freq: string) {
  return freqSeconds(freq) * 1000;
}

/** 以最近一个已完成周期边界为结束，默认回看 100 个周期。 */
export function defaultRange(freq: string, now = Date.now()): TimeRange | null {
  const interval = intervalMs(freq);
  if (interval <= 0) return null;
  const end = Math.floor(now / interval) * interval;
  return { start: end - interval * DEFAULT_RANGE_PERIODS, end };
}

export function quickRange(freq: string, kind: "periods" | "day" | "week", now = Date.now()): TimeRange | null {
  const interval = intervalMs(freq);
  if (interval <= 0) return null;
  const end = Math.floor(now / interval) * interval;
  const span = kind === "periods" ? interval * DEFAULT_RANGE_PERIODS : kind === "day" ? DAY_MS : 7 * DAY_MS;
  return { start: end - span, end };
}

/**
 * 按交易时段分桶的市场：空间 ID 即市场 ID（后端 packages/barclock），crypto 全天连续，其余（A 股等）按交易时段。
 * 前端没有交易日历，这类市场的周期数只能按自然时间估算。
 */
export function isSessionMarket(spaceId: string): boolean {
  return Boolean(spaceId) && spaceId !== "crypto";
}

export const SESSION_ESTIMATE_NOTE = "按自然时间估算，实际以交易时段为准";

export interface EstimateInput {
  freq: string;
  /** 按交易时段的市场：周期数按自然时间估算，文字里注明。 */
  sessionMarket?: boolean;
  start?: number | null;
  end?: number | null;
  factorCount: number;
  subjectMode: RecalcSubjectMode;
  subjectCount: number;
}

/** 提交前的汇总：周期数 × 因子数 × 对象范围（按频率与时间范围前端估算）。 */
export function estimateRecalc({ freq, start, end, factorCount, subjectMode, subjectCount, sessionMarket }: EstimateInput) {
  const interval = intervalMs(freq);
  const periods = interval > 0 && start != null && end != null && end > start ? Math.floor((end - start) / interval) : 0;
  const scope = subjectMode === "include" ? `${subjectCount} 个对象` : "全部对象";
  const note = sessionMarket ? `（${SESSION_ESTIMATE_NOTE}）` : "";
  return { periods, factors: factorCount, text: `将处理 ${periods} 个周期 × ${factorCount} 个因子 × ${scope}${note}` };
}
