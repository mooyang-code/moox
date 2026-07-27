import assert from "node:assert/strict";
import { mkdtemp, readFile, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  acceptKlineWriteEvidence,
  activeUSDTSymbolIDs,
  assertDatasetContract,
  assertNoRuntimeParams,
  buildKlineDatasetContract,
  buildSCFNodeDefinitions,
  buildSymbolDatasetContract,
  collectKlineWriteEvidence,
  hasMorePages,
  parseArgs,
  restoreCleanupScope,
  validateKlineStorageRow,
  validatePublishSummary,
  validateKlineJobs,
  verifiedSCFFleet,
  writeState,
} from "./collector-symbol-kline.mjs";

test("builds record symbol dataset contract", () => {
  const dataset = buildSymbolDatasetContract("crypto", "e2e_binance_symbols", "data-node");
  assert.equal(dataset.data_kind, "DATA_KIND_RECORD");
  assert.deepEqual(dataset.freqs, []);
  assert.equal(dataset.data_source_id, "binance");
  assert.deepEqual(dataset.columns.map(({ column_name, value_type }) => [column_name, value_type]), [
    ["symbol", "FIELD_VALUE_TYPE_STRING"],
    ["external_symbol", "FIELD_VALUE_TYPE_STRING"],
    ["base_asset", "FIELD_VALUE_TYPE_STRING"],
    ["quote_asset", "FIELD_VALUE_TYPE_STRING"],
    ["status", "FIELD_VALUE_TYPE_STRING"],
    ["min_qty", "FIELD_VALUE_TYPE_DOUBLE"],
    ["max_qty", "FIELD_VALUE_TYPE_DOUBLE"],
    ["tick_size", "FIELD_VALUE_TYPE_DOUBLE"],
    ["lot_size", "FIELD_VALUE_TYPE_DOUBLE"],
  ]);
});

test("builds 1m time-series dataset contract", () => {
  const dataset = buildKlineDatasetContract("crypto", "e2e_binance_kline_1m", "data-node");
  assert.equal(dataset.data_kind, "DATA_KIND_TIME_SERIES");
  assert.deepEqual(dataset.freqs, ["1m"]);
  assert.deepEqual(dataset.columns.map(({ column_name, value_type }) => [column_name, value_type]), [
    ["open", "FIELD_VALUE_TYPE_DOUBLE"],
    ["high", "FIELD_VALUE_TYPE_DOUBLE"],
    ["low", "FIELD_VALUE_TYPE_DOUBLE"],
    ["close", "FIELD_VALUE_TYPE_DOUBLE"],
    ["volume", "FIELD_VALUE_TYPE_DOUBLE"],
    ["quote_volume", "FIELD_VALUE_TYPE_DOUBLE"],
    ["trade_num", "FIELD_VALUE_TYPE_INT"],
  ]);
});

test("rejects dataset ids longer than 30 characters", () => {
  assert.throws(
    () => buildSymbolDatasetContract("crypto", "x".repeat(31), "data-node"),
    /at most 30 characters/,
  );
});

test("existing active dataset must exactly match the fixture contract", () => {
  const expected = buildKlineDatasetContract("crypto", "e2e_binance_kline_1m", "node-a");
  const columns = expected.columns;
  assert.doesNotThrow(() => assertDatasetContract({
    ...expected,
    columns: undefined,
    status: "active",
    binding_locked: true,
  }, columns, expected));
  assert.throws(() => assertDatasetContract({
    ...expected,
    columns: undefined,
    data_node_id: "node-b",
    status: "active",
  }, columns, expected), /data_node_id/);
  assert.throws(() => assertDatasetContract({
    ...expected,
    columns: undefined,
    status: "active",
  }, columns.slice(1), expected), /columns/);
});

test("counts only active USDT symbol memberships", () => {
  const records = [
    record("BTC-USDT", { status: "active", quote_asset: "USDT" }),
    record("ETH-USDT", { status: "active", quote_asset: "USDT" }),
    record("OLD-USDT", { status: "inactive", quote_asset: "USDT" }),
    record("BTC-USDC", { status: "active", quote_asset: "USDC" }),
  ];
  const memberships = [
    membership("BTC-USDT", "active"),
    membership("ETH-USDT", "active"),
    membership("OLD-USDT", "inactive"),
    membership("BTC-USDC", "inactive"),
  ];
  const symbols = [
    symbol("BTC-USDT", "BTCUSDT"),
    symbol("ETH-USDT", "ETHUSDT"),
    symbol("OLD-USDT", "OLDUSDT"),
    symbol("BTC-USDC", "BTCUSDC"),
  ];
  assert.deepEqual([...activeUSDTSymbolIDs(records, memberships, symbols)], ["BTC-USDT", "ETH-USDT"]);
});

