import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const source = await readFile(new URL("./view-form-utils.ts", import.meta.url), "utf8");
const viewPageSource = await readFile(new URL("./index.vue", import.meta.url), "utf8");

const outputText = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2020,
    target: ts.ScriptTarget.ES2020
  }
}).outputText;
const moduleUrl = `data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`;
const {
  buildDraftViewColumns,
  buildViewDatasetIds,
  availableIncludedDatasets,
  defaultViewEngine,
  defaultViewGrainKeys,
  removePrimaryFromIncludes
} = await import(moduleUrl);

const datasets = [
  { dataset_id: "kline", data_kind: "DATA_KIND_TIME_SERIES", freqs: ["1h"], name: "K线" },
  { dataset_id: "factor", data_kind: "DATA_KIND_TIME_SERIES", freqs: ["1h"], name: "因子" },
  { dataset_id: "spot_kline", data_kind: "DATA_KIND_TIME_SERIES", freqs: ["1m", "1h", "1d"], name: "现货K线" },
  { dataset_id: "kline_5m", data_kind: "DATA_KIND_TIME_SERIES", freqs: ["5m"], name: "5分钟K线" },
  { dataset_id: "news", data_kind: "DATA_KIND_RECORD", name: "新闻" }
];

assert.deepEqual(buildViewDatasetIds("kline", ["factor", "kline", "factor"]), ["kline", "factor"]);
assert.deepEqual(
  availableIncludedDatasets(datasets).map(item => item.dataset_id),
  ["kline", "factor", "spot_kline", "kline_5m", "news"]
);
assert.deepEqual(removePrimaryFromIncludes("kline", ["kline", "factor", "news"]), ["factor", "news"]);
assert.deepEqual(defaultViewGrainKeys(datasets, "kline"), ["subject_id", "freq", "data_time"]);
assert.deepEqual(defaultViewGrainKeys(datasets, "news"), ["record_id", "version"]);
assert.equal(defaultViewEngine(datasets, "kline"), "duckdb");
assert.equal(defaultViewEngine(datasets, "news"), "bleve");
assert.doesNotMatch(viewPageSource, /label="粒度键"/);
assert.doesNotMatch(viewPageSource, /grainTags/);
assert.doesNotMatch(viewPageSource, /label="引擎"/);

const draft = buildDraftViewColumns("kline", ["factor"], {
  kline: [
    { space_id: "crypto", dataset_id: "kline", column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE", status: "active" },
    { space_id: "crypto", dataset_id: "kline", column_name: "volume", value_type: "FIELD_VALUE_TYPE_DOUBLE", status: "active" }
  ],
  factor: [
    { space_id: "crypto", dataset_id: "factor", column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE", status: "active" },
    {
      space_id: "crypto",
      dataset_id: "factor",
      column_name: "alpha-score",
      value_type: "FIELD_VALUE_TYPE_DOUBLE",
      status: "active"
    }
  ]
});

assert.deepEqual(
  draft.map(item => [item.column_name, item.origin_id, item.sort_order]),
  [
    ["kline.close", "kline.close", 1],
    ["kline.volume", "kline.volume", 2],
    ["factor.close", "factor.close", 3],
    ["factor.alpha-score", "factor.alpha-score", 4]
  ]
);

console.log("view form utils tests passed");
