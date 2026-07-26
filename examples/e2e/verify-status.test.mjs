import assert from "node:assert/strict";
import test from "node:test";

import { JOB_STATUS, failedJobItems, statusName } from "./verify.mjs";

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
