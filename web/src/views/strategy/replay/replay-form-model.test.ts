import { describe, expect, it } from "vitest";
import type { Strategy } from "@/api/strategy-types";
import { fixtureRequirement } from "../../../../tests/strategy-requirement";
import * as model from "./replay-form-model";
import { replaySourceOf, wholeDays, type ReplayFormState } from "./replay-form-model";

// 页面会把后端按定义或 DSL 草稿推出的要求写进 form.requirement；这里按同样的规则模拟，测试仍按（表单, 定义列表）调用。
function withRequirement(form: ReplayFormState, list: Strategy[]): ReplayFormState {
  const lookup = (id: string) => list.find(item => item.strategy_id === id)?.dsl_yaml ?? null;
  // 从实例发起时按实例引用的定义读
  const dsl = form.instance_id
    ? (lookup(form.instance_strategy_id ?? "") ?? "")
    : form.source === "dsl"
      ? form.dsl_yaml
      : (lookup(form.strategy_id) ?? "");
  return { ...form, requirement: dsl ? fixtureRequirement(dsl, lookup) : null };
}
const replayPool = (form: ReplayFormState, list: Strategy[]) => model.replayPool(withRequirement(form, list));
const replayDeclared = (form: ReplayFormState, list: Strategy[]) => model.replayDeclared(withRequirement(form, list));
const replayMarkets = (form: ReplayFormState, list: Strategy[]) => model.replayMarkets(withRequirement(form, list));
const replayFactorTasks = (form: ReplayFormState, list: Strategy[]) => model.replayFactorTasks(withRequirement(form, list));
const replayDateOnly = (form: ReplayFormState, list: Strategy[], tasks: Parameters<typeof model.replayDateOnly>[1]) =>
  model.replayDateOnly(withRequirement(form, list), tasks);
const replayFundingColumn = (form: ReplayFormState, list: Strategy[] = []) =>
  model.replayFundingColumn(withRequirement(form, list));
const validateReplayForm = (form: ReplayFormState, list: Strategy[] = []) =>
  model.validateReplayForm(withRequirement(form, list));
const replayIsPool = (form: ReplayFormState, list: Strategy[]) => model.replayIsPool(withRequirement(form, list));
const replayDriftHint = (form: ReplayFormState, list: Strategy[]) => model.replayDriftHint(withRequirement(form, list));
const replayTuned = (form: ReplayFormState, list: Strategy[]) => model.replayTuned(withRequirement(form, list));

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
  slippage_bps: 0,
  funding_column: "",
  input: "view"
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
      { task_id: "ftask_swap", frequency: "1d" },
      { task_id: "ftask_5m", frequency: "5m" }
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
    for (const value of [-1, 201, Number.NaN]) {
      expect(validateReplayForm({ ...base, slippage_bps: value }, strategies)).toContain("滑点");
    }
    expect(validateReplayForm({ ...base, slippage_bps: 200 }, strategies)).toBe("");
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

