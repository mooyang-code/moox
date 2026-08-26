import { callTrade } from "./http";
import type {
  AccountResponse,
  AddLogicalAccountMemberReq,
  CreateTradingAccountReq,
  CreatePaperSimulationReq,
  CreateLogicalAccountReq,
  TradingAccount,
  ExecutionCapabilities,
  EquityPoint,
  Fill,
  Holding,
  ListTradingAccountsReq,
  ListFillsReq,
  ListOrdersReq,
  ListPositionsReq,
  LogicalAccount,
  LogicalAccountResponse,
  LogicalAccountTarget,
  OperatorAction,
  Order,
  Page,
  PageResult,
  PlaceManualOrderReq,
  Position,
  RetInfo,
  UpdateTradingAccountReq
} from "./types";

export * from "./types";
export { tradeServiceMap } from "./http";

export function createTradingAccount(req: CreateTradingAccountReq) {
  return callTrade<CreateTradingAccountReq, AccountResponse>("console", "CreateTradingAccount", req);
}

export function updateTradingAccount(req: UpdateTradingAccountReq) {
  return callTrade<UpdateTradingAccountReq, AccountResponse>("console", "UpdateTradingAccount", req);
}

export function getTradingAccount(trading_account_id: string) {
  return callTrade<{ trading_account_id: string }, AccountResponse>("console", "GetTradingAccount", {
    trading_account_id
  });
}

export function listTradingAccounts(req: ListTradingAccountsReq = {}) {
  return callTrade<ListTradingAccountsReq, { ret_info: RetInfo; accounts: TradingAccount[]; page_result: PageResult }>(
    "console",
    "ListTradingAccounts",
    req
  );
}

