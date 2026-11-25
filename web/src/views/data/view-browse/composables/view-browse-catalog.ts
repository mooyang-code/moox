import { getDataset } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";

/** 按 ID 读取调用方指定的数据集（每个数据集恰好有一个查询索引）；任何一个读不到或身份不符都报错，不返回半份目录。 */
export async function loadTargetedDatasets(spaceId: string, datasetIds: string[]): Promise<Dataset[]> {
  const uniqueIds = [...new Set(datasetIds.map(id => id.trim()).filter(Boolean))];
  if (!spaceId.trim() || uniqueIds.length === 0) return [];

  const results = await Promise.all(uniqueIds.map(dataset_id => getDataset({ space_id: spaceId, dataset_id })));
  const datasets = results.map(result => result.dataset);
  if (
    datasets.some(
      (dataset, index) => !dataset || dataset.dataset_id.trim() !== uniqueIds[index] || dataset.space_id.trim() !== spaceId.trim()
    )
  ) {
    throw new Error("数据集身份与请求不一致");
  }
  return datasets;
}
