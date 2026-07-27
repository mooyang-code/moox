export type CollectorRuleInput = {
  dataType: "kline" | "symbol";
  exchange: string;
  market: "spot" | "swap";
  datasetId: string;
  intervals: string[];
  scheduleInterval: string;
};

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

  const intervals = input.dataType === "kline" ? input.intervals.map(interval => interval.trim()).filter(Boolean) : [];
  if (input.dataType === "kline" && intervals.length === 0) {
    throw new Error("请选择 K线周期");
  }

  return {
    source:
      input.dataType === "kline"
        ? {
            kind: "dataset_subjects",
            dataset_id: datasetId
          }
        : {
            kind: "none"
          },
    collector: {
      exchange,
      market: input.market,
      data_type: input.dataType,
      intervals,
      live: false
    },
    target: {
      dataset_id: datasetId
    },
    schedule: {
      interval: scheduleInterval
    }
  };
}
