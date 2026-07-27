import { describe, expect, it } from "vitest";
import { getStatusText, normalizeCloudNodes, normalizeSupportedWorkloads, parseMetadata } from "./cloud-node-model";

describe("cloud node model", () => {
  it("normalizes canonical node payloads without sharing mutable workload arrays", () => {
    const workloads = ["kline"];
    const [node] = normalizeCloudNodes([{ node_id: "n1", package_version: "v2", supported_workloads: workloads }]);
    expect(node.package_version).toBe("v2");
    expect(node.status).toBe("NODE_STATUS_OFFLINE");
    expect(node.supported_workloads).toEqual(["kline"]);
    expect(node.supported_workloads).not.toBe(workloads);
  });

  it("parses metadata and workload JSON defensively", () => {
    expect(parseMetadata('{"tag":"海外"}')).toEqual({ tag: "海外" });
    expect(parseMetadata("broken")).toEqual({});
    expect(normalizeSupportedWorkloads('["ticker"]')).toEqual(["ticker"]);
    expect(normalizeSupportedWorkloads("broken")).toEqual([]);
  });

  it("formats canonical node statuses", () => {
    expect(getStatusText(2)).toBe("在线");
    expect(getStatusText("online")).toBe("online");
  });
});
