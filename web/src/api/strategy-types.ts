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
  dsl_yaml: string;
  created_at: string;
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

export interface StrategyResult {
  result_id: string;
  instance_id?: string;
  session_id?: string;
  bar_end_time?: string;
  period_time?: string;
  valid_until?: string;
  targets: InstrumentTarget[];
  rule_states_json?: string;
  publish_status?: "none" | "pending" | "sent" | "cancelled" | string;
  created_at: string;
}

export interface StrategyTargetSnapshot {
  targets: InstrumentTarget[];
  session_id: string;
  bar_end_time: string;
  valid_until: string;
}
