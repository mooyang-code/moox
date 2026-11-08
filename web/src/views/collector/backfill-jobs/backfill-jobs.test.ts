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
  quickRange
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

  it("快捷范围是取整到分钟的两个时刻，和显示时区无关", () => {
    expect(quickRange(1, Date.parse("2026-10-15T12:34:56Z"))).toEqual([
      Date.parse("2026-10-15T11:34:00Z"),
      Date.parse("2026-10-15T12:34:00Z")
    ]);
  });
});

describe("collector backfill page", () => {
  const page = readFileSync(resolve(__dirname, "index.vue"), "utf8");
  const dialog = readFileSync(resolve(__dirname, "kline-range-dialog.vue"), "utf8");

  it("is a standalone tab like factor recalc: toolbar, dataset filter, segments and a job table", () => {
    expect(page).toContain('class="moox-page backfill-page"');
    expect(page).not.toContain("条补采作业");
    expect(page).toContain("<InfoTip");
    expect(page).toContain('placeholder="全部数据集"');
    expect(page).toContain('v-for="item in JOB_SEGMENTS"');
    expect(page).toContain(
      "listKlineBackfills(space, filterOf(), page.value, pageSize, { silentError: Boolean(options.silent) })"
    );
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

  it("按空间时区选范围，并能补采检查出的缺口", () => {
    expect(dialog).toContain("<ZonedRangePicker");
    expect(dialog).toContain(':zone="displayZone"');
    expect(dialog).toContain("补采这些缺口");
    expect(dialog).toContain('emit("submitted", created.job_id)');
  });
});

describe("大范围补采的确认摘要", () => {
  it("没选数据集或整段重拉时给出摘要，只补一个数据集的缺口时不用确认", async () => {
    const { backfillScopeSummary } = await import("./backfill-model");
    const base = { datasetCount: 12, subjectId: "", from: "2026-11-01T00:00:00Z", to: "2026-11-08T00:00:00Z" };
    expect(backfillScopeSummary({ ...base, datasetLabel: "现货K线", force: false })).toBeNull();
    expect(backfillScopeSummary({ ...base, datasetLabel: "", force: false })).toBe(
      "补采范围：全部 12 个数据集、全部标的，共 7 天；只补缺口。"
    );
    expect(
      backfillScopeSummary({ ...base, datasetLabel: "现货K线", subjectId: "BTCUSDT", to: "2026-11-01T06:00:00Z", force: true })
    ).toBe("补采范围：数据集「现货K线」、标的 BTCUSDT，共 6 小时；范围内应有的 K 线全部重拉（不检查缺口）。");
  });
});
