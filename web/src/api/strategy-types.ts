export type StrategyMode = 'observe' | 'paper' | 'live';
export type StrategyStatus = 'enabled' | 'disabled' | 'running' | 'paused' | 'waiting_data' | 'failed' | 'unknown' | 'stale';
export type PerformanceSource = 'backtest' | 'observe' | 'paper' | 'live';

export interface PageRequest { page?: number; page_size?: number }
export interface PageResponse { total?: number; page?: number; page_size?: number }

export interface StrategyHealth {
  status: string;
  mode: StrategyMode | string;
  last_run_id?: string;
  last_success_at?: string;
  last_error_type?: string;
  last_error_message?: string;
  last_data_revision?: string;
  data_cutoff?: string;
  worker_status?: string;
  outbox_lag_seconds?: number;
  observed_at?: string;
  stale?: boolean;
}

export interface RunningStrategySummary {
  strategy_id: string;
  version: string;
  binding_id: string;
  space_id: string;
  view_id?: string;
  freq?: string;
  mode: StrategyMode | string;
  status: string;
  source_hash?: string;
  last_run_id?: string;
  last_run_at?: string;
  last_data_revision?: string;
  last_duration_ms?: number;
  health?: StrategyHealth;
}

export interface StrategyDefinition {
  strategy_id: string;
  version: string;
  api_version?: string;
  source_hash?: string;
  status?: string;
}

export interface StrategyBinding {
  binding_id: string;
  strategy_id: string;
  strategy_version: string;
  space_id: string;
  view_id?: string;
  freq?: string;
  params_json?: string;
  group_id?: string;
  capital_weight?: string;
  status?: string;
}

export interface StrategyState {
  binding_id: string;
  revision: number;
  state_json?: string;
  last_run_id?: string;
}

export interface StrategyRun {
  run_id: string;
  binding_id: string;
  trigger_bar_time: string;
  data_revision?: string;
  action?: string;
  status?: string;
  output_json?: string;
}

export interface TargetWeight {
  instrument_id: string;
  target_weight: string;
  symbol?: string;
  market_type?: string;
}

export interface PerformancePoint {
  point_time: string;
  nav: string;
  cumulative_return: string;
  drawdown: string;
  gross_exposure: string;
  net_exposure: string;
  turnover: string;
  fees: string;
  data_revision?: string;
}

export interface PerformanceSummary {
  nav?: string;
  return_value?: string;
  max_drawdown?: string;
  turnover?: string;
  fees?: string;
  volatility?: string;
  win_rate?: string;
  as_of?: string;
  status?: 'ok' | 'insufficient_data' | 'stale' | string;
}

export interface StrategyPerformance {
  performance_source: PerformanceSource | string;
  summary?: PerformanceSummary;
  points: PerformancePoint[];
  data_revision?: string;
  as_of?: string;
}

export interface StrategyOverview {
  summary?: RunningStrategySummary;
  binding?: StrategyBinding;
  definition?: StrategyDefinition;
  state?: StrategyState;
  health?: StrategyHealth;
}
