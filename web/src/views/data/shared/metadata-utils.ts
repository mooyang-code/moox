import type {
  ColumnOriginType,
  DataKind,
  DatasetColumnOriginType,
  FieldValueType,
  PageResult,
} from '@/api/storage/types';

export interface AdminPagination {
  current: number;
  pageSize: number;
  total: number;
  showTotal: boolean;
  showPageSize: boolean;
  pageSizeOptions: number[];
}

export interface SelectOption<T extends string | number = string> {
  label: string;
  value: T;
}

export const defaultPagination = (): AdminPagination => ({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [20, 50, 100],
});

export function applyPageResult(pagination: AdminPagination, page?: PageResult | { total?: string | number }) {
  const total = Number(page?.total || 0);
  pagination.total = Number.isFinite(total) ? total : 0;
}

export function statusColor(status?: string) {
  if (status === 'active') return 'green';
  if (status === 'disabled') return 'orange';
  if (status === 'failed') return 'red';
  if (status === 'building') return 'blue';
  return 'gray';
}

export function formatTime(value?: string) {
  if (!value) return '-';
  return value.replace('T', ' ').replace(/Z$/, '');
}

export function jsonText(value?: string) {
  return value?.trim() || '{}';
}

export function splitList(value?: string | string[]) {
  if (Array.isArray(value)) return value.map((item) => item.trim()).filter(Boolean);
  if (!value) return [];
  return value
    .split(/[,，\n]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function joinList(value?: string[]) {
  return (value || []).join(',');
}

export const statusOptions: SelectOption[] = [
  { label: '启用', value: 'active' },
  { label: '禁用', value: 'disabled' },
];

export const dataKindOptions: SelectOption<DataKind>[] = [
  { label: '时序数据', value: 'DATA_KIND_TIME_SERIES' },
  { label: '记录数据', value: 'DATA_KIND_RECORD' },
  { label: '快照数据', value: 'DATA_KIND_SNAPSHOT' },
  { label: '事件数据', value: 'DATA_KIND_EVENT' },
  { label: '文档数据', value: 'DATA_KIND_DOCUMENT' },
  { label: '表格数据', value: 'DATA_KIND_TABLE' },
];

export const fieldValueTypeOptions: SelectOption<FieldValueType>[] = [
  { label: '字符串', value: 'FIELD_VALUE_TYPE_STRING' },
  { label: '整数', value: 'FIELD_VALUE_TYPE_INT' },
  { label: '浮点数', value: 'FIELD_VALUE_TYPE_DOUBLE' },
  { label: '布尔', value: 'FIELD_VALUE_TYPE_BOOL' },
  { label: '时间', value: 'FIELD_VALUE_TYPE_TIME' },
  { label: 'JSON', value: 'FIELD_VALUE_TYPE_JSON' },
  { label: '二进制', value: 'FIELD_VALUE_TYPE_BYTES' },
];

export const datasetColumnOriginOptions: SelectOption<DatasetColumnOriginType>[] = [
  { label: '字段', value: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD' },
  { label: '因子', value: 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR' },
  { label: '系统列', value: 'DATASET_COLUMN_ORIGIN_TYPE_SYSTEM' },
];

export const viewColumnOriginOptions: SelectOption<ColumnOriginType>[] = [
  { label: '数据集列', value: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN' },
  { label: '系统列', value: 'COLUMN_ORIGIN_TYPE_SYSTEM' },
  { label: '表达式', value: 'COLUMN_ORIGIN_TYPE_EXPRESSION' },
];

export function optionLabel<T extends string | number>(options: SelectOption<T>[], value?: T) {
  return options.find((item) => item.value === value)?.label || value || '-';
}

export function isTimeSeriesDataKind(value?: DataKind) {
  return value === 'DATA_KIND_TIME_SERIES' || value === 2;
}

export type ViewRebuildKind = 'time_series' | 'record' | 'missing';

export function resolveViewRebuildKind(
  datasets: Array<{ dataset_id?: string; data_kind?: DataKind }>,
  primaryDatasetId?: string,
): ViewRebuildKind {
  if (!primaryDatasetId) return 'missing';
  const primaryDataset = datasets.find((item) => item.dataset_id === primaryDatasetId);
  if (!primaryDataset) return 'missing';
  return isTimeSeriesDataKind(primaryDataset.data_kind) ? 'time_series' : 'record';
}
