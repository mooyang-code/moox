import { describe, expect, it } from "vitest";
import { instanceTasks, marketRequirement, singleTaskId, tasksText } from "./market-mode";

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

  it("实例的因子任务按现货、永续、股票排列并展示", () => {
    const tasks = { swap: "ftask_swap", spot: "ftask_spot" };
    expect(instanceTasks(tasks)).toEqual([
      { market: "spot", taskId: "ftask_spot" },
      { market: "swap", taskId: "ftask_swap" }
    ]);
    expect(tasksText(tasks)).toBe("现货 ftask_spot、永续 ftask_swap");
    expect(tasksText({ equity: "ftask_cn" })).toBe("ftask_cn");
    expect(singleTaskId({ equity: "ftask_cn" })).toBe("ftask_cn");
    expect(singleTaskId(tasks)).toBe("");
    expect(tasksText(undefined)).toBe("");
  });
});
