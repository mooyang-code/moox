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
export type AccountEnvironment = 0 | 1 | 2;
export type OrderType = 0 | 1 | 2;
export type FillPolicy = 0 | 1 | 2 | 3;
export type PositionSide = 0 | 1;
export type OrderSide = 0 | 1 | 2;

export interface AssetBalance {
  asset: string;
  available: string;
  locked: string;
  total: string;
}

export interface TradingAccountSnapshot {
  balances: AssetBalance[];
  equity: string;
  available_funds: string;
  used_margin: string;
  maintenance_margin: string;
  unrealized_pnl: string;
  exchange_updated_at: string;
}

export interface TradingAccount {
  trading_account_id: string;
  space_id: string;
  name: string;
  exchange: Exchange;
  market_type: MarketType;
  execution_mode: ExecutionMode;
  settlement_asset: string;
  margin_mode: string;
  status: string;
  ready: boolean;
  leverage_settings: Record<string, string>;
  snapshot?: TradingAccountSnapshot;
  last_sync_at: string;
  last_ready_at: string;
  last_error: string;
  created_at: string;
  updated_at: string;
  sync_symbols: string[];
  live?: LiveConfig;
  paper?: PaperConfig;
}

export interface LiveConfig {
  environment: AccountEnvironment;
  credential_secret_id: string;
}

export interface PaperConfig {
  initial_balance: string;
  maker_fee_rate: string;
  taker_fee_rate: string;
  slippage_bps: string;
}

export interface LogicalAccountMember {
  trading_account_id: string;
  enabled: boolean;
  priority: number;
}

export interface LogicalAccount {
  logical_account_id: string;
  space_id: string;
  name: string;
  /** @deprecated compatibility field for legacy strategy runners. */
  owner_runner_id?: string;
  owner_instance_id?: string;
  owner_session_id?: string;
  execution_mode: ExecutionMode;
  market_type: MarketType;
  settlement_asset: string;
  automation_state: "ACTIVE" | "PAUSED" | string;
  pause_reason: string;
  members: LogicalAccountMember[];
  ready: boolean;
  readiness_reasons: string[];
  created_at: string;
  updated_at: string;
}

export interface Order {
  order_id: string;
  trading_account_id: string;
  client_order_id: string;
  exchange_order_id: string;
  exchange: Exchange;
  market_type: MarketType;
  instrument_id: string;
  exchange_symbol: string;
  order_type: OrderType;
  fill_policy: FillPolicy;
  side: OrderSide;
  position_side: PositionSide;
  quantity: string;
  limit_price?: string;
  reference_price: string;
  reference_price_at: string;
  reduce_position_only: boolean;
  owner_type: string;
  owner_id: string;
  logical_account_id: string;
  runner_id: string;
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
  trading_account_id: string;
  exchange: Exchange;
  market_type: MarketType;
  instrument_id: string;
  exchange_symbol: string;
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
  trading_account_id: string;
  instrument_id: string;
  exchange_symbol: string;
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

export interface InstrumentTarget {
  instrument_id: string;
  quantity: string;
}

export interface BlockedTarget extends InstrumentTarget {
  reason: string;
}

export interface LogicalAccountTarget {
  target_id: string;
  logical_account_id: string;
  runner_id?: string;
  command_sequence?: string;
  targets: InstrumentTarget[];
  status: string;
  blocked_targets: BlockedTarget[];
  last_error: string;
  accepted_at: string;
  updated_at: string;
  instance_id?: string;
  session_id?: string;
  strategy_id?: string;
  bar_end_time?: number | string;
  effective_at?: number | string;
  valid_until?: number | string;
}

export interface OperatorAction {
  action_id: string;
  logical_account_id: string;
  action_type: string;
  reason: string;
  status: string;
  result_json: string;
  last_error: string;
  created_at: string;
  updated_at: string;
}

export interface RemainingPosition {
  instrument_id?: string;
  asset?: string;
  quantity: string;
  reason: string;
}

export interface FlattenAccountResult {
  trading_account_id: string;
  status: string;
  child_order_ids?: string[];
  remaining_positions?: RemainingPosition[];
  error?: string;
}

export interface FlattenResult {
  accounts: FlattenAccountResult[];
}

export interface CreateTradingAccountReq {
  name: string;
  exchange: Exchange;
  market_type: MarketType;
  settlement_asset: string;
  margin_mode?: string;
  sync_symbols?: string[];
  live: LiveConfig;
}

export interface UpdateTradingAccountReq {
  trading_account_id: string;
  name?: string;
  credential_secret_id?: string;
  settlement_asset?: string;
  margin_mode?: string;
  status?: string;
  sync_symbols?: string[];
}

export interface ListTradingAccountsReq {
  exchange?: Exchange;
  market_type?: MarketType;
  execution_mode?: ExecutionMode;
  status?: string;
  page?: Page;
}

export interface CreateLogicalAccountReq {
  name: string;
  execution_mode: ExecutionMode;
  market_type: MarketType;
  settlement_asset: string;
}

export interface AddLogicalAccountMemberReq {
  logical_account_id: string;
  trading_account_id: string;
  enabled: boolean;
  priority: number;
  adopt_existing_exposure: boolean;
}

export interface PlaceManualOrderReq {
  action_id: string;
  trading_account_id: string;
  client_order_id: string;
  instrument_id: string;
  order_type: OrderType;
  fill_policy: FillPolicy;
  side: OrderSide;
  position_side?: PositionSide;
  quantity: string;
  limit_price?: string;
  reason: string;
}

export interface ListOrdersReq {
  logical_account_id?: string;
  trading_account_id?: string;
  instrument_id?: string;
  state?: string;
  only_open?: boolean;
  start_time?: string;
  end_time?: string;
  page?: Page;
}

export interface ListFillsReq {
  trading_account_id?: string;
  order_id?: string;
  instrument_id?: string;
  start_time?: string;
  end_time?: string;
  page?: Page;
}

export interface ListPositionsReq {
  logical_account_id?: string;
  trading_account_id?: string;
  instrument_id?: string;
}

export interface AccountResponse {
  ret_info: RetInfo;
  account: TradingAccount;
}

export interface LogicalAccountResponse {
  ret_info: RetInfo;
  logical_account: LogicalAccount;
}

export interface CreatePaperSimulationReq {
  account_name: string;
  logical_account_name: string;
  exchange: Exchange;
  market_type: MarketType;
  settlement_asset: string;
  margin_mode?: string;
  initial_balance: string;
  maker_fee_rate: string;
  taker_fee_rate: string;
  slippage_bps: string;
}

export interface EquityPoint {
  bucket_time: string;
  equity: string;
  available_funds: string;
  used_margin: string;
  unrealized_pnl?: string;
  source_time: string;
}

export interface Holding {
  trading_account_id: string;
  instrument_id: string;
  exchange_symbol: string;
  asset: string;
  quantity: string;
  average_cost: string;
  mark_price: string;
  market_value: string;
  unrealized_pnl?: string;
  source_time: string;
}

export interface ExecutionCapabilities {
  can_place_order: boolean;
  unavailable_reason: string;
  order_types: OrderType[];
  fill_policies: FillPolicy[];
  can_close_paper_simulation: boolean;
}
