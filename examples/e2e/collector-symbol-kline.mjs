#!/usr/bin/env node
import { createCipheriv, createHash, createHmac, randomBytes } from "node:crypto";
import { chmod, readFile, writeFile } from "node:fs/promises";
import { setTimeout as sleep } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const APP_INFO = {
  app_id: "moox_frontend",
  app_key: "2521e0d21b6be0347b72bca93904a0dd",
};

const DEFAULTS = {
  phase: "setup",
  gateway: "http://127.0.0.1:11000",
  web: "http://127.0.0.1:9527",
  space: "crypto",
  symbolDataset: "e2e_binance_symbols",
  klineDataset: "e2e_binance_kline_1m",
  symbolRule: "e2e_binance_symbols_1m",
  klineRule: "e2e_binance_kline_1m",
  cloudAccount: "",
  packageName: "",
  packageVersion: "",
  zip: "",
  region: "",
  fleetPrefix: "moox-e2e-collector",
  scfCount: 50,
  timeoutSeconds: 600,
  stateFile: "",
  publishSummary: "",
  username: "mooxe2eadmin",
  password: "",
};

const SYMBOL_COLUMNS = [
  ["symbol", "string", "交易标的"],
  ["external_symbol", "string", "外部代码"],
  ["base_asset", "string", "基础资产"],
  ["quote_asset", "string", "计价资产"],
  ["status", "string", "交易状态"],
  ["min_qty", "double", "最小数量"],
  ["max_qty", "double", "最大数量"],
  ["tick_size", "double", "价格步长"],
  ["lot_size", "double", "数量步长"],
];

const KLINE_COLUMNS = [
  ["open", "double", "开盘价"],
  ["high", "double", "最高价"],
  ["low", "double", "最低价"],
  ["close", "double", "收盘价"],
  ["volume", "double", "成交量"],
  ["quote_volume", "double", "成交额"],
  ["trade_num", "int", "成交笔数"],
];
const KLINE_VIEW_ID = "e2e_binance_kline_view";

const VALUE_TYPES = {
  string: "FIELD_VALUE_TYPE_STRING",
  int: "FIELD_VALUE_TYPE_INT",
  double: "FIELD_VALUE_TYPE_DOUBLE",
};
const VALUE_TYPE_CODES = { string: 1, int: 2, double: 3 };
const ENUM_CODES = {
  DATA_KIND_RECORD: 1,
  DATA_KIND_TIME_SERIES: 2,
  DATASET_COLUMN_ORIGIN_TYPE_FIELD: 1,
  FIELD_VALUE_TYPE_STRING: 1,
  FIELD_VALUE_TYPE_INT: 2,
  FIELD_VALUE_TYPE_DOUBLE: 3,
};

const FORBIDDEN_JOB_PARAM_KEYS = new Set([
  "storage_target",
  "storage_rpc_gateway_target",
  "storage_ip_port",
  "service_gateway_target",
  "eventbus_url",
  "app_key",
  "secret",
  "hmac",
]);

const TERMINAL_FAILURES = new Set([
  4,
  6,
  "JOB_ITEM_STATUS_FAILED",
  "JOB_ITEM_STATUS_ENQUEUE_FAILED",
]);

class FatalAssertionError extends Error {}

export function parseArgs(argv) {
  const args = { ...DEFAULTS };
  if (process.env.MOOX_E2E_ADMIN_PASSWORD) {
    args.password = process.env.MOOX_E2E_ADMIN_PASSWORD;
  }
  const stringOptions = new Map([
    ["--phase", "phase"],
    ["--gateway", "gateway"],
    ["--web", "web"],
    ["--space", "space"],
    ["--symbol-dataset", "symbolDataset"],
    ["--kline-dataset", "klineDataset"],
    ["--symbol-rule", "symbolRule"],
    ["--kline-rule", "klineRule"],
    ["--cloud-account", "cloudAccount"],
    ["--package-name", "packageName"],
    ["--package-version", "packageVersion"],
    ["--zip", "zip"],
    ["--region", "region"],
    ["--fleet-prefix", "fleetPrefix"],
    ["--state-file", "stateFile"],
    ["--publish-summary", "publishSummary"],
    ["--username", "username"],
  ]);
  for (let index = 0; index < argv.length; index += 1) {
    const option = argv[index];
    if (option === "--scf-count" || option === "--timeout-seconds") {
      const value = argv[++index];
      if (!value) throw new Error(`${option} requires a value`);
      args[option === "--scf-count" ? "scfCount" : "timeoutSeconds"] = Number(value);
      continue;
    }
    if (option === "-h" || option === "--help") {
      usage();
      process.exit(0);
    }
    const property = stringOptions.get(option);
    if (!property) throw new Error(`unknown option: ${option}`);
    const value = argv[++index];
    if (!value) throw new Error(`${option} requires a value`);
    args[property] = value;
  }
  if (!["setup", "fleet", "symbols", "klines", "assert", "cleanup"].includes(args.phase)) {
    throw new Error(`unsupported phase: ${args.phase}`);
  }
  if (!args.stateFile) throw new Error("--state-file is required");
  if (!Number.isInteger(args.scfCount) || args.scfCount <= 0) {
    throw new Error("--scf-count must be a positive integer");
  }
  if (!Number.isFinite(args.timeoutSeconds) || args.timeoutSeconds <= 0) {
    throw new Error("--timeout-seconds must be positive");
  }
  if (args.phase === "fleet") {
    for (const [option, value] of [
      ["--publish-summary", args.publishSummary],
      ["--cloud-account", args.cloudAccount],
      ["--package-version", args.packageVersion],
      ["--region", args.region],
    ]) {
      if (!value) throw new Error(`${option} is required for the fleet phase`);
    }
  }
  args.gateway = trimRight(args.gateway, "/");
  args.web = trimRight(args.web, "/");
  validateDatasetID(args.symbolDataset);
  validateDatasetID(args.klineDataset);
  validateE2ERuleID(args.symbolRule);
  validateE2ERuleID(args.klineRule);
  if (args.symbolRule === args.klineRule) throw new Error("Symbol and Kline Rule IDs must differ");
  return args;
}

function usage() {
  console.log(`Usage:
  node examples/e2e/collector-symbol-kline.mjs \\
    --phase setup|fleet|symbols|klines|assert|cleanup --state-file <path> [options]

Dataset options:
  --space <id> --symbol-dataset <id> --kline-dataset <id>
  --symbol-rule <id> --kline-rule <id>

Fleet hand-off options:
  --cloud-account <id> --package-name <name> --package-version <version>
  --zip <path> --region <region> --fleet-prefix <prefix> --scf-count <n>
  --publish-summary <path>  JSON output from collector function publish.

Runtime options:
  --gateway <url> --web <url> --timeout-seconds <n>`);
}

export function validateDatasetID(datasetID) {
  const value = String(datasetID || "").trim();
  if (!value) throw new Error("dataset id is required");
  if (value.length > 30) throw new Error(`dataset id must be at most 30 characters: ${value}`);
  if (!/^[a-z][a-z0-9_]*$/.test(value)) {
    throw new Error(`dataset id must be lower_snake_case: ${value}`);
  }
  return value;
}

function validateE2ERuleID(ruleID) {
  if (!/^e2e_[a-z0-9_]+$/.test(String(ruleID || ""))) {
    throw new Error(`E2E Rule ID must start with e2e_: ${ruleID || ""}`);
  }
}

export function buildSymbolDatasetContract(spaceID, datasetID, dataNodeID) {
  return buildDatasetContract({
    spaceID,
    datasetID,
    dataNodeID,
    name: "币安标的测试",
    description: "Binance active USDT spot symbols for Collector E2E",
    dataKind: "DATA_KIND_RECORD",
    freqs: [],
    columns: SYMBOL_COLUMNS,
  });
}

export function buildKlineDatasetContract(spaceID, datasetID, dataNodeID) {
  return buildDatasetContract({
    spaceID,
    datasetID,
    dataNodeID,
    name: "币安分钟测试",
    description: "Binance closed 1m spot klines for Collector E2E",
    dataKind: "DATA_KIND_TIME_SERIES",
    freqs: ["1m"],
    columns: KLINE_COLUMNS,
  });
}

export function buildKlineViewContract(spaceID, datasetID) {
  return {
    space_id: spaceID,
    view_id: KLINE_VIEW_ID,
    name: "币安分钟视图",
    description: "Collector E2E 1m Kline query view",
    primary_dataset_id: datasetID,
    dataset_ids: [datasetID],
    grain_keys: ["subject_id", "freq", "data_time"],
    filter_json: JSON.stringify({ freq: "1m" }),
    engine: "duckdb",
    keep_duration: "0",
    status: "active",
  };
}

