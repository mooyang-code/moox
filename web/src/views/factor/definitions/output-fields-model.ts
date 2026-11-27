import {
  UNCATEGORIZED,
  type FactorCategory,
  type FactorTask,
  type FactorTaskInfo,
  type FactorTaskStatus,
  type FactorType,
  type MemberKind,
  type MemberStatus
} from "@/api/factor/types";
import type { GroupFilterItem } from "@/components/group-filter-panel/index.vue";
import { categoryName, paramsText } from "@/views/factor/shared/params";
import { categoryOf, orderedCategories } from "./definitions-model";

export interface FactorOutputFieldRow {
  row_key: string;
  column_name: string;
  /** 因子内输出项名；单输出因子与 factor_id 相同时为空，多输出时显示输出项名 */
  output_label: string;
  factor_id: string;
  factor_name: string;
  factor_type: FactorType;
  category_id: string;
  category_name: string;
  member_key: string;
  member_kind: MemberKind;
  params_text: string;
  member_status: MemberStatus;
  task_id: string;
  task_label: string;
  task_status: FactorTaskStatus;
  freq: string;
  result_dataset_id: string;
  input_columns: string[];
  lookback_periods: number;
}

export interface OutputFieldsFilter {
  category: string;
  taskId: string;
  kind: MemberKind | "";
  status: MemberStatus | "";
  keyword: string;
}

type RouteQuery = Record<string, unknown>;

const queryText = (value: unknown) => (Array.isArray(value) ? String(value[0] ?? "") : String(value ?? "")).trim();

export function outputFieldsQueryFromRoute(query: RouteQuery): OutputFieldsFilter {
  const kind = queryText(query.kind);
  const status = queryText(query.status);
  return {
    category: queryText(query.category),
    taskId: queryText(query.task),
    kind: kind === "fixed" || kind === "var" ? kind : "",
    status: status === "enabled" || status === "disabled" ? status : "",
    keyword: queryText(query.keyword)
  };
}

export function outputFieldsQueryToRoute(filter: OutputFieldsFilter): Record<string, string> {
  const query: Record<string, string> = {};
  if (filter.category) query.category = filter.category;
  if (filter.taskId) query.task = filter.taskId;
  if (filter.kind) query.kind = filter.kind;
  if (filter.status) query.status = filter.status;
  if (filter.keyword.trim()) query.keyword = filter.keyword.trim();
  return query;
}

/**
 * 从当前空间的因子计算任务展开全部成员产出的结果列：
 * 列名取服务端派生的 member.output_columns（与计算任务详情一致，缺省回退 member.factor.outputs）。
 */
export function expandFactorOutputFields(
  tasks: ReadonlyArray<FactorTaskInfo>,
  categories: FactorCategory[],
  taskLabelOf: (task: FactorTask) => string
): FactorOutputFieldRow[] {
  const rows: FactorOutputFieldRow[] = [];
  for (const info of tasks) {
    const task = info.factor_task;
    if (!task) continue;
    const taskLabel = taskLabelOf(task);
    for (const member of info.members || []) {
      const factor = member.factor;
      const memberKey = (member.member_key || member.factor_id || "").trim();
      const columns = member.output_columns?.length ? member.output_columns : factor?.outputs || [];
      const rawOutputs = factor?.outputs || [];
      const categoryId = factor ? categoryOf(factor) : UNCATEGORIZED;
      const catName = categoryName(categories, categoryId);
      const formattedParams = paramsText(member.params);

      columns.forEach((columnName, index) => {
        const cleanColumn = columnName.trim();
        if (!cleanColumn) return;
        const outputName = rawOutputs.length > 1 ? rawOutputs[index] || "" : "";
        rows.push({
          row_key: `${task.task_id}:${memberKey}:${cleanColumn}`,
          column_name: cleanColumn,
          output_label: outputName,
          factor_id: member.factor_id,
          factor_name: factor?.name?.trim() || member.factor_id,
          factor_type: factor?.factor_type || "timeseries",
          category_id: categoryId,
          category_name: catName,
          member_key: memberKey,
          member_kind: member.kind || "fixed",
          params_text: formattedParams,
          member_status: member.status || "enabled",
          task_id: task.task_id,
          task_label: taskLabel,
          task_status: task.status,
          freq: task.freq,
          result_dataset_id: task.result_dataset_id,
          input_columns: factor?.input_columns || [],
          lookback_periods: factor?.lookback_periods ?? 0
        });
      });
    }
  }
  return rows;
}

export function filterFactorOutputFields(
  rows: ReadonlyArray<FactorOutputFieldRow>,
  filter: OutputFieldsFilter
): FactorOutputFieldRow[] {
  const keyword = filter.keyword.trim().toLowerCase();
  return rows.filter(row => {
    if (filter.category && row.category_id !== filter.category) return false;
    if (filter.taskId && row.task_id !== filter.taskId) return false;
    if (filter.kind && row.member_kind !== filter.kind) return false;
    if (filter.status && row.member_status !== filter.status) return false;
    if (!keyword) return true;
    return [
      row.column_name,
      row.output_label,
      row.factor_id,
      row.factor_name,
      row.member_key,
      row.params_text,
      row.task_id,
      row.task_label,
      row.result_dataset_id
    ].some(text => text.toLowerCase().includes(keyword));
  });
}

/** 按因子分类排序生成左侧分组目录（只展示当前空间有产出字段的分类），计数取除分类外筛选后的数量。 */
export function buildOutputFieldCategoryItems(
  categories: FactorCategory[],
  allRows: ReadonlyArray<FactorOutputFieldRow>,
  filteredWithoutCategory: ReadonlyArray<FactorOutputFieldRow>
): GroupFilterItem[] {
  const presentIds = new Set(allRows.map(row => row.category_id));
  const counts = new Map<string, number>();
  for (const row of filteredWithoutCategory) {
    counts.set(row.category_id, (counts.get(row.category_id) || 0) + 1);
  }

  const ordered = orderedCategories(categories);
  const items: GroupFilterItem[] = [];
  const seen = new Set<string>();
  for (const category of ordered) {
    if (!presentIds.has(category.category_id)) continue;
    seen.add(category.category_id);
    items.push({
      key: category.category_id,
      label: category.name,
      count: counts.get(category.category_id) || 0
    });
  }
  // 兼容未登记在分类列表里的兜底分类 ID（插在未分类前或末尾）
  for (const id of presentIds) {
    if (seen.has(id)) continue;
    items.push({
      key: id,
      label: categoryName(categories, id),
      count: counts.get(id) || 0
    });
  }
  return items;
}
