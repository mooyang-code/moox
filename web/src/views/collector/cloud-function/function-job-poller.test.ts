import { afterEach, describe, expect, it, vi } from "vitest";
import type { GetNodeBatchChangeResponse } from "@/api/cloud-node";
import { createCloudNodeBatchPoller } from "./cloud-node-batch-poller";

function response(status: GetNodeBatchChangeResponse["job"]["status"]): GetNodeBatchChangeResponse {
  return {
    job: {
      job_id: "node-batch-1",
      operation: "NODE_BATCH_OPERATION_CREATE_NODES",
      status,
      total_count: 2,
      pending_count: status === "NODE_BATCH_STATUS_PENDING" ? 2 : 0,
      running_count: 0,
      success_count: status === "NODE_BATCH_STATUS_SUCCESS" ? 2 : 0,
      failed_count: 0,
      progress_percent: status === "NODE_BATCH_STATUS_SUCCESS" ? 100 : 0,
      created_at: "2026-07-28T00:00:00Z"
    },
    items: []
  };
}

describe("cloud node batch poller", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("polls immediately and then every two seconds", async () => {
    vi.useFakeTimers();
    const query = vi.fn().mockResolvedValue(response("NODE_BATCH_STATUS_RUNNING"));
    const poller = createCloudNodeBatchPoller(query);

    await poller.start("node-batch-1");
    expect(query).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(2000);
    expect(query).toHaveBeenCalledTimes(2);
    poller.dispose();
  });

  it.each(["NODE_BATCH_STATUS_SUCCESS", "NODE_BATCH_STATUS_FAILED", "NODE_BATCH_STATUS_PARTIAL"] as const)(
    "stops on %s",
    async status => {
      vi.useFakeTimers();
      const query = vi.fn().mockResolvedValue(response(status));
      const onTerminal = vi.fn();
      const poller = createCloudNodeBatchPoller(query);

      await poller.start("node-batch-1", { onTerminal });
      await vi.advanceTimersByTimeAsync(4000);

      expect(query).toHaveBeenCalledTimes(1);
      expect(onTerminal).toHaveBeenCalledWith(response(status));
      poller.dispose();
    }
  );

  it("keeps polling after a transient query error", async () => {
    vi.useFakeTimers();
    const query = vi.fn().mockRejectedValueOnce(new Error("temporary")).mockResolvedValue(response("NODE_BATCH_STATUS_RUNNING"));
    const onError = vi.fn();
    const poller = createCloudNodeBatchPoller(query);

    await poller.start("node-batch-1", { onError });
    await vi.advanceTimersByTimeAsync(2000);

    expect(query).toHaveBeenCalledTimes(2);
    expect(onError).toHaveBeenCalledTimes(1);
    poller.dispose();
  });

  it("stops locally after thirty minutes without changing backend state", async () => {
    vi.useFakeTimers();
    const query = vi.fn().mockResolvedValue(response("NODE_BATCH_STATUS_RUNNING"));
    const onPollingTimeout = vi.fn();
    const poller = createCloudNodeBatchPoller(query);

    await poller.start("node-batch-1", { onPollingTimeout });
    await vi.advanceTimersByTimeAsync(30 * 60 * 1000);
    const callsAtTimeout = query.mock.calls.length;
    await vi.advanceTimersByTimeAsync(10_000);

    expect(onPollingTimeout).toHaveBeenCalledWith("node-batch-1");
    expect(query).toHaveBeenCalledTimes(callsAtTimeout);
    poller.dispose();
  });

  it("replaces the active job and disposes timers", async () => {
    vi.useFakeTimers();
    const query = vi.fn().mockResolvedValue(response("NODE_BATCH_STATUS_RUNNING"));
    const poller = createCloudNodeBatchPoller(query);

    await poller.start("node-batch-1");
    await poller.start("node-batch-2");
    poller.dispose();
    const callsAfterDispose = query.mock.calls.length;
    await vi.advanceTimersByTimeAsync(4000);

    expect(query.mock.calls[0][0]).toBe("node-batch-1");
    expect(query.mock.calls[1][0]).toBe("node-batch-2");
    expect(query).toHaveBeenCalledTimes(callsAfterDispose);
  });
});
