import { callStorage } from "./http";
import type {
  Page,
  PageResult,
  RecordKey,
  RecordRow,
  RetInfo,
  SortOrder,
  TimeRange,
  TimeSeriesKey,
  TimeSeriesRow,
  VersionRange
} from "./types";

export interface ReadTimeSeriesRowsReq {
  keys: TimeSeriesKey[];
  time_range?: TimeRange;
  order?: SortOrder;
  column_names?: string[];
  page?: Page;
}

export interface ReadRecordRowsReq {
  keys: RecordKey[];
  version_range?: VersionRange;
  order?: SortOrder;
  column_names?: string[];
  page?: Page;
}

export function writeTimeSeriesRows(rows: TimeSeriesRow[]) {
  return callStorage<{ rows: TimeSeriesRow[] }, { ret_info: RetInfo }>("MergeTimeSeriesRows", { rows });
}

export function readTimeSeriesRows(req: ReadTimeSeriesRowsReq) {
  return callStorage<ReadTimeSeriesRowsReq, { ret_info: RetInfo; rows: TimeSeriesRow[]; page_result: PageResult }>(
    "ReadTimeSeriesRows",
    req
  );
}

export function writeRecordRows(rows: RecordRow[]) {
  return callStorage<{ rows: RecordRow[] }, { ret_info: RetInfo }>("MergeRecordRows", { rows });
}

export function readRecordRows(req: ReadRecordRowsReq) {
  return callStorage<ReadRecordRowsReq, { ret_info: RetInfo; rows: RecordRow[]; page_result: PageResult }>("ReadRecordRows", req);
}
