import type {
  FactorMember,
  FactorPeriodState,
  FactorSet,
  FactorSetInfo,
  FactorSetStatus,
  MemberStatus
} from "@/api/factor/types";

export interface TaskFilters {
  keyword: string;
  status: "" | FactorSetStatus;
}

export interface ActionState {
  disabled: boolean;
  reason: string;
}

const enabledAction: ActionState = { disabled: false, reason: "" };
const blocked = (reason: string): ActionState => ({ disabled: true, reason });

export function filterSets(sets: FactorSetInfo[], filters: TaskFilters, labelOf: (set: FactorSet) => string) {
  const keyword = filters.keyword.trim().toLowerCase();
  return sets.filter(({ factor_set: set }) => {
    if (filters.status && set.status !== filters.status) return false;
    if (!keyword) return true;
    return [set.set_id, set.source_dataset_id, labelOf(set)].some(text => text.toLowerCase().includes(keyword));
  });
}

export function memberCounts(info: Pick<FactorSetInfo, "members">) {
  const members = info.members || [];
  return { enabled: members.filter(member => member.status === "enabled").length, total: members.length };
}

export function memberCountText(info: Pick<FactorSetInfo, "members">) {
  const { enabled, total } = memberCounts(info);
  return `${enabled} / ${total}`;
}

export function scopeText(set: Pick<FactorSet, "subject_mode" | "subjects">) {
  return set.subject_mode === "include" ? `指定 ${(set.subjects || []).length} 个` : "全部对象";
}

export type SetActionKind = "retry" | "disable" | "enable" | "none";

/** 列表行 / 抽屉上与状态相关的主动作：激活失败重试，运行中停用，已停用启用。 */
export function setActionKind(set: Pick<FactorSet, "status">): SetActionKind {
  if (set.status === "pending") return "retry";
  if (set.status === "enabled") return "disable";
  if (set.status === "disabled") return "enable";
  return "none";
}

const lockedReason = (status: FactorSetStatus) =>
  status === "pending" ? "计算任务创建中，暂不能操作" : "计算任务清理中，暂不能操作";
const isLocked = (set: Pick<FactorSet, "status">) => set.status === "pending" || set.status === "deleting";

/** 删除计算任务的前置条件：没有成员（D23），且不在清理中。 */
export function deleteState(info: Pick<FactorSetInfo, "factor_set" | "members">): ActionState {
  if (info.factor_set.status === "deleting") return blocked("计算任务清理中");
  if ((info.members || []).length > 0) return blocked("请先在「因子」里移除全部因子成员，再删除计算任务");
  return enabledAction;
}

export function addFactorState(set: Pick<FactorSet, "status">): ActionState {
  return isLocked(set) ? blocked(lockedReason(set.status)) : enabledAction;
}

export interface MemberActions {
  toggleLabel: "启用" | "停用";
  toggleTarget: MemberStatus;
  toggle: ActionState;
  edit: ActionState;
  remove: ActionState;
}

export function memberActions(set: Pick<FactorSet, "status">, member: Pick<FactorMember, "status">): MemberActions {
  const enabled = member.status === "enabled";
  return {
    toggleLabel: enabled ? "停用" : "启用",
    toggleTarget: enabled ? "disabled" : "enabled",
    toggle: isLocked(set) ? blocked(lockedReason(set.status)) : enabledAction,
    edit: enabled ? blocked("已启用的因子需要先停用，才能编辑定义") : enabledAction,
    remove: enabled ? blocked("已启用的因子需要先停用，才能移除") : enabledAction
  };
}

export function memberPeriodState(info: Pick<FactorSetInfo, "last_run">, factorId: string): FactorPeriodState | undefined {
  return info.last_run?.factors?.find(item => item.factor_id === factorId);
}
