import { callControl } from '@/api/admin/http';
import type { RetInfo } from '@/api/storage/types';

export function callFactor<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  method: string,
  req: TReq,
) {
  return callControl<TReq, TRsp>('factormgr', method, req);
}
