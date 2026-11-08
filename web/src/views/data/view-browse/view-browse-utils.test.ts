import { describe, expect, it } from "vitest";
import { buildViewFilterExprs, buildViewFilterFieldOptions, defaultSeriesTagForView } from "./view-browse-utils";

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

describe("defaultSeriesTagForView", () => {
  it("A股空间的 View 默认 series_tag 为 default，其他空间留空", () => {
    expect(defaultSeriesTagForView({ space_id: "stockcn" })).toBe("default");
    expect(defaultSeriesTagForView({ space_id: "crypto" })).toBe("");
    expect(defaultSeriesTagForView(undefined)).toBe("");
  });
});

describe("buildViewFilterExprs 时间列", () => {
  const time = "FIELD_VALUE_TYPE_TIME" as const;
  const conds = (value: string, zone: string) =>
    buildViewFilterExprs([{ fieldName: "data_time", operator: "gte", value, valueType: time }], zone)?.groups[0].conds;

  it("墙钟按所给时区解析，提交 UTC 的 RFC3339", () => {
    expect(conds("2026-11-07 09:30", "Asia/Shanghai")).toEqual([
      { column: "data_time", op: "FILTER_OP_GTE", values: [{ time_value: "2026-11-07T01:30:00Z" }] }
    ]);
    expect(conds("2026-11-07", "UTC")?.[0].values).toEqual([{ time_value: "2026-11-07T00:00:00Z" }]);
  });

  it("自带偏移的时间按它自己的偏移；看不懂的值不加条件", () => {
    expect(conds("2026-11-07T09:30:00+08:00", "UTC")?.[0].values).toEqual([{ time_value: "2026-11-07T01:30:00Z" }]);
    expect(conds("昨天", "UTC")).toBeUndefined();
    // 纯数字不当成毫秒
    expect(conds("20261107", "UTC")).toBeUndefined();
  });
});

describe("记录模式的版本列", () => {
  it("版本是任意字符串（1、v1），原样显示，不按时间格式化", async () => {
    const { readFileSync } = await import("node:fs");
    const { resolve } = await import("node:path");
    const page = readFileSync(resolve(__dirname, "index.vue"), "utf8");
    const recordColumn = page.slice(page.lastIndexOf('<a-table-column data-index="version"'), page.indexOf(">版本<"));
    expect(recordColumn).not.toContain("formatInstant");
    const timeColumn = page.slice(page.indexOf('<a-table-column data-index="version"'), page.indexOf(">时间<"));
    expect(timeColumn).toContain("formatInstant(record.version, zone)");
  });
});
