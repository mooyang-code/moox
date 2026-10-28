import { describe, expect, it } from "vitest";
import {
  buildResultInsights,
  buildResultOverview,
  deriveTargetState,
  formatDelay,
  itemReasonLabel,
  parseResolved,
  parseSummary,
  percent,
  shortHash,
  skipReasonLabel,
  sortResultItems,
  stageLabel
} from "./model";

describe("strategy model helpers", () => {
  it("derives the target state from the instance session and validity", () => {
    const instance = { enabled: true, session_id: "s1" };
    const snapshot = {
      targets: [{ instrument_id: "BTC-USDT", target_weight: "0.5" }],
      session_id: "s1",
      bar_end_time: "2026-10-01T01:00:00Z",
      valid_until: "2026-10-01T03:00:00Z",
      result_id: "r1"
    };
    expect(deriveTargetState(instance, snapshot, Date.parse("2026-10-01T02:00:00Z"))).toBe("valid");
    expect(deriveTargetState(instance, snapshot, Date.parse("2026-10-01T04:00:00Z"))).toBe("expired");
    expect(deriveTargetState({ enabled: false, session_id: "" }, snapshot)).toBe("inactive");
    expect(deriveTargetState(instance, { ...snapshot, session_id: "s2" })).toBe("unknown");
    expect(deriveTargetState(instance, { targets: [], session_id: "s1", bar_end_time: "", valid_until: "", result_id: "" })).toBe(
      "empty"
    );
  });

  it("formats percentages, hashes and labels", () => {
    expect(percent("0.256")).toBe("25.60%");
    expect(percent("x")).toBe("-");
    expect(shortHash("sha256:0123456789abcdef")).toBe("0123456789ab…");
    expect(skipReasonLabel("previous_version_unknown")).toContain("上一根版本未知");
    expect(skipReasonLabel("custom")).toBe("custom");
    expect(skipReasonLabel("no_data")).toBe("本期无数据（no_data）");
    expect(skipReasonLabel("history_insufficient")).toContain("历史覆盖不足");
    expect(stageLabel("weighted")).toBe("入选");
  });

  it("parses summaries and resolved bindings defensively", () => {
    const summary = parseSummary(
      '{"universe":5,"rules":{"r":{"expected":5,"missing":1}},"gross":"0.8","cash":"0.2","notes":["n"]}'
    );
    expect(summary.universe).toBe(5);
    expect(summary.rules.r.missing).toBe(1);
    expect(summary.notes).toEqual(["n"]);
    expect(parseSummary("not json").rules).toEqual({});
    const resolved = parseResolved(
      '{"view_id":"v","bar":"1h","calendar":"crypto_24x7","spot":true,"columns":{"ma_20":{"source":"factor","factor_id":"ma","definition_hash":"sha256:x"},"close":{"source":"dataset"}}}'
    );
    expect(resolved?.columns.map(column => column.name)).toEqual(["close", "ma_20"]);
    expect(resolved?.columns[1]).toMatchObject({ factor_id: "ma", definition_hash: "sha256:x" });
    expect(parseResolved("{}")).toBeNull();
  });

  it("translates explanation reasons including prefixed ones", () => {
    expect(itemReasonLabel("")).toBe("-");
    expect(itemReasonLabel("not_selected")).toBe("名次未入选（not_selected）");
    expect(itemReasonLabel("tranche:37")).toBe("由 37 个子策略持有（tranche:37）");
    expect(itemReasonLabel("stop_loss;missing:x")).toBe("固定止损；缺少 x（stop_loss;missing:x）");
    expect(itemReasonLabel("tranche:2;stopped:1")).toBe("由 2 个子策略持有；本期有 1 个持仓单位止损（tranche:2;stopped:1）");
    expect(itemReasonLabel("tranche_filter_after")).toBe("被全部子策略的后置过滤剔除（tranche_filter_after）");
    expect(itemReasonLabel("volume_zero")).toBe("本期无成交（volume=0），不作为候选（volume_zero）");
    expect(itemReasonLabel("holding")).toBe("延续批次持有（holding）");
    expect(itemReasonLabel("holding:filter")).toBe("延续批次持有，本期filter 未通过（holding:filter）");
    expect(itemReasonLabel("missing:close")).toBe("缺少 close（missing:close）");
    expect(itemReasonLabel("factor_failed:ma_20")).toContain("上游因子失败（ma_20）");
    expect(itemReasonLabel("source_failed:close")).toContain("上游数据失败（close）");
    expect(itemReasonLabel("custom_reason")).toBe("custom_reason");
  });
});