test("active symbol records, memberships, and mappings must be identical sets", () => {
  assert.throws(() => activeUSDTSymbolIDs(
    [record("BTC-USDT", { status: "active", quote_asset: "USDT" })],
    [membership("ETH-USDT", "active")],
    [symbol("BTC-USDT", "BTCUSDT")],
  ), /set mismatch/);
  assert.throws(() => activeUSDTSymbolIDs(
    [record("BTC-USDT", { status: "active", quote_asset: "USDT" })],
    [membership("BTC-USDT", "active")],
    [],
  ), /exactly one active binance symbol/);
  assert.throws(() => activeUSDTSymbolIDs(
    [record("ETH-USDT", { status: "active", quote_asset: "USDT" })],
    [membership("ETH-USDT", "active")],
    [symbol("ETH-USDT", "ETHUSDT")],
  ), /must contain BTC-USDT/);
});

test("matches kline jobs to active symbol subjects", () => {
  const subjects = new Map([
    ["BTC-USDT", "BTCUSDT"],
    ["ETH-USDT", "ETHUSDT"],
  ]);
  const jobs = [
    job("BTC-USDT", "BTCUSDT"),
    job("ETH-USDT", "ETHUSDT"),
  ];
  assert.doesNotThrow(() => validateKlineJobs(jobs, subjects, "e2e_binance_kline_1m"));
});

test("pagination follows server-clamped page size", () => {
  assert.equal(hasMorePages({ size: 100, total: 449 }, 100, 100, 500), true);
  assert.equal(hasMorePages({ size: 100, total: 449 }, 449, 49, 500), false);
  assert.equal(hasMorePages({}, 25, 25, 25), true);
  assert.equal(hasMorePages({}, 20, 20, 25), false);
});

test("10 active and 2 inactive memberships expect only 10 kline jobs", () => {
  const subjects = new Map(Array.from({ length: 10 }, (_, index) => [
    `S${index}-USDT`,
    `S${index}USDT`,
  ]));
  const jobs = [...subjects].map(([subjectID, external]) => job(subjectID, external));
  assert.equal(validateKlineJobs(jobs, subjects, "e2e_binance_kline_1m").executeAt, "2026-07-27T12:35:00Z");
  assert.equal(jobs.length, 10);
});

test("rejects duplicate subjects or missing external symbol", () => {
  const subjects = new Map([["BTC-USDT", "BTCUSDT"]]);
  assert.throws(() => validateKlineJobs([
    job("BTC-USDT", "BTCUSDT"),
    job("BTC-USDT", "BTCUSDT"),
  ], subjects, "e2e_binance_kline_1m"), /duplicate subject/);
  assert.throws(() => validateKlineJobs([
    job("BTC-USDT", ""),
  ], subjects, "e2e_binance_kline_1m"), /symbol/);
});

test("rejects kline jobs targeting the symbol dataset", () => {
  const wrong = job("BTC-USDT", "BTCUSDT");
  wrong.params.dataset_id = "e2e_binance_symbols";
  assert.throws(() => validateKlineJobs(
    [wrong],
    new Map([["BTC-USDT", "BTCUSDT"]]),
    "e2e_binance_kline_1m",
  ), /target dataset/);
});

test("rejects inconsistent execute_at windows and unstable job ids", () => {
  const subjects = new Map([
    ["BTC-USDT", "BTCUSDT"],
    ["ETH-USDT", "ETHUSDT"],
  ]);
  assert.throws(() => validateKlineJobs([
    job("BTC-USDT", "BTCUSDT"),
    job("ETH-USDT", "ETHUSDT", { execute_at: "2026-07-27T12:36:00Z" }),
  ], subjects, "e2e_binance_kline_1m"), /execute_at/);
  assert.throws(() => validateKlineJobs([
    job("BTC-USDT", "BTCUSDT", { job_item_id: "unrelated-id" }),
    job("ETH-USDT", "ETHUSDT"),
  ], subjects, "e2e_binance_kline_1m"), /job_item_id/);
});

