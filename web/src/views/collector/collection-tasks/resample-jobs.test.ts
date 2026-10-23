import { describe, expect, it } from "vitest";
import {
  defaultResampleRange,
  isActiveResampleJob,
  newResampleRequestId,
  parseSubjectInput,
  parseUtcInput,
  resampleJobOriginLabel,
  resampleJobProgress,
  resampleJobStatusLabel,
  sourceCandidateReason,
  validateResampleRange
} from "./resample-jobs";

describe("resample jobs", () => {
  it("labels statuses and origins", () => {
    expect(resampleJobStatusLabel("running")).toBe("运行中");
    expect(resampleJobStatusLabel("cancelled")).toBe("已取消");
    expect(resampleJobStatusLabel("")).toBe("-");
    expect(resampleJobOriginLabel("source_backfill")).toBe("补采触发");
    expect(resampleJobOriginLabel("manual")).toBe("手动");
    expect(isActiveResampleJob("syncing")).toBe(true);
    expect(isActiveResampleJob("succeeded")).toBe(false);
  });

  it("computes progress from done and total buckets", () => {
    expect(resampleJobProgress({ status: "queued", total_buckets: 0, done_buckets: 0 })).toBe(0);
    expect(resampleJobProgress({ status: "running", total_buckets: 96, done_buckets: 48 })).toBe(50);
    expect(resampleJobProgress({ status: "running", total_buckets: 3, done_buckets: 1 })).toBe(33);
    expect(resampleJobProgress({ status: "running", total_buckets: 2, done_buckets: 5 })).toBe(100);
    expect(resampleJobProgress({ status: "succeeded", total_buckets: 96, done_buckets: 10 })).toBe(100);
  });

  it("greys out source tasks whose frequency cannot build the target", () => {
    expect(sourceCandidateReason("5m", "45m")).toBe("");
    expect(sourceCandidateReason("30m", "45m")).toContain("不是源周期 30m 的整数倍");
    expect(sourceCandidateReason("1h", "1h")).toContain("必须长于");
    expect(sourceCandidateReason("1h", "15m")).toContain("必须长于");
    expect(sourceCandidateReason("1w", "1d")).toContain("不是 1m～1d 的固定周期");
    expect(sourceCandidateReason("5m", "")).toBe("");
    expect(sourceCandidateReason("5m", "7m")).toBe("");
  });

  it("parses naive date inputs as UTC", () => {
    expect(parseUtcInput("2026-10-20").toISOString()).toBe("2026-10-20T00:00:00.000Z");
    expect(parseUtcInput("2026-10-20 08:30").toISOString()).toBe("2026-10-20T08:30:00.000Z");
    expect(parseUtcInput("2026-10-20T08:30:00+08:00").toISOString()).toBe("2026-10-20T00:30:00.000Z");
    expect(Number.isNaN(parseUtcInput("not a time").getTime())).toBe(true);
  });

  it("validates the submit range", () => {
    expect(validateResampleRange("2026-10-20", "2026-10-21")).toBe("");
    expect(validateResampleRange("2026-10-21", "2026-10-20")).toContain("晚于");
    expect(validateResampleRange("x", "2026-10-20")).toContain("起始时间");
    expect(validateResampleRange("2026-10-20", "y")).toContain("结束时间");
  });

  it("uses the last three UTC days as the default range", () => {
    expect(defaultResampleRange(new Date("2026-10-22T10:20:00Z"))).toEqual({
      start: "2026-10-19T00:00:00Z",
      end: "2026-10-22T00:00:00Z"
    });
  });

  it("parses subject lists and generates distinct request ids", () => {
    expect(parseSubjectInput("BTC, ETH  SOL\nBTC")).toEqual(["BTC", "ETH", "SOL"]);
    expect(parseSubjectInput("")).toEqual([]);
    expect(newResampleRequestId()).toMatch(/^ui-[0-9a-f]{16}$/);
    expect(newResampleRequestId()).not.toBe(newResampleRequestId());
  });
});
