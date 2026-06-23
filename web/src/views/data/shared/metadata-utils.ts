import type {
  ColumnOriginType,
  DataKind,
  DatasetColumnOriginType,
  FieldValueType,
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
  aliases?: Array<string | number>;
}

export interface PageResultTotal {
  total: number;
}

export const defaultPagination = (): AdminPagination => ({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [20, 50, 100],
});

export function pageResultTotal(page?: PageResultTotal) {
  if (!page) return 0;
  if (typeof page.total !== 'number' || !Number.isFinite(page.total) || page.total < 0) {
    throw new Error(`page_result.total must be a number, got ${typeof page.total}`);
  }
  return page.total;
}

export function applyPageResult(pagination: AdminPagination, page?: PageResultTotal) {
  pagination.total = pageResultTotal(page);
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

export function validateLowerSnakeId(value: string | undefined, maxLength: number) {
  const id = (value || '').trim();
  if (!id) return 'ID 不能为空';
  if (id.length > maxLength) return `ID 总长度不能超过 ${maxLength}`;
  if (!/^[a-z][a-z0-9_]*$/.test(id)) return 'ID 只能使用小写字母、数字和下划线，且必须以小写字母开头';
  return '';
}

export const statusOptions: SelectOption[] = [
  { label: '启用', value: 'active' },
  { label: '禁用', value: 'disabled' },
];

export const dataKindOptions: SelectOption<DataKind>[] = [
  { label: '时序数据', value: 'DATA_KIND_TIME_SERIES', aliases: [2] },
  { label: '记录数据', value: 'DATA_KIND_RECORD', aliases: [1] },
  { label: '快照数据', value: 'DATA_KIND_SNAPSHOT', aliases: [3] },
  { label: '事件数据', value: 'DATA_KIND_EVENT', aliases: [4] },
  { label: '文档数据', value: 'DATA_KIND_DOCUMENT', aliases: [5] },
  { label: '表格数据', value: 'DATA_KIND_TABLE', aliases: [6] },
];

export const fieldValueTypeOptions: SelectOption<FieldValueType>[] = [
  { label: '字符串', value: 'FIELD_VALUE_TYPE_STRING', aliases: [1] },
  { label: '整数', value: 'FIELD_VALUE_TYPE_INT', aliases: [2] },
  { label: '浮点数', value: 'FIELD_VALUE_TYPE_DOUBLE', aliases: [3] },
  { label: '布尔', value: 'FIELD_VALUE_TYPE_BOOL', aliases: [4] },
  { label: '时间', value: 'FIELD_VALUE_TYPE_TIME', aliases: [5] },
  { label: 'JSON', value: 'FIELD_VALUE_TYPE_JSON', aliases: [6] },
  { label: '二进制', value: 'FIELD_VALUE_TYPE_BYTES', aliases: [7] },
];

export const datasetColumnOriginOptions: SelectOption<DatasetColumnOriginType>[] = [
  { label: '字段', value: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', aliases: [1] },
  { label: '因子', value: 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR', aliases: [2] },
  { label: '系统列', value: 'DATASET_COLUMN_ORIGIN_TYPE_SYSTEM', aliases: [3] },
];

export const viewColumnOriginOptions: SelectOption<ColumnOriginType>[] = [
  { label: '数据集列', value: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN', aliases: [1] },
  { label: '系统列', value: 'COLUMN_ORIGIN_TYPE_SYSTEM', aliases: [2] },
  { label: '表达式', value: 'COLUMN_ORIGIN_TYPE_EXPRESSION', aliases: [3] },
];

export function optionLabel<T extends string | number>(options: SelectOption<T>[], value?: T | null) {
  if (value === undefined || value === null || value === '') return '-';
  const matched = options.find((item) => item.value === value || item.aliases?.some((alias) => alias === value));
  return matched?.label || String(value);
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
