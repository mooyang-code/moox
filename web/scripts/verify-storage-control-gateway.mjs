import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const source = readFileSync(resolve('src/api/storage/http.ts'), 'utf8');

assert.doesNotMatch(
  source,
  /\/api\/storage\//,
  'frontend storage APIs must not call /api/storage directly',
);

assert.match(
  source,
  /\/api\/control\/\$\{storageServiceID\(group\)\}\/\$\{method\}/,
  'frontend storage APIs must go through /api/control/{service}/{method}',
);

assert.match(source, /localStorage\.getItem\('user-info'\)/, 'storage APIs must read frontend login state');
assert.match(source, /headers\.Authorization\s*=\s*token/, 'storage APIs must set Authorization token');
assert.match(source, /headers\['X-Access-Token'\]\s*=\s*token/, 'storage APIs must set X-Access-Token token');

for (const serviceID of ['storage_metadata', 'storage_access', 'storage_view']) {
  assert.match(
    source,
    new RegExp(`${serviceID}`),
    `frontend storage API must map a group to ${serviceID}`,
  );
}

console.log('storage control gateway path verified');
