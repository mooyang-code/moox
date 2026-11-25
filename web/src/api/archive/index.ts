import { call } from "@/api/http";
import type * as T from "./types";

export type * from "./types";
export * from "./raw";

/**
 * ArchiveMgr 的调用都经 Console 的 /api/admin/archive/<方法>。
 * 空间以请求头 X-Space-Id 为准（签名客户端自动带上），请求体里不写 space_id：体里的空间和头不一致会被拒绝。
 * 失败只抛错不提示，由页面决定怎么显示。
 */
const callArchive = <Req extends object, Rsp>(method: string, req: Req) => call<Req, Rsp>("archive", method, req);

export const getArchiveOverview = () => callArchive<Record<string, never>, T.GetArchiveOverviewRsp>("GetArchiveOverview", {});

export const listArchiveTasks = (page: T.Page) =>
  callArchive<{ page: T.Page }, T.ListArchiveTasksRsp>("ListArchiveTasks", { page });

/** dirName 为空时检查建议的目录名；不为空时检查它是否已被占用（不合规的目录名返回参数错误）。 */
export const prepareArchiveTask = (datasetId: string, dirName = "") =>
  callArchive<{ dataset_id: string; dir_name?: string }, T.PrepareArchiveTaskRsp>(
    "PrepareArchiveTask",
    dirName ? { dataset_id: datasetId, dir_name: dirName } : { dataset_id: datasetId }
  );

export const createArchiveTask = (req: T.CreateArchiveTaskReq) =>
  callArchive<T.CreateArchiveTaskReq, T.ArchiveTaskRsp>("CreateArchiveTask", req);

export const updateArchiveTask = (req: T.UpdateArchiveTaskReq) =>
  callArchive<T.UpdateArchiveTaskReq, T.ArchiveTaskRsp>("UpdateArchiveTask", req);

export const deleteArchiveTask = (datasetId: string) =>
  callArchive<{ dataset_id: string }, Record<string, never>>("DeleteArchiveTask", { dataset_id: datasetId });

export const runArchiveTaskNow = (datasetId: string) =>
  callArchive<{ dataset_id: string }, T.ArchiveRunRsp>("RunArchiveTaskNow", { dataset_id: datasetId });

export const backfillArchiveRange = (req: T.BackfillArchiveRangeReq) =>
  callArchive<T.BackfillArchiveRangeReq, T.ArchiveRunRsp>("BackfillArchiveRange", req);

export const cancelArchiveRun = (runId: string) =>
  callArchive<{ run_id: string }, T.ArchiveRunRsp>("CancelArchiveRun", { run_id: runId });

export const listArchiveRuns = (req: T.ListArchiveRunsReq) =>
  callArchive<T.ListArchiveRunsReq, T.ListArchiveRunsRsp>("ListArchiveRuns", req);

export const listArchivePartitions = (req: T.ListArchivePartitionsReq) =>
  callArchive<T.ListArchivePartitionsReq, T.ListArchivePartitionsRsp>("ListArchivePartitions", req);

export const getArchivePartition = (datasetId: string, partitionKey: string) =>
  callArchive<{ dataset_id: string; partition_key: string }, T.GetArchivePartitionRsp>("GetArchivePartition", {
    dataset_id: datasetId,
    partition_key: partitionKey
  });
