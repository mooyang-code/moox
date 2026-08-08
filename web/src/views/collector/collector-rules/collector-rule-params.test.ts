import { describe, expect, it } from "vitest";
import { buildCollectorRuleParams, buildCollectorRuleRequest, datasetMatchesCollector, normalizeCollectorRule } from "./collector-rule-params";

describe("buildCollectorRuleParams", () => {
  it("builds the dataset-driven Kline contract", () => {
    expect(
      buildCollectorRuleParams({
        dataType: "kline",
        exchange: "binance",
        market: "spot",
        datasetId: "spot_kline_1h",
        symbolDatasetId: "binance_spot_symbols",
        scheduleInterval: "1h"
      })
    ).toEqual({
      provider: "binance",
      market_type: "spot",
      symbol_source: "dataset",
      symbol_dataset_id: "binance_spot_symbols",
      target_dataset_id: "spot_kline_1h",
      frequency: "1h"
    });
  });

  it("builds a full exchange Symbol snapshot contract", () => {
    expect(
      buildCollectorRuleParams({
        dataType: "symbol",
        exchange: "binance",
        market: "spot",
        datasetId: "binance_spot_symbols",
        scheduleInterval: "6h"
      })
    ).toEqual({
      provider: "binance",
      market_type: "spot",
      symbol_source: "exchange",
      target_dataset_id: "binance_spot_symbols",
      frequency: "6h"
    });
  });

  it("rejects a missing Dataset instead of inferring its ID", () => {
    expect(() =>
      buildCollectorRuleParams({
        dataType: "kline",
        exchange: "binance",
        market: "spot",
        datasetId: " ",
        symbolDatasetId: "symbols",
        scheduleInterval: "5m"
      })
    ).toThrow("请选择 Dataset");
  });

  it("uses the selected market without changing the explicit Dataset", () => {
    const baseInput = {
      dataType: "kline" as const,
      exchange: "binance",
      datasetId: "shared_kline",
      symbolDatasetId: "symbols",
      scheduleInterval: "5m"
    };

    const spot = buildCollectorRuleParams({ ...baseInput, market: "spot" });
    const swap = buildCollectorRuleParams({ ...baseInput, market: "swap" });

    expect(spot).toMatchObject({
      market_type: "spot",
      target_dataset_id: "shared_kline"
    });
    expect(swap).toMatchObject({
      market_type: "swap",
      target_dataset_id: "shared_kline"
    });
  });
});

describe("collector rule RPC contract", () => {
  it("emits provider and market_type instead of exchange", () => {
    const request = buildCollectorRuleRequest(
      {
        dataType: "kline",
        exchange: "binance",
        market: "spot",
        datasetId: "binance_spot_kline_1m",
        symbolDatasetId: "binance_spot_symbols",
        scheduleInterval: "1m"
      },
      "crypto_market",
      "admin"
    );
    expect(request).toMatchObject({ space_id: "crypto_market", data_type: "kline", provider: "binance", market_type: "spot" });
    expect(request).not.toHaveProperty("exchange");
  });

  it("normalizes provider responses for the list page", () => {
    expect(normalizeCollectorRule({ provider: "binance", collect_params: { provider: "binance" } }).data_source).toBe("binance");
  });
});

describe("datasetMatchesCollector", () => {
  it("accepts provider-owned symbols and aggregate kline targets", () => {
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: "DATA_KIND_TIME_SERIES" }, "binance", "kline")).toBe(
      true
    );
    expect(datasetMatchesCollector({ data_source_id: "crypto_market", data_kind: "DATA_KIND_TIME_SERIES" }, "binance", "kline")).toBe(true);
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: "DATA_KIND_RECORD" }, "binance", "kline")).toBe(false);
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: 1 }, "binance", "symbol")).toBe(true);
  });
});
