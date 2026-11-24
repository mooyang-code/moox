import { call, RequestError } from "@/api/http";

/** 交易账户的交易所会话还没有启动（ErrorCode.TRADE_SESSION_NOT_READY = 19）：请求没有发到交易所，等几秒重试即可。 */
export function isTradeSessionNotReady(error: unknown): boolean {
  return error instanceof RequestError && error.hasCode(19, "TRADE_SESSION_NOT_READY");
}

/** 执行账户上已有持仓或挂单，加入组合要确认接管（ErrorCode.TRADE_ADOPTION_REQUIRED = 20）。 */
export function isTradeAdoptionRequired(error: unknown): boolean {
  return error instanceof RequestError && error.hasCode(20, "TRADE_ADOPTION_REQUIRED");
}

/** 对象不存在（ErrorCode.NOT_FOUND）。 */
export function isTradeNotFound(error: unknown): boolean {
  return error instanceof RequestError && error.hasCode(5, "NOT_FOUND");
}
import type {
  AccountResponse,
  AddLogicalAccountMemberReq,
  BlockedInstrument,
  CreateTradingAccountReq,
  CreatePaperSimulationReq,
  CreateLogicalAccountReq,
  TradingAccount,
  ExchangeInspection,
  ExecutionCapabilities,
  EquityPoint,
  Fill,
  Holding,
  InspectExchangeCredentialReq,
  LedgerEntry,
  ListAccountLedgerReq,
  ListOperatorActionsReq,
  ListTradingAccountsReq,
  ListFillsReq,
  ListOrdersReq,
  ListPositionsReq,
  LogicalAccount,
  LogicalAccountResponse,
  LogicalAccountTarget,
  OperatorAction,
  OperatorActionType,
  Order,
  Page,
  PageResult,
  SubmitOrderReq,
  Position,
  ShadowTarget,
  SummarizeAccountPnlReq,
  SummarizeAccountPnlRsp,
  TargetLane,
  TradableInstrument,
  RetInfo,
  UnblockInstrumentReq,
  UpdateTradingAccountReq
} from "./types";

export * from "./types";

export function createTradingAccount(req: CreateTradingAccountReq) {
  return call<CreateTradingAccountReq, AccountResponse>("trade", "CreateTradingAccount", req);
}

export function updateTradingAccount(req: UpdateTradingAccountReq) {
  return call<UpdateTradingAccountReq, AccountResponse>("trade", "UpdateTradingAccount", req);
}

export function listTradingAccounts(req: ListTradingAccountsReq = {}) {
  return call<ListTradingAccountsReq, { ret_info: RetInfo; accounts: TradingAccount[]; page_result: PageResult }>(
    "trade",
    "ListTradingAccounts",
    req
  );
}

export function setLeverage(req: { trading_account_id: string; instrument_id: string; leverage: string }) {
  return call<typeof req, AccountResponse>("trade", "SetLeverage", req);
}

export function syncTradingAccount(trading_account_id: string) {
  return call<
    { trading_account_id: string },
    {
      ret_info: RetInfo;
      fills_ingested: number;
      orders_updated: number;
      positions_updated: number;
      account_snapshot_updated: boolean;
      unknown_orders_resolved: number;
      ready: boolean;
      warnings: string[];
    }
  >("trade", "SyncTradingAccount", { trading_account_id });
}

// 只读检测一把交易所密钥，不保存；检测没有完成（超时、限频）时返回业务错误，不代表密钥有问题。
export function inspectExchangeCredential(req: InspectExchangeCredentialReq) {
  return call<InspectExchangeCredentialReq, { ret_info: RetInfo; inspection: ExchangeInspection }>(
    "trade",
    "InspectExchangeCredential",
    req
  );
}

// 对已有账户重新检测，结果写回账户类型和检测结果。
export function reinspectTradingAccount(trading_account_id: string) {
  return call<
    { trading_account_id: string },
    { ret_info: RetInfo; inspection: ExchangeInspection; account: TradingAccount; mode_changed: boolean }
  >("trade", "ReinspectTradingAccount", { trading_account_id });
}

export function createPaperSimulation(req: CreatePaperSimulationReq) {
  return call<CreatePaperSimulationReq, { ret_info: RetInfo; account: TradingAccount; logical_account: LogicalAccount }>(
    "trade",
    "CreatePaperSimulation",
    req
  );
}

export function closePaperSimulation(trading_account_id: string) {
  return call<{ trading_account_id: string }, { ret_info: RetInfo; account: TradingAccount; logical_account: LogicalAccount }>(
    "trade",
    "ClosePaperSimulation",
    { trading_account_id }
  );
}

export function getExecutionCapabilities(trading_account_id: string) {
  return call<{ trading_account_id: string }, { ret_info: RetInfo; capabilities: ExecutionCapabilities }>(
    "trade",
    "GetExecutionCapabilities",
    { trading_account_id }
  );
}

export function listTradableInstruments(trading_account_id: string) {
  return call<{ trading_account_id: string }, { ret_info: RetInfo; instruments: TradableInstrument[] }>(
    "trade",
    "ListTradableInstruments",
    { trading_account_id }
  );
}

