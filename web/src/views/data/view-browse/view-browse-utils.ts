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

const commonViewLabels: Record<string, string> = {
  subject_id: '数据ID',
  record_id: '记录ID',
  freq: '频率',
  data_time: '时间',
  version: '版本',
  open: '开盘价',
  high: '最高价',
  low: '最低价',
  close: '收盘价',
  volume: '成交量',
  quote_volume: '成交额',
  trade_num: '成交笔数',
  fundingRate: '资金费率',
  ma20_close: '20周期收盘均线',
  spread: '价差',
  Spread: '价差',
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
      const label = datasetColumnLabels[fromDataset.column_name] || readableViewColumnLabel(column.column_name);
      labels[column.column_name] = showDatasetName ? appendDatasetName(label, fromDataset.dataset_id, datasets) : label;
      continue;
    }
    labels[column.column_name] = commonViewLabels[column.origin_id] || readableViewColumnLabel(column.column_name);
  }
  return labels;
}

export function buildViewSorts(sort: ViewSortState): SortSpec[] {
  const fieldName = sort.fieldName.trim();
  if (!fieldName || !sort.direction) return [];
  return [{ field_name: fieldName, desc: sort.direction === 'desc' }];
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

function readableViewColumnLabel(columnName: string) {
  return commonViewLabels[columnName] || columnName;
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
    if (isOriginType(column.origin_type, 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', 1)) {
      labels[column.column_name] = readableMetadataLabel(
        column.column_name,
        fieldByID.get(column.origin_id)?.name || fieldByID.get(column.column_name)?.name,
      );
      continue;
    }
    if (isOriginType(column.origin_type, 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR', 2)) {
      labels[column.column_name] = readableMetadataLabel(
        column.column_name,
        factorByID.get(column.origin_id)?.name || factorByID.get(column.column_name)?.name,
      );
      continue;
    }
    labels[column.column_name] = commonViewLabels[column.origin_id] || readableViewColumnLabel(column.column_name);
  }
  return labels;
}

function readableMetadataLabel(columnName: string, metadataName?: string) {
  if (metadataName && containsCJK(metadataName)) return metadataName;
  return commonViewLabels[columnName] || metadataName || columnName;
}

function isOriginType(value: unknown, name: string, alias: number) {
  return value === name || value === alias;
}

function isTimeSeriesDataKind(value: unknown) {
  return value === 'DATA_KIND_TIME_SERIES' || value === 2;
}

function containsCJK(value: string) {
  return /[\u3400-\u9fff]/.test(value);
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