describe("result insights", () => {
  const base = { summary_json: "{}", skip_reason: "", session_id: "s1" };
  const at = (minute: number) => `2026-10-01T01:0${minute}:00Z`;
  const ok = (id: string, minute: number, ids: string[], delaySec = 2, session_id = "s1") => ({
    ...base,
    session_id,
    result_id: id,
    status: "ok",
    bar_end_time: at(minute),
    created_at: new Date(Date.parse(at(minute)) + delaySec * 1000).toISOString(),
    targets: ids.map(instrument_id => ({ instrument_id, target_weight: "0.2" }))
  });

  it("compares holdings between adjacent ok results regardless of input order", () => {
    const insights = buildResultInsights([ok("c", 3, ["A", "C"]), ok("a", 1, ["A", "B"]), ok("b", 2, ["A", "B"])]);
    expect(insights.a.comparable).toBe(false);
    expect(insights.b).toMatchObject({ comparable: true, added: [], removed: [] });
    expect(insights.c).toMatchObject({ comparable: true, added: ["C"], removed: ["B"] });
    expect(insights.c.delayMs).toBe(2000);
  });

  it("never compares results across sessions", () => {
    const insights = buildResultInsights([
      ok("a", 1, ["A", "B"], 2, "s1"),
      ok("b", 2, ["A", "B"], 2, "s1"),
      ok("c", 3, ["X"], 2, "s2"),
      ok("d", 4, ["X", "Y"], 2, "s2")
    ]);
    expect(insights.c).toMatchObject({ comparable: false, added: [], removed: [] });
    expect(insights.d).toMatchObject({ comparable: true, added: ["Y"], removed: [] });
  });

  it("keeps the holdings baseline across skipped periods", () => {
    const skipped = {
      ...base,
      result_id: "s",
      status: "skipped",
      skip_reason: "no_data",
      bar_end_time: at(2),
      created_at: at(2),
      targets: []
    };
    const insights = buildResultInsights([ok("a", 1, ["A"]), skipped, ok("c", 3, ["B"])]);
    expect(insights.s.comparable).toBe(false);
    expect(insights.c).toMatchObject({ added: ["B"], removed: ["A"] });
  });

  it("summarizes skip reasons, delay and trend", () => {
    const skipped = (id: string, minute: number, reason: string) => ({
      ...base,
      result_id: id,
      status: "skipped",
      skip_reason: reason,
      bar_end_time: at(minute),
      created_at: at(minute),
      targets: []
    });
    const overview = buildResultOverview([
      ok("a", 1, ["A"], 2),
      ok("b", 2, ["A", "B"], 4),
      skipped("x", 3, "no_data"),
      skipped("y", 4, "no_data"),
      skipped("z", 5, "expired")
    ]);
    expect(overview).toMatchObject({ total: 5, ok: 2, skipped: 3, avgDelayMs: 3000, maxDelayMs: 4000 });
    expect(overview.skipReasons).toEqual([
      { reason: "no_data", count: 2 },
      { reason: "expired", count: 1 }
    ]);
    expect(overview.trend.map(point => point.targets)).toEqual([1, 2]);
    expect(formatDelay(1500)).toBe("1.5s");
    expect(formatDelay(null)).toBe("-");
  });
});

describe("result item ordering", () => {
  const item = (instrument_id: string, stage: string, rank = 0, rule_id = "r") => ({ instrument_id, stage, rank, rule_id });

  it("puts weighted items first by rank and filtered ones last", () => {
    const sorted = sortResultItems([
      item("A", "filtered"),
      item("B", "weighted", 2),
      item("C", "scored", 7),
      item("D", "weighted", 1),
      item("E", "idle"),
      item("F", "scored", 6),
      item("G", "missing")
    ]);
    expect(sorted.map(row => row.instrument_id)).toEqual(["D", "B", "F", "C", "G", "E", "A"]);
  });
});
