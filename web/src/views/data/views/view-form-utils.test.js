import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const source = await readFile(new URL("./view-form-utils.ts", import.meta.url), "utf8");
const viewPageSource = await readFile(new URL("./index.vue", import.meta.url), "utf8");
const viewColumnPanelSource = await readFile(new URL("./components/view-column-panel.vue", import.meta.url), "utf8");

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
  buildTimeSeriesViewFilterJSON,
  defaultViewEngine,
  defaultViewGrainKeys,
  freqOptionsForPrimaryDataset,
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
assert.deepEqual(freqOptionsForPrimaryDataset(datasets, "spot_kline"), ["1m", "1h", "1d"]);
assert.deepEqual(freqOptionsForPrimaryDataset(datasets, "news"), []);
assert.deepEqual(
  availableIncludedDatasets(datasets, "spot_kline", "1h").map(item => item.dataset_id),
  ["kline", "factor", "spot_kline"]
);
assert.deepEqual(
  availableIncludedDatasets(datasets, "spot_kline", "1m").map(item => item.dataset_id),
  ["spot_kline"]
);
assert.deepEqual(JSON.parse(buildTimeSeriesViewFilterJSON("{}", "1m")), { freq: "1m" });
assert.deepEqual(JSON.parse(buildTimeSeriesViewFilterJSON('{"subject_id":"BTC-USDT"}', "1m")), {
  subject_id: "BTC-USDT",
  freq: "1m"
});
assert.deepEqual(removePrimaryFromIncludes("kline", ["kline", "factor", "news"]), ["factor", "news"]);
assert.deepEqual(defaultViewGrainKeys(datasets, "kline"), ["subject_id", "freq", "data_time"]);
assert.deepEqual(defaultViewGrainKeys(datasets, "news"), ["record_id", "version"]);
assert.equal(defaultViewEngine(datasets, "kline"), "duckdb");
assert.equal(defaultViewEngine(datasets, "news"), "bleve");
assert.doesNotMatch(viewPageSource, /label="粒度键"/);
assert.doesNotMatch(viewPageSource, /grainTags/);
assert.doesNotMatch(viewPageSource, /label="引擎"/);
assert.match(viewPageSource, /field="view_freq" label="频率"/);
assert.match(viewPageSource, /请选择频率/);
assert.match(viewColumnPanelSource, /title="中文名"/);
assert.match(viewColumnPanelSource, /field="display_name" label="中文名"/);
assert.match(viewColumnPanelSource, /validateChineseDisplayName/);
assert.match(viewColumnPanelSource, /attributes:\s*\{\s*display_name:\s*form\.display_name\s*\}/);

const draft = buildDraftViewColumns("kline", ["factor"], {
  kline: [
    { space_id: "crypto", dataset_id: "kline", column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE", status: "active", attributes: { display_name: "收盘价" } },
    { space_id: "crypto", dataset_id: "kline", column_name: "volume", value_type: "FIELD_VALUE_TYPE_DOUBLE", status: "active", attributes: { display_name: "成交量" } }
  ],
  factor: [
    { space_id: "crypto", dataset_id: "factor", column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE", status: "active", attributes: { display_name: "因子收盘" } },
    {
      space_id: "crypto",
      dataset_id: "factor",
      column_name: "alpha-score",
      value_type: "FIELD_VALUE_TYPE_DOUBLE",
      status: "active",
      attributes: { display_name: "阿尔法" }
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

assert.deepEqual(
  draft.map(item => item.attributes?.display_name),
  ["收盘价", "成交量", "因子收盘", "阿尔法"]
);

const draftWithoutDisplayName = buildDraftViewColumns("kline", [], {
  kline: [{ space_id: "crypto", dataset_id: "kline", column_name: "legacy_close", value_type: "FIELD_VALUE_TYPE_DOUBLE", status: "active" }]
});
assert.equal(draftWithoutDisplayName[0].attributes?.display_name, "未命名");

console.log("view form utils tests passed");
