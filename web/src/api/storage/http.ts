import axios from "axios";
import { Message } from "@arco-design/web-vue";
import { gatewayOrigin } from "@/api/gateway";
import { isRetInfoSuccess } from "../ret-info";
import { getStorageAuthInfo } from "./auth";
import type { RetInfo } from "./types";
import { installSpaceAwareSignedClient, readPersistedAuth } from "../admin/signed-client";
import { StaleSpaceRequestError } from "../admin/stale-space";
import { explainTransportError } from "../network-error";
import { onSpaceGenerationChange, readSelectedSpaceId, selectedSpaceGeneration } from "../admin/space-header";

const storageClient = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { "Content-Type": "application/json" }
});

const STORAGE_READ_CACHE_TTL_MS = 3000;
const storageReadCache = new Map<string, { expiresAt: number; data: unknown }>();
const storageReadInflight = new Map<string, Promise<unknown>>();
let storageReadGeneration = 0;

// Only idempotent metadata/data reads are cached. Mutation responses invalidate
// the short cache so a newly created View or Dataset is visible immediately.
const storageReadMethods = new Set([
  "ListDataSources",
  "ListSubjects",
  "ListTags",
  "ListTagMembers",
  "GetDataset",
  "ListDatasets",
  "ListFieldGroups",
  "ListFields",
  "ListDatasetColumns",
  "GetView",
  "ListViews",
  "ListViewColumns",
  "ListDataNodes",
  "QueryTimeSeriesRows",
  "SearchRecordRows"
]);

function storageCacheKey(method: string, req: object): string {
  const auth = readPersistedAuth();
  const spaceID = readSelectedSpaceId();
  return JSON.stringify({
    method,
    req,
    sessionID: auth.sessionId || "",
    token: auth.token || "",
    spaceID: spaceID || "",
    // 切走又切回同一空间后，旧代次被丢弃的在途请求不能再被复用
    spaceGeneration: selectedSpaceGeneration()
  });
}

function invalidateStorageReadCache(): void {
  storageReadCache.clear();
  storageReadInflight.clear();
  storageReadGeneration += 1;
}

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

/** 调用选项：spaceId 显式指定请求所属空间（X-Space-Id），用于读取当前所选空间以外的元数据。 */
export interface StorageCallOptions {
  spaceId?: string;
}

async function callStorage<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  method: string,
  req: TReq,
  options: StorageCallOptions = {}
): Promise<TRsp> {
  const readOnly = storageReadMethods.has(method);
  if (!readOnly) {
    invalidateStorageReadCache();
  }
  const key = readOnly ? storageCacheKey(method, { ...req, explicit_space_id: options.spaceId || "" }) : "";
  if (readOnly) {
    const cached = storageReadCache.get(key);
    if (cached && cached.expiresAt > Date.now()) {
      return cached.data as TRsp;
    }
    storageReadCache.delete(key);
    const pending = storageReadInflight.get(key);
    if (pending) {
      return (await pending) as TRsp;
    }
  }

  const generation = storageReadGeneration;
  const request = (async () => {
    const url = `/api/admin/storage/${method}`;
    const body = { auth_info: getStorageAuthInfo(), ...req };
    // 显式空间的请求带上自己的 X-Space-Id，签名客户端不再注入当前所选空间。
    const rsp = options.spaceId
      ? await storageClient.post<TRsp>(url, body, { headers: { "X-Space-Id": options.spaceId } })
      : await storageClient.post<TRsp>(url, body);
    try {
      assertSuccess(rsp.data.ret_info);
    } catch (error) {
      // DeleteTag returns the blocking dataset references alongside ret_info.
      if (method === "DeleteTag" && error instanceof Error) {
        Object.assign(error, { response: { data: rsp.data } });
      }
      throw error;
    }
    if (readOnly && generation === storageReadGeneration) {
      storageReadCache.set(key, { expiresAt: Date.now() + STORAGE_READ_CACHE_TTL_MS, data: rsp.data });
    }
    return rsp.data;
  })();
  if (readOnly) {
    storageReadInflight.set(key, request);
    try {
      return await request;
    } finally {
      if (storageReadInflight.get(key) === request) {
        storageReadInflight.delete(key);
      }
    }
  }
  const response = await request;
  // A read that raced the mutation may have captured the pre-commit value;
  // invalidate again after success so it cannot survive the write boundary.
  invalidateStorageReadCache();
  return response;
}

export { callStorage };

installSpaceAwareSignedClient(storageClient);
// 切换空间后旧代次的缓存与在途条目不会再被命中，立即回收，避免随浏览累计。
onSpaceGenerationChange(invalidateStorageReadCache);

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
    if (error instanceof StaleSpaceRequestError) return Promise.reject(error);
    Message.error(explainTransportError(error, "Storage 请求失败"));
    return Promise.reject(error);
  }
);
