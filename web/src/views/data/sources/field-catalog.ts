import type { ExtraFieldOption, OutputFieldOption } from "@/api/collector";
import type { Dataset, DatasetColumn } from "@/api/storage/types";
import { isTimeSeriesDataKind, normalizeFieldValueType, type CanonicalFieldValueType } from "@/views/data/shared/metadata-utils";
import { marketLabel } from "@/views/data/shared/series-market";
import { extraFieldProviderName } from "./extra-field-sources";

export type FieldCatalogCategory = "base" | "extra" | "dataset";
export type FieldLandedFilter = "" | "landed" | "spec_only";

export const UNGROUPED_LABEL = "未分组";

/** 左侧一级分组的固定业务展示顺序；未列出的自定义分组排在「未分组」之前。 */
const GROUP_ORDER = ["行情价格", "成交估值", "合约指标", "复权", "行业", "财务数据", "标识信息"];

export interface FieldExtraScope {
  provider_id: string;
  provider_name: string;
  instrument_types: string[];
  frequencies: string[];
}

export interface FieldDatasetUsage {
  dataset_id: string;
  dataset_name: string;
  data_kind: "time_series" | "record";
  freq: string;
  dataset_status: string;
  column_status: string;
  required: boolean;
  aliases: string[];
}

export interface CatalogFieldItem {
  /** 规范化后的小写字段 ID，同时作主键与搜索/排序键 */
  field_id: string;
  display_name: string;
  category: FieldCatalogCategory;
  group: string;
  value_type: CanonicalFieldValueType | "";
  fill: string;
  extra_scopes: FieldExtraScope[];
  datasets: FieldDatasetUsage[];
  aliases: string[];
  /** 原始排序权重：保持 Collector 规格声明顺序 */
  order: number;
}

export interface FieldCatalogFilter {
  group: string;
  keyword: string;
  category: FieldCatalogCategory | "";
  valueType: CanonicalFieldValueType | "";
  landed: FieldLandedFilter;
  provider: string;
}

export interface FieldGroupItem {
  key: string;
  label: string;
  count: number;
}

export interface DatasetColumnBundle {
  dataset: Dataset;
  columns: DatasetColumn[];
}

type RouteQuery = Record<string, unknown>;

const queryText = (value: unknown) => (Array.isArray(value) ? String(value[0] ?? "") : String(value ?? "")).trim();

export function fieldTabQueryFromRoute(query: RouteQuery): FieldCatalogFilter {
  const category = queryText(query.category);
  const landed = queryText(query.landed);
  return {
    group: queryText(query.group),
    keyword: queryText(query.keyword),
    category: category === "base" || category === "extra" || category === "dataset" ? category : "",
    valueType: normalizeFieldValueType(queryText(query.value_type)),
    landed: landed === "landed" || landed === "spec_only" ? landed : "",
    provider: queryText(query.provider)
  };
}

export function fieldTabQueryToRoute(filter: FieldCatalogFilter): Record<string, string> {
  const query: Record<string, string> = {};
  if (filter.group) query.group = filter.group;
  if (filter.keyword.trim()) query.keyword = filter.keyword.trim();
  if (filter.category) query.category = filter.category;
  if (filter.valueType) query.value_type = filter.valueType;
  if (filter.landed) query.landed = filter.landed;
  if (filter.provider) query.provider = filter.provider;
  return query;
}

function normalizeGroup(group?: string): string {
  const trimmed = (group || "").trim();
  return trimmed || UNGROUPED_LABEL;
}

function addUnique(list: string[], value: string) {
  const trimmed = value.trim();
  if (trimmed && !list.includes(trimmed)) list.push(trimmed);
}

/**
 * 归并当前空间的全部原始数据字段：
 * - S1: Collector K 线基础输出字段（base_output_fields）
 * - S2: Collector 当前空间 K 线附加字段（extra_fields，按 market_id === spaceId 过滤）
 * - S3: Storage 当前空间非因子数据集列（含记录型财报、采集结果、手工/导入列）
 *
 * 按小写 field_id 去重归并，元信息优先级：基础规格 > 附加规格 > 数据集列；
 * 同一字段落地在多个数据集时全部记入 datasets，不产生重复行。
 */