function buildDatasetContract({ spaceID, datasetID, dataNodeID, name, description, dataKind, freqs, columns }) {
  validateDatasetID(datasetID);
  if (!String(dataNodeID || "").trim()) throw new Error("data node id is required");
  return {
    space_id: spaceID,
    dataset_id: datasetID,
    data_source_id: "binance",
    data_node_id: dataNodeID,
    name,
    description,
    data_kind: dataKind,
    freqs: [...freqs],
    keep_duration: "0",
    status: "disabled",
    columns: columns.map(([columnName, type, displayName]) => ({
      space_id: spaceID,
      dataset_id: datasetID,
      column_name: columnName,
      origin_type: "DATASET_COLUMN_ORIGIN_TYPE_FIELD",
      origin_id: columnName,
      value_type: VALUE_TYPES[type],
      required: true,
      status: "active",
      attributes: { display_name: displayName },
    })),
  };
}

export function assertDatasetContract(actual, actualColumns, expected) {
  if (!actual) throw new Error(`dataset ${expected.dataset_id} is missing`);
  for (const key of ["space_id", "dataset_id", "data_source_id", "data_node_id", "data_kind"]) {
    const gotValue = key === "data_kind" ? enumCode(actual[key]) : actual[key];
    const wantValue = key === "data_kind" ? enumCode(expected[key]) : expected[key];
    if (gotValue !== wantValue) {
      throw new Error(`dataset ${expected.dataset_id} ${key}=${actual[key]} want=${expected[key]}`);
    }
  }
  if (!sameStringSet(actual.freqs || [], expected.freqs || [])) {
    throw new Error(`dataset ${expected.dataset_id} freqs do not match`);
  }
  const simplify = (column) => [
    column.column_name,
    enumCode(column.origin_type),
    column.origin_id,
    enumCode(column.value_type),
    column.status,
  ];
  const got = (actualColumns || []).map(simplify).sort(compareJSON);
  const want = expected.columns.map(simplify).sort(compareJSON);
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    throw new Error(`dataset ${expected.dataset_id} columns do not match: got=${JSON.stringify(got)} want=${JSON.stringify(want)}`);
  }
  if (actual.status === "active" && actual.binding_locked !== true) {
    throw new Error(`dataset ${expected.dataset_id} active binding is not locked`);
  }
}

function enumCode(value) {
  return typeof value === "string" && Object.hasOwn(ENUM_CODES, value) ? ENUM_CODES[value] : value;
}

export function activeUSDTSymbolIDs(records, memberships, mappings) {
  const recordIDs = new Set();
  for (const row of records || []) {
    const fields = recordFields(row);
    const recordID = String(row?.key?.record_id || "").trim();
    if (recordID && fields.status === "active" && fields.quote_asset === "USDT") {
      if (!recordID.endsWith("-USDT")) {
        throw new Error(`active Symbol Record is not a USDT subject: ${recordID}`);
      }
      recordIDs.add(recordID);
    }
  }
  const membershipIDs = new Set((memberships || [])
    .filter((item) => item.status === "active")
    .map((item) => item.subject_id));
  assertSameSet(recordIDs, membershipIDs, "active Symbol Record and DatasetSubject set mismatch");

  const activeMappings = new Map();
  for (const item of mappings || []) {
    if (item.status !== "active" || item.data_source_id !== "binance") continue;
    const list = activeMappings.get(item.subject_id) || [];
    list.push(item.external_symbol);
    activeMappings.set(item.subject_id, list);
  }
  for (const subjectID of recordIDs) {
    const external = activeMappings.get(subjectID) || [];
    if (external.length !== 1) {
      throw new Error(`${subjectID} must have exactly one active binance symbol`);
    }
    if (!external[0].endsWith("USDT") || external[0].includes("-")) {
      throw new Error(`${subjectID} has invalid Binance external symbol ${external[0]}`);
    }
  }
  if (!recordIDs.has("BTC-USDT")) {
    throw new Error("active Symbol set must contain BTC-USDT");
  }
  if (activeMappings.get("BTC-USDT")?.[0] !== "BTCUSDT") {
    throw new Error("BTC-USDT must map to BTCUSDT");
  }
  return new Set([...recordIDs].sort());
}

export function validateKlineJobs(jobItems, subjectSymbols, targetDatasetID) {
  if (!(subjectSymbols instanceof Map)) throw new Error("subjectSymbols must be a Map");
  const seen = new Set();
  let executeAt = "";
  for (const item of jobItems) {
    if (item.job_type !== "collect.binance.kline") {
      throw new Error(`${item.job_item_id} has invalid job_type ${item.job_type}`);
    }
    const params = item.params || {};
    assertNoRuntimeParams(params);
    const subjectID = String(params.subject_id || "");
    if (seen.has(subjectID)) throw new Error(`duplicate subject in Kline JobItems: ${subjectID}`);
    seen.add(subjectID);
    if (!subjectSymbols.has(subjectID)) throw new Error(`unexpected Kline subject ${subjectID}`);
    if (!params.symbol || params.symbol !== subjectSymbols.get(subjectID)) {
      throw new Error(`${subjectID} symbol=${params.symbol || ""} does not match external symbol`);
    }
    if (params.dataset_id !== targetDatasetID) {
      throw new Error(`${item.job_item_id} target dataset=${params.dataset_id} want=${targetDatasetID}`);
    }
    if (params.interval !== "1m") throw new Error(`${item.job_item_id} interval must be 1m`);
    if (!item.execute_at) throw new Error(`${item.job_item_id} execute_at is required`);
    if (executeAt && item.execute_at !== executeAt) {
      throw new Error(`Kline execute_at values are inconsistent`);
    }
    executeAt = item.execute_at;
    if (!String(item.job_item_id || "").includes(executeAt)) {
      throw new Error(`job_item_id ${item.job_item_id} does not contain execute_at window ${executeAt}`);
    }
  }
  if (jobItems.length !== subjectSymbols.size) {
    throw new Error(`Kline JobItem count=${jobItems.length} want=${subjectSymbols.size}`);
  }
  assertSameSet(seen, new Set(subjectSymbols.keys()), "Kline JobItem subject set mismatch");
  return { executeAt };
}

export function acceptKlineWriteEvidence(task) {
  const rows = Number(task?.rows_written || 0);
  if (rows > 0) return rows;
  if (rows === 0 && task?.zero_write_reason === "no_new_closed_kline") return 0;
  throw new Error("zero-write Kline result is missing no_new_closed_kline");
}

export function collectKlineWriteEvidence(jobItems, spaceID, targetDatasetID) {
  let rowsWritten = 0;
  const samples = [];
  const executionNodes = new Set();
  for (const item of jobItems || []) {
    if (!isSuccess(item.status)) {
      throw new Error(`Kline JobItem ${item.job_item_id || ""} is not successful`);
    }
    if (!item.execution_node) throw new Error(`Kline JobItem ${item.job_item_id || ""} has no execution_node`);
    const tasks = item.result_summary?.tasks;
    if (!Array.isArray(tasks) || tasks.length !== 1) {
      throw new Error(`Kline JobItem ${item.job_item_id || ""} must contain one task result`);
    }
    const task = tasks[0];
    const expectedScope = {
      space_id: spaceID,
      dataset_id: targetDatasetID,
      subject_id: item.params?.subject_id,
      freq: "1m",
    };
    if (JSON.stringify(stableObject(task.storage_read_scope || {})) !== JSON.stringify(stableObject(expectedScope))) {
      throw new Error(`Kline JobItem ${item.job_item_id || ""} has an invalid Storage scope`);
    }
    if (task.data_type !== "kline" || task.symbol !== item.params?.symbol || task.interval !== "1m") {
      throw new Error(`Kline JobItem ${item.job_item_id || ""} result does not match its request`);
    }
    const count = acceptKlineWriteEvidence(task);
    rowsWritten += count;
    if (count > 0) {
      executionNodes.add(item.execution_node);
      const taskSamples = task.written_row_key_samples || [];
      if (taskSamples.length === 0) {
        throw new Error(`positive Kline JobItem ${item.job_item_id || ""} has no RowKey sample`);
      }
      for (const key of taskSamples) {
        const scope = {
          space_id: key.space_id,
          dataset_id: key.dataset_id,
          subject_id: key.subject_id,
          freq: key.freq,
        };
        if (!key.data_time ||
            JSON.stringify(stableObject(scope)) !== JSON.stringify(stableObject(expectedScope))) {
          throw new Error(`Kline JobItem ${item.job_item_id || ""} has an out-of-scope RowKey sample`);
        }
      }
      samples.push(...taskSamples.map((key) => ({ key, executionNode: item.execution_node })));
    }
  }
  return { rowsWritten, samples, executionNodes };
}

