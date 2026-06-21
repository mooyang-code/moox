import { callView } from './http';
import type {
  FilterExpr,
  Page,
  PageResult,
  RecordKey,
  RecordRow,
  ResultColumn,
  RetInfo,
  SortSpec,
  TimeRange,
  TimeSeriesKey,
  TimeSeriesRow,
  VersionRange,
} from './types';

export interface QueryTimeSeriesRowsReq {
  space_id: string;
  view_id: string;
  keys?: TimeSeriesKey[];
  time_range?: TimeRange;
  column_names?: string[];
  filters?: FilterExpr[];
  sorts?: SortSpec[];
  page?: Page;
}

export interface SearchRecordRowsReq {
  space_id: string;
  view_id: string;
  keys?: RecordKey[];
  text_query?: string;
  version_range?: VersionRange;
  filters?: FilterExpr[];
  sorts?: SortSpec[];
  column_names?: string[];
  page?: Page;
}

export function queryTimeSeriesRows(req: QueryTimeSeriesRowsReq) {
  return callView<QueryTimeSeriesRowsReq, { ret_info: RetInfo; columns: ResultColumn[]; rows: TimeSeriesRow[]; page_result: PageResult }>('QueryTimeSeriesRows', req);
}

export function searchRecordRows(req: SearchRecordRowsReq) {
  return callView<SearchRecordRowsReq, { ret_info: RetInfo; columns: ResultColumn[]; rows: RecordRow[]; page_result: PageResult }>('SearchRecordRows', req);
}

export function rebuildTimeSeriesView(req: { space_id: string; view_id: string }) {
  return callView<typeof req, { ret_info: RetInfo; rebuild_id: string }>('RebuildTimeSeriesView', req);
}

export function rebuildRecordView(req: { space_id: string; view_id: string }) {
  return callView<typeof req, { ret_info: RetInfo; rebuild_id: string }>('RebuildRecordView', req);
}
