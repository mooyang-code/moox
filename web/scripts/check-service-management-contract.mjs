import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const source = fs.readFileSync(path.join(process.cwd(), 'src/views/ops/service-management/index.vue'), 'utf8');
const nodes = fs.readFileSync(path.join(process.cwd(), 'src/views/ops/service-management/gateway-nodes.vue'), 'utf8');
const instances = fs.readFileSync(path.join(process.cwd(), 'src/views/settings/service-deployments/index.vue'), 'utf8');
const required = [
  'PageTitleTabs',
  'aria-label="服务管理"',
  "label: '网关节点'",
  "label: '服务实例'",
  "label: '可用性监控'",
  "label: '应用指标'",
  'management-content',
];
const forbidden = ['<h2>服务管理</h2>', 'type="rounded"', '<a-tabs'];
const missing = required.filter((token) => !source.includes(token));
const remaining = forbidden.filter((token) => source.includes(token));
const nodeRequired = ['row-key="node_id"', '查看路由', 'gatewayHashState', 'icon-eye', 'icon-edit', '@before-ok="submit"', 'onActivated', 'createLatestRequestGuard', 'reportControlError'];
const instanceRequired = [':row-key="serviceDeploymentRowKey"', 'filters.node_id', 'gateway_service_id', 'gateway_enabled', 'validateGatewayDeployment', '@before-ok="submit"', 'createLatestRequestGuard', 'reportControlError'];
const instanceForbidden = ['healthLabel', 'monitorApi', 'loadLatestHealthResults'];
const missingNode = nodeRequired.filter((token) => !nodes.includes(token));
const missingInstance = instanceRequired.filter((token) => !instances.includes(token));
const forbiddenInstance = instanceForbidden.filter((token) => instances.includes(token));

if (missing.length || remaining.length || missingNode.length || missingInstance.length || forbiddenInstance.length) {
  console.error(`service management layout contract failed; missing: ${missing.join(', ') || 'none'}; node: ${missingNode.join(', ') || 'none'}; instances: ${missingInstance.join(', ') || 'none'}; forbidden instances: ${forbiddenInstance.join(', ') || 'none'}; remaining: ${remaining.join(', ') || 'none'}`);
  process.exit(1);
}

console.log('service management frontend contract passed');