export function validateKlineStorageRow(row, expectedKey) {
  const key = row?.key || {};
  if (JSON.stringify(stableObject(key)) !== JSON.stringify(stableObject(expectedKey))) {
    throw new Error(`Storage returned an unexpected Kline RowKey`);
  }
  const timestamp = new Date(key.data_time || "");
  if (!String(key.data_time || "").endsWith("Z") ||
      Number.isNaN(timestamp.getTime()) ||
      timestamp.getUTCSeconds() !== 0 ||
      timestamp.getUTCMilliseconds() !== 0) {
    throw new Error(`Kline data_time is not a UTC whole minute: ${key.data_time || ""}`);
  }
  const fields = Object.fromEntries((row.fields || []).map((field) => [
    field.field_id,
    Number(typedValue(field.value)),
  ]));
  for (const name of ["open", "high", "low", "close", "volume", "quote_volume", "trade_num"]) {
    if (!Number.isFinite(fields[name])) throw new Error(`Kline field ${name} is missing or invalid`);
  }
  if (fields.high < Math.max(fields.open, fields.close)) throw new Error("Kline high is below open/close");
  if (fields.low > Math.min(fields.open, fields.close)) throw new Error("Kline low is above open/close");
  if (fields.volume < 0 || fields.quote_volume < 0 || fields.trade_num < 0) {
    throw new Error("Kline volume/quote_volume/trade_num must be non-negative");
  }
  if (!Number.isInteger(fields.trade_num)) throw new Error("Kline trade_num must be an integer");
  return fields;
}

function stableObject(value) {
  if (Array.isArray(value)) return value.map(stableObject);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, stableObject(value[key])]));
}

export function hasMorePages(pageInfo, collectedCount, returnedCount, requestedSize) {
  if (pageInfo?.has_more === true) return true;
  const total = Number(pageInfo?.total || 0);
  if (total > 0) return collectedCount < total;
  const effectiveSize = Number(pageInfo?.size || requestedSize);
  return returnedCount > 0 && returnedCount >= effectiveSize;
}

export function assertNoRuntimeParams(value, path = "params") {
  if (!value || typeof value !== "object") return;
  for (const [key, nested] of Object.entries(value)) {
    if (FORBIDDEN_JOB_PARAM_KEYS.has(key.toLowerCase())) {
      throw new Error(`runtime parameter ${path}.${key} is forbidden in JobItem params`);
    }
    assertNoRuntimeParams(nested, `${path}.${key}`);
  }
}

export function buildSCFNodeDefinitions(prefix, count) {
  if (!Number.isInteger(count) || count <= 0) throw new Error("SCF count must be a positive integer");
  return Array.from({ length: count }, (_, index) => {
    const suffix = String(index + 1).padStart(2, "0");
    return {
      node_id: `${prefix}-${suffix}`,
      function_name: `${prefix}-${suffix}`,
      index,
    };
  });
}

export function validatePublishSummary(summary, expectedCount) {
  if (!summary?.package_id) throw new Error("publish summary is missing package_id");
  if (!["created", "updated"].includes(summary.fleet_mode)) {
    throw new Error(`publish summary has invalid fleet_mode=${summary?.fleet_mode}`);
  }
  const created = Number(summary.create_processed_count || 0);
  const deployed = Number(summary.deploy_processed_count || 0);
  if (summary.fleet_mode === "created") {
    if (created !== expectedCount) {
      throw new Error(`created fleet processed ${created} nodes; expected ${expectedCount}`);
    }
    if (deployed !== 0) throw new Error("created fleet must not report deployed nodes");
    const expectedBatches = Math.ceil(expectedCount / 5);
    if (!Array.isArray(summary.create_batch_ids) || summary.create_batch_ids.length !== expectedBatches) {
      throw new Error(`created fleet has ${summary.create_batch_ids?.length || 0} batches; expected ${expectedBatches}`);
    }
    assertUniqueBatchIDs(summary.create_batch_ids);
  } else {
    const skipped = Number(summary.deploy_skipped_count || 0);
    const batchSize = Number(summary.deploy_batch_size || 0);
    if (deployed + skipped !== expectedCount) {
      throw new Error(`updated fleet covered ${deployed + skipped} nodes; expected ${expectedCount}`);
    }
    if (created !== 0) throw new Error("updated fleet must not report created nodes");
    if (!Number.isInteger(batchSize) || batchSize <= 0) {
      throw new Error(`updated fleet has invalid deploy_batch_size=${summary.deploy_batch_size}`);
    }
    const expectedBatches = Math.ceil(deployed / batchSize);
    const deployBatchIDs = summary.deploy_batch_ids || [];
    if (!Array.isArray(deployBatchIDs) || deployBatchIDs.length !== expectedBatches) {
      throw new Error(`updated fleet has ${deployBatchIDs.length || 0} batches; expected ${expectedBatches}`);
    }
    assertUniqueBatchIDs(deployBatchIDs);
  }
  return summary;
}

export function verifiedSCFFleet(nodes, criteria, now = Date.now(), heartbeatMaxAgeMS = 120_000) {
  const matching = (nodes || []).filter((node) =>
    node.is_deleted !== true &&
    node.cloud_account_id === criteria.cloudAccount &&
    node.region === criteria.region &&
    fleetNodeIndex(node, criteria.fleetPrefix) !== null);
  if (matching.length !== criteria.scfCount) {
    throw new Error(`fleet prefix ${criteria.fleetPrefix} has ${matching.length} nodes; expected ${criteria.scfCount}`);
  }
  const nodeIDs = new Set();
  const functionNames = new Set();
  const indices = new Set();
  for (const node of matching) {
    if (!node.node_id || nodeIDs.has(node.node_id)) throw new Error(`fleet has duplicate or empty node_id ${node.node_id || ""}`);
    if (!node.function_name || functionNames.has(node.function_name)) {
      throw new Error(`fleet has duplicate or empty function_name ${node.function_name || ""}`);
    }
    if (!node.function_name.startsWith(criteria.fleetPrefix)) {
      throw new Error(`fleet node ${node.node_id} function_name does not use prefix ${criteria.fleetPrefix}`);
    }
    nodeIDs.add(node.node_id);
    functionNames.add(node.function_name);
    const index = fleetNodeIndex(node, criteria.fleetPrefix);
    if (!Number.isInteger(index) || index < 0 || index >= criteria.scfCount || indices.has(index)) {
      throw new Error(`fleet node ${node.node_id} has invalid or duplicate index ${index}`);
    }
    indices.add(index);
    for (const [field, want] of Object.entries({
      package_id: criteria.packageID,
      package_version: criteria.packageVersion,
      running_version: criteria.packageVersion,
      provider: "tencent-scf",
      node_type: "scf-event",
    })) {
      if (node[field] !== want) throw new Error(`fleet node ${node.node_id} ${field}=${node[field]} want=${want}`);
    }
    if (node.metadata?.runtime_code_package_id !== criteria.packageID) {
      throw new Error(
        `fleet node ${node.node_id} runtime_code_package_id=${node.metadata?.runtime_code_package_id || ""} ` +
        `want=${criteria.packageID}`,
      );
    }
    if (!(node.status === 2 || node.status === "NODE_STATUS_ONLINE")) {
      throw new Error(`fleet node ${node.node_id} is not online`);
    }
    const heartbeat = Date.parse(node.last_heartbeat || "");
    if (!Number.isFinite(heartbeat) || now - heartbeat > heartbeatMaxAgeMS) {
      throw new Error(`fleet node ${node.node_id} has a stale heartbeat`);
    }
    const workloads = new Set(node.supported_workloads || []);
    for (const workload of ["collect.binance.symbol", "collect.binance.kline"]) {
      if (!workloads.has(workload)) throw new Error(`fleet node ${node.node_id} is missing workload ${workload}`);
    }
  }
  return [...matching].sort((left, right) =>
    fleetNodeIndex(left, criteria.fleetPrefix) - fleetNodeIndex(right, criteria.fleetPrefix));
}

