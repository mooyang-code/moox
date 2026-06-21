import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const pages = [
  'src/views/collector/cloud-account/cloud-account-manage.vue',
  'src/views/collector/cloud-function/cloud-function-async.vue',
  'src/views/collector/cloud-function/cloud-function.vue',
  'src/views/collector/cloud-function/function-package-manage.vue',
  'src/views/collector/collector-rules/collector-rules.vue',
  'src/views/collector/task-instances/task-instances.vue',
  'src/views/container/container-list/container-list.vue',
  'src/views/container/file-management/file-management.vue',
  'src/views/container/resource-monitor/resource-monitor.vue',
  'src/views/container/service-status/service-status.vue',
  'src/views/container/ssh-file-manager/ssh-file-manager.vue',
  'src/views/container/ssh-hosts/ssh-hosts.vue',
  'src/views/container/ssh-sessions/ssh-sessions.vue',
  'src/views/container/ssh-terminal/ssh-terminal.vue',
  'src/views/strategy/strategy-list/strategy-list.vue',
  'src/views/trading/account-overview/account-overview.vue',
  'src/views/trading/position-detail/position-detail.vue',
  'src/views/trading/trade-record/trade-record.vue',
];

const missing = pages.filter((page) => {
  const source = readFileSync(resolve(page), 'utf8');
  return !source.includes('SpaceContextBar');
});

if (missing.length > 0) {
  console.error(`Missing SpaceContextBar in ${missing.length} page(s):`);
  for (const page of missing) {
    console.error(`- ${page}`);
  }
  process.exit(1);
}

console.log(`SpaceContextBar verified in ${pages.length} page(s).`);
