import type {
  Dataset,
  DatasetColumn,
  Field,
  FieldValue,
  FieldValueType,
  FilterCond,
  FilterSpec,
  RecordRow,
  SortSpec,
  TimeSeriesRow,
  TypedValue,
  View,
  ViewColumn
} from "@/api/storage/types";
import { formatInstant, toInstantMs, wallClockToMs } from "@/utils/time-zone";

export type ViewBrowseMode = "none" | "time_series" | "record" | "missing";
export type ViewSortDirection = "" | "asc" | "desc";
export type ViewFilterOperator =
  | "like"
  | "prefix"
  | "suffix"
  | "eq"
  | "neq"
  | "contains"
  | "not_contains"
  | "range"
  | "empty"
  | "not_empty"
  // 数值比较只供调用方附加的条件使用（例如因子条件），通用页的算子下拉不提供。
  | "gt"
  | "gte"
  | "lt"
  | "lte";

export interface ViewSortState {
  fieldName: string;
  direction: ViewSortDirection;
}

export interface ViewFilterState {
  fieldName: string;
  operator: ViewFilterOperator;
  value?: string;
  startValue?: string;
  endValue?: string;
  valueType?: FieldValueType;
}

export interface KlineTableRow {
  key: string;
  version: string;
  freq?: string;
  seriesTag?: string;
  values: Record<string, string>;
}

export interface KlineChartRecord {
  timestamp: number;
  open: number;
  high: number;
  low: number;
  close: number;
  volume?: number;
}

export const DEFAULT_KLINE_LIMIT = 200;
export const MIN_KLINE_LIMIT = 1;
export const MAX_KLINE_LIMIT = 5000;

const systemViewLabels: Record<string, string> = {
  subject_id: "数据ID",
  record_id: "记录ID",
  freq: "频率",
  data_time: "时间",
  version: "版本"
};

export type ViewFilterFieldOption = {
  value: string;
  label: string;
  valueType: FieldValueType;
};

/** 逐列搜索项：系统键加业务列；传了 onlyColumns 时业务列只取其中的列（系统键始终保留）。 */
export function buildViewFilterFieldOptions(
  mode: ViewBrowseMode,
  viewColumns: Array<Pick<ViewColumn, "column_name" | "value_type">>,
  datasetColumns: Array<Pick<DatasetColumn, "column_name" | "value_type">>,
  labels: Record<string, string> = {},
  onlyColumns?: string[]
): ViewFilterFieldOption[] {
  const options: ViewFilterFieldOption[] = [];
  const seen = new Set<string>();
  const push = (value: string, label: string, valueType: FieldValueType) => {
    if (!value || seen.has(value)) return;
    seen.add(value);
    options.push({ value, label, valueType });
  };
  if (mode === "time_series") {
    push("subject_id", "数据ID", "FIELD_VALUE_TYPE_STRING");
    push("freq", "频率", "FIELD_VALUE_TYPE_STRING");
    push("series_tag", "序列标签", "FIELD_VALUE_TYPE_STRING");
    push("data_time", "时间", "FIELD_VALUE_TYPE_TIME");
  } else if (mode === "record") {
    push("record_id", "记录ID", "FIELD_VALUE_TYPE_STRING");
    push("version", "版本", "FIELD_VALUE_TYPE_STRING");
  }
  const projected = viewColumns.some(column => column.column_name) ? viewColumns : datasetColumns;
  const only = onlyColumns ? new Set(onlyColumns) : undefined;
  for (const column of projected) {
    if (!column.column_name || (only && !only.has(column.column_name))) continue;
    push(column.column_name, labels[column.column_name] || column.column_name, column.value_type || "FIELD_VALUE_TYPE_STRING");
  }
  return options;
}

export function viewDisplayName(view?: Pick<View, "view_id" | "name"> | null) {
  if (!view) return "";
  return view.name || view.view_id || "";
}

