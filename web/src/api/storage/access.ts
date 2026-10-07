import { callStorage } from "./http";
import type { RetInfo, RowFieldUpsert } from "./types";

export function upsertFields(rows: RowFieldUpsert[]) {
  return callStorage<{ rows: RowFieldUpsert[] }, { ret_info: RetInfo }>("UpsertFields", { rows });
}
