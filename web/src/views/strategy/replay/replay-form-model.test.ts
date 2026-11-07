import { describe, expect, it } from "vitest";
import type { Strategy } from "@/api/strategy-types";
import { parseUtcInput } from "./model";
import { type ReplayFormState, replayFactorTasks, replayMarkets, replaySourceOf, validateReplayForm } from "./replay-form-model";

const strategies = [
  { strategy_id: "s1", dsl_yaml: "name: a\nmarket: {select: both, prefer: spot}\n" },
  { strategy_id: "s2", dsl_yaml: "name: b\nmarket: {select: swap}\n" },
  { strategy_id: "s3", dsl_yaml: "name: c\n" }
] as Strategy[];

const base: ReplayFormState = {
  source: "definition",
  strategy_id: "s2",
  dsl_yaml: "",
  instance_id: "",
  single_market: "",
  tasks: { swap: "ftask_swap" },
  start: "2026-09-01 00:00",
  end: "2026-09-02 00:00",
  spot_fee_bps: 10,
  swap_fee_bps: 5
};

describe("replay form model", () => {
  it("prefers the instance, then the chosen definition or DSL text", () => {
    expect(replaySourceOf({ ...base, instance_id: "i1" })).toEqual({ instance_id: "i1" });
    expect(replaySourceOf(base)).toEqual({ strategy_id: "s2" });
    expect(replaySourceOf({ ...base, source: "dsl", dsl_yaml: "  " })).toBeNull();
    expect(replaySourceOf({ ...base, source: "dsl", dsl_yaml: "name: x" })).toEqual({ dsl_yaml: "name: x" });
  });

  it("reads the markets from the strategy, the DSL draft, the instance or the user's choice", () => {
    expect(replayMarkets({ ...base, strategy_id: "s1" }, strategies)).toEqual(["spot", "swap"]);
    expect(replayMarkets(base, strategies)).toEqual(["swap"]);
    expect(replayMarkets({ ...base, source: "dsl", dsl_yaml: "name: x\nmarket: {select: spot}\n" }, strategies)).toEqual([
      "spot"
    ]);
    expect(replayMarkets({ ...base, instance_id: "i1", tasks: { swap: "b", spot: "a" } }, strategies)).toEqual(["spot", "swap"]);
    // 统一数据集上的实例：任务键是 multi，回放读这一个任务。
    expect(replayMarkets({ ...base, instance_id: "i1", tasks: { multi: "u" } }, strategies)).toEqual(["multi"]);
    expect(replayFactorTasks({ ...base, instance_id: "i1", tasks: { multi: "u" } }, strategies)).toEqual({ multi: "u" });
    expect(replayMarkets({ ...base, strategy_id: "s3", single_market: "" }, strategies)).toEqual([]);
    expect(replayMarkets({ ...base, strategy_id: "s3", single_market: "equity" }, strategies)).toEqual(["equity"]);
  });

  it("submits only the tasks of the markets in use", () => {
    expect(
      replayFactorTasks({ ...base, strategy_id: "s1", tasks: { spot: "a", swap: "b", equity: "stale" } }, strategies)
    ).toEqual({
      spot: "a",
      swap: "b"
    });
  });

  it("accepts a valid form", () => {
    expect(validateReplayForm(base, parseUtcInput, strategies)).toBe("");
  });

  it("requires a source and a task for every market in use", () => {
    expect(validateReplayForm({ ...base, strategy_id: "" }, parseUtcInput, strategies)).toContain("请选择定义");
    expect(validateReplayForm({ ...base, tasks: {} }, parseUtcInput, strategies)).toContain("每个市场");
    expect(validateReplayForm({ ...base, strategy_id: "s1", tasks: { spot: "a" } }, parseUtcInput, strategies)).toContain(
      "每个市场"
    );
    expect(validateReplayForm({ ...base, strategy_id: "s3" }, parseUtcInput, strategies)).toContain("先选任务所属的市场");
  });

  it("lets an instance replay omit the tasks and read the instance's own", () => {
    const fromInstance = { ...base, instance_id: "i1", tasks: {} };
    expect(validateReplayForm(fromInstance, parseUtcInput, strategies)).toBe("");
    expect(replayFactorTasks(fromInstance, strategies)).toEqual({});
    expect(validateReplayForm({ ...fromInstance, instance_id: "" }, parseUtcInput, strategies)).toContain("每个市场");
  });

  it("checks the time format and ordering", () => {
    expect(validateReplayForm({ ...base, start: "yesterday" }, parseUtcInput, strategies)).toContain("时间格式");
    expect(validateReplayForm({ ...base, end: base.start }, parseUtcInput, strategies)).toBe("结束时间必须晚于开始时间");
    expect(validateReplayForm({ ...base, start: base.end, end: base.start }, parseUtcInput, strategies)).toBe(
      "结束时间必须晚于开始时间"
    );
  });

  it("limits both fees", () => {
    for (const key of ["spot_fee_bps", "swap_fee_bps"] as const) {
      expect(validateReplayForm({ ...base, [key]: -1 }, parseUtcInput, strategies)).toContain("手续费");
      expect(validateReplayForm({ ...base, [key]: 1001 }, parseUtcInput, strategies)).toContain("手续费");
      expect(validateReplayForm({ ...base, [key]: Number.NaN }, parseUtcInput, strategies)).toContain("手续费");
      expect(validateReplayForm({ ...base, [key]: 0 }, parseUtcInput, strategies)).toBe("");
    }
  });
});
