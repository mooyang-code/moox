import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { callControl, MockControlRequestError } = vi.hoisted(() => {
  class MockControlRequestError extends Error {}
  return { callControl: vi.fn(), MockControlRequestError };
});
vi.mock("@/api/admin/http", () => ({ callControl, ControlRequestError: MockControlRequestError }));

import { deleteFunctionInstances, getFunctionJob, importSCFFunctions, previewSCFFunctions } from "./function-instance";

describe("cloud instance batch API", () => {
  beforeEach(() => {
    callControl.mockReset();
  });
  afterEach(() => vi.useRealTimers());

  function deleteSnapshot(instance_id: string) {
    return {
      instance_id,
      package_id: `pkg-${instance_id}`,
      deployment_id: `dep-${instance_id}`,
      modify_time: "2026-10-03T10:00:00Z"
    };
  }

  function deleteRequest(instance_ids: string[]) {
    return { space_id: "crypto", instance_ids, instance_snapshots: instance_ids.map(deleteSnapshot) };
  }

  function mockDeleteWorkflow(poll: (jobId: string) => unknown, submit?: (index: number) => unknown) {
    let submissions = 0;
    callControl.mockImplementation(async (_service, method, request) => {
      if (method === "AcquireCollectorPublishLease" || method === "RenewCollectorPublishLease") {
        return { lease_id: "lease-1", fencing_token: "9" };
      }
      if (method === "ListFunctionInstances")
        return { items: [deleteSnapshot(request.instance_id)], total: 1, page: { has_more: false } };
      if (method === "SubmitDeleteFunctionInstances") {
        submissions++;
        return submit ? submit(submissions) : { job_id: `delete-${submissions}` };
      }
      if (method === "GetFunctionJob") return poll(request.job_id);
      if (method === "ReleaseCollectorPublishLease") return { released: true };
      throw new Error(`Unexpected method: ${method}`);
    });
  }

  it("splits 170 deletes into bounded jobs under the same publish fence", async () => {
    mockDeleteWorkflow(jobId => ({
      job: { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: jobId === "delete-1" ? 100 : 70 }
    }));
    const instanceIds = Array.from({ length: 170 }, (_, index) => `instance-${index}`);
    await expect(deleteFunctionInstances(deleteRequest(instanceIds))).resolves.toEqual({ processed_count: 170 });
    const submissions = callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteFunctionInstances");
    expect(submissions.map(([, , request]) => request.instance_ids)).toEqual([instanceIds.slice(0, 100), instanceIds.slice(100)]);
    for (const [, , request] of submissions) {
      expect(request).toMatchObject({ collector_publish_lease_id: "lease-1", collector_publish_fencing_token: "9" });
    }
    expect(callControl.mock.calls.filter(([, method]) => method === "AcquireCollectorPublishLease")).toHaveLength(1);
  });

  it("renews and waits for later jobs after the first job fails", async () => {
    vi.useFakeTimers();
    let laterTerminal = false;
    mockDeleteWorkflow(jobId => ({
      job:
        jobId === "delete-1"
          ? { status: "FUNCTION_JOB_STATUS_FAILED", failed_count: 100 }
          : { status: laterTerminal ? "FUNCTION_JOB_STATUS_SUCCESS" : "FUNCTION_JOB_STATUS_RUNNING", success_count: 70 }
    }));
    const result = deleteFunctionInstances(deleteRequest(Array.from({ length: 170 }, (_, i) => `instance-${i}`)));
    const rejected = expect(result).rejects.toThrow("100 个节点未完成");
    await vi.advanceTimersByTimeAsync(31_000);
    expect(callControl.mock.calls.some(([, method]) => method === "RenewCollectorPublishLease")).toBe(true);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    laterTerminal = true;
    await vi.advanceTimersByTimeAsync(250);
    await rejected;
    expect(callControl).toHaveBeenLastCalledWith("publishlease", "ReleaseCollectorPublishLease", {
      space_id: "crypto",
      lease_id: "lease-1",
      fencing_token: "9"
    });
  });

  it("retries unknown poll outcomes and renews beyond 30 minutes", async () => {
    vi.useFakeTimers();
    mockDeleteWorkflow(() => ({ job: { status: "FUNCTION_JOB_STATUS_RUNNING" } }));
    const result = deleteFunctionInstances(deleteRequest(["instance-1"]));
    const resolved = expect(result).resolves.toEqual({ processed_count: 1 });
    await vi.advanceTimersByTimeAsync(31 * 60_000);
    expect(callControl.mock.calls.filter(([, method]) => method === "RenewCollectorPublishLease").length).toBeGreaterThan(30);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    const implementation = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "GetFunctionJob") return { job: { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: 1 } };
      return implementation(service, method, request);
    });
    await vi.advanceTimersByTimeAsync(250);
    await resolved;
  });

  it("verifies the selected instance version under the lease before submitting deletion", async () => {
    mockDeleteWorkflow(() => ({ job: { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: 1 } }));
    const normal = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "ListFunctionInstances") {
        return {
          items: [
            {
              ...deleteSnapshot(request.instance_id),
              package_id: "new-package",
              deployment_id: "new-deployment",
              modify_time: "2026-10-03T11:00:00Z"
            }
          ],
          page: { has_more: false }
        };
      }
      return normal(service, method, request);
    });

    await expect(deleteFunctionInstances(deleteRequest(["instance-1"]))).rejects.toThrow("已发生变化，请刷新");
    const methods = callControl.mock.calls.map(([, method]) => method);
    expect(methods.indexOf("AcquireCollectorPublishLease")).toBeLessThan(methods.indexOf("ListFunctionInstances"));
    expect(methods).not.toContain("SubmitDeleteFunctionInstances");
    expect(methods).toContain("ReleaseCollectorPublishLease");
  });

  it("bounds permanent poll failures, returns an unknown outcome, and retains the lease", async () => {
    vi.useFakeTimers();
    mockDeleteWorkflow(() => {
      throw new MockControlRequestError("permission denied");
    });
    const result = deleteFunctionInstances(deleteRequest(["instance-1"]));
    const rejected = expect(result).rejects.toThrow("删除批次状态无法确认");
    await vi.advanceTimersByTimeAsync(10_000);
    await rejected;
    expect(callControl.mock.calls.filter(([, method]) => method === "GetFunctionJob")).toHaveLength(3);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });

  it("recovers from a finite series of network poll losses", async () => {
    vi.useFakeTimers();
    let polls = 0;
    mockDeleteWorkflow(() => {
      polls++;
      if (polls < 3) throw new Error("network reset");
      return { job: { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: 1 } };
    });
    const result = deleteFunctionInstances(deleteRequest(["instance-1"]));
    const resolved = expect(result).resolves.toEqual({ processed_count: 1 });
    await vi.advanceTimersByTimeAsync(5_000);
    await resolved;
    expect(polls).toBe(3);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(true);
  });

  it("stops later submissions after any lease renewal error but drains accepted jobs", async () => {
    vi.useFakeTimers();
    let firstJobTerminal = false;
    let finishSubmission: (() => void) | undefined;
    mockDeleteWorkflow(
      () => ({
        job: firstJobTerminal
          ? { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: 100 }
          : { status: "FUNCTION_JOB_STATUS_RUNNING" }
      }),
      index =>
        index === 1
          ? new Promise(resolve => {
              finishSubmission = () => resolve({ job_id: "delete-1" });
            })
          : { job_id: `delete-${index}` }
    );
    const normal = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "RenewCollectorPublishLease") return Promise.reject(new MockControlRequestError("lease conflict"));
      return normal(service, method, request);
    });
    const result = deleteFunctionInstances(deleteRequest(Array.from({ length: 170 }, (_, index) => `instance-${index}`)));
    const completion = result.then(
      () => undefined,
      error => error
    );
    await vi.advanceTimersByTimeAsync(31_000);
    expect(callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteFunctionInstances")).toHaveLength(1);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    finishSubmission?.();
    await vi.advanceTimersByTimeAsync(1);
    expect(callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteFunctionInstances")).toHaveLength(1);
    firstJobTerminal = true;
    await vi.advanceTimersByTimeAsync(1_000);
    const error = await completion;
    expect(error).toBeInstanceOf(Error);
    expect(error.message).toContain("续租失败");
    expect(callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteFunctionInstances")).toHaveLength(1);
    expect(callControl.mock.calls.filter(([, method]) => method === "GetFunctionJob").length).toBeGreaterThan(1);
    expect(callControl.mock.calls.filter(([, method]) => method === "ReleaseCollectorPublishLease")).toHaveLength(1);
  });

  it("drains known accepted jobs after an ambiguous later submission without releasing", async () => {
    vi.useFakeTimers();
    let terminal = false;
    mockDeleteWorkflow(
      () => ({ job: { status: terminal ? "FUNCTION_JOB_STATUS_SUCCESS" : "FUNCTION_JOB_STATUS_RUNNING", success_count: 100 } }),
      index => {
        if (index === 2) throw new Error("submission outcome unknown");
        return { job_id: "delete-1" };
      }
    );
    const result = deleteFunctionInstances(deleteRequest(Array.from({ length: 170 }, (_, i) => `instance-${i}`)));
    const rejected = expect(result).rejects.toThrow("submission outcome unknown");
    await vi.advanceTimersByTimeAsync(31_000);
    expect(callControl.mock.calls.some(([, method]) => method === "RenewCollectorPublishLease")).toBe(true);
    terminal = true;
    await vi.advanceTimersByTimeAsync(250);
    await rejected;
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });

  it("allows explicit abort without releasing a running job", async () => {
    vi.useFakeTimers();
    mockDeleteWorkflow(() => ({ job: { status: "FUNCTION_JOB_STATUS_RUNNING" } }));
    const controller = new AbortController();
    const result = deleteFunctionInstances(deleteRequest(["instance-1"]), { signal: controller.signal });
    const rejected = expect(result).rejects.toThrow();
    await vi.advanceTimersByTimeAsync(31_000);
    controller.abort();
    await vi.advanceTimersByTimeAsync(250);
    await rejected;
    const renewals = callControl.mock.calls.filter(([, method]) => method === "RenewCollectorPublishLease").length;
    await vi.advanceTimersByTimeAsync(31_000);
    expect(callControl.mock.calls.filter(([, method]) => method === "RenewCollectorPublishLease")).toHaveLength(renewals);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });

  it.each(["SubmitDeleteFunctionInstances", "GetFunctionJob"])(
    "renews while %s is unresolved and aborts promptly",
    async blockedMethod => {
      vi.useFakeTimers();
      mockDeleteWorkflow(() => ({ job: { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: 1 } }));
      const normal = callControl.getMockImplementation()!;
      callControl.mockImplementation((service, method, request) => {
        if (method === blockedMethod)
          return new Promise(() => {
            /* Deliberately unresolved transport request. */
          });
        return normal(service, method, request);
      });
      const controller = new AbortController();
      const result = deleteFunctionInstances(deleteRequest(["instance-1"]), { signal: controller.signal });
      const rejected = expect(result).rejects.toThrow();
      await vi.advanceTimersByTimeAsync(31_000);
      expect(callControl.mock.calls.some(([, method]) => method === "RenewCollectorPublishLease")).toBe(true);
      controller.abort();
      await rejected;
      expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    }
  );

  it("does not treat an HTTP error response as proof that submission was rejected", async () => {
    mockDeleteWorkflow(() => ({ job: { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: 1 } }));
    const implementation = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "SubmitDeleteFunctionInstances")
        return Promise.reject({ message: "upstream error", response: { status: 500 } });
      return implementation(service, method, request);
    });
    await expect(deleteFunctionInstances(deleteRequest(["instance-1"]))).rejects.toThrow("upstream error");
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });

  it("does not submit after a lost renewal response and releases only after the accepted job is terminal", async () => {
    vi.useFakeTimers();
    let terminal = false;
    let renewals = 0;
    mockDeleteWorkflow(() => ({
      job: { status: terminal ? "FUNCTION_JOB_STATUS_SUCCESS" : "FUNCTION_JOB_STATUS_RUNNING", success_count: 1 }
    }));
    const normal = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "RenewCollectorPublishLease" && ++renewals === 1) return Promise.reject(new Error("lost renewal"));
      return normal(service, method, request);
    });
    const result = deleteFunctionInstances(deleteRequest(["instance-1"]));
    const completion = result.then(
      () => undefined,
      error => error
    );
    await vi.advanceTimersByTimeAsync(32_000);
    expect(renewals).toBe(1);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    terminal = true;
    await vi.advanceTimersByTimeAsync(250);
    const error = await completion;
    expect(error).toBeInstanceOf(Error);
    expect(error.message).toContain("续租失败");
    expect(callControl.mock.calls.filter(([, method]) => method === "ReleaseCollectorPublishLease")).toHaveLength(1);
  });

  it("converts numeric batch enums returned by the backend to enum names", async () => {
    callControl.mockResolvedValue({
      job: { job_id: "job-1", status: 3 },
      items: [
        { item_id: "i1", status: 3 },
        { item_id: "i2", status: "4" },
        { item_id: "i3", status: "FUNCTION_JOB_ITEM_STATUS_RUNNING" }
      ]
    });

    const result = await getFunctionJob("job-1");

    expect(result.job.status).toBe("FUNCTION_JOB_STATUS_SUCCESS");
    expect(result.items.map(item => item.status)).toEqual([
      "FUNCTION_JOB_ITEM_STATUS_SUCCESS",
      "FUNCTION_JOB_ITEM_STATUS_FAILED",
      "FUNCTION_JOB_ITEM_STATUS_RUNNING"
    ]);
  });

  it("previews and imports only selected SCF references", async () => {
    callControl.mockResolvedValueOnce({
      functions: [
        { function: { region: "ap-guangzhou", namespace: "default", function_name: "moox-fetcher" }, importable: true }
      ],
      region_errors: []
    });
    const preview = await previewSCFFunctions("account-1");
    expect(preview.functions).toHaveLength(1);
    expect(callControl).toHaveBeenNthCalledWith(
      1,
      "cloudfunction",
      "PreviewSCFFunctions",
      { account_id: "account-1" },
      { timeout: 960000 }
    );
    callControl.mockResolvedValueOnce({ created: 1, restored: 0, unchanged: 0, failed: 0, results: [] });
    await importSCFFunctions("account-1", [preview.functions[0].function]);
    expect(callControl).toHaveBeenLastCalledWith(
      "cloudfunction",
      "ImportSCFFunctions",
      {
        account_id: "account-1",
        functions: [{ region: "ap-guangzhou", namespace: "default", function_name: "moox-fetcher" }]
      },
      { timeout: 960000 }
    );
  });

  it("holds the publish lease until the async delete batch is terminal", async () => {
    mockDeleteWorkflow(() => ({ job: { status: "FUNCTION_JOB_STATUS_SUCCESS", success_count: 1 } }));
    await expect(deleteFunctionInstances(deleteRequest(["timer-1"]))).resolves.toEqual({ processed_count: 1 });

    expect(callControl).toHaveBeenNthCalledWith(1, "publishlease", "AcquireCollectorPublishLease", {
      space_id: "crypto",
      holder_id: expect.any(String)
    });
    const methods = callControl.mock.calls.map(([, method]) => method);
    expect(methods.indexOf("AcquireCollectorPublishLease")).toBeLessThan(methods.indexOf("ListFunctionInstances"));
    expect(methods.indexOf("ListFunctionInstances")).toBeLessThan(methods.indexOf("SubmitDeleteFunctionInstances"));
    expect(callControl).toHaveBeenCalledWith("cloudfunction", "SubmitDeleteFunctionInstances", {
      instance_ids: ["timer-1"],
      collector_publish_lease_id: "lease-1",
      collector_publish_fencing_token: "9"
    });
    expect(callControl).toHaveBeenLastCalledWith("publishlease", "ReleaseCollectorPublishLease", {
      space_id: "crypto",
      lease_id: "lease-1",
      fencing_token: "9"
    });
  });

  it("does not release a lease when delete submission has an ambiguous network outcome", async () => {
    callControl
      .mockResolvedValueOnce({ lease_id: "lease-1", fencing_token: "9" })
      .mockResolvedValueOnce({ items: [deleteSnapshot("timer-1")] })
      .mockRejectedValueOnce({ message: "network reset" });

    await expect(deleteFunctionInstances(deleteRequest(["timer-1"]))).rejects.toThrow("network reset");
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });
});
