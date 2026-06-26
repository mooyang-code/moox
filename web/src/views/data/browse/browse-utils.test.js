import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';

const source = await readFile(new URL('./browse-utils.ts', import.meta.url), 'utf8');
const vueSource = await readFile(new URL('./index.vue', import.meta.url), 'utf8');

assert.equal(vueSource.includes('当前空间'), false);
assert.equal(vueSource.includes('dataset-summary'), false);
assert.equal(vueSource.includes('datasetDisplayName(dataset)'), true);
assert.equal(vueSource.includes('<span v-if="activeFreq"> / {{ activeFreq }}</span>'), false);
assert.equal(vueSource.includes('class="inline-freq"'), true);
assert.equal(vueSource.includes(':width="150"'), false);
assert.equal(vueSource.includes(':width="dynamicColumnWidth(column)"'), true);
assert.equal((vueSource.match(/<a-table-column title="序号"[^>]*fixed="left"/g) || []).length, 2);
assert.equal(vueSource.includes('.data-browse-page :deep(.arco-spin)'), true);
assert.equal(vueSource.includes('box-sizing: border-box;'), true);
assert.equal(vueSource.includes('<a-table-column title="记录ID" data-index="key" :width="180" fixed="left" />'), true);
assert.equal(vueSource.includes('<a-table-column title="版本" data-index="version" :width="160" />'), true);

const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2020,
    target: ts.ScriptTarget.ES2020,
  },
});
const moduleUrl = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`;
const {
  buildSubjectDataIds,
  adaptiveColumnWidth,
  buildColumnLabels,
  columnValueText,
  datasetDisplayName,
  displayDataIdText,
  rowsToColumnNames,
  timeSeriesRowsToTableRows,
  recordRowsToTableRows,
} = await import(moduleUrl);

const dataIds = buildSubjectDataIds(
  [
    { dataset_id: 'kline', subject_id: 'ETH-USDT', status: 'active' },
    { dataset_id: 'kline', subject_id: 'BTC-USDT', status: 'active' },
    { dataset_id: 'kline', subject_id: 'OLD-USDT', status: 'disabled' },
  ],
  [
    { subject_id: 'ETH-USDT', name: 'ETH 永续', subject_type: 'crypto_swap', market: 'crypto' },
    { subject_id: 'BTC-USDT', name: 'BTC 永续', subject_type: 'crypto_swap', market: 'crypto' },
  ],
);

assert.deepEqual(dataIds, [
  { id: 'BTC-USDT', name: 'BTC 永续', description: 'crypto_swap / crypto' },
  { id: 'ETH-USDT', name: 'ETH 永续', description: 'crypto_swap / crypto' },
]);
assert.equal(displayDataIdText(dataIds[0]), 'BTC-USDT');
assert.equal(datasetDisplayName({ dataset_id: 'binance_swap_kline', name: '币安U本位合约K线' }), '币安U本位合约K线');
assert.equal(datasetDisplayName({ dataset_id: 'binance_swap_kline', name: 'Binance Swap Kline' }), 'Binance Swap Kline');
assert.equal(datasetDisplayName({ dataset_id: 'custom_dataset', name: 'Custom Dataset' }), 'Custom Dataset');
assert.equal(adaptiveColumnWidth('close', '收盘价', []), 112);
assert.equal(
  adaptiveColumnWidth('spread', '价差', [{ values: { spread: '0.0005441899222828894' } }]),
  216,
);
assert.equal(adaptiveColumnWidth('note', '很长的中文字段名称', []), 176);

const columnLabels = buildColumnLabels(
  [
    { column_name: 'close', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'close' },
    { column_name: 'ma20', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR', origin_id: 'ma20_close' },
    { column_name: 'record_id', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_SYSTEM', origin_id: 'record_id' },
    { column_name: 'fallback', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'missing_field' },
  ],
  [{ field_id: 'close', name: '收盘价' }],
  [{ factor_id: 'ma20_close', name: '20日均线' }],
);
assert.deepEqual(columnLabels, {
  close: '收盘价',
  ma20: '20日均线',
  record_id: '记录ID',
  fallback: 'fallback',
});
assert.equal(
  buildColumnLabels(
    [{
      column_name: 'close',
      origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD',
      origin_id: 'close',
      attributes: { display_name: '最新价' },
    }],
    [{ field_id: 'close', name: '收盘价' }],
    [],
  ).close,
  '最新价',
);
assert.equal(
  buildColumnLabels(
    [{ column_name: 'close', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'close' }],
    [{ field_id: 'close', name: 'Close Price' }],
    [],
  ).close,
  'Close Price',
);
assert.equal(
  buildColumnLabels(
    [{ column_name: 'trading_status', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'trading_status' }],
    [{ field_id: 'trading_status', name: 'Trading Status' }],
    [],
  ).trading_status,
  'Trading Status',
);
assert.equal(source.includes('commonColumnLabels'), false);
assert.equal(source.includes('commonDatasetLabels'), false);

const tsRows = [
  {
    key: { subject_id: 'ETH-USDT', data_time: '2026-06-21T01:00:00.000000000Z' },
    columns: [
      { column_name: 'close', value: { double_value: 123.45 } },
      { column_name: 'volume', value: { int_value: '900' } },
    ],
  },
  {
    key: { subject_id: 'ETH-USDT', data_time: '2026-06-21T02:00:00.000000000Z' },
    columns: [
      { column_name: 'close', value: { double_value: 124.5 } },
      { column_name: 'fundingRate', value: { double_value: 0.0001 } },
    ],
  },
];

assert.deepEqual(rowsToColumnNames(tsRows, ['open', 'close']), ['open', 'close', 'volume', 'fundingRate']);
assert.deepEqual(timeSeriesRowsToTableRows(tsRows), [
  {
    id: 'ts-0-ETH-USDT-2026-06-21T01:00:00.000000000Z',
    key: 'ETH-USDT',
    version: '2026-06-21T01:00:00.000000000Z',
    values: { close: '123.45', volume: '900' },
  },
  {
    id: 'ts-1-ETH-USDT-2026-06-21T02:00:00.000000000Z',
    key: 'ETH-USDT',
    version: '2026-06-21T02:00:00.000000000Z',
    values: { close: '124.5', fundingRate: '0.0001' },
  },
]);

assert.equal(columnValueText({ column_name: 'active', value: { bool_value: false } }), 'false');
assert.equal(columnValueText(undefined), '-');

assert.deepEqual(recordRowsToTableRows([
  {
    key: { record_id: 'news-1', version: 'v2' },
    columns: [{ column_name: 'title', value: { string_value: '宏观快讯' } }],
  },
]), [
  { id: 'record-0-news-1-v2', key: 'news-1', version: 'v2', values: { title: '宏观快讯' } },
]);

console.log('browse utils tests passed');
