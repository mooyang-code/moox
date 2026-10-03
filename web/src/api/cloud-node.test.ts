import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { callControl, MockControlRequestError } = vi.hoisted(() => {
  class MockControlRequestError extends Error {}
  return { callControl: vi.fn(), MockControlRequestError };
});
vi.mock("@/api/admin/http", () => ({ callControl, ControlRequestError: MockControlRequestError }));

import { batchDeleteNodes, importSCFFunctions, previewSCFFunctions, submitCreateNodes, submitDeployNodes } from "./cloud-node";

describe("cloud node batch API", () => {
  beforeEach(() => {
    callControl.mockReset();
  });
  afterEach(() => vi.useRealTimers());

  function deleteSnapshot(node_id: string) {
    return { node_id, package_id: `pkg-${node_id}`, deployment_id: `dep-${node_id}`, modify_time: "2026-10-03T10:00:00Z" };
  }

  function deleteRequest(node_ids: string[]) {
    return { space_id: "crypto", node_ids, node_snapshots: node_ids.map(deleteSnapshot) };
  }

  function mockDeleteWorkflow(poll: (jobId: string) => unknown, submit?: (index: number) => unknown) {
    let submissions = 0;
    callControl.mockImplementation(async (_service, method, request) => {
      if (method === "AcquireCollectorPublishLease" || method === "RenewCollectorPublishLease") {
        return { lease_id: "lease-1", fencing_token: "9" };
      }
      if (method === "GetNodeList") return { items: [deleteSnapshot(request.node_id)], total: 1, page: { has_more: false } };
      if (method === "SubmitDeleteNodes") {
        submissions++;
        return submit ? submit(submissions) : { job_id: `delete-${submissions}` };
      }
      if (method === "GetNodeBatchChange") return poll(request.job_id);
      if (method === "ReleaseCollectorPublishLease") return { released: true };
      throw new Error(`Unexpected method: ${method}`);
    });
  }

  it("splits 170 deletes into bounded jobs under the same publish fence", async () => {
    mockDeleteWorkflow(jobId => ({
      job: { status: "NODE_BATCH_STATUS_SUCCESS", success_count: jobId === "delete-1" ? 100 : 70 }
    }));
    const nodeIds = Array.from({ length: 170 }, (_, index) => `node-${index}`);
    await expect(batchDeleteNodes(deleteRequest(nodeIds))).resolves.toEqual({ processed_count: 170 });
    const submissions = callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteNodes");
    expect(submissions.map(([, , request]) => request.node_ids)).toEqual([nodeIds.slice(0, 100), nodeIds.slice(100)]);
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
          ? { status: "NODE_BATCH_STATUS_FAILED", failed_count: 100 }
          : { status: laterTerminal ? "NODE_BATCH_STATUS_SUCCESS" : "NODE_BATCH_STATUS_RUNNING", success_count: 70 }
    }));
    const result = batchDeleteNodes(deleteRequest(Array.from({ length: 170 }, (_, i) => `node-${i}`)));
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
    mockDeleteWorkflow(() => ({ job: { status: "NODE_BATCH_STATUS_RUNNING" } }));
    const result = batchDeleteNodes(deleteRequest(["node-1"]));
    const resolved = expect(result).resolves.toEqual({ processed_count: 1 });
    await vi.advanceTimersByTimeAsync(31 * 60_000);
    expect(callControl.mock.calls.filter(([, method]) => method === "RenewCollectorPublishLease").length).toBeGreaterThan(30);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    const implementation = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "GetNodeBatchChange") return { job: { status: "NODE_BATCH_STATUS_SUCCESS", success_count: 1 } };
      return implementation(service, method, request);
    });
    await vi.advanceTimersByTimeAsync(250);
    await resolved;
  });

  it("verifies the selected node version under the lease before submitting deletion", async () => {
    mockDeleteWorkflow(() => ({ job: { status: "NODE_BATCH_STATUS_SUCCESS", success_count: 1 } }));
    const normal = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "GetNodeList") {
        return {
          items: [
            {
              ...deleteSnapshot(request.node_id),
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

    await expect(batchDeleteNodes(deleteRequest(["node-1"]))).rejects.toThrow("已发生变化，请刷新");
    const methods = callControl.mock.calls.map(([, method]) => method);
    expect(methods.indexOf("AcquireCollectorPublishLease")).toBeLessThan(methods.indexOf("GetNodeList"));
    expect(methods).not.toContain("SubmitDeleteNodes");
    expect(methods).toContain("ReleaseCollectorPublishLease");
  });

  it("bounds permanent poll failures, returns an unknown outcome, and retains the lease", async () => {
    vi.useFakeTimers();
    mockDeleteWorkflow(() => {
      throw new MockControlRequestError("permission denied");
    });
    const result = batchDeleteNodes(deleteRequest(["node-1"]));
    const rejected = expect(result).rejects.toThrow("删除批次状态无法确认");
    await vi.advanceTimersByTimeAsync(10_000);
    await rejected;
    expect(callControl.mock.calls.filter(([, method]) => method === "GetNodeBatchChange")).toHaveLength(3);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });

  it("recovers from a finite series of network poll losses", async () => {
    vi.useFakeTimers();
    let polls = 0;
    mockDeleteWorkflow(() => {
      polls++;
      if (polls < 3) throw new Error("network reset");
      return { job: { status: "NODE_BATCH_STATUS_SUCCESS", success_count: 1 } };
    });
    const result = batchDeleteNodes(deleteRequest(["node-1"]));
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
          ? { status: "NODE_BATCH_STATUS_SUCCESS", success_count: 100 }
          : { status: "NODE_BATCH_STATUS_RUNNING" }
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
    const result = batchDeleteNodes(deleteRequest(Array.from({ length: 170 }, (_, index) => `node-${index}`)));
    const completion = result.then(
      () => undefined,
      error => error
    );
    await vi.advanceTimersByTimeAsync(31_000);
    expect(callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteNodes")).toHaveLength(1);
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    finishSubmission?.();
    await vi.advanceTimersByTimeAsync(1);
    expect(callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteNodes")).toHaveLength(1);
    firstJobTerminal = true;
    await vi.advanceTimersByTimeAsync(1_000);
    const error = await completion;
    expect(error).toBeInstanceOf(Error);
    expect(error.message).toContain("续租失败");
    expect(callControl.mock.calls.filter(([, method]) => method === "SubmitDeleteNodes")).toHaveLength(1);
    expect(callControl.mock.calls.filter(([, method]) => method === "GetNodeBatchChange").length).toBeGreaterThan(1);
    expect(callControl.mock.calls.filter(([, method]) => method === "ReleaseCollectorPublishLease")).toHaveLength(1);
  });

  it("drains known accepted jobs after an ambiguous later submission without releasing", async () => {
    vi.useFakeTimers();
    let terminal = false;
    mockDeleteWorkflow(
      () => ({ job: { status: terminal ? "NODE_BATCH_STATUS_SUCCESS" : "NODE_BATCH_STATUS_RUNNING", success_count: 100 } }),
      index => {
        if (index === 2) throw new Error("submission outcome unknown");
        return { job_id: "delete-1" };
      }
    );
    const result = batchDeleteNodes(deleteRequest(Array.from({ length: 170 }, (_, i) => `node-${i}`)));
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
    mockDeleteWorkflow(() => ({ job: { status: "NODE_BATCH_STATUS_RUNNING" } }));
    const controller = new AbortController();
    const result = batchDeleteNodes(deleteRequest(["node-1"]), { signal: controller.signal });
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

  it.each(["SubmitDeleteNodes", "GetNodeBatchChange"])(
    "renews while %s is unresolved and aborts promptly",
    async blockedMethod => {
      vi.useFakeTimers();
      mockDeleteWorkflow(() => ({ job: { status: "NODE_BATCH_STATUS_SUCCESS", success_count: 1 } }));
      const normal = callControl.getMockImplementation()!;
      callControl.mockImplementation((service, method, request) => {
        if (method === blockedMethod)
          return new Promise(() => {
            /* Deliberately unresolved transport request. */
          });
        return normal(service, method, request);
      });
      const controller = new AbortController();
      const result = batchDeleteNodes(deleteRequest(["node-1"]), { signal: controller.signal });
      const rejected = expect(result).rejects.toThrow();
      await vi.advanceTimersByTimeAsync(31_000);
      expect(callControl.mock.calls.some(([, method]) => method === "RenewCollectorPublishLease")).toBe(true);
      controller.abort();
      await rejected;
      expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
    }
  );

  it("does not treat an HTTP error response as proof that submission was rejected", async () => {
    mockDeleteWorkflow(() => ({ job: { status: "NODE_BATCH_STATUS_SUCCESS", success_count: 1 } }));
    const implementation = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "SubmitDeleteNodes") return Promise.reject({ message: "upstream error", response: { status: 500 } });
      return implementation(service, method, request);
    });
    await expect(batchDeleteNodes(deleteRequest(["node-1"]))).rejects.toThrow("upstream error");
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });

  it("does not submit after a lost renewal response and releases only after the accepted job is terminal", async () => {
    vi.useFakeTimers();
    let terminal = false;
    let renewals = 0;
    mockDeleteWorkflow(() => ({
      job: { status: terminal ? "NODE_BATCH_STATUS_SUCCESS" : "NODE_BATCH_STATUS_RUNNING", success_count: 1 }
    }));
    const normal = callControl.getMockImplementation()!;
    callControl.mockImplementation((service, method, request) => {
      if (method === "RenewCollectorPublishLease" && ++renewals === 1) return Promise.reject(new Error("lost renewal"));
      return normal(service, method, request);
    });
    const result = batchDeleteNodes(deleteRequest(["node-1"]));
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

  it("submits create nodes and returns the backend job id", async () => {
    callControl.mockResolvedValue({
      job_id: "node-batch-create",
      operation: "NODE_BATCH_OPERATION_CREATE_NODES",
      total_count: 1
    });

    const result = await submitCreateNodes({
      nodes: [
        {
          cloud_account_id: "account-1",
          runtime: "Go1",
          region: "ap-guangzhou",
          package_id: "pkg-1",
          metadata: { function_name_prefix: "collector", index: 0 }
        }
      ],
      cloud_account_id: "account-1",
      region: "ap-guangzhou",
      namespace: "default",
      function_name_prefix: "collector",
      runtime: "Go1",
      count: 1
    });

    expect(result.job_id).toBe("node-batch-create");
    expect(callControl).toHaveBeenCalledWith("cloudnode", "SubmitCreateNodes", {
      nodes: expect.arrayContaining([expect.objectContaining({ package_id: "pkg-1" })])
    });
  });

  it("submits deployments and returns the backend job id", async () => {
    callControl.mockResolvedValue({
      job_id: "node-batch-deploy",
      operation: "NODE_BATCH_OPERATION_DEPLOY_NODES",
      total_count: 2
    });

    const result = await submitDeployNodes({ node_ids: ["node-1", "node-2"], package_id: "pkg-2" });

    expect(result.job_id).toBe("node-batch-deploy");
    expect(callControl).toHaveBeenCalledWith("cloudnode", "SubmitDeployNodes", {
      deployments: [
        { node_id: "node-1", package_id: "pkg-2" },
        { node_id: "node-2", package_id: "pkg-2" }
      ]
    });
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
      "cloudnode",
      "PreviewSCFFunctions",
      { account_id: "account-1" },
      { timeout: 960000 }
    );
    callControl.mockResolvedValueOnce({ created: 1, restored: 0, unchanged: 0, failed: 0, results: [] });
    await importSCFFunctions("account-1", [preview.functions[0].function]);
    expect(callControl).toHaveBeenLastCalledWith(
      "cloudnode",
      "ImportSCFFunctions",
      {
        account_id: "account-1",
        functions: [{ region: "ap-guangzhou", namespace: "default", function_name: "moox-fetcher" }]
      },
      { timeout: 960000 }
    );
  });

  it("holds the publish lease until the async delete batch is terminal", async () => {
    mockDeleteWorkflow(() => ({ job: { status: "NODE_BATCH_STATUS_SUCCESS", success_count: 1 } }));
    await expect(batchDeleteNodes(deleteRequest(["timer-1"]))).resolves.toEqual({ processed_count: 1 });

    expect(callControl).toHaveBeenNthCalledWith(1, "publishlease", "AcquireCollectorPublishLease", {
      space_id: "crypto",
      holder_id: expect.any(String)
    });
    const methods = callControl.mock.calls.map(([, method]) => method);
    expect(methods.indexOf("AcquireCollectorPublishLease")).toBeLessThan(methods.indexOf("GetNodeList"));
    expect(methods.indexOf("GetNodeList")).toBeLessThan(methods.indexOf("SubmitDeleteNodes"));
    expect(callControl).toHaveBeenCalledWith("cloudnode", "SubmitDeleteNodes", {
      node_ids: ["timer-1"],
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

    await expect(batchDeleteNodes(deleteRequest(["timer-1"]))).rejects.toThrow("network reset");
    expect(callControl.mock.calls.some(([, method]) => method === "ReleaseCollectorPublishLease")).toBe(false);
  });
});
