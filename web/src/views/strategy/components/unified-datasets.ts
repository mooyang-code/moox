import { listAllDatasets } from "@/api/storage/metadata";
import { isUnifiedMarketType } from "@/views/data/shared/series-market";

/**
 * 空间里统一数据集（market_type 为 multi）的数据集 ID。独立的回放发起与试算只按市场登记任务，统一数据集上的任务要登记为 multi
 * （从实例发起的回放不受影响），所以用它把这类任务从可选项里去掉。读取失败时返回空集合：不过滤，后端校验会给出明确提示。
 */
export async function loadUnifiedDatasetIds(spaceId: string): Promise<ReadonlySet<string>> {
  try {
    const datasets = await listAllDatasets(spaceId);
    return new Set(datasets.filter(item => isUnifiedMarketType(item.attributes?.market_type)).map(item => item.dataset_id));
  } catch {
    return new Set();
  }
}
