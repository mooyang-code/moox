import type { Dataset, View } from "@/api/storage/types";

export const ownerModules = ["collector", "factor", "trade", "manual", "system"] as const;
export type OwnerModule = (typeof ownerModules)[number];

export const datasetRoles = ["raw_collection", "factor_result", "merged_factor", "import", "analysis", "manual"] as const;
export type DatasetRole = (typeof datasetRoles)[number];

export const viewRoles = ["collection_browse", "factor_result", "analysis", "manual"] as const;
export type ViewRole = (typeof viewRoles)[number];

export interface DatasetAttributionInput {
  ownerModule?: OwnerModule;
  datasetRole?: DatasetRole;
  managedBy?: string;
  sourceDatasetId?: string;
  sourceFreq?: string;
}

export interface ViewAttributionInput {
  ownerModule?: OwnerModule;
  viewRole?: ViewRole;
  managedBy?: string;
  primaryDatasetRole?: DatasetRole;
}

export interface AttributionFilter {
  ownerModules?: OwnerModule[];
  datasetRoles?: DatasetRole[];
  viewRoles?: ViewRole[];
  includeUnowned?: boolean;
}

function clean(value?: string) {
  return (value || "").trim();
}

function putIfPresent(target: Record<string, string>, key: string, value?: string) {
  const normalized = clean(value);
  if (normalized) {
    target[key] = normalized;
  }
}

function includes<T extends string>(values: readonly T[] | undefined, value: string) {
  return !values || values.length === 0 || values.includes(value as T);
}

export function mergeDatasetAttribution(attributes: Record<string, string> | undefined, input: DatasetAttributionInput) {
  const next = { ...(attributes || {}) };
  putIfPresent(next, "owner_module", input.ownerModule);
  putIfPresent(next, "dataset_role", input.datasetRole);
  putIfPresent(next, "managed_by", input.managedBy);
  putIfPresent(next, "source_dataset_id", input.sourceDatasetId);
  putIfPresent(next, "source_freq", input.sourceFreq);
  return next;
}

export function mergeViewAttribution(attributes: Record<string, string> | undefined, input: ViewAttributionInput) {
  const next = { ...(attributes || {}) };
  putIfPresent(next, "owner_module", input.ownerModule);
  putIfPresent(next, "view_role", input.viewRole);
  putIfPresent(next, "managed_by", input.managedBy);
  putIfPresent(next, "primary_dataset_role", input.primaryDatasetRole);
  return next;
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

export function fieldOwnershipLabel(column: { origin_type?: string | number; attributes?: Record<string, string> }) {
  const kind = clean(column.attributes?.field_kind);
  const origin = String(column.origin_type || "").toLowerCase();
  if (kind === "factor_output" || origin.includes("factor")) {
    return "因子输出";
  }
  return "基础字段";
}

export function isMergedFactorDataset(dataset: Dataset) {
  const attrs = dataset.attributes || {};
  return dataset.dataset_id.startsWith("mdataset_") || clean(attrs.dataset_role) === "merged_factor";
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
