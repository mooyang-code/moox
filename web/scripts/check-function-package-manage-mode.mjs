import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const packageManageSource = readFileSync(join(webRoot, "src/views/collector/cloud-function/function-package-manage.vue"), "utf8");
const cloudFunctionSource = readFileSync(join(webRoot, "src/views/collector/cloud-function/cloud-function.vue"), "utf8");

assert.match(
  packageManageSource,
  /import\s+\{[^}]*\bModal\b[^}]*\}\s+from\s+['"]@arco-design\/web-vue['"]/,
  "dialog mode must use the real Arco Modal component",
);

assert.match(
  packageManageSource,
  /:is="packageManageContainer"/,
  "dialog and standalone modes must share a resolved container component",
);

assert.match(
  packageManageSource,
  /mode\?:\s*['"]page['"]\s*\|\s*['"]modal['"]/,
  "function package manage must expose an explicit page/modal mode",
);

assert.match(
  packageManageSource,
  /const\s+packageManageMode\s*=\s*computed\(\(\)\s*=>\s*props\.mode\s*\|\|\s*['"]page['"]\)/,
  "standalone route mode must be the default when no explicit mode is passed",
);

assert.match(
  packageManageSource,
  /class:\s*['"]function-package-page['"]/,
  "standalone route mode must render a page container",
);

assert.doesNotMatch(
  packageManageSource,
  /getCurrentInstance|vnode\.props|hasInputModelValue/,
  "mode selection must not depend on fragile vnode prop introspection",
);

assert.doesNotMatch(
  packageManageSource,
  /<component\b[\s\S]*:is="standalone\s*\?\s*'div'\s*:\s*'a-modal'"/,
  "do not render Arco modal through a string dynamic component",
);

assert.match(
  cloudFunctionSource,
  /<FunctionPackageManage[\s\S]*\bmode=["']modal["'][\s\S]*\/>/,
  "collector functions page must open package management in a modal instead of rendering it inline",
);

console.log("function package manage mode checks passed");
