import { call } from "@/api/http";
import type { Page, PageResult } from "@/api/page";

export interface CollectorTaskResult {
  result_name?: string;
  /** 结果写入的数据集；采集任务页据此显示归档状态 */
  dataset_id?: string;
  status?: string;
  last_data_time?: string;
  coverage_start?: string;
  coverage_end?: string;
  data_kind?: string;
}

export interface CollectorTask {
  task_id: string;
  task_name?: string;
  description?: string;
  space_id?: string;
  data_type?: string;
  /** K 线任务的采集方式：stream、scf、local；合成任务为空 */
  ingest?: string;
  tag_ids?: string[];
  definition_hash?: string;
  collect_params?: Record<string, unknown>;
  enabled?: boolean | string;
  creator?: string;
  /** 创建、更新时刻：Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
  prepare_state?: string;
  last_error?: string;
  result?: CollectorTaskResult;
}

export interface CollectionTaskPayload {
  space_id?: string;
  task_id?: string;
  task_name?: string;
  description?: string;
  data_type?: string;
  /** 新建 K 线任务时的采集方式；不带表示用市场默认值 */
  ingest?: string;
  tag_ids?: string[];
  definition_hash?: string;
  collect_params?: Record<string, unknown>;
  enabled?: boolean;
  creator?: string;
}

export interface CollectionTaskResultConfig {
  description?: string;
}

export interface GetTaskListRequest {
  space_id: string;
  data_type?: string;
  enabled?: boolean;
  task_id?: string;
  keyword?: string;
  page?: Page;
}

export interface GetTaskListResponse {
  tasks?: CollectorTask[];
  page_result?: PageResult;
}

export interface GetTaskDetailRequest {
  space_id: string;
  task_id: string;
}

export interface GetTaskDetailResponse {
  task?: CollectorTask;
}

/** 标签同步的最近运行状态；没有运行记录的标签不会出现在结果里。 */
export interface TagSyncStatus {
  space_id: string;
  tag_id: string;
  /** 上次运行时刻：Unix 毫秒（数字字符串）。 */
  last_run_at?: string;
  last_status?: "success" | "failed" | string;
  last_error?: string;
  /** 最近一次成功的时刻：Unix 毫秒（数字字符串）；从未成功过为 "0"。 */
  last_success_at?: string;
}

/** 一个数据来源能提供标的快照的市场类型。 */
export interface SubjectLister {
  source: string;
  market_types?: string[];
}

export interface CreateTaskRequest {
  task: CollectionTaskPayload;
  result_config: CollectionTaskResultConfig;
}

export interface CreateTaskResponse {
  task_id?: string;
}

export interface UpdateTaskRequest {
  space_id: string;
  task_id: string;
  task: Pick<CollectionTaskPayload, "task_id" | "task_name" | "description" | "enabled">;
}

export interface UpdateTaskResponse {
  task?: CollectorTask;
}

export interface DisableTaskRequest {
  space_id: string;
  task_id: string;
}

export interface DeleteTaskRequest {
  space_id: string;
  task_id: string;
  delete_result_data: boolean;
}

// 历史合成作业：范围 [start, end) 按目标桶推进，状态 queued / running / succeeded / failed / cancelled。
export interface ResampleJob {
  job_id: string;
  task_id: string;
  request_id: string;
  origin: "manual" | "source_backfill" | string;
  subjects?: string[];
  start: string;
  end: string;
  next_bucket: string;
  /** 最早开始时刻：Unix 毫秒（数字字符串），"0" 表示不限。 */
  not_before: string;
  status: string;
  total_buckets: number;
  done_buckets: number;
  written: number;
  skipped: number;
  skipped_samples?: string[];
  last_error?: string;
  /** Unix 毫秒（数字字符串）。 */
  created_at: string;
  updated_at: string;
}

export interface SubmitResampleJobRequest {
  space_id: string;
  task_id: string;
  request_id: string;
  start: string;
  end: string;
  subjects?: string[];
}

// 由源采集任务推导出的合成源参数，以及目标频率与实际可合成列的校验结果。
export interface ResampleSourcePreview {
  source_dataset_id: string;
  source_frequency: string;
  source_series_tag: string;
  subject_count: number;
  market_id: string;
  provider?: string;
  market_type?: string;
  alignment: "epoch_utc" | "session" | string;
  calendar_id?: string;
  allowed_frequencies?: string[];
  allowed_output_fields?: OutputFieldOption[];
  validation_error?: string;
}

export interface FrequencyOption {
  value: string;
  label: string;
}

export interface DataTypeConfig {
  data_type: string;
  type_name: string;
  type_desc?: string;
  /** K 线任务的频率选项；重采样等类型为空 */
  frequency_options?: FrequencyOption[];
  /** 任务类型可选的基础输出字段（结果数据集能建的列，按建列顺序） */
  base_output_fields?: OutputFieldOption[];
  /** K 线任务可选的附加字段，默认都不选 */
  extra_fields?: ExtraFieldOption[];
}

