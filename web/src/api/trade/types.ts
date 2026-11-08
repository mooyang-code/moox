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
/** 1 现货、2 永续、3 股票 */
export type MarketType = 0 | 1 | 2 | 3;
export type ExecutionMode = 0 | 1 | 2;
export type ControlMode = 0 | 1 | 2;
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
  /** 余额所在的钱包：SPOT、FUTURES、MARGIN、UNIFIED */
  wallet?: string;
  borrowed?: string;
}

export interface TradingAccountSnapshot {
  balances: AssetBalance[];
  equity: string;
  available_funds: string;
  used_margin: string;
  maintenance_margin: string;
  unrealized_pnl: string;
  exchange_updated_at: string;
  /** 交易所给出的总权益，只用于展示核对 */
  exchange_equity?: string;
}

export interface TradingAccount {
  trading_account_id: string;
  space_id: string;
  name: string;
  exchange: Exchange;
  /** 账户已开通的市场，按现货、永续、股票的固定顺序排列 */
  markets: MarketType[];
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
  live?: LiveConfig;
  paper?: PaperConfig;
  /** 账户类型，只由交易所检测写入：BINANCE_PORTFOLIO_MARGIN、BINANCE_STANDARD、OKX_UNIFIED；模拟盘和股票为空 */
  account_mode?: string;
  /** 最近一次检测的结果（JSON） */
  inspection_json?: string;
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
  owner_instance_id?: string;
  owner_session_id?: string;
  control_mode: ControlMode;
  execution_mode: ExecutionMode;
  /** 可交易的市场：启用成员已开通市场的并集（只读，由 Trade 计算）；策略要输出的市场必须都在里面。 */
  markets: MarketType[];
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
  /** 伪市价单的第几次尝试，从 0 开始 */
  attempt?: number;
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
  /** 持仓所属市场；现货和合约的持仓互不抵消 */
  market_type: MarketType;
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
  hold?: boolean;
}

export interface TradableInstrument {
  instrument_id: string;
  quantity_step: string;
  min_quantity: string;
  min_notional: string;
  price_tick: string;
}

export interface BlockedTarget extends InstrumentTarget {
  reason: string;
  /** 不是被拦下，而是被单币每小时上限顺延到下一个整点：用中性色显示“顺延”，不计入“被拦下” */
  deferred?: boolean;
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
  /** 下不了单的现货零头，不算剩余 */
  dust_positions?: RemainingPosition[];
  error?: string;
}

export interface FlattenResult {
  accounts: FlattenAccountResult[];
}

export interface CreateTradingAccountReq {
  name: string;
  exchange: Exchange;
  /** 要开通的市场：加密货币取现货、永续的子集 */
  markets: MarketType[];
  settlement_asset: string;
  margin_mode?: string;
  live: LiveConfig;
}

export interface UpdateTradingAccountReq {
  trading_account_id: string;
  name?: string;
  credential_secret_id?: string;
  settlement_asset?: string;
  margin_mode?: string;
  status?: string;
}

export interface ListTradingAccountsReq {
  exchange?: Exchange;
  /** 只列已开通了该市场的账户 */
  market_type?: MarketType;
  execution_mode?: ExecutionMode;
  status?: string;
  page?: Page;
}

export interface CreateLogicalAccountReq {
  name: string;
  control_mode?: ControlMode;
  execution_mode: ExecutionMode;
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
  deadline_at?: string;
  /** 订单所在市场；账户开通了多个市场时必填 */
  market_type?: MarketType;
}

export interface SubmitOrderReq extends PlaceManualOrderReq {
  logical_account_id: string;
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
  /** 订单来源：TARGET、OPERATOR、EXTERNAL、SYSTEM；空表示全部 */
  owner_type?: string;
}

