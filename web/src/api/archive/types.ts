/**
 * 归档（ArchiveMgr）接口类型，按 modules/archive/proto/archive.proto 写。
 * - 字段都可选：protojson 省略零值；
 * - uint64 经 protojson 是字符串，写成 number | string，显示前用 toNum 转换；uint32 是数字；
 * - 时间是 RFC3339（UTC）字符串，空串表示没有。
 */
import type { Page, PageResult } from "@/api/storage/types";

export type { Page, PageResult };

/** follow：每分钟按 Storage 变更日志跟随；manual：整段重读线上保留期；range：补拉区间；import：moox-cli import 的归档目标 */
export type ArchiveRunKind = "follow" | "manual" | "range" | "import";
export type ArchiveRunStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type ArchiveTaskStatus = "enabled" | "paused";
export type ArchiveHealth = "ok" | "warning" | "error" | "paused";
export type ArchivePartitionStatus = "open" | "exported" | "final";
/** off：没启用对象存储；none：还没有远端副本；verified：远端是当前版本且已校验 */
export type ArchiveRemoteState = "off" | "none" | "pending" | "verified" | "failed";

/** 超出线上保留期、无法补齐的区间 */
export interface UnrecoverableRange {
  subject_id?: string;
  series_tag?: string;
  start_time?: string;
  end_time?: string;
}

export interface ArchiveRun {
  run_id?: string;
  space_id?: string;
  dataset_id?: string;
  kind?: ArchiveRunKind;
  range_start?: string;
  range_end?: string;
  status?: ArchiveRunStatus;
  scheduled_at?: string;
  started_at?: string;
  finished_at?: string;
  lease_until?: string;
  subjects_total?: number | string;
  subjects_done?: number | string;
  request_count?: number | string;
  bytes_read?: number | string;
  rows_read?: number | string;
  rows_added?: number | string;
  rows_changed?: number | string;
  partitions_exported?: number;
  partitions_finalized?: number;
  partitions_uploaded?: number;
  unrecoverable?: UnrecoverableRange[];
  message?: string;
}

export interface ArchiveTask {
  space_id?: string;
  dataset_id?: string;
  freq?: string;
  /** day：按空间本地日；month：按月 */
  partition_unit?: "day" | "month" | string;
  retention?: string;
  /** 回补起点（归档起点）：创建时确定，之后不改 */
  backfill_from?: string;
  status?: ArchiveTaskStatus;
  /** 最近一次成功的跟随或整段重读的开始时间 */
  last_success_at?: string;
  final_until?: string;
  creator?: string;
  created_at?: string;
  updated_at?: string;
  health?: ArchiveHealth;
  health_reason?: string;
  partition_count?: number;
  file_count?: number;
  total_rows?: number | string;
  total_file_bytes?: number | string;
  min_data_time?: string;
  max_data_time?: string;
  last_run?: ArchiveRun;
  unrecoverable?: UnrecoverableRange[];
  blocked_reason?: string;
  remote_verified_count?: number;
  remote_pending_count?: number;
  /** 用途：collect（采集结果）、factor（因子结果）、data（其他） */
  purpose?: string;
  /** 归档目录名，空间内唯一，建好后不改；分区路径 <空间>/<用途>/<目录名>/<年>/<文件> */
  dir_name?: string;
}

export interface ArchivePartition {
  space_id?: string;
  dataset_id?: string;
  /** 本地日（YYYY-MM-DD）或月（YYYY-MM）的标签，不是时刻 */
  partition_key?: string;
  range_start?: string;
  range_end?: string;
  status?: ArchivePartitionStatus;
  row_count?: number | string;
  subject_count?: number | string;
  min_data_time?: string;
  max_data_time?: string;
  file_path?: string;
  file_bytes?: number | string;
  file_sha256?: string;
  columns?: string[];
  /** 表头加前 20 行的 CSV */
  preview?: string;
  export_count?: number;
  last_changed_at?: string;
  exported_at?: string;
  finalized_at?: string;
  last_run_id?: string;
  remote_state?: ArchiveRemoteState;
  remote_key?: string;
  remote_sha256?: string;
  remote_verified_at?: string;
  remote_error?: string;
}

