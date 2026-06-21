import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';

const source = await readFile(new URL('./metadata-utils.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2020,
    target: ts.ScriptTarget.ES2020,
  },
});
const moduleUrl = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`;
const { resolveViewRebuildKind } = await import(moduleUrl);

const datasets = [
  { dataset_id: 'kline', data_kind: 'DATA_KIND_TIME_SERIES' },
  { dataset_id: 'company_profile', data_kind: 'DATA_KIND_RECORD' },
];

assert.equal(resolveViewRebuildKind(datasets, 'kline'), 'time_series');
assert.equal(resolveViewRebuildKind(datasets, 'company_profile'), 'record');
assert.equal(resolveViewRebuildKind(datasets, 'missing_dataset'), 'missing');
assert.equal(resolveViewRebuildKind([], 'kline'), 'missing');

// 数据浏览：数据集/视图均按 primary dataset 的 data_kind 推断查询类型
assert.equal(resolveViewRebuildKind(datasets, 'kline'), 'time_series'); // 时序数据集 → 时序读取
assert.equal(resolveViewRebuildKind(datasets, 'company_profile'), 'record'); // 记录数据集 → 记录读取
assert.equal(resolveViewRebuildKind(datasets, ''), 'missing'); // 未选对象

console.log('metadata utils tests passed');
