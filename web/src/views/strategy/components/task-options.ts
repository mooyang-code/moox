import type { FactorTaskInfo } from "@/api/factor/types";

/** 创建实例时可选的因子任务：列出任务的全部结果列名，策略 DSL 里直接写这些列名。 */
export interface TaskOption {
  taskId: string;
  /** 任务的源数据集：它的 market_type 是 multi 时任务在统一数据集上。 */
  sourceDatasetId: string;
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

/** 当前空间里运行中的因子任务（试算只读，不要求未关联实例）；按名称排序。 */
export function enabledTasks(infos: FactorTaskInfo[], spaceId: string): TaskOption[] {
  return infos
    .filter(info => info.factor_task.space_id === spaceId && info.factor_task.status === "enabled")
    .map(info => ({
      taskId: info.factor_task.task_id,
      sourceDatasetId: info.factor_task.source_dataset_id,
      name: info.factor_task.name || info.factor_task.task_id,
      freq: info.factor_task.freq,
      columns: taskColumns(info),
      varMembers: (info.members || []).filter(member => member.kind === "var").map(member => member.member_key)
    }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.taskId.localeCompare(b.taskId));
}

/**
 * 创建实例时可选的任务：当前空间里运行中、还没有关联实例的因子任务（关联是独占且永久的）；按名称排序。
 */
export function availableTasks(infos: FactorTaskInfo[], spaceId: string): TaskOption[] {
  const unbound = new Set(infos.filter(info => !info.factor_task.bound_instance_id).map(info => info.factor_task.task_id));
  return enabledTasks(infos, spaceId).filter(task => unbound.has(task.taskId));
}

/** 任务是否在统一数据集上（源数据集的 market_type 是 multi，一个任务同时覆盖现货与永续）。 */
export function isUnifiedTask(task: Pick<TaskOption, "sourceDatasetId">, unifiedDatasetIds: ReadonlySet<string>): boolean {
  return unifiedDatasetIds.has(task.sourceDatasetId);
}

/**
 * 带可变成员的任务在这种绑定方式下能不能用：可变成员要由策略的 factor_params 逐期提交参数，
 * 而策略池（成员也不能写）、统一数据集、现货永续双市场都不允许写 factor_params（与 Strategy 的 dsl/pool.go、input/resolve.go、dsl/market.go 一致），
 * 这几种方式下带可变成员的任务一定通不过绑定预检。能用时返回空串，否则返回原因。
 */
export function varMemberBlockReason(binding: { pool: boolean; unified: boolean; marketCount: number }): string {
  if (binding.pool) return "策略池和它的成员都不能写 factor_params，带可变成员的任务没法提交参数";
  if (binding.unified)
    return "统一数据集上的任务暂不支持 factor_params（现货、永续会共用同一组参数），带可变成员的任务没法提交参数";
  if (binding.marketCount > 1)
    return "现货、永续双市场策略不支持 factor_params（两个任务的可变成员键可能重名），带可变成员的任务没法提交参数";
  return "";
}
