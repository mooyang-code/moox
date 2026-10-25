import { fixedParamsText } from "@/views/factor/shared/member-params";
import {
  UNCATEGORIZED,
  type FactorCategory,
  type FactorInfo,
  type FactorType,
  type FactorUsage,
  type MemberStatus
} from "@/api/factor/types";
import { memberStatusTag } from "@/views/factor/shared/status";
import { categoryName } from "@/views/factor/shared/params";

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
    if (filters.category && categoryOf(factor) !== filters.category) return false;
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
  /** 成员的固定参数摘要（如 span=20, warmup=50000）；没有固定参数时为空。 */
  paramsText?: string;
}

/** 使用情况 chip：「数据集名 · 频率」+ 状态点（labelOf 由页面用 store 拼出）。 */
export function usageChips(usages: FactorUsage[], labelOf: (setId: string) => string): UsageChip[] {
  return usages.map(usage => ({
    key: `${usage.set_id}/${usage.member_alias ?? ""}`,
    setId: usage.set_id,
    alias: usage.member_alias || undefined,
    label: labelOf(usage.set_id),
    status: usage.status,
    statusLabel: memberStatusTag(usage.status).label,
    paramsText: fixedParamsText(usage.params) || undefined
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

/** 分类 ID 合法形式：小写字母开头，只含小写字母、数字、下划线，最长 32 位。 */
export const CATEGORY_ID_PATTERN = /^[a-z][a-z0-9_]{0,31}$/;

export function categoryOf(factor: Pick<FactorInfo["factor"], "category_id">) {
  return factor.category_id || UNCATEGORIZED;
}

/** 分类树的顺序：按 sort_order，未分类固定最后（清单里没有时补上）。 */
export function orderedCategories(categories: FactorCategory[]): FactorCategory[] {
  const rest = categories
    .filter(item => item.category_id !== UNCATEGORIZED)
    .sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0));
  const uncategorized = categories.find(item => item.category_id === UNCATEGORIZED) ?? {
    category_id: UNCATEGORIZED,
    name: "未分类"
  };
  return [...rest, uncategorized];
}

/** 各分类的数量：传入的是已按关键词、类型、使用情况过滤（但未按分类过滤）的定义。 */
export function categoryCounts(items: FactorInfo[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const { factor } of items) {
    const id = categoryOf(factor);
    counts[id] = (counts[id] ?? 0) + 1;
  }
  return counts;
}

export interface CategoryDeleteApi {
  setCategory: (factorId: string, categoryId: string) => Promise<unknown>;
  deleteCategory: (categoryId: string) => Promise<unknown>;
}

/**
 * 删除分类：后端只允许删除空分类，所以先把其中的定义（取自未经筛选的全量清单）逐个改到未分类，
 * 全部成功后再删除。中途失败就停止，不删除分类，返回失败的定义。
 */
export async function deleteCategoryMovingFactors(categoryId: string, allItems: FactorInfo[], api: CategoryDeleteApi) {
  const moving = allItems.filter(item => categoryOf(item.factor) === categoryId).map(item => item.factor.factor_id);
  const moved: string[] = [];
  for (const factorId of moving) {
    try {
      await api.setCategory(factorId, UNCATEGORIZED);
      moved.push(factorId);
    } catch (error) {
      return { deleted: false, moved, failed: factorId, error };
    }
  }
  await api.deleteCategory(categoryId);
  return { deleted: true, moved };
}

/** 定义表格的一行：factor_id 与 updated_at 提到顶层，供表格按列排序。 */
export interface DefinitionRow {
  factor_id: string;
  updated_at: string;
  factor: FactorInfo["factor"];
  category: string;
  paramNames: string;
  chips: UsageChip[];
  edit: ActionState;
  remove: ActionState;
}

export function definitionRows(
  items: FactorInfo[],
  categories: FactorCategory[],
  labelOf: (setId: string) => string
): DefinitionRow[] {
  return items.map(item => ({
    factor_id: item.factor.factor_id,
    updated_at: item.factor.updated_at,
    factor: item.factor,
    category: categoryName(categories, item.factor.category_id),
    paramNames: (item.factor.params ?? []).map(param => param.name).join(", ") || "-",
    chips: usageChips(item.usages, labelOf),
    edit: definitionEditState(item),
    remove: definitionDeleteState(item)
  }));
}

/** 批量移动分类：逐个调用，中途失败就停止，返回已移动的定义与失败的那个。 */
export async function moveFactorsToCategory(
  factorIds: string[],
  categoryId: string,
  setCategory: CategoryDeleteApi["setCategory"]
) {
  const moved: string[] = [];
  for (const factorId of factorIds) {
    try {
      await setCategory(factorId, categoryId);
      moved.push(factorId);
    } catch (error) {
      return { moved, failed: factorId, error };
    }
  }
  return { moved };
}
