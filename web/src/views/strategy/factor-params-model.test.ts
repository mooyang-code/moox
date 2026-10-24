import { describe, expect, it } from "vitest";
import type { ParamSubmission } from "@/api/strategy-types";
import { parseFactorBinding, submissionRows, submissionStatus, submissionSummary } from "./factor-params-model";

const resolved = JSON.stringify({
  view_id: "view_factor_i1",
  source_view_id: "view_kline",
  factor_set_id: "fset_i1",
  bar: "1m",
  columns: {
    fast_bias: { source: "factor", member: "fast", factor_id: "Bias" },
    vol_zf_std: { source: "factor", member: "vol", factor_id: "ZfStd" }
  },
  factor_params: { fast: [{ name: "window" }, { name: "k" }], slow: [{ name: "window" }] }
});

describe("factor binding of an instance", () => {
  it("reads the private set, members and declared parameter names", () => {
    expect(parseFactorBinding(resolved)).toEqual({
      setId: "fset_i1",
      viewId: "view_factor_i1",
      sourceViewId: "view_kline",
      members: ["fast", "slow", "vol"],
      params: { fast: ["window", "k"], slow: ["window"] }
    });
  });

  it("returns null for instances without a private factor set", () => {
    expect(parseFactorBinding("{}")).toBeNull();
    expect(parseFactorBinding(undefined)).toBeNull();
    expect(parseFactorBinding("not json")).toBeNull();
    expect(parseFactorBinding(JSON.stringify({ view_id: "v" }))).toBeNull();
  });
});

describe("parameter submissions", () => {
  const submission = (overrides: Partial<ParamSubmission>): ParamSubmission => ({
    request_id: "p-1",
    instance_id: "i1",
    result_id: "r1",
    set_id: "fset_i1",
    period_time: Date.UTC(2026, 9, 23, 8, 1, 0),
    based_on_period: 0,
    params_json: JSON.stringify({
      fast: { window: { default: 20, overrides: { BTC: 10, ETH: 30 } }, k: { default: 2 } },
      slow: { window: { default: 60 } }
    }),
    notes: "",
    status: "accepted",
    attempts: 1,
    outcome_json: "[]",
    created_at: "",
    updated_at: "",
    ...overrides
  });

  it("summarises a snapshot by member and parameter", () => {
    expect(submissionSummary(submission({}).params_json)).toBe("fast: k=2，window=20（2 个标的覆盖）；slow: window=60");
    expect(submissionSummary("{}")).toBe("");
  });

  it("maps statuses and lists only the members that were not accepted", () => {
    expect(submissionStatus("too_late")).toMatchObject({ label: "已晚到", color: "orange" });
    expect(submissionStatus("weird")).toMatchObject({ label: "weird" });
    const [row] = submissionRows([
      submission({
        status: "rejected",
        outcome_json: JSON.stringify([
          { member: "fast", result: "accepted" },
          { member: "slow", result: "rejected", reason: "参数 window 超出范围" }
        ]),
        notes: "成员 vol 求值失败"
      })
    ]);
    expect(row).toMatchObject({ statusLabel: "被拒绝", periodText: "2026-10-23 08:01:00 UTC" });
    expect(row.outcome).toBe("slow rejected：参数 window 超出范围");
    expect(row.notes).toBe("成员 vol 求值失败");
  });
});
