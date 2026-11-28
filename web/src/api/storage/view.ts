import { call, RequestError, type CallOptions } from "@/api/http";
import type {
  FilterSpec,
  Page,
  PageResult,
  RecordRowKey,
  ResultColumn,
  RetInfo,
  Row,
  SortSpec,
  TimeRange,
  TimeSeriesSelector,
  TotalMode,
  VersionRange
} from "./types";

/** Storage 的调用；读取带短时缓存，见 @/api/http。 */
const callStorage = <TReq extends object, TRsp>(method: string, req: TReq, options?: CallOptions) =>
  call<TReq, TRsp>("storage", method, req, options);

export interface QueryTimeSeriesRowsReq {
  space_id: string;
  dataset_id: string;
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
  dataset_id: string;
  keys?: RecordRowKey[];
  text_query?: string;
  version_range?: VersionRange;
  filter?: FilterSpec;
  sorts?: SortSpec[];
  column_names?: string[];
  page?: Page;
}

export function queryTimeSeriesRows(req: QueryTimeSeriesRowsReq, options: CallOptions = {}) {
  return callStorage<
    QueryTimeSeriesRowsReq,
    { ret_info: RetInfo; columns: ResultColumn[]; rows: Row[]; page_result: PageResult }
  >("QueryTimeSeriesRows", req, options);
}

export function searchRecordRows(req: SearchRecordRowsReq, options: CallOptions = {}) {
  return callStorage<SearchRecordRowsReq, { ret_info: RetInfo; columns: ResultColumn[]; rows: Row[]; page_result: PageResult }>(
    "SearchRecordRows",
    req,
    options
  );
}

export interface GetViewStateReq {
  space_id: string;
  dataset_id: string;
}

/** View 跟随数据集变更日志已扫描到的位置。 */
export interface DatasetChangeCursor {
  store_id?: string;
  /** uint64，JSON 里可能是字符串。 */
  sequence?: number | string;
}

/** View 当前的索引状态（DataView.GetViewState）。 */
export interface GetViewStateRsp {
  ret_info: RetInfo;
  /** 可读的活动索引；为空表示还没建好或正在重建，View 暂不可读。 */
  index_id?: string;
  /** 索引代次（uint64，JSON 里可能是字符串）。 */
  generation?: number | string;
  view_revision?: number | string;
  view_schema_hash?: string;
  /** 已建进活动索引的列。 */
  columns?: ResultColumn[];
  /** 活动索引覆盖的最早、最晚业务时间（两端都含，可能为空）。 */
  indexed_from?: string;
  indexed_to?: string;
  applied_cursor?: DatasetChangeCursor;
}

export function getViewState(req: GetViewStateReq, options: CallOptions = {}) {
  return callStorage<GetViewStateReq, GetViewStateRsp>("GetViewState", req, options);
}

/** storage-view 还没有这个数据集的查询索引（ret_info.code 为 VIEW_NOT_FOUND）。 */
export function isViewNotFoundError(error: unknown): boolean {
  return error instanceof RequestError && error.hasCode(10, "VIEW_NOT_FOUND");
}
