// Trade 模块类型定义（对应 modules/trade/proto/trade_service.proto）

export interface RetInfo {
  code: number | string;
  msg: string;
}

export interface Page {
  page?: number;
  size?: number;
}

export interface PageResult {
  page: number;
  size: number;
  total: number;
  has_more?: boolean;
}

// ========== 枚举 ==========

export type AccountType = 0 | 1 | 2 | 3; // SPOT=0 MARGIN=1 SWAP=2 SIM=3
export type AccountStatus = 0 | 1 | 2 | 3; // DISABLED=0 NORMAL=1 FROZEN=2 READONLY=3
export type MarketType = 0 | 1 | 2 | 3; // SPOT=0 MARGIN=1 SWAP=2 FUTURES=3
export type OrderSide = 0 | 1; // BUY=0 SELL=1
export type OrderType = 0 | 1 | 2 | 3 | 4 | 5 | 6; // LIMIT=0 MARKET=1 STOP=2 STOP_LIMIT=3 POST_ONLY=4 IOC=5 FOK=6
export type OrderStatus = 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7;
export type ChannelStatus = 0 | 1 | 2 | 3; // DISABLED=0 ONLINE=1 OFFLINE=2 ERROR=3

// ========== 账户域消息 ==========

export interface Account {
  account_id: string;
  user_id: string;
  account_name: string;
  account_type: AccountType;
  channel_id: string;
  base_currency: string;
  status: AccountStatus;
  is_default: boolean;
  remark: string;
  created_at: number;
  updated_at: number;
}

export interface Balance {
  account_id: string;
  currency: string;
  available: string;
  frozen: string;
  total: string;
}

export interface FundFlow {
  flow_id: string;
  account_id: string;
  currency: string;
  biz_type: string;
  direction: number;
  amount: string;
  balance_after: string;
  ref_type: string;
  ref_id: string;
  remark: string;
  created_at: number;
}

export interface ApiKey {
  api_key_id: string;
  account_id: string;
  exchange: string;
  api_key: string;
  passphrase: string;
  permissions: string[];
  status: number;
  created_at: number;
}

// ========== 交易域消息 ==========

export interface TradeChannel {
  channel_id: string;
  channel_name: string;
  exchange: string;
  market_type: MarketType;
  account_id: string;
  api_key_id: string;
  endpoint: string;
  is_simulated: boolean;
  status: ChannelStatus;
  rate_limit: number;
  last_heartbeat: number;
  created_at: number;
  updated_at: number;
}

export interface Order {
  order_id: string;
  client_order_id: string;
  exchange_order_id: string;
  account_id: string;
  channel_id: string;
  exchange: string;
  symbol: string;
  market_type: MarketType;
  side: OrderSide;
  pos_side: string;
  order_type: OrderType;
  time_in_force: string;
  price: string;
  quantity: string;
  amount: string;
  filled_qty: string;
  filled_amount: string;
  avg_price: string;
  fee: string;
  fee_currency: string;
  status: OrderStatus;
  reduce_only: boolean;
  trigger_price: string;
  source: string;
  strategy_id: string;
  reject_reason: string;
  submitted_at: number;
  finished_at: number;
  created_at: number;
  updated_at: number;
}

export interface Trade {
  trade_id: string;
  exchange_trade_id: string;
  order_id: string;
  exchange_order_id: string;
  account_id: string;
  channel_id: string;
  exchange: string;
  symbol: string;
  side: OrderSide;
  price: string;
  quantity: string;
  amount: string;
  fee: string;
  fee_currency: string;
  role: string;
  traded_at: number;
}

export interface Position {
  position_id: string;
  account_id: string;
  channel_id: string;
  exchange: string;
  symbol: string;
  pos_side: string;
  quantity: string;
  avg_price: string;
  leverage: string;
  margin: string;
  liq_price: string;
  unrealized_pnl: string;
  realized_pnl: string;
  updated_at: number;
}

// ========== 请求/响应 ==========

// AccountSvc
export interface CreateAccountReq {
  account_name: string;
  account_type: AccountType;
  channel_id?: string;
  base_currency?: string;
  remark?: string;
}
export interface CreateAccountRsp {
  ret_info: RetInfo;
  account_id: string;
  account: Account;
}

export interface UpdateAccountReq {
  account_id: string;
  account_name?: string;
  status?: AccountStatus;
  is_default?: boolean;
  remark?: string;
}
export interface UpdateAccountRsp {
  ret_info: RetInfo;
  account: Account;
}

export interface DeleteAccountReq { account_id: string; }
export interface DeleteAccountRsp { ret_info: RetInfo; }

export interface GetAccountReq { account_id: string; }
export interface GetAccountRsp { ret_info: RetInfo; account: Account; }

export interface ListAccountsReq {
  user_id?: string;
  account_type?: AccountType;
  keyword?: string;
  page?: Page;
}
export interface ListAccountsRsp {
  ret_info: RetInfo;
  accounts: Account[];
  page_result: PageResult;
}

// BalanceSvc
export interface GetBalancesReq {
  account_id: string;
  currencies?: string[];
}
export interface GetBalancesRsp {
  ret_info: RetInfo;
  balances: Balance[];
}

