import { describe, expect, it } from "vitest";
import { parseUtcInput } from "./model";
import { type ReplayFormState, replaySourceOf, validateReplayForm } from "./replay-form-model";

const base: ReplayFormState = {
  source: "definition",
  strategy_id: "s1",
  dsl_yaml: "",
  instance_id: "",
  factor_task_id: "ftask_1",
  start: "2026-09-01 00:00",
  end: "2026-09-02 00:00",
  fee_bps: 10
};

describe("replay form model", () => {
  it("prefers the instance, then the chosen definition or DSL text", () => {
    expect(replaySourceOf({ ...base, instance_id: "i1" })).toEqual({ instance_id: "i1" });
    expect(replaySourceOf(base)).toEqual({ strategy_id: "s1" });
    expect(replaySourceOf({ ...base, source: "dsl", dsl_yaml: "  " })).toBeNull();
    expect(replaySourceOf({ ...base, source: "dsl", dsl_yaml: "name: x" })).toEqual({ dsl_yaml: "name: x" });
  });

  it("accepts a valid form", () => {
    expect(validateReplayForm(base, parseUtcInput)).toBe("");
  });

  it("requires a factor task and a source", () => {
    expect(validateReplayForm({ ...base, factor_task_id: "" }, parseUtcInput)).toContain("请选择因子任务");
    expect(validateReplayForm({ ...base, strategy_id: "" }, parseUtcInput)).toContain("请选择因子任务");
  });

  it("checks the time format and ordering", () => {
    expect(validateReplayForm({ ...base, start: "yesterday" }, parseUtcInput)).toContain("时间格式");
    expect(validateReplayForm({ ...base, end: base.start }, parseUtcInput)).toBe("结束时间必须晚于开始时间");
    expect(validateReplayForm({ ...base, start: base.end, end: base.start }, parseUtcInput)).toBe("结束时间必须晚于开始时间");
  });

  it("limits the fee", () => {
    expect(validateReplayForm({ ...base, fee_bps: -1 }, parseUtcInput)).toContain("手续费");
    expect(validateReplayForm({ ...base, fee_bps: 1001 }, parseUtcInput)).toContain("手续费");
    expect(validateReplayForm({ ...base, fee_bps: Number.NaN }, parseUtcInput)).toContain("手续费");
    expect(validateReplayForm({ ...base, fee_bps: 0 }, parseUtcInput)).toBe("");
  });
});
