import type { FactorTask, FactorTaskInfo } from "@/api/factor/types";
import type { View } from "@/api/storage/types";
import { formatPeriod } from "@/views/factor/shared/health";

/** 结果 Tab 只展示已经有结果数据集的计算任务：创建中、清理中的不出现。 */
export function resultTasks(sets: FactorTaskInfo[]) {
  return sets.filter(item => item.factor_task.status === "enabled" || item.factor_task.status === "disabled");
}

export function resultTabTitle(info: Pick<FactorTaskInfo, "factor_task">, label: string) {
  return info.factor_task.status === "disabled" ? `${label}（已停用）` : label;
}

/** 计算任务唯一输出数据集的默认结果视图。 */
export function pickResultView(views: View[], set: Pick<FactorTask, "result_dataset_id">): View | null {
  return views.find(view => view.attributes?.view_role === "factor_result" && view.dataset_id === set.result_dataset_id) ?? null;
}

export function resolveActiveTaskId(sets: FactorTaskInfo[], preferred: string) {
  return sets.find(item => item.factor_task.task_id === preferred)?.factor_task.task_id ?? sets[0]?.factor_task.task_id ?? "";
}

export function resultSummary(info: Pick<FactorTaskInfo, "last_run">) {
  const states = info.last_run?.factors || [];
  const period = info.last_run?.last_period_time;
  return {
    total: states.length,
    complete: states.filter(state => state.status === "complete").length,
    degraded: states.filter(state => state.status === "degraded").length,
    skipped: states.filter(state => state.status === "skipped").length,
    hasPeriod: Boolean(period),
    lastPeriod: formatPeriod(period)
  };
}
