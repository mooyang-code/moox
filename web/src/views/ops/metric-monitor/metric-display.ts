import type { MetricLatestPoint } from "@/api/metric-monitor/types";

export interface MetricDimension {
  key: string;
  label: string;
  value: string;
}

const SERVICE_LABELS: Record<string, string> = {
  archive: "数据归档",
  cloudnode: "云节点",
  collector: "行情采集",
  eventbus: "事件总线",
  factor: "因子计算",
  gateway: "服务网关",
  hostagent: "主机监控",
  monitor: "系统监控",
  moox_archive: "数据归档",
  moox_cloudnode: "云节点",
  moox_collector: "行情采集",
  moox_eventbus: "事件总线",
  moox_factor: "因子计算",
  moox_gateway: "服务网关",
  moox_hostagent: "主机监控",
  moox_monitor: "系统监控",
  moox_storage: "数据存储",
  moox_strategy: "策略服务",
  moox_trade: "交易服务",
  storage: "数据存储",
  strategy: "策略服务",
  trade: "交易服务",
  web_host: "管理页面"
};

const METRIC_LABELS: Record<string, string> = {
  moox_collector_dns_resolver_failures_total: "DNS 解析失败次数",
  moox_collector_dns_resolver_refreshes_total: "DNS 路由刷新次数",
  moox_collector_dns_resolver_route_age_seconds: "DNS 路由已持续时间",
  moox_collector_dns_resolver_route_count: "DNS 路由数量",
  moox_collector_dns_resolver_source: "DNS 路由来源",
  moox_collector_market_fetch_assignment_active: "采集任务运行中",
  moox_collector_market_fetch_assignment_errors_total: "采集任务错误次数",
  moox_collector_market_fetch_assignment_last_success_timestamp_seconds: "采集任务最近成功时间",
  moox_collector_market_fetch_assignment_required: "采集任务需求数量",
  moox_collector_market_fetch_coordination_healthy: "采集协调状态",
  moox_collector_market_fetch_coordination_pending: "待协调采集任务",
  moox_collector_market_fetch_coordination_pending_since_timestamp_seconds: "协调等待开始时间",
  moox_collector_market_fetch_timer_active: "采集定时器运行中",
  moox_collector_market_fetch_timer_capacity_active: "采集定时器已使用容量",
  moox_collector_market_fetch_timer_capacity_total: "采集定时器总容量",
  moox_collector_market_fetch_timer_capacity_headroom: "采集定时器剩余容量",
  moox_collector_market_fetch_timer_capacity_required: "采集定时器需求容量",
  moox_collector_market_fetch_timer_headroom: "采集定时器剩余容量",
  moox_collector_market_fetch_timer_required: "采集定时器需求容量",
  moox_collector_market_fetch_timer_available: "采集定时器可用状态",
  moox_collector_dataset_enabled: "数据集采集开关",
  moox_collector_dataset_expected_interval_seconds: "数据集预期间隔",
  moox_collector_dataset_last_success_timestamp_seconds: "数据集最近成功时间",
  moox_collector_period_pending_total: "待处理周期数量",
  moox_collector_period_report_retry_total: "周期上报重试次数",
  moox_factor_batch_elapsed_seconds: "因子批次耗时",
  moox_factor_batch_factor_total: "批次因子数量",
  moox_factor_batch_running: "因子批次运行中",
  moox_factor_batch_total: "因子批次总数",
  moox_factor_dataset_input_watermark_timestamp_seconds: "因子输入数据时间",
  moox_factor_dataset_output_watermark_timestamp_seconds: "因子输出数据时间",
  moox_factor_source_ready_lag_seconds: "因子输入等待时长",
  moox_factor_dataset_enabled: "因子数据集开关",
  moox_factor_dataset_expected_interval_seconds: "因子数据集预期间隔",
  moox_factor_dataset_last_success_timestamp_seconds: "因子数据集最近成功时间",
  moox_factor_metrics_errors_total: "因子计算错误次数",
  moox_factor_metrics_report_errors_total: "因子日志上报错误次数",
  moox_factor_report_errors_total: "因子结果上报错误次数",
  moox_factor_report_last_error_timestamp_seconds: "因子结果最近错误时间",
  moox_monitor_metrics_consumer_pending: "监控指标待处理数量",
  moox_monitor_metrics_ingest_latency_seconds: "监控指标接收延迟",
  moox_monitor_metrics_ingest_last_success_timestamp_seconds: "监控指标最近接收时间",
  moox_storage_view_output_watermark_timestamp_seconds: "视图最新数据时间",
  moox_storage_view_consumer_lag_messages: "视图待消费消息",
  moox_storage_view_oldest_pending_event_age_seconds: "视图最早待处理时长",
  moox_storage_outbox_pending_entries: "存储待发送消息",
  moox_storage_outbox_publish_errors_total: "存储发布错误次数"
};

