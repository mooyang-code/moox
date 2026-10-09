import { describe, expect, it } from "vitest";

import { countBackfillBuckets, parseFixedFrequencyMinutes } from "./resample-backfill";

describe("resample backfill helpers", () => {
  it("normalizes fixed frequencies and counts complete UTC buckets", () => {
    expect(parseFixedFrequencyMinutes("4h")).toBe(240);
    expect(parseFixedFrequencyMinutes("90m")).toBe(90);
    expect(countBackfillBuckets("2026-08-29T00:00:00Z", "2026-08-29T04:00:00Z", "1h")).toBe(4);
    expect(countBackfillBuckets("2026-08-29T00:01:00Z", "2026-08-29T04:00:00Z", "1h")).toBe(0);
  });
});
