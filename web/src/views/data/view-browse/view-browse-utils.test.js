import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';

const source = await readFile(new URL('./view-browse-utils.ts', import.meta.url), 'utf8');
const vueSource = await readFile(new URL('./index.vue', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2020,
    target: ts.ScriptTarget.ES2020,
  },
});
const moduleUrl = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`;
const {
  buildViewFilterExprs,
  buildViewSorts,
  buildViewColumnLabels,
  buildKlineQuerySorts,
  buildKlineChartRecords,
  DEFAULT_KLINE_LIMIT,
  klineRowsHaveFreq,
  klineSubjectIdFromFilters,
  normalizeKlineLimit,
  viewDisplayName,
  viewModeFromPrimaryDataset,
} = await import(moduleUrl);

assert.equal(
  viewDisplayName({ view_id: 'binance_swap_kline_view', name: '合约K线' }),
  '合约K线',
);
assert.equal(
  viewDisplayName({ view_id: 'kline_view', name: 'K线视图' }),
  'K线视图',
);

assert.equal(
  viewModeFromPrimaryDataset(
    [
      { dataset_id: 'kline', data_kind: 'DATA_KIND_TIME_SERIES' },
      { dataset_id: 'news', data_kind: 'DATA_KIND_RECORD' },
    ],
    'kline',
  ),
  'time_series',
);
assert.equal(
  viewModeFromPrimaryDataset([{ dataset_id: 'news', data_kind: 'DATA_KIND_RECORD' }], 'news'),
  'record',
);
assert.equal(viewModeFromPrimaryDataset([], 'missing'), 'missing');

const labels = buildViewColumnLabels(
  [
    { column_name: 'close', origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN', origin_id: 'kline.close' },
    { column_name: 'ma20', origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN', origin_id: 'kline.ma20_close' },
    { column_name: 'subject_id', origin_type: 'COLUMN_ORIGIN_TYPE_SYSTEM', origin_id: 'subject_id' },
    {
      column_name: 'spread',
      origin_type: 'COLUMN_ORIGIN_TYPE_EXPRESSION',
      origin_id: 'close-open',
      attributes: { display_name: '价差' },
    },
  ],
  [
    { dataset_id: 'kline', column_name: 'close', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'close' },
    { dataset_id: 'kline', column_name: 'ma20_close', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR', origin_id: 'ma20_close' },
  ],
  [{ field_id: 'close', name: '收盘价' }],
  [{ factor_id: 'ma20_close', name: '20周期收盘均线' }],
  [{ dataset_id: 'kline', name: 'K线' }],
  { primary_dataset_id: 'kline', dataset_ids: ['kline'] },
);

assert.deepEqual(labels, {
  close: '收盘价',
  ma20: '20周期收盘均线',
  subject_id: '数据ID',
  spread: '价差',
});
assert.equal(
  buildViewColumnLabels(
    [{
      column_name: 'close',
      origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN',
      origin_id: 'kline.close',
      attributes: { display_name: '最新价' },
    }],
    [{ dataset_id: 'kline', column_name: 'close', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'close' }],
    [{ field_id: 'close', name: '收盘价' }],
    [],
    [{ dataset_id: 'kline', name: 'K线' }],
    { primary_dataset_id: 'kline', dataset_ids: ['kline'] },
  ).close,
  '最新价',
);
assert.equal(
  buildViewColumnLabels(
    [{ column_name: 'close', origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN', origin_id: 'kline.close' }],
    [{ dataset_id: 'kline', column_name: 'close', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'close' }],
    [{ field_id: 'close', name: 'Close Price' }],
    [],
    [{ dataset_id: 'kline', name: 'K线' }],
    { primary_dataset_id: 'kline', dataset_ids: ['kline'] },
  ).close,
  'Close Price',
);

const joinedLabels = buildViewColumnLabels(
  [
    {
      column_name: 'binance_swap_kline.close',
      origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN',
      origin_id: 'binance_swap_kline.close',
    },
    {
      column_name: 'binance_spot_kline.close',
      origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN',
      origin_id: 'binance_spot_kline.close',
    },
  ],
  [
    {
      dataset_id: 'binance_swap_kline',
      column_name: 'close',
      origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD',
      origin_id: 'close',
    },
    {
      dataset_id: 'binance_spot_kline',
      column_name: 'close',
      origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD',
      origin_id: 'close',
    },
  ],
  [{ field_id: 'close', name: '收盘价' }],
  [],
  [
    { dataset_id: 'binance_swap_kline', name: '币安U本位合约K线' },
    { dataset_id: 'binance_spot_kline', name: '币安现货K线' },
  ],
  {
    primary_dataset_id: 'binance_swap_kline',
    dataset_ids: ['binance_swap_kline', 'binance_spot_kline'],
  },
);

assert.deepEqual(joinedLabels, {
  'binance_swap_kline.close': '收盘价（币安U本位合约K线）',
  'binance_spot_kline.close': '收盘价（币安现货K线）',
});
assert.equal(source.includes('commonViewLabels'), false);
assert.equal(source.includes("open: '开盘价'"), false);

assert.deepEqual(buildViewSorts({ fieldName: 'close', direction: 'asc' }), [{ field_name: 'close', desc: false }]);
assert.deepEqual(buildViewSorts({ fieldName: 'volume', direction: 'desc' }), [{ field_name: 'volume', desc: true }]);
assert.deepEqual(buildViewSorts({ fieldName: 'volume', direction: '' }), []);

assert.deepEqual(
  buildViewFilterExprs([
    { fieldName: 'subject_id', operator: 'contains', value: 'BTC', valueType: 'FIELD_VALUE_TYPE_STRING' },
    { fieldName: 'close', operator: 'range', startValue: '650', endValue: '700', valueType: 'FIELD_VALUE_TYPE_DOUBLE' },
    { fieldName: 'volume', operator: 'empty', valueType: 'FIELD_VALUE_TYPE_DOUBLE' },
  ]),
  [
    {
      expr: 'subject_id contains $subject_id_contains',
      args: { subject_id_contains: { string_value: 'BTC' } },
    },
    {
      expr: 'close >= $close_start',
      args: { close_start: { double_value: 650 } },
    },
    {
      expr: 'close <= $close_end',
      args: { close_end: { double_value: 700 } },
    },
    {
      expr: 'is_empty(volume)',
      args: {},
    },
  ],
);

assert.deepEqual(
  buildViewFilterExprs([
    { fieldName: 'symbol', operator: 'prefix', value: 'BTC', valueType: 'FIELD_VALUE_TYPE_STRING' },
    { fieldName: 'name', operator: 'suffix', value: 'USDT', valueType: 'FIELD_VALUE_TYPE_STRING' },
    { fieldName: 'note', operator: 'not_contains', value: 'test', valueType: 'FIELD_VALUE_TYPE_STRING' },
    { fieldName: 'status', operator: 'not_empty', valueType: 'FIELD_VALUE_TYPE_STRING' },
  ]),
  [
    {
      expr: 'starts_with(symbol, $symbol_prefix)',
      args: { symbol_prefix: { string_value: 'BTC' } },
    },
    {
      expr: 'ends_with(name, $name_suffix)',
      args: { name_suffix: { string_value: 'USDT' } },
    },
    {
      expr: 'not_contains(note, $note_not_contains)',
      args: { note_not_contains: { string_value: 'test' } },
    },
    {
      expr: 'is_not_empty(status)',
      args: {},
    },
  ],
);

assert.equal(
  klineSubjectIdFromFilters([
    { fieldName: 'freq', operator: 'contains', value: '1m' },
    { fieldName: 'subject_id', operator: 'contains', value: 'BTC-USDT' },
  ]),
  'BTC-USDT',
);
assert.equal(
  klineSubjectIdFromFilters([{ fieldName: 'subject_id', operator: 'empty', value: 'BTC-USDT' }]),
  '',
);
assert.equal(klineRowsHaveFreq([{ key: 'BTC-USDT', version: '2026-06-28T07:27:00Z', freq: '1m', values: {} }]), true);
assert.equal(klineRowsHaveFreq([{ key: 'BTC-USDT', version: '2026-06-28T07:27:00Z', freq: '-', values: {} }]), false);

assert.equal(DEFAULT_KLINE_LIMIT, 200);
assert.equal(normalizeKlineLimit(undefined), 200);
assert.equal(normalizeKlineLimit('800'), 800);
assert.equal(normalizeKlineLimit(0), 1);
assert.equal(normalizeKlineLimit(99999), 5000);
assert.deepEqual(buildKlineQuerySorts(), [{ field_name: 'data_time', desc: true }]);

assert.deepEqual(
  buildKlineChartRecords(
    [
      {
        key: 'ETH-USDT',
        version: '2026-06-28T07:26:00.000000000Z',
        freq: '1m',
        values: { open: '10', high: '11', low: '9', close: '10.5' },
      },
      {
        key: 'BTC-USDT',
        version: '2026-06-28T07:27:00.000000000Z',
        freq: '1m',
        values: { open: '100', high: '110', low: '95', close: '105' },
      },
    ],
    'BTC',
  ),
  [{ timestamp: 1782631620000, open: 100, high: 110, low: 95, close: 105 }],
);
assert.deepEqual(
  buildKlineChartRecords(
    [
      {
        key: 'ETH-USDT',
        version: '2026-06-28T07:28:00.000000000Z',
        freq: '1m',
        values: { open: '10', high: '11', low: '9', close: '10.5', volume: '100' },
      },
      {
        key: 'BTC-USDT',
        version: '2026-06-28T07:28:00.000000000Z',
        freq: '1m',
        values: { open: '101', high: '111', low: '99', close: '108', volume: '2,400.5' },
      },
      {
        key: 'BTC-USDT',
        version: '2026-06-28T07:27:00.000000000Z',
        freq: '1m',
        values: {
          'binance_spot_kline.open': '100',
          'binance_spot_kline.high': '110',
          'binance_spot_kline.low': '95',
          'binance_spot_kline.close': '105',
          'binance_spot_kline.volume': '1200',
        },
      },
    ],
    'BTC-USDT',
  ),
  [
    { timestamp: 1782631620000, open: 100, high: 110, low: 95, close: 105, volume: 1200 },
    { timestamp: 1782631680000, open: 101, high: 111, low: 99, close: 108, volume: 2400.5 },
  ],
);

assert.equal(vueSource.includes('@click="openKlineModal"'), true);
assert.equal(vueSource.includes('K线'), true);
assert.equal(vueSource.includes('kline-chart-host'), true);
assert.equal(vueSource.includes('buildKlineChartRecords'), true);
assert.equal(vueSource.includes('buildKlineQuerySorts()'), true);
assert.equal(vueSource.includes('page: { page: 1, size: normalizedKlineLimit.value }'), true);
assert.equal(vueSource.includes('v-model="klineLimit"'), true);
assert.equal(vueSource.includes('@click="toggleKlinePlayback"'), true);
assert.equal(vueSource.includes('klinePlaying'), true);
assert.equal(vueSource.includes('startKlinePlayback'), true);
assert.equal(vueSource.includes('stopKlinePlayback'), true);
assert.equal(vueSource.includes('scrollToRealTime(0)'), false);
assert.equal(vueSource.includes('TooltipShowRule.FollowCross'), false);
assert.equal((vueSource.match(/TooltipShowRule\.None/g) || []).length >= 2, true);
assert.equal(vueSource.includes('class="kline-spin"'), true);
assert.equal(vueSource.includes("upColor: '#ef5350'"), true);
assert.equal(vueSource.includes("upBorderColor: '#ef5350'"), true);
assert.equal(vueSource.includes("upWickColor: '#ef5350'"), true);
assert.equal(vueSource.includes("downColor: '#26a69a'"), true);
assert.equal(vueSource.includes("downBorderColor: '#26a69a'"), true);
assert.equal(vueSource.includes("downWickColor: '#26a69a'"), true);
assert.equal(vueSource.includes("from 'klinecharts'"), true);
assert.equal(vueSource.includes("from 'lightweight-charts'"), false);

function scopedStyleFor(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = vueSource.match(new RegExp(`${escaped} \\{([\\s\\S]*?)\\n\\}`));
  return match?.[1] || '';
}

assert.match(scopedStyleFor('.view-browse-page'), /width:\s*100%;/);
assert.match(scopedStyleFor('.view-browse-page'), /min-width:\s*0;/);
assert.match(scopedStyleFor('.view-browse-page'), /box-sizing:\s*border-box;/);
assert.match(scopedStyleFor('.view-browse-page'), /height:\s*100%;/);
assert.match(scopedStyleFor('.view-browse-page'), /overflow-y:\s*auto;/);
assert.match(scopedStyleFor('.view-browse-page'), /padding-bottom:\s*72px;/);
assert.match(scopedStyleFor('.view-browse-page :deep(.arco-spin)'), /display:\s*block;/);
assert.match(scopedStyleFor('.view-browse-page :deep(.arco-spin)'), /width:\s*100%;/);
assert.match(scopedStyleFor('.view-browse-page :deep(.arco-spin)'), /min-width:\s*0;/);
assert.match(scopedStyleFor('.result-pane'), /width:\s*100%;/);
assert.match(scopedStyleFor('.result-pane'), /max-width:\s*100%;/);
assert.match(scopedStyleFor('.kline-spin'), /display:\s*block;/);
assert.match(scopedStyleFor('.kline-spin'), /width:\s*100%;/);
assert.match(scopedStyleFor('.kline-modal-body'), /width:\s*100%;/);
assert.match(scopedStyleFor('.kline-modal-body'), /box-sizing:\s*border-box;/);
assert.match(scopedStyleFor('.kline-chart-host'), /max-width:\s*100%;/);
assert.match(scopedStyleFor('.kline-chart-host'), /box-sizing:\s*border-box;/);
assert.match(scopedStyleFor('.is-up'), /color:\s*#ef5350;/);
assert.match(scopedStyleFor('.is-down'), /color:\s*#26a69a;/);
assert.match(scopedStyleFor('.result-pane'), /box-sizing:\s*border-box;/);
assert.match(scopedStyleFor('.result-pane :deep(.arco-pagination)'), /margin-top:\s*12px;/);

console.log('view browse utils tests passed');
