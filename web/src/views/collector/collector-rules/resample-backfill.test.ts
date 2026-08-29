import { describe, expect, it } from "vitest";
import {
  countBackfillBuckets,
  defaultClosedEnd,
  formatUtcInput,
  parseFixedFrequencyMinutes,
  parseKeepDurationMinutes,
  summarizeBackfillResults
} from "./resample-backfill";

describe("resample backfill helpers", () => {
  it("normalizes fixed frequencies and counts complete UTC buckets", () => {
    expect(parseFixedFrequencyMinutes("4h")).toBe(240);
    expect(parseFixedFrequencyMinutes("90m")).toBe(90);
    expect(countBackfillBuckets("2026-08-29T00:00:00Z", "2026-08-29T04:00:00Z", "1h")).toBe(4);
    expect(countBackfillBuckets("2026-08-29T00:01:00Z", "2026-08-29T04:00:00Z", "1h")).toBe(0);
  });

  it("understands source retention and closed epoch ends", () => {
    expect(parseKeepDurationMinutes("4320h")).toBe(259200);
    expect(parseKeepDurationMinutes("30d")).toBe(43200);
    expect(formatUtcInput(defaultClosedEnd("5m", new Date("2026-08-29T09:07:12Z")))).toBe("2026-08-29T09:05:00Z");
  });

  it("aggregates one active backfill from TaskInstance result JSON", () => {
    expect(
      summarizeBackfillResults([
        { result: JSON.stringify({ backfill: { request_id: "r1", start: "a", end: "b", next_bucket: "2026-08-29T01:00:00Z", state: "syncing" } }) },
        { result: JSON.stringify({ backfill: { request_id: "r1", next_bucket: "2026-08-29T02:00:00Z", state: "syncing" } }) },
        { result: JSON.stringify({ backfill: { request_id: "old", next_bucket: "2026-08-29T00:00:00Z", state: "complete" } }) }
      ])
    ).toMatchObject({ requestId: "r1", participants: 2, nextBucket: "2026-08-29T01:00:00Z" });
  });
});
