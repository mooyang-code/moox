import axios from 'axios';
import type { AxiosRequestConfig } from 'axios';
import { Message } from '@arco-design/web-vue';
import { gatewayOrigin } from '@/api/gateway';
import { isRetInfoSuccess } from '../ret-info';
import type { ControlResponse } from './types';
import { withSelectedSpaceHeader } from './space-header';

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

function readAccessTokenFromConfig(config?: AxiosRequestConfig): string {
  const headers = (config?.headers || {}) as Record<string, string | undefined>;
  return headers.Authorization || headers['X-Access-Token'] || '';
}

function assertControlSuccess<T>(rsp: ControlResponse<T>): T {
  if (!rsp.ret_info) {
    throw new Error('control response missing ret_info');
  }
  const retCode = rsp.ret_info.code;
  if (!isRetInfoSuccess(retCode)) {
    throw new Error(rsp.ret_info.msg || `control request failed: ${retCode}`);
  }
  return rsp as T;
}

export async function callControl<TReq extends object, TRsp>(
  service: string,
  method: string,
  req: TReq,
  config?: AxiosRequestConfig,
): Promise<TRsp> {
  if (!readAccessToken() && !readAccessTokenFromConfig(config)) {
    throw new Error('未登录或登录态已失效，请重新登录后再访问管理接口');
  }
  const rsp = await adminClient.post<ControlResponse<TRsp>>(`/api/admin/${service}/${method}`, req, config);
  return assertControlSuccess<TRsp>(rsp.data);
}

adminClient.interceptors.request.use((config) => {
  const token = readAccessToken();
  const headers = withSelectedSpaceHeader((config.headers || {}) as Record<string, string | undefined>);
  config.headers = headers as typeof config.headers;
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
    const data = error?.response?.data as ControlResponse<unknown> | undefined;
    const message = data?.ret_info?.msg || error?.message || 'Control 请求失败';
    Message.error(message);
    return Promise.reject(new Error(message));
  },
);
