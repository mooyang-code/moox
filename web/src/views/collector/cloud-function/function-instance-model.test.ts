import { describe, expect, it } from "vitest";
import { normalizeFunctionInstances, parseMetadata } from "./function-instance-model";

describe("cloud instance model", () => {
  it("normalizes canonical instance payloads", () => {
    const [instance] = normalizeFunctionInstances([{ instance_id: "n1", package_version: "v2" }]);
    expect(instance.package_version).toBe("v2");
  });

  it("parses metadata defensively", () => {
    expect(parseMetadata('{"tag":"海外"}')).toEqual({ tag: "海外" });
    expect(parseMetadata("broken")).toEqual({});
  });
});