// 新加“序列标签”筛选时的默认值：当前结果里只有一种序列标签时用它（例如 A 股恒为 default），有多种或还没有结果时不填。
export function defaultSeriesTag(rows: Array<{ seriesTag?: string }>) {
  const tags = new Set(rows.map(row => row.seriesTag ?? ""));
  return tags.size === 1 ? [...tags][0] : "";
}

export function viewModeFromPrimaryDataset(
  datasets: Array<Pick<Dataset, "dataset_id" | "data_kind">>,
  primaryDatasetId?: string
): ViewBrowseMode {
  if (!primaryDatasetId) return "missing";
  const dataset = datasets.find(item => item.dataset_id === primaryDatasetId);
  if (!dataset) return "missing";
  return isTimeSeriesDataKind(dataset.data_kind) ? "time_series" : "record";
}

// 一个视图索引一个数据集，视图里的数据集列按它读取的数据集列命名，所以 origin_id 就是 view.dataset_id 下的裸列名。
export function buildViewColumnLabels(
  viewColumns: ViewColumn[],
  datasetColumns: DatasetColumn[],
  fields: Field[],
  view?: Pick<View, "dataset_id"> | null
) {
  const datasetColumnLabels = buildDatasetColumnLabels(datasetColumns, fields);
  const datasetId = view?.dataset_id || "";
  const datasetColumnByName = new Map<string, DatasetColumn>();
  for (const column of datasetColumns) {
    if (!column.column_name || (datasetId && column.dataset_id && column.dataset_id !== datasetId)) continue;
    datasetColumnByName.set(column.column_name, column);
  }

  const labels: Record<string, string> = {};
  for (const column of viewColumns) {
    if (!column.column_name) continue;
    const fromDataset = datasetColumnByName.get(column.origin_id);
    if (fromDataset) {
      labels[column.column_name] =
        factorOutputName(column.attributes, column.column_name) ||
        factorOutputName(fromDataset.attributes, fromDataset.column_name) ||
        displayName(column.attributes) ||
        datasetColumnLabels[qualifiedDatasetColumnName(fromDataset)] ||
        datasetColumnLabels[fromDataset.column_name] ||
        readableViewColumnLabel(column.column_name);
      continue;
    }
    labels[column.column_name] =
      factorOutputName(column.attributes, column.column_name) ||
      displayName(column.attributes) ||
      systemViewLabels[column.origin_id] ||
      readableViewColumnLabel(column.column_name);
  }
  return labels;
}

export function buildViewSorts(sort: ViewSortState): SortSpec[] {
  const fieldName = sort.fieldName.trim();
  if (!fieldName || !sort.direction) return [];
  return [{ field_name: fieldName, desc: sort.direction === "desc" }];
}

export function buildKlineQuerySorts(): SortSpec[] {
  return [{ field_name: "data_time", desc: true }];
}

export function normalizeKlineLimit(value: unknown) {
  const parsed = Number.parseInt(String(value ?? ""), 10);
  if (!Number.isFinite(parsed)) return DEFAULT_KLINE_LIMIT;
  return Math.min(MAX_KLINE_LIMIT, Math.max(MIN_KLINE_LIMIT, parsed));
}

const compareOps: Partial<Record<ViewFilterOperator, FilterCond["op"]>> = {
  gt: "FILTER_OP_GT",
  gte: "FILTER_OP_GTE",
  lt: "FILTER_OP_LT",
  lte: "FILTER_OP_LTE"
};

