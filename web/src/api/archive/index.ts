import type { AxiosRequestConfig } from "axios";
import { callControl, reportControlError } from "@/api/admin/http";
import type * as T from "./types";

export type * from "./types";
export * from "./raw";

/**
 * ArchiveMgr 的调用都经 Console 的 /api/admin/archive/<方法>。
 * 空间以请求头 X-Space-Id 为准（签名客户端自动带上），请求体里不写 space_id：体里的空间和头不一致会被拒绝。
 * 后台轮询的读取传 { silentError: true }，错误由页面自己显示；其余调用失败时在这里弹提示：
 * Console 转发的业务错误是 HTTP 200 加失败的 ret_info，不进请求层的错误拦截器，callControl 只抛错不提示。
 */
const call = <Req extends object, Rsp>(method: string, req: Req, config?: AxiosRequestConfig) =>
  callControl<Req, Rsp>("archive", method, req, config).catch((error: unknown) => {
    if (!config?.silentError) reportControlError(error);
    throw error;
  });

export const getArchiveOverview = (config?: AxiosRequestConfig) =>
  call<Record<string, never>, T.GetArchiveOverviewRsp>("GetArchiveOverview", {}, config);

export const listArchiveTasks = (page: T.Page, config?: AxiosRequestConfig) =>
  call<{ page: T.Page }, T.ListArchiveTasksRsp>("ListArchiveTasks", { page }, config);

/** dirName 为空时检查建议的目录名；不为空时检查它是否已被占用（不合规的目录名返回参数错误）。 */
export const prepareArchiveTask = (datasetId: string, dirName = "", config?: AxiosRequestConfig) =>
  call<{ dataset_id: string; dir_name?: string }, T.PrepareArchiveTaskRsp>(
    "PrepareArchiveTask",
    dirName ? { dataset_id: datasetId, dir_name: dirName } : { dataset_id: datasetId },
    config
  );

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
