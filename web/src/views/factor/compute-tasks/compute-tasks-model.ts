import type {
  FactorMember,
  FactorPeriodState,
  FactorTask,
  FactorTaskInfo,
  FactorTaskStatus,
  MemberStatus
} from "@/api/factor/types";

/**
 * 计算引擎每分钟同步一次目录：启用后约 1 分钟才开始实时计算，期间完成的周期不会自动补算。
 */
export const ENABLE_ENGINE_HINT = "计算引擎约 1 分钟内生效，期间的周期不会自动补算，如需补齐可手动提交补算。确认启用？";

export interface TaskFilters {
  keyword: string;
  status: "" | FactorTaskStatus;
}

export interface ActionState {
  disabled: boolean;
  reason: string;
}

const enabledAction: ActionState = { disabled: false, reason: "" };
const PRIVATE_SET_REASON = "策略实例的私有因子任务，成员与状态由实例维护";

/** 策略实例的私有因子任务：成员恰好是策略 factors 声明的因子，随实例启用、停用与删除自动维护。 */
export const isPrivateTask = (set: Pick<FactorTask, "owner_instance_id">) => Boolean(set.owner_instance_id);
const blocked = (reason: string): ActionState => ({ disabled: true, reason });

export function filterTasks(sets: FactorTaskInfo[], filters: TaskFilters, labelOf: (set: FactorTask) => string) {
  const keyword = filters.keyword.trim().toLowerCase();
  return sets.filter(({ factor_task: set }) => {
    if (filters.status && set.status !== filters.status) return false;
    if (!keyword) return true;
    return [set.task_id, set.source_dataset_id, labelOf(set)].some(text => text.toLowerCase().includes(keyword));
  });
}

export function memberCounts(info: Pick<FactorTaskInfo, "members">) {
  const members = info.members || [];
  return { enabled: members.filter(member => member.status === "enabled").length, total: members.length };
}

export function memberCountText(info: Pick<FactorTaskInfo, "members">) {
  const { enabled, total } = memberCounts(info);
  return `${enabled} / ${total}`;
}

export type SetActionKind = "retry" | "disable" | "enable" | "none";

/** 列表行 / 抽屉上与状态相关的主动作：激活失败重试，运行中停用，已停用启用。 */
export function setActionKind(set: Pick<FactorTask, "status">): SetActionKind {
  if (set.status === "pending") return "retry";
  if (set.status === "enabled") return "disable";
  if (set.status === "disabled") return "enable";
  return "none";
}

const lockedReason = (status: FactorTaskStatus) =>
  status === "pending" ? "计算任务创建中，暂不能操作" : "计算任务清理中，暂不能操作";
const isLocked = (set: Pick<FactorTask, "status">) => set.status === "pending" || set.status === "deleting";

/** 删除计算任务的前置条件：已停用、没有成员（D23），且不在清理中。 */
export function deleteState(info: Pick<FactorTaskInfo, "factor_task" | "members">): ActionState {
  if (info.factor_task.status === "deleting") return blocked("计算任务清理中");
  if (info.factor_task.status !== "disabled") return blocked("请先停用计算任务，再删除");
  if ((info.members || []).length > 0) return blocked("请先在「因子」里移除全部因子成员，再删除计算任务");
  return enabledAction;
}

export function addFactorState(set: Pick<FactorTask, "status"> & Partial<Pick<FactorTask, "owner_instance_id">>): ActionState {
  if (isPrivateTask(set)) return blocked(PRIVATE_SET_REASON);
  return isLocked(set) ? blocked(lockedReason(set.status)) : enabledAction;
}

export interface MemberActions {
  toggleLabel: "启用" | "停用";
  toggleTarget: MemberStatus;
  toggle: ActionState;
  edit: ActionState;
  remove: ActionState;
}

export function memberActions(
  set: Pick<FactorTask, "status"> & Partial<Pick<FactorTask, "owner_instance_id">>,
  member: Pick<FactorMember, "status">
): MemberActions {
  const enabled = member.status === "enabled";
  if (isPrivateTask(set)) {
    return {
      toggleLabel: enabled ? "停用" : "启用",
      toggleTarget: enabled ? "disabled" : "enabled",
      toggle: blocked(PRIVATE_SET_REASON),
      edit: blocked(PRIVATE_SET_REASON),
      remove: blocked(PRIVATE_SET_REASON)
    };
  }
  return {
    toggleLabel: enabled ? "停用" : "启用",
    toggleTarget: enabled ? "disabled" : "enabled",
    toggle: isLocked(set)
      ? blocked(lockedReason(set.status))
      : !enabled && set.status === "disabled"
        ? blocked("计算任务已停用，请先启用计算任务，再启用因子")
        : enabledAction,
    edit: enabled ? blocked("已启用的因子需要先停用，才能编辑定义") : enabledAction,
    remove: enabled ? blocked("已启用的因子需要先停用，才能移除") : enabledAction
  };
}

/** 成员本期的状态：周期状态以成员别名为单位（同一定义可以用不同别名出现多次）。 */
export function memberPeriodState(info: Pick<FactorTaskInfo, "last_run">, memberAlias: string): FactorPeriodState | undefined {
  return info.last_run?.factors?.find(item => (item.member_alias || item.factor_id) === memberAlias);
}

/** 参数来源的展示：requested 提交生效、carried 沿用、carried_offline 引擎离线沿用、default 默认值。 */
export function paramsSourceLabel(source?: string): { label: string; color: string } | null {
  switch (source) {
    case "requested":
      return { label: "参数已更新", color: "green" };
    case "carried":
      return { label: "沿用参数", color: "gray" };
    case "carried_offline":
      return { label: "离线沿用", color: "orangered" };
    case "default":
      return { label: "默认参数", color: "arcoblue" };
    default:
      return null;
  }
}
