import { callTrade } from './http';
import type {
  CreateAccountReq, CreateAccountRsp,
  UpdateAccountReq, UpdateAccountRsp,
  DeleteAccountReq, DeleteAccountRsp,
  GetAccountReq, GetAccountRsp,
  ListAccountsReq, ListAccountsRsp,
  GetBalancesReq, GetBalancesRsp,
  SyncBalancesReq, SyncBalancesRsp,
  ListFundFlowsReq, ListFundFlowsRsp,
  TransferReq, TransferRsp,
  CreateApiKeyReq, CreateApiKeyRsp,
  DeleteApiKeyReq, DeleteApiKeyRsp,
  ListApiKeysReq, ListApiKeysRsp,
  CreateChannelReq, CreateChannelRsp,
  UpdateChannelReq, UpdateChannelRsp,
  DeleteChannelReq, DeleteChannelRsp,
  ListChannelsReq, ListChannelsRsp,
  TestChannelReq, TestChannelRsp,
  PlaceOrderReq, PlaceOrderRsp,
  CancelOrderReq, CancelOrderRsp,
  CancelAllOrdersReq, CancelAllOrdersRsp,
  AmendOrderReq, AmendOrderRsp,
  SetLeverageReq, SetLeverageRsp,
  GetOrderReq, GetOrderRsp,
  ListOrdersReq, ListOrdersRsp,
  ListTradesReq, ListTradesRsp,
  ListPositionsReq, ListPositionsRsp,
} from './types';

// ========== AccountSvc ==========

export function createAccount(req: CreateAccountReq) {
  return callTrade<CreateAccountReq, CreateAccountRsp>('account', 'CreateAccount', req);
}

export function updateAccount(req: UpdateAccountReq) {
  return callTrade<UpdateAccountReq, UpdateAccountRsp>('account', 'UpdateAccount', req);
}

export function deleteAccount(account_id: string) {
  return callTrade<DeleteAccountReq, DeleteAccountRsp>('account', 'DeleteAccount', { account_id });
}

export function getAccount(account_id: string) {
  return callTrade<GetAccountReq, GetAccountRsp>('account', 'GetAccount', { account_id });
}

export function listAccounts(params: ListAccountsReq) {
  return callTrade<ListAccountsReq, ListAccountsRsp>('account', 'ListAccounts', params);
}

// ========== BalanceSvc ==========

export function getBalances(account_id: string, currencies?: string[]) {
  return callTrade<GetBalancesReq, GetBalancesRsp>('balance', 'GetBalances', { account_id, currencies });
}

export function syncBalances(account_id: string) {
  return callTrade<SyncBalancesReq, SyncBalancesRsp>('balance', 'SyncBalances', { account_id });
}

// ========== FundSvc ==========

export function listFundFlows(params: ListFundFlowsReq) {
  return callTrade<ListFundFlowsReq, ListFundFlowsRsp>('fund', 'ListFundFlows', params);
}

export function transfer(req: TransferReq) {
  return callTrade<TransferReq, TransferRsp>('fund', 'Transfer', req);
}

// ========== ApiKeySvc ==========

export function createApiKey(req: CreateApiKeyReq) {
  return callTrade<CreateApiKeyReq, CreateApiKeyRsp>('apikey', 'CreateApiKey', req);
}

export function deleteApiKey(api_key_id: string) {
  return callTrade<DeleteApiKeyReq, DeleteApiKeyRsp>('apikey', 'DeleteApiKey', { api_key_id });
}

export function listApiKeys(account_id: string) {
  return callTrade<ListApiKeysReq, ListApiKeysRsp>('apikey', 'ListApiKeys', { account_id });
}

// ========== ChannelSvc ==========

export function createChannel(req: CreateChannelReq) {
  return callTrade<CreateChannelReq, CreateChannelRsp>('channel', 'CreateChannel', req);
}

export function updateChannel(req: UpdateChannelReq) {
  return callTrade<UpdateChannelReq, UpdateChannelRsp>('channel', 'UpdateChannel', req);
}

export function deleteChannel(channel_id: string) {
  return callTrade<DeleteChannelReq, DeleteChannelRsp>('channel', 'DeleteChannel', { channel_id });
}

