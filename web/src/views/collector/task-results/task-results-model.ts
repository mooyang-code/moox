import type { CollectorTask } from "@/api/collector";

export type TaskResultState = "ready" | "preparing" | "stale" | "error";

export interface TaskResultTab {
  task: CollectorTask;
  taskId: string;
  title: string;
  resample: boolean;
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
    title: (task.task_name?.trim() || task.task_id) + (isResampleTask(task) ? " · 合成" : ""),
    resample: isResampleTask(task),
    viewIds: getTaskResultViewIds(task),
    state: getTaskResultState(task)
  }));
}

export function isResampleTask(task: CollectorTask): boolean {
  return task.data_type === "kline_resample";
}

/** 合成任务的目标频率（collect_params.target_frequency）。 */
export function resampleTargetFrequency(task: CollectorTask): string {
  const value = task.collect_params?.target_frequency;
  return typeof value === "string" ? value.trim() : "";
}

/** 合成任务的源任务 ID；老任务没有 source_task_id 时为空。 */
export function resampleSourceTaskId(task: CollectorTask): string {
  const value = task.collect_params?.source_task_id;
  return typeof value === "string" ? value.trim() : "";
}

/**
 * 合成结果还没有数据时的空状态说明：任务还在准备时说明在准备数据集；已就绪则说明在等第一个目标桶收盘。
 * 预计时间按 UTC 固定网格给出（交易时段市场遇到休市会顺延），1d 等长周期的第一个桶可能要等很久，说清楚避免被当成故障。
 */
export function resampleEmptyDescription(task: CollectorTask, frequencyMinutes: number, now = new Date()): string {
  const prepareState = String(task.prepare_state || "").toLowerCase();
  if (prepareState && prepareState !== "ready") return "正在准备结果数据集，请稍后刷新";
  if (frequencyMinutes <= 0) return "等待第一个目标桶收盘";
  const size = frequencyMinutes * 60 * 1000;
  const next = new Date((Math.floor(now.getTime() / size) + 1) * size);
  const hh = String(next.getUTCHours()).padStart(2, "0");
  const mm = String(next.getUTCMinutes()).padStart(2, "0");
  const day = frequencyMinutes >= 24 * 60 ? `${next.getUTCMonth() + 1}-${next.getUTCDate()} ` : "";
  return `等待第一个目标桶收盘（预计 ${day}${hh}:${mm} UTC，交易时段市场遇休市会顺延）`;
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
  if (["stale", "unknown"].includes(resultStatus)) return "stale";
  if (getTaskResultViewIds(task).length > 0 && !["pending", "preparing", "waiting_view"].includes(resultStatus)) {
    return "ready";
  }
  return "preparing";
}

export function selectTaskIdFromQuery(tasks: CollectorTask[], requestedTaskId: unknown): string {
  const requested = queryString(requestedTaskId);
  const sortedTasks = sortTasksByCreatedAt(tasks);
  return sortedTasks.some(task => task.task_id === requested) ? requested : sortedTasks[0]?.task_id || "";
}

export function resolveActiveTask(
  tasks: CollectorTask[],
  details: Record<string, CollectorTask>,
  selectedTaskId: string
): CollectorTask | undefined {
  if (selectedTaskId) {
    return details[selectedTaskId] || tasks.find(task => task.task_id === selectedTaskId);
  }
  return sortTasksByCreatedAt(tasks)[0];
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
