import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const source = fs.readFileSync(path.join(root, "src/views/data/browse/index.vue"), "utf8");

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

assert(source.includes("usesPreviewPager"), "data browse must detect skipped total state");
assert(source.includes("previewPagerText"), "data browse must show preview pager hint");
assert(source.includes("timeSeriesTablePagination"), "data browse time-series table must switch pagination mode");
assert(source.includes("recordTablePagination"), "data browse record table must switch pagination mode");
assert(source.includes("preview-pager"), "data browse must render the preview pager");
assert(source.includes("sortArrowClass('data_time', 'asc')"), "time-series time header must expose ascending sort");
assert(source.includes("sortArrowClass('data_time', 'desc')"), "time-series time header must expose descending sort");
assert(source.includes("sortArrowClass('version', 'asc')"), "record version header must expose ascending sort");
assert(source.includes("sortArrowClass('version', 'desc')"), "record version header must expose descending sort");

console.log("data browse pagination and sorting parity ok");
