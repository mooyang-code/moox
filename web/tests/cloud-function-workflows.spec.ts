import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { getNodeBatchJobId, setNodeBatchJobId } from "@/utils/cloud-node-batch-change";

describe("cloud node workflows", () => {
  it("restores polling from job_id in the route", () => {
    expect(getNodeBatchJobId({ job_id: "node-batch-1" })).toBe("node-batch-1");
    expect(getNodeBatchJobId({ job_id: ["node-batch-2", "ignored"] })).toBe("node-batch-2");
    expect(setNodeBatchJobId({ tab: "nodes" }, "node-batch-3")).toEqual({
      tab: "nodes",
      job_id: "node-batch-3"
    });
    expect(setNodeBatchJobId({ tab: "nodes", job_id: "node-batch-3" })).toEqual({ tab: "nodes" });
  });

  it("disposes polling and submits one backend job without client chunks", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/collector/cloud-node/cloud-node.vue"), "utf8");

    expect(source).toContain("batchPoller.dispose()");
    expect(source).toContain("getNodeBatchJobId(route.query)");
    expect(source).not.toContain("chunkTasks");
    expect(source).not.toContain("makeCompletedBatchChangeStatus");
    expect(source).not.toContain("completeCloudNodeBatchChange");
  });

  it("stops a space-scoped batch poll when the selected space changes", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/collector/cloud-node/cloud-node.vue"), "utf8");
    const watcherStart = source.indexOf("watch(selectedSpaceId");
    const watcher = source.slice(watcherStart, source.indexOf("onBeforeUnmount(() =>", watcherStart));

    expect(watcher).toContain("batchPoller.stop()");
    expect(watcher).toContain("currentBatchChangeStatus.value = null");
    expect(watcher).toContain("await replaceRouteJobId()");
  });
});
