import type { Page, PageResult, RetInfo } from "@/api/storage/types";

export type FactorStatus = "enabled" | "disabled";
export type FactorSetStatus = "pending" | "enabled" | "disabled" | "deleting";
export type PeriodFactorStatus = "complete" | "degraded" | "skipped";
export type RecalcJobStatus = "accepted" | "running" | "succeeded" | "failed" | "cancelled";
export type SubjectMode = "all" | "include";
export type FactorType = "timeseries" | "cross_section";

export interface FactorSet {
  set_id: string;
  space_id: string;
  source_dataset_id: string;
  freq: string;
  subject_mode: SubjectMode;
  subjects: string[];
  result_dataset_id: string;
  status: FactorSetStatus;
  created_at?: string;
  updated_at?: string;
}

export interface FactorDef {
  factor_id: string;
  set_id: string;
  factor_type: FactorType;
  name: string;
  source_code: string;
  source_hash?: string;
  input_columns: string[];
  outputs: string[];
  params_json: string;
  lookback_periods: number;
  allow_partial_universe?: boolean;
  status: FactorStatus;
  created_at?: string;
  updated_at?: string;
}

export interface FactorPeriodState {
  factor_id: string;
  status: PeriodFactorStatus;
  failed_subjects?: string[];
  source_hash?: string;
}

export interface SetRunSummary {
  set_id: string;
  last_period_time: number;
  last_status: string;
  lag_seconds: number;
  factors?: FactorPeriodState[];
  failed_subjects?: string[];
}

export interface FactorSetInfo {
  factor_set: FactorSet;
  factors: FactorDef[];
  last_run?: SetRunSummary;
}

export interface FactorLaneStatus {
  set_id: string;
  queued: number;
  active: boolean;
}

export interface EngineStatus {
  ret_info: RetInfo;
  consumer_running: boolean;
  python_workers: number;
  python_busy: number;
  lanes: FactorLaneStatus[];
  recent_runs: SetRunSummary[];
}

export interface RecalcFactorsReq {
  set_id: string;
  factor_ids: string[];
  subjects: string[];
  start_time: string;
  end_time: string;
  request_id: string;
}

export interface RecalcJob {
  job_id: string;
  request_id: string;
  set_id: string;
  factor_ids: string[];
  subjects: string[];
  start_time: string;
  end_time: string;
  status: RecalcJobStatus;
  progress_time: string;
  error: string;
  created_at: string;
  updated_at: string;
}

export type FactorRetRsp<T extends object = Record<string, never>> = T & { ret_info: RetInfo };

export interface ListFactorSetsReq {
  status?: FactorSetStatus;
  page?: Page;
}

export interface ListFactorSetsRsp {
  factor_sets: FactorSetInfo[];
  page_result: PageResult;
}

export interface ListFactorsReq {
  set_id: string;
  status?: FactorStatus;
  page?: Page;
}

export interface ListFactorsRsp {
  factors: FactorDef[];
  page_result: PageResult;
}

export interface ListRecalcJobsReq {
  set_id: string;
  statuses?: RecalcJobStatus[];
  page?: Page;
}

export interface ListRecalcJobsRsp {
  jobs: RecalcJob[];
  page_result: PageResult;
}

export interface SetFactorStatusResult {
  factor: FactorDef;
  backfill_job?: RecalcJob;
}