export interface ListFillsReq {
  logical_account_id?: string;
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
  control_mode?: ControlMode;
  logical_account_name: string;
  exchange: Exchange;
  /** 要开通的市场集合；模拟账户用一个资金池同时撮合开通的各个市场 */
  markets: MarketType[];
  settlement_asset: string;
  margin_mode?: string;
  initial_balance: string;
  maker_fee_rate: string;
  taker_fee_rate: string;
  slippage_bps: string;
  /** 加入已有的组合账户：后端在创建账户的同一个事务里加成启用成员，此时 logical_account_name 必须为空 */
  logical_account_id?: string;
  /** 加入已有组合时的优先级；不填排在现有成员之后 */
  priority?: number;
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
  /** 能否查询资金流水（币安现货没有统一流水接口） */
  ledger?: boolean;
}

/** 资金流水类型：TRADE、FEE、REALIZED_PNL、FUNDING_FEE、TRANSFER、LIQUIDATION、INTEREST、OTHER */
export type LedgerType = "TRADE" | "FEE" | "REALIZED_PNL" | "FUNDING_FEE" | "TRANSFER" | "LIQUIDATION" | "INTEREST" | "OTHER";

export interface LedgerEntry {
  id: string;
  time: string;
  type: LedgerType;
  exchange_type?: string;
  asset: string;
  amount: string;
  exchange_symbol?: string;
  trade_id?: string;
  info?: string;
  market_type?: MarketType;
}

export interface ListAccountLedgerReq {
  trading_account_id: string;
  /** 要查流水的市场；账户只有一个市场支持流水时可省略 */
  market_type?: MarketType;
  start_time?: string;
  end_time?: string;
  limit?: number;
}

// 交易所密钥检测（InspectExchangeCredential、ReinspectTradingAccount）的返回。
export interface InspectionFinding {
  code: string;
  message: string;
}

export interface ExchangeInspection {
  account_mode?: string;
  skipped?: boolean;
  can_withdraw?: boolean;
  can_trade_spot?: boolean;
  can_trade_swap?: boolean;
  universal_transfer?: boolean;
  ip_restricted?: boolean;
  raw_permissions?: string;
  okx_acct_lv?: string;
  position_mode?: string;
  multi_assets_margin?: boolean;
  available_markets?: MarketType[];
  blockers?: InspectionFinding[];
  warnings?: InspectionFinding[];
  inspected_at?: number | string;
  consecutive_failures?: number;
  last_failure?: string;
}

export interface InspectExchangeCredentialReq {
  exchange: Exchange;
  environment: AccountEnvironment;
  credential_secret_id: string;
  markets?: MarketType[];
}

/** 一个资产加一个金额；金额是字符串小数 */
export interface PnlAmount {
  asset: string;
  amount: string;
}

/** logical_account_id 和 trading_account_id 二选一；时间是毫秒时间戳，不传时后端默认近 7 天 */
export interface SummarizeAccountPnlReq {
  logical_account_id?: string;
  trading_account_id?: string;
  start_time?: string;
  end_time?: string;
}

/** 收益拆解。proto3 的零值不出现在 JSON 里：没有的数组就是空，没有的布尔就是 false */
export interface SummarizeAccountPnlRsp {
  ret_info: RetInfo;
  /** 手续费，按手续费资产分别汇总，不折算；正数是支付 */
  fees?: PnlAmount[];
  /** 已实现盈亏，按结算资产汇总 */
  realized_pnl?: PnlAmount[];
  /** 资金费，按结算资产汇总，收到为正、支付为负 */
  funding_fees?: PnlAmount[];
  fill_count?: string;
  /** false 表示没取到资金费（没有永续市场、交易所不支持），原因见 funding_note */
  funding_available?: boolean;
  /** true 表示资金费只是一部分：流水太多没取完 */
  funding_truncated?: boolean;
  funding_note?: string;
  /** 资金费实际统计的起点（毫秒时间戳） */
  funding_from?: string;
}

/** 操作类型：MANUAL_ORDER、SUBMIT_ORDER、CANCEL_ORDER、FLATTEN、REDUCE、DELIST_FLATTEN */
export type OperatorActionType = "MANUAL_ORDER" | "SUBMIT_ORDER" | "CANCEL_ORDER" | "FLATTEN" | "REDUCE" | "DELIST_FLATTEN";

export interface ListOperatorActionsReq {
  logical_account_id: string;
  action_type?: OperatorActionType;
  start_time?: string;
  end_time?: string;
  page?: Page;
}
