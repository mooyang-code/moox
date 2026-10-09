/** 健康概览 v2 的状态：healthy 正常、degraded 需关注、down 异常、unknown 未知、disabled 已停用、unchecked 不探测。 */
export type HealthStatus = "healthy" | "degraded" | "down" | "unknown" | "disabled" | "unchecked";

/** int64 字段在 JSON 中可能是字符串。 */
export type WireInt64 = number | string;

export interface HealthSummary {
  alerts?: number;
  attention?: number;
  healthy?: number;
  unknown?: number;
}

/** 告警对象：kind 为 component、dataset、host 或 business。 */
export interface HealthTarget {
  kind?: string;
  host_id?: string;
  component_id?: string;
  space_id?: string;
  dataset_id?: string;
  frequency?: string;
}

export interface HealthAlert {
  id?: string;
  /** critical 或 warning。 */
  severity?: string;
  target?: HealthTarget;
  title?: string;
  reason?: string;
  raw_error?: string;
  triggered_at?: string;
  last_checked_at?: string;
  /** 所属的数据链路阶段：collect、storage、factor、trade，不属于数据链路时为空。 */
  stage?: string;
}

export interface HealthProbe {
  status?: string;
  url?: string;
  checked_at?: string;
  raw_error?: string;
}

/** 运行指标上报：status 为 healthy、stale、never_reported；只做健康探测的组件为空。 */
export interface HealthReporter {
  status?: string;
  instance_id?: string;
  version?: string;
  last_seen_at?: string;
}

export interface HealthComponent {
  host_id?: string;
  component_id?: string;
  name?: string;
  status?: string;
  reason?: string;
  probe?: HealthProbe;
  history?: HealthProbe[];
  reporter?: HealthReporter;
  status_since?: string;
}

export interface HealthPipelineDataset {
  space_id?: string;
  dataset_id?: string;
  frequency?: string;
  producer?: string;
  status?: string;
  watermark_at?: string;
  lag_seconds?: WireInt64;
  last_success_at?: string;
  reason?: string;
  raw_error?: string;
}

export interface HealthPipelineStage {
  stage?: string;
  name?: string;
  status?: string;
  datasets?: HealthPipelineDataset[];
}

export interface HealthBusinessCheck {
  kind?: string;
  name?: string;
  module?: string;
  space_id?: string;
  status?: string;
  reason?: string;
  raw_error?: string;
  checked_at?: string;
  stage?: string;
}

export interface HealthHost {
  host_id?: string;
  agent_id?: string;
  status?: string;
  reason?: string;
  gateway_state?: string;
  cpu_percent?: number;
  memory_percent?: number;
  disk_percent?: number;
  last_seen_at?: string;
}

export interface HealthUnregistered {
  host_id?: string;
  component_id?: string;
  instance_id?: string;
  version?: string;
  last_seen_at?: string;
}

export interface HealthNotification {
  channel_type?: string;
  configured?: boolean;
  webhook_masked?: string;
}

export interface HealthOverview {
  generated_at?: string;
  summary?: HealthSummary;
  alerts?: HealthAlert[];
  components?: HealthComponent[];
  pipeline?: HealthPipelineStage[];
  business_checks?: HealthBusinessCheck[];
  hosts?: HealthHost[];
  unregistered?: HealthUnregistered[];
  notification?: HealthNotification;
  warnings?: string[];
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