export function mergeFieldCatalog(input: {
  spaceId: string;
  baseFields?: ReadonlyArray<OutputFieldOption>;
  extraFields?: ReadonlyArray<ExtraFieldOption>;
  datasetBundles?: ReadonlyArray<DatasetColumnBundle>;
}): CatalogFieldItem[] {
  const space = input.spaceId.trim().toLowerCase();
  const byId = new Map<string, CatalogFieldItem>();
  let order = 0;

  // S1: K 线基础输出字段
  for (const field of input.baseFields || []) {
    const id = (field.field_id || "").trim().toLowerCase();
    if (!id || byId.has(id)) continue;
    byId.set(id, {
      field_id: id,
      display_name: field.display_name?.trim() || id,
      category: "base",
      group: normalizeGroup(field.group),
      value_type: normalizeFieldValueType(field.value_type),
      fill: "",
      extra_scopes: [],
      datasets: [],
      aliases: [],
      order: order++
    });
  }

  // S2: 当前空间的 K 线附加字段（同字段可能出现在多个品种/提供方）
  for (const option of input.extraFields || []) {
    if ((option.market_id || "").trim().toLowerCase() !== space) continue;
    const id = (option.field_id || "").trim().toLowerCase();
    if (!id) continue;
    let item = byId.get(id);
    if (!item) {
      item = {
        field_id: id,
        display_name: option.display_name?.trim() || id,
        category: "extra",
        group: normalizeGroup(option.group),
        value_type: normalizeFieldValueType(option.value_type),
        fill: (option.fill || "").trim(),
        extra_scopes: [],
        datasets: [],
        aliases: [],
        order: order++
      };
      byId.set(id, item);
    } else {
      if (!item.value_type) item.value_type = normalizeFieldValueType(option.value_type);
      if (!item.fill && option.fill) item.fill = option.fill.trim();
    }
    const providerId = (option.provider_id || "").trim();
    if (providerId) {
      const normalizedProvider = providerId.toLowerCase();
      let scope = item.extra_scopes.find(s => s.provider_id.toLowerCase() === normalizedProvider);
      if (!scope) {
        scope = {
          provider_id: providerId,
          provider_name: extraFieldProviderName(providerId),
          instrument_types: [],
          frequencies: []
        };
        item.extra_scopes.push(scope);
      }
      addUnique(scope.instrument_types, option.instrument_type || "");
      for (const freq of option.frequencies || []) addUnique(scope.frequencies, freq);
    }
  }

  // S3: 当前空间非因子数据集的列（记录型、采集结果、手工/导入）；仅来自 S3 时优先取首个 active 列的元信息
  const datasetMetaFromActive = new Set<string>();
  for (const bundle of input.datasetBundles || []) {
    const dataset = bundle.dataset;
    const dataKind: "time_series" | "record" = isTimeSeriesDataKind(dataset.data_kind) ? "time_series" : "record";
    for (const column of bundle.columns || []) {
      const id = (column.column_name || "").trim().toLowerCase();
      if (!id) continue;
      const columnType = normalizeFieldValueType(column.value_type);
      const columnFill = (column.attributes?.fill || "").trim();
      const columnStatus = (column.status || "active").trim();
      const isActiveColumn = columnStatus.toLowerCase() === "active";
      const aliases = (column.aliases || []).map(a => a.trim()).filter(Boolean);
      let item = byId.get(id);
      if (!item) {
        item = {
          field_id: id,
          display_name: column.display_name?.trim() || id,
          category: "dataset",
          group: normalizeGroup(column.group),
          value_type: columnType,
          fill: columnFill,
          extra_scopes: [],
          datasets: [],
          aliases: [...aliases],
          order: order++
        };
        byId.set(id, item);
        if (isActiveColumn) datasetMetaFromActive.add(id);
      } else {
        if (item.category === "dataset" && isActiveColumn && !datasetMetaFromActive.has(id)) {
          if (column.display_name?.trim()) item.display_name = column.display_name.trim();
          if (column.group?.trim()) item.group = column.group.trim();
          if (columnType) item.value_type = columnType;
          if (columnFill) item.fill = columnFill;
          datasetMetaFromActive.add(id);
        } else {
          if (!item.value_type && columnType) item.value_type = columnType;
          if (!item.fill && columnFill) item.fill = columnFill;
          if (item.category === "dataset" && item.display_name === item.field_id && column.display_name?.trim()) {
            item.display_name = column.display_name.trim();
          }
          if (item.category === "dataset" && item.group === UNGROUPED_LABEL && column.group?.trim()) {
            item.group = column.group.trim();
          }
        }
        for (const alias of aliases) addUnique(item.aliases, alias);
      }
      if (!item.datasets.some(d => d.dataset_id === dataset.dataset_id)) {
        item.datasets.push({
          dataset_id: dataset.dataset_id,
          dataset_name: dataset.name?.trim() || dataset.dataset_id,
          data_kind: dataKind,
          freq: (dataset.freq || "").trim(),
          dataset_status: (dataset.status || "active").trim(),
          column_status: columnStatus,
          required: Boolean(column.required),
          aliases
        });
      }
    }
  }

  return [...byId.values()].sort((a, b) => a.order - b.order);
}

