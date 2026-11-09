import { describe, expect, it } from "vitest";
import type { Strategy } from "@/api/strategy-types";
import {
  type ReplayFormState,
  replayDateOnly,
  replayFactorTasks,
  replayDeclared,
  replayDriftHint,
  replayFundingColumn,
  replayMarkets,
  replayPool,
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
  swap_fee_bps: 5,
  funding_column: ""
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

  it("checks the optional funding column is a plain column name", () => {
    expect(validateReplayForm({ ...base, funding_column: "funding_rate" }, strategies)).toBe("");
    expect(validateReplayForm({ ...base, funding_column: "  " }, strategies)).toBe("");
    expect(validateReplayForm({ ...base, funding_column: "funding-rate" }, strategies)).toContain("资金费列");
  });

  it("drops a funding column left over from a swap strategy after switching to a spot one", () => {
    expect(replayFundingColumn({ ...base, funding_column: " funding_rate " }, strategies)).toBe("funding_rate");
    // 换成纯现货策略：资金费列的输入框已隐藏，旧值不提交，也不参与校验。
    const spot = { ...base, source: "dsl" as const, dsl_yaml: "name: x\nmarket: {select: spot}\n", tasks: { spot: "a" } };
    expect(replayFundingColumn({ ...spot, funding_column: "funding-rate" }, strategies)).toBe("");
    expect(validateReplayForm({ ...spot, funding_column: "funding-rate" }, strategies)).toBe("");
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

describe("策略池回放表单", () => {
  const members = [
    { strategy_id: "m_spot", dsl_yaml: "name: a\nbar: 1h\nmarket: {select: spot}\n" },
    { strategy_id: "m_swap", dsl_yaml: "name: b\nbar: 1h\nmarket: {select: swap}\n" },
    { strategy_id: "m_stock", dsl_yaml: "name: c\nbar: 1d\n" }
  ] as Strategy[];
  const pool = (list: string, allocation = "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n") =>
    ({ strategy_id: "p", dsl_yaml: `name: p\nbar: 1h\nstrategy_pool:\n  members:\n${list}${allocation}` }) as Strategy;
  const both = pool("    - {id: a, strategy: m_spot}\n    - {id: b, strategy: m_swap}\n");
  const form = (strategy: Strategy, extra: Partial<ReplayFormState> = {}): ReplayFormState => ({
    ...base,
    strategy_id: strategy.strategy_id,
    tasks: { spot: "ftask_spot", swap: "ftask_swap" },
    ...extra
  });

  it("策略池读成员所需市场的并集，不用先选任务所属的市场", () => {
    const catalog = [...members, both];
    expect(replayPool(form(both), catalog)).toEqual({ markets: ["spot", "swap"], problems: [] });
    expect(replayDeclared(form(both), catalog)).toBe(true);
    expect(replayMarkets(form(both), catalog)).toEqual(["spot", "swap"]);
    expect(replayFactorTasks(form(both), catalog)).toEqual({ spot: "ftask_spot", swap: "ftask_swap" });
    expect(validateReplayForm(form(both), catalog)).toBe("");
    // 普通策略不受影响
    expect(replayPool(base, strategies)).toBeNull();
    expect(replayDeclared({ ...base, strategy_id: "s3" }, strategies)).toBe(false);
  });

  it("成员不存在或股票与加密成员混用时不能发起", () => {
    const ghost = pool("    - {id: a, strategy: m_spot}\n    - {id: b, strategy: ghost}\n");
    expect(validateReplayForm(form(ghost), [...members, ghost])).toContain("成员 b 引用的策略 ghost 不存在");
    const mixed = pool("    - {id: a, strategy: m_spot}\n    - {id: b, strategy: m_stock}\n");
    expect(validateReplayForm(form(mixed), [...members, mixed])).toContain("策略池的成员还不能回放");
  });

  it("策略池的资金费写在池 DSL 里，表单里的资金费列不提交", () => {
    const catalog = [...members, both];
    expect(replayFundingColumn(form(both, { funding_column: "funding_rate" }), catalog)).toBe("");
    expect(validateReplayForm(form(both, { funding_column: "funding-rate" }), catalog)).toBe("");
  });

  it("drift 池的区间旁说明起点规则；不是 drift 时没有说明", () => {
    const list = "    - {id: a, strategy: m_spot}\n    - {id: b, strategy: m_swap}\n";
    const started = pool(
      list,
      '  allocation:\n    method: drift\n    ratios: {a: 0.5, b: 0.5}\n    start: "2026-09-01T05:00:00Z"\n'
    );
    const free = pool(list, "  allocation:\n    method: drift\n    ratios: {a: 0.5, b: 0.5}\n");
    expect(replayDriftHint(form(started), [...members, started])).toBe(
      "drift 池：起点必须选漂移起点 2026-09-01T05:00:00Z，结果才与实时可比。"
    );
    expect(replayDriftHint(form(free), [...members, free])).toBe("drift 池：漂移从区间的第一根开始。");
    expect(replayDriftHint(form(both), [...members, both])).toBe("");
    expect(replayDriftHint({ ...form(started), instance_id: "i1" }, [...members, started])).toBe("");
  });
});
