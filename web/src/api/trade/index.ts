import { callTrade } from "./http";
import type {
  CancelAllOrdersReq,
  CancelAllOrdersRsp,
  CancelOrderRsp,
  CreateAccountReq,
  CreateAccountRsp,
  GetAccountRsp,
  GetExecutionRsp,
  GetOrderRsp,
  ListAccountsReq,
  ListAccountsRsp,
  ListExecutionsReq,
  ListExecutionsRsp,
  ListFillsReq,
  ListFillsRsp,
  ListOrdersReq,
  ListOrdersRsp,
  ListPositionsRsp,
  PauseAccountReq,
  PauseAccountRsp,
  PlaceOrderReq,
  PlaceOrderRsp,
  SetLeverageReq,
  SetLeverageRsp,
  SubmitTargetReq,
  SubmitTargetRsp,
  SyncAccountRsp,
  UpdateAccountReq,
  UpdateAccountRsp
} from "./types";

export * from "./types";
export { tradeServiceMap } from "./http";

export function createAccount(req: CreateAccountReq) {
  return callTrade<CreateAccountReq, CreateAccountRsp>("exchangeAccount", "CreateAccount", req);
}

export function updateAccount(req: UpdateAccountReq) {
  return callTrade<UpdateAccountReq, UpdateAccountRsp>("exchangeAccount", "UpdateAccount", req);
}

export function getAccount(exchange_account_id: string) {
  return callTrade<{ exchange_account_id: string }, GetAccountRsp>("exchangeAccount", "GetAccount", {
    exchange_account_id
  });
}

export function listAccounts(req: ListAccountsReq = {}) {
  return callTrade<ListAccountsReq, ListAccountsRsp>("exchangeAccount", "ListAccounts", req);
}

export function setLeverage(req: SetLeverageReq) {
  return callTrade<SetLeverageReq, SetLeverageRsp>("exchangeAccount", "SetLeverage", req);
}

export function pauseAccount(req: PauseAccountReq) {
  return callTrade<PauseAccountReq, PauseAccountRsp>("exchangeAccount", "PauseAccount", req);
}

export function syncAccount(exchange_account_id: string) {
  return callTrade<{ exchange_account_id: string }, SyncAccountRsp>("exchangeAccount", "SyncAccount", {
    exchange_account_id
  });
}

export function placeOrder(req: PlaceOrderReq) {
  return callTrade<PlaceOrderReq, PlaceOrderRsp>("execution", "PlaceOrder", req);
}

export function cancelOrder(order_id: string) {
  return callTrade<{ order_id: string }, CancelOrderRsp>("execution", "CancelOrder", { order_id });
}

export function cancelAllOrders(req: CancelAllOrdersReq) {
  return callTrade<CancelAllOrdersReq, CancelAllOrdersRsp>("execution", "CancelAllOrders", req);
}

export function submitTarget(req: SubmitTargetReq) {
  return callTrade<SubmitTargetReq, SubmitTargetRsp>("execution", "SubmitTarget", req);
}

export function getExecution(execution_id: string) {
  return callTrade<{ execution_id: string }, GetExecutionRsp>("execution", "GetExecution", { execution_id });
}

export function listExecutions(req: ListExecutionsReq = {}) {
  return callTrade<ListExecutionsReq, ListExecutionsRsp>("execution", "ListExecutions", req);
}

export function getOrder(order_id: string) {
  return callTrade<{ order_id: string }, GetOrderRsp>("execution", "GetOrder", { order_id });
}

export function listOrders(req: ListOrdersReq) {
  return callTrade<ListOrdersReq, ListOrdersRsp>("execution", "ListOrders", req);
}

export function listFills(req: ListFillsReq) {
  return callTrade<ListFillsReq, ListFillsRsp>("execution", "ListFills", req);
}

export function listPositions(exchange_account_id: string, symbol = "") {
  return callTrade<{ exchange_account_id: string; symbol: string }, ListPositionsRsp>("execution", "ListPositions", {
    exchange_account_id,
    symbol
  });
}

export const exchangeLabels: Record<number, string> = { 0: "-", 1: "Binance", 2: "OKX" };
export const marketTypeLabels: Record<number, string> = { 0: "-", 1: "SPOT", 2: "SWAP" };
export const executionModeLabels: Record<number, string> = { 0: "-", 1: "Paper", 2: "Live" };
export const orderTypeLabels: Record<number, string> = { 0: "-", 1: "MARKET", 2: "LIMIT" };
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
