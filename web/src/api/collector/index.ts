import { callControl } from "@/api/admin/http";
import type { ResampleBackfillSummary } from "@/views/collector/collection-tasks/resample-backfill";

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
  provider?: string;
  market_type?: string;
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
  provider?: string;
  market_type?: string;
  collect_params?: Record<string, unknown>;
  enabled?: boolean;
  creator?: string;
}

export interface CollectionTaskResultConfig {
  data_node_id?: string;
  keep_duration?: string;
  description?: string;
}

export interface GetTaskListRequest {
  space_id: string;
  data_type?: string;
  provider?: string;
  market_type?: string;
  enabled?: boolean;
  task_id?: string;
  page?: { page?: number; size?: number; cursor?: string };
}

export interface GetTaskListResponse {
  tasks?: CollectorTask[];
  page?: CollectorPage;
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

export interface KlineResampleBackfillRequest {
  space_id: string;
  task_id: string;
  request_id: string;
  start: string;
  end: string;
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

export async function GetTaskList(params: GetTaskListRequest): Promise<GetTaskListResponse> {
  return callControl<GetTaskListRequest, GetTaskListResponse>("collectmgr", "GetTaskList", params);
}

export async function CreateTask(params: CreateTaskRequest): Promise<CreateTaskResponse> {
  return callControl<CreateTaskRequest, CreateTaskResponse>("collectmgr", "CreateTask", params);
}

export async function UpdateTask(params: UpdateTaskRequest): Promise<UpdateTaskResponse> {
  return callControl<UpdateTaskRequest, UpdateTaskResponse>("collectmgr", "UpdateTask", params);
}

export async function DisableTask(params: DisableTaskRequest): Promise<Record<string, unknown>> {
  return callControl<DisableTaskRequest, Record<string, unknown>>("collectmgr", "DisableTask", params);
}

export async function DeleteTask(params: DeleteTaskRequest): Promise<Record<string, unknown>> {
  return callControl<DeleteTaskRequest, Record<string, unknown>>("collectmgr", "DeleteTask", params);
}

export async function GetDataTypeConfigs(): Promise<{ configs?: DataTypeConfig[] }> {
  return callControl<Record<string, never>, { configs?: DataTypeConfig[] }>("collectmgr", "GetDataTypeConfigs", {});
}

export async function startKlineResampleBackfill(request: KlineResampleBackfillRequest) {
  return callControl<KlineResampleBackfillRequest, Record<string, unknown>>("collectmgr", "StartKlineResampleBackfill", request);
}

export async function cancelKlineResampleBackfill(
  request: Pick<KlineResampleBackfillRequest, "space_id" | "task_id" | "request_id">
) {
  return callControl<typeof request, Record<string, unknown>>("collectmgr", "CancelKlineResampleBackfill", request);
}

export async function getKlineResampleBackfillStatus(
  spaceId: string,
  taskId: string,
  requestId = ""
): Promise<ResampleBackfillSummary | null> {
  let response: {
    request_id?: string;
    start?: string;
    end?: string;
    next_bucket?: string;
    state?: ResampleBackfillSummary["state"];
    participants?: number;
    running?: number;
    waiting_source?: number;
    syncing?: number;
    complete?: number;
    canceled?: number;
    failed?: number;
  };
  try {
    response = await callControl<{ space_id: string; task_id: string; request_id?: string }, typeof response>(
      "collectmgr",
      "GetKlineResampleBackfill",
      { space_id: spaceId, task_id: taskId, request_id: requestId }
    );
  } catch (error) {
    if (error instanceof Error && /backfill request not found/i.test(error.message)) return null;
    throw error;
  }
  if (!response.request_id) return null;
  const participants = Number(response.participants || 0);
  const active = Number(response.running || 0) + Number(response.waiting_source || 0) + Number(response.syncing || 0);
  const terminal = Number(response.complete || 0) + Number(response.canceled || 0) + Number(response.failed || 0);
  if (participants > 0 && active === 0 && terminal >= participants) return null;
  return {
    requestId: String(response.request_id),
    start: String(response.start || ""),
    end: String(response.end || ""),
    nextBucket: String(response.next_bucket || ""),
    state: response.state || "running",
    participants: Number(response.participants || 0)
  };
}
