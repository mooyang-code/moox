import { describe, expect, it } from "vitest";

import type { DatasetColumn, ViewColumn } from "@/api/storage/types";

import { rowsToColumnNames } from "./browse-utils";
import { buildKlineChartRecords, buildViewColumnLabels, buildViewFilterFieldOptions, buildViewFilterExprs, exactSeriesTagFromFilters, viewModeFromPrimaryDataset } from "../view-browse/view-browse-utils";

describe("Kline series tag isolation", () => {
  it("requires an exact tag and never overwrites another tag at the same timestamp", () => {
    const rows = [
      {
        key: "BTC-USDT",
        version: "2026-07-29T00:00:00Z",
        freq: "1H",
        seriesTag: "venue:binance",
        values: { open: "100", high: "110", low: "90", close: "105" }
      },
      {
        key: "BTC-USDT",
        version: "2026-07-29T00:00:00Z",
        freq: "1H",
        seriesTag: "venue:okx",
        values: { open: "200", high: "210", low: "190", close: "205" }
      },
      {
        key: "sh600000",
        version: "2026-07-29T00:00:00Z",
        freq: "1D",
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
            fields: [
              { field_id: "dataset_binance_kline_1m.open" },
              { field_id: "dataset_binance_kline_1m.bias__bias_20" },
              { field_id: "dataset_binance_kline_1m.cci__cci" }
            ]
          }
        ],
        ["dataset_binance_kline_1m.open", "dataset_binance_kline_1m.close"]
      )
    ).toEqual(["dataset_binance_kline_1m.open", "dataset_binance_kline_1m.close"]);
  });

  it("discovers columns from rows only when no projection is declared", () => {
    expect(
      rowsToColumnNames([
        {
          fields: [{ field_id: "open" }, { field_id: "close" }]
        }
      ])
    ).toEqual(["open", "close"]);
  });
});

describe("buildViewFilterFieldOptions", () => {
  it("keeps search fields aligned with the table projection", () => {
    const labels = {
      "dataset_binance_kline_1m.open": "开盘价",
      "dataset_binance_kline_1m.close": "收盘价",
      amount: "成交额"
    };
    const options = buildViewFilterFieldOptions(
      "time_series",
      [
        { column_name: "dataset_binance_kline_1m.open", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
        { column_name: "dataset_binance_kline_1m.close", value_type: "FIELD_VALUE_TYPE_DOUBLE" }
      ],
      [
        { column_name: "open", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
        { column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
        { column_name: "amount", value_type: "FIELD_VALUE_TYPE_DOUBLE" }
      ],
      labels
    );

    expect(options.map(item => item.value)).toEqual([
      "subject_id",
      "freq",
      "series_tag",
      "data_time",
      "dataset_binance_kline_1m.open",
      "dataset_binance_kline_1m.close"
    ]);
    expect(options.map(item => item.label)).not.toContain("成交额");
  });

  it("falls back to dataset columns only when the view has no projection", () => {
    const options = buildViewFilterFieldOptions(
      "time_series",
      [],
      [{ column_name: "open", value_type: "FIELD_VALUE_TYPE_DOUBLE" }],
      { open: "开盘价" }
    );
    expect(options.map(item => item.value)).toEqual(["subject_id", "freq", "series_tag", "data_time", "open"]);
  });
});

describe("view factor column labels", () => {
  it("uses the factor output name stored in result metadata", () => {
    const columnName = "bin_e0a2079753cf4faf.bias__bias_5";
    const labels = buildViewColumnLabels(
      [
        {
          column_name: columnName,
          origin_id: columnName,
          attributes: { display_name: "bias_5", factor_output: "bias_5" }
        } as ViewColumn
      ],
      [
        {
          dataset_id: "bin_e0a2079753cf4faf",
          column_name: "bias__bias_5",
          origin_type: 2,
          origin_id: "bias.bias_5",
          attributes: { display_name: "bias_5", factor_output: "bias_5" }
        } as DatasetColumn
      ],
      [],
      [],
      { dataset_id: "bin_e0a2079753cf4faf" }
    );

    expect(labels[columnName]).toBe("bias_5");
  });
});

describe("viewBoundDatasetId", () => {

  it("treats SQL time_series data_kind as a time-series dataset", () => {
    expect(
      viewModeFromPrimaryDataset(
        [{ dataset_id: "dataset_binance_kline_1m", data_kind: "time_series" as never }],
        "dataset_binance_kline_1m"
      )
    ).toBe("time_series");
  });
});