export function queryEquityCurve(req: {
  trading_account_id?: string;
  logical_account_id?: string;
  start_time?: string;
  end_time?: string;
}) {
  return call<typeof req, { ret_info: RetInfo; points: EquityPoint[] }>("trade", "QueryEquityCurve", req);
}

export function listHoldings(trading_account_id: string) {
  return call<{ trading_account_id: string }, { ret_info: RetInfo; holdings: Holding[] }>("trade", "ListHoldings", {
    trading_account_id
  });
}

/** 从交易所即查资金流水（手续费、资金费、已实现盈亏、划转等），按时间倒序 */
export function listAccountLedger(req: ListAccountLedgerReq) {
  return call<ListAccountLedgerReq, { ret_info: RetInfo; entries: LedgerEntry[]; has_more: boolean }>(
    "trade",
    "ListAccountLedger",
    req
  );
}

/** 下架名单：被禁止买入的标的，全局，不分空间 */
export function listBlockedInstruments() {
  return call<Record<string, never>, { ret_info: RetInfo; instruments: BlockedInstrument[] }>(
    "trade",
    "ListBlockedInstruments",
    {}
  );
}

/** 解除一个标的的买入禁止；不在名单里不算错误，removed 为 false。对应的下架清仓还在跑时会提前结束 */
export function unblockInstrument(req: UnblockInstrumentReq) {
  return call<UnblockInstrumentReq, { ret_info: RetInfo; removed: boolean }>("trade", "UnblockInstrument", req);
}

/** 只平组合账户里的这一个执行账户；组合账户会被暂停 */
export function flattenTradingAccount(action_id: string, trading_account_id: string, reason: string) {
  return call<{ action_id: string; trading_account_id: string; reason: string }, { ret_info: RetInfo; action: OperatorAction }>(
    "trade",
    "FlattenTradingAccount",
    { action_id, trading_account_id, reason }
  );
}

export function createLogicalAccount(req: CreateLogicalAccountReq) {
  return call<CreateLogicalAccountReq, LogicalAccountResponse>("trade", "CreateLogicalAccount", req);
}

export function getLogicalAccount(logical_account_id: string) {
  return call<{ logical_account_id: string }, LogicalAccountResponse>("trade", "GetLogicalAccount", { logical_account_id });
}

export function listLogicalAccounts(page: Page = {}) {
  return call<{ page: Page }, { ret_info: RetInfo; logical_accounts: LogicalAccount[]; page_result: PageResult }>(
    "trade",
    "ListLogicalAccounts",
    { page }
  );
}

export function updateLogicalAccount(logical_account_id: string, name: string) {
  return call<{ logical_account_id: string; name: string }, LogicalAccountResponse>("trade", "UpdateLogicalAccount", {
    logical_account_id,
    name
  });
}

export function addLogicalAccountMember(req: AddLogicalAccountMemberReq) {
  return call<AddLogicalAccountMemberReq, LogicalAccountResponse>("trade", "AddLogicalAccountMember", req);
}

export function removeLogicalAccountMember(logical_account_id: string, trading_account_id: string) {
  return call<{ logical_account_id: string; trading_account_id: string }, LogicalAccountResponse>(
    "trade",
    "RemoveLogicalAccountMember",
    { logical_account_id, trading_account_id }
  );
}

export function pauseLogicalAccount(logical_account_id: string, reason: string) {
  return call<{ logical_account_id: string; reason: string }, LogicalAccountResponse>("trade", "PauseLogicalAccount", {
    logical_account_id,
    reason
  });
}

export function resumeLogicalAccount(logical_account_id: string) {
  return call<{ logical_account_id: string }, LogicalAccountResponse & { warning?: string }>("trade", "ResumeLogicalAccount", {
    logical_account_id
  });
}

export function flattenLogicalAccount(action_id: string, logical_account_id: string, reason: string) {
  return call<{ action_id: string; logical_account_id: string; reason: string }, { ret_info: RetInfo; action: OperatorAction }>(
    "trade",
    "FlattenLogicalAccount",
    { action_id, logical_account_id, reason }
  );
}

/** 人工下单的唯一入口：要不要先暂停组合由后端按控制模式决定。策略驱动的组合提交被顺延时响应里没有 order。 */
export function submitOrder(req: SubmitOrderReq) {
  return call<SubmitOrderReq, { ret_info: RetInfo; action: OperatorAction; order?: Order }>("trade", "SubmitOrder", req);
}

export function cancelOrder(action_id: string, order_id: string, reason: string) {
  return call<
    { action_id: string; order_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction; order: Order }
  >("trade", "CancelOrder", { action_id, order_id, reason });
}

export function getOperatorAction(action_id: string) {
  return call<{ action_id: string }, { ret_info: RetInfo; action: OperatorAction }>("trade", "GetOperatorAction", { action_id });
}

export function getLogicalAccountTarget(logical_account_id: string) {
  return call<{ logical_account_id: string }, { ret_info: RetInfo; target?: LogicalAccountTarget; lanes?: TargetLane[] }>(
    "trade",
    "GetLogicalAccountTarget",
    { logical_account_id }
  );
}

