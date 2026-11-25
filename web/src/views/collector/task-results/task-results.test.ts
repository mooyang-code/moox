import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

import type { CollectorTask } from "@/api/collector";

import {
  buildResultsQuery,
  buildTaskResultTabs,
  getTaskResultState,
  resolveActiveTask,
  resampleEmptyDescription,
  resampleSourceTaskId,
  resampleTargetFrequency,
  selectTaskIdFromQuery,
  taskIdForDataset
} from "./task-results-model";

describe("collector result workflow", () => {
  const tasks: CollectorTask[] = [
    {
      task_id: "older",
      task_name: "旧任务",
      created_at: "1789776000000",
      result: { dataset_id: "dataset-older", status: "active" }
    },
    {
      task_id: "newer",
      task_name: "新任务",
      created_at: "1789862400000",
      result: { dataset_id: "dataset-newer", status: "active", last_data_time: "2026-09-20T12:00:00Z" }
    }
  ];

  it("sorts dynamic result tabs by creation time and keeps task identity as key", () => {
    const tabs = buildTaskResultTabs(tasks);
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(results).toContain('v-for="tab in resultTabs"');
    expect(results).toContain(':key="tab.taskId"');
    expect(results).toContain('type="rounded"');
    expect(results).not.toContain("<a-select");
    expect(tabs.map(tab => tab.taskId)).toEqual(["newer", "older"]);
    expect(tabs.map(tab => tab.title)).toEqual(["新任务", "旧任务"]);
  });

  it('创建时刻是毫秒数字字符串，没有创建时刻（"0" 或缺省）的排最后', () => {
    const mixed: CollectorTask[] = [
      { task_id: "older", created_at: "1789776000000" },
      { task_id: "newer", created_at: "1789862400000" },
      { task_id: "zero", created_at: "0" },
      { task_id: "none" }
    ];
    expect(buildTaskResultTabs(mixed).map(tab => tab.taskId)).toEqual(["newer", "older", "none", "zero"]);
  });

  it("keeps unknown list state stale until the selected task is inspected", () => {
    expect(getTaskResultState({ task_id: "unknown", result: { dataset_id: "dataset-unknown", status: "unknown" } })).toBe(
      "stale"
    );
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(results).toContain("TASK_PAGE_SIZE = 100");
    expect(results).toContain("GetTaskDetail({ space_id: spaceId, task_id: taskId })");
    expect(results).toContain("保留上次读取的结果状态");
    expect(results).not.toContain("保留上次已确认结果");
    expect(results).not.toContain("size: 1000");
  });

  it("preserves or repairs the selected result query", () => {
    expect(selectTaskIdFromQuery(tasks, "older")).toBe("older");
    expect(selectTaskIdFromQuery(tasks, "missing")).toBe("newer");
    expect(selectTaskIdFromQuery([], "missing")).toBe("");
    expect(buildResultsQuery("older")).toEqual({ tab: "results", resultTask: "older" });
    expect(buildResultsQuery("")).toEqual({ tab: "results" });
  });

  it("does not show another task when a deep-linked task is missing", () => {
    expect(resolveActiveTask(tasks, {}, "missing")).toBeUndefined();
    expect(resolveActiveTask(tasks, {}, "older")?.task_id).toBe("older");
    expect(resolveActiveTask(tasks, {}, "")?.task_id).toBe("newer");
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(results).toContain("resolveActiveTask(tasks.value, taskDetails.value, activeTaskId.value)");
    expect(results).not.toMatch(/resultTabs\.value\[0\]\?\.task\b/);
  });

  it("marks resample tasks and explains why a fresh resample result is empty", () => {
    const resample: CollectorTask = {
      task_id: "res-1",
      task_name: "45m 合成",
      data_type: "kline_resample",
      created_at: "1792627200000",
      collect_params: { source_task_id: "src-1", target_frequency: "45m" },
      prepare_state: "ready",
      result: { dataset_id: "dataset-res", status: "active" }
    };
    const [tab] = buildTaskResultTabs([resample]);
    expect(tab.title).toBe("45m 合成 · 合成");
    expect(tab.resample).toBe(true);
    expect(buildTaskResultTabs([{ task_id: "plain", task_name: "采集" }])[0].resample).toBe(false);
    expect(resampleSourceTaskId(resample)).toBe("src-1");
    expect(resampleTargetFrequency(resample)).toBe("45m");

    expect(resampleEmptyDescription({ ...resample, prepare_state: "waiting_view" }, 45, "UTC")).toBe(
      "正在准备结果数据集，请稍后刷新"
    );
    // 45m 网格上 10:20 之后的下一个边界是 10:30。
    expect(resampleEmptyDescription(resample, 45, "UTC", Date.parse("2026-10-22T10:20:00Z"))).toContain("预计 10:30 UTC");
    expect(resampleEmptyDescription(resample, 1440, "UTC", Date.parse("2026-10-22T10:20:00Z"))).toContain("预计 10-23 00:00 UTC");

    const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(page).toContain("合成自");
    expect(page).toContain("历史合成进行中");
  });
});

describe("按结果数据集选任务", () => {
  it("?dataset= 找到写入这个数据集的任务；同一数据集多个任务时按周期挑；找不到时为空", () => {
    const list = [
      {
        task_id: "a",
        data_type: "kline",
        collect_params: { frequency: "1m" },
        result: { dataset_id: "ds" },
        created_at: "1790812800000"
      },
      {
        task_id: "b",
        data_type: "kline_resample",
        collect_params: { target_frequency: "5m" },
        result: { dataset_id: "ds" },
        created_at: "1790899200000"
      }
    ];
    expect(taskIdForDataset(list, "ds", "1m")).toBe("a");
    expect(taskIdForDataset(list, "ds", "5m")).toBe("b");
    expect(taskIdForDataset(list, "ds", "")).toBe("b");
    expect(taskIdForDataset(list, "other", "1m")).toBe("");
    expect(taskIdForDataset(list, "", "1m")).toBe("");
  });
});
