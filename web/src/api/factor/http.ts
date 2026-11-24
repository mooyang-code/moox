import { call, type CallOptions } from "@/api/http";
import type { RetInfo } from "@/api/storage/types";

/** 因子管理端（factor-mgr）的调用。 */
export function callFactor<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  method: string,
  req: TReq,
  options?: CallOptions
) {
  return call<TReq, TRsp>("factor-mgr", method, req, options);
}
