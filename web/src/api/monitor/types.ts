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
export interface HealthAlert {
  id?: string;
  severity?: string;
  object?: HealthObject;
  title?: string;
  reason?: string;
  raw_error?: string;
  triggered_at?: string;
  last_checked_at?: string;
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
}
export interface HealthPipelineStage {
  id?: string;
  name?: string;
  status?: HealthStatus;
  datasets?: HealthDataset[];
}
export interface HealthBusinessCheck {
  check_id?: string;
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
