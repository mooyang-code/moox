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
  quantity: string;
}

export interface Strategy {
  strategy_id: string;
  name: string;
  manifest_yaml: string;
  source_code: string;
  source_hash: string;
  created_at: string;
}

export interface StrategyRunner {
  runner_id: string;
  strategy_id: string;
  space_id: string;
  view_id: string;
  frequency: string;
  params_json: string;
  logical_account_id: string;
  status: string;
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
  trigger_bar_time: string;
  namespace: string;
  input_hash: string;
  action: string;
  output_json: string;
  command_sequence?: string;
  created_at: string;
}

export interface EngineStatus {
  workers: number;
  ready_workers: number;
}