function fleetNodeIndex(node, fleetPrefix) {
  if (node?.metadata?.function_name_prefix) {
    if (node.metadata.function_name_prefix !== fleetPrefix) return null;
    const index = Number(node.metadata.index);
    return Number.isInteger(index) ? index : null;
  }
  const stablePrefix = `${fleetPrefix}-${node?.region || ""}-`;
  if (!String(node?.function_name || "").startsWith(stablePrefix)) return null;
  const rawIndex = String(node.function_name).slice(stablePrefix.length);
  if (!/^\d+$/.test(rawIndex)) return null;
  const index = Number(rawIndex);
  return Number.isInteger(index) ? index : null;
}

function assertUniqueBatchIDs(ids) {
  if (ids.some((id) => !id) || new Set(ids).size !== ids.length) {
    throw new Error("publish summary contains empty or duplicate batch IDs");
  }
}

export async function writeState(path, value) {
  assertNoStateSecrets(value);
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
  await chmod(path, 0o600);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const handlers = { setup, fleet, symbols, klines, assert: assertPhase, cleanup };
  await handlers[args.phase](args);
}

async function fleet(args) {
  const token = await login(args);
  const state = await readState(args.stateFile);
  let summary;
  try {
    summary = JSON.parse(await readFile(args.publishSummary, "utf8"));
  } catch (error) {
    throw new FatalAssertionError(`cannot read publish summary: ${error.message}`);
  }
  validatePublishSummary(summary, args.scfCount);
  const criteria = {
    cloudAccount: args.cloudAccount,
    region: args.region,
    fleetPrefix: args.fleetPrefix,
    packageID: summary.package_id,
    packageVersion: args.packageVersion,
    scfCount: args.scfCount,
  };
  const nodes = await waitFor("50-node Tencent SCF fleet", args.timeoutSeconds * 1000, async () =>
    verifiedSCFFleet(await listFleetNodes(args, token), criteria));
  await writeState(args.stateFile, {
    ...state,
    package_id: summary.package_id,
    package_version: args.packageVersion,
    fleet_mode: summary.fleet_mode,
    scf_node_ids: nodes.map((node) => node.node_id),
  });
  log(`fleet ready count=${nodes.length} package_id=${summary.package_id} mode=${summary.fleet_mode}`);
}

async function setup(args) {
  const token = await login(args);
  const dataSource = await ensureDataSource(args, token);
  if (dataSource.status !== "active") throw new Error("binance DataSource is not active");
  const preferredDataNodes = await existingDatasetNodeIDs(args, token);
  const dataNode = await selectDataNode(args, token, preferredDataNodes);
  await ensureFields(args, token, [...SYMBOL_COLUMNS, ...KLINE_COLUMNS]);
  const symbolContract = buildSymbolDatasetContract(args.space, args.symbolDataset, dataNode.node_id);
  const klineContract = buildKlineDatasetContract(args.space, args.klineDataset, dataNode.node_id);
  await ensureDataset(args, token, symbolContract);
  await ensureDataset(args, token, klineContract);
  await ensureKlineView(args, token);
  const initialState = {
    ...emptyState(),
    run_id: `${Date.now()}-${randomBytes(4).toString("hex")}`,
    rule_owner: `collector-symbol-kline-e2e:${Date.now()}-${randomBytes(4).toString("hex")}`,
    space_id: args.space,
    symbol_dataset_id: args.symbolDataset,
    kline_dataset_id: args.klineDataset,
    symbol_rule_id: args.symbolRule,
    kline_rule_id: args.klineRule,
    started_at: new Date().toISOString(),
  };
  await writeState(args.stateFile, initialState);
  args.ruleOwner = initialState.rule_owner;
  await disableOwnedRule(args, token, buildSymbolRule(args), true);
  await disableOwnedRule(args, token, buildKlineRule(args), true);
  await ensureRule(args, token, { ...buildSymbolRule(args), enabled: false });
  await ensureRule(args, token, { ...buildKlineRule(args), enabled: false });
  log(`setup ready data_node=${dataNode.node_id}`);
}

async function ensureKlineView(args, token) {
  const expected = buildKlineViewContract(args.space, args.klineDataset);
  let rsp = await storagePostRaw(args, token, "metadata", "GetView", {
    space_id: args.space,
    view_id: expected.view_id,
  });
  if (!retOK(rsp.ret_info)) {
    rsp = await storagePost(args, token, "metadata", "CreateView", { view: expected });
  }
  const assertContract = (view) => {
    for (const field of ["space_id", "view_id", "primary_dataset_id", "engine", "keep_duration", "status"]) {
      if (view?.[field] !== expected[field]) {
        throw new Error(`Kline View ${field}=${view?.[field] || ""} want=${expected[field]}`);
      }
    }
    if (!sameStringSet(view.dataset_ids || [], expected.dataset_ids) ||
        !sameStringSet(view.grain_keys || [], expected.grain_keys) ||
        view.filter_json !== expected.filter_json) {
      throw new Error("Kline View contract does not match the E2E dataset");
    }
  };
  assertContract(rsp.view);
  await waitFor("active Kline DataView", 120_000, async () => {
    const current = await storagePost(args, token, "metadata", "GetView", {
      space_id: args.space,
      view_id: expected.view_id,
    });
    assertContract(current.view);
    if (!current.view?.active_index_id) throw new Error("Kline View has no active index");
    return current.view;
  });
}

async function symbols(args) {
  const token = await login(args);
  const state = await readState(args.stateFile);
  args.ruleOwner = state.rule_owner;
  assertFleetState(state, args.scfCount);
  await ensureRule(args, token, buildSymbolRule(args));
  const seconds = new Date().getUTCSeconds();
  if (seconds >= 55) await sleep((61 - seconds) * 1000);
  const scheduledAt = new Date();
  await adminPost(args, token, "collectmgr", "ScheduleTasks", { space_id: args.space });

  const instance = await waitFor("one current Symbol TaskInstance", args.timeoutSeconds * 1000, async () => {
    const rows = await listTaskInstances(args, token, args.symbolRule);
    if (rows.length !== 1) throw new Error(`Symbol TaskInstance count=${rows.length} want=1`);
    if (!rows[0].cloud_job_item_id) throw new Error("Symbol TaskInstance is not bound to a JobItem");
    return rows[0];
  });
  const expectedExecuteAt = nextMinute(scheduledAt).toISOString();
  const jobItem = await waitFor("Symbol JobItem success", args.timeoutSeconds * 1000, async () => {
    const item = await getJobItem(args, token, instance.cloud_job_item_id);
    assertJobNotFailed(item);
    if (!isSuccess(item.status)) throw new Error(`Symbol JobItem status=${item.status}`);
    if (item.job_type !== "collect.binance.symbol") throw new FatalAssertionError(`invalid Symbol job_type=${item.job_type}`);
    assertNoRuntimeParams(item.params || {});
    if (new Date(item.execute_at).toISOString() !== expectedExecuteAt) {
      throw new FatalAssertionError(`Symbol execute_at=${item.execute_at} want=${expectedExecuteAt}`);
    }
    if (!String(item.job_item_id || "").includes(item.execute_at)) {
      throw new FatalAssertionError(`Symbol job_item_id does not contain execute_at window`);
    }
    if (Number(item.duration_ms || 0) >= 20_000) {
      throw new FatalAssertionError(`Symbol JobItem duration_ms=${item.duration_ms} exceeds worker timeout`);
    }
    if (!state.scf_node_ids.includes(item.execution_node)) {
      throw new FatalAssertionError(`Symbol JobItem execution_node=${item.execution_node} is outside the verified fleet`);
    }
    return item;
  });
  const windowSymbolJobs = (await listAllJobItems(args, token, args.symbolRule))
    .filter((item) => item.execute_at === jobItem.execute_at);
  if (windowSymbolJobs.length !== 1 || windowSymbolJobs[0].job_item_id !== jobItem.job_item_id) {
    throw new FatalAssertionError(`Symbol window JobItem count=${windowSymbolJobs.length} want=1`);
  }

  const memberships = await listAll(args, token, "metadata", "ListDatasetSubjects", "dataset_subjects", {
    space_id: args.space,
    dataset_id: args.symbolDataset,
  });
  const recordIDs = memberships
    .filter((item) => item.status === "active")
    .map((item) => item.subject_id)
    .filter(Boolean);
  if (recordIDs.length === 0) {
    throw new FatalAssertionError("Symbol collection produced no active dataset memberships");
  }
  const symbolResult = symbolTaskResult(jobItem);
  const snapshotVersion = String(symbolResult.record_snapshot_version || "");
  if (Number.isNaN(new Date(snapshotVersion).getTime())) {
    throw new FatalAssertionError("Symbol Job result is missing a valid record_snapshot_version");
  }
  const records = await readExactRecordRows(
    args,
    token,
    args.symbolDataset,
    recordIDs,
    snapshotVersion,
  );
  const mappings = await listAll(args, token, "metadata", "ListSubjectSymbols", "subject_symbols", {
    space_id: args.space,
    data_source_id: "binance",
  });
  const subjectIDs = activeUSDTSymbolIDs(records, memberships, mappings);
  if (subjectIDs.size === 0) throw new FatalAssertionError("Symbol collection produced no active USDT symbols");
  const symbolWriteCount = Number(symbolResult.rows_written || 0);
  if (symbolWriteCount <= 0) {
    throw new FatalAssertionError("Symbol Job result does not contain a positive rows_written count");
  }
  await disableRule(args, token, args.symbolRule);
  await ensureRule(args, token, buildKlineRule(args));

  const symbolMap = new Map(mappings
    .filter((item) => subjectIDs.has(item.subject_id) && item.status === "active" && item.data_source_id === "binance")
    .map((item) => [item.subject_id, item.external_symbol]));
  await writeState(args.stateFile, {
    ...state,
    symbol_job_item_id: jobItem.job_item_id,
    symbol_count: subjectIDs.size,
    symbol_subjects: Object.fromEntries([...symbolMap].sort()),
    symbol_rule_disabled: true,
  });
  log(`symbols verified count=${subjectIDs.size} job_item_id=${jobItem.job_item_id}`);
}

