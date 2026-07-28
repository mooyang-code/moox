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

export type Exchange = 0 | 1 | 2;
export type MarketType = 0 | 1 | 2;
export type ExecutionMode = 0 | 1 | 2;
export type OrderType = 0 | 1 | 2;
export type TimeInForce = 0 | 1 | 2 | 3;
export type PositionSide = 0 | 1;
export type OrderSide = 0 | 1 | 2;

export interface AssetBalance {
  asset: string;
  available: string;
  locked: string;
  total: string;
}

export interface ExchangeAccountSnapshot {
  balances: AssetBalance[];
  equity: string;
  available_funds: string;
  used_margin: string;
  maintenance_margin: string;
  unrealized_pnl: string;
  exchange_updated_at: string;
}

export interface ExchangeAccount {
  exchange_account_id: string;
  space_id: string;
  name: string;
  exchange: Exchange;
  market_type: MarketType;
  execution_mode: ExecutionMode;
  credential_secret_id: string;
  settlement_asset: string;
  margin_mode: string;
  status: string;
  paused: boolean;
  pause_reason: string;
  ready: boolean;
  leverage_settings: Record<string, string>;
  snapshot?: ExchangeAccountSnapshot;
  last_sync_at: string;
  last_ready_at: string;
  last_error: string;
  created_at: string;
  updated_at: string;
  sync_symbols: string[];
}

export interface Order {
  order_id: string;
  exchange_account_id: string;
  client_order_id: string;
  exchange_order_id: string;
  exchange: Exchange;
  market_type: MarketType;
  symbol: string;
  order_type: OrderType;
  time_in_force: TimeInForce;
  side: OrderSide;
  position_side: PositionSide;
  quantity: string;
  limit_price?: string;
  reference_price: string;
  reference_price_at: string;
  reduce_only: boolean;
  source: string;
  strategy_execution_id: string;
  state: string;
  filled_quantity: string;
  average_price: string;
  reserved_asset: string;
  reserved_quantity: string;
  remaining_reserved_quantity: string;
  reject_reason: string;
  version: string;
  submitted_at: string;
  finished_at: string;
  created_at: string;
  updated_at: string;
}

export interface Fill {
  fill_id: string;
  exchange_trade_id: string;
  order_id: string;
  exchange_order_id: string;
  exchange_account_id: string;
  exchange: Exchange;
  market_type: MarketType;
  symbol: string;
  side: OrderSide;
  position_side: PositionSide;
  price: string;
  quantity: string;
  fee: string;
  fee_asset: string;
  settlement_asset: string;
  realized_pnl: string;
  role: string;
  traded_at: string;
  created_at: string;
}

export interface Position {
  exchange_account_id: string;
  symbol: string;
  position_side: PositionSide;
  signed_quantity: string;
  entry_price: string;
  mark_price: string;
  leverage: string;
  margin_mode: string;
  used_margin: string;
  liquidation_price: string;
  unrealized_pnl: string;
  realized_pnl: string;
  exchange_updated_at: string;
  updated_at: string;
}

export interface TargetPosition {
  instrument_id: string;
  symbol: string;
  target_quantity: string;
}

export interface TargetExecution {
  execution_id: string;
  event_id: string;
  strategy_run_id: string;
  execution_binding_id: string;
  exchange_account_id: string;
  command_sequence: string;
  not_after: string;
  data_revision: string;
  targets: TargetPosition[];
  status: string;
  progress: string;
  residual_quantity: string;
  last_error: string;
  created_at: string;
  updated_at: string;
}

export interface CreateAccountReq {
  name: string;
  exchange: Exchange;
  market_type: MarketType;
  execution_mode: ExecutionMode;
  credential_secret_id: string;
  settlement_asset: string;
  margin_mode?: string;
  sync_symbols?: string[];
}
export interface CreateAccountRsp {
  ret_info: RetInfo;
  account: ExchangeAccount;
}

export interface UpdateAccountReq {
  exchange_account_id: string;
  name?: string;
  credential_secret_id?: string;
  settlement_asset?: string;
  margin_mode?: string;
  status?: string;
  sync_symbols?: string[];
}
export interface UpdateAccountRsp {
  ret_info: RetInfo;
  account: ExchangeAccount;
}

export interface GetAccountRsp {
  ret_info: RetInfo;
  account: ExchangeAccount;
}

export interface GetExecutionRsp {
  ret_info: RetInfo;
  execution: TargetExecution;
}

export interface GetOrderRsp {
  ret_info: RetInfo;
  order: Order;
}

export interface ListAccountsReq {
  exchange?: Exchange;
  market_type?: MarketType;
  execution_mode?: ExecutionMode;
  status?: string;
  page?: Page;
}
export interface ListAccountsRsp {
  ret_info: RetInfo;
  accounts: ExchangeAccount[];
  page_result: PageResult;
}

export interface SetLeverageReq {
  exchange_account_id: string;
  symbol: string;
  leverage: string;
}
export interface SetLeverageRsp {
  ret_info: RetInfo;
  account: ExchangeAccount;
}

export interface PauseAccountReq {
  exchange_account_id: string;
  paused: boolean;
  reason: string;
}
export interface PauseAccountRsp {
  ret_info: RetInfo;
  account: ExchangeAccount;
}

export interface SyncAccountRsp {
  ret_info: RetInfo;
  fills_ingested: number;
  orders_updated: number;
  positions_updated: number;
  account_snapshot_updated: boolean;
  unknown_orders_resolved: number;
  ready: boolean;
  warnings: string[];
}

export interface PlaceOrderReq {
  exchange_account_id: string;
  client_order_id: string;
  symbol: string;
  order_type: OrderType;
  time_in_force: TimeInForce;
  side: OrderSide;
  position_side: PositionSide;
  quantity: string;
  limit_price?: string;
  reduce_only: boolean;
  source: string;
  strategy_execution_id?: string;
}
export interface PlaceOrderRsp {
  ret_info: RetInfo;
  order: Order;
}

export interface CancelOrderRsp {
  ret_info: RetInfo;
  order: Order;
}

export interface CancelAllOrdersReq {
  exchange_account_id: string;
  symbol?: string;
}
export interface CancelAllOrdersRsp {
  ret_info: RetInfo;
  canceled_count: number;
}

export interface SubmitTargetReq {
  event_id: string;
  execution_id: string;
  strategy_run_id: string;
  execution_binding_id: string;
  exchange_account_id: string;
  command_sequence: string;
  not_after: string;
  data_revision: string;
  targets: TargetPosition[];
}
export interface SubmitTargetRsp {
  ret_info: RetInfo;
  execution: TargetExecution;
}

export interface ListExecutionsReq {
  exchange_account_id?: string;
  execution_binding_id?: string;
  status?: string;
  page?: Page;
}
export interface ListExecutionsRsp {
  ret_info: RetInfo;
  executions: TargetExecution[];
  page_result: PageResult;
}

export interface ListOrdersReq {
  exchange_account_id: string;
  symbol?: string;
  state?: string;
  only_open?: boolean;
  start_time?: string;
  end_time?: string;
  page?: Page;
}
export interface ListOrdersRsp {
  ret_info: RetInfo;
  orders: Order[];
  page_result: PageResult;
}

export interface ListFillsReq {
  exchange_account_id: string;
  order_id?: string;
  symbol?: string;
  start_time?: string;
  end_time?: string;
  page?: Page;
}
export interface ListFillsRsp {
  ret_info: RetInfo;
  fills: Fill[];
  page_result: PageResult;
}

export interface ListPositionsRsp {
  ret_info: RetInfo;
  positions: Position[];
}