test("accepts zero-write only with no_new_closed_kline", () => {
  assert.equal(acceptKlineWriteEvidence({
    rows_written: 0,
    zero_write_reason: "no_new_closed_kline",
  }), 0);
  assert.equal(acceptKlineWriteEvidence({ rows_written: 2 }), 2);
  assert.throws(() => acceptKlineWriteEvidence({ rows_written: 0 }), /no_new_closed_kline/);
});

test("collects positive Kline write evidence in the requested Storage scope", () => {
  const item = job("BTC-USDT", "BTCUSDT");
  item.status = "JOB_ITEM_STATUS_SUCCESS";
  item.execution_node = "node-1";
  item.result_summary = {
    tasks: [{
      data_type: "kline",
      symbol: "BTCUSDT",
      interval: "1m",
      rows_written: 1,
      storage_read_scope: {
        space_id: "crypto",
        dataset_id: "e2e_binance_kline_1m",
        subject_id: "BTC-USDT",
        freq: "1m",
      },
      written_row_key_samples: [{
        space_id: "crypto",
        dataset_id: "e2e_binance_kline_1m",
        subject_id: "BTC-USDT",
        freq: "1m",
        data_time: "2026-07-27T12:34:00Z",
      }],
    }],
  };
  const evidence = collectKlineWriteEvidence([item], "crypto", "e2e_binance_kline_1m");
  assert.equal(evidence.rowsWritten, 1);
  assert.equal(evidence.samples.length, 1);
  assert.equal(evidence.samples[0].executionNode, "node-1");
  assert.equal(evidence.samples[0].key.subject_id, "BTC-USDT");
  assert.deepEqual([...evidence.executionNodes], ["node-1"]);

  item.result_summary.tasks[0].storage_read_scope.dataset_id = "e2e_binance_symbols";
  assert.throws(
    () => collectKlineWriteEvidence([item], "crypto", "e2e_binance_kline_1m"),
    /Storage scope/,
  );
});

test("zero-write success does not count as a distinct writing SCF node", () => {
  const positive = job("BTC-USDT", "BTCUSDT");
  positive.status = "JOB_ITEM_STATUS_SUCCESS";
  positive.execution_node = "node-write";
  positive.result_summary = {
    tasks: [{
      data_type: "kline",
      symbol: "BTCUSDT",
      interval: "1m",
      rows_written: 1,
      storage_read_scope: {
        space_id: "crypto",
        dataset_id: "e2e_binance_kline_1m",
        subject_id: "BTC-USDT",
        freq: "1m",
      },
      written_row_key_samples: [{
        space_id: "crypto",
        dataset_id: "e2e_binance_kline_1m",
        subject_id: "BTC-USDT",
        freq: "1m",
        data_time: "2026-07-27T12:34:00Z",
      }],
    }],
  };
  const zero = job("ETH-USDT", "ETHUSDT");
  zero.status = "JOB_ITEM_STATUS_SUCCESS";
  zero.execution_node = "node-zero";
  zero.result_summary = {
    tasks: [{
      data_type: "kline",
      symbol: "ETHUSDT",
      interval: "1m",
      rows_written: 0,
      zero_write_reason: "no_new_closed_kline",
      storage_read_scope: {
        space_id: "crypto",
        dataset_id: "e2e_binance_kline_1m",
        subject_id: "ETH-USDT",
        freq: "1m",
      },
    }],
  };

  const evidence = collectKlineWriteEvidence(
    [positive, zero],
    "crypto",
    "e2e_binance_kline_1m",
  );
  assert.deepEqual([...evidence.executionNodes], ["node-write"]);
});

test("validates sampled 1m OHLCV rows", () => {
  const row = {
    key: {
      space_id: "crypto",
      dataset_id: "e2e_binance_kline_1m",
      subject_id: "BTC-USDT",
      freq: "1m",
      data_time: "2026-07-27T12:34:00Z",
    },
    fields: [
      field("open", 100),
      field("high", 110),
      field("low", 90),
      field("close", 105),
      field("volume", 12.5),
      field("quote_volume", 1300),
      field("trade_num", 42),
    ],
  };
  assert.doesNotThrow(() => validateKlineStorageRow(row, row.key));
  const invalid = structuredClone(row);
  invalid.fields.find((item) => item.field_id === "high").value = typed(99);
  assert.throws(() => validateKlineStorageRow(invalid, invalid.key), /high/);
});

