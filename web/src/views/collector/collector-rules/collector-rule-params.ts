export type CollectorRuleInput = {
  dataType: "kline" | "symbol";
  exchange: string;
  market: "spot" | "swap";
  datasetId: string;
  symbolDatasetId?: string;
  scheduleInterval: string;
};

export type CollectorDatasetOption = {
  data_source_id: string;
  data_kind: string | number;
};

export function datasetMatchesCollector(dataset: CollectorDatasetOption, exchange: string, dataType: string): boolean {
  if (dataset.data_source_id !== exchange) {
    return false;
  }
  const expected = dataType === "kline" ? ["DATA_KIND_TIME_SERIES", "time_series", 2] : ["DATA_KIND_RECORD", "record", 1];
  return expected.includes(dataset.data_kind);
}

export function buildCollectorRuleParams(input: CollectorRuleInput): Record<string, unknown> {
  const datasetId = input.datasetId.trim();
  if (!datasetId) {
    throw new Error("请选择 Dataset");
  }

  const exchange = input.exchange.trim().toLowerCase();
  if (!exchange) {
    throw new Error("请选择数据源");
  }

  const scheduleInterval = input.scheduleInterval.trim();
  if (!scheduleInterval) {
    throw new Error("请输入采集频率");
  }

  if (input.dataType === "symbol") {
    return {
      provider: exchange,
      market_type: input.market,
      symbol_source: "exchange",
      target_dataset_id: datasetId,
      frequency: scheduleInterval
    };
  }

  const symbolDatasetId = input.symbolDatasetId?.trim();
  if (!symbolDatasetId) {
    throw new Error("请选择标的 Dataset");
  }
  return {
    provider: exchange,
    market_type: input.market,
    symbol_source: "dataset",
    symbol_dataset_id: symbolDatasetId,
    target_dataset_id: datasetId,
    frequency: scheduleInterval
  };
}
