import axios from 'axios';
import { Message } from '@arco-design/web-vue';
import { isRetInfoSuccess } from '../ret-info';
import type { RetInfo } from './types';

// trade 服务 ID → 网关路径映射（与 admin/config/gateway.yaml 对齐）
const tradeServiceMap: Record<string, string> = {
  account: 'trade_account',
  balance: 'trade_balance',
  fund: 'trade_fund',
  apikey: 'trade_apikey',
  channel: 'trade_channel',
  tradeop: 'trade_tradeop',
  order: 'trade_order',
  tradeq: 'trade_tradeq',
  position: 'trade_position',
};

const tradeClient = axios.create({
  baseURL: '',
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

function assertSuccess(retInfo?: RetInfo) {
  if (!retInfo) {
    throw new Error('trade response missing ret_info');
  }
  if (!isRetInfoSuccess(retInfo.code)) {
    throw new Error(retInfo.msg || `trade request failed: ${retInfo.code}`);
  }
}

/**
 * 调用 trade 微服务。
 * @param group  服务域: account/balance/fund/apikey/channel/tradeop/order/tradeq/position
 * @param method RPC 方法名，如 ListAccounts
 * @param req    请求体
 */
export async function callTrade<TReq extends object, TRsp extends { ret_info?: RetInfo }>(
  group: keyof typeof tradeServiceMap,
  method: string,
  req: TReq,
): Promise<TRsp> {
  const serviceId = tradeServiceMap[group];
  const rsp = await tradeClient.post<TRsp>(`/api/admin/${serviceId}/${method}`, req);
  assertSuccess(rsp.data.ret_info);
  return rsp.data;
}

tradeClient.interceptors.request.use((config) => {
  const token = readAccessToken();
  if (token) {
    config.headers.Authorization = token;
    config.headers['X-Access-Token'] = token;
  }
  return config;
});

tradeClient.interceptors.response.use(
  (rsp) => {
    const trpcRet = rsp.headers?.['trpc-ret'] ?? rsp.headers?.['Trpc-Ret'];
    if (trpcRet !== undefined && trpcRet !== null && String(trpcRet) !== '0') {
      const funcRet = rsp.headers?.['trpc-func-ret'] ?? '';
      return Promise.reject(new Error(funcRet || `框架错误(${trpcRet})`));
    }
    return rsp;
  },
  (error) => {
    Message.error(error?.message || 'Trade 请求失败');
    return Promise.reject(error);
  },
);
