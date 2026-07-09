export type CheckKind = 'CHECK_KIND_HTTP' | 'CHECK_KIND_TCP' | number;
export type CheckStatus = 'CHECK_STATUS_OK' | 'CHECK_STATUS_DEGRADED' | 'CHECK_STATUS_DOWN' | number;
export type AlertStatus = 'ALERT_STATUS_OK' | 'ALERT_STATUS_FIRING' | 'ALERT_STATUS_RESOLVED' | number;

export interface PageReq {
  page?: number;
  size?: number;
}

export interface PageResult {
  page?: number;
  size?: number;
  total?: number;
  has_more?: boolean;
}

export interface MonitorCheck {
  space_id?: string;
  check_id?: string;
  name?: string;
  group_name?: string;
  kind?: CheckKind;
  url?: string;
  method?: string;
  headers_json?: string;
  body?: string;
  tcp_host?: string;
  tcp_port?: number;
  interval_seconds?: number;
  timeout_ms?: number;
  expected_status?: string;
  max_response_ms?: number;
  body_contains?: string;
  enabled?: boolean;
  source?: string;
  labels_json?: string;
  description?: string;
  last_checked_at?: string;
  next_check_at?: string;
  created_at?: string;
  updated_at?: string;
}

export interface CheckResult {
  result_id?: string;
  space_id?: string;
  check_id?: string;
  instance_id?: string;
  success?: boolean;
  status?: CheckStatus;
  http_status?: number;
  connected?: boolean;
  latency_ms?: number;
  error_message?: string;
  body_excerpt?: string;
  checked_at?: string;
}

export interface WebhookChannel {
  space_id?: string;
  webhook_id?: string;
  name?: string;
  url?: string;
  method?: string;
  headers_json?: string;
  body_template?: string;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface AlertRule {
  space_id?: string;
  rule_id?: string;
  check_id?: string;
  webhook_id?: string;
  failure_threshold?: number;
  success_threshold?: number;
  minimum_reminder_interval_seconds?: number;
  send_on_resolved?: boolean;
  enabled?: boolean;
  description?: string;
  created_at?: string;
  updated_at?: string;
}

export interface AlertEvent {
  event_id?: string;
  space_id?: string;
  rule_id?: string;
  check_id?: string;
  event_type?: string | number;
  status?: AlertStatus;
  owner_instance_id?: string;
  message?: string;
  payload_json?: string;
  created_at?: string;
}

export interface MonitorInstance {
  instance_id?: string;
  base_url?: string;
  status?: string;
  last_seen_at?: string;
  snapshot_json?: string;
  is_local?: boolean;
}

export interface GroupSummary {
  group_name?: string;
  total_checks?: number;
  healthy_checks?: number;
  degraded_checks?: number;
  down_checks?: number;
}

export interface MonitorOverview {
  total_checks?: number;
  healthy_checks?: number;
  degraded_checks?: number;
  down_checks?: number;
  firing_alerts?: number;
  active_instances?: number;
  success_rate_24h?: number;
  p95_latency_ms?: number;
  groups?: GroupSummary[];
}