test("never places storage endpoints or secrets in job params", () => {
  assert.doesNotThrow(() => assertNoRuntimeParams({
    space_id: "crypto",
    dataset_id: "e2e_binance_kline_1m",
    subject_id: "BTC-USDT",
  }));
  for (const key of [
    "storage_target",
    "storage_rpc_gateway_target",
    "storage_ip_port",
    "service_gateway_target",
    "eventbus_url",
    "app_key",
    "secret",
    "hmac",
  ]) {
    assert.throws(() => assertNoRuntimeParams({ nested: { [key]: "forbidden" } }), /runtime parameter/);
  }
});

test("builds exactly 50 unique SCF node definitions", () => {
  const nodes = buildSCFNodeDefinitions("e2e-collector", 50);
  assert.equal(nodes.length, 50);
  assert.equal(new Set(nodes.map((item) => item.node_id)).size, 50);
  assert.equal(new Set(nodes.map((item) => item.function_name)).size, 50);
});

test("validates created and updated fleet publish summaries", () => {
  assert.equal(validatePublishSummary({
    package_id: "package-new",
    fleet_mode: "created",
    create_batch_ids: Array.from({ length: 10 }, (_, index) => `create-${index}`),
    create_processed_count: 50,
  }, 50).package_id, "package-new");
  assert.equal(validatePublishSummary({
    package_id: "package-new",
    fleet_mode: "updated",
    deploy_batch_ids: Array.from({ length: 5 }, (_, index) => `deploy-${index}`),
    deploy_processed_count: 50,
  }, 50).fleet_mode, "updated");
  assert.throws(() => validatePublishSummary({
    package_id: "package-new",
    fleet_mode: "created",
    create_processed_count: 49,
  }, 50), /processed 49/);
});

test("fleet readiness requires exactly 50 fresh online Tencent SCFs", () => {
  const now = Date.parse("2026-07-27T12:00:00Z");
  const nodes = Array.from({ length: 50 }, (_, index) => fleetNode(index, now));
  assert.equal(verifiedSCFFleet(nodes, {
    cloudAccount: "account",
    region: "ap-shanghai",
    fleetPrefix: "e2e-fleet",
    packageID: "package-new",
    packageVersion: "v1",
    scfCount: 50,
  }, now).length, 50);
  assert.throws(() => verifiedSCFFleet(nodes.slice(1), {
    cloudAccount: "account",
    region: "ap-shanghai",
    fleetPrefix: "e2e-fleet",
    packageID: "package-new",
    packageVersion: "v1",
    scfCount: 50,
  }, now), /has 49 nodes/);
  assert.throws(() => verifiedSCFFleet([
    { ...nodes[0], last_heartbeat: "2026-07-27T11:55:00Z" },
    ...nodes.slice(1),
  ], {
    cloudAccount: "account",
    region: "ap-shanghai",
    fleetPrefix: "e2e-fleet",
    packageID: "package-new",
    packageVersion: "v1",
    scfCount: 50,
  }, now), /stale heartbeat/);

  const heartbeatNodes = nodes.map((node) => ({
    ...node,
    metadata: {
      arch: "amd64",
      version: "runtime",
      runtime_code_package_id: "package-new",
    },
  }));
  assert.equal(verifiedSCFFleet(heartbeatNodes, {
    cloudAccount: "account",
    region: "ap-shanghai",
    fleetPrefix: "e2e-fleet",
    packageID: "package-new",
    packageVersion: "v1",
    scfCount: 50,
  }, now).length, 50);
  assert.throws(() => verifiedSCFFleet([
    { ...nodes[0], running_version: "old-version" },
    ...nodes.slice(1),
  ], {
    cloudAccount: "account",
    region: "ap-shanghai",
    fleetPrefix: "e2e-fleet",
    packageID: "package-new",
    packageVersion: "v1",
    scfCount: 50,
  }, now), /running_version/);
  assert.throws(() => verifiedSCFFleet([
    { ...nodes[0], metadata: { ...nodes[0].metadata, runtime_code_package_id: "package-old" } },
    ...nodes.slice(1),
  ], {
    cloudAccount: "account",
    region: "ap-shanghai",
    fleetPrefix: "e2e-fleet",
    packageID: "package-new",
    packageVersion: "v1",
    scfCount: 50,
  }, now), /runtime_code_package_id/);
});

test("state is written with mode 0600 and contains no secrets", async () => {
  const directory = await mkdtemp(join(tmpdir(), "moox-symbol-kline-"));
  const path = join(directory, "state.json");
  await writeState(path, {
    run_id: "run",
    symbol_dataset_id: "e2e_binance_symbols",
  });
  assert.equal((await stat(path)).mode & 0o777, 0o600);
  assert.equal(JSON.parse(await readFile(path, "utf8")).run_id, "run");
  await assert.rejects(() => writeState(path, { secret: "oops" }), /secret/);
});

