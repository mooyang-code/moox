import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  jobActive,
  jobDatasetText,
  jobKindText,
  jobRetryable,
  jobSegments,
  jobStatusInfo,
  normalizeSegment,
  quickRange,
  toRFC3339,
  utcText
} from "./backfill-model";

describe("collector backfill helpers", () => {
  it("labels job states, kinds and datasets", () => {
    expect(jobStatusInfo("failed").color).toBe("red");
    expect(jobStatusInfo(undefined).text).toBe("未知");
    expect(jobKindText("history")).toBe("确认历史缺口");
    expect(jobDatasetText({ dataset_id: "ds", dataset_name: "Binance 现货 K 线 1m" })).toBe("Binance 现货 K 线 1m");
    expect(jobDatasetText({ dataset_id: "ds" })).toBe("ds");
    expect(jobDatasetText({})).toBe("全部数据集");
    expect(normalizeSegment("auto")).toBe("auto");
    expect(normalizeSegment("x")).toBe("attention");
  });

  it("computes job segments and actions", () => {
    const job = { status: "failed", succeeded: 6, failed: 2, running: 1, pending: 1, canceled: 0 };
    expect(jobSegments(job)).toEqual({ total: 10, done: 8, succeeded: 60, failed: 20, running: 10 });
    expect(jobSegments({})).toEqual({ total: 0, done: 0, succeeded: 0, failed: 0, running: 0 });
    expect(jobRetryable(job)).toBe(true);
    expect(jobRetryable({ status: "failed", failed: 0 })).toBe(false);
    expect(jobActive({ status: "running" })).toBe(true);
    expect(jobActive(job)).toBe(false);
  });

  it("formats UTC times and parses time input as UTC", () => {
    const now = new Date("2026-10-15T12:00:00Z");
    expect(utcText("2026-10-15T08:30:00Z", now)).toBe("10-15 08:30");
    expect(utcText("2025-01-02T03:04:00Z", now)).toBe("2025-01-02 03:04");
    expect(quickRange(1, new Date("2026-10-15T12:34:56Z"))).toEqual(["2026-10-15 11:34", "2026-10-15 12:34"]);
    expect(toRFC3339("2026-10-12 08:00")).toBe("2026-10-12T08:00:00Z");
    expect(toRFC3339("")).toBeNull();
    expect(toRFC3339("not-a-time")).toBeNull();
  });
});

describe("collector backfill page", () => {
  const page = readFileSync(resolve(__dirname, "index.vue"), "utf8");
  const dialog = readFileSync(resolve(__dirname, "kline-range-dialog.vue"), "utf8");

  it("is a standalone tab like factor recalc: toolbar, dataset filter, segments and a job table", () => {
    expect(page).toContain('class="moox-page backfill-page"');
    expect(page).toContain("共 {{ total }} 条补采作业");
    expect(page).toContain('placeholder="全部数据集"');
    expect(page).toContain('v-for="item in JOB_SEGMENTS"');
    expect(page).toContain("listKlineBackfills(space, filterOf(), page.value, pageSize)");
    expect(page).toContain('fixed="right"');
  });

  it("keeps filters and the opened job in the URL and polls while jobs run", () => {
    expect(page).toContain('tab: "backfills"');
    expect(page).toContain("route.query.job");
    expect(page).toContain("usePolling(");
  });

  it("retries failed chunks and shows dataset display names", () => {
    expect(page).toContain("retryKlineBackfill(");
    expect(page).toContain("重试失败分块");
    expect(page).toContain("jobDatasetText(record)");
  });

  it("uses a UTC range picker and can backfill the checked gaps", () => {
    expect(dialog).toContain("a-range-picker");
    expect(dialog).toContain("时间范围（UTC");
    expect(dialog).toContain("补采这些缺口");
    expect(dialog).toContain('emit("submitted", created.job_id)');
  });
});
