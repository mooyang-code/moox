import axios from "axios";
import { Message } from "@arco-design/web-vue";
import { gatewayOrigin } from "@/api/gateway";
import { isRetInfoSuccess } from "../ret-info";
import type { OperatorAction, Order, RetInfo } from "./types";
import { installSpaceAwareSignedClient } from "../admin/signed-client";
import { explainTransportError } from "../network-error";

const tradeServiceMap = {
  console: "trade"
} as const;

const tradeClient = axios.create({
  baseURL: gatewayOrigin(),
  timeout: 30000,
  headers: { "Content-Type": "application/json" }
});

/**
 * 后端还没改成中文的错误：按错误常量开头对照成中文，只用于展示，不拿来做逻辑判断
 * （逻辑判断看错误码）。后端错误常量改成中文后，这张表里对应的项就删掉。
 * 第三项说明常量前后接的具体原因怎么显示，不写时显示“text（原因）”，没有原因时显示 text：
 *   - "chinese"：这个常量只是外层包装（例如 "trade store: invalid record: 修改组合成员前要先暂停组合"），
 *     原因含中文时只显示原因；原因是英文或没有原因时照常显示“text（原因）”或 text。
 *   - "hide"：只显示 text。原因是给排查用的内部说明（例如冲突时的 stale order version），对用户没有帮助。
 * 常量后面可以接半角冒号或全角冒号“：”。
 */
const tradeMessageTexts: readonly [prefix: string, text: string, detail?: "chinese" | "hide"][] = [
  ["trade: invalid trading account", "执行账户的参数不合法"],
  ["trade: trading account is not executable", "执行账户当前不能下单"],
  ["trade: invalid order", "订单参数不合法"],
  ["trade: invalid order specification", "订单参数不合法"],
  ["trade: invalid operator action", "操作参数不合法"],
  ["trade: invalid decimal", "数值格式不合法"],
  ["trade target: invalid target", "交易目标不合法"],
  ["trade target: target validity window elapsed", "交易目标已过有效期"],
  ["trade store: target expired", "交易目标已过有效期"],
  ["trade Exchange: invalid instrument ID", "标的 ID 不合法"],
  ["trade store: conflict", "数据已经被修改，刷新后再试", "hide"],
  ["trade store: invalid record", "数据不合法", "chinese"]
];

/**
 * 错误常量在消息里的位置：后面必须是结尾或冒号（半角或全角），避免 "trade: invalid order" 命中 "trade: invalid order transition"
 * （空白不算：那样就会命中 transition）。
 */
function constantAt(raw: string, prefix: string): number {
  for (let at = raw.indexOf(prefix); at >= 0; at = raw.indexOf(prefix, at + 1)) {
    const next = raw.charAt(at + prefix.length);
    if (next === "" || next === ":" || next === "：") return at;
  }
  return -1;
}

/** 含汉字：标为 "chinese" 的常量，原因只有是中文时才单独显示。 */
const HAN = /[一-鿿]/;

/**
 * 交易服务错误的展示文字：已知的英文错误常量换成中文，后端包装进去的具体原因（如 "%w: 具体原因"）按表里的设定接在括号里、
 * 单独显示或不显示；其余原样返回；没有消息时给出错误码。
 */
function tradeErrorText(retInfo: RetInfo): string {
  const raw = retInfo.msg || "";
  if (!raw) return `交易服务请求失败（错误码 ${retInfo.code}）`;
  for (const [prefix, text, mode] of tradeMessageTexts) {
    const at = constantAt(raw, prefix);
    if (at < 0) continue;
    const before = raw.slice(0, at).replace(/[:：\s]+$/, "");
    const after = raw.slice(at + prefix.length).replace(/^[:：\s]+/, "");
    const detail = [before, after].filter(Boolean).join("；");
    if (!detail || mode === "hide") return text;
    return mode === "chinese" && HAN.test(detail) ? detail : `${text}（${detail}）`;
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

/** 交易账户的交易所会话还没有启动（ErrorCode.TRADE_SESSION_NOT_READY = 19）：请求没有发到交易所，等几秒重试即可。 */
export function isTradeSessionNotReady(error: unknown): boolean {
  return error instanceof TradeResponseError && error.hasCode(19, "TRADE_SESSION_NOT_READY");
}

/** 执行账户上已有持仓或挂单，加入组合要确认接管（ErrorCode.TRADE_ADOPTION_REQUIRED = 20）。 */
export function isTradeAdoptionRequired(error: unknown): boolean {
  return error instanceof TradeResponseError && error.hasCode(20, "TRADE_ADOPTION_REQUIRED");
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

// 确认对话框提交期间发出的请求不弹全局提示：错误由对话框自己显示，避免同一个错误提示两遍
let dialogSubmissions = 0;

/** 在确认对话框里执行提交：期间发出的交易请求出错时只抛给调用方，不弹全局提示。 */
export async function withDialogErrors<T>(submit: () => Promise<T>): Promise<T> {
  dialogSubmissions += 1;
  try {
    return await submit();
  } finally {
    dialogSubmissions -= 1;
  }
}

export async function callTrade<TReq extends object, TRsp extends { ret_info: RetInfo }>(
  group: keyof typeof tradeServiceMap,
  method: string,
  req: TReq,
  options: { silent?: boolean } = {}
): Promise<TRsp> {
  // silent 同时管传输错误：后台轮询失败不弹全局提示，由页面自己显示
  const silent = Boolean(options.silent) || dialogSubmissions > 0;
  const rsp = await tradeClient.post<TRsp>(`/api/admin/${tradeServiceMap[group]}/${method}`, req, {
    silentError: silent
  });
  try {
    assertSuccess(rsp.data);
  } catch (error) {
    if (error instanceof TradeResponseError && !silent && !quietMethod.test(method)) {
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
    // 静默时也要把网络错误改写成中文（调用方会把 error.message 显示在自己的对话框里），只是不弹全局提示。
    const message = explainTransportError(error, "Trade 请求失败");
    if (!error?.config?.silentError) Message.error(message);
    return Promise.reject(error);
  }
);
