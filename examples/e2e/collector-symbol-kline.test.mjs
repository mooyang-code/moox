import assert from "node:assert/strict";
import { createHash, createHmac } from "node:crypto";
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
  fieldContractMatches,
  fleetCLSTopicID,
  hasMorePages,
  parseArgs,
  restoreCleanupScope,
  searchCLSLogLines,
  selectKlineLifecycleCandidates,
  validateKlineStorageRow,
  validateCLSLifecycle,
  validatePublishSummary,
  validateSymbolWriteCount,
  signedHeaders,
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

test("requires one shared CLS topic across the SCF fleet", () => {
  assert.equal(fleetCLSTopicID([
    { metadata: { cls_topic_id: "topic-a" } },
    { metadata: { cls_topic_id: "topic-a" } },
  ]), "topic-a");
  assert.equal(fleetCLSTopicID([{ metadata: {} }], "topic-from-publish"), "topic-from-publish");
  assert.throws(() => fleetCLSTopicID([
    { metadata: { cls_topic_id: "topic-a" } },
    { metadata: { cls_topic_id: "topic-b" } },
  ]), /exactly one CLS topic_id/);
});

test("existing field contract accepts Storage scalar and protobuf enum names", () => {
  assert.equal(fieldContractMatches({ value_type: "string", status: "active" }, "string"), true);
  assert.equal(fieldContractMatches({ value_type: "FIELD_VALUE_TYPE_STRING", status: "active" }, "string"), true);
  assert.equal(fieldContractMatches({ value_type: 1, status: "active" }, "string"), true);
  assert.equal(fieldContractMatches({ value_type: "double", status: "disabled" }, "double"), false);
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

test("dataset contract accepts numeric protobuf enum responses", () => {
  const expected = buildSymbolDatasetContract("crypto", "e2e_symbols", "node-a");
  const valueCodes = {
    FIELD_VALUE_TYPE_STRING: 1,
    FIELD_VALUE_TYPE_INT: 2,
    FIELD_VALUE_TYPE_DOUBLE: 3,
  };
  assert.doesNotThrow(() => assertDatasetContract(
    { ...expected, data_kind: 1, status: "disabled" },
    expected.columns.map((column) => ({ ...column, origin_type: 1, value_type: valueCodes[column.value_type] })),
    expected,
  ));
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

test("Symbol rows_written must equal the active subject count", () => {
  const subjectIDs = new Set(["BTC-USDT", "ETH-USDT"]);
  assert.equal(validateSymbolWriteCount({ rows_written: 2 }, subjectIDs), 2);
  assert.throws(
    () => validateSymbolWriteCount({ rows_written: 3 }, subjectIDs),
    /rows_written=3 want=2/,
  );
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

test("derives empty from zero rows and requires a watermark for positive writes", () => {
  assert.equal(acceptKlineWriteEvidence({ rows_written: 0 }), 0);
  assert.equal(acceptKlineWriteEvidence({
    rows_written: 2, output_watermark: "2026-07-27T12:34:00Z",
  }), 2);
  assert.throws(() => acceptKlineWriteEvidence({ rows_written: 2 }), /output_watermark/);
});

test("collects positive Kline write evidence in the requested Storage scope", () => {
  const item = job("BTC-USDT", "BTCUSDT");
  item.status = "JOB_ITEM_STATUS_SUCCESS";
  item.execution_node = "node-1";
  item.result_summary = {
    tasks: [{
      data_type: "kline",
      dataset_id: "e2e_binance_kline_1m",
      freq: "1m",
      rows_written: 1,
      output_watermark: "2026-07-27T12:34:00Z",
    }],
  };
  const evidence = collectKlineWriteEvidence([item], "crypto", "e2e_binance_kline_1m");
  assert.equal(evidence.rowsWritten, 1);
  assert.equal(evidence.samples.length, 1);
  assert.equal(evidence.samples[0].executionNode, "node-1");
  assert.equal(evidence.samples[0].key.subject_id, "BTC-USDT");
  assert.deepEqual([...evidence.executionNodes], ["node-1"]);

  item.result_summary.tasks[0].dataset_id = "e2e_binance_symbols";
  assert.throws(
    () => collectKlineWriteEvidence([item], "crypto", "e2e_binance_kline_1m"),
    /does not match its request/,
  );
});

test("zero-write success does not count as a distinct writing SCF node", () => {
  const positive = job("BTC-USDT", "BTCUSDT");
  positive.status = "JOB_ITEM_STATUS_SUCCESS";
  positive.execution_node = "node-write";
  positive.result_summary = {
    tasks: [{
      data_type: "kline",
      dataset_id: "e2e_binance_kline_1m",
      freq: "1m",
      rows_written: 1,
      output_watermark: "2026-07-27T12:34:00Z",
    }],
  };
  const zero = job("ETH-USDT", "ETHUSDT");
  zero.status = "JOB_ITEM_STATUS_SUCCESS";
  zero.execution_node = "node-zero";
  zero.result_summary = {
    tasks: [{
      data_type: "kline",
      dataset_id: "e2e_binance_kline_1m",
      freq: "1m",
      rows_written: 0,
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
  const expectedKey = {
    space_id: "crypto",
    dataset_id: "e2e_binance_kline_1m",
    subject_id: "BTC-USDT",
    freq: "1m",
    data_time: "2026-07-27T12:34:00Z",
  };
  const row = {
    key: {
      ...expectedKey,
      dimensions: {},
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
  assert.doesNotThrow(() => validateKlineStorageRow(row, expectedKey));
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

test("validates successful asynchronous fleet publish summaries", () => {
  assert.equal(validatePublishSummary({
    package_id: "package-new",
    fleet_mode: "created",
    job_id: "node-batch-create",
    operation: "create_nodes",
    total_count: 50,
    job: {
      job_id: "node-batch-create",
      operation: "NODE_BATCH_OPERATION_CREATE_NODES",
      status: "NODE_BATCH_STATUS_SUCCESS",
      total_count: 50,
      pending_count: 0,
      running_count: 0,
      success_count: 50,
      failed_count: 0,
      progress_percent: 100,
    },
    items: Array.from({ length: 50 }, (_, index) => ({
      item_id: `create-${index}`,
      status: "NODE_BATCH_ITEM_STATUS_SUCCESS",
    })),
  }, 50).package_id, "package-new");
  assert.equal(validatePublishSummary({
    package_id: "package-new",
    fleet_mode: "updated",
    job_id: "node-batch-deploy",
    operation: "deploy_nodes",
    total_count: 50,
    job: {
      job_id: "node-batch-deploy",
      operation: "NODE_BATCH_OPERATION_DEPLOY_NODES",
      status: "NODE_BATCH_STATUS_SUCCESS",
      total_count: 50,
      pending_count: 0,
      running_count: 0,
      success_count: 50,
      failed_count: 0,
      progress_percent: 100,
    },
    items: Array.from({ length: 50 }, (_, index) => ({
      item_id: `deploy-${index}`,
      status: "NODE_BATCH_ITEM_STATUS_SUCCESS",
    })),
  }, 50).fleet_mode, "updated");
  assert.throws(() => validatePublishSummary({
    package_id: "package-new",
    fleet_mode: "created",
    job_id: "node-batch-running",
    operation: "create_nodes",
    total_count: 50,
    job: {
      job_id: "node-batch-running",
      operation: "NODE_BATCH_OPERATION_CREATE_NODES",
      status: "NODE_BATCH_STATUS_RUNNING",
      total_count: 50,
      success_count: 49,
      failed_count: 0,
    },
    items: [],
  }, 50), /status/);
  assert.throws(() => validatePublishSummary({
    package_id: "package-new",
    fleet_mode: "created",
    job_id: "node-batch-failed",
    operation: "create_nodes",
    total_count: 50,
    job: {
      job_id: "node-batch-failed",
      operation: "NODE_BATCH_OPERATION_CREATE_NODES",
      status: "NODE_BATCH_STATUS_PARTIAL",
      total_count: 50,
      success_count: 49,
      failed_count: 1,
    },
    items: [],
  }, 50), /status/);
});

test("request signature includes empty app headers when space is signed", () => {
  const payload = JSON.stringify({ space_id: "crypto" });
  const headers = signedHeaders("signing-key", "/api/admin/cloudnode/GetNodeList", payload, {
    "X-Space-Id": "crypto",
  });
  const canonical = [
    "moox-request-v1",
    "POST",
    "/api/admin/cloudnode/GetNodeList",
    "x-app-id:",
    "x-app-key:",
    "x-space-id:crypto",
    createHash("sha256").update(payload).digest("hex"),
    headers["X-Moox-Timestamp"],
    headers["X-Moox-Nonce"],
  ].join("\n");
  assert.equal(
    headers["X-Moox-Signature"],
    createHmac("sha256", "signing-key").update(canonical).digest("hex"),
  );
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

test("validates CLS lifecycle, retry, HTTP, and Storage evidence", () => {
  const jobs = [
    { jobItemID: "symbol-job", kind: "symbol" },
    { jobItemID: "kline-job", kind: "kline" },
  ];
  const terminal = (id, requestID, completion) => [
    clsLine(requestID, `event="collector_job_received" job_item_id="${id}"`),
    clsLine(requestID, `event="collector_job_started" job_item_id="${id}"`),
    clsLine(requestID, `event="collector_job_done" job_item_id="${id}" status="success"`),
    clsLine(requestID, `event="collector_job_cloudnode_reported" job_item_id="${id}" status="success"`),
    clsLine(requestID, `event="collector_job_delivery_action" job_item_id="${id}" decision="ACK"`),
    clsLine(requestID, `collector_http_completed domain=api.binance.com status=200 duration_ms=8 job_item_id="${id}"`),
    clsLine(requestID, `${completion} job_item_id="${id}"`),
  ];
  const logs = [
    ...terminal("symbol-job", "symbol-request", "[SymbolCollector] 标的采集完成, dataset_id=e2e_symbols"),
    ...terminal("kline-job", "kline-request", "K线采集完成: dataset_id=e2e_klines count=1"),
    clsLine("retry-request", 'event="collector_job_deferred" job_item_id="kline-job"'),
    clsLine("retry-request", 'event="collector_job_delivery_action" job_item_id="kline-job" decision="RETRY"'),
  ];
  assert.equal(validateCLSLifecycle(logs, jobs), true);
  assert.throws(
    () => validateCLSLifecycle([...logs, "x509: certificate signed by unknown authority"], jobs),
    /forbidden failure/,
  );
  assert.throws(
    () => validateCLSLifecycle([...logs, "collector_http_failed domain=api.binance.com status=429 duration_ms=4"], jobs),
    /forbidden failure/,
  );
});

test("does not fill a JobItem lifecycle with logs from another JobItem", () => {
  const job = { jobItemID: "symbol-job", kind: "symbol" };
  const transport = [
    "collector_job_received",
    "collector_job_started",
    "collector_job_done",
    "collector_job_cloudnode_reported",
  ].map((event) => clsLine("job-request", `event="${event}" job_item_id="symbol-job"`));
  const logs = [
    ...transport,
    clsLine("job-request", 'event="collector_job_delivery_action" job_item_id="symbol-job" decision="ACK"'),
    clsLine("retry-request", 'event="collector_job_deferred" job_item_id="symbol-job"'),
    clsLine("retry-request", 'event="collector_job_delivery_action" job_item_id="symbol-job" decision="RETRY"'),
    clsLine(
      "unrelated-request",
      'collector_http_completed domain=api.binance.com status=200 job_item_id="symbol-job" job_item_id="other-job"',
    ),
    clsLine(
      "unrelated-request",
      '[SymbolCollector] 标的采集完成 job_item_id="symbol-job" job_item_id="other-job"',
    ),
  ];
  assert.throws(
    () => validateCLSLifecycle(logs, [job]),
    /for JobItem/,
  );
});

test("accepts ten complete Kline lifecycles from a larger distributed sample", () => {
  const symbol = { jobItemID: "symbol-job", kind: "symbol" };
  const klines = Array.from({ length: 20 }, (_, index) => ({
    jobItemID: `kline-${index}`,
    kind: "kline",
  }));
  const terminal = (id, completion = "K线采集完成") => [
    clsLine(`${id}-request`, `event="collector_job_received" job_item_id="${id}"`),
    clsLine(`${id}-request`, `event="collector_job_started" job_item_id="${id}"`),
    clsLine(`${id}-request`, `event="collector_job_done" job_item_id="${id}" status="success"`),
    clsLine(`${id}-request`, `event="collector_job_cloudnode_reported" job_item_id="${id}" status="success"`),
    clsLine(`${id}-request`, `event="collector_job_delivery_action" job_item_id="${id}" decision="ACK"`),
    clsLine(`${id}-request`, `collector_http_completed status=200 job_item_id="${id}"`),
    clsLine(`${id}-request`, `${completion} job_item_id="${id}"`),
  ];
  const logs = [
    ...terminal(symbol.jobItemID, "[SymbolCollector] 标的采集完成"),
    ...klines.slice(0, 10).flatMap((job) => terminal(job.jobItemID)),
    clsLine("symbol-retry", 'event="collector_job_deferred" job_item_id="symbol-job"'),
    clsLine("symbol-retry", 'event="collector_job_delivery_action" job_item_id="symbol-job" decision="RETRY"'),
  ];
  assert.equal(validateCLSLifecycle(logs, [symbol, ...klines]), true);
  assert.throws(
    () => validateCLSLifecycle(logs.filter((line) => !line.includes("kline-9")), [symbol, ...klines]),
    /JobItem lifecycle count=9/,
  );
});

test("selects Kline lifecycle candidates across execution nodes first", () => {
  const candidates = selectKlineLifecycleCandidates([
    { job_item_id: "job-a1", execution_node: "node-a" },
    { job_item_id: "job-a2", execution_node: "node-a" },
    { job_item_id: "job-b1", execution_node: "node-b" },
    { job_item_id: "job-c1", execution_node: "node-c" },
  ], 3);
  assert.deepEqual(candidates.map((item) => item.jobItemID), ["job-a1", "job-b1", "job-c1"]);
});

test("CLS SearchLog follows Context pagination", async () => {
  const originalFetch = globalThis.fetch;
  const originalID = process.env.MOOX_CLS_SECRET_ID;
  const originalKey = process.env.MOOX_CLS_SECRET_KEY;
  process.env.MOOX_CLS_SECRET_ID = "test-id";
  process.env.MOOX_CLS_SECRET_KEY = "test-key";
  const bodies = [];
  globalThis.fetch = async (_url, options) => {
    bodies.push(JSON.parse(options.body));
    const second = bodies.length === 2;
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify({
        Response: {
          ListOver: second,
          Context: second ? "" : "next-page",
          Results: [{ LogJson: JSON.stringify({ content: second ? "second" : "first" }) }],
        },
      }),
    };
  };
  try {
    const lines = await searchCLSLogLines({
      topicID: "topic-a",
      region: "ap-guangzhou",
      from: 1,
      to: 2,
      terms: ["job-a"],
    });
    assert.equal(lines.length, 2);
    assert.equal(bodies.length, 2);
    assert.equal(bodies[0].Context, undefined);
    assert.equal(bodies[1].Context, "next-page");
  } finally {
    globalThis.fetch = originalFetch;
    if (originalID === undefined) delete process.env.MOOX_CLS_SECRET_ID;
    else process.env.MOOX_CLS_SECRET_ID = originalID;
    if (originalKey === undefined) delete process.env.MOOX_CLS_SECRET_KEY;
    else process.env.MOOX_CLS_SECRET_KEY = originalKey;
  }
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

function clsLine(requestID, content) {
  return `${JSON.stringify({ SCF_RequestId: requestID, content })}\n${content}`;
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
