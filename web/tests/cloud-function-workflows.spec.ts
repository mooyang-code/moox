import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { getFunctionJobId, setFunctionJobId } from "@/utils/function-job-change";

describe("cloud instance workflows", () => {
  it("restores polling from job_id in the route", () => {
    expect(getFunctionJobId({ job_id: "function-job-1" })).toBe("function-job-1");
    expect(getFunctionJobId({ job_id: ["function-job-2", "ignored"] })).toBe("function-job-2");
    expect(setFunctionJobId({ tab: "instances" }, "function-job-3")).toEqual({
      tab: "instances",
      job_id: "function-job-3"
    });
    expect(setFunctionJobId({ tab: "instances", job_id: "function-job-3" })).toEqual({ tab: "instances" });
  });

  it("disposes polling and submits one backend job without client chunks", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/collector/cloud-function/function-instance.vue"), "utf8");

    expect(source).toContain("batchPoller.dispose()");
    expect(source).toContain("getFunctionJobId(route.query)");
    expect(source).not.toContain("chunkTasks");
    expect(source).not.toContain("makeCompletedBatchChangeStatus");
    expect(source).not.toContain("completeCloudFunctionJobChange");
  });

  it("passes the workload type to the package manager under its current prop name", () => {
    const parent = readFileSync(resolve(__dirname, "../src/views/collector/cloud-function/function-instance.vue"), "utf8");
    const child = readFileSync(resolve(__dirname, "../src/views/collector/cloud-function/function-package-manage.vue"), "utf8");

    expect(child).toContain("workloadType?: string");
    expect(parent).toContain(':workload-type="currentWorkloadType"');
    expect(parent).not.toContain(":biz-type");
  });

  it("stops a space-scoped batch poll when the selected space changes", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/collector/cloud-function/function-instance.vue"), "utf8");
    const watcherStart = source.indexOf("watch(selectedSpaceId");
    const watcher = source.slice(watcherStart, source.indexOf("onBeforeUnmount(() =>", watcherStart));

    expect(watcher).toContain("batchPoller.stop()");
    expect(watcher).toContain("currentBatchChangeStatus.value = null");
    expect(watcher).toContain("await replaceRouteJobId()");
  });
});