export function listChannels(params: ListChannelsReq) {
  return callTrade<ListChannelsReq, ListChannelsRsp>('channel', 'ListChannels', params);
}

export function testChannel(channel_id: string) {
  return callTrade<TestChannelReq, TestChannelRsp>('channel', 'TestChannel', { channel_id });
}

// ========== TradeOpSvc ==========

export function placeOrder(req: PlaceOrderReq) {
  return callTrade<PlaceOrderReq, PlaceOrderRsp>('tradeop', 'PlaceOrder', req);
}

export function cancelOrder(req: CancelOrderReq) {
  return callTrade<CancelOrderReq, CancelOrderRsp>('tradeop', 'CancelOrder', req);
}

export function cancelAllOrders(req: CancelAllOrdersReq) {
  return callTrade<CancelAllOrdersReq, CancelAllOrdersRsp>('tradeop', 'CancelAllOrders', req);
}

export function amendOrder(req: AmendOrderReq) {
  return callTrade<AmendOrderReq, AmendOrderRsp>('tradeop', 'AmendOrder', req);
}

export function setLeverage(req: SetLeverageReq) {
  return callTrade<SetLeverageReq, SetLeverageRsp>('tradeop', 'SetLeverage', req);
}

// ========== OrderSvc ==========

export function getOrder(order_id: string) {
  return callTrade<GetOrderReq, GetOrderRsp>('order', 'GetOrder', { order_id });
}

export function listOrders(params: ListOrdersReq) {
  return callTrade<ListOrdersReq, ListOrdersRsp>('order', 'ListOrders', params);
}

// ========== TradeQuerySvc ==========

export function listTrades(params: ListTradesReq) {
  return callTrade<ListTradesReq, ListTradesRsp>('tradeq', 'ListTrades', params);
}

// ========== PositionSvc ==========

export function listPositions(account_id: string, symbol?: string) {
  return callTrade<ListPositionsReq, ListPositionsRsp>('position', 'ListPositions', { account_id, symbol });
}

// ========== 枚举标签映射 ==========

export const accountTypeLabels: Record<number, string> = {
  0: '现货', 1: '杠杆', 2: '合约', 3: '模拟',
};

export const accountStatusLabels: Record<number, string> = {
  0: '禁用', 1: '正常', 2: '冻结', 3: '只读',
};

export const accountStatusColors: Record<number, string> = {
  0: 'gray', 1: 'green', 2: 'orange', 3: 'blue',
};

export const marketTypeLabels: Record<number, string> = {
  0: '现货', 1: '杠杆', 2: '永续', 3: '交割',
};

export const orderSideLabels: Record<number, string> = {
  0: '买入', 1: '卖出',
};

export const orderSideColors: Record<number, string> = {
  0: 'red', 1: 'green',
};

export const orderTypeLabels: Record<number, string> = {
  0: '限价', 1: '市价', 2: '止损市价', 3: '止损限价', 4: '只挂单', 5: 'IOC', 6: 'FOK',
};

export const orderStatusLabels: Record<number, string> = {
  0: '待提交', 1: '已提交', 2: '部分成交', 3: '完全成交', 4: '已撤销', 5: '部分撤销', 6: '拒绝', 7: '过期',
};

export const orderStatusColors: Record<number, string> = {
  0: 'gray', 1: 'blue', 2: 'orange', 3: 'green', 4: 'gray', 5: 'orange', 6: 'red', 7: 'gray',
};

export const channelStatusLabels: Record<number, string> = {
  0: '禁用', 1: '在线', 2: '离线', 3: '异常',
};

export const channelStatusColors: Record<number, string> = {
  0: 'gray', 1: 'green', 2: 'gray', 3: 'red',
};

export const bizTypeLabels: Record<string, string> = {
  deposit: '充值', withdraw: '提现', transfer_in: '转入', transfer_out: '转出',
  trade: '成交', fee: '手续费', funding: '资金费', adjust: '调整',
};

// ========== 工具函数 ==========

export function formatTimestamp(ts?: number): string {
  if (!ts) return '-';
  return new Date(ts * 1000).toLocaleString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  });
}
