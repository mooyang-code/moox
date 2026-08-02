import { describe, expect, it } from "vitest";
import { getStatusText, normalizeCloudNodes, parseMetadata } from "./cloud-node-model";

describe("cloud node model", () => {
  it("normalizes canonical node payloads", () => {
	const [node] = normalizeCloudNodes([{ node_id: "n1", package_version: "v2" }]);
    expect(node.package_version).toBe("v2");
    expect(node.status).toBe("NODE_STATUS_OFFLINE");
  });

  it("parses metadata defensively", () => {
    expect(parseMetadata('{"tag":"海外"}')).toEqual({ tag: "海外" });
    expect(parseMetadata("broken")).toEqual({});
  });

  it("formats canonical node statuses", () => {
    expect(getStatusText(2)).toBe("在线");
    expect(getStatusText("online")).toBe("online");
  });
});
