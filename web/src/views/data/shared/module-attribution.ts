import type { Dataset } from "@/api/storage/types";

export const ownerModules = ["collector", "factor", "trade", "manual", "system"] as const;
export type OwnerModule = (typeof ownerModules)[number];

export const datasetRoles = ["raw_collection", "kline_resample_result", "factor_result"] as const;
export type DatasetRole = (typeof datasetRoles)[number];

export interface AttributionFilter {
  ownerModules?: OwnerModule[];
  datasetRoles?: DatasetRole[];
  includeUnowned?: boolean;
}

function clean(value?: string) {
  return (value || "").trim();
}

function includes<T extends string>(values: readonly T[] | undefined, value: string) {
  return !values || values.length === 0 || values.includes(value as T);
}

export function datasetMatchesAttribution(dataset: Dataset, filter: AttributionFilter) {
  const attrs = dataset.attributes || {};
  const owner = clean(attrs.owner_module);
  const role = clean(attrs.dataset_role);
  if (!owner && filter.includeUnowned) {
    return true;
  }
  return includes(filter.ownerModules, owner) && includes(filter.datasetRoles, role);
}

/** 仅按后端写入的数据集归属属性判断是否为因子结果数据集，不再按数据集 ID 后缀猜测。 */
export function isFactorResultDataset(dataset: Dataset) {
  const attrs = dataset.attributes || {};
  return clean(attrs.owner_module) === "factor" || clean(attrs.dataset_role) === "factor_result";
}
