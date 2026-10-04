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
  const match = String(raw || "").trim().match(/^(\d+)(m|h|d)$/i);
  if (!match) return 0;
  const count = Number(match[1]);
  if (!Number.isSafeInteger(count) || count <= 0) return 0;
  const unit = match[2].toLowerCase();
  const multiplier = unit === "m" ? 1 : unit === "h" ? 60 : 24 * 60;
  const minutes = count * multiplier;
  return Number.isSafeInteger(minutes) && minutes > 0 ? minutes : 0;
}

export function parseKeepDurationMinutes(raw: string): number {
  const text = String(raw || "").trim();
  if (!text || text === "0") return 0;
  const match = text.match(/^(\d+(?:\.\d+)?)(s|m|h|d)$/i);
  if (!match) return 0;
  const count = Number(match[1]);
  if (!Number.isFinite(count) || count <= 0) return 0;
  const multiplier = { s: 1 / 60, m: 1, h: 60, d: 24 * 60 }[match[2].toLowerCase() as "s" | "m" | "h" | "d"];
  const minutes = count * multiplier;
  return Number.isFinite(minutes) && minutes > 0 ? minutes : 0;
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

export function countBackfillBuckets(startRaw: string, endRaw: string, frequency: string): number {
  const start = new Date(startRaw);
  const end = new Date(endRaw);
  const minutes = parseFixedFrequencyMinutes(frequency);
  if (!minutes || Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || end <= start) return 0;
  const span = end.getTime() - start.getTime();
  const size = minutes * 60 * 1000;
  if (span % size !== 0) return 0;
  const alignedStart = floorToEpochBucket(start, frequency);
  const alignedEnd = floorToEpochBucket(end, frequency);
  if (!alignedStart || !alignedEnd || alignedStart.getTime() !== start.getTime() || alignedEnd.getTime() !== end.getTime()) return 0;
  const count = span / size;
  return Number.isSafeInteger(count) && count > 0 ? count : 0;
}

export function formatUtcInput(date: Date | null): string {
  return date && !Number.isNaN(date.getTime()) ? date.toISOString().replace(".000Z", "Z") : "";
}

export function summarizeBackfillResults(instances: Array<Record<string, any>>): ResampleBackfillSummary | null {
  let summary: ResampleBackfillSummary | null = null;
  for (const instance of instances) {
    const raw = instance.result ?? instance.Result;
    let result: Record<string, any>;
    try {
      result = typeof raw === "string" ? JSON.parse(raw || "{}") : raw || {};
    } catch {
      continue;
    }
    const backfill = result.backfill;
    if (!backfill || !backfill.request_id) continue;
    const state = String(backfill.state || "");
    // Completed, canceled, and failed requests remain in TaskInstance history;
    // only an active request should disable the dialog's start action.
    if (state !== "running" && state !== "waiting_source" && state !== "syncing") continue;
    if (!summary) {
      summary = {
        requestId: String(backfill.request_id),
        start: String(backfill.start || ""),
        end: String(backfill.end || ""),
        nextBucket: String(backfill.next_bucket || ""),
        state: state as ResampleBackfillState,
        participants: 0
      };
    }
    if (summary.requestId !== String(backfill.request_id)) return null;
    summary.participants += 1;
    const nextBucket = String(backfill.next_bucket || "");
    // The aggregate cursor is the slowest participant, so show the earliest
    // next bucket rather than allowing a fast subject to hide lagging work.
    if (nextBucket && (!summary.nextBucket || nextBucket < summary.nextBucket)) summary.nextBucket = nextBucket;
    if (state === "syncing" || (state === "waiting_source" && summary.state === "running")) {
      summary.state = state as ResampleBackfillState;
    }
  }
  return summary;
}
