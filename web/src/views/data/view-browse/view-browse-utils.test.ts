import { describe, expect, it } from "vitest";
import { buildViewFilterExprs, buildViewFilterFieldOptions } from "./view-browse-utils";

const double = "FIELD_VALUE_TYPE_DOUBLE" as const;

describe("buildViewFilterExprs 数值比较", () => {
  it("四个比较算子与区间都生成 double_value，并与其他条件合成单个“且”组", () => {
    const spec = buildViewFilterExprs([
      { fieldName: "subject_id", operator: "eq", value: "BTC" },
      { fieldName: "rsi", operator: "gt", value: "70", valueType: double },
      { fieldName: "rsi", operator: "gte", value: "70.5", valueType: double },
      { fieldName: "atr", operator: "lt", value: "-1e-3", valueType: double },
      { fieldName: "atr", operator: "lte", value: "2", valueType: double },
      { fieldName: "boll_pctb", operator: "range", startValue: "0", endValue: "1", valueType: double }
    ]);
    expect(spec?.group_logical).toBe("FILTER_LOGICAL_AND");
    expect(spec?.groups).toHaveLength(1);
    expect(spec?.groups[0].logical).toBe("FILTER_LOGICAL_AND");
    expect(spec?.groups[0].conds).toEqual([
      { column: "subject_id", op: "FILTER_OP_EQ", values: [{ string_value: "BTC" }] },
      { column: "rsi", op: "FILTER_OP_GT", values: [{ double_value: 70 }] },
      { column: "rsi", op: "FILTER_OP_GTE", values: [{ double_value: 70.5 }] },
      { column: "atr", op: "FILTER_OP_LT", values: [{ double_value: -0.001 }] },
      { column: "atr", op: "FILTER_OP_LTE", values: [{ double_value: 2 }] },
      { column: "boll_pctb", op: "FILTER_OP_GTE", values: [{ double_value: 0 }] },
      { column: "boll_pctb", op: "FILTER_OP_LTE", values: [{ double_value: 1 }] }
    ]);
  });

  it("数值列的值不是完整的有限数字时整条丢弃", () => {
    expect(
      buildViewFilterExprs([
        { fieldName: "rsi", operator: "gt", value: "12abc", valueType: double },
        { fieldName: "rsi", operator: "eq", value: "NaN", valueType: double },
        { fieldName: "rsi", operator: "lt", value: "Infinity", valueType: double },
        { fieldName: "trade_num", operator: "eq", value: "1.5", valueType: "FIELD_VALUE_TYPE_INT" }
      ])
    ).toBeUndefined();
  });
});

describe("buildViewFilterFieldOptions 指定搜索列", () => {
  const columns = [
    { column_name: "close", value_type: double },
    { column_name: "rsi", value_type: double }
  ];

  it("未指定时为全部列，指定时只含指定列与系统键", () => {
    expect(buildViewFilterFieldOptions("time_series", columns, []).map(item => item.value)).toEqual([
      "subject_id",
      "freq",
      "series_tag",
      "data_time",
      "close",
      "rsi"
    ]);
    expect(buildViewFilterFieldOptions("time_series", columns, [], {}, ["close"]).map(item => item.value)).toEqual([
      "subject_id",
      "freq",
      "series_tag",
      "data_time",
      "close"
    ]);
  });
});
