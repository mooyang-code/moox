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
  attributes?: Record<string, string>;
  freqs?: string[];
};

export type CollectorRuleRecord = {
  id?: number;
  rule_id: string;
  space_id: string;
  data_type: string;
  data_source: string;
  collect_params: string;
  enabled: string;
  creator: string;
  create_time: string;
  modify_time: string;
};

export function datasetMatchesCollector(
  dataset: CollectorDatasetOption,
  exchange: string,
  dataType: string,
  marketType?: string,
  frequency?: string
): boolean {
  // K-line targets may be aggregate datasets (for example crypto_market)
  // fed by more than one provider; symbols remain provider-owned.
  if (dataType !== "kline" && dataset.data_source_id !== exchange) {
    return false;
  }
  const expected = dataType === "kline" ? ["DATA_KIND_TIME_SERIES", "time_series", 2] : ["DATA_KIND_RECORD", "record", 1];
  if (!expected.includes(dataset.data_kind)) return false;
  if (marketType && dataset.attributes?.market_type?.toLowerCase() !== marketType.toLowerCase()) return false;
  if (frequency) {
    const supportedFrequencies = (dataset.freqs || []).map(value => value.toLowerCase());
    if (!supportedFrequencies.includes(frequency.toLowerCase())) return false;
  }
  return true;
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

export function buildCollectorRuleRequest(
  input: CollectorRuleInput,
  spaceId: string,
  creator: string,
  enabled = true,
  ruleId?: string
): Record<string, unknown> {
  const params = buildCollectorRuleParams(input);
  return {
    ...(ruleId ? { rule_id: ruleId } : {}),
    space_id: spaceId,
    data_type: input.dataType,
    provider: input.exchange.trim().toLowerCase(),
    market_type: input.market,
    collect_params: params,
    enabled,
    creator
  };
}

export function normalizeCollectorRule(raw: any): CollectorRuleRecord {
  return {
    id: raw.id,
    rule_id: raw.rule_id || "",
    space_id: raw.space_id || "",
    data_type: raw.data_type || "",
    data_source: raw.provider || raw.data_source || raw.exchange || "",
    collect_params: JSON.stringify(normalizeJSONValue(raw.collect_params)),
    enabled: (raw.enabled ?? true) ? "true" : "false",
    creator: raw.creator || "",
    create_time: raw.create_time || "",
    modify_time: raw.modify_time || ""
  };
}

function normalizeJSONValue(value: any): Record<string, any> {
  if (!value) return {};
  if (typeof value === "object") return value;
  try {
    const parsed = JSON.parse(String(value));
    return parsed && typeof parsed === "object" ? parsed : { value: parsed };
  } catch {
    return { raw: String(value) };
  }
}
