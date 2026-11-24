import { parseDocument } from "yaml";
import type { FactorTaskInfo } from "@/api/factor/types";

/** 创建实例时可选的计算任务：列出任务的全部结果列名，策略 DSL 里直接写这些列名。 */
export interface TaskOption {
  taskId: string;
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
      name: info.factor_task.name || info.factor_task.task_id,
      freq: info.factor_task.freq,
      columns: taskColumns(info),
      varMembers: (info.members || []).filter(member => member.kind === "var").map(member => member.member_key),
      boundInstanceId: info.factor_task.bound_instance_id || ""
    }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.taskId.localeCompare(b.taskId));
}

/**
 * DSL 是否声明了 factor_params：声明了的实例启用时独占关联主帧任务、逐期提交参数。与后端一致按成员个数判断，
 * 写成空对象 `factor_params: {}` 不算声明；解析不了按没有声明处理（服务端预检再核对）。
 */
export function declaresFactorParams(dslYaml: string): boolean {
  try {
    const params = parseDocument(dslYaml).toJS()?.factor_params;
    return Boolean(params && typeof params === "object" && Object.keys(params).length > 0);
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

/**
 * 带可变成员的任务能不能登记在这个任务键下：附加来源的任务不能有可变成员（factor_params 只作用于主帧的任务，与 Strategy 的
 * input/sources.go 一致）；主帧任务都能选，不写 factor_params 的实例（含策略池、双市场）只读可变成员的列。
 * 能选时返回空串，否则返回原因。
 */
export function varMemberBlockReason(source: string): string {
  return source ? SOURCE_VAR_MEMBER_REASON : "";
}

/** 附加来源的任务不能有可变成员的原因。 */
export const SOURCE_VAR_MEMBER_REASON = "factor_params 只作用于主帧的任务，附加来源的任务不能有可变成员";
