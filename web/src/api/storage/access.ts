import { callAccess } from './http';
import type {
  Page,
  PageResult,
  RecordKey,
  RetInfo,
  SortOrder,
  TimeRange,
  TimeSeriesKey,
  TimeSeriesRow,
  RevisionRange,
  RecordMutation,
  RecordReadMode,
} from './types';

export interface ReadTimeSeriesRowsReq {
  keys: TimeSeriesKey[];
  time_range?: TimeRange;
  order?: SortOrder;
  column_names?: string[];
  page?: Page;
}

export interface ReadRecordRowsReq {
  keys: RecordKey[];
  order?: SortOrder;
  column_names?: string[];
  page?: Page;
  mode?: RecordReadMode;
  revision_range?: RevisionRange;
}

export interface UpsertRecordRowsReq {
  request_id: string;
  mutations: RecordMutation[];
}

export function writeTimeSeriesRows(rows: TimeSeriesRow[]) {
  return callAccess<{ rows: TimeSeriesRow[] }, { ret_info: RetInfo }>('WriteTimeSeriesRows', { rows });
}

export function readTimeSeriesRows(req: ReadTimeSeriesRowsReq) {
  return callAccess<ReadTimeSeriesRowsReq, { ret_info: RetInfo; rows: TimeSeriesRow[]; page_result: PageResult }>('ReadTimeSeriesRows', req);
}

export function readRecordRows(req: ReadRecordRowsReq) {
  return callAccess<ReadRecordRowsReq, { ret_info: RetInfo; rows: RecordRow[]; page_result: PageResult }>('ReadRecordRows', req);
}

export function upsertRecordRows(req: UpsertRecordRowsReq) {
  return callAccess<UpsertRecordRowsReq, { ret_info: RetInfo; rows: RecordRow[] }>('UpsertRecordRows', req);
}
