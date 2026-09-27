import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

describe("shared collector task instances", () => {
  it("renders one provider request with independent write targets", () => {
    const source = readFileSync(resolve(__dirname, "task-instances.vue"), "utf8");

    expect(source).toContain('title="Provider / 市场"');
    expect(source).toContain('title="写入目标"');
    expect(source).toContain("record.Targets.length");
    expect(source).toContain("写入目标明细");
    expect(source).toContain("target.TaskID");
    expect(source).toContain("target.DatasetID");
    expect(source).toContain("target.ViewID");
    expect(source).toContain("target.OutputFields.length");
    expect(source).not.toContain('title="任务 ID" data-index="TaskID"');
    expect(source).not.toContain('label="结果对象">{{ detailData.DatasetID');
  });

  it("keeps task and dataset as target-backed filters", () => {
    const source = readFileSync(resolve(__dirname, "task-instances.vue"), "utf8");
    expect(source).toContain("filter.task_id = form.value.taskId");
    expect(source).toContain("filter.dataset_id = form.value.datasetId");
    expect(source).toContain("raw.targets");
    expect(source).toContain("raw.Targets");
  });
});
