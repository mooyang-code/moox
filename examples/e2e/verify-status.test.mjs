import assert from "node:assert/strict";
import test from "node:test";
import * as verify from "./verify.mjs";

import {
  JOB_STATUS,
  PUBLIC_DEPLOYMENTS,
  assertFutureExecutionTimes,
  assertRealExecutionNodes,
  assertRealSCFFleet,
  assertTaskInstanceWriteEvidence,
  collectStorageWriteEvidence,
  controlledFailureJob,
  currentRealSCFNodes,
  currentScheduledJobItems,
  currentScheduledTaskInstances,
  failedJobItems,
  parseArgs,
  primaryWrittenRowsRequest,
  scheduleLeadDelay,
  statusName,
} from "./verify.mjs";

test("public deployment rewrite excludes internal Storage routes", () => {
  assert.deepEqual(
    [...PUBLIC_DEPLOYMENTS].sort(),
    ["admin_gateway", "service_gateway", "web_host"],
  );
});

test("storage snapshot queries the target View without an empty subject selector", () => {
  assert.equal(typeof verify.datasetSnapshotRequest, "function");
  assert.deepEqual(
    verify.datasetSnapshotRequest({ space: "crypto" }),
    {
      space_id: "crypto",
      view_id: "binance_spot_1h_view",
      time_range: { start_time: "1970-01-01T00:00:00Z" },
      sorts: [{ field_name: "data_time", desc: true }],
      page: { page: 1, size: 200 },
    },
  );
});

test("storage write evidence accepts only an explicit K-line zero-write result", () => {
  assert.deepEqual(collectStorageWriteEvidence([{
    job_item_id: "zero",
    space_id: "crypto",
    params: {
      space_id: "crypto", dataset_id: "binance_spot_kline_1h", data_type: "kline",
      symbol: "BTCUSDT", subject_id: "BTC-USDT", interval: "1h",
    },
    result_summary: {
      tasks: [{
        data_type: "kline", symbol: "BTCUSDT", interval: "1h", rows_written: 0,
        zero_write_reason: "no_new_closed_kline",
        storage_read_scope: {
          space_id: "crypto", dataset_id: "binance_spot_kline_1h",
          subject_id: "BTC-USDT", freq: "1H",
        },
      }],
    },
  }]), { rowsWritten: 0, writtenKeys: [] });
  assert.throws(() => collectStorageWriteEvidence([{
    job_item_id: "bad-zero",
    space_id: "crypto",
    params: {
      space_id: "crypto", dataset_id: "binance_spot_kline_1h", data_type: "kline",
      symbol: "BTCUSDT", subject_id: "BTC-USDT", interval: "1h",
    },
    result_summary: { tasks: [{
      data_type: "kline", symbol: "BTCUSDT", interval: "1h", rows_written: 0,
      storage_read_scope: {
        space_id: "crypto", dataset_id: "binance_spot_kline_1h",
        subject_id: "BTC-USDT", freq: "1H",
      },
    }] },
  }]), /missing an accepted reason/);
});

test("storage write evidence requires every successful JobItem and requested interval", () => {
  const key = {
    space_id: "crypto",
    dataset_id: "binance_spot_kline_1h",
    subject_id: "BTC-USDT",
    freq: "1H",
    data_time: "2026-07-27T06:00:00Z",
  };
  const jobItems = [{
    job_item_id: "positive",
    space_id: "crypto",
    params: {
      space_id: "crypto", dataset_id: "binance_spot_kline_1h", data_type: "kline",
      symbol: "BTCUSDT", subject_id: "BTC-USDT", interval: "1h",
    },
    result_summary: {
      tasks: [{
        data_type: "kline", symbol: "BTCUSDT", interval: "1h", rows_written: 1,
        storage_read_scope: {
          space_id: "crypto", dataset_id: "binance_spot_kline_1h",
          subject_id: "BTC-USDT", freq: "1H",
        },
        written_row_key_samples: [key],
      }],
    },
  }];
  assert.deepEqual(collectStorageWriteEvidence(jobItems), { rowsWritten: 1, writtenKeys: [key] });
  assert.throws(() => collectStorageWriteEvidence([
    ...jobItems,
    { job_item_id: "missing", params: { interval: "1h" }, result_summary: {} },
  ]), /missing.*does not contain collection write evidence/);
  assert.throws(() => collectStorageWriteEvidence([{
    ...jobItems[0],
    params: { interval: "5m" },
  }]), /does not match requested intervals/);
  assert.throws(() => collectStorageWriteEvidence([{
    ...jobItems[0],
    params: { ...jobItems[0].params, symbol: "ETHUSDT", subject_id: "ETH-USDT" },
  }]), /does not match requested data_type\/symbol/);
});