/** zone 是 TIME 类型筛选值的输入时区：墙钟按它解析成 UTC 时刻再提交。 */
export function buildViewFilterExprs(filters: ViewFilterState[], zone = "UTC"): FilterSpec | undefined {
  const out: FilterCond[] = [];
  for (const filter of filters) {
    const fieldName = filter.fieldName.trim();
    if (!fieldName) continue;
    const valueType = filter.valueType || "FIELD_VALUE_TYPE_STRING";
    const push = (op: FilterCond["op"], raw: string) => {
      const typed = typedFilterValue(raw, valueType, zone);
      if (typed) out.push({ column: fieldName, op, values: [typed] });
    };

    if (filter.operator === "empty") {
      out.push({
        column: fieldName,
        op: "FILTER_OP_EQ",
        values: [fieldName === "series_tag" ? { string_value: "" } : { null_value: "NULL_VALUE_NULL" }]
      });
      continue;
    }
    if (filter.operator === "not_empty") {
      out.push({
        column: fieldName,
        op: "FILTER_OP_NE",
        values: [fieldName === "series_tag" ? { string_value: "" } : { null_value: "NULL_VALUE_NULL" }]
      });
      continue;
    }
    if (filter.operator === "range") {
      const startValue = (filter.startValue || "").trim();
      const endValue = (filter.endValue || "").trim();
      if (startValue) push("FILTER_OP_GTE", startValue);
      if (endValue) push("FILTER_OP_LTE", endValue);
      continue;
    }

    const value = (filter.value || "").trim();
    if (!value) continue;
    const compareOp = compareOps[filter.operator];
    if (compareOp) {
      push(compareOp, value);
      continue;
    }
    if (filter.operator === "prefix") {
      out.push({ column: fieldName, op: "FILTER_OP_LIKE", values: [{ string_value: `${value}%` }] });
      continue;
    }
    if (filter.operator === "suffix") {
      out.push({ column: fieldName, op: "FILTER_OP_LIKE", values: [{ string_value: `%${value}` }] });
      continue;
    }
    if (filter.operator === "not_contains") {
      out.push({ column: fieldName, op: "FILTER_OP_NOT_LIKE", values: [{ string_value: `%${value}%` }] });
      continue;
    }
    if (filter.operator === "eq") {
      push("FILTER_OP_EQ", value);
      continue;
    }
    if (filter.operator === "neq") {
      push("FILTER_OP_NE", value);
      continue;
    }
    out.push({ column: fieldName, op: "FILTER_OP_LIKE", values: [{ string_value: `%${value}%` }] });
  }
  return out.length === 0
    ? undefined
    : { groups: [{ conds: out, logical: "FILTER_LOGICAL_AND" }], group_logical: "FILTER_LOGICAL_AND" };
}

export function klineSubjectIdFromFilters(filters: ViewFilterState[]) {
  const filter = filters.find(item => item.fieldName.trim() === "subject_id");
  if (!filter || filter.operator === "empty" || filter.operator === "not_empty" || filter.operator === "range") {
    return "";
  }
  return (filter.value || "").trim();
}

export function klineRowsHaveFreq(rows: KlineTableRow[]) {
  return rows.some(row => isMeaningfulKlineText(row.freq));
}

export function buildKlineChartRecords(rows: KlineTableRow[], subjectId: string, seriesTag: string): KlineChartRecord[] {
  return buildKlineRecords(rows, subjectId, seriesTag).map(item => item.record);
}

export function exactSeriesTagFromFilters(filters: ViewFilterState[]) {
  const filter = filters.find(item => item.fieldName.trim() === "series_tag");
  if (!filter) return undefined;
  if (filter.operator === "empty") return "";
  if (filter.operator !== "eq") return undefined;
  const value = (filter.value || "").trim();
  return value || undefined;
}

function readableViewColumnLabel(columnName: string) {
  return systemViewLabels[columnName] || columnName;
}

function buildDatasetColumnLabels(datasetColumns: DatasetColumn[], fields: Field[]) {
  const fieldByID = new Map(fields.map(item => [item.field_id, item]));
  const labels: Record<string, string> = {};
  for (const column of datasetColumns) {
    if (!column.column_name) continue;
    const labelKey = qualifiedDatasetColumnName(column);
    const columnDisplayName = displayName(column.attributes);
    if (columnDisplayName) {
      labels[labelKey] = columnDisplayName;
      if (!column.dataset_id) labels[column.column_name] = columnDisplayName;
      continue;
    }
    if (isOriginType(column.origin_type, "DATASET_COLUMN_ORIGIN_TYPE_FIELD", 1)) {
      const label = readableMetadataLabel(
        column.column_name,
        fieldByID.get(column.origin_id)?.name || fieldByID.get(column.column_name)?.name
      );
      labels[labelKey] = label;
      if (!column.dataset_id) labels[column.column_name] = label;
      continue;
    }
    if (isOriginType(column.origin_type, "DATASET_COLUMN_ORIGIN_TYPE_CUSTOM", 2)) {
      const label = readableMetadataLabel(column.column_name);
      labels[labelKey] = label;
      if (!column.dataset_id) labels[column.column_name] = label;
      continue;
    }
    const label = systemViewLabels[column.origin_id] || readableViewColumnLabel(column.column_name);
    labels[labelKey] = label;
    if (!column.dataset_id) labels[column.column_name] = label;
  }
  return labels;
}