const DIMENSION_LABELS: Record<string, string> = {
  dataset: "Dataset",
  dataset_id: "Dataset",
  freq: "频率",
  frequency: "频率",
  node: "节点",
  node_id: "节点",
  region: "地域",
  space: "空间",
  space_id: "空间",
  subject: "标的",
  symbol: "标的",
  instance: "实例",
  instance_id: "实例"
};

const WORD_LABELS: Record<string, string> = {
  active: "运行中",
  age: "持续时间",
  available: "可用",
  batch: "批次",
  capacity: "容量",
  consumer: "消费",
  count: "数量",
  elapsed: "耗时",
  errors: "错误",
  failure: "失败",
  headroom: "剩余容量",
  history: "历史",
  input: "输入",
  lag: "延迟",
  latency: "延迟",
  output: "输出",
  pending: "待处理",
  rate: "速率",
  ready: "就绪",
  refreshes: "刷新",
  report: "上报",
  required: "需求",
  resolver: "解析",
  route: "路由",
  running: "运行中",
  source: "来源",
  success: "成功",
  timer: "定时器",
  timestamp: "时间",
  total: "总数",
  watermark: "数据时间",
  usage: "使用率"
};

export function serviceDisplayName(serviceName?: string): string {
  if (!serviceName) return "未知服务";
  return SERVICE_LABELS[serviceName] || humanizeIdentifier(serviceName);
}

export function metricDisplayName(metricName?: string): string {
  if (!metricName) return "未命名指标";
  return METRIC_LABELS[metricName] || humanizeIdentifier(metricName);
}

export function humanizeIdentifier(identifier: string): string {
  const words = identifier
    .replace(/^moox_/, "")
    .replace(/_seconds?$/, "")
    .split(/[_\-.]+/)
    .filter(Boolean)
    .map(word => WORD_LABELS[word.toLowerCase()] || word);
  return words.length ? words.join(" · ") : identifier;
}

export function parseMetricLabels(labelsJson?: string): MetricDimension[] {
  if (!labelsJson) return [];
  try {
    const parsed = JSON.parse(labelsJson) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return [];
    return Object.entries(parsed as Record<string, unknown>)
      .filter(([, value]) => value !== undefined && value !== null && String(value) !== "")
      .map(([key, value]) => ({
        key,
        label: DIMENSION_LABELS[key.toLowerCase()] || humanizeIdentifier(key),
        value: String(value)
      }))
      .sort((left, right) => left.key.localeCompare(right.key));
  } catch {
    return [];
  }
}

export function metricDimensionSummary(labelsJson?: string, limit = 3): { items: MetricDimension[]; overflow: number } {
  const dimensions = parseMetricLabels(labelsJson);
  return { items: dimensions.slice(0, limit), overflow: Math.max(0, dimensions.length - limit) };
}

function formatDuration(seconds: number): string {
  const absolute = Math.abs(seconds);
  if (absolute < 60) return `${Math.round(seconds)}秒`;
  if (absolute < 3600) return `${(seconds / 60).toFixed(absolute < 600 ? 1 : 0)}分钟`;
  if (absolute < 86400) return `${(seconds / 3600).toFixed(absolute < 36000 ? 1 : 0)}小时`;
  return `${(seconds / 86400).toFixed(absolute < 864000 ? 1 : 0)}天`;
}

function formatTimestamp(seconds: number): string {
  const observedAt = new Date(seconds * 1000);
  if (Number.isNaN(observedAt.valueOf())) return "时间无效";
  const age = (Date.now() - observedAt.valueOf()) / 1000;
  if (age >= 0 && age < 60) return "刚刚";
  return observedAt.toLocaleString();
}

export function metricValueDisplay(metricName?: string, value?: number): string {
  if (value === undefined || value === null || Number.isNaN(Number(value))) return "暂无数据";
  const name = metricName || "";
  if (/timestamp_seconds$/.test(name)) return formatTimestamp(Number(value));
  if (/(?:lag|elapsed|duration|latency|age|pending_since).*seconds/.test(name)) return formatDuration(Number(value));
  if (/(?:percent|usage|ratio)/.test(name)) return `${Number(value).toLocaleString(undefined, { maximumFractionDigits: 2 })}%`;
  const unit = /(errors|refreshes|runs|total|count|capacity|active|required|headroom|pending|route_count)/.test(name) ? "个" : "";
  return `${Number(value).toLocaleString(undefined, { maximumFractionDigits: 4 })}${unit}`;
}

export function metricStatusText(row: Pick<MetricLatestPoint, "stale" | "observed_at">): string {
  const stale = !!row.stale || (!!row.observed_at && Date.now() - new Date(row.observed_at).valueOf() > 90_000);
  return stale ? "需关注" : "正常";
}

export function metricStatusReason(row: Pick<MetricLatestPoint, "stale" | "observed_at">): string {
  return metricStatusText(row) === "需关注" ? "陈旧数据" : "数据正常";
}