async function klines(args) {
  const token = await login(args);
  const state = await readState(args.stateFile);
  args.ruleOwner = state.rule_owner;
  assertFleetState(state, args.scfCount);
  const subjectSymbols = new Map(Object.entries(state.symbol_subjects || {}));
  if (subjectSymbols.size === 0 || subjectSymbols.size !== state.symbol_count) {
    throw new FatalAssertionError("state does not contain the verified Symbol subject set");
  }
  const seconds = new Date().getUTCSeconds();
  if (seconds >= 55) await sleep((61 - seconds) * 1000);
  const scheduledAt = new Date();
  await adminPost(args, token, "collectmgr", "ScheduleTasks", { space_id: args.space });
  const instances = await waitFor("Kline TaskInstances", 60_000, async () => {
    const rows = (await listTaskInstances(args, token, args.klineRule))
      .filter((item) => subjectSymbols.has(item.subject_id));
    if (rows.length !== subjectSymbols.size) {
      throw new Error(`Kline TaskInstance count=${rows.length} want=${subjectSymbols.size}`);
    }
    if (rows.some((item) => !item.cloud_job_item_id)) throw new Error("Kline TaskInstance is missing cloud_job_item_id");
    return rows;
  });
  const wanted = new Set(instances.map((item) => item.cloud_job_item_id));
  const items = await waitFor("Kline JobItems", 60_000, async () => {
    const rows = await listAllJobItems(args, token, args.klineRule);
    const current = rows.filter((item) => wanted.has(item.job_item_id));
    if (current.length !== wanted.size) {
      throw new Error(`Kline JobItem count=${current.length} want=${wanted.size}`);
    }
    return current;
  });
  const { executeAt } = validateKlineJobs(items, subjectSymbols, args.klineDataset);
  if (new Date(executeAt).toISOString() !== nextMinute(scheduledAt).toISOString()) {
    throw new FatalAssertionError(`Kline execute_at=${executeAt} is not the next whole-minute boundary`);
  }
  await writeState(args.stateFile, {
    ...state,
    kline_job_item_ids: [...wanted].sort(),
    kline_execute_at: executeAt,
  });
  log(`klines planned count=${items.length} execute_at=${executeAt}`);
}

async function assertPhase(args) {
  const token = await login(args);
  const state = await readState(args.stateFile);
  assertFleetState(state, args.scfCount);
  const symbolContract = await currentDatasetContract(args, token, args.symbolDataset, buildSymbolDatasetContract);
  const klineContract = await currentDatasetContract(args, token, args.klineDataset, buildKlineDatasetContract);
  if (symbolContract.dataset.status !== "active" || klineContract.dataset.status !== "active") {
    throw new FatalAssertionError("fixture datasets must remain active");
  }
  const subjectSymbols = new Map(Object.entries(state.symbol_subjects || {}));
  let wanted = new Set(state.kline_job_item_ids || []);
  if (wanted.size === 0) throw new FatalAssertionError("state does not contain Kline JobItems");

  const windows = [];
  let jobs = await waitForKlineWindowSuccess(args, token, wanted, subjectSymbols);
  windows.push(jobs);
  let evidence = collectKlineWriteEvidence(jobs, args.space, args.klineDataset);
  if (evidence.rowsWritten === 0 || evidence.executionNodes.size < 2) {
    const next = await scheduleNextKlineWindow(args, token, wanted, subjectSymbols);
    wanted = next.wanted;
    jobs = await waitForKlineWindowSuccess(args, token, wanted, subjectSymbols);
    windows.push(jobs);
    evidence = mergeKlineEvidence(windows, args.space, args.klineDataset);
  }
  if (evidence.rowsWritten <= 0) {
    throw new FatalAssertionError("two Kline windows completed without positive current-write evidence");
  }
  if (evidence.executionNodes.size < 2) {
    throw new FatalAssertionError("Kline execution did not cover at least two SCF nodes across two windows");
  }
  for (const nodeID of evidence.executionNodes) {
    if (!state.scf_node_ids.includes(nodeID)) {
      throw new FatalAssertionError(`Kline JobItem execution_node=${nodeID} is outside the verified fleet`);
    }
  }
  const verifiedSamples = selectKlineVerificationSamples(evidence.samples);
  await verifyWrittenKlineRows(args, token, verifiedSamples);
  const distribution = jobDistribution(windows.flat());
  log(`Kline node distribution: ${JSON.stringify(distribution)}`);
  logLifecycleQuery(state.symbol_job_item_id, "symbol");
  for (const item of windows.flat().slice(0, 10)) logLifecycleQuery(item.job_item_id, "kline");
  await writeState(args.stateFile, {
    ...state,
    kline_job_item_ids: [...wanted].sort(),
    positive_write_count: evidence.rowsWritten,
    distinct_execution_node_count: evidence.executionNodes.size,
    execution_distribution: distribution,
    verified_row_keys: verifiedSamples,
    finished_at: new Date().toISOString(),
  });
  log(`assert ok symbol_count=${state.symbol_count} kline_jobs=${wanted.size} rows_written=${evidence.rowsWritten}`);
}

async function waitForKlineWindowSuccess(args, token, wanted, subjectSymbols) {
  return waitFor("all Kline JobItems success", args.timeoutSeconds * 1000, async () => {
    const current = (await listAllJobItems(args, token, args.klineRule))
      .filter((item) => wanted.has(item.job_item_id));
    if (current.length !== wanted.size) {
      throw new Error(`Kline terminal count=${current.length} want=${wanted.size}`);
    }
    for (const item of current) assertJobNotFailed(item);
    const pending = current.filter((item) => !isSuccess(item.status));
    if (pending.length > 0) throw new Error(`${pending.length}/${current.length} Kline JobItems are still pending`);
    validateKlineJobs(current, subjectSymbols, args.klineDataset);
    return current;
  });
}

async function scheduleNextKlineWindow(args, token, previous, subjectSymbols) {
  await adminPost(args, token, "collectmgr", "ScheduleTasks", { space_id: args.space });
  const instances = await waitFor("next Kline TaskInstance window", 60_000, async () => {
    const rows = (await listTaskInstances(args, token, args.klineRule))
      .filter((item) => subjectSymbols.has(item.subject_id));
    if (rows.length !== subjectSymbols.size || rows.some((item) => !item.cloud_job_item_id)) {
      throw new Error(`next Kline TaskInstance count=${rows.length} want=${subjectSymbols.size}`);
    }
    const ids = new Set(rows.map((item) => item.cloud_job_item_id));
    if ([...ids].some((id) => previous.has(id))) throw new Error("Kline TaskInstances have not advanced to the next window");
    return rows;
  });
  const wanted = new Set(instances.map((item) => item.cloud_job_item_id));
  await waitFor("next Kline JobItem window", 60_000, async () => {
    const current = (await listAllJobItems(args, token, args.klineRule))
      .filter((item) => wanted.has(item.job_item_id));
    if (current.length !== wanted.size) throw new Error(`next Kline JobItem count=${current.length} want=${wanted.size}`);
    validateKlineJobs(current, subjectSymbols, args.klineDataset);
    return current;
  });
  return { wanted };
}

