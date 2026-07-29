import { describe, expect, it } from "vitest";
import { buildCollectorRuleParams, datasetMatchesCollector } from "./collector-rule-params";

describe("buildCollectorRuleParams", () => {
  it("builds the single dataset-driven Kline contract", () => {
    expect(
      buildCollectorRuleParams({
        dataType: "kline",
        exchange: "binance",
        market: "spot",
        datasetId: "spot_kline_1h",
        intervals: ["1h", "4h"],
        scheduleInterval: "1h"
      })
    ).toEqual({
      source: {
        kind: "dataset_subjects",
        dataset_id: "spot_kline_1h"
      },
      collector: {
        exchange: "binance",
        market: "spot",
        data_type: "kline",
        intervals: ["1h", "4h"],
        live: false
      },
      target: {
        dataset_id: "spot_kline_1h"
      },
      schedule: {
        interval: "1h"
      }
    });
  });

  it("builds a Symbol contract without collection intervals", () => {
    expect(
      buildCollectorRuleParams({
        dataType: "symbol",
        exchange: "binance",
        market: "spot",
        datasetId: "binance_spot_symbols",
        intervals: ["1m"],
        scheduleInterval: "6h"
      })
    ).toEqual({
      source: {
        kind: "none"
      },
      collector: {
        exchange: "binance",
        market: "spot",
        data_type: "symbol",
        intervals: [],
        live: false
      },
      target: {
        dataset_id: "binance_spot_symbols"
      },
      schedule: {
        interval: "6h"
      }
    });
  });

  it("rejects a missing Dataset instead of inferring its ID", () => {
    expect(() =>
      buildCollectorRuleParams({
        dataType: "kline",
        exchange: "binance",
        market: "spot",
        datasetId: " ",
        intervals: ["1m"],
        scheduleInterval: "5m"
      })
    ).toThrow("请选择 Dataset");
  });

  it("uses the selected market without changing the explicit Dataset", () => {
    const baseInput = {
      dataType: "kline" as const,
      exchange: "binance",
      datasetId: "shared_kline",
      intervals: ["1m"],
      scheduleInterval: "5m"
    };

    const spot = buildCollectorRuleParams({ ...baseInput, market: "spot" });
    const swap = buildCollectorRuleParams({ ...baseInput, market: "swap" });

    expect(spot).toMatchObject({
      collector: { market: "spot" },
      target: { dataset_id: "shared_kline" }
    });
    expect(swap).toMatchObject({
      collector: { market: "swap" },
      target: { dataset_id: "shared_kline" }
    });
  });
});

describe("datasetMatchesCollector", () => {
  it("requires both provider and data shape", () => {
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: "DATA_KIND_TIME_SERIES" }, "binance", "kline")).toBe(
      true
    );
    expect(datasetMatchesCollector({ data_source_id: "okx", data_kind: "DATA_KIND_TIME_SERIES" }, "binance", "kline")).toBe(
      false
    );
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: "DATA_KIND_RECORD" }, "binance", "kline")).toBe(false);
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: 1 }, "binance", "symbol")).toBe(true);
  });
});
