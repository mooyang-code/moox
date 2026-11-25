import type { FactorTaskInfo } from "@/api/factor/types";
import { formatPeriod } from "@/views/factor/shared/health";

/** 结果 Tab 只展示已经有结果数据集的计算任务：创建中、清理中的不出现。 */
export function resultTasks(sets: FactorTaskInfo[]) {
  return sets.filter(item => item.factor_task.status === "enabled" || item.factor_task.status === "disabled");
}

export function resolveActiveTaskId(sets: FactorTaskInfo[], preferred: string) {
  return sets.find(item => item.factor_task.task_id === preferred)?.factor_task.task_id ?? sets[0]?.factor_task.task_id ?? "";
}

export function resultSummary(info: Pick<FactorTaskInfo, "last_run">, zone: string) {
  const states = info.last_run?.factors || [];
  const period = info.last_run?.last_period_time;
  return {
    total: states.length,
    complete: states.filter(state => state.status === "complete").length,
    degraded: states.filter(state => state.status === "degraded").length,
    skipped: states.filter(state => state.status === "skipped").length,
    hasPeriod: Boolean(period),
    lastPeriod: formatPeriod(period, zone)
  };
}
