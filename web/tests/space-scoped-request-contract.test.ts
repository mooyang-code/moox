import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

function read(path: string) {
  return readFileSync(resolve(__dirname, `../src/views/${path}`), "utf8");
}

describe("space-scoped request ownership", () => {
  it("discards stale home requests after a space switch", () => {
    const source = read("home/home.vue");

    expect(source).toContain("const spaceLoadGate = new RequestGate()");
    expect(source).toContain("spaceLoadGate.isCurrent(token)");
    expect(source).toContain("selectedSpaceId.value === spaceId");
    const ruleCall = source.indexOf('"GetTaskList"');
    expect(source.slice(ruleCall, ruleCall + 180)).toContain("{ space_id: spaceId, page: { page: 1, size: 1 } }");
  });
});