function mergeKlineEvidence(windows, spaceID, datasetID) {
  const merged = { rowsWritten: 0, samples: [], executionNodes: new Set() };
  for (const jobs of windows) {
    const current = collectKlineWriteEvidence(jobs, spaceID, datasetID);
    merged.rowsWritten += current.rowsWritten;
    merged.samples.push(...current.samples);
    for (const nodeID of current.executionNodes) merged.executionNodes.add(nodeID);
  }
  return merged;
}

function selectKlineVerificationSamples(samples) {
  const bySubject = new Map();
  for (const sample of samples) {
    if (!bySubject.has(sample.key.subject_id)) bySubject.set(sample.key.subject_id, sample);
  }
  const btc = bySubject.get("BTC-USDT");
  if (!btc) throw new FatalAssertionError("positive Kline evidence does not include BTC-USDT");
  const candidates = [...bySubject.entries()]
    .filter(([subjectID]) => subjectID !== "BTC-USDT")
    .sort(([left], [right]) => left.localeCompare(right));
  const selected = [btc];
  const secondNode = candidates.find(([, sample]) => sample.executionNode !== btc.executionNode);
  if (!secondNode) {
    throw new FatalAssertionError("positive Kline RowKey samples do not cover two SCF nodes");
  }
  selected.push(secondNode[1]);
  for (const [, sample] of candidates) {
    if (selected.length >= 10) break;
    if (!selected.includes(sample)) selected.push(sample);
  }
  if (selected.length < 10) {
    throw new FatalAssertionError(`positive Kline evidence covers only ${selected.length} subjects; need at least 10`);
  }
  return selected.map((sample) => sample.key);
}

async function verifyWrittenKlineRows(args, token, keys) {
  const rsp = await storagePost(args, token, "access", "ReadTimeSeriesRows", {
    space_id: args.space,
    dataset_id: args.klineDataset,
    keys,
    column_names: KLINE_COLUMNS.map(([name]) => name),
    page: { page: 1, size: keys.length },
  });
  const byKey = new Map((rsp.rows || []).map((row) => [JSON.stringify(stableObject(row.key || {})), row]));
  for (const key of keys) {
    const row = byKey.get(JSON.stringify(stableObject(key)));
    if (!row) throw new Error(`written Kline RowKey is not readable: ${JSON.stringify(key)}`);
    validateKlineStorageRow(row, key);
  }
}

function jobDistribution(jobs) {
  const counts = new Map();
  for (const item of jobs) counts.set(item.execution_node, (counts.get(item.execution_node) || 0) + 1);
  return Object.fromEntries([...counts].sort(([left], [right]) => left.localeCompare(right)));
}

function logLifecycleQuery(jobItemID, kind) {
  log(`CLS query: job_item_id="${jobItemID}" kind=${kind} expected_events=received,deferred_or_started,collector_http_completed,done,cloudnode_reported,delivery_action`);
}

async function cleanup(args) {
  const token = await login(args);
  const state = await readState(args.stateFile);
  if (!state.rule_owner) {
    log("cleanup skipped Rule changes because this state has no rule owner");
    return;
  }
  restoreCleanupScope(args, state);
  await disableOwnedRule(args, token, buildSymbolRule(args), true);
  await disableOwnedRule(args, token, buildKlineRule(args), true);
}

export function restoreCleanupScope(args, state) {
  const scope = {
    space: String(state?.space_id || "").trim(),
    symbolDataset: validateDatasetID(state?.symbol_dataset_id),
    klineDataset: validateDatasetID(state?.kline_dataset_id),
    symbolRule: String(state?.symbol_rule_id || "").trim(),
    klineRule: String(state?.kline_rule_id || "").trim(),
    ruleOwner: String(state?.rule_owner || "").trim(),
  };
  validateE2ERuleID(scope.symbolRule);
  validateE2ERuleID(scope.klineRule);
  if (!scope.space) throw new FatalAssertionError("cleanup state space_id is required");
  if (!scope.ruleOwner) throw new FatalAssertionError("cleanup state rule_owner is required");
  if (scope.symbolRule === scope.klineRule) {
    throw new FatalAssertionError("cleanup state Symbol and Kline Rule IDs must differ");
  }
  Object.assign(args, scope);
  return args;
}

async function ensureDataSource(args, token) {
  const existing = await storagePostRaw(args, token, "metadata", "GetDataSource", {
    space_id: args.space,
    data_source_id: "binance",
  });
  if (retOK(existing.ret_info)) {
    const source = existing.data_source;
    for (const [key, want] of Object.entries({ kind: "exchange", market: "crypto", timezone: "UTC", status: "active" })) {
      if (source?.[key] !== want) throw new Error(`binance DataSource ${key}=${source?.[key]} want=${want}`);
    }
    return source;
  }
  const rsp = await storagePost(args, token, "metadata", "CreateDataSource", {
    data_source: {
      space_id: args.space,
      data_source_id: "binance",
      name: "币安",
      kind: "exchange",
      market: "crypto",
      timezone: "UTC",
      status: "active",
    },
  });
  return rsp.data_source;
}

async function existingDatasetNodeIDs(args, token) {
  const ids = new Set();
  for (const datasetID of [args.symbolDataset, args.klineDataset]) {
    const rsp = await storagePostRaw(args, token, "metadata", "GetDataset", {
      space_id: args.space,
      dataset_id: datasetID,
    });
    if (retOK(rsp.ret_info) && rsp.dataset?.data_node_id) ids.add(rsp.dataset.data_node_id);
  }
  if (ids.size > 1) {
    throw new Error(`existing fixture Datasets use different DataNodes: ${[...ids].join(",")}`);
  }
  return ids;
}

async function selectDataNode(args, token, preferredIDs = new Set()) {
  const rsp = await storagePost(args, token, "metadata", "ListDataNodes", {
    page: { page: 1, size: 200 },
  });
  const nodes = (rsp.items || []).map((item) => item.node).filter(Boolean);
  const eligible = nodes.filter((item) => item.status === "active" && item.service_target);
  const preferredID = [...preferredIDs][0];
  const node = preferredID
    ? eligible.find((item) => item.node_id === preferredID)
    : eligible.sort((left, right) => left.node_id.localeCompare(right.node_id))[0];
  if (!node) throw new Error("no deployed active DataNode is available");
  return node;
}

async function ensureFields(args, token, definitions) {
  await ensureFieldGroup(args, token);
  const unique = new Map(definitions.map((item) => [item[0], item]));
  for (const [fieldID, type, name] of unique.values()) {
    const existing = await storagePostRaw(args, token, "metadata", "GetField", {
      space_id: args.space,
      field_id: fieldID,
    });
    if (retOK(existing.ret_info)) {
      if (!fieldContractMatches(existing.field, type)) {
        throw new Error(
          `field ${fieldID} contract mismatch: value_type=${existing.field?.value_type} status=${existing.field?.status}`,
        );
      }
      continue;
    }
    await storagePost(args, token, "metadata", "CreateField", {
      field: {
        space_id: args.space,
        group_id: "e2e_collector",
        field_id: fieldID,
        name,
        value_type: VALUE_TYPES[type],
        status: "active",
      },
    });
  }
}

export function fieldContractMatches(field, type) {
  return [type, VALUE_TYPES[type], VALUE_TYPE_CODES[type]].includes(field?.value_type) &&
    field?.status === "active";
}

async function ensureFieldGroup(args, token) {
  const existing = await storagePostRaw(args, token, "metadata", "GetFieldGroup", {
    space_id: args.space,
    group_id: "e2e_collector",
  });
  if (retOK(existing.ret_info)) {
    if (existing.field_group?.status !== "active") throw new Error("e2e_collector FieldGroup is not active");
    return;
  }
  await storagePost(args, token, "metadata", "CreateFieldGroup", {
    field_group: {
      space_id: args.space,
      group_id: "e2e_collector",
      name: "采集测试",
      description: "Collector E2E fields",
      sort_order: 900,
      status: "active",
    },
  });
}

