import type { AxiosRequestConfig } from "axios";
import { callControl } from "@/api/admin/http";
import type { RetInfo } from "@/api/storage/types";

export function callFactor<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  method: string,
  req: TReq,
  config?: AxiosRequestConfig
) {
  return callControl<TReq, TRsp>("factor-mgr", method, req, config);
}
