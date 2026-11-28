import { call, type CallOptions } from "@/api/http";
import type { Page, PageResult } from "@/api/page";

export type HealthLevel = "ok" | "warn" | "error";

export interface FetchPlanSummary {
  id?: number | string;
  status?: string;
  last_error?: string;
  /** Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
}

export interface CollectorDatasetStatus {
  dataset_id?: string;
  /** 数据集展示名（所属采集任务名）；不限数据集或任务已删除时为空。 */
  dataset_name?: string;
  frequency?: string;
  subject_count?: number;
  function_count?: number;
  effective_from?: string;
  /** 自动巡检已查到的时间（不含），没有巡检过为空。 */
  inspected_until?: string;
  /** 自动巡检超出预期的秒数：衡量自动补漏是否跟上，不是数据延迟。 */
  inspect_lag_seconds?: number | string;
  gap_bars_24h?: number;
  gap_subjects_24h?: number;
  failed_chunks_24h?: number;
  absent_bars?: number;
  level?: HealthLevel;
  reasons?: string[];
}

export interface CollectorFunctionDataset {
  /** “数据集|频率”。 */
  dataset?: string;
  /** 本函数在该数据集上负责的标的（当前生效的那份快照）。 */
  subjects?: string[];
  effective_from?: string;
}

export interface CollectorFunctionStatus {
  name?: string;
  region?: string;
  market?: string;
  cron?: string;
  subject_count?: number;
  datasets?: string[];
  state?: string;
  dataset_subjects?: CollectorFunctionDataset[];
  /** 执行端：scf（云函数）、stream（WS 推送）、local（本地执行端）。 */
  executor?: string;
  /**
   * 以下四项是 Collector 内存里最近一次云上读回，只对 scf 函数有意义；Collector 重启后到下一次读回之前为空。
   * 值为 false 时后端不下发字段。
   */
  cloud_exists?: boolean;
  cloud_timer_matches?: boolean;
  cloud_package_latest?: boolean;
  /** 读回时刻：Unix 毫秒（数字字符串），"0" 表示还没有读回。 */
  cloud_read_at?: string;
}

/** 计划外的函数：符合本空间命名格式但不在计划里，对账器已停掉定时器，不会自动删除。 */
export interface CollectorUnplannedFunction {
  name?: string;
  region?: string;
  namespace?: string;
}

export interface StreamConnStatus {
  market?: string;
  frequency?: string;
  streams?: number;
  /** 最近一条消息的时刻：Unix 毫秒（数字字符串）。 */
  last_message_at?: string;
  reconnects?: number | string;
  subscribed?: boolean;
  endpoint?: string;
}

/** 一个（数据集, 周期）的推送接收与提交统计；时间是相对收盘的毫秒数。 */
export interface StreamPeriodStat {
  market?: string;
  dataset_id?: string;
  frequency?: string;
  period?: number | string;
  expected?: number;
  arrived?: number;
  missing?: number;
  late_after_commit?: number;
  last_arrival_ms?: number | string;
  commit_done_ms?: number | string;
  commit_ms?: number | string;
  committed?: boolean;
  error?: string;
  suspected?: number;
}

export interface StreamWorkerStatus {
  instance_id?: string;
  online?: boolean;
  /** 心跳时刻：Unix 毫秒（数字字符串）。 */
  heartbeat_at?: string;
  applied_plan_id?: number | string;
  conns?: StreamConnStatus[];
  recent?: StreamPeriodStat[];
  clock_offset_ms?: number | string;
  version?: string;
}

/** 一个 K 线任务的采集方式：stream（推送）、scf（云函数拉取）、local（本地拉取）。 */
export interface IngestMode {
  dataset_id?: string;
  task_id?: string;
  ingest?: string;
  effective_periods?: Record<string, string>;
  enabled?: boolean;
}

/** 一个数据集（某个频率）首次接入时的历史补齐记录；补齐期间下游因子不计算、策略不求值。 */
export interface HistorySeed {
  space_id?: string;
  dataset_id?: string;
  frequency?: string;
  /** running 补齐中、done 已完成、skipped 用户手动放行。 */
  status?: string;
  /** 补齐的目标起点（RFC3339）。 */
  target_from?: string;
  effective_from?: string;
  total_chunks?: number;
  done_chunks?: number;
  failed_chunks?: number;
  /** 按最近速率估计的剩余秒数，0 表示不能估计。 */
  eta_seconds?: number | string;
  /** 开始、结束时刻：Unix 毫秒（数字字符串），还没结束时 finished_at 为 "0" 或不下发。 */
  started_at?: string;
  finished_at?: string;
  note?: string;
}

