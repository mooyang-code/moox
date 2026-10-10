import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

// 服务部署页的契约：两个页签（服务、网关路由），只能查看和启用 / 停用，主机与部署来自 moox.toml。
const read = (file) => fs.readFileSync(path.join(process.cwd(), 'src/views/ops/deployments', file), 'utf8');
const index = read('index.vue');
const services = read('services-tab.vue');
const routes = read('routes-tab.vue');
const drawer = read('component-drawer.vue');

const checks = [
  [index, ['PageTitleTabs', 'aria-label="服务部署"', /label:\s*["']服务["']/, /label:\s*["']网关路由["']/, 'deployments-content']],
  [services, ['createLatestRequestGuard', 'onActivated', 'sysdeployApi.setPlacementStatus', 'sysdeployApi.setHostStatus', 'monitorApi.getOverview', '受保护', '显示已停用']],
  [routes, ['sysdeployApi.getHostRoutes', '快照版本', '同步状态', 'filterRoutes']],
  [drawer, ['aclSummary', '组件目录', '运行状态']]
];
const forbidden = ['<a-tabs', 'type="rounded"', '新增', 'SyncHostPlacements', 'DeleteHost', 'listServiceDeployments', 'GatewayNode'];

const failures = [];
for (const [source, tokens] of checks) {
  for (const token of tokens) {
    const ok = token instanceof RegExp ? token.test(source) : source.includes(token);
    if (!ok) failures.push(`missing ${token}`);
  }
}
for (const source of [index, services, routes, drawer]) {
  for (const token of forbidden) {
    if (source.includes(token)) failures.push(`forbidden ${token}`);
  }
}
for (const retired of ['src/views/ops/service-management', 'src/views/settings/service-deployments']) {
  if (fs.existsSync(path.join(process.cwd(), retired))) failures.push(`retired page remains: ${retired}`);
}

if (failures.length) {
  console.error(`deployments frontend contract failed: ${failures.join('; ')}`);
  process.exit(1);
}

console.log('deployments frontend contract passed');
