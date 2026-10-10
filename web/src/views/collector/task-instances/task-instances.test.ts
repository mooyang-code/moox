import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { taskInstancePaginationTotal } from "./task-instances-pagination";

describe("shared collector task instances", () => {
  it("uses the lookahead flag instead of an exact historical total", () => {
    expect(taskInstancePaginationTotal(1, 20, 20, { total: 0, total_state: 2, has_more: true })).toBe(21);
    expect(taskInstancePaginationTotal(2, 20, 7, { total: 0, total_state: "SKIPPED", has_more: false })).toBe(27);
    expect(taskInstancePaginationTotal(1, 20, 20, { total: 53, total_state: 1, has_more: true })).toBe(53);
  });

  it("registers each pagination action once", () => {
    const source = readFileSync(resolve(__dirname, "task-instances.vue"), "utf8");
    expect(source).toContain('@page-change="onPageChange"');
    expect(source).toContain('@page-size-change="onPageSizeChange"');
    expect(source).not.toContain("onChange:");
    expect(source).not.toContain("onPageSizeChange:");
  });

  it("matches the main table scroll width to its fixed columns", () => {
    const source = readFileSync(resolve(__dirname, "task-instances.vue"), "utf8");
    const document = new DOMParser().parseFromString(source, "text/html");
    const template = document.querySelector("template") as HTMLTemplateElement;
    const table = template.content.querySelector("a-table")!;
    const columns = table.querySelector("template") as HTMLTemplateElement;
    const widths = Array.from(columns.content.querySelectorAll("a-table-column"), column =>
      Number(column.getAttribute(":width"))
    );

    expect(widths).toHaveLength(11);
    expect(widths.every(width => Number.isFinite(width) && width > 0)).toBe(true);
    const totalWidth = widths.reduce((total, width) => total + width, 0);
    expect(totalWidth).toBe(1600);
    expect(table.getAttribute(":scroll")).toBe(`{ x: ${totalWidth} }`);
  });

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
