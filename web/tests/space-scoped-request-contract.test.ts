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
    const ruleCall = source.indexOf('"GetTaskRuleList"');
    expect(source.slice(ruleCall, ruleCall + 180)).toContain("{ space_id: spaceId, page: { page: 1, size: 1 } }");
  });

  it("discards stale dataset requests after a space switch", () => {
    const source = read("data/datasets/index.vue");

    expect(source).toContain("const datasetLoadGate = new RequestGate()");
    expect(source).toContain("const activationGate = new RequestGate()");
    expect(source).toContain("const rebindGate = new RequestGate()");
    expect(source).toContain("datasetLoadGate.isCurrent(token)");
    expect(source).toContain("activationGate.isCurrent(token)");
    expect(source).toContain("rebindGate.isCurrent(token)");
    expect(source).toContain("selectedSpaceId.value === spaceId");
    expect(source).toContain("item.space_id === dataset.space_id && item.dataset_id === dataset.dataset_id");
    const watcherStart = source.indexOf("watch(selectedSpaceId");
    const watcherEnd = source.indexOf("onMounted(", watcherStart);
    expect(source.slice(watcherStart, watcherEnd)).toContain("resetDatasetSpaceState()");
  });
});
