import { callTrade } from "./http";
export {
  isTradeAdoptionRequired,
  isTradeInvalidParam,
  isTradeNotFound,
  isTradeSessionNotReady,
  TradeResponseError
} from "./http";
import type {
  AccountResponse,
  AddLogicalAccountMemberReq,
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
  PlaceManualOrderReq,
  SubmitOrderReq,
  Position,
  SummarizeAccountPnlReq,
  SummarizeAccountPnlRsp,
  TradableInstrument,
  RetInfo,
  UpdateTradingAccountReq
} from "./types";

export * from "./types";
export { tradeServiceMap } from "./http";

export function createTradingAccount(req: CreateTradingAccountReq) {
  return callTrade<CreateTradingAccountReq, AccountResponse>("console", "CreateTradingAccount", req, {
    silent: true
  });
}

/** silent：错误由调用方显示（例如确认框里），这里不再弹一次。 */
export function updateTradingAccount(req: UpdateTradingAccountReq, options: { silent?: boolean } = {}) {
  return callTrade<UpdateTradingAccountReq, AccountResponse>("console", "UpdateTradingAccount", req, options);
}

export function getTradingAccount(trading_account_id: string) {
  return callTrade<{ trading_account_id: string }, AccountResponse>("console", "GetTradingAccount", {
    trading_account_id
  });
}

export function listTradingAccounts(req: ListTradingAccountsReq = {}, options: { silent?: boolean } = {}) {
  return callTrade<ListTradingAccountsReq, { ret_info: RetInfo; accounts: TradingAccount[]; page_result: PageResult }>(
    "console",
    "ListTradingAccounts",
    req,
    options
  );
}

export function setLeverage(
  req: { trading_account_id: string; instrument_id: string; leverage: string },
  options: { silent?: boolean } = {}
) {
  return callTrade<typeof req, AccountResponse>("console", "SetLeverage", req, options);
}

export function syncTradingAccount(trading_account_id: string) {
  return callTrade<
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
  >("console", "SyncTradingAccount", { trading_account_id });
}

// 只读检测一把交易所密钥，不落库；检测没有完成（超时、限频）时返回业务错误，不代表密钥有问题。
export function inspectExchangeCredential(req: InspectExchangeCredentialReq) {
  return callTrade<InspectExchangeCredentialReq, { ret_info: RetInfo; inspection: ExchangeInspection }>(
    "console",
    "InspectExchangeCredential",
    req,
    { silent: true }
  );
}

// 对已有账户重新检测，结果写回账户类型和检测结果。
export function reinspectTradingAccount(trading_account_id: string) {
  return callTrade<
    { trading_account_id: string },
    { ret_info: RetInfo; inspection: ExchangeInspection; account: TradingAccount; mode_changed: boolean }
  >("console", "ReinspectTradingAccount", { trading_account_id }, { silent: true });
}

export function createPaperSimulation(req: CreatePaperSimulationReq) {
  return callTrade<CreatePaperSimulationReq, { ret_info: RetInfo; account: TradingAccount; logical_account: LogicalAccount }>(
    "console",
    "CreatePaperSimulation",
    req,
    { silent: true }
  );
}

export function closePaperSimulation(trading_account_id: string) {
  return callTrade<
    { trading_account_id: string },
    { ret_info: RetInfo; account: TradingAccount; logical_account: LogicalAccount }
  >("console", "ClosePaperSimulation", { trading_account_id }, { silent: true });
}

export function getExecutionCapabilities(trading_account_id: string) {
  return callTrade<{ trading_account_id: string }, { ret_info: RetInfo; capabilities: ExecutionCapabilities }>(
    "console",
    "GetExecutionCapabilities",
    { trading_account_id }
  );
}

