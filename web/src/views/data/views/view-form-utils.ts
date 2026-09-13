import type { Dataset, DatasetColumn, ViewColumn } from "@/api/storage/types";

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
  datasetId: string,
  columnsByDataset: Record<string, DatasetColumn[]>
): ViewColumn[] {
  const trimmed = datasetId.trim();
  if (!trimmed) return [];
  const seen = new Set<string>();
  const out: ViewColumn[] = [];
  const columns = (columnsByDataset[trimmed] || []).filter(item => !item.status || item.status === "active");
  for (const column of columns) {
    if (!column.column_name) continue;
    const columnName = `${trimmed}.${column.column_name}`;
    if (seen.has(columnName)) continue;
    seen.add(columnName);
    out.push({
      space_id: column.space_id,
      view_id: "",
      column_name: columnName,
      origin_type: "COLUMN_ORIGIN_TYPE_DATASET_COLUMN",
      origin_id: `${trimmed}.${column.column_name}`,
      value_type: column.value_type || "FIELD_VALUE_TYPE_STRING",
      sort_order: out.length + 1,
      attributes: {
        ...(column.attributes || {}),
        display_name: column.attributes?.display_name || "未命名"
      }
    });
  }
  return out;
}

function isTimeSeriesDataKind(value?: Dataset["data_kind"]) {
  return value === "DATA_KIND_TIME_SERIES" || value === 2;
}

function jsonText(value?: string) {
  return value?.trim() || "{}";
}
