import axios from "axios";
import { Message } from "@arco-design/web-vue";
import { gatewayOrigin } from "@/api/gateway";
import { isRetInfoSuccess } from "../ret-info";
import type { RetInfo } from "./types";
import { installSpaceAwareSignedClient } from "../admin/signed-client";

export const tradeServiceMap = {
  exchangeAccount: "trade_exchange_account",
  execution: "trade_execution",
  logicalAccount: "trade_logical_account"
} as const;

const tradeClient = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { "Content-Type": "application/json" }
});

function assertSuccess(retInfo?: RetInfo) {
  if (!retInfo) throw new Error("trade response missing ret_info");
  if (!isRetInfoSuccess(retInfo.code)) {
    throw new Error(retInfo.msg || `trade request failed: ${retInfo.code}`);
  }
}

export async function callTrade<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  group: keyof typeof tradeServiceMap,
  method: string,
  req: TReq
): Promise<TRsp> {
  const rsp = await tradeClient.post<TRsp>(`/api/admin/${tradeServiceMap[group]}/${method}`, req);
  assertSuccess(rsp.data.ret_info);
  return rsp.data;
}

installSpaceAwareSignedClient(tradeClient);

tradeClient.interceptors.response.use(
  rsp => {
    const trpcRet = rsp.headers?.["trpc-ret"] ?? rsp.headers?.["Trpc-Ret"];
    if (trpcRet !== undefined && trpcRet !== null && String(trpcRet) !== "0") {
      const funcRet = rsp.headers?.["trpc-func-ret"] ?? "";
      return Promise.reject(new Error(funcRet || `框架错误(${trpcRet})`));
    }
    return rsp;
  },
  error => {
    Message.error(error?.message || "Trade 请求失败");
    return Promise.reject(error);
  }
);