test("parses all fixture phases and enforces positive SCF count", () => {
  for (const phase of ["setup", "symbols", "klines", "assert", "cleanup"]) {
    assert.equal(parseArgs(["--phase", phase, "--state-file", "/tmp/state"]).phase, phase);
  }
  const fleet = parseArgs([
    "--phase", "fleet",
    "--state-file", "/tmp/state",
    "--publish-summary", "/tmp/publish.json",
    "--cloud-account", "account",
    "--package-version", "v1",
    "--region", "ap-shanghai",
  ]);
  assert.equal(fleet.publishSummary, "/tmp/publish.json");
  assert.throws(
    () => parseArgs(["--phase", "setup", "--state-file", "/tmp/state", "--scf-count", "0"]),
    /positive integer/,
  );
});

test("rejects non-E2E Rule IDs before making cleanup-capable requests", () => {
  assert.throws(() => parseArgs([
    "--phase", "setup",
    "--state-file", "/tmp/state.json",
    "--symbol-rule", "production_symbols",
  ]), /must start with e2e_/);
  assert.throws(() => parseArgs([
    "--phase", "setup",
    "--state-file", "/tmp/state.json",
    "--symbol-rule", "e2e_same",
    "--kline-rule", "e2e_same",
  ]), /must differ/);
});

test("cleanup restores the exact run scope from state", () => {
  const args = parseArgs(["--phase", "cleanup", "--state-file", "/tmp/state.json"]);
  restoreCleanupScope(args, {
    space_id: "crypto-old",
    symbol_dataset_id: "e2e_symbols_old",
    kline_dataset_id: "e2e_kline_old",
    symbol_rule_id: "e2e_symbols_old_1m",
    kline_rule_id: "e2e_kline_old_1m",
    rule_owner: "collector-symbol-kline-e2e:old",
  });
  assert.equal(args.space, "crypto-old");
  assert.equal(args.symbolDataset, "e2e_symbols_old");
  assert.equal(args.klineDataset, "e2e_kline_old");
  assert.equal(args.symbolRule, "e2e_symbols_old_1m");
  assert.equal(args.klineRule, "e2e_kline_old_1m");
  assert.equal(args.ruleOwner, "collector-symbol-kline-e2e:old");
});

function typed(value) {
  if (typeof value === "string") return { string_value: value };
  if (Number.isInteger(value)) return { int_value: String(value) };
  return { double_value: value };
}

function record(subjectID, fields) {
  return {
    key: { record_id: subjectID, version: "2026-07-27T12:00:00Z" },
    fields: Object.entries(fields).map(([field_id, value]) => ({ field_id, value: typed(value) })),
  };
}

function field(fieldID, value) {
  return { field_id: fieldID, value: typed(value) };
}

function membership(subjectID, status) {
  return {
    subject_id: subjectID,
    dataset_id: "e2e_binance_symbols",
    status,
  };
}

function symbol(subjectID, externalSymbol) {
  return {
    subject_id: subjectID,
    data_source_id: "binance",
    external_symbol: externalSymbol,
    status: "active",
  };
}

function job(subjectID, externalSymbol, overrides = {}) {
  const executeAt = overrides.execute_at || "2026-07-27T12:35:00Z";
  return {
    job_item_id: `e2e-kline-${subjectID}-${executeAt}`,
    job_type: "collect.binance.kline",
    execute_at: executeAt,
    params: {
      space_id: "crypto",
      dataset_id: "e2e_binance_kline_1m",
      data_type: "kline",
      market: "spot",
      subject_id: subjectID,
      symbol: externalSymbol,
      interval: "1m",
    },
    ...overrides,
  };
}

function fleetNode(index, now) {
  return {
    node_id: `node-${index}`,
    cloud_account_id: "account",
    package_id: "package-new",
    package_version: "v1",
    running_version: "v1",
    region: "ap-shanghai",
    node_type: "scf-event",
    provider: "tencent-scf",
    function_name: `e2e-fleet-ap-shanghai-${index}`,
    supported_workloads: ["collect.binance.symbol", "collect.binance.kline"],
    metadata: {
      function_name_prefix: "e2e-fleet",
      index,
      runtime_code_package_id: "package-new",
    },
    status: "NODE_STATUS_ONLINE",
    last_heartbeat: new Date(now - 30_000).toISOString(),
  };
}
