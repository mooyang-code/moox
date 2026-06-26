import { loadConfigFromFile } from 'vite';

const loaded = await loadConfigFromFile({ command: 'serve', mode: 'development' }, 'vite.config.ts');
if (!loaded?.config) {
  console.error('Failed to load vite.config.ts');
  process.exit(1);
}

const config = loaded.config;
const proxy = config.server?.proxy || {};

const expectedTargets = new Map([
  ['/api/control', 'http://106.53.107.122:20103'],
  ['/trpc.moox.server', 'http://localhost:20102'],
]);

const mismatches = [];
for (const [proxyPath, expectedTarget] of expectedTargets) {
  const actualTarget = proxy[proxyPath]?.target || '';
  if (actualTarget !== expectedTarget) {
    mismatches.push({ proxyPath, expectedTarget, actualTarget: actualTarget || '<missing>' });
  }
}

for (const forbiddenPath of ['/api/storage/metadata', '/api/storage/access', '/api/storage/view']) {
  if (proxy[forbiddenPath]) {
    mismatches.push({
      proxyPath: forbiddenPath,
      expectedTarget: '<absent>',
      actualTarget: proxy[forbiddenPath]?.target || '<configured>',
    });
  }
}

if (mismatches.length > 0) {
  console.error('Unexpected Vite dev proxy target(s):');
  for (const item of mismatches) {
    console.error(`- ${item.proxyPath}: expected ${item.expectedTarget}, got ${item.actualTarget}`);
  }
  process.exit(1);
}

console.log(`Vite dev proxy targets verified: ${expectedTargets.size} route(s).`);
