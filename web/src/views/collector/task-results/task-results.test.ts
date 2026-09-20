import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { CollectorTask } from "@/api/collector";
import {
  buildResultsQuery,
  buildTaskResultTabs,
  getTaskResultState,
  resultCoverage,
  selectTaskIdFromQuery,
  taskStateLabel
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
    expect(results).toContain(':hide-technical-identity="true"');
    expect(browse).toContain('v-if="!props.hideTechnicalIdentity" class="view-status-line"');
    expect(results).toContain(':auto-refresh-interval-ms="30000"');
    expect(browse).toContain("<KlineModal");
    expect(browse).toContain('@click="openKlineModal"');
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

  it("shows preparation and invalid states without trying to browse a missing view", () => {
    expect(getTaskResultState({ task_id: "pending", result: { status: "pending" } })).toBe("preparing");
    expect(getTaskResultState({ task_id: "broken", result: { status: "error" }, last_error: "view failed" })).toBe("error");
    expect(resultCoverage({ task_id: "pending", result: { status: "pending" } })).toEqual({ start: "-", end: "-" });
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(results).toContain('description="结果准备中，请稍后刷新"');
    expect(results).toContain('description="暂无采集任务"');
    expect(results).toContain("新建采集任务");
    expect(results).toContain('empty-rows-description="任务已准备，尚未产生数据"');
    expect(taskStateLabel({ task_id: "disabled", enabled: false })).toBe("任务已停用");
  });

  it("preserves or repairs the selected result query", () => {
    expect(selectTaskIdFromQuery(tasks, "older")).toBe("older");
    expect(selectTaskIdFromQuery(tasks, "missing")).toBe("newer");
    expect(selectTaskIdFromQuery([], "missing")).toBe("");
    expect(buildResultsQuery("older")).toEqual({ tab: "results", resultTask: "older" });
    expect(buildResultsQuery("")).toEqual({ tab: "results" });
  });
});
