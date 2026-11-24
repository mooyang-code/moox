import axios from "axios";
import type { AxiosRequestConfig } from "axios";
import { Message } from "@arco-design/web-vue";
import { gatewayOrigin } from "@/api/gateway";
import pinia from "@/store/index";
import { useUserInfoStore } from "@/store/modules/user-info";
import { isRetInfoSuccess } from "./ret-info";
import { installSpaceAwareClient } from "./admin/space-client";
import { onSpaceGenerationChange, readSelectedSpaceId, selectedSpaceGeneration } from "./admin/space-header";
import { AuthSessionExpiredError } from "./admin/auth-errors";
import { StaleSpaceRequestError } from "./admin/stale-space";
import { explainTransportError } from "./network-error";

/**
 * 管理台唯一的请求入口：call(服务, 方法, 请求体) 发到 /api/admin/<服务>/<方法>。
 * 规则：请求层不弹提示，只把失败抛成 RequestError（会话失效抛 AuthSessionExpiredError，切换空间造成的取消抛
 * StaleSpaceRequestError）；要不要提示、在哪提示由页面决定，页面要提示时用 reportError。
 */

/** 后端响应里的 ret_info。 */
export interface RetInfo {
  code?: number | string;
  msg?: string;
}

/** 失败的请求：业务失败带着完整的响应体（ret_info 与同时返回的其他字段），网络与 HTTP 失败只有提示文字。 */
export class RequestError extends Error {
  constructor(
    message: string,
    // 响应体：调用方按需读取 ret_info 之外随错误返回的字段（例如删除标签时阻止删除的数据集）
    readonly response?: { ret_info?: RetInfo } & Record<string, any>
  ) {
    super(message);
    this.name = "RequestError";
  }

  /** 错误码；网络与 HTTP 失败没有错误码。 */
  get code(): number | string | undefined {
    return this.response?.ret_info?.code;
  }

  /** 错误码是否是给定的几种之一；后端返回的错误码可能是数字，也可能是枚举名。 */
  hasCode(...codes: readonly (number | string)[]): boolean {
    const code = this.code;
    return code !== undefined && codes.some(item => String(item) === String(code));
  }
}

export interface CallOptions {
  /** 显式指定请求所属空间（X-Space-Id），用于读取当前所选空间以外的数据。 */
  spaceId?: string;
  /** 超时（毫秒），默认 30 秒。 */
  timeout?: number;
  /** 登录探测（登录、取用户信息、退出）：401 只说明没有登录，不跳登录页。 */
  authProbe?: boolean;
}

const client = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { "Content-Type": "application/json" }
});

// serverClockOffset 是服务端时钟减浏览器时钟（毫秒），取自最近一次响应的 Date 头；还没有读到时为 null。
let serverClockOffset: number | null = null;

/** 按最近一次响应的 Date 头换算的服务端当前时间（毫秒）；还没有读到 Date 头时返回 null。浏览器时钟可能与服务端不一致，
 *  比较服务端给出的时间戳时用它。 */
export function serverNow(): number | null {
  return serverClockOffset === null ? null : Date.now() + serverClockOffset;
}

/** 记录一次响应的 Date 头（服务端时钟减浏览器时钟）；读不出时间时保持原值。 */
export function recordServerDate(header: unknown) {
  const date = Date.parse(String(header ?? ""));
  if (Number.isFinite(date)) serverClockOffset = date - Date.now();
}

const reportedErrors = new WeakSet<object>();

/** 页面用来提示一次失败：同一个错误对象只提示一次，切换空间造成的取消不提示。 */
export function reportError(error: unknown, fallback = "请求失败") {
  if (error instanceof StaleSpaceRequestError) return;
  if (typeof error === "object" && error !== null && reportedErrors.has(error)) return;
  Message.error(error instanceof Error && error.message ? error.message : String(error || fallback));
  if (typeof error === "object" && error !== null) reportedErrors.add(error);
}

/** 失败的提示文字：错误对象的 message，没有时用 fallback。 */
export function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

// ---- Storage 的短时读缓存与在途合并：只缓存幂等的元数据与数据读取，任何 Storage 写入都会清掉缓存。 ----

const STORAGE_READ_CACHE_TTL_MS = 3000;
const storageReadCache = new Map<string, { expiresAt: number; data: unknown }>();
const storageReadInflight = new Map<string, Promise<unknown>>();
let storageReadGeneration = 0;

const storageReadMethods = new Set([
  "ListSpaces",
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
  "QueryTimeSeriesRows",
  "SearchRecordRows",
  "GetViewState"
]);

