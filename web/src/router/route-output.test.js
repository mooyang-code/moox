import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';

const root = process.cwd();
const routeConfig = fs.readFileSync(path.join(root, 'src/store/modules/route-config.ts'), 'utf8');
const routeOutput = fs.readFileSync(path.join(root, 'src/router/route-output.ts'), 'utf8');
const staticRoutes = fs.readFileSync(path.join(root, 'src/router/route.ts'), 'utf8');

assert.match(
  routeConfig,
  /router\.hasRoute\(route\.name\)/,
  'dynamic route registration must skip routes already provided by static route definitions',
);

assert.ok(
  routeOutput.includes('/src\\/views\\/') || routeOutput.includes('/src/views/'),
  'route module matching must normalize Vite glob keys such as /src/views/data/views/index.vue',
);

assert.doesNotMatch(
  routeOutput,
  /\^\.\*\\\/views\\\//,
  'route module matching must not greedily strip nested directory names like data/views/index',
);

assert.match(
  staticRoutes,
  /path:\s*["']\/data\/list["'][\s\S]*redirect:\s*["']\/data\/browse["']/,
  'legacy data list route must redirect to the data browse page',
);

console.log('route-output tests passed');