/** 按固定业务顺序排列分组；未预置的自定义分组按字典序排在「未分组」之前。 */
export function compareGroupNames(a: string, b: string): number {
  if (a === b) return 0;
  if (a === UNGROUPED_LABEL) return 1;
  if (b === UNGROUPED_LABEL) return -1;
  const ia = GROUP_ORDER.indexOf(a);
  const ib = GROUP_ORDER.indexOf(b);
  if (ia !== -1 && ib !== -1) return ia - ib;
  if (ia !== -1) return -1;
  if (ib !== -1) return 1;
  return a.localeCompare(b, "zh-Hans-CN");
}

/** 按全量字段抽出左侧分组目录，并用筛选后（不含 group）的字段计算每组计数。 */
export function buildFieldGroupItems(
  allItems: ReadonlyArray<CatalogFieldItem>,
  filteredWithoutGroup: ReadonlyArray<CatalogFieldItem>
): FieldGroupItem[] {
  const allGroups = new Set<string>();
  for (const item of allItems) allGroups.add(item.group);
  const counts = new Map<string, number>();
  for (const item of filteredWithoutGroup) counts.set(item.group, (counts.get(item.group) || 0) + 1);
  return [...allGroups].sort(compareGroupNames).map(group => ({
    key: group,
    label: group,
    count: counts.get(group) || 0
  }));
}

export function filterFieldCatalog(items: ReadonlyArray<CatalogFieldItem>, filter: FieldCatalogFilter): CatalogFieldItem[] {
  const keyword = filter.keyword.trim().toLowerCase();
  const provider = filter.provider.trim().toLowerCase();
  return items.filter(item => {
    if (filter.group && item.group !== filter.group) return false;
    if (filter.category && item.category !== filter.category) return false;
    if (filter.valueType && item.value_type !== filter.valueType) return false;
    if (filter.landed === "landed" && item.datasets.length === 0) return false;
    if (filter.landed === "spec_only" && item.datasets.length > 0) return false;
    if (provider && !item.extra_scopes.some(scope => scope.provider_id.toLowerCase() === provider)) return false;
    if (!keyword) return true;
    const haystacks = [
      item.field_id,
      item.display_name,
      item.group,
      ...item.aliases,
      ...item.extra_scopes.flatMap(s => [s.provider_id, s.provider_name, ...s.frequencies]),
      ...item.datasets.flatMap(d => [d.dataset_id, d.dataset_name])
    ];
    return haystacks.some(text => text.toLowerCase().includes(keyword));
  });
}

export function fieldCategoryLabel(category: FieldCatalogCategory): string {
  if (category === "base") return "基础";
  if (category === "extra") return "附加";
  return "数据集";
}

export function fieldCategoryColor(category: FieldCatalogCategory): string {
  if (category === "base") return "arcoblue";
  if (category === "extra") return "green";
  return "purple";
}

/** 生成表格「来源与适用范围」列的简洁说明文本。 */
export function fieldSourceSummary(item: CatalogFieldItem): string {
  if (item.category === "base") return "K 线基础输出";
  if (item.category === "extra" && item.extra_scopes.length) {
    return item.extra_scopes
      .map(scope => {
        const parts = [scope.provider_name];
        const instruments = scope.instrument_types.map(marketLabel).filter(Boolean).join("/");
        if (instruments) parts.push(instruments);
        if (scope.frequencies.length) parts.push(scope.frequencies.join("/"));
        return parts.join(" · ");
      })
      .join("；");
  }
  if (item.datasets.length) {
    const first = item.datasets[0];
    const kind = first.data_kind === "record" ? "记录型" : first.freq ? `时序 ${first.freq}` : "时序";
    return item.datasets.length === 1 ? `${kind} · ${first.dataset_name}` : `${kind} · ${first.dataset_name} 等`;
  }
  return "—";
}

/** 有限并发执行异步映射，按原序返回全部 settled 结果。 */
export async function mapWithConcurrency<T, R>(
  items: ReadonlyArray<T>,
  limit: number,
  worker: (item: T) => Promise<R>
): Promise<Array<PromiseSettledResult<R>>> {
  const results = new Array<PromiseSettledResult<R>>(items.length);
  const concurrency = Math.max(1, Math.min(limit, items.length || 1));
  let cursor = 0;
  async function run() {
    while (cursor < items.length) {
      const index = cursor++;
      try {
        const value = await worker(items[index]);
        results[index] = { status: "fulfilled", value };
      } catch (reason) {
        results[index] = { status: "rejected", reason };
      }
    }
  }
  await Promise.all(Array.from({ length: concurrency }, run));
  return results;
}
