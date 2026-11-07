import { describe, expect, it } from "vitest";
import { formatFactorTasks, instanceTasks, marketRequirement, parseFactorTasks, positionLabel, tasksText } from "./market-mode";

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
