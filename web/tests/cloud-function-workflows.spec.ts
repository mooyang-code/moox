import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

describe("cloud instance workflows", () => {
  it("submits one backend job without client chunks", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/collector/cloud-function/function-instance.vue"), "utf8");

    expect(source).not.toContain("chunkTasks");
    expect(source).not.toContain("makeCompletedBatchChangeStatus");
    expect(source).not.toContain("completeCloudFunctionJobChange");
  });

  it("passes the workload type to the package manager under its current prop name", () => {
    const parent = readFileSync(resolve(__dirname, "../src/views/collector/cloud-function/function-instance.vue"), "utf8");
    const child = readFileSync(resolve(__dirname, "../src/views/collector/cloud-function/function-package-manage.vue"), "utf8");

    expect(child).toContain("workloadType?: string");
    expect(parent).toContain(':workload-type="WORKLOAD_TYPE"');
    expect(parent).not.toContain(":biz-type");
  });
});
