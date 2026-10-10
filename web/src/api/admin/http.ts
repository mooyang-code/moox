import axios from "axios";
import type { AxiosRequestConfig } from "axios";
import { Message } from "@arco-design/web-vue";
import { gatewayOrigin } from "@/api/gateway";
import { isRetInfoSuccess } from "../ret-info";
import type { ControlResponse } from "./types";
import { expireBrowserSession, installSpaceAwareSignedClient } from "./signed-client";

declare module "axios" {
  interface AxiosRequestConfig {
    /** 为 true 时请求失败不弹全局错误提示，由调用方自行展示（例如后台轮询已有自己的失败提示）。 */
    silentError?: boolean;
  }
}

const adminClient = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { "Content-Type": "application/json" }
});
const reportedErrors = new WeakSet<object>();
// serverClockOffset 是服务端时钟减浏览器时钟（毫秒），取自最近一次响应的 Date 头；还没有读到时为 null。
let serverClockOffset: number | null = null;

/** 按最近一次响应的 Date 头换算的服务端当前时间（毫秒）；还没有读到 Date 头时返回 null。浏览器时钟可能与服务端不一致，
 *  比较服务端给出的时间戳时用它。跨域时网关要暴露 Date 头才读得到。 */
export function serverNow(): number | null {
  return serverClockOffset === null ? null : Date.now() + serverClockOffset;
}

/** 记录一次响应的 Date 头（服务端时钟减浏览器时钟）；读不出时间时保持原值。 */
export function recordServerDate(header: unknown) {
  const date = Date.parse(String(header ?? ""));
  if (Number.isFinite(date)) serverClockOffset = date - Date.now();
}

export class ControlRequestError<T = unknown> extends Error {
  constructor(
    message: string,
    public readonly response?: ControlResponse<T>
  ) {
    super(message);
    this.name = "ControlRequestError";
  }
}

export function reportControlError(error: unknown) {
  if (typeof error === "object" && error !== null && reportedErrors.has(error)) return;
  const message = error instanceof Error ? error.message : String(error || "Control 请求失败");
  Message.error(message);
  if (typeof error === "object" && error !== null) reportedErrors.add(error);
}

function readAccessTokenFromConfig(config?: AxiosRequestConfig): string {
  const headers = (config?.headers || {}) as Record<string, string | undefined>;
  return headers.Authorization || headers["X-Access-Token"] || "";
}

function assertControlSuccess<T>(rsp: ControlResponse<T>): T {
  if (!rsp.ret_info) {
    throw new Error("control response missing ret_info");
  }
  const retCode = rsp.ret_info.code;
  if (!isRetInfoSuccess(retCode)) {
    throw new ControlRequestError(rsp.ret_info.msg || `control request failed: ${retCode}`, rsp);
  }
  return rsp as T;
}

export async function callControl<TReq extends object, TRsp>(
  service: string,
  method: string,
  req: TReq,
  config?: AxiosRequestConfig
): Promise<TRsp> {
  if (!localStorage.getItem("user-info") && !readAccessTokenFromConfig(config)) {
    throw await expireBrowserSession("未登录或登录态已失效，请重新登录后再访问管理接口");
  }
  const rsp = await adminClient.post<ControlResponse<TRsp>>(`/api/admin/${service}/${method}`, req, config);
  return assertControlSuccess<TRsp>(rsp.data);
}

installSpaceAwareSignedClient(adminClient);

adminClient.interceptors.response.use(
  rsp => {
    recordServerDate(rsp.headers?.date);
    // 框架错误：HTTP 200 但 trpc-ret != 0，body 为空，错误信息在 header。
    const trpcRet = rsp.headers?.["trpc-ret"] ?? rsp.headers?.["Trpc-Ret"];
    if (trpcRet !== undefined && trpcRet !== null && String(trpcRet) !== "0") {
      const funcRet = rsp.headers?.["trpc-func-ret"] ?? "";
      return Promise.reject(new Error(funcRet || `框架错误(${trpcRet})`));
    }
    return rsp;
  },
  error => {
    const data = error?.response?.data as ControlResponse<unknown> | undefined;
    const message = data?.ret_info?.msg || error?.message || "Control 请求失败";
    const reportedError = new ControlRequestError(message, data);
    if (!error?.config?.silentError) reportControlError(reportedError);
    return Promise.reject(reportedError);
  }
);
