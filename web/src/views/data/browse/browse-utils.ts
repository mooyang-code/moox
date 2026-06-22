import type {
  ColumnValue,
  Dataset,
  DatasetColumn,
  DatasetColumnOriginType,
  Factor,
  Field,
  RecordRow,
  DatasetSubject,
  Subject,
  TimeSeriesRow,
  TypedValue,
} from '@/api/storage/types';

export interface BrowseDataId {
  id: string;
  name: string;
  description: string;
}

export interface BrowseTableRow {
  id: string;
  key: string;
  version: string;
  values: Record<string, string>;
}

const minAdaptiveColumnWidth = 112;
const maxAdaptiveColumnWidth = 320;

const systemColumnLabels: Record<string, string> = {
  subject_id: '数据ID',
  record_id: '记录ID',
  freq: '频率',
  data_time: '时间',
  version: '版本',
};

const commonColumnLabels: Record<string, string> = {
  symbol: '交易对',
  open: '开盘价',
  high: '最高价',
  low: '最低价',
  close: '收盘价',
  volume: '成交量',
  quote_volume: '成交额',
  trade_num: '成交笔数',
  taker_buy_base_asset_volume: '主动买入成交量',
  taker_buy_quote_asset_volume: '主动买入成交额',
  avg_price_1m: '1分钟均价',
  avg_price_5m: '5分钟均价',
  fundingRate: '资金费率',
  spread: '价差',
  Spread: '价差',
  ma20_close: '20周期收盘均线',
  text_note: '备注',
  trading_status: '交易状态',
};

const commonDatasetLabels: Record<string, string> = {
  binance_spot_kline: '币安现货K线',
  binance_spot_symbols: '币安现货交易对',
  binance_swap_kline: '币安U本位合约K线',
  eastmoney_daily_kline: '东方财富日线行情',
  eastmoney_stock_profile: '东方财富股票资料',
  akshare_financial_indicator: 'AKShare 财务指标',
};

export function datasetDisplayName(dataset?: Pick<Dataset, 'dataset_id' | 'name'> | null) {
  if (!dataset) return '';
  if (dataset.name && containsCJK(dataset.name)) return dataset.name;
  return commonDatasetLabels[dataset.dataset_id] || dataset.dataset_id || dataset.name || '';
}

export function adaptiveColumnWidth(columnName: string, label: string, rows: Array<Pick<BrowseTableRow, 'values'>>) {
  const headerWidth = visualTextWidth(label || columnName);
  const valueWidth = rows.reduce((maxWidth, row) => {
    const value = row.values?.[columnName];
    return Math.max(maxWidth, visualTextWidth(value || ''));
  }, 0);
  const rawWidth = Math.max(headerWidth, valueWidth) + 48;
  return clamp(roundUp(rawWidth, 8), minAdaptiveColumnWidth, maxAdaptiveColumnWidth);
}

export function buildSubjectDataIds(datasetSubjects: DatasetSubject[], subjects: Subject[]): BrowseDataId[] {
  const subjectByID = new Map(subjects.map((item) => [item.subject_id, item]));
  return datasetSubjects
    .filter((item) => !item.status || item.status === 'active')
    .map((item) => {
      const subject = subjectByID.get(item.subject_id);
      return {
        id: item.subject_id,
        name: subject?.name || item.subject_id,
        description: [subject?.subject_type, subject?.market].filter(Boolean).join(' / '),
      };
    })
    .sort((a, b) => a.id.localeCompare(b.id));
}

export function displayDataIdText(item: BrowseDataId) {
  return item.id;
}

export function buildColumnLabels(columns: DatasetColumn[], fields: Field[], factors: Factor[]) {
  const fieldByID = new Map(fields.map((item) => [item.field_id, item]));
  const factorByID = new Map(factors.map((item) => [item.factor_id, item]));
  const labels: Record<string, string> = {};
  for (const column of columns) {
    if (!column.column_name) continue;
    labels[column.column_name] = resolveColumnLabel(column, fieldByID, factorByID);
  }
  return labels;
}

function resolveColumnLabel(
  column: DatasetColumn,
  fieldByID: Map<string, Field>,
  factorByID: Map<string, Factor>,
) {
  if (isOriginType(column.origin_type, 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', 1)) {
    return readableColumnLabel(
      column.column_name,
      fieldByID.get(column.origin_id)?.name || fieldByID.get(column.column_name)?.name,
    );
  }
  if (isOriginType(column.origin_type, 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR', 2)) {
    return readableColumnLabel(
      column.column_name,
      factorByID.get(column.origin_id)?.name || factorByID.get(column.column_name)?.name,
    );
  }
  return systemColumnLabels[column.origin_id] || readableColumnLabel(column.column_name);
}

function readableColumnLabel(columnName: string, metadataName?: string) {
  if (metadataName && containsCJK(metadataName)) return metadataName;
  return commonColumnLabels[columnName] || metadataName || systemColumnLabels[columnName] || columnName;
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

function isOriginType(value: DatasetColumnOriginType, name: string, alias: number) {
  return value === name || value === alias;
}

export function columnValueText(column?: ColumnValue) {
  if (!column?.value) return '-';
  return typedValueText(column.value);
}

export function typedValueText(value?: TypedValue): string {
  if (!value) return '-';
  if (value.string_value !== undefined) return value.string_value;
  if (value.int_value !== undefined) return String(value.int_value);
  if (value.double_value !== undefined) return String(value.double_value);
  if (value.bool_value !== undefined) return value.bool_value ? 'true' : 'false';
  if (value.time_value !== undefined) return value.time_value;
  if (value.json_value !== undefined) return value.json_value;
  if (value.bytes_value !== undefined) return value.bytes_value;
  if (value.list_value?.values) return value.list_value.values.map((item) => typedValueText(item)).join(', ');
  return '-';
}

export function rowsToColumnNames(rows: Array<TimeSeriesRow | RecordRow>, preferred: string[] = []) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const name of preferred) {
    if (!name || seen.has(name)) continue;
    seen.add(name);
    out.push(name);
  }
  for (const row of rows) {
    for (const column of row.columns || []) {
      if (!column.column_name || seen.has(column.column_name)) continue;
      seen.add(column.column_name);
      out.push(column.column_name);
    }
  }
  return out;
}

export function timeSeriesRowsToTableRows(rows: TimeSeriesRow[]): BrowseTableRow[] {
  return rows.map((row, index) => ({
    id: `ts-${index}-${row.key?.subject_id || ''}-${row.key?.data_time || ''}`,
    key: row.key?.subject_id || '-',
    version: row.key?.data_time || '-',
    values: columnsToValueMap(row.columns || []),
  }));
}

export function recordRowsToTableRows(rows: RecordRow[]): BrowseTableRow[] {
  return rows.map((row, index) => ({
    id: `record-${index}-${row.key?.record_id || ''}-${row.key?.version || ''}`,
    key: row.key?.record_id || '-',
    version: row.key?.version || '-',
    values: columnsToValueMap(row.columns || []),
  }));
}

function columnsToValueMap(columns: ColumnValue[]) {
  const out: Record<string, string> = {};
  for (const column of columns) {
    out[column.column_name] = columnValueText(column);
  }
  return out;
}
