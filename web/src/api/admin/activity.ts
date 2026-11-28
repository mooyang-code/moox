import { call } from "@/api/http";
import type { Page, PageResult } from "@/api/page";

export interface ActivityLogItem {
  id?: number | string;
  space_id?: string;
  source?: string;
  category?: string;
  level?: string;
  title?: string;
  detail?: string;
  ref_id?: string;
  /** Unix 毫秒的数字字符串 */
  created_at?: string;
}

export interface ListActivityLogsRequest {
  space_id?: string;
  // 为 true 时只查不属于任何空间的全局事件（如登录记录），与 space_id 二选一。
  global?: boolean;
  source?: string;
  category?: string;
  ref_id?: string;
  /** 起止是 Unix 毫秒（[since, until)），不传表示不限 */
  since?: string;
  until?: string;
  page?: Page;
}

export interface ListActivityLogsResponse {
  items?: ActivityLogItem[];
  page_result?: PageResult;
}

export function listActivityLogs(request: ListActivityLogsRequest) {
  return call<ListActivityLogsRequest, ListActivityLogsResponse>("activity", "ListActivityLogs", request);
}
