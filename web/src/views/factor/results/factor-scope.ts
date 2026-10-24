import { UNCATEGORIZED, type FactorCategory, type FactorMember } from "@/api/factor/types";
import type { DisplayHidden } from "@/api/strategy-types";
import type { TimeSeriesKey, ViewColumn } from "@/api/storage/types";
import type { QueryTimeSeriesRowsReq } from "@/api/storage/view";

/** 时序视图的四个系统键：随 row.key 返回，不能放进 column_names。 */
export const SYSTEM_KEYS = ["subject_id", "freq", "series_tag", "data_time"] as const;
/** 列设置里不可隐藏的系统键。 */
export const LOCKED_SYSTEM_KEYS = ["subject_id", "data_time"];
export const SYSTEM_KEY_LABELS: Record<string, string> = {
  subject_id: "数据ID",
  freq: "频率",
  series_tag: "序列标签",
  data_time: "时间"
};
/** 默认显示的基础行情列（与视图实际字段取交集）：crypto 有成交额 quote_volume、成交笔数 trade_num，A 股与指数用 amount。 */
export const BASE_MARKET_COLUMNS = ["open", "high", "low", "close", "volume", "quote_volume", "trade_num", "amount"];
/** 因子值弹窗与选择弹窗每页的输出项数。 */
export const FACTOR_PAGE_SIZE = 20;

export interface FactorParamColumn {
  column: string;
  name: string;
}

/** 一个输出项：因子目录、因子条件、钉住列、弹窗的最小单位。 */
export interface FactorOutputItem {
  column: string;
  output: string;
  alias: string;
  factorId: string;
  factorName: string;
  categoryId: string;
}

export interface FactorScope {
  /** 按分类顺序、成员顺序排列的全部可见输出项。 */
  outputs: FactorOutputItem[];
  outputByColumn: Map<string, FactorOutputItem>;
  /** 成员别名 → 可见的参数列。 */
  paramsByAlias: Map<string, FactorParamColumn[]>;
  /** 默认显示的基础行情列（按视图列顺序）。 */
  baseMarket: string[];
  /** 其他源字段，例如 request_id、quality_status，默认不显示。 */
  otherSource: string[];
  /** 分类 ID → 名称；包含成员用到但分类清单里没有的 ID。 */
  categoryNames: Map<string, string>;
  /** 有输出项的分类，按分类清单顺序，未分类最后。 */
  categoryOrder: string[];
}

export interface FactorScopeInput {
  viewColumns: ViewColumn[];
  members: FactorMember[];
  categories: FactorCategory[];
  /** 策略私有因子集的外显设置；共享因子集为空。 */
  hidden?: DisplayHidden;
}

const systemKeySet = new Set<string>(SYSTEM_KEYS);

function isSystemColumn(column: ViewColumn) {
  return systemKeySet.has(column.column_name) || column.origin_type === "COLUMN_ORIGIN_TYPE_SYSTEM";
}

function categoryOf(member: FactorMember) {
  return member.factor?.category_id || UNCATEGORIZED;
}

/**
 * 结果页的有效范围：因子目录只认当前成员清单（已移除成员留在视图里的残留列不算），分类取成员当前定义；
 * 策略外显设置按分类、成员、单列三种规则逐列隐藏；行情列是不带 column_kind 的业务列。
 */
