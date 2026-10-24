import { callControl } from "@/api/admin/http";

export interface CollectorPage {
  page?: number;
  size?: number;
  total?: number;
  has_more?: boolean;
  next_cursor?: string;
}

export interface CollectorTaskResult {
  result_name?: string;
  view_id?: string;
  view_ids?: string[];
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
  tag_ids?: string[];
  definition_hash?: string;
  collect_params?: Record<string, unknown>;
  enabled?: boolean | string;
  creator?: string;
  create_time?: string;
  modify_time?: string;
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
  tag_ids?: string[];
  definition_hash?: string;
  collect_params?: Record<string, unknown>;
  enabled?: boolean;
  creator?: string;
}

export interface CollectionTaskResultConfig {
  data_node_id?: string;
  description?: string;
}

export interface GetTaskListRequest {
  space_id: string;
  data_type?: string;
  enabled?: boolean;
  task_id?: string;
  keyword?: string;
  page?: { page?: number; size?: number; cursor?: string };
}

export interface GetTaskListResponse {
  tasks?: CollectorTask[];
  page?: CollectorPage;
}

export interface GetTaskDetailRequest {
  space_id: string;
  task_id: string;
}

export interface GetTaskDetailResponse {
  task?: CollectorTask;
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

// 历史合成作业：范围 [start, end) 按目标桶推进，状态 queued / running / syncing / succeeded / failed / cancelled。
export interface ResampleJob {
  job_id: string;
  task_id: string;
  request_id: string;
  origin: "manual" | "source_backfill" | string;
  subjects?: string[];
  start: string;
  end: string;
  next_bucket: string;
  not_before: string;
  status: string;
  total_buckets: number;
  done_buckets: number;
  written: number;
  skipped: number;
  skipped_samples?: string[];
  last_error?: string;
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

// 由源采集任务推导出的合成源参数，以及目标频率的校验结果。
export interface ResampleSourcePreview {
  source_dataset_id: string;
  source_frequency: string;
  source_series_tag: string;
  subject_count: number;
  market_id: string;
  provider: string;
  market_type: string;
  alignment: "epoch_utc" | "session" | string;
  allowed_frequencies?: string[];
  validation_error?: string;
}

export interface DataTypeConfig {
  id: number;
  data_type: string;
  type_name: string;
  type_desc: string;
  data_source_options?: Record<string, unknown>;
  sort_order: number;
  version: number;
  create_time: string;
  modify_time: string;
}

export interface DataTypeFieldConfig {
  id: number;
  data_type: string;
  field_key: string;
  field_name: string;
  field_type: string;
  is_required: boolean;
  default_value?: unknown;
  field_options?: Record<string, unknown>;
  data_source_options?: Record<string, unknown>;
  sort_order: number;
}

export interface DataTypeConfigDetail {
  config?: DataTypeConfig;
  fields?: DataTypeFieldConfig[];
}

export async function GetTaskList(params: GetTaskListRequest): Promise<GetTaskListResponse> {
  return callControl<GetTaskListRequest, GetTaskListResponse>("collector", "GetTaskList", params);
}

export async function GetTaskDetail(params: GetTaskDetailRequest): Promise<GetTaskDetailResponse> {
  return callControl<GetTaskDetailRequest, GetTaskDetailResponse>("collector", "GetTaskDetail", params);
}

export async function CreateTask(params: CreateTaskRequest): Promise<CreateTaskResponse> {
  return callControl<CreateTaskRequest, CreateTaskResponse>("collector", "CreateTask", params);
}

export async function UpdateTask(params: UpdateTaskRequest): Promise<UpdateTaskResponse> {
  return callControl<UpdateTaskRequest, UpdateTaskResponse>("collector", "UpdateTask", params);
}

export async function DisableTask(params: DisableTaskRequest): Promise<Record<string, unknown>> {
  return callControl<DisableTaskRequest, Record<string, unknown>>("collector", "DisableTask", params);
}

export async function DeleteTask(params: DeleteTaskRequest): Promise<Record<string, unknown>> {
  return callControl<DeleteTaskRequest, Record<string, unknown>>("collector", "DeleteTask", params);
}

export async function GetDataTypeConfigs(): Promise<{ configs?: DataTypeConfig[] }> {
  return callControl<Record<string, never>, { configs?: DataTypeConfig[] }>("collector", "GetDataTypeConfigs", {});
}

export async function GetDataTypeConfigWithFields(dataType: string): Promise<{ detail?: DataTypeConfigDetail }> {
  return callControl<{ data_type: string }, { detail?: DataTypeConfigDetail }>("collector", "GetDataTypeConfigWithFields", {
    data_type: dataType
  });
}

export async function submitKlineResampleJob(
  request: SubmitResampleJobRequest
): Promise<{ job?: ResampleJob; created?: boolean }> {
  return callControl<SubmitResampleJobRequest, { job?: ResampleJob; created?: boolean }>(
    "collector",
    "SubmitKlineResampleJob",
    request
  );
}

export async function getKlineResampleJob(spaceId: string, jobId: string): Promise<ResampleJob | undefined> {
  const response = await callControl<{ space_id: string; job_id: string }, { job?: ResampleJob }>(
    "collector",
    "GetKlineResampleJob",
    {
      space_id: spaceId,
      job_id: jobId
    }
  );
  return response.job;
}

export async function listKlineResampleJobs(spaceId: string, taskId = "", status = "", limit = 50): Promise<ResampleJob[]> {
  const response = await callControl<
    { space_id: string; task_id: string; status: string; limit: number },
    { jobs?: ResampleJob[] }
  >("collector", "ListKlineResampleJobs", { space_id: spaceId, task_id: taskId, status, limit });
  return response.jobs || [];
}

export async function cancelKlineResampleJob(spaceId: string, jobId: string): Promise<ResampleJob | undefined> {
  const response = await callControl<{ space_id: string; job_id: string }, { job?: ResampleJob }>(
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
  return callControl<{ space_id: string; source_task_id: string; target_frequency: string }, ResampleSourcePreview>(
    "collector",
    "PreviewKlineResampleSource",
    { space_id: spaceId, source_task_id: sourceTaskId, target_frequency: targetFrequency }
  );
}
