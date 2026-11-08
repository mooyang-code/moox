import { callControl } from "@/api/admin/http";
import type { RetInfo } from "@/api/storage/types";

/** options.silentError 为真时失败不弹全局提示（后台轮询由页面自己显示失败）。 */
export function callFactor<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  method: string,
  req: TReq,
  options: { silentError?: boolean } = {}
) {
  return callControl<TReq, TRsp>("factor-mgr", method, req, options);
}
