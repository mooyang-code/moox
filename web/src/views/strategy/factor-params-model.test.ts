import { describe, expect, it } from "vitest";
import type { StrategyResult } from "@/api/strategy-types";
import { deliveryStatus, memberParamsSummary, nextParamsRows, parseFactorBinding, periodText } from "./factor-params-model";

const result = (patch: Partial<StrategyResult> = {}): StrategyResult => ({
  result_id: "r1",
  instance_id: "i1",
  session_id: "s1",
  bar_end_time: "2026-10-26T10:00:00Z",
  valid_until: "",
  status: "ok",
  skip_reason: "",
  dsl_hash: "",
  targets: [],
  summary_json: "{}",
  input_json: "{}",
  rule_states_json: "{}",
  publish_status: "none",
  created_at: "",
  next_params_json: "",
  params_delivered_at: "",
  params_error: "",
  ...patch
});

describe("parseFactorBinding", () => {
  it("reads the task, result view and the variable members with their parameters", () => {
    const binding = parseFactorBinding(
      JSON.stringify({
        factor_task_id: "ftask_a",
        view_id: "view_factor_a",
        factor_params: { ma__var: [{ name: "span" }], bias__var: [{ name: "window" }, { name: "k" }] }
      })
    );
    expect(binding).toEqual({
      taskId: "ftask_a",
      viewId: "view_factor_a",
      members: ["bias__var", "ma__var"],
      params: { ma__var: ["span"], bias__var: ["window", "k"] }
    });
  });

  it("returns null without a task and tolerates malformed json", () => {
    expect(parseFactorBinding("{}")).toBeNull();
    expect(parseFactorBinding("not json")).toBeNull();
    expect(parseFactorBinding(undefined)).toBeNull();
    // 没有可变成员的实例仍然关联着任务。
    expect(parseFactorBinding('{"factor_task_id":"ftask_a"}')).toMatchObject({ members: [], params: {} });
  });
});

describe("params summaries", () => {
  it("summarises members and parameters in a stable order, mentioning per-subject overrides", () => {
    expect(
      memberParamsSummary({
        ma__var: { span: { default: 60 } },
        bias__var: { window: { default: 20, overrides: { BTC: 10, ETH: 30 } }, k: { default_value: 2 } }
      })
    ).toBe("bias__var: k=2，window=20（2 个标的覆盖）；ma__var: span=60");
  });

  it("classifies the delivery state of a result", () => {
    expect(deliveryStatus(result({ params_delivered_at: "2026-10-26T10:01:00Z" })).key).toBe("delivered");
    expect(deliveryStatus(result({ params_error: "超出范围" }))).toMatchObject({ key: "rejected", hint: "超出范围" });
    expect(deliveryStatus(result()).key).toBe("pending");
    // 已送达优先于遗留的拒绝原因。
    expect(deliveryStatus(result({ params_delivered_at: "x", params_error: "y" })).key).toBe("delivered");
  });

  it("lists only results that carry next params, newest first", () => {
    const envelope = JSON.stringify({ period: 1790000000, members: { bias__var: { window: { default: 20 } } } });
    const rows = nextParamsRows([
      result({
        result_id: "old",
        bar_end_time: "2026-10-26T09:00:00Z",
        next_params_json: envelope,
        params_delivered_at: "2026-10-26T09:01:00Z"
      }),
      result({ result_id: "none", bar_end_time: "2026-10-26T09:30:00Z" }),
      result({ result_id: "new", bar_end_time: "2026-10-26T10:00:00Z", next_params_json: envelope, params_error: "被拒绝" })
    ]);
    expect(rows.map(row => row.resultId)).toEqual(["new", "old"]);
    expect(rows[0]).toMatchObject({ statusKey: "rejected", summary: "bias__var: window=20", targetText: periodText(1790000000) });
    expect(rows[1].statusKey).toBe("delivered");
  });

  it("formats unix seconds as UTC text", () => {
    expect(periodText(0)).toBe("-");
    expect(periodText("1790000000")).toBe("2026-09-21 14:13:20 UTC");
  });
});
