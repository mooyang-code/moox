import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const browseDir = path.join(root, "src/views/data/view-browse");
const read = name => fs.readFileSync(path.join(browseDir, name), "utf8");
const index = read("index.vue");
const controls = read("query-controls.vue");
const table = read("result-table.vue");

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

assert(index.includes("datasetIds?: string[]"), "view browse must take an explicit dataset allowlist");
assert(index.includes("activeDatasetId?: string"), "view browse must support an active dataset");
assert(index.includes("datasets.length > 1"), "dataset tabs must only render when multiple datasets are available");
assert(index.includes("DEFAULT_VIEW_PAGE_SIZE"), "view browse must use a bounded preview page size");
assert(index.includes("<KlineModal"), "view browse must retain K-line inspection");
assert(
  index.includes("<QueryControls") && index.includes("<ResultTable"),
  "view browse must compose query controls and result table"
);
assert(table.includes("preview-pager"), "result table must render the preview pager");
assert(
  table.includes("versionField") && table.includes("'asc'") && table.includes("'desc'"),
  "time and version headers must expose sorting"
);
assert(table.includes("openDetail"), "result table must retain row details");
assert(controls.includes("filterOperatorOptions"), "query controls must expose filter operators");
// ViewBrowse 只作为嵌入组件使用：不再有独立页头，表格行工具函数统一放在 view-browse 目录
assert(!index.includes("props.embedded"), "view browse must not keep a standalone page branch");
assert(!fs.existsSync(path.join(root, "src/views/data/browse")), "browse utilities must live in view-browse");

console.log("view browse filtering, pagination, sorting, and K-line parity ok");
