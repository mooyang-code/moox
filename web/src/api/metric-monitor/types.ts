export const METRIC_HISTORY_LIMIT = 500;
export const METRIC_PREVIEW_LIMIT = 50;
export const METRIC_SERIES_LIMIT = 200;
export const METRIC_RULE_LIMIT = 100;

export const LogicalOperator = {
  AND: 1,
  OR: 2,
} as const;
export type LogicalOperatorValue = (typeof LogicalOperator)[keyof typeof LogicalOperator] | number;

export const CompareOperator = {
  GT: 1,
  GTE: 2,
  LT: 3,
  LTE: 4,
  EQ: 5,
  NEQ: 6,
} as const;
export type CompareOperatorValue = (typeof CompareOperator)[keyof typeof CompareOperator] | number;

export const TimeReducer = {
  CURRENT: 1,
  AVG: 2,
  MIN: 3,
  MAX: 4,
  SUM: 5,
  RATE: 6,
  INCREASE: 7,
} as const;
export type TimeReducerValue = (typeof TimeReducer)[keyof typeof TimeReducer] | number;

export const SeriesReducer = {
  AVG: 1,
  MIN: 2,
  MAX: 3,
  SUM: 4,
} as const;
export type SeriesReducerValue = (typeof SeriesReducer)[keyof typeof SeriesReducer] | number;

export const NoDataPolicy = {
  KEEP_STATE: 1,
  OK: 2,
  FIRING: 3,
} as const;
export type NoDataPolicyValue = (typeof NoDataPolicy)[keyof typeof NoDataPolicy] | number;

export type AlertStatusValue = number | string;

export interface PageRequest {
  page?: number;
  size?: number;
}

export interface PageResult {
  page?: number;
  size?: number;
  total?: number;
  has_more?: boolean;
}

export interface MetricServiceInfo {
  service_name?: string;
  instance_id?: string;
  boot_id?: string;
  node_id?: string;
  version?: string;
  last_seen_at?: string;
  stale?: boolean;
}

export interface MetricNameInfo {
  service_name?: string;
  metric_name?: string;
  metric_type?: string;
  help?: string;
  series_count?: number;
  last_seen_at?: string;
}

export interface MetricSeriesInfo {
  series_id?: string;
  service_name?: string;
  instance_id?: string;
  metric_name?: string;
  metric_type?: string;
  labels_json?: string;
  last_seen_at?: string;
  stale?: boolean;
}

export interface MetricLatestPoint {
  series_id?: string;
  service_name?: string;
  instance_id?: string;
  metric_name?: string;
  metric_type?: string;
  labels_json?: string;
  value?: number;
  observed_at?: string;
  stale?: boolean;
  message_id?: string;
}

export interface MetricHistoryPoint {
  series_id?: string;
  value?: number;
  observed_at?: string;
  labels_json?: string;
  message_id?: string;
}

export interface LabelMatcher {
  name: string;
  value: string;
  negate?: boolean;
}

export interface MetricSelector {
  service_name: string;
  metric_name: string;
  matchers?: LabelMatcher[];
}

export interface MetricQuery {
  selector: MetricSelector;
  time_reducer: TimeReducerValue;
  window_seconds?: number;
  series_reducer: SeriesReducerValue;
}

export interface MetricCondition {
  condition_id: string;
  query: MetricQuery;
  compare: CompareOperatorValue;
  threshold: number;
  no_data_policy: NoDataPolicyValue;
}

export type MetricConditionPatch = Omit<Partial<MetricCondition>, 'query'> & {
  query?: {
    selector?: Partial<MetricSelector>;
    time_reducer?: TimeReducerValue;
    window_seconds?: number;
    series_reducer?: SeriesReducerValue;
  };
};

export interface MetricRule {
  space_id?: string;
  rule_id?: string;
  name: string;
  conditions: MetricCondition[];
  connector: LogicalOperatorValue;
  consecutive_trigger_count: number;
  consecutive_recovery_count: number;
  evaluation_interval_seconds: number;
  webhook_ids: string[];
  enabled: boolean;
  description?: string;
  created_at?: string;
  updated_at?: string;
}

export interface MetricConditionEvaluation {
  condition_id?: string;
  selected_series_count?: number;
  value?: number;
  threshold?: number;
  has_data?: boolean;
  result?: boolean;
  no_data_reason?: string;
}

export interface MetricRuleEvaluation {
  evaluation_id?: string;
  space_id?: string;
  rule_id?: string;
  evaluated_at?: string;
  status?: AlertStatusValue;
  result?: boolean;
  conditions?: MetricConditionEvaluation[];
  result_json?: string;
}

export interface MetricRuleState {
  space_id?: string;
  rule_id?: string;
  status?: AlertStatusValue;
  trigger_count?: number;
  recovery_count?: number;
  owner_instance_id?: string;
  last_evaluated_at?: string;
  last_triggered_at?: string;
  last_recovered_at?: string;
}

export interface WebhookChannel {
  space_id?: string;
  webhook_id?: string;
  name?: string;
  url?: string;
  enabled?: boolean;
}

export interface MetricQueryFilters {
  space_id?: string;
  service_name?: string;
  metric_name?: string;
  labels_json?: string;
  page?: PageRequest;
}
