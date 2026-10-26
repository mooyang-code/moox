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
const blocked = (reason: string): ActionState => ({ disabled: true, reason });

export function filterTasks(sets: FactorTaskInfo[], filters: TaskFilters, labelOf: (set: FactorTask) => string) {
  const keyword = filters.keyword.trim().toLowerCase();
  return sets.filter(({ factor_task: set }) => {
    if (filters.status && set.status !== filters.status) return false;
    if (!keyword) return true;
    return [set.task_id, set.name || "", set.source_dataset_id, labelOf(set)].some(text => text.toLowerCase().includes(keyword));
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

const LOCKED_REASON = "因子任务创建中，暂不能操作";
const isLocked = (set: Pick<FactorTask, "status">) => set.status === "pending";

/** 删除因子任务的前置条件：已停用、没有成员、没有关联策略实例（关联是永久的）。 */
export function deleteState(info: Pick<FactorTaskInfo, "factor_task" | "members">): ActionState {
  if (info.factor_task.bound_instance_id) return blocked("已关联策略实例（关联永久有效），不能删除");
  if (info.factor_task.status !== "disabled") return blocked("请先停用因子任务，再删除");
  if ((info.members || []).length > 0) return blocked("请先在「因子」里移除全部因子成员，再删除因子任务");
  return enabledAction;
}

export function addFactorState(set: Pick<FactorTask, "status">): ActionState {
  return isLocked(set) ? blocked(LOCKED_REASON) : enabledAction;
}

export interface MemberActions {
  toggleLabel: "启用" | "停用";
  toggleTarget: MemberStatus;
  toggle: ActionState;
  edit: ActionState;
  remove: ActionState;
}

export function memberActions(set: Pick<FactorTask, "status">, member: Pick<FactorMember, "status">): MemberActions {
  const enabled = member.status === "enabled";
  return {
    toggleLabel: enabled ? "停用" : "启用",
    toggleTarget: enabled ? "disabled" : "enabled",
    toggle: isLocked(set)
      ? blocked(LOCKED_REASON)
      : !enabled && set.status === "disabled"
        ? blocked("因子任务已停用，请先启用因子任务，再启用因子")
        : enabledAction,
    edit: enabled ? blocked("已启用的因子需要先停用，才能编辑定义") : enabledAction,
    remove: enabled ? blocked("已启用的因子需要先停用，才能移除") : enabledAction
  };
}

/** 成员本期的状态：周期状态以成员键为单位（同一定义可以用不同参数多次出现）。 */
export function memberPeriodState(info: Pick<FactorTaskInfo, "last_run">, memberKey: string): FactorPeriodState | undefined {
  return info.last_run?.factors?.find(item => (item.member_key || item.factor_id) === memberKey);
}

/** 可变成员最近一次被接受的提交：从未提交过返回 null（实际用定义的默认参数）。 */
export function lastSubmission(member: Pick<FactorMember, "kind" | "last_submitted_period" | "last_submitted_at">) {
  if (member.kind !== "var") return null;
  const period = Number(member.last_submitted_period || 0);
  if (!period) return null;
  return { period, at: member.last_submitted_at || "" };
}

/** 任务名直接用作结果数据集的名称：1～10 个字、含汉字、首尾无空白、无控制字符（与服务端、CLI 的规则一致）。 */
export const MAX_TASK_NAME_RUNES = 10;

/** 返回任务名不合法的原因；合法返回空串。 */
export function validateTaskName(name: string): string {
  if (!name || name !== name.trim() || Array.from(name).length > MAX_TASK_NAME_RUNES) {
    return `任务名称必须是 1～${MAX_TASK_NAME_RUNES} 个字，首尾不能有空白`;
  }
  if (/[\u0000-\u001f\u007f-\u009f]/.test(name)) return "任务名称不能包含控制字符";
  if (!/\p{Script=Han}/u.test(name)) return "任务名称必须含汉字";
  return "";
}
