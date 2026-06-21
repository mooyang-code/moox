import axios from 'axios';
import { Message } from '@arco-design/web-vue';
import type { ControlResponse } from './types';

const controlClient = axios.create({
  baseURL: '',
  timeout: 30000,
  headers: { 'Content-Type': 'application/json' },
});

function assertControlSuccess<T>(rsp: ControlResponse<T>): T {
  const code = rsp.code ?? 0;
  if (code !== 0 && code !== '0' && code !== 'SUCCESS') {
    throw new Error(rsp.message || rsp.msg || `control request failed: ${code}`);
  }
  return (rsp.data ?? rsp) as T;
}

export async function callControl<TReq extends object, TRsp>(
  service: string,
  method: string,
  req: TReq,
): Promise<TRsp> {
  const rsp = await controlClient.post<ControlResponse<TRsp>>(`/api/control/${service}/${method}`, req);
  return assertControlSuccess<TRsp>(rsp.data);
}

controlClient.interceptors.response.use(
  (rsp) => rsp,
  (error) => {
    Message.error(error?.message || 'Control 请求失败');
    return Promise.reject(error);
  },
);