describe("写了参考市场的策略", () => {
  // 主帧只有现货，参考市场永续只用于年龄判定：回放也要登记参考市场的任务，资金费与股票判断只看主帧
  const withReference = [
    {
      strategy_id: "r",
      dsl_yaml: "name: r\nbar: 1h\nmarket: {select: spot, reference: [swap]}\nuniverse: {min_age_bars: 3, age_markets: all}\n"
    }
  ] as Strategy[];
  const form = { ...base, strategy_id: "r", tasks: { spot: "ftask_spot", swap: "ftask_swap" }, funding_column: "funding_rate" };

  it("参考市场的任务也提交，因子现算可用，不带资金费列", () => {
    expect(replayMarkets(form, withReference)).toEqual(["spot", "swap"]);
    expect(replayFactorTasks(form, withReference)).toEqual({ spot: "ftask_spot", swap: "ftask_swap" });
    expect(model.replayMainMarkets(withRequirement(form, withReference))).toEqual(["spot"]);
    expect(model.replayIsReference(withRequirement(form, withReference), "swap")).toBe(true);
    expect(validateReplayForm({ ...form, input: "compute" }, withReference)).toBe("");
    expect(replayFundingColumn(form, withReference)).toBe("");
  });

  it("从实例发起时原样提交实例的任务键，资金费列只看主帧", () => {
    const instanceForm = { ...form, instance_id: "i1", instance_strategy_id: "r" };
    expect(replayFactorTasks(instanceForm, withReference)).toEqual({ spot: "ftask_spot", swap: "ftask_swap" });
    expect(validateReplayForm({ ...instanceForm, input: "compute" }, withReference)).toBe("");
    expect(replayFundingColumn(instanceForm, withReference)).toBe("");
    // 实例引用的定义还没读到时按全部市场算，永续在内就带资金费列
    expect(replayFundingColumn({ ...instanceForm, instance_strategy_id: "" }, withReference)).toBe("funding_rate");
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
    expect(replayPool(form(both), catalog)).toMatchObject({ pool: true, taskKeys: ["spot", "swap"], problems: [] });
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
    expect(validateReplayForm(form(mixed), [...members, mixed])).toContain("这份定义还不能回放");
  });

  it("策略池的资金费写在池 DSL 里，表单里的资金费列不提交", () => {
    const catalog = [...members, both];
    expect(replayFundingColumn(form(both, { funding_column: "funding_rate" }), catalog)).toBe("");
    expect(validateReplayForm(form(both, { funding_column: "funding-rate" }), catalog)).toBe("");
  });

  it("从池实例发起时按实例引用的定义认出策略池，资金费列不提交；定义还没读到或不是池时照常提交", () => {
    const catalog = [...members, both, ...strategies];
    const instance = (strategyId: string | undefined): ReplayFormState => ({
      ...base,
      instance_id: "i1",
      instance_strategy_id: strategyId,
      tasks: { spot: "ftask_spot", swap: "ftask_swap" },
      funding_column: "funding_rate"
    });
    expect(replayIsPool(instance("p"), catalog)).toBe(true);
    expect(replayFundingColumn(instance("p"), catalog)).toBe("");
    expect(replayIsPool(instance("s1"), catalog)).toBe(false);
    expect(replayFundingColumn(instance("s1"), catalog)).toBe("funding_rate");
    expect(replayFundingColumn(instance(undefined), catalog)).toBe("funding_rate");
    // 不从实例发起时只看回放对象的 DSL，表单里残留的实例定义不起作用。
    expect(replayIsPool({ ...instance("p"), instance_id: "" }, catalog)).toBe(false);
    expect(replayIsPool(form(both), catalog)).toBe(true);
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
    expect(replayDriftHint(form(free), [...members, free])).toBe(
      "drift 池：漂移从区间的第一根开始，与实时不可比；要逐期比对请在池 DSL 写 allocation.start。"
    );
    expect(replayDriftHint(form(both), [...members, both])).toBe("");
    expect(replayDriftHint({ ...form(started), instance_id: "i1" }, [...members, started])).toBe("");
  });

  it("因子数据来源：factor_params 的策略必须用因子现算；因子现算不支持策略池、两个任务与股票", () => {
    const tuned = [
      ...strategies,
      {
        strategy_id: "t1",
        dsl_yaml: 'name: t\nmarket: {select: swap}\nfactor_params:\n  ma__var:\n    window: {default: "close"}\n'
      }
    ] as Strategy[];
    const form = { ...base, strategy_id: "t1" };
    expect(replayTuned(form, tuned)).toBe(true);
    expect(replayTuned(base, tuned)).toBe(false);
    expect(replayTuned({ ...base, strategy_id: "", instance_id: "i1", instance_strategy_id: "t1" }, tuned)).toBe(true);
    expect(validateReplayForm(form, tuned)).toContain("因子现算");
    expect(validateReplayForm({ ...form, input: "compute" }, tuned)).toBe("");
    // 现货加永续各开一个现算会话：两个主帧任务都放行
    expect(
      validateReplayForm({ ...base, strategy_id: "s1", tasks: { spot: "a", swap: "b" }, input: "compute" }, strategies)
    ).toBe("");
    expect(
      validateReplayForm(
        { ...base, strategy_id: "s3", single_market: "equity", tasks: { equity: "e" }, input: "compute" },
        strategies
      )
    ).toContain("股票");
  });
});
