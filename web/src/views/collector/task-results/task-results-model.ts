import type { CollectorTask } from "@/api/collector";

export type TaskResultState = "ready" | "preparing" | "error";

export interface TaskResultTab {
  task: CollectorTask;
  taskId: string;
  title: string;
  viewIds: string[];
  state: TaskResultState;
}

export function sortTasksByCreatedAt(tasks: CollectorTask[]): CollectorTask[] {
  return [...tasks].sort((left, right) => {
    const timeDifference = timestamp(right.create_time) - timestamp(left.create_time);
    if (timeDifference !== 0) return timeDifference;
    return String(left.task_id).localeCompare(String(right.task_id));
  });
}

export function buildTaskResultTabs(tasks: CollectorTask[]): TaskResultTab[] {
  return sortTasksByCreatedAt(tasks).map(task => ({
    task,
    taskId: task.task_id,
    title: task.task_name?.trim() || task.task_id,
    viewIds: getTaskResultViewIds(task),
    state: getTaskResultState(task)
  }));
}

export function getTaskResultViewIds(task: CollectorTask): string[] {
  const ids = [...(task.result?.view_ids || []), task.result?.view_id || ""].map(value => value.trim()).filter(Boolean);
  return [...new Set(ids)];
}

export function getTaskResultState(task: CollectorTask): TaskResultState {
  const resultStatus = String(task.result?.status || "").toLowerCase();
  const prepareState = String(task.prepare_state || "").toLowerCase();
  if (["error", "failed", "invalid"].includes(resultStatus) || prepareState === "error" || Boolean(task.last_error)) {
    return "error";
  }
  if (getTaskResultViewIds(task).length > 0 && !["pending", "preparing", "waiting_view"].includes(resultStatus)) {
    return "ready";
  }
  return "preparing";
}

export function resultStateLabel(state: TaskResultState): string {
  if (state === "ready") return "结果可用";
  if (state === "error") return "结果异常";
  return "结果准备中";
}

export function taskStateLabel(task: CollectorTask): string {
  return task.enabled === false || task.enabled === "false" ? "任务已停用" : "任务已启用";
}

export function lastDataTime(task: CollectorTask): string {
  return task.result?.last_data_time?.trim() || "-";
}

export function resultCoverage(task: CollectorTask): { start: string; end: string } {
  return {
    start: task.result?.coverage_start?.trim() || "-",
    end: task.result?.coverage_end?.trim() || "-"
  };
}

export function selectTaskIdFromQuery(tasks: CollectorTask[], requestedTaskId: unknown): string {
  const requested = queryString(requestedTaskId);
  const sortedTasks = sortTasksByCreatedAt(tasks);
  return sortedTasks.some(task => task.task_id === requested) ? requested : sortedTasks[0]?.task_id || "";
}

export function buildResultsQuery(taskId: string): { tab: "results"; resultTask?: string } {
  const normalized = taskId.trim();
  return normalized ? { tab: "results", resultTask: normalized } : { tab: "results" };
}

function timestamp(value?: string) {
  if (!value) return 0;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
}

function queryString(value: unknown) {
  return Array.isArray(value) ? String(value[0] || "") : String(value || "");
}
