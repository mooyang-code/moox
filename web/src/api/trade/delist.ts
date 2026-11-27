import { call } from "@/api/http";
import type { RetInfo } from "@/api/trade/types";

// 下架处理用到的接口：
//  - Trade 的 ListInstrumentHolders：只读，用来预览会被平仓的组合；
//  - Collector 的 RegisterDelistNotice：登记后由 Collector 发布“标的即将下架”，Trade 收到后禁止买入并按时间线平仓；
//  - Trade 的 ListDelists：只读，下架记录与各轮各组合的清仓动作。

export type DelistExchange = "binance" | "okx";
export type DelistMarket = "spot" | "swap";

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
  /** 下架时刻，毫秒（数字字符串） */
  delist_at: string;
  paper_only: boolean;
}

export interface RegisterDelistNoticeResult {
  /** false 表示这条已经登记过。 */
  created: boolean;
  event_id: string;
}

export function buildHoldersRequest(input: {
  exchange: DelistExchange;
  market: DelistMarket;
  exchange_symbol: string;
  paper_only: boolean;
}): ListInstrumentHoldersReq {
  return {
    exchange: input.exchange,
    market_type: input.market,
    exchange_symbol: input.exchange_symbol.trim().toUpperCase(),
    paper_only: input.paper_only
  };
}

export async function listInstrumentHolders(input: Parameters<typeof buildHoldersRequest>[0]): Promise<HoldersPreview> {
  const rsp = await call<
    ListInstrumentHoldersReq,
    {
      ret_info: RetInfo;
      holders?: Partial<InstrumentHolder>[];
      instrument_unknown?: boolean;
      skipped_accounts?: Partial<SkippedAccount>[];
      notes?: string[];
    }
  >("trade", "ListInstrumentHolders", buildHoldersRequest(input));
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
  const rsp = await call<RegisterDelistNoticeReq, Partial<RegisterDelistNoticeResult>>("collector", "RegisterDelistNotice", req);
  return { created: Boolean(rsp.created), event_id: rsp.event_id ?? "" };
}

/** 下架记录里一轮一个组合的清仓。 */
export interface DelistFlatten {
  round: number;
  space_id: string;
  logical_account_id: string;
  action_id: string;
  /** RUNNING、COMPLETED、PARTIAL、FAILED；空表示还没发起 */
  action_status: string;
  last_error: string;
  /** 毫秒（数字字符串），经 toInstantMs/formatInstant 解读 */
  created_at: string;
}

/** 一条下架记录：一个（标的、范围、下架时间）。 */
export interface DelistRecord {
  delist_id: string;
  /** 小写交易所（binance、okx）与市场（spot、swap），交易所代码是交易所原文（大写） */
  exchange: string;
  market_type: string;
  exchange_symbol: string;
  /** ALL 或 PAPER_ONLY */
  scope: string;
  /** 下架时刻，毫秒（数字字符串） */
  delist_at: string;
  notice_source: string;
  source_event_id: string;
  /** ACTIVE、COMPLETED、FAILED、CANCELED、RESCHEDULED */
  status: string;
  /** 已规划到第几轮：0 还没到 T−24h，1 已按 T−24h 规划，2 已按 T−2h 规划 */
  planned_round: number;
  note: string;
  result: string;
  /** 毫秒（数字字符串）；finished_at 为 "0" 表示还没结束 */
  created_at: string;
  finished_at: string;
  flattens: DelistFlatten[];
}

/** 列出下架记录（全局，不分空间）；activeOnly 时只列进行中的，否则按创建时间倒序列最近 limit 条。 */
export async function listDelists(activeOnly: boolean, limit = 50): Promise<DelistRecord[]> {
  const rsp = await call<{ active_only: boolean; limit: number }, { ret_info: RetInfo; delists?: Partial<DelistRecord>[] }>(
    "trade",
    "ListDelists",
    { active_only: activeOnly, limit }
  );
  return (rsp.delists ?? []).map(item => ({
    delist_id: item.delist_id ?? "",
    exchange: item.exchange ?? "",
    market_type: item.market_type ?? "",
    exchange_symbol: item.exchange_symbol ?? "",
    scope: item.scope ?? "",
    delist_at: String(item.delist_at ?? "0"),
    notice_source: item.notice_source ?? "",
    source_event_id: item.source_event_id ?? "",
    status: item.status ?? "",
    planned_round: Number(item.planned_round ?? 0),
    note: item.note ?? "",
    result: item.result ?? "",
    created_at: String(item.created_at ?? "0"),
    finished_at: String(item.finished_at ?? "0"),
    flattens: (item.flattens ?? []).map(flatten => ({
      round: Number(flatten.round ?? 0),
      space_id: flatten.space_id ?? "",
      logical_account_id: flatten.logical_account_id ?? "",
      action_id: flatten.action_id ?? "",
      action_status: flatten.action_status ?? "",
      last_error: flatten.last_error ?? "",
      created_at: String(flatten.created_at ?? "0")
    }))
  }));
}
