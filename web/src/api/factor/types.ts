import type { Page, PageResult, RetInfo } from "@/api/storage/types";

export type MemberStatus = "enabled" | "disabled";
export type FactorTaskStatus = "pending" | "enabled" | "disabled" | "deleting";
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
  result_dataset_id: string;
  status: FactorTaskStatus;
  created_at?: string;
  updated_at?: string;
  /** 非空表示策略实例的专属任务：该实例是唯一的参数写入方；为空是普通计算任务，成员带固定参数。 */
  owner_instance_id?: string;
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
  /** 成员在这个任务里的别名（同一定义可以用不同别名出现多次）。 */
  member_alias?: string;
  /** 成员的固定参数（空表示参数由策略实例逐期提交）。 */
  params?: Record<string, number>;
}

export interface FactorInfo {
  factor: FactorDef;
  usages: FactorUsage[];
}

export interface FactorMember {
  task_id: string;
  /** 成员身份：任务内唯一的别名；共享任务里就是定义 ID。 */
  member_alias: string;
  factor_id: string;
  status: MemberStatus;
  factor: FactorDef;
  created_at: string;
  updated_at: string;
  /** 连续沿用参数超过多少期告警。 */
  carry_alert_periods?: number;
  /** 成员在结果数据集里的列名（派生，只读）：输出列与参数列。 */
  output_columns?: string[];
  param_columns?: string[];
  /** 成员的固定参数（加成员时给定，之后不变；策略实例专属任务的成员为空）。 */
  params?: Record<string, number>;
}

/** 一期冻结参数的来源：requested 提交生效、carried 沿用、carried_offline 引擎离线沿用、default 从未提交过。 */
export type ParamsSource = "requested" | "carried" | "carried_offline" | "default";

export interface FactorPeriodState {
  factor_id: string;
  /** 成员别名：周期状态以成员为单位。 */
  member_alias?: string;
  status: PeriodFactorStatus;
  failed_subjects?: string[];
  spec_hash?: string;
  params_source?: ParamsSource;
  params_digest?: string;
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
  /** 要补算的成员别名；空表示全部已启用成员。 */
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
  created_at: string;
  updated_at: string;
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

/** 冻结记录：快照与来源相同的连续期次 [start_period, end_period]（Unix 秒，两端都含）。 */
export interface ParamInterval {
  member_alias: string;
  start_period: number | string;
  end_period: number | string;
  params: Record<string, ParamValue>;
  source: ParamsSource;
  snapshot_hash: string;
}

/** 参数请求（审计）：accepted 已接受、applied 被某期采用、superseded 同期被后一份取代、not_applied 未生效。 */
export interface ParamRequestInfo {
  seq: number | string;
  request_id: string;
  member_alias: string;
  period_time: number | string;
  based_on_period?: number | string;
  params: Record<string, ParamValue>;
  status: "accepted" | "applied" | "superseded" | "not_applied";
  writer: string;
  created_at: string;
}

export interface GetFactorParamsReq {
  task_id: string;
  member_alias?: string;
  from?: number;
  to?: number;
  include_requests?: boolean;
  limit?: number;
}

export interface GetFactorParamsRsp {
  intervals: ParamInterval[];
  requests?: ParamRequestInfo[];
  /** 任务已冻结到的最新一期（Unix 秒）。 */
  frozen_through?: number | string;
}
