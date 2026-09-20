import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("collector task workbench", () => {
  it("keeps the ordinary create form in the documented task-first order", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    const form = source.slice(source.indexOf("<a-form"), source.indexOf("</a-form>"));
    const markers = [
      'field="task_name" label="任务名称"',
      'field="description" label="描述"',
      'field="data_type" label="数据类型"',
      'field="provider" label="数据源"',
      'field="market_type" label="市场类型"',
      "采集频率",
      'label="标的来源"',
      'field="enabled" label="启用状态"'
    ];
    const positions = markers.map(marker => form.indexOf(marker));
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
  });

  it("keeps result storage settings in a collapsed create-only advanced section", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain('header="高级设置"');
    expect(source).toContain("resultConfig.data_node_id");
    expect(source).toContain("resultConfig.keep_duration");
    expect(source).toContain("resultConfig.description");
    expect(source).toContain("result_config:");
    expect(source).toContain("delete_result_data:");
    expect(source).not.toContain("target_dataset_id");
    expect(source).not.toContain("buildCollectorRule");
    expect(source).not.toContain("CollectorRule");
  });

  it("renders the task list with operational result columns and no rule terminology", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    for (const label of ["任务名称", "数据类型", "数据源", "市场", "频率", "结果状态", "最近数据时间", "启用状态", "操作"]) {
      expect(source).toContain(`title="${label}"`);
    }
    expect(source).toContain("新建采集任务");
    expect(source).toContain('ok-text="保存任务"');
    expect(source).toContain("请创建新任务");
    expect(source).toContain("删除任务，保留结果数据");
    expect(source).toContain("删除任务并物理删除结果数据和数据视图");
    expect(source).not.toContain(["采集", "规则"].join(""));
    expect(source).not.toContain("采集参数");
    expect(source).not.toContain(["Dataset", " ID"].join(""));
    expect(source).not.toContain(["数据集", "管理"].join(""));
  });

  it("keeps resample backfill in the task surface", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    const backfill = fs.readFileSync(path.resolve(__dirname, "resample-backfill.vue"), "utf8");
    expect(source).toContain("kline_resample");
    expect(source).toContain("ResampleBackfillDialog");
    expect(source).toContain("sourceKeepDuration");
    expect(backfill).toContain("开始回填");
    expect(backfill).toContain("内部行情 `crypto`");
    expect(backfill).not.toContain("ruleId");
  });
});
