import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

import type { CollectorTask } from "@/api/collector";

import {
  buildResultsQuery,
  buildTaskResultTabs,
  getTaskResultState,
  resolveActiveTask,
  selectTaskIdFromQuery
} from "./task-results-model";

describe("collector result workflow", () => {
  const tasks: CollectorTask[] = [
    {
      task_id: "older",
      task_name: "旧任务",
      create_time: "2026-09-19T00:00:00Z",
      result: { view_id: "view-older", status: "active" }
    },
    {
      task_id: "newer",
      task_name: "新任务",
      create_time: "2026-09-20T00:00:00Z",
      result: { view_id: "view-newer", status: "active", last_data_time: "2026-09-20T12:00:00Z" }
    }
  ];

  it("renders every task result through the shared real-data browser", () => {
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const browse = fs.readFileSync(path.resolve(__dirname, "../../data/view-browse/index.vue"), "utf8");
    expect(results).toContain("<ViewBrowse");
    expect(results).toContain(":active-view-id=");
    expect(results).toContain(":view-ids=");
    expect(results).not.toContain("hide-technical-identity");
    expect(results).not.toContain("auto-refresh-interval-ms");
    expect(results).not.toContain("result-context");
    expect(browse).toContain('v-if="!props.hideTechnicalIdentity" class="view-status-line"');
    expect(browse).toContain("<KlineModal");
    expect(browse).toContain('@click="openKlineModal"');
    expect(browse).toContain('() => targetedViewIds.value.join("\\u0000")');
    expect(browse).toContain("void loadMeta();");
  });

  it("sorts dynamic result tabs by creation time and keeps task identity as key", () => {
    const tabs = buildTaskResultTabs(tasks);
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(results).toContain('v-for="tab in resultTabs"');
    expect(results).toContain(':key="tab.taskId"');
    expect(results).toContain(':title="tab.title"');
    expect(tabs.map(tab => tab.taskId)).toEqual(["newer", "older"]);
    expect(tabs.map(tab => tab.title)).toEqual(["新任务", "旧任务"]);
  });

  it("keeps unknown list state stale until the selected task is inspected", () => {
    expect(getTaskResultState({ task_id: "unknown", result: { view_id: "view-unknown", status: "unknown" } })).toBe("stale");
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(results).toContain("pageSize: 100");
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
});
