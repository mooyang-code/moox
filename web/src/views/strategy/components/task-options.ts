import type { FactorTaskInfo } from "@/api/factor/types";

/** 创建实例时可选的因子任务：列出任务的全部结果列名，策略 DSL 里直接写这些列名。 */
export interface TaskOption {
  taskId: string;
  /** 展示名：任务名（没有名称时用任务 ID）。 */
  name: string;
  freq: string;
  /** 任务全部成员的结果列名（按成员顺序）。 */
  columns: string[];
  /** 可变成员的成员键：策略的 factor_params 要为它们逐个给出参数。 */
  varMembers: string[];
}

/** 任务的全部结果列名。 */
export function taskColumns(info: Pick<FactorTaskInfo, "members">): string[] {
  return (info.members || []).flatMap(member => member.output_columns ?? []);
}

/**
 * 创建实例时可选的任务：当前空间里运行中、还没有关联实例的因子任务（关联是独占且永久的）；按名称排序。
 */
export function availableTasks(infos: FactorTaskInfo[], spaceId: string): TaskOption[] {
  return infos
    .filter(
      info =>
        info.factor_task.space_id === spaceId && info.factor_task.status === "enabled" && !info.factor_task.bound_instance_id
    )
    .map(info => ({
      taskId: info.factor_task.task_id,
      name: info.factor_task.name || info.factor_task.task_id,
      freq: info.factor_task.freq,
      columns: taskColumns(info),
      varMembers: (info.members || []).filter(member => member.kind === "var").map(member => member.member_key)
    }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.taskId.localeCompare(b.taskId));
}
