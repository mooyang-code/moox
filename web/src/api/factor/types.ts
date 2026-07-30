import type { Page, PageResult, RetInfo } from "@/api/storage/types";

export type FactorStatus = "enabled" | "disabled" | string;
export type BindingStatus = "enabled" | "disabled" | string;
export type SubjectMode = "all" | "include" | string;

export interface FactorDef {
  factor_id: string;
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
  source_dataset: string;
  freq: string;
  subject_mode: SubjectMode;
  subjects_json: string;
  target_dataset: string;
  status: BindingStatus;
  created_at?: string;
  updated_at?: string;
}

export interface EngineStatus {
  ret_info: RetInfo;
  queue_depth: number;
  queue_overflow_count: number | string;
}

export interface RecalcFactorReq {
  factor_id?: string;
  space_id: string;
  source_dataset: string;
  subject_id: string;
  freq: string;
  start_time: string;
  end_time: string;
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
  source_dataset?: string;
  freq?: string;
  status?: string;
  page?: Page;
}

export interface ListBindingsRsp {
  bindings: FactorBinding[];
  page_result: PageResult;
}
