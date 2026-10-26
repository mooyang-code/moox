import { UNCATEGORIZED, type FactorCategory, type FactorMember } from "@/api/factor/types";
import type { DisplayHidden } from "@/api/strategy-types";
import type { TimeSeriesKey, ViewColumn } from "@/api/storage/types";
import type { QueryTimeSeriesRowsReq } from "@/api/storage/view";
import { PARAMS_COLUMN, parseParams } from "@/utils/factor-name";

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
/** 列设置里行情列的中文名；没有的直接显示字段名。 */
export const MARKET_COLUMN_LABELS: Record<string, string> = {
  open: "开盘",
  high: "最高",
  low: "最低",
  close: "收盘",
  volume: "成交量",
  quote_volume: "成交额",
  trade_num: "成交笔数",
  amount: "成交额"
};
/** 因子值弹窗与选择弹窗每页的输出项数。 */
export const FACTOR_PAGE_SIZE = 20;

/** 一个输出项：因子目录、因子条件、钉住列、弹窗的最小单位。 */
export interface FactorOutputItem {
  column: string;
  output: string;
  /** 成员键：任务内唯一，固定成员参数全为默认值时就是因子 ID，可变成员以 __var 结尾。 */
  memberKey: string;
  factorId: string;
  factorName: string;
  categoryId: string;
}

export interface FactorScope {
  /** 按分类顺序、成员顺序排列的全部可见输出项。 */
  outputs: FactorOutputItem[];
  outputByColumn: Map<string, FactorOutputItem>;
  /** 结果视图里记录各成员实际参数的系统列（__params）；视图没有时为空串。它默认不展示，由“显示参数”开关按需读取。 */
  paramsColumn: string;
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
  /** 关联策略实例的外显设置；任务还没有关联实例时为空。 */
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
  const paramsColumn = input.viewColumns.find(column => column.column_name === PARAMS_COLUMN)?.column_name ?? "";
  const visibleMembers: Array<{ member: FactorMember; categoryId: string; index: number }> = [];
  input.members.forEach((member, index) => {
    for (const column of member.output_columns ?? []) memberColumns.add(column);
    const categoryId = categoryOf(member);
    if (hiddenCategories.has(categoryId) || hiddenMembers.has(member.member_key)) return;
    visibleMembers.push({ member, categoryId, index });
  });
  visibleMembers.sort((a, b) => rank(a.categoryId) - rank(b.categoryId) || a.index - b.index);

  const outputs: FactorOutputItem[] = [];
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
        memberKey: member.member_key,
        factorId: member.factor_id,
        factorName: member.factor?.name || member.factor_id,
        categoryId
      });
    });
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
    paramsColumn,
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

/** 按分类与关键词（输出列名、输出名、成员键、定义 ID、定义名称）过滤输出项。 */
export function filterOutputs(items: FactorOutputItem[], filter: OutputFilter) {
  const keyword = (filter.keyword ?? "").trim().toLowerCase();
  return items.filter(item => {
    if (filter.category && item.categoryId !== filter.category) return false;
    if (!keyword) return true;
    return [item.column, item.output, item.memberKey, item.factorId, item.factorName].some(text =>
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

/** 一页输出项需要查询的列：这些输出列；打开“显示参数”时再加上记录各成员实际参数的 __params 列。 */
export function valueColumnsFor(scope: FactorScope, items: FactorOutputItem[], withParams = false) {
  const columns = new Set(items.map(item => item.column));
  if (withParams && scope.paramsColumn) columns.add(scope.paramsColumn);
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

/**
 * 一个成员在这一行实际用的参数文本，例如 "k=2, window=20"：从 __params 列的 JSON 里取这个成员的参数（参数名升序）。
 * 没有读到 __params、解析失败或这一行没有记录这个成员（全部按默认参数计算）时为空串。
 */
export function paramText(memberKey: string, rawParams: string | undefined) {
  if (!rawParams) return "";
  try {
    const values = parseParams(rawParams)[memberKey];
    return Object.keys(values ?? {})
      .sort()
      .map(name => `${name}=${formatValue(String(values[name]))}`)
      .join(", ");
  } catch {
    return "";
  }
}

/** 因子值保留 6 位有效数字；空值显示 "-"。 */
export function formatValue(raw: string | undefined) {
  if (raw === undefined || raw === "") return "-";
  const value = Number(raw);
  if (!Number.isFinite(value)) return raw;
  return Number.isInteger(value) ? String(value) : String(Number(value.toPrecision(6)));
}
