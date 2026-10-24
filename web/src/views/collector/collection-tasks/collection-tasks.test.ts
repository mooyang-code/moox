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

  it("renders the task list focused on configuration and keeps run info in the detail drawer", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    for (const label of ["任务名称", "数据类型", "数据范围标签", "来源 / 市场", "频率", "启用状态", "操作"]) {
      expect(source).toContain(`title="${label}"`);
    }
    // 运行状态类信息离开列表列，收敛进详情抽屉的「运行信息」分组
    expect(source).not.toContain('title="结果状态"');
    expect(source).not.toContain('title="最近数据时间"');
    expect(source).toContain('label="结果状态"');
    expect(source).toContain('label="最近数据时间"');
    expect(source).toContain("运行信息");
    expect(source).toContain("新建采集任务");
    expect(source).toContain("保存任务");
    expect(source).toContain("删除任务，保留结果数据");
    expect(source).toContain("删除任务并物理删除结果数据和数据视图");
    expect(source).not.toContain(["采集", "规则"].join(""));
    expect(source).not.toContain("采集参数");
  });

  it("searches by task name or id with enter-to-query and keeps filters in the URL", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain('v-model="filters.keyword"');
    expect(source).toContain('placeholder="搜索任务名称 / 任务 ID"');
    expect(source).toContain('@press-enter="search"');
    expect(source).toContain('@clear="search"');
    expect(source).toContain("syncListQuery");
    expect(source).toContain("route.query.keyword");
    // 后端模糊匹配走 keyword，列表不再按内部 task_id 精确筛选
    expect(source).not.toContain("filters.taskId");
    expect(source).not.toContain("{ task_id: filters");
  });

  it("keeps enable toggles behind a confirmation with row-level loading", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain("confirmEnableChange");
    expect(source).toContain("Modal.confirm");
    expect(source).toContain("不再采集新周期数据");
    expect(source).toContain("togglingTaskId");
    expect(source).not.toContain('@click="handleEnableChange(record, !record.enabled)"');
  });

  it("keeps only primary row actions inline and folds the rest into the more menu", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain("<a-dropdown");
    expect(source).toContain('confirmEnableChange(record)');
    expect(source).toContain('openBackfill(record)');
    expect(source).toContain('openDelete(record)');
    expect(source).toContain("action-danger");
    // 平铺的操作按钮只剩「修改」
    expect(source).not.toContain('status="record.enabled ? \'warning\' : \'success\'');
  });

  it("opens the create and edit forms and the detail view as drawers with readonly summaries", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    for (const drawer of [
      'v-model:visible="editorVisible"',
      'v-model:visible="outputFieldsVisible"',
      'v-model:visible="detailVisible"'
    ]) {
      expect(source).toContain(drawer);
    }
    // 删除确认弹窗（radio 二选一）保留为弹窗，其余表面全部抽屉化
    expect(source).toContain('v-model:visible="deleteVisible"');
    expect(source).not.toContain('<a-modal v-model:visible="editorVisible"');
    expect(source).not.toContain('<a-modal v-model:visible="outputFieldsVisible"');
    expect(source).not.toContain('<a-modal v-model:visible="detailVisible"');
    expect(source).toContain("saveEditor");
    expect(source).toContain("readonly-summary");
    expect(source).toContain("editorSubjectTags");
  });

  it("lets the wide task table fill the spin wrapper so the fixed action column is not clipped", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8").replace(/\s+/g, "");
    expect(source).toContain(".moox-page:deep(.arco-spin){display:block;width:100%;min-width:0;}");
  });

  it("keeps resample backfill in the task surface", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    const backfill = fs.readFileSync(path.resolve(__dirname, "resample-backfill.vue"), "utf8");
    expect(source).toContain("kline_resample");
    expect(source).toContain("ResampleBackfillDialog");
    expect(source).toContain("sourceRetention");
    expect(backfill).toContain("开始回填");
    expect(backfill).toContain("内部行情 `crypto`");
    expect(backfill).not.toContain("ruleId");
  });

  it("shows output fields in both task detail and edit surfaces", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    expect(source).toContain('<section class="editor-section editor-section--output">');
    expect(source).not.toContain('<section v-if="!editing" class="editor-section editor-section--output">');
    expect(source).toContain("detailOutputFieldLabels");
    expect(source).toContain("selectedOutputFields.value = input.outputFields || []");
    expect(source).toContain("outputFields: selectedOutputFields.value");
  });

  it("clears source-derived tags on switch and waits for the backfill before saving", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collection-tasks.vue"), "utf8");
    const loader = source.slice(
      source.indexOf("function loadSourceSubjectTags"),
      source.indexOf("async function fetchSourceSubjectTags")
    );
    expect(loader).toContain("subjectTagIds.value = []");
    expect(loader).toContain("sourceTagsPending = task");
    const submit = source.slice(source.indexOf("async function saveEditor"));
    expect(submit.indexOf("await sourceTagsPending")).toBeGreaterThan(-1);
    expect(submit.indexOf("await sourceTagsPending")).toBeLessThan(submit.indexOf("currentTaskInput()"));
  });
});
