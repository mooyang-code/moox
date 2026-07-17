import { describe, expect, it } from "vitest";
import { BatchChangeStatus } from "@/utils/cloud-node-batch-change";
import {
  computeAggregateStatus,
  getStatusText,
  normalizeCloudNodes,
  normalizeSupportedWorkloads,
  parseMetadata
} from "./cloud-node-model";

describe("cloud node model", () => {
  it("normalizes legacy node payloads without sharing mutable workload arrays", () => {
    const workloads = ["kline"];
    const [node] = normalizeCloudNodes([{ node_id: "n1", package_version: "v2", supported_workloads: workloads }]);
    expect(node.version).toBe("v2");
    expect(node.status).toBe("offline");
    expect(node.supported_workloads).toEqual(["kline"]);
    expect(node.supported_workloads).not.toBe(workloads);
  });

  it("parses metadata and workload JSON defensively", () => {
    expect(parseMetadata('{"tag":"海外"}')).toEqual({ tag: "海外" });
    expect(parseMetadata("broken")).toEqual({});
    expect(normalizeSupportedWorkloads('["ticker"]')).toEqual(["ticker"]);
    expect(normalizeSupportedWorkloads("broken")).toEqual([]);
  });

  it("aggregates partial batch results", () => {
    const status = computeAggregateStatus([
      { batchIndex: 0, batch_change_status: BatchChangeStatus.SUCCESS, total_count: 2, success_count: 2, failed_count: 0 },
      { batchIndex: 1, batch_change_status: BatchChangeStatus.FAILED, total_count: 1, success_count: 0, failed_count: 1 }
    ]);
    expect(status.batch_change_status).toBe(BatchChangeStatus.PARTIAL);
    expect(status.total_count).toBe(3);
    expect(getStatusText(2)).toBe("在线");
  });
});
