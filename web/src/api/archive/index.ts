import type { AxiosRequestConfig } from "axios";
import { callControl } from "@/api/admin/http";
import type * as T from "./types";

export type * from "./types";

/**
 * ArchiveMgr 的调用都经 Console 的 /api/admin/archive/<方法>。
 * 空间以请求头 X-Space-Id 为准（签名客户端自动带上），请求体里不写 space_id：体里的空间和头不一致会被拒绝。
 * 后台轮询的读取传 { silentError: true }，错误由页面自己显示。
 */
const call = <Req extends object, Rsp>(method: string, req: Req, config?: AxiosRequestConfig) =>
  callControl<Req, Rsp>("archive", method, req, config);

export const getArchiveOverview = (config?: AxiosRequestConfig) =>
  call<Record<string, never>, T.GetArchiveOverviewRsp>("GetArchiveOverview", {}, config);

export const listArchiveTasks = (page: T.Page, config?: AxiosRequestConfig) =>
  call<{ page: T.Page }, T.ListArchiveTasksRsp>("ListArchiveTasks", { page }, config);

export const prepareArchiveTask = (datasetId: string, config?: AxiosRequestConfig) =>
  call<{ dataset_id: string }, T.PrepareArchiveTaskRsp>("PrepareArchiveTask", { dataset_id: datasetId }, config);

export const previewArchiveSchedule = (req: T.PreviewArchiveScheduleReq, config?: AxiosRequestConfig) =>
  call<T.PreviewArchiveScheduleReq, T.PreviewArchiveScheduleRsp>("PreviewArchiveSchedule", req, config);

export const createArchiveTask = (req: T.CreateArchiveTaskReq) =>
  call<T.CreateArchiveTaskReq, T.ArchiveTaskRsp>("CreateArchiveTask", req);

export const updateArchiveTask = (req: T.UpdateArchiveTaskReq) =>
  call<T.UpdateArchiveTaskReq, T.ArchiveTaskRsp>("UpdateArchiveTask", req);

export const deleteArchiveTask = (datasetId: string) =>
  call<{ dataset_id: string }, Record<string, never>>("DeleteArchiveTask", { dataset_id: datasetId });

export const runArchiveTaskNow = (datasetId: string) =>
  call<{ dataset_id: string }, T.ArchiveRunRsp>("RunArchiveTaskNow", { dataset_id: datasetId });

export const backfillArchiveRange = (req: T.BackfillArchiveRangeReq) =>
  call<T.BackfillArchiveRangeReq, T.ArchiveRunRsp>("BackfillArchiveRange", req);

export const cancelArchiveRun = (runId: string) =>
  call<{ run_id: string }, T.ArchiveRunRsp>("CancelArchiveRun", { run_id: runId });

export const listArchiveRuns = (req: T.ListArchiveRunsReq, config?: AxiosRequestConfig) =>
  call<T.ListArchiveRunsReq, T.ListArchiveRunsRsp>("ListArchiveRuns", req, config);

export const listArchivePartitions = (req: T.ListArchivePartitionsReq, config?: AxiosRequestConfig) =>
  call<T.ListArchivePartitionsReq, T.ListArchivePartitionsRsp>("ListArchivePartitions", req, config);

export const getArchivePartition = (datasetId: string, partitionKey: string, config?: AxiosRequestConfig) =>
  call<{ dataset_id: string; partition_key: string }, T.GetArchivePartitionRsp>(
    "GetArchivePartition",
    { dataset_id: datasetId, partition_key: partitionKey },
    config
  );
