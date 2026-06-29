import type { Dataset, DatasetColumn, Factor, Field, FieldValueType, FilterExpr, SortSpec, TypedValue, View, ViewColumn } from '@/api/storage/types';

export type ViewBrowseMode = 'none' | 'time_series' | 'record' | 'missing';
export type ViewSortDirection = '' | 'asc' | 'desc';
export type ViewFilterOperator =
  | 'like'
  | 'prefix'
  | 'suffix'
  | 'eq'
  | 'neq'
  | 'contains'
  | 'not_contains'
  | 'range'
  | 'empty'
  | 'not_empty';

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
  subject_id: '数据ID',
  record_id: '记录ID',
  freq: '频率',
  data_time: '时间',
  version: '版本',
};

export function viewDisplayName(view?: Pick<View, 'view_id' | 'name'> | null) {
  if (!view) return '';
  return view.name || view.view_id || '';
}

export function viewModeFromPrimaryDataset(
  datasets: Array<Pick<Dataset, 'dataset_id' | 'data_kind'>>,
  primaryDatasetId?: string,
): ViewBrowseMode {
  if (!primaryDatasetId) return 'missing';
  const dataset = datasets.find((item) => item.dataset_id === primaryDatasetId);
  if (!dataset) return 'missing';
  return isTimeSeriesDataKind(dataset.data_kind) ? 'time_series' : 'record';
}

export function buildViewColumnLabels(
  viewColumns: ViewColumn[],
  datasetColumns: DatasetColumn[],
  fields: Field[],
  factors: Factor[],
  datasets: Array<Pick<Dataset, 'dataset_id' | 'name'>> = [],
  view?: Pick<View, 'primary_dataset_id' | 'dataset_ids'> | null,
) {
  const datasetColumnLabels = buildDatasetColumnLabels(datasetColumns, fields, factors);
  const datasetColumnByQualifiedName = new Map<string, DatasetColumn>();
  for (const column of datasetColumns) {
    if (!column.dataset_id || !column.column_name) continue;
    datasetColumnByQualifiedName.set(`${column.dataset_id}.${column.column_name}`, column);
  }
  const showDatasetName = viewDatasetCount(view) > 1;

  const labels: Record<string, string> = {};
  for (const column of viewColumns) {
    if (!column.column_name) continue;
    const fromDataset = datasetColumnByQualifiedName.get(column.origin_id);
    if (fromDataset) {
      const label = displayName(column.attributes)
        || datasetColumnLabels[qualifiedDatasetColumnName(fromDataset)]
        || datasetColumnLabels[fromDataset.column_name]
        || readableViewColumnLabel(column.column_name);
      labels[column.column_name] = showDatasetName ? appendDatasetName(label, fromDataset.dataset_id, datasets) : label;
      continue;
    }
    labels[column.column_name] = displayName(column.attributes)
      || systemViewLabels[column.origin_id]
      || readableViewColumnLabel(column.column_name);
  }
  return labels;
}

export function buildViewSorts(sort: ViewSortState): SortSpec[] {
  const fieldName = sort.fieldName.trim();
  if (!fieldName || !sort.direction) return [];
  return [{ field_name: fieldName, desc: sort.direction === 'desc' }];
}

export function buildKlineQuerySorts(): SortSpec[] {
  return [{ field_name: 'data_time', desc: true }];
}

export function normalizeKlineLimit(value: unknown) {
  const parsed = Number.parseInt(String(value ?? ''), 10);
  if (!Number.isFinite(parsed)) return DEFAULT_KLINE_LIMIT;
  return Math.min(MAX_KLINE_LIMIT, Math.max(MIN_KLINE_LIMIT, parsed));
}

