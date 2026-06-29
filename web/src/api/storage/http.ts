import axios from 'axios';
import { Message } from '@arco-design/web-vue';
import { gatewayOrigin } from '@/api/gateway';
import { isRetInfoSuccess } from '../ret-info';
import { getStorageAuthInfo } from './auth';
import type { RetInfo } from './types';

const storageClient = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { 'Content-Type': 'application/json' },
});

function readAccessToken(): string {
  try {
    const raw = localStorage.getItem('user-info');
    if (!raw) return '';
    const parsed = JSON.parse(raw) as { token?: string };
    return parsed.token || '';
  } catch {
    return '';
  }
}

function storageServiceID(group: 'metadata' | 'access' | 'view') {
  const serviceIDs = {
    metadata: 'storage_metadata',
    access: 'storage_access',
    view: 'storage_view',
  } as const;
  return serviceIDs[group];
}

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
  const rsp = await storageClient.post<TRsp>(`/api/admin/${storageServiceID(group)}/${method}`, {
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

storageClient.interceptors.request.use((config) => {
  const token = readAccessToken();
  if (token) {
    config.headers.Authorization = token;
    config.headers['X-Access-Token'] = token;
  }
  return config;
});

storageClient.interceptors.response.use(
  (rsp) => {
    // 框架错误：HTTP 200 但 trpc-ret != 0，body 为空，错误信息在 header。
    const trpcRet = rsp.headers?.['trpc-ret'] ?? rsp.headers?.['Trpc-Ret'];
    if (trpcRet !== undefined && trpcRet !== null && String(trpcRet) !== '0') {
      const funcRet = rsp.headers?.['trpc-func-ret'] ?? '';
      return Promise.reject(new Error(funcRet || `框架错误(${trpcRet})`));
    }
    return rsp;
  },
  (error) => {
    Message.error(error?.message || 'Storage 请求失败');
    return Promise.reject(error);
  },
);
