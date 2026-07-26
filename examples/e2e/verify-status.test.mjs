import assert from "node:assert/strict";
import test from "node:test";

import {
  JOB_STATUS,
  assertFutureExecutionTimes,
  currentScheduledJobItems,
  failedJobItems,
  scheduleLeadDelay,
  statusName,
} from "./verify.mjs";

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
