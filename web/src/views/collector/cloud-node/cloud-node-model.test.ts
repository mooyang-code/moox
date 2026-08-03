import { describe, expect, it } from "vitest";
import { normalizeCloudNodes, parseMetadata } from "./cloud-node-model";

describe("cloud node model", () => {
  it("normalizes canonical node payloads", () => {
	const [node] = normalizeCloudNodes([{ node_id: "n1", package_version: "v2" }]);
    expect(node.package_version).toBe("v2");
  });

  it("parses metadata defensively", () => {
    expect(parseMetadata('{"tag":"海外"}')).toEqual({ tag: "海外" });
    expect(parseMetadata("broken")).toEqual({});
  });
});
