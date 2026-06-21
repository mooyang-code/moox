import axios from 'axios';
import { Message } from '@arco-design/web-vue';
import { isRetInfoSuccess } from '../ret-info';
import { getStorageAuthInfo } from './auth';
import type { RetInfo } from './types';

const storageClient = axios.create({
  baseURL: '',
  timeout: 30000,
  headers: { 'Content-Type': 'application/json' },
});

function assertSuccess(retInfo?: RetInfo) {
  if (!retInfo) {
    throw new Error('storage response missing ret_info');
  }
  if (!isRetInfoSuccess(retInfo.code)) {
    throw new Error(retInfo.msg || `storage request failed: ${retInfo.code}`);
  }
}

async function callStorage<TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  group: 'metadata' | 'access' | 'view',
  method: string,
  req: TReq,
): Promise<TRsp> {
  const rsp = await storageClient.post<TRsp>(`/api/storage/${group}/${method}`, {
    auth_info: getStorageAuthInfo(),
    ...req,
  });
  assertSuccess(rsp.data.ret_info);
  return rsp.data;
}

export const callMetadata = <TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  method: string,
  req: TReq,
) => callStorage<TReq, TRsp>('metadata', method, req);

export const callAccess = <TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  method: string,
  req: TReq,
) => callStorage<TReq, TRsp>('access', method, req);

export const callView = <TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  method: string,
  req: TReq,
) => callStorage<TReq, TRsp>('view', method, req);

storageClient.interceptors.response.use(
  (rsp) => rsp,
  (error) => {
    Message.error(error?.message || 'Storage 请求失败');
    return Promise.reject(error);
  },
);
