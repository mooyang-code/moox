import type { FieldValue, RecordRow, TimeSeriesRow, TypedValue } from "@/api/storage/types";

export interface BrowseTableRow {
  id: string;
  key: string;
  version: string;
  seriesTag?: string;
  values: Record<string, string>;
}

const minAdaptiveColumnWidth = 112;
const maxAdaptiveColumnWidth = 320;

export function adaptiveColumnWidth(columnName: string, label: string, rows: Array<Pick<BrowseTableRow, "values">>) {
  const headerWidth = visualTextWidth(label || columnName);
  const valueWidth = rows.reduce((maxWidth, row) => {
    const value = row.values?.[columnName];
    return Math.max(maxWidth, visualTextWidth(value || ""));
  }, 0);
  const rawWidth = Math.max(headerWidth, valueWidth) + 48;
  return clamp(roundUp(rawWidth, 8), minAdaptiveColumnWidth, maxAdaptiveColumnWidth);
}

function containsCJK(value: string) {
  return /[\u3400-\u9fff]/.test(value);
}

function visualTextWidth(value: string) {
  return Array.from(value).reduce((sum, char) => sum + (containsCJK(char) ? 14 : 8), 0);
}

function roundUp(value: number, step: number) {
  return Math.ceil(value / step) * step;
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

export function fieldValueText(field?: FieldValue) {
  if (!field?.value) return "-";
  return typedValueText(field.value);
}

export function typedValueText(value?: TypedValue): string {
  if (!value) return "-";
  if (value.string_value !== undefined) return value.string_value;
  if (value.int_value !== undefined) return String(value.int_value);
  if (value.double_value !== undefined) return String(value.double_value);
  if (value.bool_value !== undefined) return value.bool_value ? "true" : "false";
  if (value.time_value !== undefined) return value.time_value;
  if (value.json_value !== undefined) return value.json_value;
  if (value.bytes_value !== undefined) return value.bytes_value;
  if (value.list_value?.values) return value.list_value.values.map(item => typedValueText(item)).join(", ");
  return "-";
}

export function rowsToColumnNames(rows: Array<TimeSeriesRow | RecordRow>, preferred: string[] = []) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const name of preferred) {
    if (!name || seen.has(name)) continue;
    seen.add(name);
    out.push(name);
  }
  // A declared View/Dataset projection is the table schema. Do not append
  // leftover field_ids from the live index (for example retired factor outputs).
  if (out.length > 0) return out;
  for (const row of rows) {
    for (const field of row.fields || []) {
      if (!field.field_id || seen.has(field.field_id)) continue;
      seen.add(field.field_id);
      out.push(field.field_id);
    }
  }
  return out;
}

export function timeSeriesRowsToTableRows(rows: TimeSeriesRow[]): BrowseTableRow[] {
  return rows.map(row => ({
    id: `ts-${row.key?.subject_id || ""}-${row.key?.freq || ""}-${row.key?.series_tag || ""}-${row.key?.data_time || ""}`,
    key: row.key?.subject_id || "-",
    version: row.key?.data_time || "-",
    seriesTag: row.key?.series_tag || "",
    values: fieldsToValueMap(row.fields || [])
  }));
}

export function recordRowsToTableRows(rows: RecordRow[]): BrowseTableRow[] {
  return rows.map((row, index) => ({
    id: `record-${index}-${row.key?.record_id || ""}-${row.key?.version || ""}`,
    key: row.key?.record_id || "-",
    version: row.key?.version || "-",
    values: fieldsToValueMap(row.fields || [])
  }));
}

function fieldsToValueMap(fields: FieldValue[]) {
  const out: Record<string, string> = {};
  for (const field of fields) {
    out[field.field_id] = fieldValueText(field);
  }
  return out;
}
