import { callControl } from "@/api/admin/http";
import { callTrade } from "@/api/trade/http";
import type { RetInfo } from "@/api/trade/types";

// 手工登记下架用到的两个外部接口：
//  - Trade 的 ListInstrumentHolders：只读，用来预览会被平仓的组合；
//  - Collector 的 RegisterDelistNotice：登记后由 Collector 发布“标的即将下架”。

export type DelistExchange = "binance" | "okx";
export type DelistMarket = "SPOT" | "SWAP";

/** 下架检测登记用的空间：Collector 是按空间的服务，下架检测只覆盖 crypto。 */
export const DELIST_SPACE_ID = "crypto";

export interface InstrumentHolder {
  space_id: string;
  logical_account_id: string;
  /** 执行账户。 */
  trading_account_id: string;
  quantity: string;
  /** 零头：不会被平仓，只在结果里列出。 */
  dust: boolean;
  /** 模拟盘。 */
  paper: boolean;
  /** 执行账户状态：ENABLED、DISABLED、ERROR；停用和出错的账户照常列出。 */
  status: string;
  /** 对这一项的标注，例如零头只按最小数量判断。 */
  note: string;
}

/** 查询时出错、没能判断的账户：不能当成没有持有。 */
export interface SkippedAccount {
  trading_account_id: string;
  reason: string;
}

/** 持有者预览：除了列表，还说明这份列表能不能当真。 */
export interface HoldersPreview {
  holders: InstrumentHolder[];
  /**
   * false 表示这份结果不能当真，不能当成没人持有：Trade 的返回里没有 holders 字段又没有“无此标的”标记；
   * 或者违反约定（只查模拟盘却返回了非模拟盘的项；标明无此标的却带着持有者）。
   */
  verified: boolean;
  /** Trade 没有这个标的（没开通对应市场的账户），自然没有策略持有。 */
  instrumentUnknown: boolean;
  /** 出错没能判断的账户：不能当作没有持有，要单独列出、二次确认；不影响已返回的持有者照常展示。 */
  skippedAccounts: SkippedAccount[];
  /** 明确不算持有的说明（例如标的计价币不是账户的结算币）。 */
  notes: string[];
}

export interface ListInstrumentHoldersReq {
  exchange: string;
  market_type: string;
  exchange_symbol: string;
  paper_only: boolean;
}

export interface RegisterDelistNoticeReq {
  exchange: DelistExchange;
  market: DelistMarket;
  exchange_symbol: string;
  delist_at_ms: number;
  paper_only: boolean;
}

export interface RegisterDelistNoticeResult {
  /** false 表示这条已经登记过。 */
  created: boolean;
  event_id: string;
}

const TRADE_EXCHANGE: Record<DelistExchange, string> = { binance: "EXCHANGE_BINANCE", okx: "EXCHANGE_OKX" };
const TRADE_MARKET: Record<DelistMarket, string> = { SPOT: "MARKET_TYPE_SPOT", SWAP: "MARKET_TYPE_SWAP" };

export function buildHoldersRequest(input: {
  exchange: DelistExchange;
  market: DelistMarket;
  exchange_symbol: string;
  paper_only: boolean;
}): ListInstrumentHoldersReq {
  return {
    exchange: TRADE_EXCHANGE[input.exchange],
    market_type: TRADE_MARKET[input.market],
    exchange_symbol: input.exchange_symbol.trim().toUpperCase(),
    paper_only: input.paper_only
  };
}

export async function listInstrumentHolders(input: Parameters<typeof buildHoldersRequest>[0]): Promise<HoldersPreview> {
  const rsp = await callTrade<
    ListInstrumentHoldersReq,
    {
      ret_info: RetInfo;
      holders?: Partial<InstrumentHolder>[];
      instrument_unknown?: boolean;
      skipped_accounts?: Partial<SkippedAccount>[];
      notes?: string[];
    }
  >("console", "ListInstrumentHolders", buildHoldersRequest(input), { silent: true });
  const instrumentUnknown = Boolean(rsp.instrument_unknown);
  const holders = (rsp.holders ?? []).map(item => ({
    space_id: item.space_id ?? "",
    logical_account_id: item.logical_account_id ?? "",
    trading_account_id: item.trading_account_id ?? "",
    quantity: String(item.quantity ?? ""),
    dust: Boolean(item.dust),
    paper: Boolean(item.paper),
    status: item.status ?? "",
    note: item.note ?? ""
  }));
  const skippedAccounts = (rsp.skipped_accounts ?? []).map(item => ({
    trading_account_id: item.trading_account_id ?? "",
    reason: item.reason ?? ""
  }));
  // 不替对方改写 paper：对方违约要让人看得出来，而不是被掩盖。
  const violated = (input.paper_only && holders.some(item => !item.paper)) || (instrumentUnknown && holders.length > 0);
  return {
    holders,
    verified: (Array.isArray(rsp.holders) || instrumentUnknown) && !violated,
    instrumentUnknown,
    skippedAccounts,
    notes: rsp.notes ?? []
  };
}

export async function registerDelistNotice(req: RegisterDelistNoticeReq): Promise<RegisterDelistNoticeResult> {
  const rsp = await callControl<RegisterDelistNoticeReq, Partial<RegisterDelistNoticeResult>>(
    "collector",
    "RegisterDelistNotice",
    req,
    { headers: { "X-Space-Id": DELIST_SPACE_ID } }
  );
  return { created: Boolean(rsp.created), event_id: rsp.event_id ?? "" };
}
