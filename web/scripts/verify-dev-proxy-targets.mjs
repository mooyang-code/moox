import { loadConfigFromFile } from 'vite';

const loaded = await loadConfigFromFile({ command: 'serve', mode: 'development' }, 'vite.config.ts');
if (!loaded?.config) {
  console.error('Failed to load vite.config.ts');
  process.exit(1);
}

const config = loaded.config;
const proxy = config.server?.proxy || {};

// 管理台 IP 不写死：由 vite.config.ts 的 dynamicRouter 取浏览器请求 Host 头 +
// 固定端口动态拼装。这里只校验静态默认 target（localhost + 端口）与 router 函数存在，
// 不再断言任何公网 IP，避免与“IP 随浏览器 URL”的设计冲突。
const expectedTargets = new Map([
  ['/api/admin', 'http://localhost:11000'],
  ['/trpc.moox.server', 'http://localhost:10080'],
]);

const mismatches = [];
for (const [proxyPath, expectedTarget] of expectedTargets) {
  const entry = proxy[proxyPath];
  const actualTarget = entry?.target || '';
  if (actualTarget !== expectedTarget) {
    mismatches.push({ proxyPath, expectedTarget, actualTarget: actualTarget || '<missing>' });
  }
  if (typeof entry?.router !== 'function') {
    mismatches.push({
      proxyPath,
      expectedTarget: 'a dynamic router function',
      actualTarget: typeof entry?.router === 'undefined' ? '<missing>' : `<${typeof entry?.router}>`,
    });
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