async function ensureDataset(args, token, contract) {
  let current = await storagePostRaw(args, token, "metadata", "GetDataset", {
    space_id: args.space,
    dataset_id: contract.dataset_id,
  });
  if (!retOK(current.ret_info)) {
    const dataset = { ...contract };
    delete dataset.columns;
    const created = await storagePost(args, token, "metadata", "CreateDataset", { dataset });
    current = { ret_info: created.ret_info, dataset: created.dataset };
  }
  let columns = await listAll(args, token, "metadata", "ListDatasetColumns", "columns", {
    space_id: args.space,
    dataset_id: contract.dataset_id,
  });
  if (current.dataset.status === "active") {
    assertDatasetContract(current.dataset, columns, contract);
    return current.dataset;
  }
  const structural = { ...contract, columns: [] };
  assertDatasetContract(current.dataset, [], structural);
  const existingByName = new Map(columns.map((item) => [item.column_name, item]));
  for (const column of contract.columns) {
    const existing = existingByName.get(column.column_name);
    if (existing && (existing.value_type !== column.value_type || existing.origin_id !== column.origin_id)) {
      throw new Error(`disabled dataset ${contract.dataset_id} column ${column.column_name} contract mismatch`);
    }
    if (!existing) {
      await storagePost(args, token, "metadata", "UpsertDatasetColumn", { column });
    }
  }
  columns = await listAll(args, token, "metadata", "ListDatasetColumns", "columns", {
    space_id: args.space,
    dataset_id: contract.dataset_id,
  });
  assertDatasetContract(current.dataset, columns, contract);
  const check = await storagePost(args, token, "metadata", "CheckDatasetActivation", {
    space_id: args.space,
    dataset_id: contract.dataset_id,
  });
  if (check.ready !== true) {
    throw new Error(`dataset ${contract.dataset_id} activation is not ready: ${JSON.stringify(check.checks || [])}`);
  }
  const activated = await storagePost(args, token, "metadata", "ActivateDataset", {
    space_id: args.space,
    dataset_id: contract.dataset_id,
    expected_revision: check.dataset_revision,
  });
  if (activated.dataset?.status !== "active" || activated.dataset?.binding_locked !== true) {
    throw new Error(`dataset ${contract.dataset_id} did not activate with a locked binding`);
  }
  const verified = await storagePost(args, token, "metadata", "GetDataset", {
    space_id: args.space,
    dataset_id: contract.dataset_id,
  });
  assertDatasetContract(verified.dataset, columns, contract);
  return verified.dataset;
}

async function currentDatasetContract(args, token, datasetID, builder) {
  const rsp = await storagePost(args, token, "metadata", "GetDataset", {
    space_id: args.space,
    dataset_id: datasetID,
  });
  const columns = await listAll(args, token, "metadata", "ListDatasetColumns", "columns", {
    space_id: args.space,
    dataset_id: datasetID,
  });
  const expected = builder(args.space, datasetID, rsp.dataset.data_node_id);
  assertDatasetContract(rsp.dataset, columns, expected);
  return { dataset: rsp.dataset, columns };
}

function buildSymbolRule(args) {
  return {
    space_id: args.space,
    rule_id: args.symbolRule,
    data_type: "symbol",
    exchange: "binance",
    collect_params: {
      source: { kind: "none" },
      collector: { exchange: "binance", market: "spot", data_type: "symbol" },
      target: { dataset_id: args.symbolDataset },
      schedule: { interval: "1m" },
    },
    enabled: true,
    creator: args.ruleOwner,
  };
}

function buildKlineRule(args) {
  return {
    space_id: args.space,
    rule_id: args.klineRule,
    data_type: "kline",
    exchange: "binance",
    collect_params: {
      source: { kind: "dataset_subjects", dataset_id: args.symbolDataset },
      collector: { exchange: "binance", market: "spot", data_type: "kline", intervals: ["1m"] },
      target: { dataset_id: args.klineDataset },
      schedule: { interval: "1m" },
    },
    enabled: true,
    creator: args.ruleOwner,
  };
}

async function ensureRule(args, token, rule) {
  const existing = await adminPost(args, token, "collectmgr", "GetTaskRuleDetail", {
    space_id: args.space,
    rule_id: rule.rule_id,
  }, { raw: true });
  if (retOK(existing.ret_info)) {
    assertRuleContract(existing.rule, rule);
    if (existing.rule.enabled === true) {
      throw new Error(`E2E Rule ${rule.rule_id} is already enabled by another or stale run`);
    }
    await adminPost(args, token, "collectmgr", "UpdateTaskRule", {
      space_id: args.space,
      rule_id: rule.rule_id,
      rule,
    });
    return;
  }
  await adminPost(args, token, "collectmgr", "CreateTaskRule", { rule });
}

function assertRuleContract(actual, expected) {
  for (const key of ["space_id", "rule_id", "data_type", "exchange", "creator"]) {
    if (actual?.[key] !== expected[key]) throw new Error(`Rule ${expected.rule_id} ${key} contract mismatch`);
  }
  if (JSON.stringify(stableJSON(actual.collect_params || {})) !== JSON.stringify(stableJSON(expected.collect_params))) {
    throw new Error(`Rule ${expected.rule_id} collect_params contract mismatch`);
  }
}

async function disableRule(args, token, ruleID, ignoreMissing = false) {
  const rsp = await adminPost(args, token, "collectmgr", "DisableTaskRule", {
    space_id: args.space,
    rule_id: ruleID,
  }, { raw: true });
  if (retOK(rsp.ret_info)) return;
  if (ignoreMissing && /not found|不存在/i.test(retMessage(rsp.ret_info))) return;
  throw new Error(`DisableTaskRule ${ruleID} failed: ${retMessage(rsp.ret_info)}`);
}

async function disableOwnedRule(args, token, expected, ignoreMissing = false) {
  const existing = await adminPost(args, token, "collectmgr", "GetTaskRuleDetail", {
    space_id: args.space,
    rule_id: expected.rule_id,
  }, { raw: true });
  if (!retOK(existing.ret_info)) {
    if (ignoreMissing && /not found|不存在/i.test(retMessage(existing.ret_info))) return;
    throw new Error(`GetTaskRuleDetail ${expected.rule_id} failed: ${retMessage(existing.ret_info)}`);
  }
  assertRuleContract(existing.rule, expected);
  if (existing.rule.enabled === true) {
    await disableRule(args, token, expected.rule_id);
  }
}

async function listTaskInstances(args, token, ruleID) {
  const all = [];
  for (let page = 1; ; page += 1) {
    const rsp = await adminPost(args, token, "collectmgr", "GetTaskInstanceList", {
      filter: {
        space_id: args.space,
        rule_id: ruleID,
        page: { page, size: 500 },
      },
    });
    const items = rsp.instances || [];
    all.push(...items);
    if (!hasMorePages(rsp.page, all.length, items.length, 500)) return all;
  }
}

async function getJobItem(args, token, jobItemID) {
  const rsp = await adminPost(args, token, "cloudnode", "GetJobItem", {
    space_id: args.space,
    job_item_id: jobItemID,
  }, { spaceId: args.space });
  if (!rsp.item) throw new Error(`JobItem ${jobItemID} is missing`);
  return rsp.item;
}

async function listAllJobItems(args, token, jobID) {
  const all = [];
  for (let page = 1; ; page += 1) {
    const rsp = await adminPost(args, token, "cloudnode", "ListJobItems", {
      space_id: args.space,
      job_id: jobID,
      page: { page, size: 500 },
    }, { spaceId: args.space });
    const items = rsp.items || [];
    all.push(...items);
    if (!hasMorePages(rsp.page, all.length, items.length, 500)) return all;
  }
}

async function listFleetNodes(args, token) {
  const all = [];
  for (let page = 1; ; page += 1) {
    const rsp = await adminPost(args, token, "cloudnode", "GetNodeList", {
      cloud_account_id: args.cloudAccount,
      region: args.region,
      node_type: "scf-event",
      page: { page, size: 500 },
    }, { spaceId: args.space });
    const items = rsp.items || [];
    all.push(...items);
    if (!hasMorePages(rsp.page, all.length, items.length, 500)) return all;
  }
}

async function listAll(args, token, group, method, property, body) {
  const all = [];
  for (let page = 1; ; page += 1) {
    const rsp = await storagePost(args, token, group, method, {
      ...body,
      page: { page, size: 500 },
    });
    const items = rsp[property] || [];
    all.push(...items);
    if (!hasMorePages(rsp.page_result, all.length, items.length, 500)) return all;
  }
}

async function readExactRecordRows(args, token, datasetID, recordIDs, version) {
  const rows = [];
  for (let offset = 0; offset < recordIDs.length; offset += 100) {
    const rsp = await storagePost(args, token, "primary", "ReadRecordRows", {
      keys: recordIDs.slice(offset, offset + 100).map((recordID) => ({
        space_id: args.space,
        dataset_id: datasetID,
        record_id: recordID,
        version,
      })),
      column_names: SYMBOL_COLUMNS.map(([name]) => name),
    });
    rows.push(...(rsp.rows || []));
  }
  if (rows.length !== recordIDs.length) {
    throw new Error(`exact Symbol Record rows=${rows.length} want=${recordIDs.length}`);
  }
  return rows;
}