function readableMetadataLabel(columnName: string, metadataName?: string) {
  return metadataName || systemViewLabels[columnName] || columnName;
}

function qualifiedDatasetColumnName(column: Pick<DatasetColumn, "dataset_id" | "column_name">) {
  return column.dataset_id ? `${column.dataset_id}.${column.column_name}` : column.column_name;
}

function displayName(attributes?: Record<string, string>) {
  return attributes?.display_name?.trim() || "";
}

/** 因子输出列（column_kind=output）的标签就是它的技术列名（成员键 [__输出名]）。 */
function factorOutputName(attributes: Record<string, string> | undefined, columnName: string) {
  return attributes?.column_kind === "output" ? columnName : "";
}

function isOriginType(value: unknown, name: string, alias: number) {
  return value === name || value === alias;
}

function isTimeSeriesDataKind(value: unknown) {
  return value === "DATA_KIND_TIME_SERIES" || value === "time_series" || value === 2;
}

/** 按列类型转换检索值；数值列的值不是完整的有限数字时返回 undefined，条件整条丢弃。 */
function typedFilterValue(value: string, valueType: FieldValueType, zone: string): TypedValue | undefined {
  if (isValueType(valueType, "FIELD_VALUE_TYPE_INT", 2)) {
    const parsed = Number(value);
    return Number.isInteger(parsed) ? { int_value: parsed } : undefined;
  }
  if (isValueType(valueType, "FIELD_VALUE_TYPE_DOUBLE", 3)) {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? { double_value: parsed } : undefined;
  }
  if (isValueType(valueType, "FIELD_VALUE_TYPE_BOOL", 4)) {
    return { bool_value: value === "true" || value === "1" || value === "是" };
  }
  if (isValueType(valueType, "FIELD_VALUE_TYPE_TIME", 5)) {
    // 墙钟（YYYY-MM-DD[ HH:mm[:ss]]）按输入时区解析；带 Z 或数字偏移的 RFC3339 按它自己的偏移换算；
    // 都不是（例如纯数字）时不加这个条件，不能把 20261107 当成毫秒
    const withOffset = /T.*(Z|[+-]\d{2}:?\d{2})$/i.test(value.trim());
    const ms = wallClockToMs(value, zone) ?? (withOffset ? toInstantMs(value) : null);
    return ms === null ? undefined : { time_value: new Date(ms).toISOString().replace(/\.000Z$/, "Z") };
  }
  return { string_value: value };
}

function isValueType(value: FieldValueType, name: string, alias: number) {
  return value === name || value === alias;
}

function isMeaningfulKlineText(value?: string) {
  const text = (value || "").trim();
  return Boolean(text && text !== "-");
}

function parseKlineTimestamp(value: string) {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : undefined;
}

