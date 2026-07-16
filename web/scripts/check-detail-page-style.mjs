import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");

const pageShellFiles = [
  "src/views/settings/spaces/index.vue",
  "src/views/settings/secrets/index.vue",
  "src/views/settings/service-deployments/index.vue",
  "src/views/data/sources/index.vue",
  "src/views/data/subjects/index.vue",
  "src/views/data/fields/index.vue",
  "src/views/data/datasets/index.vue",
  "src/views/data/views/index.vue",
  "src/views/data/browse/index.vue",
  "src/views/data/view-browse/index.vue",
  "src/views/data/import/index.vue",
  "src/views/factor/definitions/index.vue",
  "src/views/factor/bindings/index.vue",
  "src/views/collector/cloud-node/cloud-node.vue",
  "src/views/collector/cloud-node/function-package-manage.vue",
  "src/views/collector/collector-rules/collector-rules.vue",
  "src/views/collector/task-instances/task-instances.vue",
  "src/views/trading/account-overview/account-overview.vue",
  "src/views/trading/position-detail/position-detail.vue",
  "src/views/trading/trade-record/trade-record.vue",
  "src/views/container/ssh-hosts/ssh-hosts.vue",
  "src/views/ops/storage/nodes.vue",
  "src/views/ops/storage/routes.vue",
  "src/views/ops/storage/archive.vue"
];

const tableFiles = [
  ...pageShellFiles,
  "src/views/data/datasets/components/dataset-column-panel.vue",
  "src/views/data/datasets/components/dataset-subject-panel.vue",
  "src/views/data/views/components/view-column-panel.vue",
  "src/views/collector/cloud-account/cloud-account-manage.vue"
];

function read(relativePath) {
  return fs.readFileSync(path.join(root, relativePath), "utf8");
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

for (const relativePath of pageShellFiles) {
  const source = read(relativePath);
  assert(source.includes("moox-page"), `${relativePath}: missing moox-page shell`);
  assert(source.includes("moox-inner"), `${relativePath}: missing moox-inner surface`);
}

for (const relativePath of tableFiles) {
  const source = read(relativePath);
  const tableTags = source.match(/<a-table(?=[\s>])[\s\S]*?>/g) || [];
  for (const [index, tableTag] of tableTags.entries()) {
    assert(tableTag.includes('size="small"'), `${relativePath}: table ${index + 1} must use size=small`);
    assert(
      tableTag.includes(':bordered="{ cell: true }"'),
      `${relativePath}: table ${index + 1} must use cell borders`
    );
  }
}

console.log(`detail page style ok: ${pageShellFiles.length} pages, ${tableFiles.length} table files`);
