import { callTrade } from "./http";
import type {
  AccountResponse,
  AddLogicalAccountMemberReq,
  CreateAccountReq,
  CreateLogicalAccountReq,
  ExchangeAccount,
  Fill,
  ListAccountsReq,
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
  UpdateAccountReq
} from "./types";

export * from "./types";
export { tradeServiceMap } from "./http";

export function createAccount(req: CreateAccountReq) {
  return callTrade<CreateAccountReq, AccountResponse>("exchangeAccount", "CreateAccount", req);
}

export function updateAccount(req: UpdateAccountReq) {
  return callTrade<UpdateAccountReq, AccountResponse>("exchangeAccount", "UpdateAccount", req);
}

export function getAccount(exchange_account_id: string) {
  return callTrade<{ exchange_account_id: string }, AccountResponse>("exchangeAccount", "GetAccount", {
    exchange_account_id
  });
}

export function listAccounts(req: ListAccountsReq = {}) {
  return callTrade<ListAccountsReq, { ret_info: RetInfo; accounts: ExchangeAccount[]; page_result: PageResult }>(
    "exchangeAccount",
    "ListAccounts",
    req
  );
}

export function setLeverage(req: { exchange_account_id: string; symbol: string; leverage: string }) {
  return callTrade<typeof req, AccountResponse>("exchangeAccount", "SetLeverage", req);
}

export function syncAccount(exchange_account_id: string) {
  return callTrade<
    { exchange_account_id: string },
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
  >("exchangeAccount", "SyncAccount", { exchange_account_id });
}

export function createLogicalAccount(req: CreateLogicalAccountReq) {
  return callTrade<CreateLogicalAccountReq, LogicalAccountResponse>("logicalAccount", "CreateLogicalAccount", req);
}

export function getLogicalAccount(logical_account_id: string) {
  return callTrade<{ logical_account_id: string }, LogicalAccountResponse>("logicalAccount", "GetLogicalAccount", {
    logical_account_id
  });
}

export function listLogicalAccounts(page: Page = {}) {
  return callTrade<{ page: Page }, { ret_info: RetInfo; logical_accounts: LogicalAccount[]; page_result: PageResult }>(
    "logicalAccount",
    "ListLogicalAccounts",
    { page }
  );
}

export function updateLogicalAccount(logical_account_id: string, name: string) {
  return callTrade<{ logical_account_id: string; name: string }, LogicalAccountResponse>(
    "logicalAccount",
    "UpdateLogicalAccount",
    { logical_account_id, name }
  );
}

export function addLogicalAccountMember(req: AddLogicalAccountMemberReq) {
  return callTrade<AddLogicalAccountMemberReq, LogicalAccountResponse>("logicalAccount", "AddLogicalAccountMember", req);
}

export function removeLogicalAccountMember(logical_account_id: string, exchange_account_id: string) {
  return callTrade<{ logical_account_id: string; exchange_account_id: string }, LogicalAccountResponse>(
    "logicalAccount",
    "RemoveLogicalAccountMember",
    { logical_account_id, exchange_account_id }
  );
}

export function pauseLogicalAccount(logical_account_id: string, reason: string) {
  return callTrade<{ logical_account_id: string; reason: string }, LogicalAccountResponse>(
    "logicalAccount",
    "PauseLogicalAccount",
    { logical_account_id, reason }
  );
}

export function resumeLogicalAccount(logical_account_id: string) {
  return callTrade<{ logical_account_id: string }, LogicalAccountResponse & { warning?: string }>(
    "logicalAccount",
    "ResumeLogicalAccount",
    { logical_account_id }
  );
}

export function flattenLogicalAccount(action_id: string, logical_account_id: string, reason: string) {
  return callTrade<
    { action_id: string; logical_account_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction }
  >("logicalAccount", "FlattenLogicalAccount", { action_id, logical_account_id, reason });
}

export function placeManualOrder(req: PlaceManualOrderReq) {
  return callTrade<PlaceManualOrderReq, { ret_info: RetInfo; action: OperatorAction; order: Order }>(
    "execution",
    "PlaceManualOrder",
    req
  );
}

export function cancelOrder(action_id: string, order_id: string, reason: string) {
  return callTrade<
    { action_id: string; order_id: string; reason: string },
    { ret_info: RetInfo; action: OperatorAction; order: Order }
  >("execution", "CancelOrder", { action_id, order_id, reason });
}

export function getOperatorAction(action_id: string) {
  return callTrade<{ action_id: string }, { ret_info: RetInfo; action: OperatorAction }>("execution", "GetOperatorAction", {
    action_id
  });
}

export function getLogicalAccountTarget(logical_account_id: string) {
  return callTrade<{ logical_account_id: string }, { ret_info: RetInfo; target?: LogicalAccountTarget }>(
    "execution",
    "GetLogicalAccountTarget",
    { logical_account_id }
  );
}

export function getOrder(order_id: string) {
  return callTrade<{ order_id: string }, { ret_info: RetInfo; order: Order }>("execution", "GetOrder", { order_id });
}

export function listOrders(req: ListOrdersReq = {}) {
  return callTrade<ListOrdersReq, { ret_info: RetInfo; orders: Order[]; page_result: PageResult }>(
    "execution",
    "ListOrders",
    req
  );
}

export function listFills(req: ListFillsReq = {}) {
  return callTrade<ListFillsReq, { ret_info: RetInfo; fills: Fill[]; page_result: PageResult }>("execution", "ListFills", req);
}

export function listPositions(req: ListPositionsReq) {
  return callTrade<ListPositionsReq, { ret_info: RetInfo; positions: Position[] }>("execution", "ListPositions", req);
}

export const exchangeLabels: Record<number, string> = { 0: "-", 1: "Binance", 2: "OKX" };
export const marketTypeLabels: Record<number, string> = { 0: "-", 1: "SPOT", 2: "SWAP" };
export const executionModeLabels: Record<number, string> = { 0: "-", 1: "Paper", 2: "Live" };
export const environmentLabels: Record<number, string> = { 0: "-", 1: "Paper", 2: "Testnet", 3: "Production" };
export const orderTypeLabels: Record<number, string> = { 0: "-", 1: "MARKET", 2: "LIMIT" };
export const fillPolicyLabels: Record<number, string> = { 0: "-", 1: "GTC", 2: "IOC", 3: "FOK" };
export const orderSideLabels: Record<number, string> = { 0: "-", 1: "买入", 2: "卖出" };
export const orderSideColors: Record<number, string> = { 0: "gray", 1: "red", 2: "green" };

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
