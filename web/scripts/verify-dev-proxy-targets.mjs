import { loadConfigFromFile } from 'vite';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const loaded = await loadConfigFromFile({ command: 'serve', mode: 'development' }, 'vite.config.ts');
if (!loaded?.config) {
  console.error('Failed to load vite.config.ts');
  process.exit(1);
}

const config = loaded.config;
const proxy = config.server?.proxy || {};
const gatewaySource = readFileSync(resolve('src/api/gateway.ts'), 'utf8');

assert.deepEqual(
  Object.keys(proxy),
  [],
  'Vite dev server must not proxy API requests; frontend should call the gateway directly',
);
assert.match(gatewaySource, /DEFAULT_GATEWAY_PORT\s*=\s*'11000'/, 'gateway helper must default to port 11000');
assert.match(gatewaySource, /window\.location\.hostname/, 'gateway helper must derive host from browser URL hostname');
assert.doesNotMatch(gatewaySource, /window\.location\.host/, 'gateway helper must not reuse the web-host/dev-server port');

console.log('Gateway direct mode verified: no Vite API proxy, default gateway port 11000.');
