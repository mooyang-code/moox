import { describe, expect, it } from "vitest";
import {
  deriveTargetState,
  itemReasonLabel,
  parseResolved,
  parseSummary,
  percent,
  shortHash,
  skipReasonLabel,
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
    expect(itemReasonLabel("holding")).toBe("延续批次持有（holding）");
    expect(itemReasonLabel("holding:filter")).toBe("延续批次持有，本期filter 未通过（holding:filter）");
    expect(itemReasonLabel("missing:close")).toBe("缺少 close（missing:close）");
    expect(itemReasonLabel("factor_failed:ma_20")).toContain("上游因子失败（ma_20）");
    expect(itemReasonLabel("custom_reason")).toBe("custom_reason");
  });
});
