import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import vm from "node:vm";
import ts from "typescript";

const sourcePath = join(process.cwd(), "src/api/admin/space-header.ts");
const source = readFileSync(sourcePath, "utf8");
const transpiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2020,
  },
}).outputText;

const exports = {};
const context = vm.createContext({
  exports,
  module: { exports },
  console,
});
vm.runInContext(transpiled, context, { filename: sourcePath });

const { readSelectedSpaceId, setSelectedSpaceIdCache } = context.module.exports;

function setLocalStorageValue(value) {
  context.localStorage = {
    getItem(key) {
      return key === "spaceStore" ? value : null;
    },
  };
}

setLocalStorageValue(JSON.stringify({ selectedSpaceId: "crypto" }));
assert.equal(readSelectedSpaceId(), "crypto");

setSelectedSpaceIdCache("cache_space");
assert.equal(readSelectedSpaceId(), "cache_space");
setSelectedSpaceIdCache("");

setLocalStorageValue(JSON.stringify({ state: { selectedSpaceId: "hk_stock" } }));
assert.equal(readSelectedSpaceId(), "hk_stock");

setSelectedSpaceIdCache("");
setLocalStorageValue("{bad json");
assert.equal(readSelectedSpaceId(), "");

console.log("admin space header checks passed");
