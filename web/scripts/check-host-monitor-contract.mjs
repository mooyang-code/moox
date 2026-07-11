import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import ts from 'typescript';

const root = process.cwd();
const sources = [
  path.join(root, 'src/api/modules/host-monitor.ts'),
  path.join(root, 'src/views/container/resource-monitor/resource-monitor.vue'),
].map((file) => fs.readFileSync(file, 'utf8')).join('\n');

const required = [
  'host_id',
  'maxAvailableFilesystemUsage',
  'aggregateNetworkRate',
  'storage_available',
  'data_gap',
  'value="3d"',
  'selectedHostID',
  'filesystems',
  'rate_available',
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

console.log('host monitor frontend contract passed');
