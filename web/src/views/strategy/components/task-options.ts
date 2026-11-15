import { parseDocument } from "yaml";
import type { FactorTaskInfo } from "@/api/factor/types";

/** 创建实例时可选的计算任务：列出任务的全部结果列名，策略 DSL 里直接写这些列名。 */
export interface TaskOption {
  taskId: string;
  /** 任务的源数据集：它的 market_type 是 multi 时任务在统一数据集上。 */
  sourceDatasetId: string;
  /** 展示名：任务名（没有名称时用任务 ID）。 */
  name: string;
  freq: string;
  /** 任务全部成员的结果列名（按成员顺序）。 */
  columns: string[];
  /** 可变成员的成员键：声明了 factor_params 的策略要为它们逐个给出参数，其余策略只读它们的列。 */
  varMembers: string[];
  /** 任务关联的实例（声明了 factor_params 的实例启用时独占关联）；空串表示没有关联。 */
  boundInstanceId: string;
}

/** 任务的全部结果列名。 */
export function taskColumns(info: Pick<FactorTaskInfo, "members">): string[] {
  return (info.members || []).flatMap(member => member.output_columns ?? []);
}

/** 当前空间里运行中的计算任务（试算与不调参的实例只读，不要求未关联实例）；按名称排序。 */
export function enabledTasks(infos: FactorTaskInfo[], spaceId: string): TaskOption[] {
  return infos
    .filter(info => info.factor_task.space_id === spaceId && info.factor_task.status === "enabled")
    .map(info => ({
      taskId: info.factor_task.task_id,
      sourceDatasetId: info.factor_task.source_dataset_id,
      name: info.factor_task.name || info.factor_task.task_id,
      freq: info.factor_task.freq,
      columns: taskColumns(info),
      varMembers: (info.members || []).filter(member => member.kind === "var").map(member => member.member_key),
      boundInstanceId: info.factor_task.bound_instance_id || ""
    }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.taskId.localeCompare(b.taskId));
}

/** DSL 是否声明了 factor_params：声明了的实例启用时独占关联主帧任务、逐期提交参数；解析不了按没有声明处理（服务端预检再核对）。 */
export function declaresFactorParams(dslYaml: string): boolean {
  try {
    const document = parseDocument(dslYaml).toJS();
    return Boolean(
      document && typeof document === "object" && document.factor_params !== undefined && document.factor_params !== null
    );
  } catch {
    return false;
  }
}

/**
 * 创建实例时某个任务键下能不能选这个任务：声明了 factor_params 的实例要独占关联主帧任务，已关联到别的实例的主帧任务不能选；
 * 附加来源的任务、不调参的实例只读任务的列，不关联，已关联到别的实例的任务也能选。
 */
export function selectableTask(task: Pick<TaskOption, "boundInstanceId">, tuned: boolean, source: string): boolean {
  return !(tuned && !source && task.boundInstanceId);
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
