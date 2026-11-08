import { callStorage as callView, type StorageCallOptions } from "./http";
import type {
  FilterSpec,
  Page,
  PageResult,
  RecordKey,
  RecordRow,
  ResultColumn,
  RetInfo,
  SortSpec,
  TimeRange,
  TimeSeriesSelector,
  TimeSeriesRow,
  TotalMode,
  VersionRange
} from "./types";

export interface QueryTimeSeriesRowsReq {
  space_id: string;
  view_id: string;
  selectors?: TimeSeriesSelector[];
  time_range?: TimeRange;
  column_names?: string[];
  filter?: FilterSpec;
  sorts?: SortSpec[];
  page?: Page;
  limit?: number;
  total_mode?: TotalMode;
}

export interface SearchRecordRowsReq {
  space_id: string;
  view_id: string;
  keys?: RecordKey[];
  text_query?: string;
  version_range?: VersionRange;
  filter?: FilterSpec;
  sorts?: SortSpec[];
  column_names?: string[];
  page?: Page;
}

export function queryTimeSeriesRows(req: QueryTimeSeriesRowsReq, options: StorageCallOptions = {}) {
  return callView<
    QueryTimeSeriesRowsReq,
    { ret_info: RetInfo; columns: ResultColumn[]; rows: TimeSeriesRow[]; page_result: PageResult }
  >("QueryTimeSeriesRows", req, options);
}

export function searchRecordRows(req: SearchRecordRowsReq, options: StorageCallOptions = {}) {
  return callView<
    SearchRecordRowsReq,
    { ret_info: RetInfo; columns: ResultColumn[]; rows: RecordRow[]; page_result: PageResult }
  >("SearchRecordRows", req, options);
}
