import assert from "node:assert/strict";
import test from "node:test";
import * as verify from "./verify.mjs";

import {
  JOB_STATUS,
  assertFutureExecutionTimes,
  currentScheduledJobItems,
  currentScheduledTaskInstances,
  failedJobItems,
  scheduleLeadDelay,
  statusName,
} from "./verify.mjs";

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
