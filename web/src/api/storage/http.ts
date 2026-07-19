import axios from "axios";
import { Message } from "@arco-design/web-vue";
import { gatewayOrigin } from "@/api/gateway";
import { isRetInfoSuccess } from "../ret-info";
import { getStorageAuthInfo } from "./auth";
import type { RetInfo } from "./types";
import { installSpaceAwareSignedClient } from "../admin/signed-client";

const storageClient = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { "Content-Type": "application/json" }
});

function assertSuccess(retInfo?: RetInfo) {
  if (!retInfo) {
    const error = new Error("Storage 响应缺少 ret_info");
    Message.error(error.message);
    throw error;
  }
  if (!isRetInfoSuccess(retInfo.code)) {
    const error = new Error(retInfo.msg || `Storage 请求失败: ${retInfo.code}`);
    Message.error(error.message);
    throw error;
  }
}

async function callStorage<TReq extends object, TRsp extends { ret_info: RetInfo }>(method: string, req: TReq): Promise<TRsp> {
  const rsp = await storageClient.post<TRsp>(`/api/admin/storage/${method}`, {
    auth_info: getStorageAuthInfo(),
    ...req
  });
  assertSuccess(rsp.data.ret_info);
  return rsp.data;
}

export { callStorage };

installSpaceAwareSignedClient(storageClient);

storageClient.interceptors.response.use(
  rsp => {
    // 框架错误：HTTP 200 但 trpc-ret != 0，body 为空，错误信息在 header。
    const trpcRet = rsp.headers?.["trpc-ret"] ?? rsp.headers?.["Trpc-Ret"];
    if (trpcRet !== undefined && trpcRet !== null && String(trpcRet) !== "0") {
      const funcRet = rsp.headers?.["trpc-func-ret"] ?? "";
      return Promise.reject(new Error(funcRet || `框架错误(${trpcRet})`));
    }
    return rsp;
  },
  error => {
    Message.error(error?.message || "Storage 请求失败");
    return Promise.reject(error);
  }
);
