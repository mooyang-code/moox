import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const root = process.cwd();
const read = (file) => fs.readFileSync(path.join(root, file), 'utf8');
const trackedWithoutProtoReservation = [
  'web/src/views/collector/task-instances/task-instances.vue',
  'modules/collector/proto/collectorgen/collector.pb.go',
  'modules/collector/internal/domain/task_instance.go',
  'modules/collector/internal/store/task_instance.go',
  'modules/collector/internal/rpc/convert.go',
  'modules/collector/internal/rpc/service.go',
  'modules/collector/schema/collector.sql',
  'docs/云节点执行平台架构.md',
];
const joined = trackedWithoutProtoReservation.map(read).join('\n');
const web = read(trackedWithoutProtoReservation[0]);
const proto = read('modules/collector/proto/collector.proto');
const schema = read('modules/collector/schema/collector.sql');

const forbidden = ['planned_exec_node', 'PlannedExecNode', 'c_planned_exec_node', '计划节点'];
const remaining = forbidden.filter((token) => joined.includes(token));
const requirements = [
  [web.includes('pageSize: 20'), 'frontend default page size 20'],
  [web.includes(':scroll="{ x: 1650 }"'), 'reduced table scroll width'],
  [(proto.match(/reserved 12;/g) || []).length === 2, 'reserved field number 12 twice'],
  [(proto.match(/reserved "planned_exec_node";/g) || []).length === 2, 'reserved field name twice'],
  [!proto.includes('string planned_exec_node = 12;'), 'removed protobuf field declaration'],
  [
    schema.includes(
      'idx_collector_instances_exec ON t_collector_task_instances(c_last_exec_status)',
    ),
    'status-only execution index',
  ],
];
const missing = requirements.filter(([ok]) => !ok).map(([, label]) => label);

if (remaining.length || missing.length) {
  console.error(
    `collector planned-node removal failed; remaining: ${remaining.join(', ') || 'none'}; missing: ${missing.join(', ') || 'none'}`,
  );
  process.exit(1);
}

console.log('collector planned-node removal contract passed');
