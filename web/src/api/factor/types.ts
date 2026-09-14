import type { Page, PageResult, RetInfo } from "@/api/storage/types";

export type FactorStatus = "enabled" | "disabled" | string;
export type BindingStatus = "enabled" | "disabled" | string;
export type SubjectMode = "all" | "include" | string;
export type FactorType = "timeseries" | "cross_section";

export interface FactorDef {
  factor_id: string;
  factor_type: FactorType;
  name: string;
  source_code: string;
  source_hash?: string;
  input_columns: string[];
  outputs: string[];
  params_json: string;
  lookback_periods: number;
  status: FactorStatus;
  created_at?: string;
  updated_at?: string;
}

export interface FactorBinding {
  binding_id?: string;
  factor_id: string;
  space_id: string;
  source_view_id?: string;
  result_dataset_id?: string;
  result_view_id?: string;
  source_dataset?: string;
  freq: string;
  subject_mode: SubjectMode;
  subjects_json: string;
  target_dataset?: string;
  status: BindingStatus;
  created_at?: string;
  updated_at?: string;
}

export interface EngineStatus {
  ret_info: RetInfo;
  python_workers: number;
  active_tasks: number;
  pending_tasks: number;
  desired_revision?: number;
  applied_revision?: number;
  engine_id?: string;
  engine_last_seen?: string;
}

export interface RecalcFactorReq {
  factor_id?: string;
  space_id: string;
  source_dataset?: string;
  subject_id: string;
  freq: string;
  start_time: string;
  end_time: string;
  request_id?: string;
  source_view_id?: string;
  sync_request_id?: string;
}

export interface RecalcFactorRsp {
  ret_info: RetInfo;
  job_id: string;
  status: string;
}

export interface RecalcJob {
  job_id: string;
  request_id?: string;
  status: string;
  failure_class?: string;
  error?: string;
  binding_id?: string;
  binding_generation?: string;
}

export type FactorRetRsp<T extends object = Record<string, never>> = T & { ret_info: RetInfo };

export interface ListFactorsReq {
  status?: string;
  page?: Page;
}

export interface ListFactorsRsp {
  factors: FactorDef[];
  page_result: PageResult;
}

export interface ListBindingsReq {
  space_id?: string;
  source_view_id?: string;
  source_dataset?: string;
  freq?: string;
  status?: string;
  page?: Page;
}

export interface ListBindingsRsp {
  bindings: FactorBinding[];
  page_result: PageResult;
}
