import { describe, expect, it } from "vitest";

import { countBackfillBuckets, parseFixedFrequencyMinutes, parseUtcInput } from "./resample-backfill";

describe("resample backfill helpers", () => {
  it("normalizes fixed frequencies and counts complete UTC buckets", () => {
    expect(parseFixedFrequencyMinutes("4h")).toBe(240);
    expect(parseFixedFrequencyMinutes("90m")).toBe(90);
    expect(countBackfillBuckets("2026-08-29T00:00:00Z", "2026-08-29T04:00:00Z", "1h")).toBe(4);
    expect(countBackfillBuckets("2026-08-29T00:01:00Z", "2026-08-29T04:00:00Z", "1h")).toBe(0);
  });

  it("parses datetimes without a zone as UTC", () => {
    expect(parseUtcInput("2026-10-01T00:00:00").toISOString()).toBe("2026-10-01T00:00:00.000Z");
    expect(parseUtcInput("2026-10-01T08:00:00+08:00").toISOString()).toBe("2026-10-01T00:00:00.000Z");
    expect(parseUtcInput("2026-10-01T00:00:00Z").toISOString()).toBe("2026-10-01T00:00:00.000Z");
    expect(countBackfillBuckets("2026-08-29T00:00:00", "2026-08-29T04:00:00", "1h")).toBe(4);
  });
});