export function buildViewFilterExprs(filters: ViewFilterState[]): FilterExpr[] {
  const out: FilterExpr[] = [];
  for (const filter of filters) {
    const fieldName = filter.fieldName.trim();
    if (!fieldName) continue;
    const argPrefix = safeArgName(fieldName);
    const valueType = filter.valueType || 'FIELD_VALUE_TYPE_STRING';

    if (filter.operator === 'empty') {
      out.push({ expr: `is_empty(${fieldName})`, args: {} });
      continue;
    }
    if (filter.operator === 'not_empty') {
      out.push({ expr: `is_not_empty(${fieldName})`, args: {} });
      continue;
    }
    if (filter.operator === 'range') {
      const startValue = (filter.startValue || '').trim();
      const endValue = (filter.endValue || '').trim();
      if (startValue) {
        const argName = `${argPrefix}_start`;
        out.push({ expr: `${fieldName} >= $${argName}`, args: { [argName]: typedFilterValue(startValue, valueType) } });
      }
      if (endValue) {
        const argName = `${argPrefix}_end`;
        out.push({ expr: `${fieldName} <= $${argName}`, args: { [argName]: typedFilterValue(endValue, valueType) } });
      }
      continue;
    }

    const value = (filter.value || '').trim();
    if (!value) continue;
    const argName = `${argPrefix}_${filter.operator}`;
    const typedValue = typedFilterValue(value, valueType);
    if (filter.operator === 'prefix') {
      out.push({ expr: `starts_with(${fieldName}, $${argName})`, args: { [argName]: typedValue } });
      continue;
    }
    if (filter.operator === 'suffix') {
      out.push({ expr: `ends_with(${fieldName}, $${argName})`, args: { [argName]: typedValue } });
      continue;
    }
    if (filter.operator === 'not_contains') {
      out.push({ expr: `not_contains(${fieldName}, $${argName})`, args: { [argName]: typedValue } });
      continue;
    }
    if (filter.operator === 'eq') {
      out.push({ expr: `${fieldName} == $${argName}`, args: { [argName]: typedValue } });
      continue;
    }
    if (filter.operator === 'neq') {
      out.push({ expr: `${fieldName} != $${argName}`, args: { [argName]: typedValue } });
      continue;
    }
    out.push({ expr: `${fieldName} contains $${argName}`, args: { [argName]: typedValue } });
  }
  return out;
}

export function klineSubjectIdFromFilters(filters: ViewFilterState[]) {
  const filter = filters.find((item) => item.fieldName.trim() === 'subject_id');
  if (!filter || filter.operator === 'empty' || filter.operator === 'not_empty' || filter.operator === 'range') {
    return '';
  }
  return (filter.value || '').trim();
}

export function klineRowsHaveFreq(rows: KlineTableRow[]) {
  return rows.some((row) => isMeaningfulKlineText(row.freq));
}

export function buildKlineChartRecords(rows: KlineTableRow[], subjectId = ''): KlineChartRecord[] {
  return buildKlineRecords(rows, subjectId).map((item) => item.record);
}

function readableViewColumnLabel(columnName: string) {
  return systemViewLabels[columnName] || columnName;
}

function viewDatasetCount(view?: Pick<View, 'primary_dataset_id' | 'dataset_ids'> | null) {
  if (!view) return 0;
  const datasetIds = new Set<string>();
  if (view.primary_dataset_id) datasetIds.add(view.primary_dataset_id);
  for (const datasetId of view.dataset_ids || []) {
    if (datasetId) datasetIds.add(datasetId);
  }
  return datasetIds.size;
}

function appendDatasetName(label: string, datasetId: string, datasets: Array<Pick<Dataset, 'dataset_id' | 'name'>>) {
  const dataset = datasets.find((item) => item.dataset_id === datasetId);
  return `${label}（${dataset?.name || datasetId}）`;
}

