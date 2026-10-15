import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  datasetFunctions,
  datasetLabel,
  datasetNameMap,
  durationText,
  filterFunctions,
  functionOptions,
  functionStateInfo,
  lagColor,
  levelInfo,
  statusSummary
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
  it("labels health levels and function states", () => {
    expect(levelInfo("error").text).toBe("异常");
    expect(levelInfo("warn").color).toBe("orangered");
    expect(levelInfo(undefined).text).toBe("正常");
    expect(functionStateInfo("pending").text).toBe("下发中");
    expect(functionStateInfo("custom").text).toBe("custom");
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

  it("shows dataset display names and falls back to the id", () => {
    const names = datasetNameMap([
      { dataset_id: "ds_spot", frequency: "1m", dataset_name: "Binance 现货 K 线 1m" },
      { dataset_id: "ds_a", frequency: "1m" }
    ]);
    expect(datasetLabel(names, "ds_spot|1m")).toBe("Binance 现货 K 线 1m");
    expect(datasetLabel(names, "ds_spot")).toBe("Binance 现货 K 线 1m");
    expect(datasetLabel(names, "ds_a|1m")).toBe("ds_a|1m");
  });

  it("summarizes status and filters functions by name, dataset and display name", () => {
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
    const names = datasetNameMap([{ dataset_id: "ds_a", frequency: "1m", dataset_name: "A 股 K 线 1m" }]);
    expect(filterFunctions(functions, { ...base, keyword: "A 股" }, names)).toHaveLength(1);
    expect(filterFunctions(functions, { ...base, region: "ap-nanjing" })).toHaveLength(1);
    expect(filterFunctions(functions, { ...base, abnormalOnly: true })).toHaveLength(2);
    expect(functionOptions(functions, "region")).toEqual(["ap-nanjing", "ap-tokyo"]);
    expect(datasetFunctions(functions, { dataset_id: "ds_spot", frequency: "1m" })).toHaveLength(2);
  });
});

describe("collector runtime status page", () => {
  const page = readFileSync(resolve(__dirname, "runtime-status.vue"), "utf8");

  it("leads with the overall verdict, dataset health and the function list", () => {
    expect(page).toContain("getCollectorStatus(spaceId)");
    expect(page.indexOf('class="status-card"')).toBeLessThan(page.indexOf("<h3>数据集健康</h3>"));
    expect(page.indexOf("<h3>数据集健康</h3>")).toBeLessThan(page.indexOf('class="function-section"'));
    expect(page).toContain('category="collector.subject_change"');
    expect(page).not.toContain("a-tab-pane");
    expect(page).not.toContain("hash");
  });

  it("shows dataset display names instead of ids", () => {
    expect(page).toContain("record.dataset_name");
    expect(page).toContain("datasetLabel(datasetNames, item)");
  });

  it("keeps the function filters on one row and scrolls inside the function table", () => {
    const toolbar = page.slice(
      page.indexOf('<div class="pane-toolbar">'),
      page.indexOf("</div>", page.indexOf('<div class="pane-toolbar">'))
    );
    for (const marker of ["搜索函数名或数据集", "全部地域", "全部市场", "只看异常"]) expect(toolbar).toContain(marker);
    expect(page).toContain(":scroll=\"{ y: '100%' }\"");
    expect(page).toMatch(/\.runtime-status > \.function-section\s*\{[^}]*flex:\s*1;/);
  });

  it("offers the dataset actions and opens the backfill tab for new jobs", () => {
    expect(page).toContain("confirmHistoryGaps(");
    expect(page).toContain("clearSourceAbsences(");
    expect(page).toContain("补采确认");
    expect(page).toContain("清除并重新检查");
    expect(page).toContain('tab: "backfills"');
  });
});
