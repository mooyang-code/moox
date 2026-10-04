import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const source = fs.readFileSync(path.join(root, "src/views/data/view-browse/index.vue"), "utf8");

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

assert(source.includes("viewIds?: string[]"), "view browse must support an explicit View allowlist");
assert(source.includes("activeViewId?: string"), "view browse must support an active View");
assert(source.includes("hideTechnicalIdentity?: boolean"), "view browse must support hiding technical identity");
assert(source.includes("visibleViews.length > 1"), "view tabs must only render when multiple Views are available");
assert(source.includes("preview-pager"), "view browse must render the preview pager");
assert(source.includes("DEFAULT_VIEW_PAGE_SIZE"), "view browse must use a bounded preview page size");
assert(source.includes("sortArrowClass('data_time', 'asc')"), "time-series time header must expose ascending sort");
assert(source.includes("sortArrowClass('data_time', 'desc')"), "time-series time header must expose descending sort");
assert(source.includes("sortArrowClass('version', 'asc')"), "record version header must expose ascending sort");
assert(source.includes("sortArrowClass('version', 'desc')"), "record version header must expose descending sort");
assert(source.includes("<KlineModal"), "view browse must retain K-line inspection");
assert(source.includes("openDetail"), "view browse must retain row details");

console.log("view browse filtering, pagination, sorting, and K-line parity ok");