function storageCacheKey(method: string, req: object, spaceId: string): string {
  return JSON.stringify({
    method,
    req,
    // 会话在 HttpOnly Cookie 里读不到，按当前登录的用户名区分缓存
    user: useUserInfoStore(pinia).account.user.userName || "",
    spaceId: spaceId || readSelectedSpaceId() || "",
    explicit: Boolean(spaceId),
    // 切走又切回同一空间后，旧代次被丢弃的在途请求不能再被复用
    spaceGeneration: selectedSpaceGeneration()
  });
}

function invalidateStorageReadCache(): void {
  storageReadCache.clear();
  storageReadInflight.clear();
  storageReadGeneration += 1;
}

// 切换空间后旧代次的缓存与在途条目不会再被命中，立即回收，避免随浏览累计。
onSpaceGenerationChange(invalidateStorageReadCache);

// ---- 发送 ----

const serviceLabels: Record<string, string> = { storage: "Storage", trade: "Trade" };

async function send<TRsp>(service: string, method: string, req: object, options: CallOptions): Promise<TRsp> {
  const config: AxiosRequestConfig = {};
  // 显式空间的请求带上自己的 X-Space-Id，签名客户端不再注入当前所选空间。
  if (options.spaceId) config.headers = { "X-Space-Id": options.spaceId };
  if (options.timeout) config.timeout = options.timeout;
  if (options.authProbe) config.authProbe = true;
  let rsp;
  try {
    rsp = await client.post<TRsp & { ret_info?: RetInfo }>(`/api/admin/${service}/${method}`, req, config);
  } catch (error) {
    // 切换空间造成的取消与会话失效保持原类型，调用方按类型处理
    if (error instanceof StaleSpaceRequestError) throw error;
    const data = (error as { response?: { data?: unknown; status?: number } })?.response?.data as
      | ({ ret_info?: RetInfo } & Record<string, unknown>)
      | undefined;
    const message = data?.ret_info?.msg || explainTransportError(error, `${serviceLabels[service] || "Control"} 请求失败`);
    if ((error as { response?: { status?: number } })?.response?.status === 401) throw new AuthSessionExpiredError(message);
    throw new RequestError(message, data?.ret_info ? data : undefined);
  }
  const body = rsp.data;
  const retInfo = body?.ret_info;
  if (!retInfo) throw new RequestError(`${service}/${method} 的响应缺少 ret_info`);
  if (!isRetInfoSuccess(retInfo.code)) {
    const message = retInfo.msg || `${service}/${method} 请求失败（错误码 ${retInfo.code}）`;
    throw new RequestError(message, body);
  }
  return body;
}

/**
 * 调用 /api/admin/<service>/<method>，返回整个响应体（含 ret_info）。失败时抛错、不提示，见文件开头的规则。
 * Storage 的读取（storageReadMethods）有 3 秒的读缓存并合并同一请求的在途调用。
 */
export async function call<TReq extends object, TRsp>(
  service: string,
  method: string,
  req: TReq,
  options: CallOptions = {}
): Promise<TRsp> {
  if (service !== "storage") return send<TRsp>(service, method, req, options);
  if (!storageReadMethods.has(method)) {
    invalidateStorageReadCache();
    const response = await send<TRsp>(service, method, req, options);
    // 与写入并发的读取可能拿到写入前的值：写入成功后再清一次，不让它跨过写入边界
    invalidateStorageReadCache();
    return response;
  }
  const key = storageCacheKey(method, req, options.spaceId || "");
  const cached = storageReadCache.get(key);
  if (cached && cached.expiresAt > Date.now()) return cached.data as TRsp;
  storageReadCache.delete(key);
  const pending = storageReadInflight.get(key);
  if (pending) return (await pending) as TRsp;
  const generation = storageReadGeneration;
  const request = send<TRsp>(service, method, req, options).then(data => {
    if (generation === storageReadGeneration) {
      storageReadCache.set(key, { expiresAt: Date.now() + STORAGE_READ_CACHE_TTL_MS, data });
    }
    return data;
  });
  storageReadInflight.set(key, request);
  try {
    return await request;
  } finally {
    if (storageReadInflight.get(key) === request) storageReadInflight.delete(key);
  }
}

installSpaceAwareClient(client);

client.interceptors.response.use(rsp => {
  recordServerDate(rsp.headers?.date);
  // 框架错误：HTTP 200 但 trpc-ret != 0，body 为空，错误信息在 header。
  const trpcRet = rsp.headers?.["trpc-ret"] ?? rsp.headers?.["Trpc-Ret"];
  if (trpcRet !== undefined && trpcRet !== null && String(trpcRet) !== "0") {
    const funcRet = rsp.headers?.["trpc-func-ret"] ?? "";
    return Promise.reject(new Error(funcRet || `框架错误(${trpcRet})`));
  }
  return rsp;
});