function recordFields(row) {
  return Object.fromEntries((row?.fields || []).map((field) => [
    field.field_id,
    typedValue(field.value),
  ]));
}

function typedValue(value) {
  if (!value || typeof value !== "object") return undefined;
  for (const [key, item] of Object.entries(value)) {
    if (key.endsWith("_value")) return item;
  }
  return undefined;
}

function symbolTaskResult(jobItem) {
  const tasks = jobItem?.result_summary?.tasks || [];
  const symbolTasks = tasks.filter((task) => task.data_type === "symbol");
  if (symbolTasks.length !== 1) {
    throw new Error(`Symbol Job must contain one task result; got ${symbolTasks.length}`);
  }
  return symbolTasks[0];
}

function assertJobNotFailed(item) {
  if (TERMINAL_FAILURES.has(item.status)) {
    throw new FatalAssertionError(
      `JobItem ${item.job_item_id} failed: ${item.last_error_code || ""} ${item.last_error_message || ""}`,
    );
  }
}

function isSuccess(status) {
  return status === 3 || status === "JOB_ITEM_STATUS_SUCCESS";
}

function nextMinute(date) {
  return new Date(Math.floor(date.getTime() / 60_000) * 60_000 + 60_000);
}

function emptyState() {
  return {
    run_id: "",
    space_id: "",
    symbol_dataset_id: "",
    kline_dataset_id: "",
    symbol_rule_id: "",
    kline_rule_id: "",
    symbol_job_item_id: "",
    symbol_count: 0,
    kline_job_item_ids: [],
    scf_node_ids: [],
    package_id: "",
    started_at: "",
    finished_at: "",
  };
}

function assertFleetState(state, expectedCount) {
  const nodeIDs = state?.scf_node_ids || [];
  if (!state?.package_id || nodeIDs.length !== expectedCount || new Set(nodeIDs).size !== expectedCount) {
    throw new FatalAssertionError(`state does not contain a verified ${expectedCount}-node SCF fleet`);
  }
}

function assertNoStateSecrets(value, path = "state") {
  if (!value || typeof value !== "object") return;
  for (const [key, nested] of Object.entries(value)) {
    if (/(password|secret|hmac|credential|signing_key|private_key|keepalive_event)/i.test(key)) {
      throw new Error(`state must not contain secret field ${path}.${key}`);
    }
    assertNoStateSecrets(nested, `${path}.${key}`);
  }
}

async function readState(path) {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    throw new FatalAssertionError(`cannot read E2E state: ${error.message}`);
  }
}

async function login(args) {
  if (!args.password) {
    throw new Error("MOOX_E2E_ADMIN_PASSWORD is required");
  }
  const saltRsp = await adminPost(args, "", "auth", "GetLoginSalt", { username: args.username });
  const passwordHash = encryptPassword(args.password, saltRsp.salt, saltRsp.timestamp);
  const rsp = await adminPost(args, "", "auth", "Login", {
    username: args.username,
    password_hash: passwordHash,
    salt: saltRsp.salt,
    timestamp: saltRsp.timestamp,
    device_id: "collector-symbol-kline-e2e",
    user_agent: "moox-e2e",
    client_ip: "127.0.0.1",
  });
  if (!rsp.access_token || !rsp.request_signing_key) throw new Error("login response is incomplete");
  return { accessToken: rsp.access_token, signingKey: rsp.request_signing_key };
}

function encryptPassword(password, salt, timestamp) {
  const key = createHash("sha256").update(`${salt}${timestamp}`).digest();
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  const encrypted = Buffer.concat([cipher.update(password, "utf8"), cipher.final()]);
  return Buffer.concat([iv, encrypted, cipher.getAuthTag()]).toString("base64");
}

async function storagePost(args, token, group, method, body) {
  return adminPost(args, token, "storage", method, withAuth(body), {
    storageGroup: group,
  });
}

async function storagePostRaw(args, token, group, method, body) {
  return adminPost(args, token, "storage", method, withAuth(body), {
    raw: true,
    storageGroup: group,
  });
}

function withAuth(body) {
  return {
    auth_info: {
      ...APP_INFO,
      operator: "collector_symbol_kline_e2e",
      request_id: `collector-e2e-${Date.now()}-${randomBytes(4).toString("hex")}`,
    },
    ...body,
  };
}

async function adminPost(args, token, service, method, body, options = {}) {
  const headers = {
    "Content-Type": "application/json",
    "User-Agent": "moox-collector-symbol-kline-e2e",
    "X-Trace-Id": `collector-e2e-${service}-${method}-${Date.now()}`,
  };
  if (token?.accessToken) {
    headers.Authorization = token.accessToken;
    headers["X-Access-Token"] = token.accessToken;
  } else if (typeof token === "string" && token) {
    headers.Authorization = token;
    headers["X-Access-Token"] = token;
  }
  if (options.spaceId) headers["X-Space-Id"] = options.spaceId;
  const url = `${args.gateway}/api/admin/${service}/${method}`;
  const payload = JSON.stringify(body || {});
  if (token?.signingKey) {
    Object.assign(headers, signedHeaders(token.signingKey, new URL(url).pathname, payload, headers));
  }
  const response = await fetch(url, { method: "POST", headers, body: payload });
  const text = await response.text();
  let data;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    throw new Error(`${service}/${method} returned non-JSON: ${text.slice(0, 300)}`);
  }
  if (!response.ok) throw new Error(`${service}/${method} HTTP ${response.status}: ${text.slice(0, 300)}`);
  if (options.raw) return data;
  if (!retOK(data?.ret_info)) throw new Error(`${service}/${method} failed: ${retMessage(data?.ret_info)}`);
  return data;
}

export function signedHeaders(signingKey, path, payload, headers) {
  const timestamp = String(Math.floor(Date.now() / 1000));
  const nonce = randomBytes(32).toString("hex");
  const canonical = ["moox-request-v1", "POST", path];
  const signed = [
    ["x-app-id", headers["X-App-Id"] || ""],
    ["x-app-key", headers["X-App-Key"] || ""],
    ["x-space-id", headers["X-Space-Id"] || ""],
  ];
  if (signed.some(([, value]) => value)) {
    for (const [name, value] of signed) canonical.push(`${name}:${value}`);
  }
  canonical.push(createHash("sha256").update(payload).digest("hex"), timestamp, nonce);
  return {
    "X-Moox-Timestamp": timestamp,
    "X-Moox-Nonce": nonce,
    "X-Moox-Signature": createHmac("sha256", signingKey).update(canonical.join("\n")).digest("hex"),
  };
}

async function waitFor(label, timeoutMS, fn) {
  const deadline = Date.now() + timeoutMS;
  let lastError;
  while (Date.now() < deadline) {
    try {
      return await fn();
    } catch (error) {
      if (error instanceof FatalAssertionError) throw error;
      lastError = error;
      await sleep(2000);
    }
  }
  throw new Error(`${label} timed out: ${lastError?.message || "no result"}`);
}

function retOK(retInfo) {
  const code = retInfo?.code;
  return code === 0 || code === "0" || code === "SUCCESS" || code === "ErrorCode_SUCCESS";
}

function retMessage(retInfo) {
  return retInfo?.msg || `code=${retInfo?.code}`;
}

function assertSameSet(left, right, message) {
  if (left.size !== right.size || [...left].some((item) => !right.has(item))) {
    throw new Error(`${message}: left=${JSON.stringify([...left].sort())} right=${JSON.stringify([...right].sort())}`);
  }
}

function sameStringSet(left, right) {
  return left.length === right.length &&
    [...left].sort().every((item, index) => item === [...right].sort()[index]);
}

function stableJSON(value) {
  if (Array.isArray(value)) return value.map(stableJSON);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value).sort(([left], [right]) => left.localeCompare(right))
    .map(([key, nested]) => [key, stableJSON(nested)]));
}

function compareJSON(left, right) {
  return JSON.stringify(left).localeCompare(JSON.stringify(right));
}

function trimRight(value, suffix) {
  let result = String(value || "");
  while (result.endsWith(suffix)) result = result.slice(0, -suffix.length);
  return result;
}

function log(message) {
  console.log(`[collector-symbol-kline-e2e] ${message}`);
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  main().catch((error) => {
    console.error(`[collector-symbol-kline-e2e] failed: ${error.message}`);
    process.exitCode = 1;
  });
}