/** worker 报告的对象存储配置；endpoint 为空表示没启用 */
export interface ArchiveObjectStore {
  endpoint?: string;
  bucket?: string;
  prefix?: string;
  storage_class?: string;
  url_style?: string;
  /** 最近一次探测失败的原因，空表示正常 */
  probe_error?: string;
  /** 连续探测失败的起始时刻 */
  unavailable_since?: string;
}

export interface ArchiveWorkerInfo {
  worker_id?: string;
  version?: string;
  root_dir?: string;
  /** 磁盘总量（statfs Blocks） */
  disk_total_bytes?: number | string;
  /** 可用量（statfs Bavail），worker 按它和 min_free_bytes 判断是否暂停领取新运行 */
  disk_free_bytes?: number | string;
  /** 已用量（Blocks − Bfree）；已用 + 可用小于总量，差额是 root 保留空间 */
  disk_used_bytes?: number | string;
  /** 可用量下限（worker 配置） */
  min_free_bytes?: number | string;
  staging_bytes?: number | string;
  maintaining?: boolean;
  pacing_json?: string;
  running_run_ids?: string[];
  last_heartbeat_at?: string;
  /** 最近 2 分钟内有心跳 */
  active?: boolean;
  object_store?: ArchiveObjectStore;
}

export interface GetArchiveOverviewRsp {
  worker?: ArchiveWorkerInfo;
  /** 只算当前空间 */
  running_runs?: ArchiveRun[];
  task_count?: number;
  enabled_task_count?: number;
  queued_run_count?: number;
  alerts?: string[];
}

export interface ListArchiveTasksRsp {
  tasks?: ArchiveTask[];
  page_result?: PageResult;
}

export interface PrepareArchiveTaskRsp {
  freq?: string;
  partition_unit?: string;
  retention?: string;
  earliest_backfill_from?: string;
  exists?: boolean;
  purpose?: string;
  /** 按“交易所 + 品种 + 类型 + 频率”生成的建议目录名 */
  suggested_dir_name?: string;
  /** 请求里的目录名（为空时是建议值）已被空间内其他任务使用 */
  dir_name_conflict?: boolean;
}

export interface CreateArchiveTaskReq {
  dataset_id: string;
  /** 空串表示取线上保留期起点；保留期为 forever 时必填 */
  backfill_from: string;
  paused: boolean;
  /** 空表示取建议值 */
  dir_name: string;
}

export interface UpdateArchiveTaskReq {
  dataset_id: string;
  status?: ArchiveTaskStatus;
  clear_unrecoverable?: boolean;
}

export interface ArchiveTaskRsp {
  task?: ArchiveTask;
}

export interface ArchiveRunRsp {
  run?: ArchiveRun;
}

export interface BackfillArchiveRangeReq {
  dataset_id: string;
  range_start: string;
  range_end: string;
}

export interface ListArchiveRunsReq {
  /** 为空表示当前空间的全部任务 */
  dataset_id: string;
  /** 为空表示全部状态 */
  status?: ArchiveRunStatus | "";
  /** 是否列出成功的跟随运行（每分钟一条），默认不列 */
  include_follow?: boolean;
  page: Page;
}

export interface ListArchiveRunsRsp {
  runs?: ArchiveRun[];
  page_result?: PageResult;
}

export interface ListArchivePartitionsReq {
  dataset_id: string;
  /** YYYY-MM，含；为空表示不限 */
  from_month?: string;
  to_month?: string;
  page: Page;
}

export interface ListArchivePartitionsRsp {
  partitions?: ArchivePartition[];
  page_result?: PageResult;
}

export interface GetArchivePartitionRsp {
  partition?: ArchivePartition;
}

/** 最新数据的一列：data_time、subject_id、series_tag 在前，值列按数据集说明（dataset.json）的顺序在后 */
export interface ArchiveRecentColumn {
  name: string;
  /** double、int、string、bool、time、bytes */
  type: string;
}

export interface ArchiveRecentRowsRsp {
  columns?: ArchiveRecentColumn[];
  /** 单元格按归档 CSV 的规则格式化（时间 RFC3339 UTC、布尔 true/false），缺失值为 null，与空字符串不同 */
  rows?: (string | null)[][];
}
