import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';

const root = process.cwd();
const routeConfig = fs.readFileSync(path.join(root, 'src/store/modules/route-config.ts'), 'utf8');
const routeOutput = fs.readFileSync(path.join(root, 'src/router/route-output.ts'), 'utf8');
const staticRoutes = fs.readFileSync(path.join(root, 'src/router/route.ts'), 'utf8');
const systemMenu = fs.readFileSync(path.join(root, 'src/mock/_data/system_menu.ts'), 'utf8');

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

assert.match(
  staticRoutes,
  /path:\s*["']\/data\/view-browse["'][\s\S]*name:\s*["']data-view-browse["']/,
  'view browse route must be registered as a standalone page',
);

assert.match(
  systemMenu,
  /directory\("0220",\s*"02",\s*"\/data\/views",\s*"data-views"/,
  'query views must be a menu group',
);

assert.match(
  systemMenu,
  /menu\("022001",\s*"0220",\s*"\/data\/views",\s*"data-view-list"/,
  'query views group must include view list',
);

assert.match(
  systemMenu,
  /menu\("022002",\s*"0220",\s*"\/data\/view-browse",\s*"data-view-browse"/,
  'query views group must include view browse',
);

assert.match(
  systemMenu,
  /directory\("0230",\s*"02",\s*"\/data\/overview",\s*"data-mgmt",\s*"data-mgmt",\s*2\)/,
  'data management group must be a direct child of data assets and appear right after data modeling',
);

assert.doesNotMatch(
  systemMenu,
  /directory\("0230",\s*"0210",\s*"\/data\/overview",\s*"data-mgmt"/,
  'data management group must not be nested under data modeling',
);

assert.match(
  systemMenu,
  /directory\("0210",\s*"02",[\s\S]*directory\("0230",\s*"02",[\s\S]*directory\("0220",\s*"02"/,
  'data management group must be ordered between data modeling and query views',
);

console.log('route-output tests passed');
