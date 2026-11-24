import { describe, expect, it } from "vitest";
import {
  formatFactorTasks,
  instanceTasks,
  parseFactorTasks,
  positionLabel,
  requirementTaskKeys,
  splitTaskKey,
  taskKeyLabel,
  taskKeyMarkets,
  tasksText
} from "./market-mode";

describe("市场模式", () => {
  it("计算任务在 URL 里往返：只认三个市场，坏项忽略", () => {
    expect(parseFactorTasks("multi=ftask_u")).toEqual({});
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

  it("实例的计算任务按现货、永续、股票排列并展示", () => {
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

describe("任务键", () => {
  it("按后端的要求拼出任务键：市场确定时用它给的键，没确定时按选的市场拼主帧与附加来源", () => {
    const declared = { pool: false, marketDeclared: true, taskKeys: ["swap", "spot", "daily:swap"], sources: [], problems: [] };
    expect(requirementTaskKeys(declared, "")).toEqual(["swap", "spot", "daily:swap"]);
    expect(taskKeyMarkets(requirementTaskKeys(declared, ""))).toEqual(["swap", "spot"]);
    const open = { ...declared, marketDeclared: false, taskKeys: [], sources: [{ name: "daily", bar: "1d" }] };
    expect(requirementTaskKeys(open, "")).toEqual([]);
    expect(requirementTaskKeys(open, "equity")).toEqual(["equity", "daily:equity"]);
    expect(requirementTaskKeys(null, "spot")).toEqual([]);
  });

  it("任务键 来源名:市场 的拆分、展示与 URL 往返", () => {
    expect(splitTaskKey("daily:swap")).toEqual({ source: "daily", market: "swap" });
    expect(splitTaskKey("spot")).toEqual({ source: "", market: "spot" });
    expect(taskKeyLabel("daily:spot")).toBe("来源 daily（现货）");
    expect(taskKeyLabel("swap")).toBe("永续");
    const tasks = { "daily:swap": "ftask_d", swap: "ftask_s", spot: "ftask_r" };
    expect(instanceTasks(tasks).map(item => item.market)).toEqual(["spot", "swap", "daily:swap"]);
    expect(tasksText(tasks)).toBe("现货 ftask_r、永续 ftask_s、来源 daily（永续） ftask_d");
    expect(parseFactorTasks(formatFactorTasks(tasks))).toEqual(tasks);
    expect(parseFactorTasks("Daily:swap=x,daily:future=y,:spot=z")).toEqual({});
  });
});
