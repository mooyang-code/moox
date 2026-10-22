import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("collector task workbench", () => {
  it("keeps the ordinary create form in the documented task-first order", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    const form = source;
    const markers = [
      'field="task_name" label="任务名称"',
      'field="description" label="描述"',
      'field="data_type" label="执行任务类型"',
      "采集频率",
      "<h3>采集范围</h3>",
      "<h3>输出字段</h3>"
    ];
    const positions = markers.map(marker => form.indexOf(marker));
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
  });

  it("keeps result storage settings in a collapsed create-only advanced section", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain('header="高级设置"');
    expect(source).toContain("resultConfig.data_node_id");
    expect(source).not.toContain("keep_duration");
    expect(source).toContain("resultConfig.description");
    expect(source).toContain("result_config:");
    expect(source).toContain("delete_result_data:");
    expect(source).not.toContain("target_dataset_id");
    expect(source).not.toContain("buildCollectorRule");
    expect(source).not.toContain("CollectorRule");
  });

  it("renders the task list with operational result columns and no rule terminology", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    for (const label of [
      "任务名称",
      "数据类型",
      "数据范围标签",
      "来源 / 市场",
      "频率",
      "结果状态",
      "最近数据时间",
      "启用状态",
      "操作"
    ]) {
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

  it("lets the wide task table fill the spin wrapper so the fixed action column is not clipped", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8").replace(/\s+/g, "");
    expect(source).toContain(".moox-page:deep(.arco-spin){display:block;width:100%;min-width:0;}");
  });

  it("keeps resample creation and history jobs in the task surface", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    const drawer = fs.readFileSync(path.resolve(__dirname, "resample-jobs-drawer.vue"), "utf8");
    expect(source).toContain("kline_resample");
    expect(source).toContain("ResampleJobsDrawer");
    expect(source).toContain('title="合成来源"');
    // 行操作：新建合成、历史合成；旧的回填弹窗与收盘等待已删除。
    expect(source).toContain("新建合成");
    expect(source).toContain("历史合成");
    expect(source).toContain("onAddResample(record)");
    expect(source).not.toContain("ResampleBackfillDialog");
    expect(source).not.toContain("settleDelay");
    expect(source).not.toContain("收盘等待");
    expect(drawer).toContain("提交作业");
    expect(drawer).toContain("跳过样例");
    expect(drawer).toContain("取消");
    expect(drawer).not.toContain("ruleId");
  });

  it("derives the resample source from a selected source task instead of free-text fields", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain('label="源采集任务"');
    expect(source).toContain("previewKlineResampleSource");
    expect(source).toContain("继承源任务，共");
    // 源周期、序列标签不再是输入框，也不再从源数据集回填标签。
    expect(source).not.toContain("sourceFrequencyValue");
    expect(source).not.toContain("sourceSeriesTagValue");
    expect(source).not.toContain("loadSourceSubjectTags");
    expect(source).not.toContain("sourceTagsPending");
    // 合成任务的参数创建后不可修改：更新时原样回传。
    expect(source).toContain('if (input.dataType === "kline_resample") return { ...original };');
  });
  it("shows output fields in both task detail and edit surfaces", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain('<section class="editor-section editor-section--output">');
    expect(source).not.toContain('<section v-if="!editing" class="editor-section editor-section--output">');
    expect(source).toContain('label="输出字段" :span="2"');
    expect(source).toContain("detailOutputFieldLabels");
    expect(source).toContain("selectedOutputFields.value = input.outputFields || []");
    expect(source).toContain("outputFields: selectedOutputFields.value");
  });
});
