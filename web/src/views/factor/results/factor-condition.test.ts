import { describe, expect, it } from "vitest";
import { buildViewFilterExprs } from "@/views/data/view-browse/view-browse-utils";
import {
  conditionText,
  decodeConditions,
  encodeConditions,
  isValidCondition,
  parseNumber,
  toFilterState,
  type FactorCondition
} from "./factor-condition";

const all: FactorCondition[] = [
  { column: "rsi", op: "gt", value: 70 },
  { column: "rsi", op: "gte", value: 70.5 },
  { column: "atr", op: "lt", value: -1 },
  { column: "atr", op: "lte", value: 2 },
  { column: "ma", op: "eq", value: 0 },
  { column: "pctb", op: "range", min: 0, max: 1 }
];

describe("因子条件", () => {
  it("六种算子经 ?fc= 来回转换不变", () => {
    const allowed = new Set(all.map(item => item.column));
    expect(decodeConditions(encodeConditions(all), allowed)).toEqual({ conditions: all, dropped: 0 });
    expect(encodeConditions([])).toBe("");
  });

  it("转成 ViewBrowse 附加条件时都是 double，并合成单个“且”组", () => {
    const spec = buildViewFilterExprs(all.map(toFilterState));
    expect(spec?.groups).toHaveLength(1);
    expect(spec?.groups[0].conds.map(cond => [cond.column, cond.op, cond.values[0]])).toEqual([
      ["rsi", "FILTER_OP_GT", { double_value: 70 }],
      ["rsi", "FILTER_OP_GTE", { double_value: 70.5 }],
      ["atr", "FILTER_OP_LT", { double_value: -1 }],
      ["atr", "FILTER_OP_LTE", { double_value: 2 }],
      ["ma", "FILTER_OP_EQ", { double_value: 0 }],
      ["pctb", "FILTER_OP_GTE", { double_value: 0 }],
      ["pctb", "FILTER_OP_LTE", { double_value: 1 }]
    ]);
  });

  it("未知列、隐藏列、非法算子、非有限数字、区间倒置、重复条件都被丢弃", () => {
    const raw = JSON.stringify([
      ["rsi", "gt", 70],
      ["unknown", "gt", 1],
      ["rsi", "between", 1],
      ["rsi", "lt", "NaN"],
      ["rsi", "lt", "12abc"],
      ["rsi", "lt", null],
      ["pctb", "range", 1, 0],
      ["rsi", "gt", 70],
      "garbage"
    ]);
    expect(decodeConditions(raw, new Set(["rsi", "pctb"]))).toEqual({
      conditions: [{ column: "rsi", op: "gt", value: 70 }],
      dropped: 8
    });
    expect(decodeConditions("{not json", new Set(["rsi"]))).toEqual({ conditions: [], dropped: 1 });
    expect(decodeConditions(undefined, new Set(["rsi"]))).toEqual({ conditions: [], dropped: 0 });
  });

  it("数值整串解析，区间要求下限不大于上限", () => {
    expect(parseNumber(" 1e3 ")).toBe(1000);
    expect(parseNumber("12abc")).toBeUndefined();
    expect(parseNumber("")).toBeUndefined();
    expect(isValidCondition({ column: "x", op: "range", min: 1, max: 1 })).toBe(true);
    expect(isValidCondition({ column: "x", op: "range", min: 2, max: 1 })).toBe(false);
    expect(conditionText({ column: "x", op: "gte", value: 3 }, "rsi")).toBe("rsi ≥ 3");
    expect(conditionText({ column: "x", op: "range", min: 0, max: 1 })).toBe("x 区间 0～1");
  });
});
