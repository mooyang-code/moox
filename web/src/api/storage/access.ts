import { callStorage } from "./http";
import type { RetInfo, RowFieldUpsert } from "./types";

/** 写入一个数据集的行：每行的 space_id、dataset_id 必须与参数一致。 */
export function upsertFields(spaceId: string, datasetId: string, rows: RowFieldUpsert[]) {
  return callStorage<{ space_id: string; dataset_id: string; rows: RowFieldUpsert[] }, { ret_info: RetInfo }>("UpsertFields", {
    space_id: spaceId,
    dataset_id: datasetId,
    rows
  });
}
