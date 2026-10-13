export type ResampleBackfillState = "running" | "waiting_source" | "syncing" | "complete" | "canceled" | "failed";

export interface ResampleBackfillSummary {
  requestId: string;
  start: string;
  end: string;
  nextBucket: string;
  state: ResampleBackfillState;
  participants: number;
}

export function parseFixedFrequencyMinutes(raw: string): number {
  const match = String(raw || "")
    .trim()
    .match(/^(\d+)(m|h|d)$/i);
  if (!match) return 0;
  const count = Number(match[1]);
  if (!Number.isSafeInteger(count) || count <= 0) return 0;
  const unit = match[2].toLowerCase();
  const multiplier = unit === "m" ? 1 : unit === "h" ? 60 : 24 * 60;
  const minutes = count * multiplier;
  return Number.isSafeInteger(minutes) && minutes > 0 ? minutes : 0;
}

export function floorToEpochBucket(date: Date, frequency: string): Date | null {
  const minutes = parseFixedFrequencyMinutes(frequency);
  if (!minutes || Number.isNaN(date.getTime())) return null;
  const size = minutes * 60 * 1000;
  return new Date(Math.floor(date.getTime() / size) * size);
}

export function defaultClosedEnd(frequency: string, now = new Date()): Date | null {
  return floorToEpochBucket(now, frequency);
}

// 表单标注 UTC：没带时区的日期时间按 UTC 解析，避免按浏览器本地时区偏移。
export function parseUtcInput(raw: string): Date {
  const text = String(raw || "").trim();
  const hasZone = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(text);
  return new Date(!hasZone && /\d{2}:\d{2}/.test(text) ? `${text}Z` : text);
}

export function countBackfillBuckets(startRaw: string, endRaw: string, frequency: string): number {
  const start = parseUtcInput(startRaw);
  const end = parseUtcInput(endRaw);
  const minutes = parseFixedFrequencyMinutes(frequency);
  if (!minutes || Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || end <= start) return 0;
  const span = end.getTime() - start.getTime();
  const size = minutes * 60 * 1000;
  if (span % size !== 0) return 0;
  const alignedStart = floorToEpochBucket(start, frequency);
  const alignedEnd = floorToEpochBucket(end, frequency);
  if (!alignedStart || !alignedEnd || alignedStart.getTime() !== start.getTime() || alignedEnd.getTime() !== end.getTime())
    return 0;
  const count = span / size;
  return Number.isSafeInteger(count) && count > 0 ? count : 0;
}

export function formatUtcInput(date: Date | null): string {
  return date && !Number.isNaN(date.getTime()) ? date.toISOString().replace(".000Z", "Z") : "";
}
