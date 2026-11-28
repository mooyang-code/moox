import { describe, expect, it } from "vitest";

import {
  buildKlineChartRecords,
  buildColumnLabels,
  buildViewFilterExprs,
  buildViewFilterFieldOptions,
  defaultSeriesTag,
  exactSeriesTagFromFilters,
  rowsToColumnNames,
  timeSeriesRowsToTableRows,
  typedValueText,
  viewModeFromDataset
} from "./view-browse-utils";

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
    expect(buildViewFilterFieldOptions("time_series", columns).map(item => item.value)).toEqual([
      "subject_id",
      "freq",
      "series_tag",
      "data_time",
      "close",
      "rsi"
    ]);
    expect(buildViewFilterFieldOptions("time_series", columns, {}, ["close"]).map(item => item.value)).toEqual([
      "subject_id",
      "freq",
      "series_tag",
      "data_time",
      "close"
    ]);
  });
});

describe("defaultSeriesTag", () => {
  it("当前结果只有一种序列标签时用它，有多种或没有结果时不填", () => {
    expect(defaultSeriesTag([{ seriesTag: "default" }, { seriesTag: "default" }])).toBe("default");
    expect(defaultSeriesTag([{ seriesTag: "market:spot" }, { seriesTag: "market:swap" }])).toBe("");
    expect(defaultSeriesTag([])).toBe("");
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

describe("Kline series tag isolation", () => {
  it("requires an exact tag and never overwrites another tag at the same timestamp", () => {
    const rows = [
      {
        key: "BTC-USDT",
        version: "2026-07-29T00:00:00Z",
        freq: "1h",
        seriesTag: "venue:binance",
        values: { open: "100", high: "110", low: "90", close: "105" }
      },
      {
        key: "BTC-USDT",
        version: "2026-07-29T00:00:00Z",
        freq: "1h",
        seriesTag: "venue:okx",
        values: { open: "200", high: "210", low: "190", close: "205" }
      },
      {
        key: "sh600000",
        version: "2026-07-29T00:00:00Z",
        freq: "1d",
        seriesTag: "",
        values: { open: "10", high: "11", low: "9", close: "10.5" }
      }
    ];

    expect(buildKlineChartRecords(rows, "sh600000", "")).toEqual([expect.objectContaining({ open: 10, close: 10.5 })]);
    expect(buildKlineChartRecords(rows, "BTC-USDT", "venue:binance")).toEqual([
      expect.objectContaining({ open: 100, close: 105 })
    ]);
    expect(buildKlineChartRecords(rows, "BTC-USDT", "venue:okx")).toEqual([expect.objectContaining({ open: 200, close: 205 })]);
    expect(exactSeriesTagFromFilters([{ fieldName: "series_tag", operator: "eq", value: "venue:binance" }])).toBe(
      "venue:binance"
    );
    expect(exactSeriesTagFromFilters([{ fieldName: "series_tag", operator: "contains", value: "binance" }])).toBeUndefined();
    expect(exactSeriesTagFromFilters([{ fieldName: "series_tag", operator: "empty" }])).toBe("");
    expect(buildViewFilterExprs([{ fieldName: "series_tag", operator: "empty" }])).toEqual({
      groups: [
        {
          conds: [{ column: "series_tag", op: "FILTER_OP_EQ", values: [{ string_value: "" }] }],
          logical: "FILTER_LOGICAL_AND"
        }
      ],
      group_logical: "FILTER_LOGICAL_AND"
    });
  });
});

describe("rowsToColumnNames", () => {
  it("keeps an explicit projection and ignores leftover row fields", () => {
    expect(
      rowsToColumnNames(
        [
          {
            key: { space_id: "", dataset_id: "" },
            fields: [{ field_id: "open" }, { field_id: "bias_20" }, { field_id: "cci" }]
          }
        ],
        ["open", "close"]
      )
    ).toEqual(["open", "close"]);
  });

  it("discovers columns from rows only when no projection is declared", () => {
    expect(
      rowsToColumnNames([
        {
          key: { space_id: "", dataset_id: "" },
          fields: [{ field_id: "open" }, { field_id: "close" }]
        }
      ])
    ).toEqual(["open", "close"]);
  });
});

describe("buildViewFilterFieldOptions", () => {
  it("只为数据集的列生成搜索项，标题用给出的列标题", () => {
    const options = buildViewFilterFieldOptions(
      "time_series",
      [
        { column_name: "open", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
        { column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE" }
      ],
      { open: "开盘价", close: "收盘价" }
    );
    expect(options.map(item => item.value)).toEqual(["subject_id", "freq", "series_tag", "data_time", "open", "close"]);
    expect(options.map(item => item.label)).toContain("开盘价");
  });
});

describe("buildColumnLabels", () => {
  it("因子输出列用技术列名，其余用列自己的显示名", () => {
    const labels = buildColumnLabels([
      { column_name: "bias_5", display_name: "乖离率", attributes: { column_kind: "output", member_key: "bias__5" } },
      { column_name: "close", display_name: "收盘价" },
      { column_name: "data_time", display_name: "" }
    ]);
    expect(labels).toEqual({ bias_5: "bias_5", close: "收盘价", data_time: "时间" });
  });
});

describe("viewModeFromDataset", () => {
  it("treats SQL time_series data_kind as a time-series dataset", () => {
    expect(viewModeFromDataset({ data_kind: "time_series" as never })).toBe("time_series");
    expect(viewModeFromDataset({ data_kind: "DATA_KIND_RECORD" })).toBe("record");
    expect(viewModeFromDataset(undefined)).toBe("missing");
  });
});

describe("时间值的显示", () => {
  it("给了时区时 TIME 字段按它显示并带后缀，没给时原样", () => {
    const value = { time_value: "2026-11-07T01:30:00Z" };
    expect(typedValueText(value, "Asia/Shanghai")).toBe("2026-11-07 09:30:00 UTC+8");
    expect(typedValueText(value)).toBe("2026-11-07T01:30:00Z");
  });

  it("时序行的 version 保留原始 data_time，字段里的 TIME 值按时区", () => {
    const [row] = timeSeriesRowsToTableRows(
      [
        {
          key: {
            space_id: "crypto",
            dataset_id: "d",
            time_series: { subject_id: "BTC", freq: "1m", series_tag: "", data_time: "2026-11-07T01:30:00Z" }
          },
          fields: [{ field_id: "close_time", value: { time_value: "2026-11-07T01:31:00Z" } }]
        }
      ] as never,
      "UTC"
    );
    expect(row.version).toBe("2026-11-07T01:30:00Z");
    expect(row.values.close_time).toBe("2026-11-07 01:31:00 UTC");
  });
});
