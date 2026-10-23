import type { FactorInfo, FactorType, FactorUsage, MemberStatus } from "@/api/factor/types";
import { memberStatusTag } from "@/views/factor/shared/status";

export type UsageFilter = "all" | "using" | "idle";

export interface DefinitionFilters {
  type: "" | FactorType;
  usage: UsageFilter;
  keyword: string;
  /** 分类筛选，空为全部。 */
  category?: string;
}

export interface ActionState {
  disabled: boolean;
  reason: string;
}

const allowed: ActionState = { disabled: false, reason: "" };

export function usageCounts(items: FactorInfo[]) {
  const using = items.filter(item => item.usages.length > 0).length;
  return { all: items.length, using, idle: items.length - using };
}

export function filterDefinitions(items: FactorInfo[], filters: DefinitionFilters) {
  const keyword = filters.keyword.trim().toLowerCase();
  return items.filter(({ factor, usages }) => {
    if (filters.type && factor.factor_type !== filters.type) return false;
    if (filters.usage === "using" && !usages.length) return false;
    if (filters.usage === "idle" && usages.length) return false;
    if (filters.category && (factor.category_id || "uncategorized") !== filters.category) return false;
    if (!keyword) return true;
    return factor.factor_id.toLowerCase().includes(keyword) || factor.name.toLowerCase().includes(keyword);
  });
}

export interface UsageChip {
  /** 列表项的唯一键：同一定义在一个集合里可以用不同别名出现多次。 */
  key: string;
  setId: string;
  /** 成员别名；与定义 ID 相同时为空。 */
  alias?: string;
  label: string;
  status: MemberStatus;
  statusLabel: string;
}

/** 使用情况 chip：「数据集名 · 频率」+ 状态点（labelOf 由页面用 store 拼出）。 */
export function usageChips(usages: FactorUsage[], labelOf: (setId: string) => string): UsageChip[] {
  return usages.map(usage => ({
    key: `${usage.set_id}/${usage.member_alias ?? ""}`,
    setId: usage.set_id,
    alias: usage.member_alias || undefined,
    label: labelOf(usage.set_id),
    status: usage.status,
    statusLabel: memberStatusTag(usage.status).label
  }));
}

/** 被任一已启用成员引用时不能编辑（后端规则 D20）。 */
export function definitionEditState(item: Pick<FactorInfo, "usages">): ActionState {
  return item.usages.some(usage => usage.status === "enabled")
    ? { disabled: true, reason: "被启用中的计算任务使用，需先停用" }
    : allowed;
}

/** 仍被任一计算任务引用时不能删除（D21）。 */
export function definitionDeleteState(item: Pick<FactorInfo, "usages">): ActionState {
  return item.usages.length > 0 ? { disabled: true, reason: "仍被计算任务使用，需先从计算任务中移除" } : allowed;
}
