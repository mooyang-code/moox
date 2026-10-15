import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  backfillCancelable,
  backfillRetryable,
  datasetFunctions,
  durationText,
  filterFunctions,
  functionOptions,
  functionStateInfo,
  jobKindText,
  jobRangeText,
  jobScopeText,
  jobSegments,
  jobStatusInfo,
  lagColor,
  levelInfo,
  quickRange,
  statusSummary,
  toRFC3339,
  utcText
} from "./runtime-status-model";

const functions = [
  {
    name: "moox-fetch-plan-crypto-spot-tokyo-000",
    region: "ap-tokyo",
    market: "binance/spot",
    datasets: ["ds_spot|1m"],
    state: "applied"
  },
  {
    name: "moox-fetch-plan-crypto-spot-tokyo-001",
    region: "ap-tokyo",
    market: "binance/spot",
    datasets: ["ds_spot|1m", "ds_spot|1h"],
    state: "pending"
  },
  {
    name: "moox-fetch-plan-stockcn-equity-nanjing-000",
    region: "ap-nanjing",
    market: "eastmoney/equity",
    datasets: ["ds_a|1m"],
    state: "retiring"
  }
];

describe("collector runtime status helpers", () => {
  it("labels health levels, function states and job states", () => {
    expect(levelInfo("error").text).toBe("异常");
    expect(levelInfo("warn").color).toBe("orangered");
    expect(levelInfo(undefined).text).toBe("正常");
    expect(functionStateInfo("pending").text).toBe("下发中");
    expect(functionStateInfo("custom").text).toBe("custom");
    expect(jobStatusInfo("failed").color).toBe("red");
    expect(jobKindText("history")).toBe("确认历史缺口");
  });

  it("computes job segments and actions", () => {
    const job = { status: "failed", succeeded: 6, failed: 2, running: 1, pending: 1, canceled: 0 };
    expect(jobSegments(job)).toEqual({ total: 10, done: 8, succeeded: 60, failed: 20, running: 10 });
    expect(jobSegments({})).toEqual({ total: 0, done: 0, succeeded: 0, failed: 0, running: 0 });
    expect(backfillRetryable(job)).toBe(true);
    expect(backfillRetryable({ status: "failed", failed: 0 })).toBe(false);
    expect(backfillCancelable({ status: "running" })).toBe(true);
    expect(backfillCancelable(job)).toBe(false);
  });

  it("describes job scope and UTC ranges", () => {
    const now = new Date("2026-10-15T12:00:00Z");
    expect(jobScopeText({ dataset_id: "ds", frequency: "1m" })).toBe("ds · 1m");
    expect(jobScopeText({ subject_id: "BTCUSDT" })).toBe("全部数据集 · BTCUSDT");
    expect(utcText("2026-10-15T08:30:00Z", now)).toBe("10-15 08:30");
    expect(utcText("2025-01-02T03:04:00Z", now)).toBe("2025-01-02 03:04");
    expect(jobRangeText({ from: "2026-10-15T08:00:00Z", to: "2026-10-15T09:00:00Z" }, now)).toBe("10-15 08:00 ~ 10-15 09:00 UTC");
  });

  it("formats lag with the backend thresholds", () => {
    expect(durationText(42 * 60)).toBe("42 分钟");
    expect(durationText(65 * 60)).toBe("1 小时 5 分钟");
    expect(durationText("7200")).toBe("2 小时");
    expect(durationText(50 * 3600)).toBe("2 天");
    expect(lagColor(60)).toBe("var(--color-text-2)");
    expect(lagColor(1000)).toContain("warning");
    expect(lagColor(4000)).toContain("danger");
  });

  it("summarizes status and filters functions", () => {
    const summary = statusSummary({
      functions,
      datasets: [
        { dataset_id: "ds_spot", frequency: "1m", subject_count: 120, gap_bars_24h: 21, gap_subjects_24h: 3 },
        { dataset_id: "ds_a", frequency: "1m", subject_count: 198, gap_bars_24h: 12, gap_subjects_24h: 2 }
      ]
    });
    expect(summary).toEqual({
      functions: 3,
      pendingFunctions: 1,
      retiringFunctions: 1,
      datasets: 2,
      subjects: 318,
      gapBars: 33,
      gapSubjects: 5
    });

    const base = { keyword: "", region: "", market: "", abnormalOnly: false };
    expect(filterFunctions(functions, { ...base, keyword: "DS_SPOT|1H" }).map(item => item.name)).toEqual([
      "moox-fetch-plan-crypto-spot-tokyo-001"
    ]);
    expect(filterFunctions(functions, { ...base, region: "ap-nanjing" })).toHaveLength(1);
    expect(filterFunctions(functions, { ...base, abnormalOnly: true })).toHaveLength(2);
    expect(functionOptions(functions, "region")).toEqual(["ap-nanjing", "ap-tokyo"]);
    expect(datasetFunctions(functions, { dataset_id: "ds_spot", frequency: "1m" })).toHaveLength(2);
  });

  it("builds quick UTC ranges and parses time input as UTC", () => {
    expect(quickRange(1, new Date("2026-10-15T12:34:56Z"))).toEqual(["2026-10-15 11:34", "2026-10-15 12:34"]);
    expect(toRFC3339("2026-10-12 08:00")).toBe("2026-10-12T08:00:00Z");
    expect(toRFC3339("2026-10-12T08:00:00Z")).toBe("2026-10-12T08:00:00Z");
    expect(toRFC3339("")).toBeNull();
    expect(toRFC3339("not-a-time")).toBeNull();
  });
});