export function setLeverage(req: { trading_account_id: string; instrument_id: string; leverage: string }) {
  return callTrade<typeof req, AccountResponse>("console", "SetLeverage", req);
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

export function createPaperSimulation(req: CreatePaperSimulationReq) {
  return callTrade<CreatePaperSimulationReq, { ret_info: RetInfo; account: TradingAccount; logical_account: LogicalAccount }>(
    "console",
    "CreatePaperSimulation",
    req
  );
}

export function closePaperSimulation(trading_account_id: string) {
  return callTrade<
    { trading_account_id: string },
    { ret_info: RetInfo; account: TradingAccount; logical_account: LogicalAccount }
  >("console", "ClosePaperSimulation", { trading_account_id });
}

export function getExecutionCapabilities(trading_account_id: string) {
  return callTrade<{ trading_account_id: string }, { ret_info: RetInfo; capabilities: ExecutionCapabilities }>(
    "console",
    "GetExecutionCapabilities",
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

export function listHoldings(trading_account_id: string) {
  return callTrade<{ trading_account_id: string }, { ret_info: RetInfo; holdings: Holding[] }>("console", "ListHoldings", {
    trading_account_id
  });
}

export function createLogicalAccount(req: CreateLogicalAccountReq) {
  return callTrade<CreateLogicalAccountReq, LogicalAccountResponse>("console", "CreateLogicalAccount", req);
}

export function getLogicalAccount(logical_account_id: string) {
  return callTrade<{ logical_account_id: string }, LogicalAccountResponse>("console", "GetLogicalAccount", {
    logical_account_id
  });
}

export function listLogicalAccounts(page: Page = {}) {
  return callTrade<{ page: Page }, { ret_info: RetInfo; logical_accounts: LogicalAccount[]; page_result: PageResult }>(
    "console",
    "ListLogicalAccounts",
    { page }
  );
}

export function updateLogicalAccount(logical_account_id: string, name: string) {
  return callTrade<{ logical_account_id: string; name: string }, LogicalAccountResponse>("console", "UpdateLogicalAccount", {
    logical_account_id,
    name
  });
}

export function addLogicalAccountMember(req: AddLogicalAccountMemberReq) {
  return callTrade<AddLogicalAccountMemberReq, LogicalAccountResponse>("console", "AddLogicalAccountMember", req);
}

export function removeLogicalAccountMember(logical_account_id: string, trading_account_id: string) {
  return callTrade<{ logical_account_id: string; trading_account_id: string }, LogicalAccountResponse>(
    "console",
    "RemoveLogicalAccountMember",
    { logical_account_id, trading_account_id }
  );
}

export function pauseLogicalAccount(logical_account_id: string, reason: string) {
  return callTrade<{ logical_account_id: string; reason: string }, LogicalAccountResponse>("console", "PauseLogicalAccount", {
    logical_account_id,
    reason
  });
}

export function resumeLogicalAccount(logical_account_id: string) {
  return callTrade<{ logical_account_id: string }, LogicalAccountResponse & { warning?: string }>(
    "console",
    "ResumeLogicalAccount",
    { logical_account_id }
  );
}

export function flattenLogicalAccount(action_id: string, logical_account_id: string, reason: string) {
  return callTrade<
    { action_id: string; logical_account_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction }
  >("console", "FlattenLogicalAccount", { action_id, logical_account_id, reason });
}

export function placeManualOrder(req: PlaceManualOrderReq) {
  return callTrade<PlaceManualOrderReq, { ret_info: RetInfo; action: OperatorAction; order: Order }>(
    "console",
    "PlaceManualOrder",
    req
  );
}

export function cancelOrder(action_id: string, order_id: string, reason: string) {
  return callTrade<
    { action_id: string; order_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction; order: Order }
  >("console", "CancelOrder", { action_id, order_id, reason });
}

export function getOperatorAction(action_id: string) {
  return callTrade<{ action_id: string }, { ret_info: RetInfo; action: OperatorAction }>("console", "GetOperatorAction", {
    action_id
  });
}

export function getLogicalAccountTarget(logical_account_id: string) {
  return callTrade<{ logical_account_id: string }, { ret_info: RetInfo; target?: LogicalAccountTarget }>(
    "console",
    "GetLogicalAccountTarget",
    { logical_account_id }
  );
}

export function getOrder(order_id: string) {
  return callTrade<{ order_id: string }, { ret_info: RetInfo; order: Order }>("console", "GetOrder", { order_id });
}

export function listOrders(req: ListOrdersReq = {}) {
  return callTrade<ListOrdersReq, { ret_info: RetInfo; orders: Order[]; page_result: PageResult }>("console", "ListOrders", req);
}

export function listFills(req: ListFillsReq = {}) {
  return callTrade<ListFillsReq, { ret_info: RetInfo; fills: Fill[]; page_result: PageResult }>("console", "ListFills", req);
}

export function listPositions(req: ListPositionsReq) {
  return callTrade<ListPositionsReq, { ret_info: RetInfo; positions: Position[] }>("console", "ListPositions", req);
}

export const exchangeLabels: Record<number, string> = { 0: "-", 1: "Binance", 2: "OKX" };
export const marketTypeLabels: Record<number, string> = { 0: "-", 1: "SPOT", 2: "SWAP" };
export const executionModeLabels: Record<number, string> = { 0: "-", 1: "Paper", 2: "Live" };
export const environmentLabels: Record<number, string> = { 0: "-", 1: "Testnet", 2: "Production" };
export const orderTypeLabels: Record<number, string> = { 0: "-", 1: "MARKET", 2: "LIMIT" };
export const fillPolicyLabels: Record<number, string> = { 0: "-", 1: "GTC", 2: "IOC", 3: "FOK" };
export const orderSideLabels: Record<number, string> = { 0: "-", 1: "买入", 2: "卖出" };
export const orderSideColors: Record<number, string> = { 0: "gray", 1: "red", 2: "green" };
export const logicalAutomationStateLabels: Record<string, string> = {
  ACTIVE: "运行中",
  PAUSED: "已暂停"
};
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
  PENDING: "等待中",
  RUNNING: "执行中",
  SUCCEEDED: "已完成",
  FAILED: "失败",
  CANCELED: "已取消"
};
export const targetStateLabels: Record<string, string> = {
  ACCEPTED: "已接收",
  RUNNING: "执行中",
  SUCCEEDED: "已完成",
  BLOCKED: "已阻塞",
  FAILED: "失败"
};

export function canCancelOrderState(state: string): boolean {
  return ["OPEN", "PARTIALLY_FILLED", "SUBMIT_UNKNOWN"].includes(state.toUpperCase());
}

export function formatTimestamp(value?: string): string {
  if (!value) return "-";
  const milliseconds = Number(value);
  if (!Number.isFinite(milliseconds) || milliseconds <= 0) return "-";
  return new Date(milliseconds).toLocaleString();
}

export function parseFlattenResult(action?: OperatorAction): import("./types").FlattenResult | null {
  if (!action?.result_json) return null;
  try {
    const parsed = JSON.parse(action.result_json) as import("./types").FlattenResult;
    return Array.isArray(parsed.accounts) ? parsed : null;
  } catch {
    return null;
  }
}
