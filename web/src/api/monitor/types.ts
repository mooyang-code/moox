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
  /** 模块自己的生命周期状态；模块没有生命周期接口时没有这一项。 */
  lifecycle?: HealthLifecycle;
}
/** 生命周期阶段：未初始化、未就绪、就绪、预热、运行、暂停、故障。 */
export type LifecycleStage = "uninitialized" | "not_ready" | "ready" | "warming" | "running" | "paused" | "fault";
export type LifecycleColor = "gray" | "red" | "blue" | "yellow" | "green" | "orange";
export interface HealthLifecycleObject {
  kind?: string;
  id?: string;
  stage?: LifecycleStage;
  color?: LifecycleColor;
  reason?: string;
  failed_checks?: string[];
}
export interface HealthLifecycle {
  stage?: LifecycleStage;
  color?: LifecycleColor;
  /** 运行中，但有对象暂停、故障或未就绪。 */
  degraded?: boolean;
  reason?: string;
  self_stage?: LifecycleStage;
  counts?: Record<string, number>;
  attention?: HealthLifecycleObject[];
  recovered?: number;
}
/** 系统状态：健康面（由各模块状态推导）与运行阶段（只由用户命令推进）。 */
export interface HealthSystem {
  health?: "healthy" | "attention";
  phase?: "uninitialized" | "ready" | "warming" | "running";
  color?: LifecycleColor;
  degraded?: boolean;
  /** 受影响的模块与原因，形如「因子引擎：crypto_1m 变更同步：变更日志断档」。 */
  issues?: string[];
  auto_recovered?: boolean;
  module_count?: number;
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
  /** 生产者上报的展示名称（例如策略实例名）；为空时按 Storage 名称或数据集 ID 展示。 */
  display_name?: string;
  /** 最新一根 K 线（output_watermark_at 是它的开盘时间）按交易日历的收线时间与生产者写完它的时刻。 */
  latest_bar_close_at?: string;
  arrived_at?: string;
  /** 到达 − 收线，在到达那一刻定值；delay_known 为 false 表示生产者没上报到达时间。 */
  delay_seconds?: number | string;
  delay_known?: boolean;
  /** 此刻应已收线却还没到的根数，其中最早一根的收线时间（应到时间），以及它已晚多久（当前时间 − 应到时间）。 */
  waiting_bars?: number;
  waiting_since?: string;
  overdue_seconds?: number | string;
  /** 应到的那根已由生产者的上报确认迟到（上报那一刻已晚超过允许迟到仍没有），数据集据此判为过期。 */
  late_confirmed?: boolean;
  /** 允许迟到的秒数：生产者的上报确认超过它仍未到时判为过期；已到的根延迟超过它只标为迟到。 */
  lateness_limit_seconds?: number | string;
  /** 无法按交易日历判断的原因，例如“日历未覆盖”“周期对齐未确认”。 */
  clock_issue?: string;
  /** 预期 K 线：此刻应已收线的最新一根的开盘时间（与 output_watermark_at 同一口径，相同即已到）与收线时间。 */
  expected_bar_at?: string;
  expected_bar_close_at?: string;
  /** 市场时区（IANA 名称）：加密货币 UTC，A 股 Asia/Shanghai；K 线时间按它显示并标注时区。 */
  timezone?: string;
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
  /** 资源总量；字节数是 uint64，JSON 里可能是字符串。磁盘取使用率最高的文件系统。 */
  cpu_cores?: number;
  memory_total_bytes?: number | string;
  memory_used_bytes?: number | string;
  disk_total_bytes?: number | string;
  disk_used_bytes?: number | string;
  disk_mountpoint?: string;
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
  /** 概览对所有登录用户可见，只带渠道数量和平台。 */
  notification?: NotificationSummary;
  /** 最近 incident_window_hours 小时的告警经过，按触发时间倒序。 */
  incidents?: AlertIncident[];
  incident_window_hours?: number;
  /** 系统状态：由各模块上报的生命周期状态汇总；没有模块上报时没有这一项。 */
  system?: HealthSystem;
}
export interface NotificationSummary {
  channel_count?: number;
  /** 各渠道的平台（wecom / feishu），按添加顺序，可重复。 */
  channel_types?: string[];
}
/** 一个告警推送渠道；告警发给全部渠道。完整的 Webhook URL 只有全局管理员能读取。 */
export interface NotificationChannel {
  channel_id?: string;
  channel_type?: string;
  webhook_url?: string;
  created_at?: string;
}

/** 数据链路逐根到达历史的一个点：收线时间（UTC）、到达延迟、与上一条记录之间跳过的根数。 */
export interface ArrivalHistoryPoint {
  close_at?: string;
  delay_seconds?: number | string;
  skipped_bars?: number;
}
/** 链路上一个环节的一个数据集一天的到达曲线；stage 为 storage、view、factor、factor_view、strategy。 */
export interface ArrivalHistorySeries {
  stage?: string;
  producer?: string;
  dataset_id?: string;
  primary_dataset_id?: string;
  lateness_limit_seconds?: number | string;
  points?: ArrivalHistoryPoint[];
}
export interface ArrivalHistory {
  /** 市场时区（IANA 名称），day 按它切分。 */
  timezone?: string;
  day?: string;
  start_at?: string;
  end_at?: string;
  series?: ArrivalHistorySeries[];
  /** 这一天早于 7 天的保留期，曲线可能不完整。 */
  retention_truncated?: boolean;
  /** 记录超过一次查询的上限，曲线只含这一天前面的部分。 */
  truncated?: boolean;
}
