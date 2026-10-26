import type { Page, PageResult, RetInfo } from "@/api/storage/types";

export type MemberStatus = "enabled" | "disabled";
export type FactorTaskStatus = "pending" | "enabled" | "disabled";
/** 成员类型：fixed 参数固定；var 参数由关联的策略实例逐期提交（成员键以 __var 结尾）。 */
export type MemberKind = "fixed" | "var";
export type PeriodFactorStatus = "complete" | "degraded" | "skipped";
export type RecalcJobStatus = "accepted" | "running" | "succeeded" | "failed" | "cancelled";
/** 补算作业的标的范围选择：全部（源数据集的全部标的）或指定一组。不是计算任务的属性。 */
export type RecalcSubjectMode = "all" | "include";
export type FactorType = "timeseries" | "cross_section";

export interface FactorTask {
  task_id: string;
  space_id: string;
  source_dataset_id: string;
  freq: string;
  /** 展示名：直接用作结果数据集的名称（含汉字、不超过 10 个字、空间内唯一）。 */
  name: string;
  result_dataset_id: string;
  status: FactorTaskStatus;
  created_at?: string;
  updated_at?: string;
  /** 关联的策略实例：独占、永久，写入后不能修改；为空表示还没有关联。 */
  bound_instance_id?: string;
}

/** 因子定义声明的可调参数（由编译 spec 派生，只读）。 */
export interface FactorParamDecl {
  name: string;
  type: "int" | "float";
  min: number;
  max: number;
  default_value: number;
}

/** 因子分类：只是元数据，用于查找与按类展示。 */
export interface FactorCategory {
  category_id: string;
  name: string;
  sort_order?: number;
  created_at?: string;
  updated_at?: string;
}

/** 未分类的分类 ID（服务端内置，不能删除）。 */
export const UNCATEGORIZED = "uncategorized";

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
  category_id?: string;
  /** spec 声明的可调参数（派生，只读）。 */
  params?: FactorParamDecl[];
  created_at: string;
  updated_at: string;
}

/** createFactor / updateFactor 的提交体：派生字段与时间戳由服务端生成，定义不带 task_id 与状态。 */
export type FactorInput = Pick<FactorDef, "factor_id" | "name"> &
  Partial<Pick<FactorDef, "allow_partial_universe" | "category_id">> & { spec: string };

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
  params?: FactorParamDecl[];
}

export interface PreviewFactorReq {
  task_id: string;
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
  task_id: string;
  status: MemberStatus;
  /** 成员在这个任务里的成员键（因子 ID 加参数段，规则见 utils/factor-name）。 */
  member_key: string;
  /** 固定成员的固定参数，或可变成员的默认参数。 */
  params?: Record<string, number>;
}

export interface FactorInfo {
  factor: FactorDef;
  usages: FactorUsage[];
}

export interface FactorMember {
  task_id: string;
  factor_id: string;
  status: MemberStatus;
  factor: FactorDef;
  created_at: string;
  updated_at: string;
  /** 成员身份：任务内唯一的成员键。 */
  member_key: string;
  /** 成员在结果数据集里的列名（派生，只读）。 */
  output_columns?: string[];
  /** 固定成员的固定参数，或可变成员的默认参数（完整参数，加成员时给定，之后不变）。 */
  params?: Record<string, number>;
  kind: MemberKind;
  /** 可变成员最近一次被接受的提交的目标期（Unix 秒，int64 可能是字符串）和提交时间；从未提交过为 0 和空。 */
  last_submitted_period?: number | string;
  last_submitted_at?: string;
}

export interface FactorPeriodState {
  factor_id: string;
  /** 成员键：周期状态以成员为单位。 */
  member_key?: string;
  member_kind?: MemberKind;
  status: PeriodFactorStatus;
  failed_subjects?: string[];
  spec_hash?: string;
}

export interface TaskRunSummary {
  task_id: string;
  /** int64：ProtoJSON 实际输出字符串，格式化函数统一转换。 */
  last_period_time: number | string;
  last_status: string;
  lag_seconds: number | string;
  factors?: FactorPeriodState[];
  failed_subjects?: string[];
  /** 预热中、未计入正式结果的对象数。 */
  warming_subjects?: number;
}

export interface FactorTaskInfo {
  factor_task: FactorTask;
  members: FactorMember[];
  last_run?: TaskRunSummary;
}

export interface FactorRunnerStatus {
  task_id: string;
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
  runners: FactorRunnerStatus[];
  recent_runs: TaskRunSummary[];
  engine?: EngineInfo;
}

export interface RecalcFactorsReq {
  task_id: string;
  /** 要补算的成员键；空表示全部已启用成员。 */
  members: string[];
  subjects: string[];
  start_time: string;
  end_time: string;
  request_id: string;
}

export interface RecalcJob {
  job_id: string;
  request_id: string;
  task_id: string;
  members: string[];
  subjects: string[];
  start_time: string;
  end_time: string;
  status: RecalcJobStatus;
  progress_time: string;
  error: string;
  /** 领取并执行该任务的计算引擎；尚未被领取时为空。 */
  engine_id?: string;
  /** 按默认参数新补的期次：原行不存在、或原行的 __params 里没有这个成员，与当时的实时结果不保证一致。 */
  default_filled?: MemberRanges[];
  created_at: string;
  updated_at: string;
}

/** Unix 秒的闭区间（int64 可能是字符串）。 */
export interface TimeRange {
  start: number | string;
  end: number | string;
}

export interface MemberRanges {
  member_key: string;
  ranges: TimeRange[];
}

export type FactorRetRsp<T extends object = Record<string, never>> = T & { ret_info: RetInfo };

export interface ListFactorTasksReq {
  status?: FactorTaskStatus;
  page?: Page;
}

export interface ListFactorTasksRsp {
  factor_tasks: FactorTaskInfo[];
  page_result: PageResult;
}

export interface ListFactorsReq {
  task_id?: string;
  status?: MemberStatus;
  include_spec?: boolean;
  page?: Page;
}

export interface ListFactorsRsp {
  factors: FactorInfo[];
  page_result: PageResult;
}

export interface ListRecalcJobsReq {
  task_id: string;
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

export interface SetTaskMemberStatusResult {
  member: FactorMember;
}

/** 一个参数一期的取值：统一值加按标的覆盖。 */
export interface ParamValue {
  default_value: number;
  overrides?: Record<string, number>;
}

/** 可变成员最近一次被接受的提交（只读）。 */
export interface MemberParamState {
  member_key: string;
  params: Record<string, ParamValue>;
  /** 目标期（Unix 秒，int64 可能是字符串）。 */
  last_submitted_period: number | string;
  last_request_id?: string;
  last_submitted_at?: string;
}

export interface GetFactorParamsReq {
  task_id: string;
}

export interface GetFactorParamsRsp {
  members: MemberParamState[];
}