test("TaskInstance persists the same write evidence as its JobItem", () => {
  const tasks = [{ data_type: "kline", interval: "1h", rows_written: 0, zero_write_reason: "no_new_closed_kline" }];
  assert.doesNotThrow(() => assertTaskInstanceWriteEvidence(
    [{ task_id: "task-1", cloud_job_item_id: "item-1", result: { tasks } }],
    [{ job_item_id: "item-1", result_summary: { tasks } }],
  ));
  assert.throws(() => assertTaskInstanceWriteEvidence(
    [{ task_id: "task-1", cloud_job_item_id: "item-1", result: {} }],
    [{ job_item_id: "item-1", result_summary: { tasks } }],
  ), /did not persist/);
});

test("exact written RowKey read is forced through Storage Primary", () => {
  const key = {
    space_id: "crypto", dataset_id: "binance_spot_kline_1h",
    subject_id: "BTC-USDT", freq: "1H", data_time: "2026-07-27T06:00:00Z",
  };
  assert.deepEqual(primaryWrittenRowsRequest(
    { space: "crypto", dataset: "binance_spot_kline_1h" },
    [key],
  ), {
    space_id: "crypto",
    dataset_id: "binance_spot_kline_1h",
    keys: [key],
    column_names: ["close"],
    page: { page: 1, size: 1 },
  });
});

test("CloudNode JobItem status values match the protobuf contract", () => {
  assert.equal(statusName(1, JOB_STATUS), "JOB_ITEM_STATUS_PENDING");
  assert.equal(statusName(3, JOB_STATUS), "JOB_ITEM_STATUS_SUCCESS");
  assert.equal(statusName(4, JOB_STATUS), "JOB_ITEM_STATUS_FAILED");
  assert.equal(statusName(6, JOB_STATUS), "JOB_ITEM_STATUS_ENQUEUE_FAILED");
});

test("failedJobItems includes enqueue failures for fail-fast assertions", () => {
  const rows = [
    { job_item_id: "pending", status: 1 },
    { job_item_id: "failed", status: 4 },
    { job_item_id: "enqueue", status: 6 },
  ];
  assert.deepEqual(failedJobItems(rows).map((item) => item.job_item_id), ["failed", "enqueue"]);
});

test("currentScheduledJobItems follows the task-instance job binding", () => {
  const instances = [{ cloud_job_item_id: "current-1" }, { cloud_job_item_id: "current-2" }];
  const rows = [
    { job_item_id: "old" },
    { job_item_id: "current-1" },
    { job_item_id: "current-2" },
  ];
  assert.deepEqual(
    currentScheduledJobItems(instances, rows).map((item) => item.job_item_id),
    ["current-1", "current-2"],
  );
});

test("currentScheduledTaskInstances excludes stale successful executions", () => {
  const instances = [
    { task_id: "stale", cloud_job_item_id: "old", last_exec_status: 2 },
    { task_id: "one", cloud_job_item_id: "current-1", last_exec_status: 2 },
    { task_id: "two", cloud_job_item_id: "current-2", last_exec_status: 1 },
  ];
  assert.deepEqual(
    currentScheduledTaskInstances(instances, new Set(["current-1", "current-2"])).map((item) => item.task_id),
    ["one", "two"],
  );
});

