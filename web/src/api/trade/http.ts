import axios from "axios";
import { Message } from "@arco-design/web-vue";
import { gatewayOrigin } from "@/api/gateway";
import { isRetInfoSuccess } from "../ret-info";
import type { OperatorAction, Order, RetInfo } from "./types";
import { installSpaceAwareSignedClient } from "../admin/signed-client";
import { explainTransportError } from "../network-error";

export const tradeServiceMap = {
  console: "trade"
} as const;

const tradeClient = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { "Content-Type": "application/json" }
});

/**
 * 后端还没改成中文的常见错误：按错误常量开头对照成中文，只用于展示，不拿来做逻辑判断
 * （逻辑判断看错误码）。后端错误常量改成中文后，这张表里对应的项就用不到了。
 */
const tradeMessageTexts: readonly [prefix: string, text: string][] = [
  ["trade logical account: adoption is required", "账户里已有持仓或余额，要勾选“接管账户里已有的持仓”才能加入组合"],
  ["trade logical account: member has active exposure", "执行账户还有持仓或挂单，要先平仓"],
  ["trade logical account: not ready", "组合账户还没有准备好，稍后再试"],
  ["trade logical account: runner ownership conflict", "组合正在被另一个执行进程处理，稍后再试"],
  ["trade: logical account membership change requires pause", "组合正在自动执行，先暂停再增删成员"],
  ["trade: logical account members must be homogeneous", "组合成员的环境和结算资产必须一致"],
  ["trade: invalid logical account", "组合账户的参数不合法"],
  ["trade: invalid trading account", "执行账户的参数不合法"],
  ["trade: trading account is not executable", "执行账户当前不能下单"],
  ["trade: invalid order", "订单参数不合法"],
  ["trade account: not found", "执行账户不存在"],
  ["trade account: conflict", "执行账户冲突：名称重复或状态已经变化，刷新后再试"],
  ["trade account: invalid Exchange credential", "交易所密钥无效"],
  ["trade account: production trading is disabled", "生产环境交易没有开启"],
  ["trade store: conflict", "数据已经被修改，刷新后再试"],
  ["trade order: idempotency conflict", "同一个操作编号已经用于另一个请求"],
  ["trade operator: invalid command", "操作参数不合法"],
  ["trade operator: cancellation is not confirmed", "撤单还没有得到交易所确认"],
  ["trade delist: invalid request", "下架请求的参数不合法"],
  ["ExchangeSession is not configured", "执行账户的交易所会话还没有建立，稍等几秒再试"]
];

/** 错误常量在消息里的位置：后面必须是结尾或冒号，避免 "trade: invalid order" 命中 "trade: invalid order transition"。 */
function constantAt(raw: string, prefix: string): number {
  for (let at = raw.indexOf(prefix); at >= 0; at = raw.indexOf(prefix, at + 1)) {
    const next = raw.charAt(at + prefix.length);
    if (next === "" || next === ":") return at;
  }
  return -1;
}

/**
 * 交易服务错误的展示文字：已知的英文错误常量换成中文，后端包装进去的具体原因（如 "%w: 具体原因"）接在括号里保留；
 * 其余原样返回；没有消息时给出错误码。
 */
export function tradeErrorText(retInfo: RetInfo): string {
  const raw = retInfo.msg || "";
  if (!raw) return `交易服务请求失败（错误码 ${retInfo.code}）`;
  for (const [prefix, text] of tradeMessageTexts) {
    const at = constantAt(raw, prefix);
    if (at < 0) continue;
    const before = raw.slice(0, at).replace(/[:\s]+$/, "");
    const after = raw.slice(at + prefix.length).replace(/^[:\s]+/, "");
    const detail = [before, after].filter(Boolean).join("；");
    return detail ? `${text}（${detail}）` : text;
  }
  return raw;
}

export class TradeResponseError extends Error {
  constructor(readonly response: { ret_info: RetInfo; action?: OperatorAction; order?: Order }) {
    super(tradeErrorText(response.ret_info));
    this.name = "TradeResponseError";
  }

  /** 错误码是否是给定的几种之一；后端返回的错误码可能是数字，也可能是枚举名。 */
  hasCode(...codes: readonly (number | string)[]): boolean {
    const code = String(this.response.ret_info.code);
    return codes.some(item => String(item) === code);
  }
}

/** 请求参数不合法（ErrorCode.INVALID_PARAM）：业务校验没通过，改了参数可以重试。 */
export function isTradeInvalidParam(error: unknown): boolean {
  return error instanceof TradeResponseError && error.hasCode(1, "INVALID_PARAM");
}

/** 对象不存在（ErrorCode.NOT_FOUND）。 */
export function isTradeNotFound(error: unknown): boolean {
  return error instanceof TradeResponseError && error.hasCode(5, "NOT_FOUND");
}

function assertSuccess(response: { ret_info?: RetInfo }) {
  const retInfo = response.ret_info;
  if (!retInfo) throw new Error("交易服务的响应缺少 ret_info");
  if (!isRetInfoSuccess(retInfo.code)) {
    throw new TradeResponseError({ ...response, ret_info: retInfo });
  }
}

// 轮询和查询失败时不弹窗，否则每次轮询都会弹一次；写操作默认提示业务错误。
const quietMethod = /^(List|Get|Query)/;

export async function callTrade<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  group: keyof typeof tradeServiceMap,
  method: string,
  req: TReq,
  options: { silent?: boolean } = {}
): Promise<TRsp> {
  const rsp = await tradeClient.post<TRsp>(`/api/admin/${tradeServiceMap[group]}/${method}`, req);
  try {
    assertSuccess(rsp.data);
  } catch (error) {
    if (error instanceof TradeResponseError && !options.silent && !quietMethod.test(method)) {
      Message.error(error.message);
    }
    throw error;
  }
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
    Message.error(explainTransportError(error, "Trade 请求失败"));
    return Promise.reject(error);
  }
);
