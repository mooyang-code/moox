import { callStorage } from "./http";
import type { Page, PageResult, RetInfo } from "./types";

/** 空间只登记在 Storage：管理台与 moox-cli 的增删改查都调 Storage 元数据接口。 */
export interface Space {
  space_id: string;
  name: string;
  description?: string;
  owner?: string;
  /** 市场，如 crypto、CN、HK、US。 */
  market?: string;
  /** 展示时区（IANA 名称）：页面上这个空间的时间按它显示。 */
  timezone?: string;
  /** active、disabled：停用后 Collector 不再采它。 */
  status: string;
  attributes?: Record<string, string>;
  created_at?: string;
  updated_at?: string;
}

export interface ListSpacesReq {
  owner?: string;
  status?: string;
  page?: Page;
}

type RetRsp = { ret_info: RetInfo };

export function listSpaces(req: ListSpacesReq = {}) {
  return callStorage<ListSpacesReq, RetRsp & { spaces?: Space[]; page_result?: PageResult }>("ListSpaces", req);
}

export async function createSpace(space: Space) {
  const rsp = await callStorage<{ space: Space }, RetRsp & { space: Space }>("CreateSpace", { space });
  return rsp.space;
}

export async function updateSpace(space: Space) {
  const rsp = await callStorage<{ space: Space }, RetRsp & { space: Space }>("UpdateSpace", { space });
  return rsp.space;
}

/** 删除空间：连同该空间的数据集、View 与全部行情、因子数据一起删除，不能恢复。 */
export async function deleteSpace(spaceId: string) {
  await callStorage<{ space_id: string }, RetRsp>("DeleteSpace", { space_id: spaceId });
}
