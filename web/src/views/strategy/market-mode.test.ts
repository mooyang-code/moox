import { describe, expect, it } from "vitest";
import {
  formatFactorTasks,
  instanceTasks,
  marketRequirement,
  parseFactorTasks,
  poolMembers,
  poolRequirement,
  positionLabel,
  tasksText
} from "./market-mode";

describe("市场模式", () => {
  it("按 DSL 的 market 读出需要绑定的因子任务", () => {
    expect(marketRequirement("name: a\nmarket: {select: spot, prefer: spot}\n")).toEqual({ declared: true, markets: ["spot"] });
    expect(marketRequirement("name: a\nmarket: {select: swap}\n")).toEqual({ declared: true, markets: ["swap"] });
    expect(marketRequirement("name: a\nmarket: {select: both, prefer: swap}\n")).toEqual({
      declared: true,
      markets: ["spot", "swap"]
    });
    expect(marketRequirement("name: a\nmarket: {select: spot, prefer: swap}\n")).toEqual({
      declared: true,
      markets: ["spot", "swap"]
    });
  });

  it("没写 market 或写法有误时不声明，由后端校验报告", () => {
    expect(marketRequirement("name: a\n")).toEqual({ declared: false, markets: [] });
    expect(marketRequirement("name: [")).toEqual({ declared: false, markets: [] });
    expect(marketRequirement("name: a\nmarket: spot\n")).toEqual({ declared: false, markets: [] });
  });

  it("统一数据集上的任务键 multi 排在最后，URL 里照常往返", () => {
    expect(instanceTasks({ multi: "ftask_u" })).toEqual([{ market: "multi", taskId: "ftask_u" }]);
    expect(tasksText({ multi: "ftask_u" })).toBe("ftask_u");
    expect(formatFactorTasks({ multi: "ftask_u" })).toBe("multi=ftask_u");
    expect(parseFactorTasks("multi=ftask_u")).toEqual({ multi: "ftask_u" });
  });

  it("因子任务在 URL 里往返：只认三个市场，坏项忽略", () => {
    expect(formatFactorTasks({ swap: "ftask_b", spot: "ftask_a" })).toBe("spot=ftask_a,swap=ftask_b");
    expect(formatFactorTasks(undefined)).toBe("");
    expect(parseFactorTasks("spot=ftask_a,swap=ftask_b")).toEqual({ spot: "ftask_a", swap: "ftask_b" });
    expect(parseFactorTasks("option=x,spot=,swap")).toEqual({});
    expect(parseFactorTasks("")).toEqual({});
  });

  it("持仓键展示成 标的（市场）", () => {
    expect(positionLabel("BTC-USDT|swap")).toBe("BTC-USDT（永续）");
    expect(positionLabel("BTC-USDT|spot")).toBe("BTC-USDT（现货）");
    expect(positionLabel("BTC-USDT")).toBe("BTC-USDT");
  });

  it("实例的因子任务按现货、永续、股票排列并展示", () => {
    const tasks = { swap: "ftask_swap", spot: "ftask_spot" };
    expect(instanceTasks(tasks)).toEqual([
      { market: "spot", taskId: "ftask_spot" },
      { market: "swap", taskId: "ftask_swap" }
    ]);
    expect(tasksText(tasks)).toBe("现货 ftask_spot、永续 ftask_swap");
    expect(tasksText({ equity: "ftask_cn" })).toBe("ftask_cn");
    expect(tasksText(undefined)).toBe("");
  });
});

describe("策略池的市场要求", () => {
  const pool = (...ids: string[]) =>
    `name: p\nbar: 1h\nstrategy_pool:\n  members:\n${ids.map((id, index) => `    - {id: m${index}, strategy: ${id}}\n`).join("")}`;
  const defs: Record<string, string | null> = {
    spot: "market: {select: spot}",
    swap: "market: {select: swap}",
    both: "market: {select: both, prefer: spot}",
    split: "market: {select: spot, prefer: swap}",
    plain: "name: x",
    nested: "strategy_pool:\n  members: []"
  };
  const lookup = (id: string) => (id === "loading" ? undefined : (defs[id] ?? null));

  it("识别策略池与成员引用", () => {
    expect(poolMembers(pool("spot", "swap"))).toEqual([
      { id: "m0", strategyId: "spot" },
      { id: "m1", strategyId: "swap" }
    ]);
    expect(poolMembers("name: a\nrules: []")).toBeNull();
    expect(poolMembers("name: [")).toBeNull();
    expect(poolRequirement("name: a", lookup)).toBeNull();
  });

  it("成员市场取并集，现货在前，省略 prefer 取 select，select 与 prefer 不同涉及两个市场", () => {
    expect(poolRequirement(pool("spot", "spot"), lookup)).toEqual({ markets: ["spot"], problems: [] });
    expect(poolRequirement(pool("swap"), lookup)).toEqual({ markets: ["swap"], problems: [] });
    expect(poolRequirement(pool("swap", "spot"), lookup)?.markets).toEqual(["spot", "swap"]);
    expect(poolRequirement(pool("both"), lookup)?.markets).toEqual(["spot", "swap"]);
    expect(poolRequirement(pool("split"), lookup)?.markets).toEqual(["spot", "swap"]);
  });

  it("成员缺失、没写 market、嵌套、读取中、没有成员时给出问题且不给市场", () => {
    expect(poolRequirement(pool("spot", "gone"), lookup)).toEqual({
      markets: [],
      problems: ["成员 m1 引用的策略 gone 不存在（定义已删除，或读取失败）"]
    });
    expect(poolRequirement(pool("plain"), lookup)?.problems[0]).toContain("没有显式写 market");
    expect(poolRequirement(pool("nested"), lookup)?.problems[0]).toContain("策略池不能嵌套");
    expect(poolRequirement(pool("loading"), lookup)?.problems[0]).toContain("正在读取");
    expect(poolRequirement("name: p\nstrategy_pool: {members: []}\n", lookup)?.problems).toEqual([
      "策略池没有写成员（strategy_pool.members）"
    ]);
    expect(poolRequirement("name: p\nstrategy_pool:\n  members:\n    - {id: a}\n", lookup)?.problems).toEqual([
      "成员 a 没有写 strategy"
    ]);
  });
});
