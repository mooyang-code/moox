import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const source = fs.readFileSync(
  path.join(process.cwd(), 'src/views/collector/task-instances/task-instances.vue'),
  'utf8',
);

const required = [
  'class="task-toolbar"',
  'class="task-filters"',
  ':bordered="{ cell: true }"',
  ':scroll="{ x: 1810 }"',
  'class="task-id-button"',
  '<icon-eye />',
  'type="primary"',
  'size="mini"',
];

const forbidden = [
  '<h2>任务实例</h2>',
  'class="task-result-pane"',
  ':row-selection=',
  ':selected-keys=',
  '@select=',
  '@select-all=',
  'selectedKeys',
  'const select =',
  'const selectAll =',
  'y: 500',
  '<a-tag bordered',
];

const missing = required.filter((token) => !source.includes(token));
const remaining = forbidden.filter((token) => source.includes(token));
const taskIDTruncationAligned = /\.task-id-button\s*\{[^}]*display:\s*block;[^}]*text-overflow:\s*ellipsis;/s.test(source);

if (missing.length || remaining.length || !taskIDTruncationAligned) {
  console.error(
    `collector task style contract failed; missing: ${missing.join(', ') || 'none'}; remaining: ${remaining.join(', ') || 'none'}; task id truncation aligned: ${taskIDTruncationAligned}`,
  );
  process.exit(1);
}

console.log('collector task page style contract passed');
