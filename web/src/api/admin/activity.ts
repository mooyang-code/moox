import { callControl } from "@/api/admin/http";

export interface ActivityLogItem {
  id?: number | string;
  space_id?: string;
  source?: string;
  category?: string;
  level?: string;
  title?: string;
  detail?: string;
  ref_id?: string;
  created_at?: string;
}

export interface ListActivityLogsRequest {
  space_id?: string;
  // 为 true 时只查不属于任何空间的全局事件（如登录记录），与 space_id 二选一。
  global?: boolean;
  source?: string;
  category?: string;
  ref_id?: string;
  since?: string;
  until?: string;
  page?: { page: number; size: number };
}

export interface ListActivityLogsResponse {
  items?: ActivityLogItem[];
  page?: { page?: number; size?: number; total?: number; has_more?: boolean };
}

export function listActivityLogs(request: ListActivityLogsRequest) {
  return callControl<ListActivityLogsRequest, ListActivityLogsResponse>("activity", "ListActivityLogs", request);
}