export interface GetCollectorStatusResponse {
  level?: HealthLevel;
  reasons?: string[];
  open?: FetchPlanSummary;
  applied?: FetchPlanSummary;
  datasets?: CollectorDatasetStatus[];
  functions?: CollectorFunctionStatus[];
  active_backfills?: number;
  failed_backfills_24h?: number;
  /** Unix 毫秒（数字字符串）。 */
  generated_at?: string;
  stream?: StreamWorkerStatus;
  ingest_modes?: IngestMode[];
  /** 对账器最近一次读回找到的计划外函数（Collector 内存里的结果，重启后到下一次读回之前为空）。 */
  unplanned_functions?: CollectorUnplannedFunction[];
  /** 本空间的历史补齐记录（补齐中的排前）。 */
  history_seeds?: HistorySeed[];
}

export interface KlineBackfillJob {
  job_id?: string;
  space_id?: string;
  source?: string;
  kind?: string;
  dataset_id?: string;
  /** 数据集展示名（所属采集任务名）；不限数据集或任务已删除时为空。 */
  dataset_name?: string;
  frequency?: string;
  subject_id?: string;
  from?: string;
  to?: string;
  status?: string;
  last_error?: string;
  /** Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
  queued?: number;
  running?: number;
  succeeded?: number;
  failed?: number;
  cancelled?: number;
}

export interface KlineBackfillChunk {
  dataset_id?: string;
  /** 数据集展示名（所属采集任务名）；不限数据集或任务已删除时为空。 */
  dataset_name?: string;
  frequency?: string;
  subject_id?: string;
  from?: string;
  to?: string;
  status?: string;
  attempts?: number;
  last_error?: string;
  rows?: number;
}

export interface KlineGap {
  dataset_id?: string;
  frequency?: string;
  subject_id?: string;
  from?: string;
  to?: string;
  missing?: number;
}

export interface CheckKlineResponse {
  gaps?: KlineGap[];
  expected?: number;
  missing?: number;
  absent?: number;
  truncated?: boolean;
}

export interface KlineRangeRequest {
  space_id: string;
  dataset_id?: string;
  subject_id?: string;
  from: string;
  to: string;
}

export type BackfillJobSource = "manual" | "auto" | "seed" | "close_refetch" | "catchup";

export interface BackfillJobFilter {
  source?: BackfillJobSource;
  attention?: boolean;
  dataset_id?: string;
}

// 网关要求请求体里的 space_id 与 X-Space-Id 一致：读当前所选空间以外的空间（首页逐个空间读历史补齐）时传 { spaceId }。
// 显式空间的请求不随切换空间取消，只在确实要跨空间读取时传。
export function getCollectorStatus(spaceId: string, options: CallOptions = {}) {
  return call<{ space_id: string }, GetCollectorStatusResponse>(
    "collector",
    "GetCollectorStatus",
    { space_id: spaceId },
    options
  );
}

export function listKlineBackfills(spaceId: string, filter: BackfillJobFilter, page = 1, size = 20) {
  return call<{ space_id: string; page: Page } & BackfillJobFilter, { items?: KlineBackfillJob[]; page_result?: PageResult }>(
    "collector",
    "ListKlineBackfills",
    { space_id: spaceId, page: { page, size }, ...filter }
  );
}

export function getKlineBackfill(spaceId: string, jobId: string) {
  return call<{ job_id: string; space_id: string }, { job?: KlineBackfillJob; failed_chunks?: KlineBackfillChunk[] }>(
    "collector",
    "GetKlineBackfill",
    { job_id: jobId, space_id: spaceId }
  );
}

export function cancelKlineBackfill(spaceId: string, jobId: string) {
  return call<{ job_id: string; space_id: string }, Record<string, never>>("collector", "CancelKlineBackfill", {
    job_id: jobId,
    space_id: spaceId
  });
}

export function retryKlineBackfill(spaceId: string, jobId: string) {
  return call<{ job_id: string; space_id: string }, { chunks?: number }>("collector", "RetryKlineBackfill", {
    job_id: jobId,
    space_id: spaceId
  });
}

export function clearSourceAbsences(spaceId: string, datasetId: string, frequency: string) {
  return call<{ space_id: string; dataset_id: string; frequency: string }, { cleared?: number | string }>(
    "collector",
    "ClearSourceAbsences",
    { space_id: spaceId, dataset_id: datasetId, frequency }
  );
}

// 这两个同步操作服务端业务预算 300 秒、网关路由预算 310 秒，浏览器超时要略大于网关预算。
const klineOpsRequestConfig = { timeout: 330000 };

export function submitKlineBackfill(request: KlineRangeRequest & { force?: boolean }) {
  return call<typeof request, { job_id?: string; chunks?: number }>(
    "collector",
    "SubmitKlineBackfill",
    request,
    klineOpsRequestConfig
  );
}

export function checkKline(request: KlineRangeRequest & { deep?: boolean }) {
  return call<typeof request, CheckKlineResponse>("collector", "CheckKline", request, klineOpsRequestConfig);
}
