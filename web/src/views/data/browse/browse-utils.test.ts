import { describe, expect, it } from "vitest";
import type { DatasetColumn, ViewColumn } from "@/api/storage/types";
import { buildTimeSeriesBrowseSelector, sortBrowseTableRows, timeSeriesRowsToTableRows } from "./browse-utils";
import {
  buildKlineChartRecords,
  buildViewColumnLabels,
  buildViewFilterExprs,
  exactSeriesTagFromFilters
} from "../view-browse/view-browse-utils";

describe("timeSeriesRowsToTableRows", () => {
  it("keeps rows with the same timestamp distinct by series tag", () => {
    const rows = timeSeriesRowsToTableRows([
      {
        key: {
          space_id: "crypto",
          dataset_id: "dataset_spot_kline_1h",
          subject_id: "BTC-USDT",
          freq: "1H",
          data_time: "2026-07-29T00:00:00Z",
          series_tag: "venue:binance"
        }
      },
      {
        key: {
          space_id: "crypto",
          dataset_id: "dataset_spot_kline_1h",
          subject_id: "BTC-USDT",
          freq: "1H",
          data_time: "2026-07-29T00:00:00Z",
          series_tag: "venue:okx"
        }
      }
    ]);

    expect(rows[0].id).not.toBe(rows[1].id);
    expect(rows[0].id).toContain("venue:binance");
    expect(rows[1].id).toContain("venue:okx");
    expect(rows.map(row => row.seriesTag)).toEqual(["venue:binance", "venue:okx"]);
    expect(sortBrowseTableRows(rows, "series_tag", "desc").map(row => row.seriesTag)).toEqual(["venue:okx", "venue:binance"]);
    expect(buildTimeSeriesBrowseSelector("crypto", "dataset_spot_kline_1h", "BTC-USDT", "1H", " venue:okx ", true)).toEqual({
      space_id: "crypto",
      dataset_id: "dataset_spot_kline_1h",
      subject_id: "BTC-USDT",
      freq: "1H",
      series_tag: "venue:okx"
    });
    expect(buildTimeSeriesBrowseSelector("stockcn", "dataset_stockcn_equity_kline", "sh600000", "1D", "", true)).toEqual({
      space_id: "stockcn",
      dataset_id: "dataset_stockcn_equity_kline",
      subject_id: "sh600000",
      freq: "1D",
      series_tag: "default"
    });
    expect(buildTimeSeriesBrowseSelector("crypto", "dataset_spot_kline_1h", "BTC-USDT", "1H", "", false)).not.toHaveProperty(
      "series_tag"
    );
  });
});

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
      [],
      { primary_dataset_id: "bin_e0a2079753cf4faf", dataset_ids: ["bin_e0a2079753cf4faf"] }
    );

    expect(labels[columnName]).toBe("bias_5");
  });
});
