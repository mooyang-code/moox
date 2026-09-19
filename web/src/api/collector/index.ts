import { callControl } from "@/api/admin/http";
import type { ResampleBackfillSummary } from "@/views/collector/collection-tasks/resample-backfill";

export interface KlineResampleBackfillRequest {
  space_id: string;
  task_id: string;
  request_id: string;
  start: string;
  end: string;
}

export interface CollectorTaskResult {
  result_name?: string;
  view_id?: string;
  status?: string;
  last_data_time?: string;
  data_kind?: string;
}

export interface CollectorTask {
  task_id: string;
  task_name?: string;
  description?: string;
  data_type?: string;
  provider?: string;
  market_type?: string;
  collect_params?: Record<string, unknown>;
  enabled?: boolean | string;
  creator?: string;
  create_time?: string;
  modify_time?: string;
  result?: CollectorTaskResult;
}

export async function listCollectorTasks(spaceId: string): Promise<CollectorTask[]> {
  const response = await callControl<{ space_id: string; page: { page: number; size: number } }, { tasks?: CollectorTask[] }>(
    "collectmgr",
    "GetTaskList",
    { space_id: spaceId, page: { page: 1, size: 1000 } }
  );
  return response.tasks || [];
}

export async function deleteCollectorTask(spaceId: string, taskId: string, deleteResultData: boolean) {
  return callControl("collectmgr", "DeleteTask", {
    space_id: spaceId,
    task_id: taskId,
    delete_result_data: deleteResultData
  });
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
