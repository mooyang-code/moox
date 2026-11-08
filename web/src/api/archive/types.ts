/**
 * 归档（ArchiveMgr）接口类型，按 modules/archive/proto/archive.proto 写。
 * - 字段都可选：protojson 省略零值；
 * - uint64 经 protojson 是字符串，写成 number | string，显示前用 toNum 转换；uint32 是数字；
 * - 时间是 RFC3339（UTC）字符串，空串表示没有。
 * - 标“批 2 之后才有”的字段在对象存储（远端副本）上线前不出现。
 */
import type { Page, PageResult } from "@/api/storage/types";

export type { Page, PageResult };

export type ArchiveRunKind = "scheduled" | "manual" | "tick" | "range";
export type ArchiveRunStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type ArchiveTaskStatus = "enabled" | "paused";
export type ArchiveHealth = "ok" | "warning" | "error" | "paused";
export type ArchivePartitionStatus = "open" | "exported" | "final";
/** 批 2 之后才有 */
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
  worker_id?: string;
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
  /** 批 2 之后才有 */
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
  data_node_id?: string;
  retention?: string;
  /** 每收盘多少根拉取一次（1～1440）；1d 及以上每天一次 */
  pull_every_bars?: number;
  /** 复核间隔（秒）：低于 1d 为 3600，1d 及以上为 86400 */
  interval_seconds?: number;
  backfill_from?: string;
  status?: ArchiveTaskStatus;
  last_boundary?: string;
  last_review?: string;
  next_pull_at?: string;
  consecutive_skips?: number;
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
  /** 批 2 之后才有 */
  remote_verified_count?: number;
  /** 批 2 之后才有 */
  remote_pending_count?: number;
}

export interface ArchivePartition {
  space_id?: string;
  dataset_id?: string;
  /** 本地日（YYYY-MM-DD）或月（YYYY-MM）的标签，不是时刻 */
  partition_key?: string;
  range_start?: string;
  range_end?: string;
  status?: ArchivePartitionStatus;
  worker_id?: string;
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
  /** 批 2 之后才有 */
  remote_state?: ArchiveRemoteState;
  /** 批 2 之后才有 */
  remote_key?: string;
  /** 批 2 之后才有 */
  remote_verified_at?: string;
  /** 批 2 之后才有 */
  remote_error?: string;
}

/** 批 2 之后才有：worker 报告的对象存储配置 */
export interface ArchiveObjectStore {
  endpoint?: string;
  bucket?: string;
  prefix?: string;
  storage_class?: string;
}

export interface ArchiveWorkerInfo {
  worker_id?: string;
  instance_id?: string;
  version?: string;
  root_dir?: string;
  disk_total_bytes?: number | string;
  disk_free_bytes?: number | string;
  staging_bytes?: number | string;
  maintaining?: boolean;
  pacing_json?: string;
  running_run_ids?: string[];
  lease_until?: string;
  last_heartbeat_at?: string;
  /** 租约未过期 */
  active?: boolean;
  /** 批 2 之后才有 */
  object_store?: ArchiveObjectStore;
}

export interface GetArchiveOverviewRsp {
  worker?: ArchiveWorkerInfo;
  stale_workers?: ArchiveWorkerInfo[];
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
  data_node_id?: string;
  recommended_pull_every_bars?: number;
  exists?: boolean;
}

export interface PreviewArchiveScheduleReq {
  dataset_id: string;
  pull_every_bars: number;
}

export interface PreviewArchiveScheduleRsp {
  next_runs?: string[];
  interval_seconds?: number;
  read_span_seconds?: number;
  partition_export_estimates?: string[];
  next_reviews?: string[];
}

export interface CreateArchiveTaskReq {
  dataset_id: string;
  pull_every_bars: number;
  /** 空串表示不指定 */
  backfill_from: string;
  paused: boolean;
}

export interface UpdateArchiveTaskReq {
  dataset_id: string;
  /** 0 表示不修改 */
  pull_every_bars?: number;
  /** 空串表示不修改，不能用来清除 */
  backfill_from?: string;
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
  /** 是否列出成功的到点拉取（每分钟一条），默认不列 */
  include_ticks?: boolean;
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