/** 列出策略实例最近的影子目标（从新到旧）：确认实盘前先看它们。 */
export function listShadowTargets(instance_id: string, limit = 20) {
  return call<{ instance_id: string; limit: number }, { ret_info: RetInfo; targets?: ShadowTarget[] }>(
    "trade",
    "ListShadowTargets",
    { instance_id, limit }
  );
}

export function getOrder(order_id: string) {
  return call<{ order_id: string }, { ret_info: RetInfo; order: Order }>("trade", "GetOrder", { order_id });
}

export function listOrders(req: ListOrdersReq = {}) {
  return call<ListOrdersReq, { ret_info: RetInfo; orders: Order[]; page_result: PageResult }>("trade", "ListOrders", req);
}

export function listFills(req: ListFillsReq = {}) {
  return call<ListFillsReq, { ret_info: RetInfo; fills: Fill[]; page_result: PageResult }>("trade", "ListFills", req);
}

export function listPositions(req: ListPositionsReq) {
  return call<ListPositionsReq, { ret_info: RetInfo; positions: Position[] }>("trade", "ListPositions", req);
}

/** 收益拆解：手续费、已实现盈亏按资产分别汇总，资金费从交易所即查，不折算 */
export function summarizeAccountPnl(req: SummarizeAccountPnlReq) {
  return call<SummarizeAccountPnlReq, SummarizeAccountPnlRsp>("trade", "SummarizeAccountPnl", req);
}

export function listOperatorActions(req: ListOperatorActionsReq) {
  return call<ListOperatorActionsReq, { ret_info: RetInfo; actions?: OperatorAction[]; page_result?: PageResult }>(
    "trade",
    "ListOperatorActions",
    req
  );
}

export const exchangeLabels: Record<number, string> = { 0: "-", 1: "币安", 2: "OKX", 3: "A 股", 4: "港股" };
export const orderStateLabels: Record<string, string> = {
  PENDING: "等待提交",
  SUBMITTING: "提交中",
  SUBMIT_UNKNOWN: "提交状态未知",
  OPEN: "挂单中",
  PARTIALLY_FILLED: "部分成交",
  CANCELING: "撤单中",
  CANCEL_UNKNOWN: "撤单状态未知",
  FILLED: "已成交",
  CANCELED: "已撤销",
  PARTIALLY_CANCELED: "部分撤销",
  REJECTED: "已拒绝",
  EXPIRED: "已过期"
};
/** 模拟撮合写的英文原因换成中文；收盘撤单、超出涨跌停价、停牌、不在交易时段等本来就是中文，原样显示。 */
const orderReasonTexts: Record<string, string> = {
  "paper limit order is not marketable": "限价没能立即成交",
  "paper reservation insufficient at match": "撮合时资金不足",
  "paper limit price invalid": "限价无效",
  "paper reduce-only capacity changed": "只减仓数量已变化",
  "paper matcher reducer is not configured": "撮合未配置"
};

export function orderReasonText(reason: string | undefined): string {
  const text = (reason || "").trim();
  return orderReasonTexts[text] || text;
}

export const actionStateLabels: Record<string, string> = {
  RUNNING: "执行中",
  COMPLETED: "已完成",
  PARTIAL: "部分完成",
  FAILED: "失败"
};
/** 人工操作的类型名：操作记录、页头进度条都用这一张表，同一个动作处处叫法一致。 */
export const operatorActionTypeLabels: Record<OperatorActionType, string> = {
  MANUAL_ORDER: "人工下单",
  CANCEL_ORDER: "撤单",
  FLATTEN: "整组清仓",
  REDUCE: "按比例减仓",
  DELIST_FLATTEN: "下架清仓"
};
export const targetStateLabels: Record<string, string> = {
  PENDING: "等待执行",
  CONVERGING: "调仓中",
  CONVERGED: "已到位",
  BLOCKED: "执行受阻",
  EXPIRED: "已过期"
};

/** 未完成的订单：和后端 activeOrderStates 一致（含等待提交、提交中，这两种还不能撤）。 */
export function isActiveOrderState(state: string): boolean {
  return ["PENDING", "SUBMITTING", "SUBMIT_UNKNOWN", "OPEN", "PARTIALLY_FILLED", "CANCELING", "CANCEL_UNKNOWN"].includes(
    state.toUpperCase()
  );
}

export function canCancelOrderState(state: string): boolean {
  return ["OPEN", "PARTIALLY_FILLED", "SUBMIT_UNKNOWN", "CANCELING", "CANCEL_UNKNOWN"].includes(state.toUpperCase());
}

export const orderOwnerLabels: Record<string, string> = {
  TARGET: "策略",
  OPERATOR: "人工",
  EXTERNAL: "外部",
  SYSTEM: "系统"
};

export function parseFlattenResult(action?: OperatorAction): import("./types").FlattenResult | null {
  if (!action?.result_json) return null;
  try {
    const parsed = JSON.parse(action.result_json) as import("./types").FlattenResult;
    return Array.isArray(parsed.accounts) ? parsed : null;
  } catch {
    return null;
  }
}
