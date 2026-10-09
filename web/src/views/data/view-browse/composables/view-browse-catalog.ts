import { getDataset, getView } from "@/api/storage/metadata";
import type { Dataset, View } from "@/api/storage/types";

export async function loadTargetedViewCatalog(spaceId: string, viewIds: string[]) {
  const uniqueViewIds = [...new Set(viewIds.map(id => id.trim()).filter(Boolean))];
  if (!spaceId.trim() || uniqueViewIds.length === 0) {
    return { views: [] as View[], datasets: [] as Dataset[] };
  }

  const viewResults = await Promise.all(uniqueViewIds.map(view_id => getView({ space_id: spaceId, view_id })));
  const views = viewResults.map(result => result.view);
  if (
    views.some((view, index) => !view || view.view_id.trim() !== uniqueViewIds[index] || view.space_id.trim() !== spaceId.trim())
  ) {
    throw new Error("采集结果视图身份与请求不一致");
  }
  const datasetIds = [...new Set(views.map(view => view.dataset_id.trim()).filter(Boolean))];
  if (datasetIds.length !== new Set(views.map(view => view.dataset_id.trim())).size) {
    throw new Error("采集结果视图缺少数据集身份");
  }
  const datasetResults = await Promise.all(datasetIds.map(dataset_id => getDataset({ space_id: spaceId, dataset_id })));
  const datasets = datasetResults.map(result => result.dataset);
  if (
    datasets.some(
      (dataset, index) =>
        !dataset || dataset.dataset_id.trim() !== datasetIds[index] || dataset.space_id.trim() !== spaceId.trim()
    )
  ) {
    throw new Error("采集结果视图对应的数据集身份与请求不一致");
  }

  return {
    views,
    datasets
  };
}
