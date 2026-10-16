export type HealthStatus = "healthy" | "degraded" | "down" | "unknown" | "disabled" | "unchecked";
export interface HealthSignal {
  status?: HealthStatus;
  reason?: string;
  raw_error?: string;
  checked_at?: string;
}
export interface HealthReporter {
  instance_id?: string;
  boot_id?: string;
  version?: string;
  status?: HealthStatus;
  last_reported_at?: string;
}
export interface HealthComponent {
  host_id?: string;
  component_id?: string;
  name?: string;
  status?: HealthStatus;
  reason?: string;
  probe?: HealthSignal;
  reporter?: HealthSignal;
  instances?: HealthReporter[];
  status_since?: string;
  probe_url?: string;
  recent_probes?: HealthSignal[];
}
export interface HealthObject {
  type?: "component" | "dataset" | "host" | "business";
  host_id?: string;
  component_id?: string;
  agent_id?: string;
  dataset_id?: string;
  freq?: string;
  kind?: string;
  space_id?: string;
  producer?: string;
}
/** critical：服务不可用、数据中断、主机失联或网关异常；warning：资源超阈值、指标上报中断等风险。 */
export type AlertSeverity = "critical" | "warning";
export interface HealthAlert {
  id?: string;
  severity?: AlertSeverity;
  object?: HealthObject;
  title?: string;
  reason?: string;
  raw_error?: string;
  triggered_at?: string;
  last_checked_at?: string;
  /** 最近一次检查已通过，正在等待连续成功确认恢复。 */
  recovering?: boolean;
  /** 数据集告警对应的数据集事实。 */
  dataset?: HealthDataset;
}
/** 一次告警经过：触发到恢复为一条。 */
export interface AlertIncident {
  id?: string;
  severity?: AlertSeverity;
  object?: HealthObject;
  title?: string;
  reason?: string;
  /** 触发早于窗口且找不到触发记录时为空。 */
  triggered_at?: string;
  /** 为空表示没有恢复记录。 */
  resolved_at?: string;
  /** 仍在告警；没有恢复记录且不在告警说明告警已撤销（检查停用或删除）。 */
  active?: boolean;
  reminder_count?: number;
  notify_failed?: boolean;
}
export interface HealthDataset {
  producer?: string;
  space_id?: string;
  dataset_id?: string;
  freq?: string;
  status?: HealthStatus;
  reason?: string;
  raw_error?: string;
  input_watermark_at?: string;
  output_watermark_at?: string;
  last_success_at?: string;
  last_run_at?: string;
  last_reported_at?: string;
  lag_seconds?: number | string;
  /** 输出水位允许落后的秒数；0 表示没有水位要求。 */
  lag_limit_seconds?: number | string;
}
export interface HealthPipelineStage {
  id?: string;
  name?: string;
  status?: HealthStatus;
  datasets?: HealthDataset[];
}
export interface HealthBusinessCheck {
  check_id?: string;
  /** 检查名称，与推送标题一致；由指标推导的检查没有名称。 */
  name?: string;
  kind?: string;
  module?: string;
  space_id?: string;
  status?: HealthStatus;
  reason?: string;
  raw_error?: string;
  checked_at?: string;
}
export interface HealthGatewaySignal {
  kind?: string;
  signal?: HealthSignal;
  pending_since?: string;
  expected_hash?: string;
  applied_hash?: string;
}
export interface HealthHost {
  host_id?: string;
  agent_id?: string;
  hostname?: string;
  address?: string;
  status?: HealthStatus;
  reason?: string;
  cpu_percent?: number;
  memory_percent?: number;
  disk_percent?: number;
  metrics_available?: boolean;
  memory_available?: boolean;
  disk_available?: boolean;
  cpu_available?: boolean;
  last_reported_at?: string;
  gateway_signals?: HealthGatewaySignal[];
  /** 告警阈值（百分比）；0 或缺省表示没有对应规则。 */
  cpu_threshold?: number;
  memory_threshold?: number;
  disk_threshold?: number;
}
export interface HealthCounts {
  healthy_count?: number;
  attention_count?: number;
  unknown_count?: number;
  disabled_count?: number;
  unchecked_count?: number;
}
export interface HealthSummary {
  alert_count?: number;
  healthy_count?: number;
  attention_count?: number;
  unknown_count?: number;
  unregistered_count?: number;
  components?: HealthCounts;
}
export interface HealthOverview {
  generated_at?: string;
  topology_known?: boolean;
  summary?: HealthSummary;
  alerts?: HealthAlert[];
  components?: HealthComponent[];
  pipeline?: HealthPipelineStage[];
  business_checks?: HealthBusinessCheck[];
  hosts?: HealthHost[];
  unregistered?: HealthComponent[];
  notification?: NotificationChannelSetting;
  /** 最近 incident_window_hours 小时的告警经过，按触发时间倒序。 */
  incidents?: AlertIncident[];
  incident_window_hours?: number;
}
export interface NotificationChannelSetting {
  channel_type?: string;
  configured?: boolean;
  masked_url?: string;
  updated_at?: string;
}
export interface NotificationChannelResponse {
  channel?: NotificationChannelSetting;
}