describe("collector runtime status page", () => {
  const page = readFileSync(resolve(__dirname, "runtime-status.vue"), "utf8");
  const jobs = readFileSync(resolve(__dirname, "backfill-jobs.vue"), "utf8");
  const dialog = readFileSync(resolve(__dirname, "kline-range-dialog.vue"), "utf8");

  it("leads with the overall verdict, dataset health and the two panes", () => {
    expect(page).toContain("getCollectorStatus(spaceId)");
    expect(page.indexOf('class="status-card"')).toBeLessThan(page.indexOf("<h3>数据集健康</h3>"));
    expect(page).toContain('<a-tab-pane key="functions">');
    expect(page).toContain('<a-tab-pane key="backfills">');
    expect(page).toContain('category="collector.subject_change"');
    expect(page).not.toContain("hash");
  });

  it("keeps the function filters on one toolbar row", () => {
    const toolbar = page.slice(
      page.indexOf('<div class="pane-toolbar">'),
      page.indexOf("</div>", page.indexOf('<div class="pane-toolbar">'))
    );
    for (const marker of ["搜索函数名或数据集", "全部地域", "全部市场", "只看异常"]) expect(toolbar).toContain(marker);
    expect(page).toMatch(/\.pane-toolbar\s*\{[^}]*display:\s*flex;[^}]*flex-wrap:\s*wrap;/);
  });

  it("offers the dataset actions for history gaps and confirmed absences", () => {
    expect(page).toContain("confirmHistoryGaps(");
    expect(page).toContain("clearSourceAbsences(");
    expect(page).toContain("补采确认");
    expect(page).toContain("清除并重新检查");
  });

  it("filters backfill jobs and retries failed chunks from the detail drawer", () => {
    expect(jobs).toContain('<a-radio value="attention">需关注</a-radio>');
    expect(jobs).toContain("listKlineBackfills(spaceId, filterOf(segment.value)");
    expect(jobs).toContain("retryKlineBackfill(");
    expect(jobs).toContain("重试失败分块");
  });

  it("uses a UTC range picker and can backfill the checked gaps", () => {
    expect(dialog).toContain("a-range-picker");
    expect(dialog).toContain("时间范围（UTC");
    expect(dialog).toContain("补采这些缺口");
  });
});
