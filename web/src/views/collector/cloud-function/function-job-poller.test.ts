import { afterEach, describe, expect, it, vi } from "vitest";
import type { GetFunctionJobResponse } from "@/api/function-instance";
import { createFunctionJobPoller } from "./function-job-poller";

function response(status: GetFunctionJobResponse["job"]["status"]): GetFunctionJobResponse {
  return {
    job: {
      job_id: "function-job-1",
      operation: "FUNCTION_JOB_OPERATION_CREATE_INSTANCES",
      status,
      total_count: 2,
      pending_count: status === "FUNCTION_JOB_STATUS_PENDING" ? 2 : 0,
      running_count: 0,
      success_count: status === "FUNCTION_JOB_STATUS_SUCCESS" ? 2 : 0,
      failed_count: 0,
      progress_percent: status === "FUNCTION_JOB_STATUS_SUCCESS" ? 100 : 0,
      created_at: "2026-07-28T00:00:00Z"
    },
    items: []
  };
}

describe("cloud instance batch poller", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("polls immediately and then every two seconds", async () => {
    vi.useFakeTimers();
    const query = vi.fn().mockResolvedValue(response("FUNCTION_JOB_STATUS_RUNNING"));
    const poller = createFunctionJobPoller(query);

    await poller.start("function-job-1");
    expect(query).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(2000);
    expect(query).toHaveBeenCalledTimes(2);
    poller.dispose();
  });

  it.each(["FUNCTION_JOB_STATUS_SUCCESS", "FUNCTION_JOB_STATUS_FAILED", "FUNCTION_JOB_STATUS_PARTIAL"] as const)(
    "stops on %s",
    async status => {
      vi.useFakeTimers();
      const query = vi.fn().mockResolvedValue(response(status));
      const onTerminal = vi.fn();
      const poller = createFunctionJobPoller(query);

      await poller.start("function-job-1", { onTerminal });
      await vi.advanceTimersByTimeAsync(4000);

      expect(query).toHaveBeenCalledTimes(1);
      expect(onTerminal).toHaveBeenCalledWith(response(status));
      poller.dispose();
    }
  );

  it("keeps polling after a transient query error", async () => {
    vi.useFakeTimers();
    const query = vi
      .fn()
      .mockRejectedValueOnce(new Error("temporary"))
      .mockResolvedValue(response("FUNCTION_JOB_STATUS_RUNNING"));
    const onError = vi.fn();
    const poller = createFunctionJobPoller(query);

    await poller.start("function-job-1", { onError });
    await vi.advanceTimersByTimeAsync(2000);

    expect(query).toHaveBeenCalledTimes(2);
    expect(onError).toHaveBeenCalledTimes(1);
    poller.dispose();
  });

  it("stops locally after thirty minutes without changing backend state", async () => {
    vi.useFakeTimers();
    const query = vi.fn().mockResolvedValue(response("FUNCTION_JOB_STATUS_RUNNING"));
    const onPollingTimeout = vi.fn();
    const poller = createFunctionJobPoller(query);

    await poller.start("function-job-1", { onPollingTimeout });
    await vi.advanceTimersByTimeAsync(30 * 60 * 1000);
    const callsAtTimeout = query.mock.calls.length;
    await vi.advanceTimersByTimeAsync(10_000);

    expect(onPollingTimeout).toHaveBeenCalledWith("function-job-1");
    expect(query).toHaveBeenCalledTimes(callsAtTimeout);
    poller.dispose();
  });

  it("replaces the active job and disposes timers", async () => {
    vi.useFakeTimers();
    const query = vi.fn().mockResolvedValue(response("FUNCTION_JOB_STATUS_RUNNING"));
    const poller = createFunctionJobPoller(query);

    await poller.start("function-job-1");
    await poller.start("function-job-2");
    poller.dispose();
    const callsAfterDispose = query.mock.calls.length;
    await vi.advanceTimersByTimeAsync(4000);

    expect(query.mock.calls[0][0]).toBe("function-job-1");
    expect(query.mock.calls[1][0]).toBe("function-job-2");
    expect(query).toHaveBeenCalledTimes(callsAfterDispose);
  });
});