function buildKlineRecords(rows: KlineTableRow[], subjectId: string, seriesTag: string) {
  const taggedRows = rows.filter(row => row.seriesTag === seriesTag);
  const exactRows = subjectId ? taggedRows.filter(row => row.key === subjectId) : [];
  const fuzzyRows = subjectId ? taggedRows.filter(row => row.key.includes(subjectId)) : [];
  const sourceRows = exactRows.length > 0 ? exactRows : fuzzyRows.length > 0 ? fuzzyRows : taggedRows;
  const recordsByTime = new Map<number, { record: KlineChartRecord }>();

  for (const row of sourceRows) {
    const timestamp = parseKlineTimestamp(row.version);
    if (timestamp === undefined) continue;
    const open = klineFieldNumber(row.values, "open");
    const high = klineFieldNumber(row.values, "high");
    const low = klineFieldNumber(row.values, "low");
    const close = klineFieldNumber(row.values, "close");
    if (open === undefined || high === undefined || low === undefined || close === undefined) continue;

    const volume = klineFieldNumber(row.values, "volume");
    recordsByTime.set(timestamp, {
      record: {
        timestamp,
        open,
        high,
        low,
        close,
        ...(volume === undefined ? {} : { volume })
      }
    });
  }

  return Array.from(recordsByTime.values()).sort((a, b) => a.record.timestamp - b.record.timestamp);
}

function klineFieldNumber(values: Record<string, string>, fieldName: "open" | "high" | "low" | "close" | "volume") {
  const raw = klineFieldValue(values, fieldName);
  if (!isMeaningfulKlineText(raw)) return undefined;
  const parsed = Number.parseFloat(String(raw).replace(/,/g, ""));
  return Number.isFinite(parsed) ? parsed : undefined;
}

function klineFieldValue(values: Record<string, string>, fieldName: "open" | "high" | "low" | "close" | "volume") {
  const aliases = fieldName === "volume" ? ["volume", "vol"] : [fieldName];
  for (const alias of aliases) {
    const exact = values[alias];
    if (exact !== undefined) return exact;

    const lowerAlias = alias.toLowerCase();
    const entry = Object.entries(values).find(([name]) => name.toLowerCase() === lowerAlias);
    if (entry) return entry[1];
  }
  return undefined;
}

// ---- 表格行转换：把时序行、记录行转成表格行，并估算列宽 ----

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

/** zone 给出时，TIME 类型的值按这个时区显示并带后缀；不给时原样显示。 */
function fieldValueText(field?: FieldValue, zone?: string) {
  if (!field?.value) return "-";
  return typedValueText(field.value, zone);
}

export function typedValueText(value?: TypedValue, zone?: string): string {
  if (!value) return "-";
  if (value.string_value !== undefined) return value.string_value;
  if (value.int_value !== undefined) return String(value.int_value);
  if (value.double_value !== undefined) return String(value.double_value);
  if (value.bool_value !== undefined) return value.bool_value ? "true" : "false";
  if (value.time_value !== undefined) return zone ? formatInstant(value.time_value, zone) : value.time_value;
  if (value.json_value !== undefined) return value.json_value;
  if (value.bytes_value !== undefined) return value.bytes_value;
  if (value.list_value?.values) return value.list_value.values.map(item => typedValueText(item, zone)).join(", ");
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
  // 声明过的 View/Dataset 投影就是表结构：不再追加实时索引里残留的字段（例如已下线的因子输出）。
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

/** version 保留原始的 data_time（排序、K 线要用），显示时再按时区格式化；zone 只用于 TIME 类型的字段值。 */
export function timeSeriesRowsToTableRows(rows: TimeSeriesRow[], zone?: string): BrowseTableRow[] {
  return rows.map(row => ({
    id: `ts-${row.key?.subject_id || ""}-${row.key?.freq || ""}-${row.key?.series_tag || ""}-${row.key?.data_time || ""}`,
    key: row.key?.subject_id || "-",
    version: row.key?.data_time || "-",
    seriesTag: row.key?.series_tag || "",
    values: fieldsToValueMap(row.fields || [], zone)
  }));
}

export function recordRowsToTableRows(rows: RecordRow[], zone?: string): BrowseTableRow[] {
  return rows.map((row, index) => ({
    id: `record-${index}-${row.key?.record_id || ""}-${row.key?.version || ""}`,
    key: row.key?.record_id || "-",
    version: row.key?.version || "-",
    values: fieldsToValueMap(row.fields || [], zone)
  }));
}

function fieldsToValueMap(fields: FieldValue[], zone?: string) {
  const out: Record<string, string> = {};
  for (const field of fields) {
    out[field.field_id] = fieldValueText(field, zone);
  }
  return out;
}