function buildDatasetColumnLabels(datasetColumns: DatasetColumn[], fields: Field[], factors: Factor[]) {
  const fieldByID = new Map(fields.map((item) => [item.field_id, item]));
  const factorByID = new Map(factors.map((item) => [item.factor_id, item]));
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
    if (isOriginType(column.origin_type, 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', 1)) {
      const label = readableMetadataLabel(
        column.column_name,
        fieldByID.get(column.origin_id)?.name || fieldByID.get(column.column_name)?.name,
      );
      labels[labelKey] = label;
      if (!column.dataset_id) labels[column.column_name] = label;
      continue;
    }
    if (isOriginType(column.origin_type, 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR', 2)) {
      const label = readableMetadataLabel(
        column.column_name,
        factorByID.get(column.origin_id)?.name || factorByID.get(column.column_name)?.name,
      );
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

function qualifiedDatasetColumnName(column: Pick<DatasetColumn, 'dataset_id' | 'column_name'>) {
  return column.dataset_id ? `${column.dataset_id}.${column.column_name}` : column.column_name;
}

function displayName(attributes?: Record<string, string>) {
  return attributes?.display_name?.trim() || '';
}

function isOriginType(value: unknown, name: string, alias: number) {
  return value === name || value === alias;
}

function isTimeSeriesDataKind(value: unknown) {
  return value === 'DATA_KIND_TIME_SERIES' || value === 2;
}

function safeArgName(value: string) {
  return value.replace(/[^A-Za-z0-9_]/g, '_') || 'value';
}

function typedFilterValue(value: string, valueType: FieldValueType): TypedValue {
  if (isValueType(valueType, 'FIELD_VALUE_TYPE_INT', 2)) {
    const parsed = Number.parseInt(value, 10);
    return { int_value: Number.isFinite(parsed) ? parsed : value };
  }
  if (isValueType(valueType, 'FIELD_VALUE_TYPE_DOUBLE', 3)) {
    const parsed = Number.parseFloat(value);
    return { double_value: Number.isFinite(parsed) ? parsed : 0 };
  }
  if (isValueType(valueType, 'FIELD_VALUE_TYPE_BOOL', 4)) {
    return { bool_value: value === 'true' || value === '1' || value === '是' };
  }
  if (isValueType(valueType, 'FIELD_VALUE_TYPE_TIME', 5)) {
    return { time_value: value };
  }
  return { string_value: value };
}

function isValueType(value: FieldValueType, name: string, alias: number) {
  return value === name || value === alias;
}

function isMeaningfulKlineText(value?: string) {
  const text = (value || '').trim();
  return Boolean(text && text !== '-');
}

function parseKlineTimestamp(value: string) {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : undefined;
}

function buildKlineRecords(rows: KlineTableRow[], subjectId = '') {
  const exactRows = subjectId ? rows.filter((row) => row.key === subjectId) : [];
  const fuzzyRows = subjectId ? rows.filter((row) => row.key.includes(subjectId)) : [];
  const sourceRows = exactRows.length > 0 ? exactRows : fuzzyRows.length > 0 ? fuzzyRows : rows;
  const recordsByTime = new Map<number, { record: KlineChartRecord }>();

  for (const row of sourceRows) {
    const timestamp = parseKlineTimestamp(row.version);
    if (timestamp === undefined) continue;
    const open = klineFieldNumber(row.values, 'open');
    const high = klineFieldNumber(row.values, 'high');
    const low = klineFieldNumber(row.values, 'low');
    const close = klineFieldNumber(row.values, 'close');
    if (open === undefined || high === undefined || low === undefined || close === undefined) continue;

    const volume = klineFieldNumber(row.values, 'volume');
    recordsByTime.set(timestamp, {
      record: {
        timestamp,
        open,
        high,
        low,
        close,
        ...(volume === undefined ? {} : { volume }),
      },
    });
  }

  return Array.from(recordsByTime.values()).sort((a, b) => a.record.timestamp - b.record.timestamp);
}

function klineFieldNumber(values: Record<string, string>, fieldName: 'open' | 'high' | 'low' | 'close' | 'volume') {
  const raw = klineFieldValue(values, fieldName);
  if (!isMeaningfulKlineText(raw)) return undefined;
  const parsed = Number.parseFloat(String(raw).replace(/,/g, ''));
  return Number.isFinite(parsed) ? parsed : undefined;
}

function klineFieldValue(values: Record<string, string>, fieldName: 'open' | 'high' | 'low' | 'close' | 'volume') {
  const aliases = fieldName === 'volume' ? ['volume', 'vol'] : [fieldName];
  for (const alias of aliases) {
    const exact = values[alias];
    if (exact !== undefined) return exact;

    const lowerAlias = alias.toLowerCase();
    const entry = Object.entries(values).find(([name]) => {
      const lowerName = name.toLowerCase();
      return lowerName === lowerAlias || lowerName.endsWith(`.${lowerAlias}`);
    });
    if (entry) return entry[1];
  }
  return undefined;
}
