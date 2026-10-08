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
            fields: [
              { field_id: "open" },
              { field_id: "bias_20" },
              { field_id: "cci" }
            ]
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
          fields: [{ field_id: "open" }, { field_id: "close" }]
        }
      ])
    ).toEqual(["open", "close"]);
  });
});

describe("buildViewFilterFieldOptions", () => {
  it("keeps search fields aligned with the table projection", () => {
    const labels = {
      open: "开盘价",
      close: "收盘价",
      amount: "成交额"
    };
    const options = buildViewFilterFieldOptions(
      "time_series",
      [
        { column_name: "open", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
        { column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE" }
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
      "open",
      "close"
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
    const labels = buildViewColumnLabels(
      [
        {
          column_name: "bias_5",
          origin_id: "bias_5",
          attributes: { display_name: "乖离率", factor_output: "bias_5" }
        } as ViewColumn
      ],
      [
        {
          dataset_id: "dataset_factor_btc_1m",
          column_name: "bias_5",
          origin_type: 2,
          origin_id: "Bias",
          attributes: { display_name: "bias_5", factor_output: "bias_5" }
        } as DatasetColumn
      ],
      [],
      { dataset_id: "dataset_factor_btc_1m" }
    );

    expect(labels.bias_5).toBe("bias_5");
  });

  it("labels a column from the View's own Dataset only", () => {
    const labels = buildViewColumnLabels(
      [{ column_name: "close", origin_id: "close" } as ViewColumn],
      [
        { dataset_id: "dataset_other", column_name: "close", attributes: { display_name: "其他收盘" } } as DatasetColumn,
        { dataset_id: "dataset_kline", column_name: "close", attributes: { display_name: "收盘价" } } as DatasetColumn
      ],
      [],
      { dataset_id: "dataset_kline" }
    );

    expect(labels.close).toBe("收盘价");
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
