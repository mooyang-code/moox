import { describe, expect, it } from "vitest";
import {
  buildCollectorRuleParams,
  buildCollectorRuleRequest,
  datasetMatchesCollector,
  normalizeCollectorRule
} from "./collector-rule-params";

describe("buildCollectorRuleParams", () => {
	it("builds a kline resample contract from a source Dataset", () => {
		expect(buildCollectorRuleParams({ dataType: "kline_resample", exchange: "moox", market: "spot", datasetId: "dataset_spot_kline_derived_5m", scheduleInterval: "5m", sourceDatasetId: "dataset_binance_spot_kline_1m", sourceFrequency: "1m", sourceSeriesTag: "venue:binance" })).toMatchObject({ source_dataset_id: "dataset_binance_spot_kline_1m", target_dataset_id: "dataset_spot_kline_derived_5m", target_frequency: "5m", alignment: "epoch_utc" });
	});

	it("preserves an explicit resample settle delay, including zero", () => {
		const base = { dataType: "kline_resample" as const, exchange: "moox", market: "spot" as const, datasetId: "dataset_spot_kline_derived_5m", scheduleInterval: "5m", sourceDatasetId: "dataset_binance_spot_kline_1m", sourceFrequency: "1m", sourceSeriesTag: "venue:binance" };
		expect(buildCollectorRuleParams({ ...base, settleDelayMS: 2500 })).toMatchObject({ settle_delay_ms: 2500 });
		expect(buildCollectorRuleParams({ ...base, settleDelayMS: 0 })).toMatchObject({ settle_delay_ms: 0 });
		expect(buildCollectorRuleParams(base)).not.toHaveProperty("settle_delay_ms");
	});
  it("builds the dataset-driven Kline contract", () => {
    expect(
      buildCollectorRuleParams({
        dataType: "kline",
        exchange: "binance",
        market: "spot",
        datasetId: "dataset_spot_kline_1h",
        symbolDatasetId: "dataset_binance_spot_symbols",
        scheduleInterval: "1h"
      })
    ).toEqual({
      provider: "binance",
      market_type: "spot",
      symbol_source: "dataset",
      symbol_dataset_id: "dataset_binance_spot_symbols",
      target_dataset_id: "dataset_spot_kline_1h",
      frequency: "1h"
    });
  });

  it("builds a full exchange Symbol snapshot contract", () => {
    expect(
      buildCollectorRuleParams({
        dataType: "symbol",
        exchange: "binance",
        market: "spot",
        datasetId: "dataset_binance_spot_symbols",
        scheduleInterval: "6h"
      })
    ).toEqual({
      provider: "binance",
      market_type: "spot",
      symbol_source: "exchange",
      target_dataset_id: "dataset_binance_spot_symbols",
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
        datasetId: "dataset_binance_spot_kline_1m",
        symbolDatasetId: "dataset_binance_spot_symbols",
        scheduleInterval: "1m"
      },
      "crypto",
      "admin"
    );
    expect(request).toMatchObject({ space_id: "crypto", data_type: "kline", provider: "binance", market_type: "spot" });
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
    expect(
      datasetMatchesCollector({ data_source_id: "crypto", data_kind: "DATA_KIND_TIME_SERIES" }, "binance", "kline")
    ).toBe(true);
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: "DATA_KIND_RECORD" }, "binance", "kline")).toBe(false);
    expect(datasetMatchesCollector({ data_source_id: "binance", data_kind: 1 }, "binance", "symbol")).toBe(true);
  });

  it("filters datasets by market type and requested frequency", () => {
    const spotBars = {
      data_source_id: "crypto",
      data_kind: "DATA_KIND_TIME_SERIES",
      attributes: { market_type: "spot" },
      freqs: ["1H"]
    };
    expect(datasetMatchesCollector(spotBars, "binance", "kline", "spot", "1h")).toBe(true);
    expect(datasetMatchesCollector(spotBars, "binance", "kline", "swap", "1h")).toBe(false);
    expect(datasetMatchesCollector(spotBars, "binance", "kline", "spot", "4h")).toBe(false);
    expect(
      datasetMatchesCollector({ ...spotBars, data_source_id: "binance", attributes: {} }, "binance", "kline", "spot", "1h")
    ).toBe(false);
  });

  it("accepts an active time-series Dataset as a resample source", () => {
    const source = { data_source_id: "crypto", data_kind: "DATA_KIND_TIME_SERIES", attributes: { market_type: "spot" }, freqs: ["1H"] };
    expect(datasetMatchesCollector(source, "moox", "kline_resample", "spot", "1h")).toBe(true);
    expect(datasetMatchesCollector({ ...source, data_kind: "DATA_KIND_RECORD" }, "moox", "kline_resample", "spot", "1h")).toBe(false);
  });

  it("does not treat month M as minute m", () => {
    const monthly = { data_source_id: "crypto", data_kind: "DATA_KIND_TIME_SERIES", attributes: { market_type: "spot" }, freqs: ["1M"] };
    expect(datasetMatchesCollector(monthly, "moox", "kline_resample", "spot", "1m")).toBe(false);
    expect(datasetMatchesCollector(monthly, "moox", "kline", "spot", "1M")).toBe(true);
  });
});
