import { createView, listViews } from "@/api/storage/metadata";
import type { Dataset, View } from "@/api/storage/types";
import { mergeViewAttribution, type OwnerModule, type ViewRole } from "@/views/data/shared/module-attribution";

export function defaultViewIdForDataset(datasetId: string) {
  const rest = datasetId.replace(/^(?:mdataset_|dataset_)/, "").replace(/[^a-z0-9_]/g, "");
  const candidate = `view_${rest}`;
  if (candidate.length <= 30 && /^view_[a-z][a-z0-9_]*$/.test(candidate)) {
    return candidate;
  }
  const clipped = rest.replace(/^[^a-z]+/, "").slice(0, 25);
  return clipped ? `view_${clipped}` : "view_default";
}

export function buildDefaultView(
  dataset: Pick<Dataset, "space_id" | "dataset_id" | "name" | "keep_duration">,
  input: { ownerModule?: OwnerModule; viewRole?: ViewRole; managedBy?: string }
): View {
  return {
    space_id: dataset.space_id,
    view_id: defaultViewIdForDataset(dataset.dataset_id),
    name: dataset.name || "默认索引",
    description: "",
    dataset_id: dataset.dataset_id,
    grain_keys: [],
    filter_json: "{}",
    engine: "",
    retention_window: dataset.keep_duration || "",
    status: "active",
    attributes: mergeViewAttribution({}, input)
  };
}

export async function ensureDefaultView(
  dataset: Pick<Dataset, "space_id" | "dataset_id" | "name" | "keep_duration">,
  input: { ownerModule?: OwnerModule; viewRole?: ViewRole; managedBy?: string }
) {
  const existing = await listViews({
    space_id: dataset.space_id,
    dataset_id: dataset.dataset_id,
    page: { page: 1, size: 1 }
  });
  if ((existing.views || []).length > 0) {
    return existing.views[0];
  }
  return createView(buildDefaultView(dataset, input));
}