export function listTradableInstruments(trading_account_id: string) {
  return callTrade<{ trading_account_id: string }, { ret_info: RetInfo; instruments: TradableInstrument[] }>(
    "console",
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
  return callTrade<typeof req, { ret_info: RetInfo; points: EquityPoint[] }>("console", "QueryEquityCurve", req);
}

export function listHoldings(trading_account_id: string, options: { silent?: boolean } = {}) {
  return callTrade<{ trading_account_id: string }, { ret_info: RetInfo; holdings: Holding[] }>(
    "console",
    "ListHoldings",
    { trading_account_id },
    options
  );
}

/** 从交易所即查资金流水（手续费、资金费、已实现盈亏、划转等），按时间倒序 */
export function listAccountLedger(req: ListAccountLedgerReq) {
  return callTrade<ListAccountLedgerReq, { ret_info: RetInfo; entries: LedgerEntry[]; has_more: boolean }>(
    "console",
    "ListAccountLedger",
    req
  );
}

/** 只平组合账户里的这一个执行账户；组合账户会被暂停 */
export function flattenTradingAccount(action_id: string, trading_account_id: string, reason: string) {
  return callTrade<
    { action_id: string; trading_account_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction }
  >("console", "FlattenTradingAccount", { action_id, trading_account_id, reason }, { silent: true });
}

export function createLogicalAccount(req: CreateLogicalAccountReq) {
  return callTrade<CreateLogicalAccountReq, LogicalAccountResponse>("console", "CreateLogicalAccount", req, { silent: true });
}

export function getLogicalAccount(logical_account_id: string, options: { silent?: boolean } = {}) {
  return callTrade<{ logical_account_id: string }, LogicalAccountResponse>(
    "console",
    "GetLogicalAccount",
    { logical_account_id },
    options
  );
}

export function listLogicalAccounts(page: Page = {}) {
  return callTrade<{ page: Page }, { ret_info: RetInfo; logical_accounts: LogicalAccount[]; page_result: PageResult }>(
    "console",
    "ListLogicalAccounts",
    { page }
  );
}

export function updateLogicalAccount(logical_account_id: string, name: string) {
  return callTrade<{ logical_account_id: string; name: string }, LogicalAccountResponse>(
    "console",
    "UpdateLogicalAccount",
    { logical_account_id, name },
    { silent: true }
  );
}

export function addLogicalAccountMember(req: AddLogicalAccountMemberReq) {
  return callTrade<AddLogicalAccountMemberReq, LogicalAccountResponse>("console", "AddLogicalAccountMember", req, {
    silent: true
  });
}

export function removeLogicalAccountMember(logical_account_id: string, trading_account_id: string) {
  return callTrade<{ logical_account_id: string; trading_account_id: string }, LogicalAccountResponse>(
    "console",
    "RemoveLogicalAccountMember",
    { logical_account_id, trading_account_id },
    { silent: true }
  );
}

export function pauseLogicalAccount(logical_account_id: string, reason: string) {
  return callTrade<{ logical_account_id: string; reason: string }, LogicalAccountResponse>(
    "console",
    "PauseLogicalAccount",
    {
      logical_account_id,
      reason
    },
    { silent: true }
  );
}

export function resumeLogicalAccount(logical_account_id: string) {
  return callTrade<{ logical_account_id: string }, LogicalAccountResponse & { warning?: string }>(
    "console",
    "ResumeLogicalAccount",
    { logical_account_id },
    { silent: true }
  );
}

export function flattenLogicalAccount(action_id: string, logical_account_id: string, reason: string) {
  return callTrade<
    { action_id: string; logical_account_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction }
  >("console", "FlattenLogicalAccount", { action_id, logical_account_id, reason }, { silent: true });
}

export function submitOrder(req: SubmitOrderReq) {
  return callTrade<SubmitOrderReq, { ret_info: RetInfo; action: OperatorAction; order: Order }>("console", "SubmitOrder", req, {
    silent: true
  });
}

export function placeManualOrder(req: PlaceManualOrderReq) {
  return callTrade<PlaceManualOrderReq, { ret_info: RetInfo; action: OperatorAction; order?: Order }>(
    "console",
    "PlaceManualOrder",
    req,
    { silent: true }
  );
}

export function cancelOrder(action_id: string, order_id: string, reason: string) {
  return callTrade<
    { action_id: string; order_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction; order: Order }
  >("console", "CancelOrder", { action_id, order_id, reason }, { silent: true });
}

export function getOperatorAction(action_id: string, options: { silent?: boolean } = {}) {
  return callTrade<{ action_id: string }, { ret_info: RetInfo; action: OperatorAction }>(
    "console",
    "GetOperatorAction",
    { action_id },
    options
  );
}

export function getLogicalAccountTarget(logical_account_id: string, options: { silent?: boolean } = {}) {
  return callTrade<{ logical_account_id: string }, { ret_info: RetInfo; target?: LogicalAccountTarget }>(
    "console",
    "GetLogicalAccountTarget",
    { logical_account_id },
    options
  );
}

export function getOrder(order_id: string, options: { silent?: boolean } = {}) {
  return callTrade<{ order_id: string }, { ret_info: RetInfo; order: Order }>("console", "GetOrder", { order_id }, options);
}

export function listOrders(req: ListOrdersReq = {}) {
  return callTrade<ListOrdersReq, { ret_info: RetInfo; orders: Order[]; page_result: PageResult }>("console", "ListOrders", req);
}

export function listFills(req: ListFillsReq = {}) {
  return callTrade<ListFillsReq, { ret_info: RetInfo; fills: Fill[]; page_result: PageResult }>("console", "ListFills", req);
}

export function listPositions(req: ListPositionsReq, options: { silent?: boolean } = {}) {
  return callTrade<ListPositionsReq, { ret_info: RetInfo; positions: Position[] }>("console", "ListPositions", req, options);
}

/** 收益拆解：手续费、已实现盈亏按资产分别汇总，资金费从交易所即查，不折算 */
export function summarizeAccountPnl(req: SummarizeAccountPnlReq) {
  return callTrade<SummarizeAccountPnlReq, SummarizeAccountPnlRsp>("console", "SummarizeAccountPnl", req);
}

export function listOperatorActions(req: ListOperatorActionsReq) {
  return callTrade<ListOperatorActionsReq, { ret_info: RetInfo; actions?: OperatorAction[]; page_result?: PageResult }>(
    "console",
    "ListOperatorActions",
    req
  );
}

export const exchangeLabels: Record<number, string> = { 0: "-", 1: "Binance", 2: "OKX" };
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
export const actionStateLabels: Record<string, string> = {
  RUNNING: "执行中",
  COMPLETED: "已完成",
  PARTIAL: "部分完成",
  FAILED: "失败"
};
/** 操作记录里的完整类型名；页头进度条另有一套短名（detail-model 的 actionTypeLabels） */
export const operatorActionTypeLabels: Record<OperatorActionType, string> = {
  MANUAL_ORDER: "人工下单",
  SUBMIT_ORDER: "提交订单",
  CANCEL_ORDER: "撤单",
  FLATTEN: "整组清仓",
  REDUCE: "按比例减仓",
  DELIST_FLATTEN: "下架清仓"
};
export const targetStateLabels: Record<string, string> = {
  PENDING: "等待执行",
  CONVERGING: "收敛中",
  CONVERGED: "已收敛",
  BLOCKED: "已阻塞",
  EXPIRED: "已过期"
};

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
