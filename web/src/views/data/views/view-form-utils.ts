import type { Dataset, DatasetColumn, ViewColumn } from "@/api/storage/types";

export function buildViewDatasetIds(primaryDatasetId: string, includeDatasetIds: string[] = []) {
  const seen = new Set<string>();
  const out: string[] = [];
  const add = (datasetId?: string) => {
    const trimmed = datasetId?.trim();
    if (!trimmed || seen.has(trimmed)) return;
    seen.add(trimmed);
    out.push(trimmed);
  };
  add(primaryDatasetId);
  includeDatasetIds.forEach(add);
  return out;
}

export function availableIncludedDatasets(datasets: Dataset[], primaryDatasetId = "", selectedFreq = "") {
  const primary = datasets.find(item => item.dataset_id === primaryDatasetId);
  const freq = selectedFreq.trim();
  if (isTimeSeriesDataKind(primary?.data_kind)) {
    if (!freq) return [];
    return datasets.filter(item => isTimeSeriesDataKind(item.data_kind) && datasetSupportsFreq(item, freq));
  }
  return datasets;
}

export function freqOptionsForPrimaryDataset(datasets: Dataset[], primaryDatasetId: string) {
  const primary = datasets.find(item => item.dataset_id === primaryDatasetId);
  if (!isTimeSeriesDataKind(primary?.data_kind)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const freq of primary?.freqs || []) {
    const trimmed = freq.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    out.push(trimmed);
  }
  return out;
}

export function freqFromViewFilterJSON(filterJSON?: string) {
  try {
    const parsed = JSON.parse(jsonText(filterJSON)) as { freq?: unknown };
    return typeof parsed.freq === "string" ? parsed.freq.trim() : "";
  } catch {
    return "";
  }
}

export function buildTimeSeriesViewFilterJSON(filterJSON: string | undefined, freq: string) {
  const trimmedFreq = freq.trim();
  const parsed = JSON.parse(jsonText(filterJSON)) as Record<string, unknown>;
  parsed.freq = trimmedFreq;
  return JSON.stringify(parsed);
}

export function defaultViewGrainKeys(datasets: Dataset[], primaryDatasetId: string) {
  const primary = datasets.find(item => item.dataset_id === primaryDatasetId);
  if (isTimeSeriesDataKind(primary?.data_kind)) {
    return ["subject_id", "freq", "data_time"];
  }
  return ["record_id", "version"];
}

export function defaultViewEngine(datasets: Dataset[], primaryDatasetId: string) {
  const primary = datasets.find(item => item.dataset_id === primaryDatasetId);
  return isTimeSeriesDataKind(primary?.data_kind) ? "duckdb" : "bleve";
}

export function buildDraftViewColumns(
  primaryDatasetId: string,
  includeDatasetIds: string[],
  columnsByDataset: Record<string, DatasetColumn[]>
): ViewColumn[] {
  const datasetIds = buildViewDatasetIds(primaryDatasetId, includeDatasetIds);
  const seen = new Set<string>();
  const out: ViewColumn[] = [];
  for (const datasetId of datasetIds) {
    const columns = (columnsByDataset[datasetId] || []).filter(item => !item.status || item.status === "active");
    for (const column of columns) {
      if (!column.column_name) continue;
      const columnName = `${datasetId}.${column.column_name}`;
      if (seen.has(columnName)) continue;
      seen.add(columnName);
      out.push({
        space_id: column.space_id,
        view_id: "",
        column_name: columnName,
        origin_type: "COLUMN_ORIGIN_TYPE_DATASET_COLUMN",
        origin_id: `${datasetId}.${column.column_name}`,
        value_type: column.value_type || "FIELD_VALUE_TYPE_STRING",
        sort_order: out.length + 1,
        attributes: {
          ...(column.attributes || {}),
          display_name: column.attributes?.display_name || "未命名"
        }
      });
    }
  }
  return out;
}

export function removePrimaryFromIncludes(primaryDatasetId: string, includeDatasetIds: string[] = []) {
  return includeDatasetIds.filter(datasetId => datasetId !== primaryDatasetId);
}

function datasetSupportsFreq(dataset: Dataset, freq: string) {
  return (dataset.freqs || []).some(item => item.trim() === freq);
}

function isTimeSeriesDataKind(value?: Dataset["data_kind"]) {
  return value === "DATA_KIND_TIME_SERIES" || value === 2;
}

function jsonText(value?: string) {
  return value?.trim() || "{}";
}