test("scheduled jobs require a valid future execute_at", () => {
  const now = Date.parse("2026-07-26T10:00:00Z");
  assert.doesNotThrow(() => assertFutureExecutionTimes([
    { job_item_id: "future", execute_at: "2026-07-26T10:00:01Z" },
  ], now));
  assert.throws(() => assertFutureExecutionTimes([
    { job_item_id: "past", execute_at: "2026-07-26T09:59:59Z" },
  ], now), /execute_at is not in the future/);
  assert.throws(() => assertFutureExecutionTimes([
    { job_item_id: "missing" },
  ], now), /execute_at is not in the future/);
});

test("schedule lead delay avoids a near-boundary E2E race", () => {
  assert.equal(scheduleLeadDelay(5_000, 30_000, 10_000), 0);
  assert.equal(scheduleLeadDelay(25_000, 30_000, 10_000), 5_025);
});

test("production setup can skip the synthetic local CloudNode", () => {
  const args = parseArgs([
    "--phase", "setup",
    "--state-file", "/tmp/moox-e2e-state.json",
    "--skip-cloud-node-setup",
  ]);
  assert.equal(args.skipCloudNodeSetup, true);
});

test("real SCF nodes must be online Tencent functions with a recent heartbeat", () => {
  const now = Date.parse("2026-07-27T04:00:00Z");
  const base = {
    provider: "tencent-scf",
    node_type: "scf-event",
    status: 2,
    last_heartbeat: "2026-07-27T03:59:30Z",
    supported_workloads: ["collect.binance.kline", "collect.binance.symbol"],
  };
  const nodes = [
    { ...base, node_id: "real" },
    { ...base, node_id: "local", provider: "local" },
    { ...base, node_id: "offline", status: 1 },
    { ...base, node_id: "stale", last_heartbeat: "2026-07-27T03:50:00Z" },
    { ...base, node_id: "wrong-type", node_type: "scf-polling" },
    { ...base, node_id: "wrong-workload", supported_workloads: ["collect.binance.symbol"] },
  ];
  assert.deepEqual(
    currentRealSCFNodes(nodes, now, 120_000).map((node) => node.node_id),
    ["real"],
  );
});

test("job execution must belong to a verified Tencent SCF node", () => {
  assert.doesNotThrow(() => assertRealExecutionNodes(
    [{ job_item_id: "one", execution_node: "scf-a" }],
    ["scf-a", "scf-b"],
  ));
  assert.throws(() => assertRealExecutionNodes(
    [{ job_item_id: "local", execution_node: "e2e-local" }],
    ["scf-a"],
  ), /not executed by verified Tencent SCF/);
  assert.throws(() => assertRealExecutionNodes(
    [{ job_item_id: "missing" }],
    ["scf-a"],
  ), /not executed by verified Tencent SCF/);
});

test("real SCF acceptance requires two distinct code packages", () => {
  assert.doesNotThrow(() => assertRealSCFFleet([
    { node_id: "a", package_id: "package-a" },
    { node_id: "b", package_id: "package-b" },
  ]));
  assert.throws(() => assertRealSCFFleet([
    { node_id: "a", package_id: "package-a" },
  ]), /two online tencent-scf nodes/);
  assert.throws(() => assertRealSCFFleet([
    { node_id: "a", package_id: "package-a" },
    { node_id: "b", package_id: "package-a" },
  ]), /different packages/);
});

test("controlled failure job is deterministic, small, and contains no credentials", () => {
  const item = controlledFailureJob(
    { space: "crypto", rule: "rule-1", dataset: "dataset-1" },
    { data_type: "kline", dataset_id: "dataset-1" },
    "fixed-run",
  );
  assert.equal(item.job_item_id, "e2e-failure-fixed-run");
  assert.equal(item.job_type, "collect.binance.kline");
  assert.equal(item.params.symbol, "INVALID-E2E-SYMBOL");
  assert.equal(item.params.task_id, "e2e-failure-fixed-run");
  assert.equal(item.execute_at, undefined);
  const encoded = JSON.stringify(item);
  assert.ok(encoded.length < 700, `controlled failure payload is too large: ${encoded.length}`);
  for (const forbidden of ["secret", "credential", "password", "access_key", "authorization"]) {
    assert.equal(encoded.toLowerCase().includes(forbidden), false, `payload contains ${forbidden}`);
  }
});
