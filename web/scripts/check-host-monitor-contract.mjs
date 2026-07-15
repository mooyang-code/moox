import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import ts from 'typescript';

const root = process.cwd();
const sources = [
  path.join(root, 'src/api/modules/host-monitor.ts'),
  path.join(root, 'src/views/ops/host-workbench/index.vue'),
  path.join(root, 'src/views/ops/host-workbench/host-monitor.vue'),
  path.join(root, 'src/views/ops/host-workbench/host-monitor-card-grid.vue'),
  path.join(root, 'src/views/ops/host-workbench/host-monitor-master-detail.vue'),
  path.join(root, 'src/views/ops/host-workbench/host-monitor-detail.vue'),
  path.join(root, 'src/router/route.ts'),
].map((file) => fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : '').join('\n');

const required = [
  'host_id',
  'maxAvailableFilesystemUsage',
  'aggregateNetworkRate',
  'storage_available',
  'data_gap',
  'key="monitor"',
  'title="主机监控"',
  'tab: "monitor"',
  'getCurrentMetrics',
  'listSSHHosts',
  '15_000',
  "'cards'",
  'value="master"',
  'host-monitor-card-grid',
  'host-monitor-master-detail',
  'host-monitor-detail',
  'value="3d"',
  'selectedRow',
  'filesystems',
  'rate_available',
  'aria-pressed',
  'aria-label="自动刷新"',
  'min-height: 0',
  '文件系统',
  '磁盘 I/O',
  '网络接口',
  'historyHasRenderableData',
];

const missing = required.filter((token) => !sources.includes(token));
if (missing.length) {
  console.error(`host monitor contract missing: ${missing.join(', ')}`);
  process.exit(1);
}

const mappingPath = path.join(root, 'src/api/modules/host-monitor-mapping.ts');
const mappingSource = fs.readFileSync(mappingPath, 'utf8');
const compiled = ts.transpileModule(mappingSource, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } }).outputText;
const mapping = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);

if (mapping.maxAvailableFilesystemUsage([{ percent: 0, percent_available: true }]) !== 0) {
  throw new Error('0% filesystem usage must remain available');
}
if (!mapping.memoryUsageAvailable(1024, true)) {
  throw new Error('0% memory usage with a valid total must remain available');
}
const rate = mapping.aggregateNetworkRate([
  { rx_speed: 10, tx_speed: 5, rate_available: true },
  { rx_speed: 7, tx_speed: 3, rate_available: true },
]);
if (!rate || rate.rx !== 17 || rate.tx !== 8) {
  throw new Error('network rates must aggregate across available interfaces');
}
if (mapping.metricValueAvailable('offline', true)) {
  throw new Error('offline hosts must not present stale values as current metrics');
}
if (!mapping.metricValueAvailable('online', true)) {
  throw new Error('online available metrics must remain visible');
}
if (mapping.historyHasRenderableData([{ cpu_available: false, memory_available: false, disk_available: false }])) {
  throw new Error('history without available percentage metrics must render the empty state');
}
if (!mapping.historyHasRenderableData([{ cpu_available: false, memory_available: true, disk_available: false }])) {
  throw new Error('history with one available percentage metric must render a chart');
}

console.log('host monitor frontend contract passed');