export function buildFactorScope(input: FactorScopeInput): FactorScope {
  const hiddenCategories = new Set(input.hidden?.categories ?? []);
  const hiddenMembers = new Set(input.hidden?.members ?? []);
  const hiddenColumns = new Set(input.hidden?.columns ?? []);
  const viewByName = new Map(input.viewColumns.map(column => [column.column_name, column]));

  const categoryNames = new Map(input.categories.map(category => [category.category_id, category.name]));
  const categoryRank = new Map(input.categories.map((category, index) => [category.category_id, index]));
  const rank = (categoryId: string) =>
    categoryId === UNCATEGORIZED ? Number.MAX_SAFE_INTEGER : (categoryRank.get(categoryId) ?? Number.MAX_SAFE_INTEGER - 1);

  const memberColumns = new Set<string>();
  const visibleMembers: Array<{ member: FactorMember; categoryId: string; index: number }> = [];
  input.members.forEach((member, index) => {
    for (const column of [...(member.output_columns ?? []), ...(member.param_columns ?? [])]) memberColumns.add(column);
    const categoryId = categoryOf(member);
    if (hiddenCategories.has(categoryId) || hiddenMembers.has(member.member_alias)) return;
    visibleMembers.push({ member, categoryId, index });
  });
  visibleMembers.sort((a, b) => rank(a.categoryId) - rank(b.categoryId) || a.index - b.index);

  const outputs: FactorOutputItem[] = [];
  const paramsByAlias = new Map<string, FactorParamColumn[]>();
  const usedCategories: string[] = [];
  for (const { member, categoryId } of visibleMembers) {
    const visible = (column: string) => viewByName.has(column) && !hiddenColumns.has(column);
    const outputNames = member.factor?.outputs ?? [];
    let added = false;
    (member.output_columns ?? []).forEach((column, index) => {
      if (!visible(column)) return;
      added = true;
      outputs.push({
        column,
        output: outputNames[index] || column,
        alias: member.member_alias,
        factorId: member.factor_id,
        factorName: member.factor?.name || member.factor_id,
        categoryId
      });
    });
    const params = (member.param_columns ?? []).filter(visible).map(column => ({
      column,
      name:
        viewByName.get(column)?.attributes?.param_name ||
        (column.startsWith(`${member.member_alias}__`) ? column.slice(member.member_alias.length + 2) : column)
    }));
    paramsByAlias.set(member.member_alias, params);
    if (added && !usedCategories.includes(categoryId)) usedCategories.push(categoryId);
    if (!categoryNames.has(categoryId)) categoryNames.set(categoryId, categoryId === UNCATEGORIZED ? "未分类" : categoryId);
  }

  const baseSet = new Set(BASE_MARKET_COLUMNS);
  const baseMarket: string[] = [];
  const otherSource: string[] = [];
  for (const column of input.viewColumns) {
    const name = column.column_name;
    if (!name || isSystemColumn(column) || hiddenColumns.has(name)) continue;
    if (column.attributes?.column_kind || memberColumns.has(name)) continue;
    (baseSet.has(name) ? baseMarket : otherSource).push(name);
  }

  return {
    outputs,
    outputByColumn: new Map(outputs.map(item => [item.column, item])),
    paramsByAlias,
    baseMarket,
    otherSource,
    categoryNames,
    categoryOrder: usedCategories
  };
}

export interface OutputFilter {
  /** 空为全部分类。 */
  category?: string;
  keyword?: string;
}

/** 按分类与关键词（输出列名、输出名、成员别名、定义 ID、定义名称）过滤输出项。 */
export function filterOutputs(items: FactorOutputItem[], filter: OutputFilter) {
  const keyword = (filter.keyword ?? "").trim().toLowerCase();
  return items.filter(item => {
    if (filter.category && item.categoryId !== filter.category) return false;
    if (!keyword) return true;
    return [item.column, item.output, item.alias, item.factorId, item.factorName].some(text =>
      text.toLowerCase().includes(keyword)
    );
  });
}

/** 各分类的输出项数量（只统计关键词过滤后的项）。 */
export function countByCategory(items: FactorOutputItem[]) {
  const counts = new Map<string, number>();
  for (const item of items) counts.set(item.categoryId, (counts.get(item.categoryId) ?? 0) + 1);
  return counts;
}

export function pageSlice<T>(items: T[], page: number, size = FACTOR_PAGE_SIZE) {
  const start = (Math.max(1, page) - 1) * size;
  return items.slice(start, start + size);
}

/** 一页输出项需要查询的列：这些输出列加所属成员的参数列（去重）。 */
export function valueColumnsFor(scope: FactorScope, items: FactorOutputItem[]) {
  const columns = new Set<string>();
  for (const item of items) {
    columns.add(item.column);
    for (const param of scope.paramsByAlias.get(item.alias) ?? []) columns.add(param.column);
  }
  return [...columns];
}

/** 精确读取一行：完整 selector（显式带 series_tag，默认序列为空串）加 [t, t+1ms)，不统计总数。 */
export function rowValuesRequest(viewId: string, key: TimeSeriesKey, columns: string[]): QueryTimeSeriesRowsReq {
  const start = new Date(key.data_time || "");
  const end = new Date(start.getTime() + 1);
  return {
    space_id: key.space_id,
    view_id: viewId,
    selectors: [
      {
        space_id: key.space_id,
        dataset_id: key.dataset_id,
        subject_id: key.subject_id,
        freq: key.freq,
        series_tag: key.series_tag ?? ""
      }
    ],
    time_range: { start_time: start.toISOString(), end_time: end.toISOString() },
    column_names: columns,
    page: { page: 1, size: 1 },
    total_mode: "NONE"
  };
}

/** 参数文本，例如 "window=20, k=2"；没有参数时为空串。 */
export function paramText(scope: FactorScope, alias: string, values: Record<string, string>) {
  return (scope.paramsByAlias.get(alias) ?? []).map(param => `${param.name}=${formatValue(values[param.column])}`).join(", ");
}

/** 因子值保留 6 位有效数字；空值显示 "-"。 */
export function formatValue(raw: string | undefined) {
  if (raw === undefined || raw === "") return "-";
  const value = Number(raw);
  if (!Number.isFinite(value)) return raw;
  return Number.isInteger(value) ? String(value) : String(Number(value.toPrecision(6)));
}