/** K 线任务可以在输出字段里选的一个附加字段及其适用范围；显示名、分组只在 Collector 的字段规格里登记。 */
export interface ExtraFieldOption {
  field_id: string;
  /** 中文显示名 */
  display_name: string;
  /** 中文分组名（一级） */
  group?: string;
  /** 适用的市场（空间） */
  market_id: string;
  /** 适用的品种，对应标签的 market_type */
  instrument_type: string;
  /** 适用的 K 线频率 */
  frequencies?: string[];
  /** 来源提供方 */
  provider_id: string;
  /** 值类型：double 或 int */
  value_type?: string;
  /** 缺失填充策略：prev（前值填充）、zero（补零）或空 */
  fill?: string;
}

/** K 线任务可以选的一个基础输出字段。 */
export interface OutputFieldOption {
  field_id: string;
  /** 中文显示名，取自 Collector 字段规格 */
  display_name: string;
  /** 中文分组名（一级） */
  group?: string;
  /** 值类型：double、int、string、bool 或 time */
  value_type?: string;
}

export async function GetTaskList(params: GetTaskListRequest): Promise<GetTaskListResponse> {
  return call<GetTaskListRequest, GetTaskListResponse>("collector", "GetTaskList", params);
}

export async function GetTaskDetail(params: GetTaskDetailRequest): Promise<GetTaskDetailResponse> {
  return call<GetTaskDetailRequest, GetTaskDetailResponse>("collector", "GetTaskDetail", params);
}

/** 读取标签同步状态；spaceId 为空表示所有空间。 */
export async function listTagSyncStatus(spaceId = ""): Promise<TagSyncStatus[]> {
  const rsp = await call<{ space_id: string }, { items?: TagSyncStatus[] }>("collector", "ListTagSyncStatus", {
    space_id: spaceId
  });
  return rsp.items || [];
}

/** 读取可用于标的同步的（来源，市场类型）组合。 */
export async function listSubjectListers(): Promise<SubjectLister[]> {
  const rsp = await call<Record<string, never>, { listers?: SubjectLister[] }>("collector", "ListSubjectListers", {});
  return rsp.listers || [];
}

export async function CreateTask(params: CreateTaskRequest): Promise<CreateTaskResponse> {
  return call<CreateTaskRequest, CreateTaskResponse>("collector", "CreateTask", params);
}

export async function UpdateTask(params: UpdateTaskRequest): Promise<UpdateTaskResponse> {
  return call<UpdateTaskRequest, UpdateTaskResponse>("collector", "UpdateTask", params);
}

export async function DisableTask(params: DisableTaskRequest): Promise<Record<string, unknown>> {
  return call<DisableTaskRequest, Record<string, unknown>>("collector", "DisableTask", params);
}

export async function DeleteTask(params: DeleteTaskRequest): Promise<Record<string, unknown>> {
  return call<DeleteTaskRequest, Record<string, unknown>>("collector", "DeleteTask", params);
}

export async function GetDataTypeConfigs(): Promise<{ configs?: DataTypeConfig[] }> {
  return call<Record<string, never>, { configs?: DataTypeConfig[] }>("collector", "GetDataTypeConfigs", {});
}

export async function submitKlineResampleJob(
  request: SubmitResampleJobRequest
): Promise<{ job?: ResampleJob; created?: boolean }> {
  return call<SubmitResampleJobRequest, { job?: ResampleJob; created?: boolean }>("collector", "SubmitKlineResampleJob", request);
}

export async function getKlineResampleJob(spaceId: string, jobId: string): Promise<ResampleJob | undefined> {
  const response = await call<{ space_id: string; job_id: string }, { job?: ResampleJob }>("collector", "GetKlineResampleJob", {
    space_id: spaceId,
    job_id: jobId
  });
  return response.job;
}

export async function listKlineResampleJobs(spaceId: string, taskId = "", status = "", limit = 50): Promise<ResampleJob[]> {
  const response = await call<{ space_id: string; task_id: string; status: string; limit: number }, { jobs?: ResampleJob[] }>(
    "collector",
    "ListKlineResampleJobs",
    { space_id: spaceId, task_id: taskId, status, limit }
  );
  return response.jobs || [];
}

export async function cancelKlineResampleJob(spaceId: string, jobId: string): Promise<ResampleJob | undefined> {
  const response = await call<{ space_id: string; job_id: string }, { job?: ResampleJob }>(
    "collector",
    "CancelKlineResampleJob",
    {
      space_id: spaceId,
      job_id: jobId
    }
  );
  return response.job;
}

export async function previewKlineResampleSource(
  spaceId: string,
  sourceTaskId: string,
  targetFrequency = ""
): Promise<ResampleSourcePreview> {
  return call<{ space_id: string; source_task_id: string; target_frequency: string }, ResampleSourcePreview>(
    "collector",
    "PreviewKlineResampleSource",
    { space_id: spaceId, source_task_id: sourceTaskId, target_frequency: targetFrequency }
  );
}
