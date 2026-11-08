import { describe, expect, it } from "vitest";
import type { Strategy } from "@/api/strategy-types";
import {
  type ReplayFormState,
  replayDateOnly,
  replayFactorTasks,
  replayMarkets,
  replaySourceOf,
  validateReplayForm,
  wholeDays
} from "./replay-form-model";

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
  range: [Date.parse("2026-09-01T00:00:00Z"), Date.parse("2026-09-02T00:00:00Z")],
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
    expect(validateReplayForm(base, strategies)).toBe("");
  });

  it("requires a source and a task for every market in use", () => {
    expect(validateReplayForm({ ...base, strategy_id: "" }, strategies)).toContain("请选择定义");
    expect(validateReplayForm({ ...base, tasks: {} }, strategies)).toContain("每个市场");
    expect(validateReplayForm({ ...base, strategy_id: "s1", tasks: { spot: "a" } }, strategies)).toContain("每个市场");
    expect(validateReplayForm({ ...base, strategy_id: "s3" }, strategies)).toContain("先选任务所属的市场");
  });

  it("lets an instance replay omit the tasks and read the instance's own", () => {
    const fromInstance = { ...base, instance_id: "i1", tasks: {} };
    expect(validateReplayForm(fromInstance, strategies)).toBe("");
    expect(replayFactorTasks(fromInstance, strategies)).toEqual({});
    expect(validateReplayForm({ ...fromInstance, instance_id: "" }, strategies)).toContain("每个市场");
  });

  it("requires a range whose end is after the start", () => {
    expect(validateReplayForm({ ...base, range: null }, strategies)).toBe("请选择回放区间");
    const [start] = base.range!;
    expect(validateReplayForm({ ...base, range: [start, start] }, strategies)).toBe("结束时间必须晚于开始时间");
  });

  it("picks dates only when every chosen task is daily", () => {
    const tasks = [
      { task_id: "ftask_swap", freq: "1d" },
      { task_id: "ftask_5m", freq: "5m" }
    ];
    expect(replayDateOnly(base, strategies, tasks)).toBe(true);
    expect(replayDateOnly({ ...base, tasks: { swap: "ftask_5m" } }, strategies, tasks)).toBe(false);
    expect(replayDateOnly({ ...base, tasks: {} }, strategies, tasks)).toBe(false);
  });

  it("limits both fees", () => {
    for (const key of ["spot_fee_bps", "swap_fee_bps"] as const) {
      expect(validateReplayForm({ ...base, [key]: -1 }, strategies)).toContain("手续费");
      expect(validateReplayForm({ ...base, [key]: 1001 }, strategies)).toContain("手续费");
      expect(validateReplayForm({ ...base, [key]: Number.NaN }, strategies)).toContain("手续费");
      expect(validateReplayForm({ ...base, [key]: 0 }, strategies)).toBe("");
    }
  });

  it("只选日期时区间吸附成显示的整天：起点零点，终点是最后一天的次日零点（按空间时区）", () => {
    const range: [number, number] = [Date.parse("2026-10-08T10:00:00Z"), Date.parse("2026-11-07T10:00:00Z")];
    expect(wholeDays(range, "UTC")).toEqual([Date.parse("2026-10-08T00:00:00Z"), Date.parse("2026-11-08T00:00:00Z")]);
    // UTC+8：10-08 18:00 是 10-08 这天，11-07 18:00 是 11-07 这天
    expect(wholeDays(range, "Asia/Shanghai")).toEqual([Date.parse("2026-10-07T16:00:00Z"), Date.parse("2026-11-07T16:00:00Z")]);
    // 已经是整天的不变
    const aligned: [number, number] = [Date.parse("2026-10-08T00:00:00Z"), Date.parse("2026-11-08T00:00:00Z")];
    expect(wholeDays(aligned, "UTC")).toEqual(aligned);
  });
});
