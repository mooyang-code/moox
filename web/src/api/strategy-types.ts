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
  strategy_name?: string;
  kind?: string;
  dsl_yaml: string;
  /** @deprecated read-only compatibility for the legacy console. */
  manifest_yaml?: string;
  /** @deprecated compiled artifacts are no longer persisted by Strategy. */
  compiled_json?: string;
  /** @deprecated definitions no longer expose a source hash. */
  source_hash?: string;
  created_at: string;
  updated_at?: string;
}

export interface StrategyInstance {
  instance_id: string;
  strategy_id: string;
  space_id: string;
  input_bindings_json: string;
  logical_account_id: string;
  enabled: boolean;
  session_id: string;
  created_at: string;
  updated_at: string;
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
  instance_id?: string;
  session_id?: string;
  runner_id?: string;
  strategy_id?: string;
  bar_end_time?: string;
  period_time?: string;
  valid_until?: string;
  targets: InstrumentTarget[];
  input_hash?: string;
  action?: string;
  debug_info_json?: string;
  rule_states_json?: string;
  publish_status?: "none" | "pending" | "sent" | "cancelled" | string;
  command_sequence?: string;
  created_at: string;
}