export interface SyncBalancesReq { account_id: string; }
export interface SyncBalancesRsp {
  ret_info: RetInfo;
  balances: Balance[];
}

// FundSvc
export interface ListFundFlowsReq {
  account_id: string;
  currency?: string;
  biz_type?: string;
  start_time?: number;
  end_time?: number;
  page?: Page;
}
export interface ListFundFlowsRsp {
  ret_info: RetInfo;
  flows: FundFlow[];
  page_result: PageResult;
}

export interface TransferReq {
  from_account_id: string;
  to_account_id: string;
  currency: string;
  amount: string;
  remark?: string;
}
export interface TransferRsp {
  ret_info: RetInfo;
  out_flow_id: string;
  in_flow_id: string;
}

// ApiKeySvc
export interface CreateApiKeyReq {
  account_id: string;
  exchange: string;
  api_key: string;
  api_secret: string;
  passphrase?: string;
  permissions?: string[];
}
export interface CreateApiKeyRsp {
  ret_info: RetInfo;
  api_key_id: string;
}

export interface DeleteApiKeyReq { api_key_id: string; }
export interface DeleteApiKeyRsp { ret_info: RetInfo; }

export interface ListApiKeysReq { account_id: string; }
export interface ListApiKeysRsp {
  ret_info: RetInfo;
  api_keys: ApiKey[];
}

// ChannelSvc
export interface CreateChannelReq {
  channel_name: string;
  exchange: string;
  market_type: MarketType;
  account_id: string;
  api_key_id?: string;
  endpoint?: string;
  is_simulated?: boolean;
  rate_limit?: number;
}
export interface CreateChannelRsp {
  ret_info: RetInfo;
  channel_id: string;
}

export interface UpdateChannelReq {
  channel_id: string;
  channel_name?: string;
  status?: ChannelStatus;
  endpoint?: string;
  rate_limit?: number;
}
export interface UpdateChannelRsp { ret_info: RetInfo; }

export interface DeleteChannelReq { channel_id: string; }
export interface DeleteChannelRsp { ret_info: RetInfo; }

export interface ListChannelsReq {
  account_id?: string;
  exchange?: string;
  page?: Page;
}
export interface ListChannelsRsp {
  ret_info: RetInfo;
  channels: TradeChannel[];
  page_result: PageResult;
}

export interface TestChannelReq { channel_id: string; }
export interface TestChannelRsp {
  ret_info: RetInfo;
  reachable: boolean;
  latency_ms: number;
}

// TradeOpSvc
export interface PlaceOrderReq {
  account_id: string;
  channel_id: string;
  client_order_id?: string;
  symbol: string;
  market_type: MarketType;
  side: OrderSide;
  pos_side?: string;
  order_type: OrderType;
  time_in_force?: string;
  price?: string;
  quantity?: string;
  amount?: string;
  reduce_only?: boolean;
  trigger_price?: string;
  source?: string;
  strategy_id?: string;
}
export interface PlaceOrderRsp {
  ret_info: RetInfo;
  order_id: string;
  exchange_order_id: string;
  status: OrderStatus;
}

export interface CancelOrderReq {
  account_id: string;
  channel_id: string;
  order_id?: string;
  client_order_id?: string;
}
export interface CancelOrderRsp {
  ret_info: RetInfo;
  status: OrderStatus;
}

export interface CancelAllOrdersReq {
  account_id: string;
  channel_id: string;
  symbol?: string;
}
export interface CancelAllOrdersRsp {
  ret_info: RetInfo;
  canceled_count: number;
}

export interface AmendOrderReq {
  account_id: string;
  channel_id: string;
  order_id: string;
  new_price?: string;
  new_quantity?: string;
}
export interface AmendOrderRsp {
  ret_info: RetInfo;
  status: OrderStatus;
}

export interface SetLeverageReq {
  account_id: string;
  channel_id: string;
  symbol: string;
  leverage: string;
}
export interface SetLeverageRsp { ret_info: RetInfo; }

// OrderSvc
export interface GetOrderReq {
  order_id?: string;
  client_order_id?: string;
}
export interface GetOrderRsp {
  ret_info: RetInfo;
  order: Order;
}

export interface ListOrdersReq {
  account_id: string;
  channel_id?: string;
  symbol?: string;
  status?: OrderStatus;
  only_open?: boolean;
  start_time?: number;
  end_time?: number;
  page?: Page;
}
export interface ListOrdersRsp {
  ret_info: RetInfo;
  orders: Order[];
  page_result: PageResult;
}

// TradeQuerySvc
export interface ListTradesReq {
  account_id: string;
  order_id?: string;
  symbol?: string;
  start_time?: number;
  end_time?: number;
  page?: Page;
}
export interface ListTradesRsp {
  ret_info: RetInfo;
  trades: Trade[];
  page_result: PageResult;
}

// PositionSvc
export interface ListPositionsReq {
  account_id: string;
  symbol?: string;
}
export interface ListPositionsRsp {
  ret_info: RetInfo;
  positions: Position[];
}
