import type { Dataset, View } from "@/api/storage/types";

export const ownerModules = ["collector", "factor", "trade", "manual", "system"] as const;
export type OwnerModule = (typeof ownerModules)[number];

export const datasetRoles = ["raw_collection", "factor_result", "import", "analysis", "manual"] as const;
export type DatasetRole = (typeof datasetRoles)[number];

export const viewRoles = ["collection_browse", "factor_result", "analysis", "manual"] as const;
export type ViewRole = (typeof viewRoles)[number];

export interface AttributionFilter {
  ownerModules?: OwnerModule[];
  datasetRoles?: DatasetRole[];
  viewRoles?: ViewRole[];
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

export function isLikelyFactorResultDataset(dataset: Dataset) {
  const attrs = dataset.attributes || {};
  return (
    clean(attrs.owner_module) === "factor" ||
    clean(attrs.dataset_role) === "factor_result" ||
    isLikelyFactorResultDatasetId(dataset.dataset_id)
  );
}

export function isLikelyFactorResultDatasetId(datasetId: string | undefined) {
  const normalized = clean(datasetId).toLowerCase();
  return normalized.endsWith("_factor") || /_f[0-9a-f]{4}$/.test(normalized);
}

export function viewMatchesAttribution(view: View, filter: AttributionFilter) {
  const attrs = view.attributes || {};
  const owner = clean(attrs.owner_module);
  const role = clean(attrs.view_role);
  if (!owner && filter.includeUnowned) {
    return true;
  }
  return includes(filter.ownerModules, owner) && includes(filter.viewRoles, role);
}
