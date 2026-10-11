import { callControl } from "@/api/admin/http";

export interface FetchPlanSummary {
  id?: number | string;
  status?: string;
  last_error?: string;
  created_at?: string;
  updated_at?: string;
}

export interface FetchDatasetState {
  dataset_id?: string;
  frequency?: string;
  hash?: string;
  subject_count?: number;
  effective_period?: string;
}

export interface FetchFunctionState {
  name?: string;
  region?: string;
  market?: string;
  cron?: string;
  subject_count?: number;
  datasets?: string[];
  state?: string;
}

export interface GetFetchPlansResponse {
  open?: FetchPlanSummary;
  applied?: FetchPlanSummary;
  datasets?: FetchDatasetState[];
  functions?: FetchFunctionState[];
}

export interface KlineBackfillJob {
  job_id?: string;
  space_id?: string;
  source?: string;
  status?: string;
  last_error?: string;
  request?: string;
  created_at?: string;
  updated_at?: string;
  pending?: number;
  running?: number;
  succeeded?: number;
  failed?: number;
  canceled?: number;
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
  truncated?: boolean;
}

export interface KlineRangeRequest {
  space_id: string;
  dataset_id?: string;
  subject_id?: string;
  from: string;
  to: string;
}

export function getFetchPlans(spaceId: string) {
  return callControl<{ space_id: string }, GetFetchPlansResponse>("collector", "GetFetchPlans", { space_id: spaceId });
}

export function listKlineBackfills(spaceId: string, page = 1, size = 20) {
  return callControl<
    { space_id: string; page: { page: number; size: number } },
    { items?: KlineBackfillJob[]; page?: { total?: number; has_more?: boolean } }
  >("collector", "ListKlineBackfills", { space_id: spaceId, page: { page, size } });
}

export function cancelKlineBackfill(jobId: string) {
  return callControl<{ job_id: string }, Record<string, never>>("collector", "CancelKlineBackfill", { job_id: jobId });
}

export function submitKlineBackfill(request: KlineRangeRequest & { force?: boolean }) {
  return callControl<typeof request, { job_id?: string; chunks?: number }>("collector", "SubmitKlineBackfill", request);
}

export function checkKline(request: KlineRangeRequest & { deep?: boolean }) {
  return callControl<typeof request, CheckKlineResponse>("collector", "CheckKline", request);
}
