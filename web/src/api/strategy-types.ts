export interface PageRequest {
  page?: number;
  page_size?: number;
}

export interface PageResponse {
  total: number;
  page?: number;
  page_size?: number;
}

export interface InstrumentTarget {
  instrument_id: string;
  target_weight: string;
}

export interface Strategy {
  strategy_id: string;
  name: string;
  kind: string;
  manifest_yaml: string;
  compiled_json: string;
  source_hash: string;
  created_at: string;
}

export type StrategyRunnerStatus = "ENABLED" | "DISABLED";

export interface StrategyRunner {
  runner_id: string;
  strategy_id: string;
  space_id: string;
  source_view_id: string;
  frequency: string;
  logical_account_id: string;
  status: StrategyRunnerStatus;
  current_targets: InstrumentTarget[];
  command_sequence: string;
  last_result_id: string;
  last_success_at: string;
  last_error: string;
  created_at: string;
  updated_at: string;
}

export interface StrategyResult {
  result_id: string;
  runner_id: string;
  strategy_id: string;
  period_time: string;
  targets: InstrumentTarget[];
  input_hash: string;
  action: string;
  debug_info_json: string;
  command_sequence?: string;
  created_at: string;
}
