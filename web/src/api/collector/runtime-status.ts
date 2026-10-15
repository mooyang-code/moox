import { callControl } from "@/api/admin/http";

export type HealthLevel = "ok" | "warn" | "error";

export interface FetchPlanSummary {
  id?: number | string;
  status?: string;
  last_error?: string;
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
  scanned_until?: string;
  lag_seconds?: number | string;
  gap_bars_24h?: number;
  gap_subjects_24h?: number;
  failed_chunks_24h?: number;
  history_scanned_at?: string;
  history_gap_bars?: number;
  history_gap_subjects?: number;
  history_error?: string;
  absent_bars?: number;
  level?: HealthLevel;
  reasons?: string[];
}

export interface CollectorFunctionStatus {
  name?: string;
  region?: string;
  market?: string;
  cron?: string;
  subject_count?: number;
  datasets?: string[];
  state?: string;
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
  generated_at?: string;
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
  created_at?: string;
  updated_at?: string;
  pending?: number;
  running?: number;
  succeeded?: number;
  failed?: number;
  canceled?: number;
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

export interface BackfillJobFilter {
  source?: "manual" | "auto";
  attention?: boolean;
  dataset_id?: string;
}

export function getCollectorStatus(spaceId: string) {
  return callControl<{ space_id: string }, GetCollectorStatusResponse>("collector", "GetCollectorStatus", { space_id: spaceId });
}

export function listKlineBackfills(spaceId: string, filter: BackfillJobFilter, page = 1, size = 20) {
  return callControl<
    { space_id: string; page: { page: number; size: number } } & BackfillJobFilter,
    { items?: KlineBackfillJob[]; page?: { total?: number; has_more?: boolean } }
  >("collector", "ListKlineBackfills", { space_id: spaceId, page: { page, size }, ...filter });
}

export function getKlineBackfill(spaceId: string, jobId: string) {
  return callControl<{ job_id: string; space_id: string }, { job?: KlineBackfillJob; failed_chunks?: KlineBackfillChunk[] }>(
    "collector",
    "GetKlineBackfill",
    { job_id: jobId, space_id: spaceId }
  );
}

export function cancelKlineBackfill(spaceId: string, jobId: string) {
  return callControl<{ job_id: string; space_id: string }, Record<string, never>>("collector", "CancelKlineBackfill", {
    job_id: jobId,
    space_id: spaceId
  });
}

export function retryKlineBackfill(spaceId: string, jobId: string) {
  return callControl<{ job_id: string; space_id: string }, { chunks?: number }>("collector", "RetryKlineBackfill", {
    job_id: jobId,
    space_id: spaceId
  });
}

export function confirmHistoryGaps(spaceId: string, datasetId: string, frequency: string) {
  return callControl<{ space_id: string; dataset_id: string; frequency: string }, { job_id?: string; chunks?: number }>(
    "collector",
    "ConfirmHistoryGaps",
    { space_id: spaceId, dataset_id: datasetId, frequency }
  );
}

export function clearSourceAbsences(spaceId: string, datasetId: string, frequency: string) {
  return callControl<{ space_id: string; dataset_id: string; frequency: string }, { cleared?: number | string }>(
    "collector",
    "ClearSourceAbsences",
    { space_id: spaceId, dataset_id: datasetId, frequency }
  );
}

// 这两个同步操作服务端业务预算 300 秒、网关路由预算 310 秒，浏览器超时要略大于网关预算。
const klineOpsRequestConfig = { timeout: 330000 };

export function submitKlineBackfill(request: KlineRangeRequest & { force?: boolean }) {
  return callControl<typeof request, { job_id?: string; chunks?: number }>(
    "collector",
    "SubmitKlineBackfill",
    request,
    klineOpsRequestConfig
  );
}

export function checkKline(request: KlineRangeRequest & { deep?: boolean }) {
  return callControl<typeof request, CheckKlineResponse>("collector", "CheckKline", request, klineOpsRequestConfig);
}
