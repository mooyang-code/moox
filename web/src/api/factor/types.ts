import type { Page, PageResult, RetInfo } from "@/api/storage/types";

export type MemberStatus = "enabled" | "disabled";
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

/** 因子定义：spec 是 YAML 表达式定义；类型、输入列、输出列与回看由服务端编译得到，只读。 */
export interface FactorDef {
  factor_id: string;
  name: string;
  /** 列表类响应默认缺省；GetFactor 始终带。 */
  spec?: string;
  spec_hash: string;
  factor_type: FactorType;
  input_columns: string[];
  outputs: string[];
  lookback_periods: number;
  allow_partial_universe: boolean;
  /** 计算契约指纹：只改注释、格式不变，影响结果的改动都会变。 */
  definition_hash?: string;
  created_at: string;
  updated_at: string;
}

/** createFactor / updateFactor 的提交体：派生字段与时间戳由服务端生成，定义不带 set_id 与状态。 */
export type FactorInput = Pick<FactorDef, "factor_id" | "name"> &
  Partial<Pick<FactorDef, "allow_partial_universe">> & { spec: string };

/** 编译诊断：path 是字段路径（如 outputs.bias_20），line/column 是 spec 内的行列（从 1 开始）。 */
export interface FactorSpecDiagnostic {
  path?: string;
  doc_line?: number;
  line?: number;
  column?: number;
  message: string;
}

export interface ValidateFactorSpecRsp {
  ret_info: RetInfo;
  valid: boolean;
  diagnostic?: FactorSpecDiagnostic;
  factor_type?: FactorType;
  input_columns?: string[];
  outputs?: string[];
  lookback_periods?: number;
  canonical?: string;
  definition_hash?: string;
}

export interface PreviewFactorReq {
  set_id: string;
  spec: string;
  subjects?: string[];
  periods?: number;
  allow_partial_universe?: boolean;
}

export interface PreviewRow {
  subject_id: string;
  series_tag: string;
  data_time: string;
  values: number[];
  nulls: boolean[];
}

export interface PreviewFactorRsp {
  ret_info: RetInfo;
  columns: string[];
  rows: PreviewRow[];
}

export interface FactorFunctionArg {
  name: string;
  kind: string;
  min?: number;
  max?: number;
  optional?: boolean;
  default_value?: number;
}

export interface FactorFunction {
  name: string;
  category: "elementwise" | "timeseries" | "cross_section";
  signature: string;
  result: string;
  args?: FactorFunctionArg[];
  lookback?: string;
  doc: string;
  example?: string;
}

export interface ListFactorFunctionsRsp {
  ret_info: RetInfo;
  functions: FactorFunction[];
  expr_version: string;
}

export interface FactorUsage {
  set_id: string;
  status: MemberStatus;
}

export interface FactorInfo {
  factor: FactorDef;
  usages: FactorUsage[];
}

export interface FactorMember {
  set_id: string;
  factor_id: string;
  status: MemberStatus;
  factor: FactorDef;
  created_at: string;
  updated_at: string;
}

export interface FactorPeriodState {
  factor_id: string;
  status: PeriodFactorStatus;
  failed_subjects?: string[];
  spec_hash?: string;
}

export interface SetRunSummary {
  set_id: string;
  /** int64：ProtoJSON 实际输出字符串，格式化函数统一转换。 */
  last_period_time: number | string;
  last_status: string;
  lag_seconds: number | string;
  factors?: FactorPeriodState[];
  failed_subjects?: string[];
  /** 预热中、未计入正式结果的对象数。 */
  warming_subjects?: number;
}

export interface FactorSetInfo {
  factor_set: FactorSet;
  members: FactorMember[];
  last_run?: SetRunSummary;
}

export interface FactorLaneStatus {
  set_id: string;
  queued: number;
  active: boolean;
  /** warming：部分对象尚未载入回看窗口，其结果不作为正式数据（不触发策略）。 */
  warmup_state?: "warming" | "ready" | "";
  warm_subjects?: number;
  expected_subjects?: number;
}

/** moox-factor-engine as last seen by moox-factor-mgr through its heartbeat. */
export interface EngineInfo {
  engine_id: string;
  boot_id: string;
  version: string;
  online: boolean;
  last_heartbeat_at: string;
  catalog_hash: string;
  catalog_synced_at: string;
  catalog_in_sync: boolean;
}

export interface EngineStatus {
  ret_info: RetInfo;
  consumer_running: boolean;
  /** 引擎表达式执行器的并行度。 */
  compute_workers?: number;
  /** 引擎与管理端内置的表达式语义版本；不一致时说明二者没有一起升级。 */
  engine_expr_version?: string;
  manager_expr_version?: string;
  lanes: FactorLaneStatus[];
  recent_runs: SetRunSummary[];
  engine?: EngineInfo;
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
  /** 领取并执行该任务的计算引擎；尚未被领取时为空。 */
  engine_id?: string;
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
  set_id?: string;
  status?: MemberStatus;
  include_spec?: boolean;
  page?: Page;
}

export interface ListFactorsRsp {
  factors: FactorInfo[];
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

export interface GetFactorRsp {
  factor: FactorDef;
  usages: FactorUsage[];
}

export interface SetFactorMemberStatusResult {
  member: FactorMember;
  backfill_job?: RecalcJob;
}
