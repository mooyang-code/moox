import axios from 'axios';
import { Message } from '@arco-design/web-vue';
import { gatewayOrigin } from '@/api/gateway';
import { isRetInfoSuccess } from '../ret-info';
import type { ControlResponse } from './types';

const adminClient = axios.create({
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

function assertControlSuccess<T>(rsp: ControlResponse<T>): T {
  if (rsp.ret_info) {
    const retCode = rsp.ret_info.code;
    if (!isRetInfoSuccess(retCode)) {
      throw new Error(rsp.ret_info.msg || `control request failed: ${retCode}`);
    }
  }
  const code = rsp.code;
  if (code !== undefined && code !== null && !isRetInfoSuccess(code)) {
    throw new Error(rsp.message || rsp.msg || `control request failed: ${code}`);
  }
  return (rsp.data ?? rsp) as T;
}

export async function callControl<TReq extends object, TRsp>(
  service: string,
  method: string,
  req: TReq,
): Promise<TRsp> {
  const rsp = await adminClient.post<ControlResponse<TRsp>>(`/api/admin/${service}/${method}`, req);
  return assertControlSuccess<TRsp>(rsp.data);
}

adminClient.interceptors.request.use((config) => {
  const token = readAccessToken();
  if (token) {
    config.headers.Authorization = token;
    config.headers['X-Access-Token'] = token;
  }
  return config;
});

adminClient.interceptors.response.use(
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
    Message.error(error?.message || 'Control 请求失败');
    return Promise.reject(error);
  },
);
